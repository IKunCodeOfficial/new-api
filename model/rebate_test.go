package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 固定单价，避免依赖全局默认值被其他测试改动。
func withRebatePrices(t *testing.T) {
	t.Helper()
	prevPrice, prevWaffo, prevPancake, prevQPU := operation_setting.Price, setting.WaffoUnitPrice, setting.WaffoPancakeUnitPrice, common.QuotaPerUnit
	prevWooshpay := setting.WooshpayUnitPrice
	operation_setting.Price = 7.3
	setting.WaffoUnitPrice = 7.3
	setting.WaffoPancakeUnitPrice = 7.3
	setting.WooshpayUnitPrice = 7.3
	common.QuotaPerUnit = 500000
	t.Cleanup(func() {
		operation_setting.Price, setting.WaffoUnitPrice, setting.WaffoPancakeUnitPrice, common.QuotaPerUnit = prevPrice, prevWaffo, prevPancake, prevQPU
		setting.WooshpayUnitPrice = prevWooshpay
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
		{"stripe 按购买数量", PaymentProviderStripe, 100, 100, 50_000_000, true},
		{"creem 按 amount", PaymentProviderCreem, 1000, 12.5, 1000, true},
		// Stripe 按数量 × 固定价格收款，分组倍率只放大到账额度（Money = amount × 倍率），
		// 实付仍是购买数量，不能按 Money 计。
		{"stripe 分组倍率放大到账不放大实付", PaymentProviderStripe, 100, 730, 50_000_000, true},
		{"stripe 分组倍率 <1 实付仍按数量", PaymentProviderStripe, 100, 80, 50_000_000, true},
		// 分组倍率 >1 时实付会超过到账额度，这里只如实换算，封顶由结算侧 min 完成。
		{"epay 分组倍率 >1 实付超过到账", PaymentProviderEpay, 100, 1460, 100_000_000, true},
		{"money 为 0", PaymentProviderEpay, 100, 0, 0, false},
		{"money 为负", PaymentProviderEpay, 100, -1, 0, false},
		{"amount 为 0", PaymentProviderStripe, 0, 10, 0, false},
		{"实付小到取整为 0", PaymentProviderEpay, 100, 0.000_001, 0, false},
		{"未知渠道", "some_new_gateway", 100, 730, 0, false},
		{"balance 不该出现在 top_ups", PaymentProviderBalance, 100, 730, 0, false},
		// 大额订单不得被 int32 边界截断（$4.3k 以上就会超过 2147483647）。
		{"epay 大额超过 int32", PaymentProviderEpay, 100_000, 100_000, 6_849_315_068, true},
		{"stripe 大额超过 int32", PaymentProviderStripe, 100_000, 100_000, 50_000_000_000, true},
		// WooshPay 与 Waffo Pancake 一样由本站定价：Money 是实付金额，分组倍率只放大实付，
		// 实付快照 = Money ÷ 单价 × QPU。
		{"wooshpay 原价", PaymentProviderWooshpay, 100, 730, 50_000_000, true},
		{"wooshpay 分组倍率只放大实付", PaymentProviderWooshpay, 100, 1460, 100_000_000, true},
		{"wooshpay 预设折扣", PaymentProviderWooshpay, 100, 365, 25_000_000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := paidQuotaFromCharge(tc.provider, tc.amount, tc.money, 0)
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
			paid, ok := paidQuotaFromCharge(tc.provider, 100, 730, 0)
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
	assert.Equal(t, PaymentProviderWooshpay, resolveTopUpProvider(&TopUp{PaymentMethod: PaymentMethodWooshpay}))
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

	// 在线支付订阅成功时 upsertSubscriptionTopUpTx 写下的 TopUp：Amount=0、Money=套餐价、
	// payment_provider 为空（靠 payment_method 推断渠道）。它属于订阅资金链，不进返利口径，
	// 不得留下实付行——否则结算侧会把它当成 money×QuotaPerUnit 的充值。
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

// The settler refuses to run on a switch or rate it cannot parse, so saving
// such a value must fail instead of silently stopping every settlement.
func TestIkunRebateOptionsRejectValuesTheSettlerRefuses(t *testing.T) {
	rejected := map[string][]string{
		IkunRebateRateOptionKey:    {"", "0", "0.0", "1", "1.5", "-0.1", "abc", "NaN", "0x1p-4", "5e-2", " 0.05", "0.05\n", ".05", "0.12345"},
		IkunRebateEnabledOptionKey: {"", "True", "TRUE", "1", "yes", "on"},
	}
	for key, values := range rejected {
		for _, value := range values {
			assert.Error(t, validateOptionValue(key, value), "%s=%q", key, value)
		}
	}
	for _, value := range []string{"0.05", "0.1", "0.0001", "0.9999"} {
		assert.NoError(t, validateOptionValue(IkunRebateRateOptionKey, value), value)
	}
	for _, value := range []string{"true", "false"} {
		assert.NoError(t, validateOptionValue(IkunRebateEnabledOptionKey, value), value)
	}
}

// assertCheckinRespectsWalletCap runs the transactional check-in credit (the
// MySQL/PostgreSQL path) for userID: a reward that would push the wallet past
// MaxWalletQuota fails without crediting or recording the check-in, a normal
// reward is credited once, and a zero reward succeeds although MySQL reports
// no changed row for it.
func assertCheckinRespectsWalletCap(t *testing.T, userID int) {
	t.Helper()
	t.Cleanup(func() { DB.Where("user_id = ?", userID).Delete(&Checkin{}) })
	cases := []struct {
		name         string
		date         string
		startQuota   int
		award        int
		wantErr      bool
		wantQuota    int
		wantRecorded bool
	}{
		{name: "beyond the wallet cap", date: "2001-01-01", startQuota: common.MaxWalletQuota - 10, award: 100, wantErr: true, wantQuota: common.MaxWalletQuota - 10},
		{name: "normal reward", date: "2001-01-02", startQuota: 1000, award: 100, wantQuota: 1100, wantRecorded: true},
		{name: "zero reward", date: "2001-01-03", startQuota: 1000, award: 0, wantQuota: 1000, wantRecorded: true},
	}
	for _, tc := range cases {
		require.NoError(t, DB.Model(&User{}).Where("id = ?", userID).Update("quota", tc.startQuota).Error, tc.name)
		checkin := &Checkin{UserId: userID, CheckinDate: tc.date, QuotaAwarded: tc.award, CreatedAt: 1}

		_, err := userCheckinWithTransaction(checkin, userID, tc.award)

		if tc.wantErr {
			assert.Error(t, err, tc.name)
		} else {
			assert.NoError(t, err, tc.name)
		}
		var got User
		require.NoError(t, DB.First(&got, userID).Error, tc.name)
		assert.Equal(t, tc.wantQuota, got.Quota, tc.name)
		var recorded int64
		require.NoError(t, DB.Model(&Checkin{}).Where("user_id = ? AND checkin_date = ?", userID, tc.date).Count(&recorded).Error, tc.name)
		assert.Equal(t, tc.wantRecorded, recorded == 1, tc.name)
	}
}

func TestCheckinRespectsTheWalletCap(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&Checkin{}))
	user := &User{Username: "checkin-cap", Password: "12345678", Status: common.UserStatusEnabled, AffCode: "ckcp"}
	require.NoError(t, DB.Create(user).Error)
	assertCheckinRespectsWalletCap(t, user.Id)
}

// The options row is what the settler and the other nodes read, so a save
// that fails to reach the database must report the failure and leave the
// in-memory value alone instead of claiming success.
func TestUpdateOptionReportsAFailedWrite(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&Option{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM options") })
	common.OptionMapRWMutex.Lock()
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
	})
	require.NoError(t, UpdateOption(IkunRebateRateOptionKey, "0.05"))

	const callback = "options_write_fails"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "options" {
			_ = tx.AddError(errors.New("options write failed"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, DB.Callback().Update().Remove(callback)) })

	require.Error(t, UpdateOption(IkunRebateRateOptionKey, "0.1"))
	var row Option
	require.NoError(t, DB.Where(map[string]any{"key": IkunRebateRateOptionKey}).First(&row).Error)
	assert.Equal(t, "0.05", row.Value)
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, "0.05", common.OptionMap[IkunRebateRateOptionKey])
}

// Completing a subscription order may only touch its amount-0 mirror row. A
// wallet top-up that shares the trade_no must stay pending: marking it success
// without crediting quota would let the settler count an unpaid recharge.
func TestSubscriptionCompletionLeavesWalletTopUpAlone(t *testing.T) {
	truncateTables(t)
	withRebatePrices(t)
	require.NoError(t, DB.Create(&TopUp{
		UserId: 7, Amount: 100, Money: 100, TradeNo: "shared-trade-no", PaymentMethod: "alipay",
		PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending,
	}).Error)
	order := &SubscriptionOrder{UserId: 7, Money: 9.9, TradeNo: "shared-trade-no", PaymentMethod: "alipay", CreateTime: 1}

	assert.ErrorIs(t, upsertSubscriptionTopUpTx(DB, order), ErrTopUpStatusInvalid)
	var wallet TopUp
	require.NoError(t, DB.Where("trade_no = ?", "shared-trade-no").First(&wallet).Error)
	assert.Equal(t, common.TopUpStatusPending, wallet.Status)
	assert.Equal(t, 100.0, wallet.Money)

	mirror := &SubscriptionOrder{UserId: 7, Money: 9.9, TradeNo: "sub-mirror", PaymentMethod: "alipay", CreateTime: 1}
	require.NoError(t, upsertSubscriptionTopUpTx(DB, mirror))
	require.NoError(t, upsertSubscriptionTopUpTx(DB, mirror), "re-completing the mirror row stays allowed")
	var row TopUp
	require.NoError(t, DB.Where("trade_no = ?", "sub-mirror").First(&row).Error)
	assert.Equal(t, common.TopUpStatusSuccess, row.Status)
	assert.Zero(t, row.Amount)
}

// Discounts only known at completion (Stripe promotion codes) and test-mode
// payments must lower the paid snapshot, never raise it, while the wallet is
// still credited exactly as before.
func TestGatewayChargeAdjustsPaidSnapshot(t *testing.T) {
	truncateTables(t)
	withRebatePrices(t)
	prevCreemTest := setting.CreemTestMode
	t.Cleanup(func() { setting.CreemTestMode = prevCreemTest })

	cases := []struct {
		name     string
		provider string
		charge   GatewayCharge
		paid     int64
	}{
		{"stripe without discount", PaymentProviderStripe, GatewayCharge{Listed: 1000, Charged: 1000}, 5_000_000},
		{"stripe promotion code 20% off", PaymentProviderStripe, GatewayCharge{Listed: 1000, Charged: 800}, 4_000_000},
		{"stripe 100% off", PaymentProviderStripe, GatewayCharge{Listed: 1000, Charged: 0}, 0},
		{"stripe test mode", PaymentProviderStripe, GatewayCharge{Listed: 1000, Charged: 1000, Test: true}, 0},
		{"stripe charge above list is ignored", PaymentProviderStripe, GatewayCharge{Listed: 1000, Charged: 1200}, 5_000_000},
		{"pancake test webhook", PaymentProviderWaffoPancake, GatewayCharge{Test: true}, 0},
		{"pancake live webhook", PaymentProviderWaffoPancake, GatewayCharge{}, 5_000_000},
		{"creem discount code 30% off", PaymentProviderCreem, GatewayCharge{Listed: 1000, Charged: 700}, 3_500_000},
		{"creem test mode", PaymentProviderCreem, GatewayCharge{Listed: 1000, Charged: 1000, Test: true}, 0},
		{"wooshpay received the server price", PaymentProviderWooshpay, GatewayCharge{Listed: 7300, Charged: 7300}, 5_000_000},
		{"wooshpay test mode", PaymentProviderWooshpay, GatewayCharge{Listed: 7300, Charged: 7300, Test: true}, 0},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tradeNo := fmt.Sprintf("charge-%d", i)
			user := &User{Id: 200 + i, Username: tradeNo, Password: "12345678", Status: common.UserStatusEnabled, AffCode: fmt.Sprintf("gc%02d", i)}
			require.NoError(t, DB.Create(user).Error)
			order := &TopUp{UserId: user.Id, Amount: 10, Money: 73, TradeNo: tradeNo, PaymentMethod: tc.provider,
				PaymentProvider: tc.provider, Status: common.TopUpStatusPending}
			switch tc.provider {
			case PaymentProviderStripe:
				order.Money = 10
			case PaymentProviderCreem:
				order.Amount, order.Money = 5_000_000, 10 // Creem amounts are quota
			}
			require.NoError(t, order.Insert())

			switch tc.provider {
			case PaymentProviderStripe:
				require.NoError(t, Recharge(tradeNo, "", "127.0.0.1", tc.charge))
			case PaymentProviderCreem:
				require.NoError(t, RechargeCreem(tradeNo, "", "", "127.0.0.1", tc.charge))
			case PaymentProviderWooshpay:
				require.NoError(t, RechargeWooshpay(tradeNo, "127.0.0.1", "73.00 CNY", tc.charge))
			default:
				require.NoError(t, RechargeWaffoPancake(tradeNo, tc.charge))
			}

			var row RebateTopupPaid
			require.NoError(t, DB.Where("trade_no = ?", tradeNo).First(&row).Error)
			assert.Equal(t, tc.paid, row.PaidQuota)
			var got User
			require.NoError(t, DB.First(&got, user.Id).Error)
			assert.Equal(t, 5_000_000, got.Quota, "the wallet credit is unchanged")
		})
	}

	setting.CreemTestMode = true
	creem := &TopUp{UserId: 7, Amount: 1000, Money: 12.5, TradeNo: "creem-test", PaymentMethod: PaymentMethodCreem,
		PaymentProvider: PaymentProviderCreem, Status: common.TopUpStatusPending}
	require.NoError(t, creem.Insert())
	var row RebateTopupPaid
	require.NoError(t, DB.Where("trade_no = ?", "creem-test").First(&row).Error)
	assert.Zero(t, row.PaidQuota, "orders created in Creem test mode are not a recharge")

	prevWooshpayKey := setting.WooshpaySecretKey
	t.Cleanup(func() { setting.WooshpaySecretKey = prevWooshpayKey })
	for key, want := range map[string]int64{"sk_test_abc": 0, "sk_live_abc": 5_000_000} {
		tradeNo := "wooshpay-" + key
		setting.WooshpaySecretKey = key
		order := &TopUp{UserId: 7, Amount: 10, Money: 73, TradeNo: tradeNo, PaymentMethod: PaymentMethodWooshpay,
			PaymentProvider: PaymentProviderWooshpay, Status: common.TopUpStatusPending}
		require.NoError(t, order.Insert())
		var wooshpayRow RebateTopupPaid
		require.NoError(t, DB.Where("trade_no = ?", tradeNo).First(&wooshpayRow).Error)
		assert.Equal(t, want, wooshpayRow.PaidQuota, "orders created with a WooshPay test key are not a recharge")
	}

	// An order carries the unit price and mode it was priced with; the
	// snapshot uses them even if the settings changed in the meantime.
	setting.WooshpaySecretKey = "sk_live_abc"
	for tradeNo, tc := range map[string]struct {
		pricing TopUpPricing
		want    int64
	}{
		"wooshpay-priced-live": {TopUpPricing{UnitPrice: 14.6}, 5_000_000},
		"wooshpay-priced-test": {TopUpPricing{UnitPrice: 14.6, TestMode: true}, 0},
	} {
		order := &TopUp{UserId: 7, Amount: 10, Money: 146, TradeNo: tradeNo, PaymentMethod: PaymentMethodWooshpay,
			PaymentProvider: PaymentProviderWooshpay, Status: common.TopUpStatusPending, Pricing: &tc.pricing}
		require.NoError(t, order.Insert())
		var pricedRow RebateTopupPaid
		require.NoError(t, DB.Where("trade_no = ?", tradeNo).First(&pricedRow).Error)
		assert.Equal(t, tc.want, pricedRow.PaidQuota, tradeNo)
	}
}

// An administrator completing a stuck order must credit exactly what the
// gateway callback would have credited, which is also the credited quota the
// settler counts: Creem amounts are already quota, Stripe uses Money. Without
// the gateway's completion event the collected amount of Stripe, Creem and
// Pancake orders is unknown, so it counts toward rebates only when the
// administrator states it; epay and WooshPay, priced by this server, keep
// their creation-time snapshot. Only pending orders are completed here: a
// WooshPay order closed as expired or failed needs a verified PaymentIntent
// (controller.AdminCompleteTopUp). A stated
// amount is in the currency the gateway charged and scales the snapshot by
// collected ÷ list price, where Stripe lists quantity × Checkout unit price.
func TestManualCompleteTopUpCreditsLikeGatewayCallback(t *testing.T) {
	truncateTables(t)
	withRebatePrices(t)
	previousStripePrice := setting.StripeUnitPrice
	setting.StripeUnitPrice = 8
	t.Cleanup(func() { setting.StripeUnitPrice = previousStripePrice })
	paid := func(money float64) *float64 { return &money }

	cases := []struct {
		tradeNo          string
		provider, method string
		amount           int64
		money            float64
		paid             *float64
		credited         int
		snapshot         int64
		noSnapshot       bool   // order created before snapshots existed
		status           string // pending when empty
	}{
		{tradeNo: "manual-creem", provider: PaymentProviderCreem, method: PaymentMethodCreem, amount: 2_500_000, money: 10, credited: 2_500_000, snapshot: 0},
		{tradeNo: "manual-creem-half", provider: PaymentProviderCreem, method: PaymentMethodCreem, amount: 2_500_000, money: 10, paid: paid(5), credited: 2_500_000, snapshot: 1_250_000},
		// 10 units at group ratio 2 credit Money 20 × QPU; Checkout lists 10 × 8.
		{tradeNo: "manual-stripe", provider: PaymentProviderStripe, method: PaymentMethodStripe, amount: 10, money: 20, credited: 10_000_000, snapshot: 0},
		{tradeNo: "manual-stripe-promo", provider: PaymentProviderStripe, method: PaymentMethodStripe, amount: 10, money: 20, paid: paid(40), credited: 10_000_000, snapshot: 2_500_000},
		{tradeNo: "manual-stripe-full", provider: PaymentProviderStripe, method: PaymentMethodStripe, amount: 10, money: 20, paid: paid(80), credited: 10_000_000, snapshot: 5_000_000},
		{tradeNo: "manual-legacy-stripe", method: PaymentMethodStripe, amount: 10, money: 20, credited: 10_000_000, snapshot: 0},
		// Like Waffo Pancake: 10 units credit Amount × QPU, and the list price is
		// Money (10 × 7.3 × group ratio 2), the amount WooshPay charged. Without
		// a stated amount the creation-time snapshot Money ÷ 7.3 × QPU stays.
		{tradeNo: "manual-wooshpay", provider: PaymentProviderWooshpay, method: PaymentMethodWooshpay, amount: 10, money: 146, credited: 5_000_000, snapshot: 10_000_000},
		{tradeNo: "manual-wooshpay-half", provider: PaymentProviderWooshpay, method: PaymentMethodWooshpay, amount: 10, money: 146, paid: paid(73), credited: 5_000_000, snapshot: 5_000_000},
		{tradeNo: "manual-wooshpay-full", provider: PaymentProviderWooshpay, method: PaymentMethodWooshpay, amount: 10, money: 146, paid: paid(146), credited: 5_000_000, snapshot: 10_000_000},
		{tradeNo: "manual-epay", provider: PaymentProviderEpay, method: "alipay", amount: 10, money: 73, credited: 5_000_000, snapshot: 5_000_000},
		{tradeNo: "manual-epay-half", provider: PaymentProviderEpay, method: "alipay", amount: 10, money: 73, paid: paid(36.5), credited: 5_000_000, snapshot: 2_500_000},
		{tradeNo: "manual-epay-above-price", provider: PaymentProviderEpay, method: "alipay", amount: 10, money: 73, paid: paid(100), credited: 5_000_000, snapshot: 5_000_000},
		{tradeNo: "manual-pancake", provider: PaymentProviderWaffoPancake, method: PaymentMethodWaffoPancake, amount: 10, money: 73, credited: 5_000_000, snapshot: 0},
		{tradeNo: "manual-pancake-full", provider: PaymentProviderWaffoPancake, method: PaymentMethodWaffoPancake, amount: 10, money: 73, paid: paid(73), credited: 5_000_000, snapshot: 5_000_000},
		{tradeNo: "manual-legacy-epay-half", provider: PaymentProviderEpay, method: "alipay", amount: 10, money: 73, paid: paid(36.5), credited: 5_000_000, snapshot: 2_500_000, noSnapshot: true},
	}
	for i, tc := range cases {
		t.Run(tc.tradeNo, func(t *testing.T) {
			user := &User{Id: 100 + i, Username: tc.tradeNo, Password: "12345678", Status: common.UserStatusEnabled, AffCode: fmt.Sprintf("mc%02d", i)}
			require.NoError(t, DB.Create(user).Error)
			status := tc.status
			if status == "" {
				status = common.TopUpStatusPending
			}
			order := &TopUp{
				UserId: user.Id, Amount: tc.amount, Money: tc.money, TradeNo: tc.tradeNo,
				PaymentMethod: tc.method, PaymentProvider: tc.provider, Status: status,
			}
			require.NoError(t, order.Insert())
			if tc.noSnapshot {
				require.NoError(t, DB.Where("trade_no = ?", tc.tradeNo).Delete(&RebateTopupPaid{}).Error)
			}

			require.NoError(t, ManualCompleteTopUp(tc.tradeNo, "127.0.0.1", tc.paid))

			var got User
			require.NoError(t, DB.First(&got, user.Id).Error)
			assert.Equal(t, tc.credited, got.Quota)
			var completed TopUp
			require.NoError(t, DB.Where("trade_no = ?", tc.tradeNo).First(&completed).Error)
			assert.Equal(t, common.TopUpStatusSuccess, completed.Status)
			var row RebateTopupPaid
			require.NoError(t, DB.Where("trade_no = ?", tc.tradeNo).First(&row).Error)
			assert.Equal(t, tc.snapshot, row.PaidQuota)
			var topupLog Log
			require.NoError(t, LOG_DB.Where("user_id = ? AND type = ?", user.Id, LogTypeTopup).First(&topupLog).Error)
			if tc.paid != nil {
				assert.Contains(t, topupLog.Content, "amount collected", "a stated collection is audited with the order")
			} else {
				assert.NotContains(t, topupLog.Content, "amount collected")
			}
		})
	}

	// Closed orders of every gateway, WooshPay included, are not completed
	// here.
	closed := &User{Id: 201, Username: "manual-closed", Password: "12345678", Status: common.UserStatusEnabled, AffCode: "mccl"}
	require.NoError(t, DB.Create(closed).Error)
	for _, order := range []*TopUp{
		{UserId: closed.Id, Amount: 10, Money: 73, TradeNo: "manual-epay-expired", PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusExpired},
		{UserId: closed.Id, Amount: 10, Money: 73, TradeNo: "manual-pancake-failed", PaymentMethod: PaymentMethodWaffoPancake, PaymentProvider: PaymentProviderWaffoPancake, Status: common.TopUpStatusFailed},
		{UserId: closed.Id, Amount: 10, Money: 73, TradeNo: "manual-wooshpay-expired", PaymentMethod: PaymentMethodWooshpay, PaymentProvider: PaymentProviderWooshpay, Status: common.TopUpStatusExpired},
		{UserId: closed.Id, Amount: 10, Money: 73, TradeNo: "manual-wooshpay-failed", PaymentMethod: PaymentMethodWooshpay, PaymentProvider: PaymentProviderWooshpay, Status: common.TopUpStatusFailed},
	} {
		require.NoError(t, order.Insert())
		require.Error(t, ManualCompleteTopUp(order.TradeNo, "127.0.0.1", nil), order.TradeNo)
	}
	var closedUser User
	require.NoError(t, DB.First(&closedUser, closed.Id).Error)
	assert.Zero(t, closedUser.Quota)

	// Completing an order again succeeds without crediting its user twice.
	require.NoError(t, ManualCompleteTopUp("manual-wooshpay", "127.0.0.1", nil))
	var completedOrder TopUp
	require.NoError(t, DB.Where("trade_no = ?", "manual-wooshpay").First(&completedOrder).Error)
	var completedUser User
	require.NoError(t, DB.First(&completedUser, completedOrder.UserId).Error)
	assert.Equal(t, 5_000_000, completedUser.Quota, "a completed order is not credited twice")

	// Without a list price the stated amount cannot be scaled, so the order
	// is left pending instead of being completed with a guessed snapshot.
	setting.StripeUnitPrice = 0
	user := &User{Id: 200, Username: "manual-no-price", Password: "12345678", Status: common.UserStatusEnabled, AffCode: "mcnp"}
	require.NoError(t, DB.Create(user).Error)
	order := &TopUp{UserId: user.Id, Amount: 10, Money: 20, TradeNo: "manual-stripe-no-price", PaymentMethod: PaymentMethodStripe,
		PaymentProvider: PaymentProviderStripe, Status: common.TopUpStatusPending}
	require.NoError(t, order.Insert())
	require.Error(t, ManualCompleteTopUp(order.TradeNo, "127.0.0.1", paid(40)))
	var pending TopUp
	require.NoError(t, DB.Where("trade_no = ?", order.TradeNo).First(&pending).Error)
	assert.Equal(t, common.TopUpStatusPending, pending.Status)
	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Zero(t, got.Quota)
}

// The cached wallet balance is what pre-consume checks, so a rebate withdrawn
// to the wallet must be spendable immediately, not after the cache expires.
func TestTransferAffQuotaUpdatesCachedWalletQuota(t *testing.T) {
	truncateTables(t)
	withRebatePrices(t)
	useUserCacheMiniRedis(t)
	user := &User{Username: "rebate-aff", Password: "12345678", Status: common.UserStatusEnabled, Group: "default",
		AffCode: "rbaf", AffQuota: 600000, AffHistoryQuota: 600000, AuthVersion: 1}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, populateUserCache(*user))

	require.NoError(t, user.TransferAffQuotaToQuota(500000))

	cached, err := getUserQuotaCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 500000, cached)
	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 500000, got.Quota)
	assert.Equal(t, 100000, got.AffQuota)

	// Paid rebates sit in aff_quota, so each withdrawal must leave a record that
	// reconciles payouts against the remaining referral balance.
	var logs []Log
	require.NoError(t, LOG_DB.Where("user_id = ? AND type = ?", user.Id, LogTypeSystem).Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Contains(t, logs[0].Content, "of referral rewards to the balance")
	assert.Contains(t, logs[0].Other, "Transferred {{quota}} of referral rewards to the balance, {{remaining}} left")
}

func TestRebateExclusionUniquePerUserAndRole(t *testing.T) {
	truncateTables(t)

	require.NoError(t, DB.Create(&RebateExclusion{UserId: 5, Role: "inviter"}).Error)
	require.NoError(t, DB.Create(&RebateExclusion{UserId: 5, Role: "invitee"}).Error, "同一用户可分别作为邀请人和受邀用户被排除")
	require.NoError(t, DB.Create(&RebateExclusion{UserId: 6, Role: "inviter"}).Error, "不同用户同一角色互不冲突")
	require.Error(t, DB.Create(&RebateExclusion{UserId: 5, Role: "inviter"}).Error)
}
