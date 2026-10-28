package model

// Fork-only file (ikun): invite-rebate support tables and paid-amount capture.
// Settlement runs in an external Python job; see
// docs/superpowers/specs/2026-10-01-invite-rebate-design.md.

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Options the external settler reads; the admin settings block writes them
// through PUT /api/option/.
const (
	IkunRebateEnabledOptionKey = "IkunRebateEnabled"
	IkunRebateRateOptionKey    = "IkunRebateRate"
)

var ikunRebateRatePattern = regexp.MustCompile(`^0\.[0-9]{1,4}$`)

// ParseIkunRebateRate accepts only the plain decimal form 0 < rate < 1 with at
// most 4 decimals (the settings block edits a percentage with 2 decimals),
// such as "0.05". The settler parses the stored string with Python Decimal, so
// forms that only Go accepts ("0x1p-4") or that Go rejects but Python accepts
// (" 0.05") must never be stored or advertised.
func ParseIkunRebateRate(value string) (float64, bool) {
	if !ikunRebateRatePattern.MatchString(value) {
		return 0, false
	}
	rate, err := strconv.ParseFloat(value, 64)
	if err != nil || rate <= 0 {
		return 0, false
	}
	return rate, true
}

// validateIkunRebateOption rejects switch and rate values the settler would
// refuse, so a bad write fails at save time instead of stopping settlement.
func validateIkunRebateOption(key, value string) error {
	switch key {
	case IkunRebateRateOptionKey:
		if _, ok := ParseIkunRebateRate(value); !ok {
			return common.NewMessage("Rebate rate must be a decimal between 0 and 1 with at most 4 decimal places, such as 0.05")
		}
	case IkunRebateEnabledOptionKey:
		if value != "true" && value != "false" {
			return common.NewMessage("Rebate switch must be true or false")
		}
	}
	return nil
}

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
// Creem and Stripe are the two channels whose stored value is not a discountable
// price, so they are valued by the purchased quantity; the rest recover the
// list-price value of the order by dividing the charged money by the unit price
// in effect.
func paidQuotaFromCharge(provider string, amount int64, money float64) (int, bool) {
	// Online-paid subscriptions (upsertSubscriptionTopUpTx) and unpaid test orders
	// reach here with a zero amount; they must not leave a paid-amount row behind.
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
		// Checkout charges Quantity=amount at the fixed Stripe price, while the
		// top-up group ratio only scales the credited quota (Money = amount ×
		// ratio). The paid value is therefore the purchased quantity; valuing it
		// at Money would pay rebates on quota the ratio granted for free.
		return toPaidQuota(decimal.NewFromInt(amount).Mul(dQuotaPerUnit))
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
//
// Orders created against a gateway's sandbox or test environment move no real
// money but still credit quota, so they store a zero paid amount: the settler
// counts min(credited, paid) and must not pay rebates on them.
func (topUp *TopUp) AfterCreate(tx *gorm.DB) error {
	provider := resolveTopUpProvider(topUp)
	paid, ok := paidQuotaFromCharge(provider, topUp.Amount, topUp.Money)
	if !ok {
		return nil
	}
	if provider == PaymentProviderWaffo && setting.WaffoSandbox || provider == PaymentProviderCreem && setting.CreemTestMode {
		paid = 0
	}
	return tx.Create(&RebateTopupPaid{
		TradeNo:   topUp.TradeNo,
		UserId:    topUp.UserId,
		PaidQuota: int64(paid),
		CreatedAt: common.GetTimestamp(),
	}).Error
}

// GatewayCharge is what a payment gateway reports when an order completes.
// Listed and Charged are the amounts before and after discounts in the same
// minor unit (Stripe amount_subtotal / amount_total); Test marks a sandbox or
// test-mode payment that moved no real money. The zero value changes nothing.
type GatewayCharge struct {
	Listed  int64
	Charged int64
	Test    bool
}

// applyGatewayCharge lowers the order's paid snapshot to what the gateway
// actually collected. Discounts such as Stripe promotion codes are only known
// at completion, and the snapshot taken at order creation would otherwise pay
// rebates on the undiscounted price. It never raises the snapshot.
func applyGatewayCharge(tx *gorm.DB, topUp *TopUp, charge GatewayCharge) error {
	charged, listed := charge.Charged, charge.Listed
	if charge.Test {
		charged, listed = 0, 1
	}
	if listed <= 0 || charged < 0 || charged >= listed {
		return nil
	}
	var row RebateTopupPaid
	err := tx.Where("trade_no = ?", topUp.TradeNo).First(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	base := decimal.NewFromInt(row.PaidQuota)
	if row.Id == 0 {
		// Orders created before the snapshot existed fall back to the
		// creation-time conversion so the discount is still recorded.
		paid, ok := paidQuotaFromCharge(resolveTopUpProvider(topUp), topUp.Amount, topUp.Money)
		if !ok {
			return nil
		}
		base = decimal.NewFromInt(int64(paid))
	}
	target := base.Mul(decimal.NewFromInt(charged)).Div(decimal.NewFromInt(listed)).Floor()
	discounted, err := common.WalletQuotaFromDecimalStrict(target)
	if err != nil {
		return err
	}
	if row.Id == 0 {
		return tx.Create(&RebateTopupPaid{
			TradeNo: topUp.TradeNo, UserId: topUp.UserId, PaidQuota: int64(discounted), CreatedAt: common.GetTimestamp(),
		}).Error
	}
	return tx.Model(&row).Update("paid_quota", discounted).Error
}
