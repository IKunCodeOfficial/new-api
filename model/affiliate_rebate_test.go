package model

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setAffiliateRebateTestSettings(t *testing.T, rate float64) {
	t.Helper()
	paymentSetting := operation_setting.GetPaymentSetting()
	originalRate := paymentSetting.AffiliateRebateRate
	originalConfirmed := paymentSetting.ComplianceConfirmed
	originalTermsVersion := paymentSetting.ComplianceTermsVersion
	paymentSetting.AffiliateRebateRate = rate
	paymentSetting.ComplianceConfirmed = true
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Cleanup(func() {
		paymentSetting.AffiliateRebateRate = originalRate
		paymentSetting.ComplianceConfirmed = originalConfirmed
		paymentSetting.ComplianceTermsVersion = originalTermsVersion
	})
}

func insertAffiliateRebateUser(t *testing.T, id int, username string, inviterId int) {
	t.Helper()
	user := &User{
		Id:        id,
		Username:  username,
		AffCode:   fmt.Sprintf("aff-%d", id),
		Status:    common.UserStatusEnabled,
		Quota:     0,
		InviterId: inviterId,
	}
	require.NoError(t, DB.Create(user).Error)
}

func getAffiliateRebateUser(t *testing.T, id int) User {
	t.Helper()
	var user User
	require.NoError(t, DB.Where("id = ?", id).First(&user).Error)
	return user
}

// expectedRebateQuota 返回按入账额度 × 比例计算的期望返利额度
func expectedRebateQuota(creditedQuota int, rate float64) int {
	return int(decimal.NewFromInt(int64(creditedQuota)).Mul(decimal.NewFromFloat(rate)).IntPart())
}

func TestRecharge_GrantsLockedAffiliateRebateAndReleasesAfterOneMonth(t *testing.T) {
	truncateTables(t)
	setAffiliateRebateTestSettings(t, 0.05)

	insertAffiliateRebateUser(t, 1, "inviter", 0)
	insertAffiliateRebateUser(t, 2, "invitee", 1)

	// Amount 与 Money 刻意不同，钉住 Stripe 订单以 Money 为入账/返利基数的语义
	topUp := &TopUp{
		UserId:          2,
		Amount:          100,
		Money:           80,
		TradeNo:         "stripe-affiliate-rebate",
		PaymentMethod:   PaymentMethodStripe,
		PaymentProvider: PaymentProviderStripe,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())

	completed, err := Recharge(topUp.TradeNo, "cus_affiliate", "127.0.0.1")
	require.NoError(t, err)
	require.True(t, completed)

	// Stripe 订单入账额度 = Money × QuotaPerUnit，返利 = 入账额度 × 比例
	creditedQuota := int(decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart())
	rewardQuota := expectedRebateQuota(creditedQuota, 0.05)

	invitee := getAffiliateRebateUser(t, 2)
	assert.Equal(t, creditedQuota, invitee.Quota)

	inviter := getAffiliateRebateUser(t, 1)
	assert.Equal(t, 0, inviter.AffQuota)
	assert.Equal(t, rewardQuota, inviter.AffHistoryQuota)

	frozen, err := GetAffiliateFrozenQuota(1)
	require.NoError(t, err)
	assert.Equal(t, rewardQuota, frozen)

	topups, total, err := GetUserTopUps(1, &common.PageInfo{Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Empty(t, topups)

	rebate := GetTopUpByTradeNo(affiliateRebateTradeNo(topUp.TradeNo))
	require.NotNil(t, rebate)
	assert.Equal(t, PaymentProviderAffiliateRebate, rebate.PaymentProvider)
	assert.Equal(t, common.TopUpStatusPending, rebate.Status)
	assert.Greater(t, rebate.CompleteTime, common.GetTimestamp())

	require.NoError(t, DB.Model(&TopUp{}).Where("id = ?", rebate.Id).Update("complete_time", common.GetTimestamp()-1).Error)
	released, err := ReleaseMatureAffiliateRebates(1)
	require.NoError(t, err)
	assert.Equal(t, rewardQuota, released)

	inviter = getAffiliateRebateUser(t, 1)
	assert.Equal(t, rewardQuota, inviter.AffQuota)
	assert.Equal(t, rewardQuota, inviter.AffHistoryQuota)

	frozen, err = GetAffiliateFrozenQuota(1)
	require.NoError(t, err)
	assert.Equal(t, 0, frozen)

	rebate = GetTopUpByTradeNo(affiliateRebateTradeNo(topUp.TradeNo))
	require.NotNil(t, rebate)
	assert.Equal(t, common.TopUpStatusSuccess, rebate.Status)

	require.NoError(t, inviter.TransferAffQuotaToQuota(rewardQuota))
	inviter = getAffiliateRebateUser(t, 1)
	assert.Equal(t, 0, inviter.AffQuota)
	assert.Equal(t, rewardQuota, inviter.Quota)
}

func TestGrantAffiliateRechargeRebate_IsIdempotentBySourceTradeNo(t *testing.T) {
	truncateTables(t)
	setAffiliateRebateTestSettings(t, 0.05)

	insertAffiliateRebateUser(t, 10, "inviter_idempotent", 0)
	insertAffiliateRebateUser(t, 11, "invitee_idempotent", 10)

	source := &TopUp{
		UserId:          11,
		Amount:          20,
		Money:           10,
		TradeNo:         "source-idempotent",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusSuccess,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, source.Insert())

	// epay 订单入账额度 = Amount × QuotaPerUnit
	creditedQuota := int(decimal.NewFromInt(source.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart())
	rewardQuota := expectedRebateQuota(creditedQuota, 0.05)

	for i := 0; i < 2; i++ {
		require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
			_, err := GrantAffiliateRechargeRebateTx(tx, source, creditedQuota)
			return err
		}))
	}

	var count int64
	require.NoError(t, DB.Model(&TopUp{}).Where("payment_provider = ?", PaymentProviderAffiliateRebate).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	inviter := getAffiliateRebateUser(t, 10)
	assert.Equal(t, rewardQuota, inviter.AffHistoryQuota)
}

func TestRecharge_IsIdempotentAfterSuccess(t *testing.T) {
	truncateTables(t)
	setAffiliateRebateTestSettings(t, 0.05)

	insertAffiliateRebateUser(t, 20, "inviter_stripe_idempotent", 0)
	insertAffiliateRebateUser(t, 21, "invitee_stripe_idempotent", 20)

	topUp := &TopUp{
		UserId:          21,
		Amount:          30,
		Money:           24,
		TradeNo:         "stripe-idempotent",
		PaymentMethod:   PaymentMethodStripe,
		PaymentProvider: PaymentProviderStripe,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())

	completed, err := Recharge(topUp.TradeNo, "cus_once", "127.0.0.1")
	require.NoError(t, err)
	require.True(t, completed)

	completed, err = Recharge(topUp.TradeNo, "cus_once", "127.0.0.1")
	require.NoError(t, err)
	require.False(t, completed)

	creditedQuota := int(decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart())
	rewardQuota := expectedRebateQuota(creditedQuota, 0.05)

	invitee := getAffiliateRebateUser(t, 21)
	assert.Equal(t, creditedQuota, invitee.Quota)

	inviter := getAffiliateRebateUser(t, 20)
	assert.Equal(t, rewardQuota, inviter.AffHistoryQuota)

	var rebateCount int64
	require.NoError(t, DB.Model(&TopUp{}).Where("payment_provider = ?", PaymentProviderAffiliateRebate).Count(&rebateCount).Error)
	assert.Equal(t, int64(1), rebateCount)
}

func TestCompleteEpayRecharge_GrantsLockedAffiliateRebateIdempotently(t *testing.T) {
	truncateTables(t)
	setAffiliateRebateTestSettings(t, 0.05)

	insertAffiliateRebateUser(t, 30, "inviter_epay", 0)
	insertAffiliateRebateUser(t, 31, "invitee_epay", 30)

	topUp := &TopUp{
		UserId:          31,
		Amount:          40,
		Money:           20,
		TradeNo:         "epay-affiliate-rebate",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())

	completedTopUp, quotaToAdd, rebateResult, err := CompleteEpayRecharge(topUp.TradeNo, "wxpay")
	require.NoError(t, err)
	require.NotNil(t, completedTopUp)
	require.NotNil(t, rebateResult)
	require.True(t, rebateResult.Inserted)
	assert.Equal(t, "wxpay", completedTopUp.PaymentMethod)

	// 返利基数为该订单实际入账额度（Amount × QuotaPerUnit），与 Money（支付货币金额）无关
	creditedQuota := int(decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart())
	rewardQuota := expectedRebateQuota(creditedQuota, 0.05)
	assert.Equal(t, creditedQuota, quotaToAdd)
	assert.Equal(t, rewardQuota, rebateResult.RewardQuota)

	completedTopUp, quotaToAdd, rebateResult, err = CompleteEpayRecharge(topUp.TradeNo, "wxpay")
	require.NoError(t, err)
	require.NotNil(t, completedTopUp)
	assert.Zero(t, quotaToAdd)
	// 重复回调会幂等重入返利发放，但唯一索引保证不会重复插入（Inserted=false，也不会重复记日志）
	if rebateResult != nil {
		assert.False(t, rebateResult.Inserted)
		assert.False(t, rebateResult.ShouldLog())
	}

	invitee := getAffiliateRebateUser(t, 31)
	assert.Equal(t, creditedQuota, invitee.Quota)

	inviter := getAffiliateRebateUser(t, 30)
	assert.Equal(t, 0, inviter.AffQuota)
	assert.Equal(t, rewardQuota, inviter.AffHistoryQuota)

	var rebateCount int64
	require.NoError(t, DB.Model(&TopUp{}).Where("payment_provider = ?", PaymentProviderAffiliateRebate).Count(&rebateCount).Error)
	assert.Equal(t, int64(1), rebateCount)
}

func TestManualCompleteTopUp_GrantsAffiliateRebateForOnlineProvider(t *testing.T) {
	truncateTables(t)
	setAffiliateRebateTestSettings(t, 0.05)

	insertAffiliateRebateUser(t, 40, "inviter_manual", 0)
	insertAffiliateRebateUser(t, 41, "invitee_manual", 40)

	// Amount 与 Money 刻意不同，钉住补单路径以入账额度（epay 按 Amount）为返利基数的语义
	topUp := &TopUp{
		UserId:          41,
		Amount:          50,
		Money:           25,
		TradeNo:         "manual-epay-affiliate-rebate",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())

	require.NoError(t, ManualCompleteTopUp(topUp.TradeNo, "127.0.0.1"))
	require.NoError(t, ManualCompleteTopUp(topUp.TradeNo, "127.0.0.1"))

	creditedQuota := int(decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart())
	rewardQuota := expectedRebateQuota(creditedQuota, 0.05)

	invitee := getAffiliateRebateUser(t, 41)
	assert.Equal(t, creditedQuota, invitee.Quota)

	inviter := getAffiliateRebateUser(t, 40)
	assert.Equal(t, rewardQuota, inviter.AffHistoryQuota)

	var rebateCount int64
	require.NoError(t, DB.Model(&TopUp{}).Where("payment_provider = ?", PaymentProviderAffiliateRebate).Count(&rebateCount).Error)
	assert.Equal(t, int64(1), rebateCount)
}

func TestManualCompleteTopUp_RejectsAffiliateRebateLockRows(t *testing.T) {
	truncateTables(t)
	setAffiliateRebateTestSettings(t, 0.05)

	insertAffiliateRebateUser(t, 50, "inviter_locked_rebate", 0)

	rebate := &TopUp{
		UserId:          50,
		Amount:          int64(common.QuotaPerUnit),
		Money:           1,
		TradeNo:         affiliateRebateTradeNo("manual-complete-source"),
		PaymentMethod:   PaymentMethodAffiliateRebate,
		PaymentProvider: PaymentProviderAffiliateRebate,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
		CompleteTime:    affiliateRebateUnlockTime(time.Now()),
	}
	require.NoError(t, rebate.Insert())

	err := ManualCompleteTopUp(rebate.TradeNo, "127.0.0.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "邀请返利锁定流水不能手动补单")

	stored := GetTopUpByTradeNo(rebate.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, common.TopUpStatusPending, stored.Status)

	inviter := getAffiliateRebateUser(t, 50)
	assert.Zero(t, inviter.Quota)
	assert.Zero(t, inviter.AffQuota)
}

func TestGrantAffiliateRechargeRebateSafely_FailureDoesNotBlockRecharge(t *testing.T) {
	truncateTables(t)
	setAffiliateRebateTestSettings(t, 0.05)

	// invitee 的 inviter_id 指向不存在的用户不会报错（返回 nil），
	// 这里通过传入 nil topUp 之外的手段验证 SAVEPOINT 隔离：
	// 让 invitee 行缺失，GrantAffiliateRechargeRebateTx 返回 ErrRecordNotFound，
	// Safely 应吞掉错误且外层事务的写入不受影响。
	topUp := &TopUp{
		UserId:          999, // 不存在的用户，触发 grant 内部查询错误
		Amount:          10,
		Money:           10,
		TradeNo:         "safely-isolated",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusSuccess,
		CreateTime:      time.Now().Unix(),
	}

	insertAffiliateRebateUser(t, 60, "bystander", 0)

	err := DB.Transaction(func(tx *gorm.DB) error {
		// 外层事务中的正常写入
		if err := tx.Model(&User{}).Where("id = ?", 60).Update("quota", gorm.Expr("quota + ?", 123)).Error; err != nil {
			return err
		}
		result := GrantAffiliateRechargeRebateSafely(tx, topUp, 10*int(common.QuotaPerUnit))
		assert.Nil(t, result)
		return nil
	})
	require.NoError(t, err)

	// 外层事务的写入应已提交，未被返利失败连累
	bystander := getAffiliateRebateUser(t, 60)
	assert.Equal(t, 123, bystander.Quota)

	var rebateCount int64
	require.NoError(t, DB.Model(&TopUp{}).Where("payment_provider = ?", PaymentProviderAffiliateRebate).Count(&rebateCount).Error)
	assert.Zero(t, rebateCount)
}

func TestGrantAffiliateRechargeRebateSafely_RollsBackPartialWrites(t *testing.T) {
	truncateTables(t)
	setAffiliateRebateTestSettings(t, 0.05)

	insertAffiliateRebateUser(t, 70, "inviter_savepoint", 0)
	insertAffiliateRebateUser(t, 71, "invitee_savepoint", 70)

	// 注入故障：返利行插入成功后，aff_history 更新失败，
	// 验证 SAVEPOINT 会回滚已插入的返利行而外层事务的写入保留
	failAffHistory := true
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("test_fail_aff_history", func(db *gorm.DB) {
		if !failAffHistory {
			return
		}
		if dest, ok := db.Statement.Dest.(map[string]interface{}); ok {
			if _, has := dest["aff_history"]; has {
				_ = db.AddError(errors.New("injected aff_history failure"))
			}
		}
	}))
	t.Cleanup(func() {
		require.NoError(t, DB.Callback().Update().Remove("test_fail_aff_history"))
	})

	topUp := &TopUp{
		UserId:          71,
		Amount:          10,
		Money:           10,
		TradeNo:         "savepoint-partial",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusSuccess,
		CreateTime:      time.Now().Unix(),
	}

	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Where("id = ?", 71).Update("quota", gorm.Expr("quota + ?", 456)).Error; err != nil {
			return err
		}
		result := GrantAffiliateRechargeRebateSafely(tx, topUp, 10*int(common.QuotaPerUnit))
		assert.Nil(t, result)
		return nil
	})
	require.NoError(t, err)
	failAffHistory = false

	// SAVEPOINT 内先插入的返利行必须被回滚，外层写入保留
	var rebateCount int64
	require.NoError(t, DB.Model(&TopUp{}).Where("payment_provider = ?", PaymentProviderAffiliateRebate).Count(&rebateCount).Error)
	assert.Zero(t, rebateCount)

	invitee := getAffiliateRebateUser(t, 71)
	assert.Equal(t, 456, invitee.Quota)

	inviter := getAffiliateRebateUser(t, 70)
	assert.Zero(t, inviter.AffHistoryQuota)
}

func TestManualCompleteTopUp_RetriesRebateForCompletedOrder(t *testing.T) {
	truncateTables(t)
	// 返利关闭时完成订单：不产生返利
	setAffiliateRebateTestSettings(t, 0)

	insertAffiliateRebateUser(t, 80, "inviter_retry", 0)
	insertAffiliateRebateUser(t, 81, "invitee_retry", 80)

	topUp := &TopUp{
		UserId:          81,
		Amount:          10,
		Money:           5,
		TradeNo:         "manual-retry-rebate",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())

	require.NoError(t, ManualCompleteTopUp(topUp.TradeNo, "127.0.0.1"))
	var rebateCount int64
	require.NoError(t, DB.Model(&TopUp{}).Where("payment_provider = ?", PaymentProviderAffiliateRebate).Count(&rebateCount).Error)
	require.Zero(t, rebateCount)

	// 开启返利后对已成功订单重跑补单：作为返利漏发的管理员修复通道
	operation_setting.GetPaymentSetting().AffiliateRebateRate = 0.05
	require.NoError(t, ManualCompleteTopUp(topUp.TradeNo, "127.0.0.1"))

	creditedQuota := int(decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart())
	rewardQuota := expectedRebateQuota(creditedQuota, 0.05)

	// 订单不会二次入账，返利按入账额度补发
	invitee := getAffiliateRebateUser(t, 81)
	assert.Equal(t, creditedQuota, invitee.Quota)

	inviter := getAffiliateRebateUser(t, 80)
	assert.Equal(t, rewardQuota, inviter.AffHistoryQuota)

	require.NoError(t, DB.Model(&TopUp{}).Where("payment_provider = ?", PaymentProviderAffiliateRebate).Count(&rebateCount).Error)
	assert.Equal(t, int64(1), rebateCount)

	// 再次重跑：幂等，不重复发放
	require.NoError(t, ManualCompleteTopUp(topUp.TradeNo, "127.0.0.1"))
	inviter = getAffiliateRebateUser(t, 80)
	assert.Equal(t, rewardQuota, inviter.AffHistoryQuota)
}

func TestAffiliateRebateUnlockTimeClampsMonthEnd(t *testing.T) {
	jan31 := time.Date(2026, time.January, 31, 10, 0, 0, 0, time.UTC)
	unlock := time.Unix(affiliateRebateUnlockTime(jan31), 0).UTC()
	assert.Equal(t, time.Date(2026, time.February, 28, 10, 0, 0, 0, time.UTC), unlock)

	mar15 := time.Date(2026, time.March, 15, 8, 30, 0, 0, time.UTC)
	unlock = time.Unix(affiliateRebateUnlockTime(mar15), 0).UTC()
	assert.Equal(t, time.Date(2026, time.April, 15, 8, 30, 0, 0, time.UTC), unlock)

	dec31 := time.Date(2026, time.December, 31, 23, 59, 59, 0, time.UTC)
	unlock = time.Unix(affiliateRebateUnlockTime(dec31), 0).UTC()
	assert.Equal(t, time.Date(2027, time.January, 31, 23, 59, 59, 0, time.UTC), unlock)
}
