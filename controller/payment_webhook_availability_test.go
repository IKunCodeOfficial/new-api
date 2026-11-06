package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func confirmPaymentComplianceForTest(t *testing.T) {
	t.Helper()
	paymentSetting := operation_setting.GetPaymentSetting()
	originalConfirmed := paymentSetting.ComplianceConfirmed
	originalTermsVersion := paymentSetting.ComplianceTermsVersion
	t.Cleanup(func() {
		paymentSetting.ComplianceConfirmed = originalConfirmed
		paymentSetting.ComplianceTermsVersion = originalTermsVersion
	})
	paymentSetting.ComplianceConfirmed = true
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
}

func TestStripeWebhookEnabledRequiresTopUpAndWebhookConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalAPISecret := setting.StripeApiSecret
	originalWebhookSecret := setting.StripeWebhookSecret
	originalPriceID := setting.StripePriceId
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		setting.StripeWebhookSecret = originalWebhookSecret
		setting.StripePriceId = originalPriceID
	})

	setting.StripeWebhookSecret = ""
	setting.StripeApiSecret = "sk_test_123"
	setting.StripePriceId = "price_123"
	require.False(t, isStripeWebhookEnabled())

	setting.StripeWebhookSecret = "whsec_test"
	require.True(t, isStripeWebhookEnabled())

	setting.StripePriceId = ""
	require.False(t, isStripeWebhookEnabled())
}

func TestWooshpayWebhookEnabledRequiresTopUpAndWebhookConfig(t *testing.T) {
	originalSecretKey := setting.WooshpaySecretKey
	originalWebhookSecret := setting.WooshpayWebhookSecret
	originalUnitPrice := setting.WooshpayUnitPrice
	originalApiBase := setting.WooshpayApiBase
	originalEnabled := setting.WooshpayEnabled
	originalServerAddress := system_setting.ServerAddress
	t.Cleanup(func() {
		system_setting.ServerAddress = originalServerAddress
		setting.WooshpayEnabled = originalEnabled
		setting.WooshpaySecretKey = originalSecretKey
		setting.WooshpayWebhookSecret = originalWebhookSecret
		setting.WooshpayUnitPrice = originalUnitPrice
		setting.WooshpayApiBase = originalApiBase
	})

	cases := []struct {
		name          string
		secretKey     string
		webhookSecret string
		unitPrice     float64
		apiBase       string
		uncompliant   bool
		enabled       bool
	}{
		{"test key on test host", "sk_test_123", "whsec_test", 8, "https://apitest.wooshpay.com", false, true},
		{"live key on live host", "sk_live_123", "whsec_test", 8, "https://api.wooshpay.com", false, true},
		{"trailing slash on the host", "sk_live_123", "whsec_test", 8, "https://api.wooshpay.com/", false, true},
		{"missing webhook secret", "sk_test_123", "", 8, "https://apitest.wooshpay.com", false, false},
		{"zero unit price", "sk_test_123", "whsec_test", 0, "https://apitest.wooshpay.com", false, false},
		{"blank secret key", " ", "whsec_test", 8, "https://apitest.wooshpay.com", false, false},
		{"test key on live host", "sk_test_123", "whsec_test", 8, "https://api.wooshpay.com", false, false},
		{"live key on test host", "sk_live_123", "whsec_test", 8, "https://apitest.wooshpay.com", false, false},
		{"unknown key prefix", "pk_live_123", "whsec_test", 8, "https://api.wooshpay.com", false, false},
		{"other host", "sk_live_123", "whsec_test", 8, "https://api.example.com", false, false},
		{"compliance not confirmed", "sk_test_123", "whsec_test", 8, "https://apitest.wooshpay.com", true, false},
	}
	system_setting.ServerAddress = "https://example.com"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			confirmPaymentComplianceForTest(t)
			if tc.uncompliant {
				operation_setting.GetPaymentSetting().ComplianceConfirmed = false
			}
			setting.WooshpaySecretKey = tc.secretKey
			setting.WooshpayWebhookSecret = tc.webhookSecret
			setting.WooshpayUnitPrice = tc.unitPrice
			setting.WooshpayApiBase = tc.apiBase
			setting.WooshpayEnabled = true
			assert.Equal(t, tc.enabled, isWooshpayWebhookEnabled())
			assert.Equal(t, tc.enabled, isWooshpayTopUpEnabled())

			// Switched off, WooshPay offers no new top-ups but still credits
			// the checkouts already opened.
			setting.WooshpayEnabled = false
			assert.False(t, isWooshpayTopUpEnabled())
			assert.Equal(t, tc.enabled, isWooshpayWebhookEnabled())
		})
	}

	// New top-ups also need a server address customers can return to: an
	// absolute http(s) URL that is not this machine, over https with a live
	// key. Payments on checkouts already opened are still accepted.
	addresses := []struct {
		secretKey     string
		serverAddress string
		topUpEnabled  bool
	}{
		{"sk_live_123", "https://example.com/", true},
		{"sk_live_123", "http://example.com", false},
		{"sk_test_123", "http://example.com", true},
		{"sk_test_123", "http://localhost:3000", false},
		{"sk_test_123", "http://LOCALHOST.", false},
		{"sk_test_123", "http://app.localhost", false},
		{"sk_test_123", "http://127.0.0.1:3000", false},
		{"sk_test_123", "http://127.8.0.1", false},
		{"sk_test_123", "http://[::1]:3000", false},
		{"sk_test_123", "http://0.0.0.0:3000", false},
		{"sk_test_123", "http://[::]:3000", false},
		{"sk_test_123", "http://[::ffff:127.0.0.1]", false},
		{"sk_test_123", "http://[::1%25lo0]:3000", false},
		{"sk_test_123", "http://localhost.localdomain", false},
		{"sk_test_123", "http://LocalHost.LocalDomain.", false},
		// Numeric forms browsers resolve to 127.0.0.1 or 0.0.0.0 although
		// they are not the canonical dotted quad.
		{"sk_test_123", "http://127.1", false},
		{"sk_test_123", "http://2130706433", false},
		{"sk_test_123", "http://0x7f000001", false},
		{"sk_test_123", "http://0X7F000001", false},
		{"sk_test_123", "http://017700000001", false},
		{"sk_test_123", "http://127.000.000.001", false},
		{"sk_test_123", "http://0x7f.0.0.1", false},
		{"sk_test_123", "http://0", false},
		{"sk_test_123", "http://0:3000", false},
		// Public addresses and names that merely contain digits stay valid.
		{"sk_test_123", "http://203.0.113.7", true},
		{"sk_test_123", "http://[2001:db8::1]", true},
		{"sk_test_123", "http://1.example.com", true},
		{"sk_test_123", "http://0xcafe.example.com", true},
		{"sk_test_123", "http://example.localdomain", true},
		{"sk_live_123", "https://localhost", false},
		{"sk_live_123", "", false},
		{"sk_live_123", "example.com", false},
		{"sk_live_123", "/wallet", false},
		{"sk_live_123", "ftp://example.com", false},
		{"sk_live_123", "https://", false},
	}
	confirmPaymentComplianceForTest(t)
	setting.WooshpayWebhookSecret, setting.WooshpayUnitPrice, setting.WooshpayEnabled = "whsec_test", 8, true
	for _, tc := range addresses {
		setting.WooshpaySecretKey, setting.WooshpayApiBase = tc.secretKey, "https://api.wooshpay.com"
		if strings.HasPrefix(tc.secretKey, "sk_test_") {
			setting.WooshpayApiBase = "https://apitest.wooshpay.com"
		}
		system_setting.ServerAddress = tc.serverAddress
		assert.Equal(t, tc.topUpEnabled, isWooshpayTopUpEnabled(), "%s with server address %q", tc.secretKey, tc.serverAddress)
		assert.True(t, isWooshpayWebhookEnabled(), "server address %q", tc.serverAddress)
	}
}

func TestCreemWebhookEnabledRequiresTopUpAndWebhookConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalAPIKey := setting.CreemApiKey
	originalProducts := setting.CreemProducts
	originalWebhookSecret := setting.CreemWebhookSecret
	t.Cleanup(func() {
		setting.CreemApiKey = originalAPIKey
		setting.CreemProducts = originalProducts
		setting.CreemWebhookSecret = originalWebhookSecret
	})

	setting.CreemWebhookSecret = ""
	setting.CreemApiKey = "creem_api_key"
	setting.CreemProducts = `[{"productId":"prod_123"}]`
	require.False(t, isCreemWebhookEnabled())

	setting.CreemWebhookSecret = "creem_secret"
	require.True(t, isCreemWebhookEnabled())

	setting.CreemProducts = "[]"
	require.False(t, isCreemWebhookEnabled())
}

func TestWaffoWebhookEnabledRequiresTopUpAndWebhookConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalEnabled := setting.WaffoEnabled
	originalSandbox := setting.WaffoSandbox
	originalAPIKey := setting.WaffoApiKey
	originalPrivateKey := setting.WaffoPrivateKey
	originalPublicCert := setting.WaffoPublicCert
	originalSandboxAPIKey := setting.WaffoSandboxApiKey
	originalSandboxPrivateKey := setting.WaffoSandboxPrivateKey
	originalSandboxPublicCert := setting.WaffoSandboxPublicCert
	t.Cleanup(func() {
		setting.WaffoEnabled = originalEnabled
		setting.WaffoSandbox = originalSandbox
		setting.WaffoApiKey = originalAPIKey
		setting.WaffoPrivateKey = originalPrivateKey
		setting.WaffoPublicCert = originalPublicCert
		setting.WaffoSandboxApiKey = originalSandboxAPIKey
		setting.WaffoSandboxPrivateKey = originalSandboxPrivateKey
		setting.WaffoSandboxPublicCert = originalSandboxPublicCert
	})

	setting.WaffoEnabled = true
	setting.WaffoSandbox = false
	setting.WaffoApiKey = ""
	setting.WaffoPrivateKey = "private"
	setting.WaffoPublicCert = "public"
	require.False(t, isWaffoWebhookEnabled())

	setting.WaffoApiKey = "api"
	require.True(t, isWaffoWebhookEnabled())

	setting.WaffoEnabled = false
	require.False(t, isWaffoWebhookEnabled())

	setting.WaffoEnabled = true
	setting.WaffoSandbox = true
	setting.WaffoSandboxApiKey = ""
	setting.WaffoSandboxPrivateKey = "sandbox_private"
	setting.WaffoSandboxPublicCert = "sandbox_public"
	require.False(t, isWaffoWebhookEnabled())

	setting.WaffoSandboxApiKey = "sandbox_api"
	require.True(t, isWaffoWebhookEnabled())
}

func TestWaffoPancakeWebhookEnabledRequiresTopUpAndWebhookConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalMerchantID := setting.WaffoPancakeMerchantID
	originalPrivateKey := setting.WaffoPancakePrivateKey
	originalProductID := setting.WaffoPancakeProductID
	t.Cleanup(func() {
		setting.WaffoPancakeMerchantID = originalMerchantID
		setting.WaffoPancakePrivateKey = originalPrivateKey
		setting.WaffoPancakeProductID = originalProductID
	})

	// Presence of all three credentials enables the gateway. Webhook public
	// keys are bundled in the SDK and there is no separate Enabled toggle —
	// clear any of the three fields to disable.
	setting.WaffoPancakeMerchantID = ""
	setting.WaffoPancakePrivateKey = "private"
	setting.WaffoPancakeProductID = "product"
	require.False(t, isWaffoPancakeWebhookEnabled())

	setting.WaffoPancakeMerchantID = "merchant"
	require.True(t, isWaffoPancakeWebhookEnabled())

	setting.WaffoPancakeProductID = ""
	require.False(t, isWaffoPancakeWebhookEnabled())

	setting.WaffoPancakeProductID = "product"
	setting.WaffoPancakePrivateKey = ""
	require.False(t, isWaffoPancakeWebhookEnabled())
}

func TestEpayWebhookEnabledRequiresTopUpAndWebhookConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalPayAddress := operation_setting.PayAddress
	originalEpayID := operation_setting.EpayId
	originalEpayKey := operation_setting.EpayKey
	originalPayMethods := operation_setting.PayMethods
	t.Cleanup(func() {
		operation_setting.PayAddress = originalPayAddress
		operation_setting.EpayId = originalEpayID
		operation_setting.EpayKey = originalEpayKey
		operation_setting.PayMethods = originalPayMethods
	})

	operation_setting.PayAddress = "https://pay.example.com"
	operation_setting.EpayId = "epay_id"
	operation_setting.EpayKey = ""
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}
	require.False(t, isEpayWebhookEnabled())

	operation_setting.EpayKey = "epay_key"
	require.True(t, isEpayWebhookEnabled())

	operation_setting.PayMethods = nil
	require.False(t, isEpayWebhookEnabled())
}

func TestEpayCallbacksVerifyBeforeSettlement(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalPayAddress := operation_setting.PayAddress
	originalEpayID := operation_setting.EpayId
	originalEpayKey := operation_setting.EpayKey
	originalPayMethods := operation_setting.PayMethods
	originalServerAddress := system_setting.ServerAddress
	originalDB := model.DB
	t.Cleanup(func() {
		operation_setting.PayAddress = originalPayAddress
		operation_setting.EpayId = originalEpayID
		operation_setting.EpayKey = originalEpayKey
		operation_setting.PayMethods = originalPayMethods
		system_setting.ServerAddress = originalServerAddress
		model.DB = originalDB
	})
	operation_setting.PayAddress = "https://pay.example.com"
	operation_setting.EpayId = "epay_id"
	operation_setting.EpayKey = "test-key"
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}
	system_setting.ServerAddress = "https://dashboard.example.com"
	// Rejected callbacks and valid non-success events must never reach settlement.
	// A nil database makes an accidental attempt to settle fail the test.
	model.DB = nil
	require.True(t, isEpayWebhookEnabled())

	// Both ambiguous maps below had this same valid signing string in v0.0.4.
	// This must fail because of parameter ambiguity, not a mismatched signature.
	ambiguousSign := epay.MD5String("money=1.00&name=item&out_trade_no=other&out_trade_no=order&pid=epay_id&trade_status=TRADE_SUCCESS&type=alipay", "test-key")
	pendingSign := epay.MD5String("money=1.00&name=item%26other&out_trade_no=order&pid=epay_id&trade_status=WAIT_BUYER_PAY&type=alipay", "test-key")
	cases := []struct {
		name         string
		productName  string
		tradeNo      string
		status       string
		sign         string
		validPending bool
	}{
		{"separator in product name", "item&out_trade_no=other", "order", epay.StatusTradeSuccess, ambiguousSign, false},
		{"separator in order number", "item", "other&out_trade_no=order", epay.StatusTradeSuccess, ambiguousSign, false},
		{"invalid signature", "item", "order", epay.StatusTradeSuccess, "invalid", false},
		{"missing signature", "item", "order", epay.StatusTradeSuccess, "", false},
		{"valid pending with literal percent escape", "item%26other", "order", "WAIT_BUYER_PAY", pendingSign, true},
	}
	endpoints := []struct {
		name             string
		path             string
		handler          gin.HandlerFunc
		status           int
		rejectedBody     string
		pendingBody      string
		rejectedLocation string
		pendingLocation  string
	}{
		{
			name: "topup notify", path: "/api/user/epay/notify", handler: EpayNotify,
			status: http.StatusOK, rejectedBody: "fail", pendingBody: "success",
		},
		{
			name: "subscription notify", path: "/api/subscription/epay/notify", handler: SubscriptionEpayNotify,
			status: http.StatusOK, rejectedBody: "fail", pendingBody: "fail",
		},
		{
			name: "subscription return", path: "/api/subscription/epay/return", handler: SubscriptionEpayReturn,
			status:           http.StatusFound,
			rejectedLocation: "https://dashboard.example.com/wallet?pay=fail",
			pendingLocation:  "https://dashboard.example.com/wallet?pay=pending",
		},
	}
	router := gin.New()
	for _, endpoint := range endpoints {
		router.Any(endpoint.path, endpoint.handler)
	}
	for _, endpoint := range endpoints {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, tc := range cases {
				t.Run(endpoint.name+"/"+method+"/"+tc.name, func(t *testing.T) {
					params := url.Values{
						"pid": {"epay_id"}, "type": {"alipay"}, "money": {"1.00"},
						"name": {tc.productName}, "out_trade_no": {tc.tradeNo},
						"trade_status": {tc.status}, "sign_type": {"MD5"},
					}
					if tc.sign != "" {
						params.Set("sign", tc.sign)
					}
					request := httptest.NewRequest(method, endpoint.path+"?"+params.Encode(), nil)
					if method == http.MethodPost {
						request = httptest.NewRequest(method, endpoint.path, strings.NewReader(params.Encode()))
						request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					}
					recorder := httptest.NewRecorder()
					require.NotPanics(t, func() { router.ServeHTTP(recorder, request) })
					assert.Equal(t, endpoint.status, recorder.Code)
					wantBody, wantLocation := endpoint.rejectedBody, endpoint.rejectedLocation
					if tc.validPending {
						wantBody, wantLocation = endpoint.pendingBody, endpoint.pendingLocation
					}
					if wantBody != "" {
						assert.Equal(t, wantBody, recorder.Body.String())
					}
					assert.Equal(t, wantLocation, recorder.Header().Get("Location"))
				})
			}
		}
	}
}

// While WooshPay is switched on but offers no top-ups, the admin page learns
// why from a stable code; it is empty while WooshPay works or is switched
// off, and it cannot be written.
func TestWooshpayUnavailableReasonNamesTheUnmetRequirement(t *testing.T) {
	setupRebateControllerTest(t)
	originalSecretKey, originalWebhookSecret := setting.WooshpaySecretKey, setting.WooshpayWebhookSecret
	originalUnitPrice, originalApiBase := setting.WooshpayUnitPrice, setting.WooshpayApiBase
	originalEnabled, originalServerAddress := setting.WooshpayEnabled, system_setting.ServerAddress
	t.Cleanup(func() {
		system_setting.ServerAddress = originalServerAddress
		setting.WooshpayEnabled = originalEnabled
		setting.WooshpaySecretKey, setting.WooshpayWebhookSecret = originalSecretKey, originalWebhookSecret
		setting.WooshpayUnitPrice, setting.WooshpayApiBase = originalUnitPrice, originalApiBase
	})
	reasonOption := func() string {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)
		GetOptions(ctx)
		var out struct {
			Data []model.Option `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &out))
		values := []string{}
		for _, option := range out.Data {
			if option.Key == "WooshpayUnavailableReason" {
				values = append(values, option.Value)
			}
		}
		require.Len(t, values, 1, "the derived option is listed once")
		return values[0]
	}

	cases := []struct {
		name   string
		change func()
		reason string
	}{
		{"available", func() {}, ""},
		{"compliance not confirmed", func() { operation_setting.GetPaymentSetting().ComplianceConfirmed = false }, "compliance_not_confirmed"},
		{"missing secret key", func() { setting.WooshpaySecretKey = " " }, "missing_secret"},
		{"missing webhook secret", func() { setting.WooshpayWebhookSecret = "" }, "missing_webhook_secret"},
		{"key of the other environment", func() { setting.WooshpayApiBase = "https://apitest.wooshpay.com" }, "key_base_mismatch"},
		{"zero unit price", func() { setting.WooshpayUnitPrice = 0 }, "invalid_unit_price"},
		{"localhost server address", func() { system_setting.ServerAddress = "http://localhost:3000" }, "invalid_server_address"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			confirmPaymentComplianceForTest(t)
			setting.WooshpaySecretKey, setting.WooshpayWebhookSecret = "sk_live_123", "whsec_test"
			setting.WooshpayUnitPrice, setting.WooshpayApiBase = 8, "https://api.wooshpay.com"
			system_setting.ServerAddress = "https://example.com"
			tc.change()

			setting.WooshpayEnabled = true
			assert.Equal(t, tc.reason, reasonOption())
			assert.Equal(t, tc.reason == "", isWooshpayTopUpEnabled())
			setting.WooshpayEnabled = false
			assert.Empty(t, reasonOption(), "nothing to explain while WooshPay is switched off")
		})
	}

	const readOnly = "WooshPay status is derived from its settings and is read-only"
	assert.EqualError(t, model.UpdateOption("WooshpayUnavailableReason", ""), readOnly)
	assert.EqualError(t, model.UpdateOptionsBulk(map[string]string{"WooshpayUnavailableReason": "missing_secret"}), readOnly)
}
