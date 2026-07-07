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
	InviterId   int
	InviteeId   int
	RewardQuota int
	UnlockAt    int64
	Inserted    bool
}

func (result *AffiliateRebateResult) ShouldLog() bool {
	return result != nil && result.Inserted
}

func affiliateRebateTradeNo(sourceTradeNo string) string {
	return affiliateRebateTradeNoPrefix + sourceTradeNo
}

func excludeAffiliateRebateTopUps(tx *gorm.DB) *gorm.DB {
	return tx.Where("(payment_provider <> ? OR payment_provider IS NULL)", PaymentProviderAffiliateRebate)
}

// lockForUpdate 为查询追加 SELECT ... FOR UPDATE 行锁。
// SQLite 不支持 FOR UPDATE（单写者模型下也无需行锁），直接返回原查询。
func lockForUpdate(tx *gorm.DB) *gorm.DB {
	if common.UsingSQLite {
		return tx
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"})
}

// tradeNoCol 返回按当前数据库方言引号包裹的 trade_no 列名。
func tradeNoCol() string {
	if common.UsingPostgreSQL {
		return `"trade_no"`
	}
	return "`trade_no`"
}

// affiliateRebateQuota 以订单实际入账额度为基数计算返利额度。
// 基数与支付货币、汇率配置无关，保证所有完成路径（webhook/补单）结果一致。
func affiliateRebateQuota(baseQuota int) int {
	rate := operation_setting.GetAffiliateRebateRate()
	if rate <= 0 || baseQuota <= 0 {
		return 0
	}
	reward := decimal.NewFromInt(int64(baseQuota)).
		Mul(decimal.NewFromFloat(rate)).
		IntPart()
	// 比例上限为 1，返利不可能超过基数；越界视为配置异常，直接不发放
	if reward <= 0 || reward > int64(baseQuota) {
		return 0
	}
	return int(reward)
}

// affiliateRebateUnlockTime 返回锁定一个自然月后的解锁时间戳。
// 月末日期溢出时钳制到下月最后一天（如 1 月 31 日 -> 2 月 28/29 日），
// 避免 time.AddDate 的规范化行为导致多锁数天。
func affiliateRebateUnlockTime(now time.Time) int64 {
	year, month, day := now.Date()
	lastDayOfNextMonth := time.Date(year, month+2, 0, 0, 0, 0, 0, now.Location()).Day()
	if day > lastDayOfNextMonth {
		day = lastDayOfNextMonth
	}
	return time.Date(year, month+1, day, now.Hour(), now.Minute(), now.Second(), 0, now.Location()).Unix()
}

// GrantAffiliateRechargeRebateTx 在充值事务内为邀请人发放锁定返利。
// baseQuota 为该笔订单实际入账的额度；幂等性由 trade_no 唯一索引 + OnConflict DoNothing 保证。
func GrantAffiliateRechargeRebateTx(tx *gorm.DB, topUp *TopUp, baseQuota int) (*AffiliateRebateResult, error) {
	if tx == nil {
		return nil, errors.New("transaction is nil")
	}
	if topUp == nil || topUp.UserId <= 0 || topUp.TradeNo == "" || baseQuota <= 0 {
		return nil, nil
	}
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return nil, nil
	}

	rewardQuota := affiliateRebateQuota(baseQuota)
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
	if err := tx.Select("id").Where("id = ?", invitee.InviterId).First(&inviter).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	now := time.Now()
	unlockAt := affiliateRebateUnlockTime(now)
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
		CreateTime:      now.Unix(),
		CompleteTime:    unlockAt,
		Status:          common.TopUpStatusPending,
	}

	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(rebate)
	if result.Error != nil {
		return nil, result.Error
	}

	rebateResult := &AffiliateRebateResult{
		InviterId:   inviter.Id,
		InviteeId:   invitee.Id,
		RewardQuota: rewardQuota,
		UnlockAt:    unlockAt,
		Inserted:    result.RowsAffected > 0,
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

// GrantAffiliateRechargeRebateSafely 在充值事务内以 SAVEPOINT 隔离发放返利：
// 返利写入失败只回滚返利本身并记录日志，绝不影响充值订单入账。
func GrantAffiliateRechargeRebateSafely(tx *gorm.DB, topUp *TopUp, baseQuota int) *AffiliateRebateResult {
	if tx == nil || topUp == nil {
		return nil
	}
	var result *AffiliateRebateResult
	err := tx.Transaction(func(rtx *gorm.DB) error {
		var grantErr error
		result, grantErr = GrantAffiliateRechargeRebateTx(rtx, topUp, baseQuota)
		return grantErr
	})
	if err != nil {
		common.SysError(fmt.Sprintf("grant affiliate rebate failed: trade_no=%s user_id=%d base_quota=%d error=%s",
			topUp.TradeNo, topUp.UserId, baseQuota, err.Error()))
		return nil
	}
	return result
}

// releaseMatureAffiliateRebatesTx 在事务内释放到期返利：将到期的锁定流水标记为成功，
// 并原子累加用户 aff_quota，返回释放的额度。
//
// 调用方必须先对该用户 users 行加锁（lockForUpdate），这既保证其读取到的 aff_quota
// 与释放结果一致，也串行化了同一用户的并发释放。topups 的扫描刻意不加 FOR UPDATE：
// 加锁范围扫描会在 MySQL 下对该用户全部 topups 行及间隙加 next-key 锁，与
// 充值完成事务（先锁订单行、后更新 users 行）形成反向加锁顺序而死锁；
// 正确性由后面按主键的守卫 UPDATE（WHERE status=pending）+ RowsAffected 校验兜底。
func releaseMatureAffiliateRebatesTx(tx *gorm.DB, userId int, now int64) (int, error) {
	if tx == nil {
		return 0, errors.New("transaction is nil")
	}
	if userId <= 0 {
		return 0, nil
	}

	var rebates []TopUp
	if err := tx.
		Where("user_id = ? AND payment_provider = ? AND status = ? AND complete_time <= ?",
			userId, PaymentProviderAffiliateRebate, common.TopUpStatusPending, now).
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

	result := tx.Model(&TopUp{}).
		Where("id IN ? AND payment_provider = ? AND status = ?", ids, PaymentProviderAffiliateRebate, common.TopUpStatusPending).
		Update("status", common.TopUpStatusSuccess)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != int64(len(ids)) {
		return 0, errors.New("邀请返利释放状态已变化，请重试")
	}

	if err := tx.Model(&User{}).
		Where("id = ?", userId).
		Update("aff_quota", gorm.Expr("aff_quota + ?", released)).Error; err != nil {
		return 0, err
	}

	return int(released), nil
}

func ReleaseMatureAffiliateRebates(userId int) (int, error) {
	if userId <= 0 {
		return 0, nil
	}
	var released int
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).Select("id").First(&user, userId).Error; err != nil {
			return err
		}
		quota, err := releaseMatureAffiliateRebatesTx(tx, user.Id, common.GetTimestamp())
		if err != nil {
			return err
		}
		released = quota
		return nil
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
	return int(frozen), nil
}

func RecordAffiliateRechargeRebateLog(result *AffiliateRebateResult) {
	if !result.ShouldLog() {
		return
	}
	unlockDate := time.Unix(result.UnlockAt, 0).Format("2006-01-02 15:04")
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
