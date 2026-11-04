package setting

import (
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// WooshPay hosted checkout configuration. Top-ups are offered once
// WooshpayEnabled is on and the secret key, webhook secret and a positive unit
// price are set; test mode follows the secret key prefix, so a live key can
// never run in test mode.
var (
	WooshpayEnabled       = false
	WooshpaySecretKey     = ""
	WooshpayWebhookSecret = ""
	WooshpayApiBase       = "https://api.wooshpay.com"
	WooshpayCurrency      = "USD"
	WooshpayUnitPrice     = 8.0
	WooshpayMinTopUp      = 1
)

// IsWooshpayTestKey reports whether the configured secret key belongs to the
// WooshPay test environment, where payments move no real money.
func IsWooshpayTestKey() bool {
	return IsWooshpayTestSecretKey(WooshpaySecretKey)
}

// IsWooshpayTestSecretKey reports whether secretKey belongs to the WooshPay
// test environment.
func IsWooshpayTestSecretKey(secretKey string) bool {
	return strings.HasPrefix(strings.TrimSpace(secretKey), "sk_test_")
}

// WooshpayMaxTopUpUnits bounds the purchased units of one WooshPay order and
// the configurable minimum top-up.
const WooshpayMaxTopUpUnits = 10000

// WooshpayMaxMinorAmount is the largest PaymentIntent amount WooshPay accepts:
// at most 8 digits in the currency's minor unit.
const WooshpayMaxMinorAmount = 99_999_999

// wooshpayKeyHost returns the API host the secret key belongs to: sk_test_
// keys only work against apitest.wooshpay.com and sk_live_ keys against
// api.wooshpay.com. ok is false for a key of neither kind.
func wooshpayKeyHost(secretKey string) (host string, ok bool) {
	secretKey = strings.TrimSpace(secretKey)
	switch {
	case strings.HasPrefix(secretKey, "sk_test_"):
		return "apitest.wooshpay.com", true
	case strings.HasPrefix(secretKey, "sk_live_"):
		return "api.wooshpay.com", true
	default:
		return "", false
	}
}

// WooshpayApiBaseMatchesKey reports whether the API base points at the
// WooshPay environment of the secret key.
func WooshpayApiBaseMatchesKey() bool {
	return WooshpayKeyMatchesApiBase(WooshpaySecretKey, WooshpayApiBase)
}

// WooshpayKeyMatchesApiBase reports whether apiBase is the https origin of
// the WooshPay environment secretKey belongs to.
func WooshpayKeyMatchesApiBase(secretKey string, apiBase string) bool {
	base, err := url.Parse(NormalizeWooshpayApiBase(apiBase))
	if err != nil || base.Scheme != "https" {
		return false
	}
	host, ok := wooshpayKeyHost(secretKey)
	return ok && base.Host == host
}

// ValidateWooshpayEnabled refuses to switch WooshPay on before the secret
// key, the webhook secret and the API base of one environment are set.
func ValidateWooshpayEnabled(secretKey string, webhookSecret string, apiBase string) error {
	if strings.TrimSpace(secretKey) == "" || strings.TrimSpace(webhookSecret) == "" || !WooshpayKeyMatchesApiBase(secretKey, apiBase) {
		return common.NewMessage("WooshPay can only be enabled once the secret key, the webhook secret and the API base of one environment are set")
	}
	return nil
}

// ValidateWooshpayKeyBase rejects a secret key paired with the API base of
// the other WooshPay environment. A key of unknown kind or an unset key is
// not a known mismatch; WooshPay simply stays disabled until both match.
func ValidateWooshpayKeyBase(secretKey string, apiBase string) error {
	host, ok := wooshpayKeyHost(secretKey)
	if !ok {
		return nil
	}
	base, err := url.Parse(NormalizeWooshpayApiBase(apiBase))
	if err == nil && base.Host == host {
		return nil
	}
	return common.NewMessage("WooshPay test secret keys (sk_test_) need the API base https://apitest.wooshpay.com, and live secret keys (sk_live_) need https://api.wooshpay.com")
}

// NormalizeWooshpayApiBase trims spaces and trailing slashes so API paths can
// be appended directly.
func NormalizeWooshpayApiBase(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

// WooshpayCurrencyMinorDigits lists the currencies WooshPay accepts with the
// number of decimal places of their minor unit (unit_amount is expressed in
// that unit), as published on the WooshPay "Supported currencies" page.
var WooshpayCurrencyMinorDigits = map[string]int32{
	"AED": 2, "AFN": 2, "ALL": 2, "AMD": 2, "ANG": 2, "AOA": 2, "ARS": 2, "AUD": 2, "AWG": 2,
	"AZN": 2, "BAM": 2, "BBD": 2, "BDT": 2, "BGN": 2, "BHD": 3, "BIF": 0, "BMD": 2, "BND": 2,
	"BOB": 2, "BRL": 2, "BSD": 2, "BTN": 2, "BWP": 2, "BYN": 2, "BZD": 2, "CAD": 2, "CDF": 2,
	"CHF": 2, "CLP": 0, "CNY": 2, "COP": 2, "CRC": 2, "CVE": 2, "CZK": 2, "DJF": 0, "DKK": 2,
	"DOP": 2, "DZD": 2, "EGP": 2, "ERN": 2, "ETB": 2, "EUR": 2, "FJD": 2, "FKP": 2, "GBP": 2,
	"GEL": 2, "GHS": 2, "GIP": 2, "GMD": 2, "GNF": 0, "GTQ": 2, "GYD": 2, "HKD": 2, "HNL": 2,
	"HRK": 2, "HTG": 2, "HUF": 2, "IDR": 2, "ILS": 2, "INR": 2, "IQD": 3, "ISK": 0, "JMD": 2,
	"JOD": 3, "JPY": 0, "KES": 2, "KGS": 2, "KHR": 2, "KMF": 0, "KRW": 0, "KWD": 3, "KYD": 2,
	"KZT": 2, "LAK": 2, "LBP": 2, "LKR": 2, "LRD": 2, "LSL": 2, "LTL": 2, "LYD": 3, "MAD": 2,
	"MDL": 2, "MGA": 2, "MKD": 2, "MMK": 2, "MNT": 2, "MOP": 2, "MRU": 2, "MUR": 2, "MVR": 2,
	"MWK": 2, "MXN": 2, "MYR": 2, "MZN": 2, "NAD": 2, "NGN": 2, "NIO": 2, "NOK": 2, "NPR": 2,
	"NZD": 2, "OMR": 3, "PAB": 2, "PEN": 2, "PGK": 2, "PHP": 2, "PKR": 2, "PLN": 2, "PYG": 0,
	"QAR": 2, "RON": 2, "RSD": 2, "RUB": 2, "RWF": 0, "SAR": 2, "SBD": 2, "SCR": 2, "SEK": 2,
	"SGD": 2, "SHP": 2, "SLL": 2, "SOS": 2, "SRD": 2, "SSP": 2, "STN": 2, "SVC": 2, "SYP": 2,
	"SZL": 2, "THB": 2, "TJS": 2, "TMT": 2, "TND": 3, "TOP": 2, "TRY": 2, "TTD": 2, "TWD": 2,
	"TZS": 2, "UAH": 2, "UGX": 0, "USD": 2, "UYU": 2, "UZS": 2, "VND": 0, "VUV": 0, "WST": 2,
	"XAF": 0, "XCD": 2, "XOF": 0, "XPF": 0, "YER": 2, "ZAR": 2, "ZWL": 2,
}

// ValidateWooshpayOption rejects WooshPay settings that would make checkout
// amounts wrong or send the secret key over plain HTTP.
func ValidateWooshpayOption(key string, value string) error {
	switch key {
	case "WooshpayCurrency":
		if _, ok := WooshpayCurrencyMinorDigits[value]; !ok {
			return common.NewMessage("WooshPay currency must be an uppercase ISO 4217 code supported by WooshPay, such as USD")
		}
	case "WooshpayUnitPrice":
		price, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 {
			return common.NewMessage("WooshPay unit price must be a number greater than 0")
		}
	case "WooshpayMinTopUp":
		minTopUp, err := strconv.Atoi(value)
		if err != nil || minTopUp < 1 || minTopUp > WooshpayMaxTopUpUnits {
			return common.NewMessage("WooshPay minimum top-up must be an integer from 1 to {{max}}", map[string]any{"max": WooshpayMaxTopUpUnits})
		}
	case "WooshpayApiBase":
		// Only an origin: API paths are appended to it, so a path, query or
		// credentials would change every request.
		base, err := url.Parse(NormalizeWooshpayApiBase(value))
		if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil ||
			base.Path != "" || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.Opaque != "" {
			return common.NewMessage("WooshPay API base must be an https:// origin without a path, such as https://api.wooshpay.com")
		}
	case "WooshpayEnabled":
		if value != "true" && value != "false" {
			return common.NewMessage("WooshPay enabled must be true or false")
		}
	}
	return nil
}

// NormalizeWooshpayCurrency trims and upper-cases an ISO 4217 currency code.
func NormalizeWooshpayCurrency(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}
