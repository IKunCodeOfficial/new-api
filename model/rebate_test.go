package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 固定单价，避免依赖全局默认值被其他测试改动。
func withRebatePrices(t *testing.T) {
	t.Helper()
	prevPrice, prevWaffo, prevPancake, prevQPU := operation_setting.Price, setting.WaffoUnitPrice, setting.WaffoPancakeUnitPrice, common.QuotaPerUnit
	operation_setting.Price = 7.3
	setting.WaffoUnitPrice = 7.3
	setting.WaffoPancakeUnitPrice = 7.3
	common.QuotaPerUnit = 500000
	t.Cleanup(func() {
		operation_setting.Price, setting.WaffoUnitPrice, setting.WaffoPancakeUnitPrice, common.QuotaPerUnit = prevPrice, prevWaffo, prevPancake, prevQPU
	})
}

func TestPaidQuotaFromCharge(t *testing.T) {
	withRebatePrices(t)

	cases := []struct {
		name     string
		provider string
		amount   int64
		money    float64
		want     int
		ok       bool
	}{
		{"epay 原价", PaymentProviderEpay, 100, 730, 50_000_000, true},
		{"epay 九折", PaymentProviderEpay, 100, 657, 45_000_000, true},
		{"waffo 原价", PaymentProviderWaffo, 100, 730, 50_000_000, true},
		{"waffo_pancake 原价", PaymentProviderWaffoPancake, 100, 730, 50_000_000, true},
		{"stripe 按 money", PaymentProviderStripe, 100, 100, 50_000_000, true},
		{"creem 按 amount", PaymentProviderCreem, 1000, 12.5, 1000, true},
		// 分组倍率 >1 时实付会超过到账额度，这里只如实换算，封顶由结算侧 min 完成。
		{"stripe 分组倍率 >1", PaymentProviderStripe, 100, 730, 365_000_000, true},
		{"money 为 0", PaymentProviderEpay, 100, 0, 0, false},
		{"money 为负", PaymentProviderEpay, 100, -1, 0, false},
		{"amount 为 0", PaymentProviderStripe, 0, 10, 0, false},
		{"实付小到取整为 0", PaymentProviderEpay, 100, 0.000_001, 0, false},
		{"未知渠道", "some_new_gateway", 100, 730, 0, false},
		{"balance 不该出现在 top_ups", PaymentProviderBalance, 100, 730, 0, false},
		// 大额订单不得被 int32 边界截断（$4.3k 以上就会超过 2147483647）。
		{"epay 大额超过 int32", PaymentProviderEpay, 100_000, 100_000, 6_849_315_068, true},
		{"stripe 大额超过 int32", PaymentProviderStripe, 100_000, 100_000, 50_000_000_000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := paidQuotaFromCharge(tc.provider, tc.amount, tc.money)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPaidQuotaFromChargeZeroUnitPrice(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		zero     func()
	}{
		{"epay 单价为 0", PaymentProviderEpay, func() { operation_setting.Price = 0 }},
		{"waffo 单价为 0", PaymentProviderWaffo, func() { setting.WaffoUnitPrice = 0 }},
		{"waffo_pancake 单价为 0", PaymentProviderWaffoPancake, func() { setting.WaffoPancakeUnitPrice = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withRebatePrices(t)
			tc.zero()
			paid, ok := paidQuotaFromCharge(tc.provider, 100, 730)
			assert.False(t, ok)
			assert.Zero(t, paid)
		})
	}
}

func TestResolveTopUpProvider(t *testing.T) {
	assert.Equal(t, PaymentProviderEpay, resolveTopUpProvider(&TopUp{PaymentProvider: PaymentProviderEpay, PaymentMethod: "alipay"}))
	assert.Equal(t, PaymentProviderStripe, resolveTopUpProvider(&TopUp{PaymentMethod: PaymentMethodStripe}))
	assert.Equal(t, PaymentProviderCreem, resolveTopUpProvider(&TopUp{PaymentMethod: PaymentMethodCreem}))
	assert.Equal(t, PaymentProviderWaffo, resolveTopUpProvider(&TopUp{PaymentMethod: PaymentMethodWaffo}))
	assert.Equal(t, PaymentProviderWaffoPancake, resolveTopUpProvider(&TopUp{PaymentMethod: PaymentMethodWaffoPancake}))
	// payment_provider 为空的旧 epay 订单，payment_method 是 alipay/wxpay 一类
	assert.Equal(t, PaymentProviderEpay, resolveTopUpProvider(&TopUp{PaymentMethod: "wxpay"}))
	// 未知的 payment_provider 原样返回，由 paidQuotaFromCharge 判定不写行
	assert.Equal(t, "some_new_gateway", resolveTopUpProvider(&TopUp{PaymentProvider: "some_new_gateway"}))
}

func TestTopUpAfterCreateWritesPaidRow(t *testing.T) {
	truncateTables(t)
	withRebatePrices(t)

	order := &TopUp{
		UserId: 7, Amount: 100, Money: 657, TradeNo: "rebate-epay-1",
		PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending,
	}
	require.NoError(t, order.Insert())

	var row RebateTopupPaid
	require.NoError(t, DB.Where("trade_no = ?", "rebate-epay-1").First(&row).Error)
	assert.Equal(t, 7, row.UserId)
	assert.Equal(t, int64(45_000_000), row.PaidQuota)
	assert.NotZero(t, row.CreatedAt)

	// Money 缺失：订单照常创建，但不留实付行（结算侧回落到账额度）
	bad := &TopUp{
		UserId: 7, Amount: 100, Money: 0, TradeNo: "rebate-epay-2",
		PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending,
	}
	require.NoError(t, bad.Insert())

	// 用余额购买订阅写下的 TopUp：Amount=0、Money=套餐价、payment_provider 为空。
	// 它属于订阅资金链，不进返利口径，不得留下实付行。
	sub := &TopUp{
		UserId: 7, Amount: 0, Money: 9.99, TradeNo: "rebate-sub-1",
		PaymentMethod: PaymentMethodStripe, Status: common.TopUpStatusSuccess,
	}
	require.NoError(t, sub.Insert())

	for _, tradeNo := range []string{"rebate-epay-2", "rebate-sub-1"} {
		var n int64
		require.NoError(t, DB.Model(&RebateTopupPaid{}).Where("trade_no = ?", tradeNo).Count(&n).Error)
		assert.Zero(t, n, tradeNo)
	}
}

func TestTopUpInsertFailsWhenPaidRowCannotBeWritten(t *testing.T) {
	truncateTables(t)
	withRebatePrices(t)

	// 同一 trade_no 的实付行已存在 → 钩子插入冲突 → 整个订单插入回滚
	require.NoError(t, DB.Create(&RebateTopupPaid{TradeNo: "rebate-dup", UserId: 1, PaidQuota: 1}).Error)
	order := &TopUp{
		UserId: 7, Amount: 100, Money: 730, TradeNo: "rebate-dup",
		PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending,
	}
	require.Error(t, order.Insert())

	var n int64
	require.NoError(t, DB.Model(&TopUp{}).Where("trade_no = ?", "rebate-dup").Count(&n).Error)
	assert.Zero(t, n, "钩子失败时订单不得留下")
}

func TestIncreaseUserQuotaAsRecharge(t *testing.T) {
	truncateTables(t)
	user := &User{Username: "rebate-u1", Password: "12345678", Quota: 1000, Status: common.UserStatusEnabled, AffCode: "rbu1"}
	require.NoError(t, DB.Create(user).Error)

	require.NoError(t, IncreaseUserQuotaAsRecharge(user.Id, 5000, 4500, 99))

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 6000, got.Quota)

	var row RebateAdminRecharge
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&row).Error)
	assert.Equal(t, int64(5000), row.Quota)
	assert.Equal(t, int64(4500), row.PaidQuota)
	assert.Equal(t, 99, row.OperatorId)
	assert.NotZero(t, row.CreatedAt)
}

func TestIncreaseUserQuotaAsRechargeRollsBackWhenRecordFails(t *testing.T) {
	truncateTables(t)
	user := &User{Username: "rebate-u2", Password: "12345678", Quota: 1000, Status: common.UserStatusEnabled, AffCode: "rbu2"}
	require.NoError(t, DB.Create(user).Error)

	require.NoError(t, DB.Migrator().DropTable(&RebateAdminRecharge{}))
	t.Cleanup(func() { require.NoError(t, DB.AutoMigrate(&RebateAdminRecharge{})) })

	require.Error(t, IncreaseUserQuotaAsRecharge(user.Id, 5000, 4500, 99))

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 1000, got.Quota, "充值记录写入失败时额度必须回滚")
}

func TestIncreaseUserQuotaAsRechargeRejectsBadInput(t *testing.T) {
	truncateTables(t)
	user := &User{Username: "rebate-u3", Password: "12345678", Quota: 1000, Status: common.UserStatusEnabled, AffCode: "rbu3"}
	require.NoError(t, DB.Create(user).Error)

	assert.Error(t, IncreaseUserQuotaAsRecharge(user.Id, 0, 0, 1))
	assert.Error(t, IncreaseUserQuotaAsRecharge(user.Id, 100, 0, 1))
	assert.Error(t, IncreaseUserQuotaAsRecharge(user.Id, 100, 101, 1))
	assert.Error(t, IncreaseUserQuotaAsRecharge(999999, 100, 100, 1), "用户不存在")

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 1000, got.Quota)
}

func TestRebateExclusionUniquePerUserAndRole(t *testing.T) {
	truncateTables(t)

	require.NoError(t, DB.Create(&RebateExclusion{UserId: 5, Role: "inviter"}).Error)
	require.NoError(t, DB.Create(&RebateExclusion{UserId: 5, Role: "invitee"}).Error, "同一用户可分别作为邀请人和受邀用户被排除")
	require.NoError(t, DB.Create(&RebateExclusion{UserId: 6, Role: "inviter"}).Error, "不同用户同一角色互不冲突")
	require.Error(t, DB.Create(&RebateExclusion{UserId: 5, Role: "inviter"}).Error)
}
