package setting

import (
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// WooshPay hosted checkout configuration. Top-ups are offered once
// WooshpayEnabled is on and the secret key, webhook secret and a positive unit
// price are set; test mode follows the secret key prefix, so a live key can
// never run in test mode. Checkouts are always charged in CNY, and
// WooshpayUnitPrice is the CNY price of one top-up unit.
var (
	WooshpayEnabled       = false
	WooshpaySecretKey     = ""
	WooshpayWebhookSecret = ""
	WooshpayApiBase       = "https://api.wooshpay.com"
	WooshpayUnitPrice     = 7.3
	WooshpayMinTopUp      = 1
)

// IsWooshpayTestSecretKey reports whether secretKey belongs to the WooshPay
// test environment.
func IsWooshpayTestSecretKey(secretKey string) bool {
	return strings.HasPrefix(strings.TrimSpace(secretKey), "sk_test_")
}

// WooshpayMaxTopUpUnits bounds the purchased units of one WooshPay order and
// the configurable minimum top-up.
const WooshpayMaxTopUpUnits = 10000

// WooshpayMaxMinorAmount is the largest PaymentIntent amount WooshPay accepts:
// at most 8 digits in fen.
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

// ValidateWooshpayServerAddress checks that serverAddress, the base of the
// pages WooshPay returns customers to, can be reached by them: an absolute
// http(s) URL whose host is not this machine, over https with a live key.
func ValidateWooshpayServerAddress(secretKey string, serverAddress string) error {
	address, err := url.Parse(strings.TrimRight(serverAddress, "/"))
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Hostname() == "" {
		return common.NewMessage("WooshPay can only be enabled once the server address in system settings is an absolute http(s) URL")
	}
	host := strings.TrimSuffix(strings.ToLower(address.Hostname()), ".")
	// An IPv6 zone (fe80::1%en0) names a local interface, not another host.
	host, _, _ = strings.Cut(host, "%")
	ip := net.ParseIP(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "localhost.localdomain" ||
		ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return common.NewMessage("WooshPay cannot return customers to a localhost server address; set the public address of this site in system settings")
	}
	if address.Scheme != "https" && strings.HasPrefix(strings.TrimSpace(secretKey), "sk_live_") {
		return common.NewMessage("WooshPay live secret keys (sk_live_) need an https:// server address in system settings")
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

// ValidateWooshpayOption rejects WooshPay settings that would make checkout
// amounts wrong or send the secret key over plain HTTP.
func ValidateWooshpayOption(key string, value string) error {
	switch key {
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
