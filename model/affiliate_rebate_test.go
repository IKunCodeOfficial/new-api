package model

import (
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

func TestRecharge_GrantsLockedAffiliateRebateAndReleasesAfterOneMonth(t *testing.T) {
	truncateTables(t)
	setAffiliateRebateTestSettings(t, 0.05)

	insertAffiliateRebateUser(t, 1, "inviter", 0)
	insertAffiliateRebateUser(t, 2, "invitee", 1)

	topUp := &TopUp{
		UserId:          2,
		Amount:          100,
		Money:           100,
		TradeNo:         "stripe-affiliate-rebate",
		PaymentMethod:   PaymentMethodStripe,
		PaymentProvider: PaymentProviderStripe,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())

	completed, err := Recharge(topUp.TradeNo, "cus_affiliate", "127.0.0.1", 80)
	require.NoError(t, err)
	require.True(t, completed)

	creditedQuota := int(100 * common.QuotaPerUnit)
	rewardQuota := int(decimal.NewFromFloat(80).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Mul(decimal.NewFromFloat(0.05)).IntPart())

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

	rewardQuota := int(decimal.NewFromFloat(source.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Mul(decimal.NewFromFloat(0.05)).IntPart())

	for i := 0; i < 2; i++ {
		require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
			_, err := GrantAffiliateRechargeRebateTx(tx, source, source.Money)
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
		Money:           30,
		TradeNo:         "stripe-idempotent",
		PaymentMethod:   PaymentMethodStripe,
		PaymentProvider: PaymentProviderStripe,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())

	completed, err := Recharge(topUp.TradeNo, "cus_once", "127.0.0.1", topUp.Money)
	require.NoError(t, err)
	require.True(t, completed)

	completed, err = Recharge(topUp.TradeNo, "cus_once", "127.0.0.1", topUp.Money)
	require.NoError(t, err)
	require.False(t, completed)

	creditedQuota := int(decimal.NewFromFloat(30).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart())
	rewardQuota := int(decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Mul(decimal.NewFromFloat(0.05)).IntPart())

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

	creditedQuota := int(decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart())
	rewardQuota := int(decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Mul(decimal.NewFromFloat(0.05)).IntPart())
	assert.Equal(t, creditedQuota, quotaToAdd)

	completedTopUp, quotaToAdd, rebateResult, err = CompleteEpayRecharge(topUp.TradeNo, "wxpay")
	require.NoError(t, err)
	require.NotNil(t, completedTopUp)
	assert.Zero(t, quotaToAdd)
	assert.Nil(t, rebateResult)

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

	topUp := &TopUp{
		UserId:          41,
		Amount:          50,
		Money:           50,
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
	rewardQuota := int(decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Mul(decimal.NewFromFloat(0.05)).IntPart())

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
		CompleteTime:    time.Now().AddDate(0, 1, 0).Unix(),
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
