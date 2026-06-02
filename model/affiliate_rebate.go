package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const affiliateRebateTradeNoPrefix = "AFFREBATE:"

type AffiliateRebateResult struct {
	InviterId     int
	InviteeId     int
	SourceTradeNo string
	RewardQuota   int
	BaseMoney     float64
	Rate          float64
	UnlockAt      int64
	Inserted      bool
}

func (result *AffiliateRebateResult) ShouldLog() bool {
	return result != nil && result.Inserted && result.InviterId > 0 && result.RewardQuota > 0
}

func affiliateRebateTradeNo(sourceTradeNo string) string {
	return affiliateRebateTradeNoPrefix + sourceTradeNo
}

func excludeAffiliateRebateTopUps(tx *gorm.DB) *gorm.DB {
	return tx.Where("(payment_provider <> ? OR payment_provider IS NULL)", PaymentProviderAffiliateRebate)
}

func affiliateRebateQuota(paymentMoney float64) int {
	rate := operation_setting.GetAffiliateRebateRate()
	if rate <= 0 || paymentMoney <= 0 {
		return 0
	}
	reward := decimal.NewFromFloat(paymentMoney).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit)).
		Mul(decimal.NewFromFloat(rate)).
		IntPart()
	maxInt := int64(^uint(0) >> 1)
	if reward <= 0 || reward > maxInt {
		return 0
	}
	return int(reward)
}

func GrantAffiliateRechargeRebateTx(tx *gorm.DB, topUp *TopUp, paymentMoney float64) (*AffiliateRebateResult, error) {
	if tx == nil {
		return nil, errors.New("transaction is nil")
	}
	if topUp == nil || topUp.UserId <= 0 || topUp.TradeNo == "" || paymentMoney <= 0 {
		return nil, nil
	}
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return nil, nil
	}

	rewardQuota := affiliateRebateQuota(paymentMoney)
	if rewardQuota <= 0 {
		return nil, nil
	}

	var invitee User
	if err := tx.Select("id", "inviter_id").Where("id = ?", topUp.UserId).First(&invitee).Error; err != nil {
		return nil, err
	}
	if invitee.InviterId <= 0 || invitee.InviterId == invitee.Id {
		return nil, nil
	}

	var inviter User
	if err := tx.Set("gorm:query_option", "FOR UPDATE").
		Select("id").
		Where("id = ?", invitee.InviterId).
		First(&inviter).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	now := common.GetTimestamp()
	unlockAt := time.Now().AddDate(0, 1, 0).Unix()
	rewardMoney := decimal.NewFromInt(int64(rewardQuota)).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		InexactFloat64()

	rebate := &TopUp{
		UserId:          inviter.Id,
		Amount:          int64(rewardQuota),
		Money:           rewardMoney,
		TradeNo:         affiliateRebateTradeNo(topUp.TradeNo),
		PaymentMethod:   PaymentMethodAffiliateRebate,
		PaymentProvider: PaymentProviderAffiliateRebate,
		CreateTime:      now,
		CompleteTime:    unlockAt,
		Status:          common.TopUpStatusPending,
	}

	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(rebate)
	if result.Error != nil {
		return nil, result.Error
	}

	rebateResult := &AffiliateRebateResult{
		InviterId:     inviter.Id,
		InviteeId:     invitee.Id,
		SourceTradeNo: topUp.TradeNo,
		RewardQuota:   rewardQuota,
		BaseMoney:     paymentMoney,
		Rate:          operation_setting.GetAffiliateRebateRate(),
		UnlockAt:      unlockAt,
		Inserted:      result.RowsAffected > 0,
	}
	if !rebateResult.Inserted {
		return rebateResult, nil
	}

	if err := tx.Model(&User{}).
		Where("id = ?", inviter.Id).
		Update("aff_history", gorm.Expr("aff_history + ?", rewardQuota)).Error; err != nil {
		return nil, err
	}

	return rebateResult, nil
}

func releaseMatureAffiliateRebatesForLockedUserTx(tx *gorm.DB, user *User, now int64) (int, error) {
	if tx == nil {
		return 0, errors.New("transaction is nil")
	}
	if user == nil || user.Id <= 0 {
		return 0, nil
	}

	var rebates []TopUp
	if err := tx.Set("gorm:query_option", "FOR UPDATE").
		Where("user_id = ? AND payment_provider = ? AND status = ? AND complete_time <= ?",
			user.Id, PaymentProviderAffiliateRebate, common.TopUpStatusPending, now).
		Find(&rebates).Error; err != nil {
		return 0, err
	}
	if len(rebates) == 0 {
		return 0, nil
	}

	var released int64
	ids := make([]int, 0, len(rebates))
	for _, rebate := range rebates {
		if rebate.Amount <= 0 {
			continue
		}
		ids = append(ids, rebate.Id)
		released += rebate.Amount
	}
	if released <= 0 || len(ids) == 0 {
		return 0, nil
	}
	maxInt := int64(^uint(0) >> 1)
	if released > maxInt {
		return 0, errors.New("邀请返利金额超出有效范围")
	}

	result := tx.Model(&TopUp{}).
		Where("id IN ? AND payment_provider = ? AND status = ?", ids, PaymentProviderAffiliateRebate, common.TopUpStatusPending).
		Update("status", common.TopUpStatusSuccess)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != int64(len(ids)) {
		return 0, errors.New("邀请返利释放状态已变化，请重试")
	}

	user.AffQuota += int(released)
	return int(released), nil
}

func ReleaseMatureAffiliateRebates(userId int) (int, error) {
	if userId <= 0 {
		return 0, nil
	}
	var released int
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := tx.Set("gorm:query_option", "FOR UPDATE").First(&user, userId).Error; err != nil {
			return err
		}
		quota, err := releaseMatureAffiliateRebatesForLockedUserTx(tx, &user, common.GetTimestamp())
		if err != nil || quota <= 0 {
			return err
		}
		released = quota
		return tx.Model(&User{}).
			Where("id = ?", user.Id).
			Update("aff_quota", gorm.Expr("aff_quota + ?", quota)).Error
	})
	return released, err
}

func GetAffiliateFrozenQuota(userId int) (int, error) {
	if userId <= 0 {
		return 0, nil
	}
	var frozen int64
	err := DB.Model(&TopUp{}).
		Where("user_id = ? AND payment_provider = ? AND status = ? AND complete_time > ?",
			userId, PaymentProviderAffiliateRebate, common.TopUpStatusPending, common.GetTimestamp()).
		Select("COALESCE(SUM(amount), 0)").
		Scan(&frozen).Error
	if err != nil {
		return 0, err
	}
	maxInt := int64(^uint(0) >> 1)
	if frozen > maxInt {
		return 0, errors.New("邀请返利冻结金额超出有效范围")
	}
	return int(frozen), nil
}

func RecordAffiliateRechargeRebateLog(result *AffiliateRebateResult) {
	if !result.ShouldLog() {
		return
	}
	unlockDate := time.Unix(result.UnlockAt, 0).Format("2006-01-02")
	RecordLog(
		result.InviterId,
		LogTypeSystem,
		fmt.Sprintf("邀请用户 #%d 在线充值成功，获得返利 %s，锁定至 %s 后可划转，返利仅可用于消费。",
			result.InviteeId,
			logger.FormatQuota(result.RewardQuota),
			unlockDate,
		),
	)
}
