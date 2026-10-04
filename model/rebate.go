package model

// Fork-only file (ikun): invite-rebate support tables and paid-amount capture.
// Settlement runs in an external Python job; see
// docs/superpowers/specs/2026-10-01-invite-rebate-design.md.

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// RebateAdminRecharge records an admin quota grant marked as a recharge.
// PaidQuota (<= Quota) is what counts toward the invite-rebate base.
type RebateAdminRecharge struct {
	Id         int   `json:"id"`
	UserId     int   `json:"user_id" gorm:"index"`
	Quota      int64 `json:"quota"`
	PaidQuota  int64 `json:"paid_quota"`
	OperatorId int   `json:"operator_id"`
	CreatedAt  int64 `json:"created_at" gorm:"type:bigint;index"`
}

func (RebateAdminRecharge) TableName() string { return "rebate_admin_recharges" }

// RebateTopupPaid stores the paid amount of an online top-up order in quota
// units, converted with the unit price in effect when the order was created.
type RebateTopupPaid struct {
	Id        int    `json:"id"`
	TradeNo   string `json:"trade_no" gorm:"type:varchar(255);uniqueIndex"`
	UserId    int    `json:"user_id" gorm:"index"`
	PaidQuota int64  `json:"paid_quota"`
	CreatedAt int64  `json:"created_at" gorm:"type:bigint"`
}

func (RebateTopupPaid) TableName() string { return "rebate_topup_paid" }

// RebateExclusion excludes a user from rebate payouts as an inviter or as an
// invitee. Excluded amounts still advance the settlement watermark, so
// removing an exclusion never back-pays.
type RebateExclusion struct {
	Id        int    `json:"id"`
	UserId    int    `json:"user_id" gorm:"uniqueIndex:idx_rebate_exclusion_user_role,priority:1"`
	Role      string `json:"role" gorm:"type:varchar(16);uniqueIndex:idx_rebate_exclusion_user_role,priority:2"`
	Note      string `json:"note" gorm:"type:varchar(255);default:''"`
	CreatedAt int64  `json:"created_at" gorm:"type:bigint"`
}

func (RebateExclusion) TableName() string { return "rebate_exclusions" }

// paidQuotaFromCharge converts what the user paid at order creation into quota
// units. ok=false means "store nothing": the settler then counts the credited
// quota, which never exceeds the paid-amount reading for discounted orders.
//
// Creem and Amount are the two channels whose stored value is not a discountable
// price, so they are returned directly; the rest recover the list-price value of
// the order by dividing the charged money by the unit price in effect.
func paidQuotaFromCharge(provider string, amount int64, money float64) (int, bool) {
	// Subscriptions bought with balance and unpaid test orders reach here with a
	// zero quantity; they must not leave a paid-amount row behind.
	if amount <= 0 || money <= 0 {
		return 0, false
	}
	dMoney := decimal.NewFromFloat(money)
	dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)

	// Wallet-sized conversion: unlike single-request quota this must stay exact
	// for large orders, so it uses the strict helper and skips the row when the
	// value cannot be represented instead of storing a saturated one.
	toPaidQuota := func(d decimal.Decimal) (int, bool) {
		quota, err := common.WalletQuotaFromDecimalStrict(d)
		if err == nil && quota > 0 {
			return quota, true
		}
		reason := "rounded to zero"
		if err != nil {
			reason = err.Error()
		}
		common.SysError(fmt.Sprintf(
			"rebate_topup_paid skipped: provider=%s amount=%d money=%v paid=%s reason=%s",
			provider, amount, money, d.String(), reason))
		return 0, false
	}

	var unitPrice float64
	switch provider {
	case PaymentProviderCreem:
		// Amount is already quota and there is no list price to compare with.
		return int(amount), true
	case PaymentProviderStripe:
		// Money is already the group-ratio-adjusted USD count that the credited
		// quota is derived from; no discount is visible here.
		return toPaidQuota(dMoney.Mul(dQuotaPerUnit))
	case PaymentProviderEpay:
		unitPrice = operation_setting.Price
	case PaymentProviderWaffo:
		unitPrice = setting.WaffoUnitPrice
	case PaymentProviderWaffoPancake:
		unitPrice = setting.WaffoPancakeUnitPrice
	default:
		// Unknown or not-yet-supported channels (including balance, which only
		// shows up when a database was edited by hand) store nothing rather than
		// guessing a paid amount.
		return 0, false
	}
	if unitPrice <= 0 {
		return 0, false
	}
	return toPaidQuota(dMoney.Div(decimal.NewFromFloat(unitPrice)).Mul(dQuotaPerUnit))
}

// resolveTopUpProvider returns the payment channel of an order. Orders from
// before payment_provider existed only carry payment_method; epay stores the
// pay type there (alipay, wxpay, ...), so anything unrecognised is epay.
func resolveTopUpProvider(topUp *TopUp) string {
	if topUp.PaymentProvider != "" {
		return topUp.PaymentProvider
	}
	switch topUp.PaymentMethod {
	case PaymentMethodStripe:
		return PaymentProviderStripe
	case PaymentMethodCreem:
		return PaymentProviderCreem
	case PaymentMethodWaffo:
		return PaymentProviderWaffo
	case PaymentMethodWaffoPancake:
		return PaymentProviderWaffoPancake
	default:
		return PaymentProviderEpay
	}
}

// AfterCreate runs inside the order-insert transaction (SkipDefaultTransaction
// is off), so an order never exists without its paid row; a failed write
// fails the order. Orders with no usable price store nothing.
func (topUp *TopUp) AfterCreate(tx *gorm.DB) error {
	paid, ok := paidQuotaFromCharge(resolveTopUpProvider(topUp), topUp.Amount, topUp.Money)
	if !ok {
		return nil
	}
	return tx.Create(&RebateTopupPaid{
		TradeNo:   topUp.TradeNo,
		UserId:    topUp.UserId,
		PaidQuota: int64(paid),
		CreatedAt: common.GetTimestamp(),
	}).Error
}
