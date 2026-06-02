package operation_setting

import (
	"math"

	"github.com/QuantumNous/new-api/setting/config"
)

type PaymentSetting struct {
	AmountOptions       []int           `json:"amount_options"`
	AmountDiscount      map[int]float64 `json:"amount_discount"`       // 充值金额对应的折扣，例如 100 元 0.9 表示 100 元充值享受 9 折优惠
	AffiliateRebateRate float64         `json:"affiliate_rebate_rate"` // 邀请用户充值返利比例，0.05 表示 5%

	ComplianceConfirmed    bool   `json:"compliance_confirmed"`
	ComplianceTermsVersion string `json:"compliance_terms_version"`
	ComplianceConfirmedAt  int64  `json:"compliance_confirmed_at"`
	ComplianceConfirmedBy  int    `json:"compliance_confirmed_by"`
	ComplianceConfirmedIP  string `json:"compliance_confirmed_ip"`
}

const CurrentComplianceTermsVersion = "v1"

// 默认配置
var paymentSetting = PaymentSetting{
	AmountOptions:       []int{10, 20, 50, 100, 200, 500},
	AmountDiscount:      map[int]float64{},
	AffiliateRebateRate: 0.05,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("payment_setting", &paymentSetting)
}

func GetPaymentSetting() *PaymentSetting {
	return &paymentSetting
}

func GetAffiliateRebateRate() float64 {
	rate := paymentSetting.AffiliateRebateRate
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > 1 {
		return 0
	}
	return rate
}

func IsPaymentComplianceConfirmed() bool {
	return paymentSetting.ComplianceConfirmed &&
		paymentSetting.ComplianceTermsVersion == CurrentComplianceTermsVersion
}
