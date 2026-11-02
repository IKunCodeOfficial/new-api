package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const wooshpayTestWebhookSecret = "whsec_yDjRWBtest"

// wooshpayTestTransport sends every outbound request to the test server, so
// the configured API base can stay the real WooshPay host.
type wooshpayTestTransport struct{ target *url.URL }

func (tr wooshpayTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = tr.target.Scheme, tr.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// setupWooshpayTest configures a live-mode WooshPay account whose API is
// served by api, with an in-memory database and USD pricing at 8 per unit.
func setupWooshpayTest(t *testing.T, api http.HandlerFunc) {
	t.Helper()
	setupRebateControllerTest(t)
	confirmPaymentComplianceForTest(t)
	service.InitHttpClient()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	client := service.GetHttpClient()
	transport := client.Transport
	client.Transport = wooshpayTestTransport{target: target}
	t.Cleanup(func() { client.Transport = transport })

	secretKey, webhookSecret, apiBase := setting.WooshpaySecretKey, setting.WooshpayWebhookSecret, setting.WooshpayApiBase
	currency, unitPrice, minTopUp := setting.WooshpayCurrency, setting.WooshpayUnitPrice, setting.WooshpayMinTopUp
	quotaPerUnit := common.QuotaPerUnit
	displayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	discount := operation_setting.GetPaymentSetting().AmountDiscount
	t.Cleanup(func() {
		setting.WooshpaySecretKey, setting.WooshpayWebhookSecret, setting.WooshpayApiBase = secretKey, webhookSecret, apiBase
		setting.WooshpayCurrency, setting.WooshpayUnitPrice, setting.WooshpayMinTopUp = currency, unitPrice, minTopUp
		common.QuotaPerUnit = quotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = displayType
		operation_setting.GetPaymentSetting().AmountDiscount = discount
	})
	setting.WooshpaySecretKey = "sk_live_wooshpay"
	setting.WooshpayWebhookSecret = wooshpayTestWebhookSecret
	setting.WooshpayApiBase = "https://api.wooshpay.com"
	setting.WooshpayCurrency = "USD"
	setting.WooshpayUnitPrice = 8
	setting.WooshpayMinTopUp = 1
	common.QuotaPerUnit = 500000
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{}
}

func insertWooshpayTestOrder(t *testing.T, userID int, tradeNo string) {
	t.Helper()
	require.NoError(t, (&model.TopUp{
		UserId: userID, Amount: 10, Money: 80, TradeNo: tradeNo, PaymentMethod: model.PaymentMethodWooshpay,
		PaymentProvider: model.PaymentProviderWooshpay, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix(),
	}).Insert())
}

// wooshpayTestIntent is a succeeded live PaymentIntent of 80.00 USD that
// matches the metadata this server writes for tradeNo.
func wooshpayTestIntent(id string, tradeNo string) string {
	return fmt.Sprintf(`{"id":%q,"object":"payment_intent","status":"succeeded","amount":8000,"amount_received":8000,`+
		`"currency":"usd","livemode":true,"merchant_order_id":%q,"metadata":{"new_api_trade_no":%q,`+
		`"new_api_amount_listed":"8000","new_api_amount_charged":"8000","new_api_currency":"USD"}}`, id, tradeNo, tradeNo)
}

func signWooshpayTestPayload(timestamp string, payload string) string {
	mac := hmac.New(sha256.New, []byte(wooshpayTestWebhookSecret))
	mac.Write([]byte(timestamp + "." + payload))
	return "t=" + timestamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func postWooshpayTestWebhook(t *testing.T, payload string, signature string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/wooshpay/webhook", strings.NewReader(payload))
	ctx.Request.Header.Set("Signature", signature)
	WooshpayWebhook(ctx)
	return recorder.Code
}

func TestVerifyWooshpaySignature(t *testing.T) {
	const body = `{"id":"evt_1"}`
	// hex(HMAC_SHA256("whsec_yDjRWBtest", "1767225600123." + body)), computed independently.
	const valid = "2b6e1324030aac57c0d9244a0968a8742e712281c820a2b532ba823112fb9237"

	cases := []struct {
		name    string
		payload string
		header  string
		secret  string
		ok      bool
	}{
		{"valid signature with whsec_ key and millisecond t", body, "t=1767225600123,v1=" + valid, wooshpayTestWebhookSecret, true},
		{"one of several v1 values matches", body, "t=1767225600123,v1=deadbeef,v1=" + valid, wooshpayTestWebhookSecret, true},
		{"spaces after commas", body, "t=1767225600123, v1=" + valid, wooshpayTestWebhookSecret, true},
		{"old timestamp is still accepted", `{"id":"evt_old"}`, signWooshpayTestPayload("1000", `{"id":"evt_old"}`), wooshpayTestWebhookSecret, true},
		{"tampered body", `{"id":"evt_2"}`, "t=1767225600123,v1=" + valid, wooshpayTestWebhookSecret, false},
		{"different t", body, "t=1767225600124,v1=" + valid, wooshpayTestWebhookSecret, false},
		{"t reformatted is a different salt", body, "t=01767225600123,v1=" + valid, wooshpayTestWebhookSecret, false},
		{"secret without whsec_ prefix", body, "t=1767225600123,v1=" + valid, "yDjRWBtest", false},
		{"missing t", body, "v1=" + valid, wooshpayTestWebhookSecret, false},
		{"missing v1", body, "t=1767225600123", wooshpayTestWebhookSecret, false},
		{"empty header", body, "", wooshpayTestWebhookSecret, false},
		{"empty secret", body, "t=1767225600123,v1=" + valid, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyWooshpaySignature([]byte(tc.payload), tc.header, tc.secret)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestWooshpayUnitAmountMinorUnits(t *testing.T) {
	cases := []struct {
		name     string
		currency string
		money    string
		want     int64
		err      string
	}{
		{"USD has two decimals", "USD", "8", 800, ""},
		{"fractional cents round half away from zero", "USD", "8.005", 801, ""},
		{"JPY has no decimals", "JPY", "800", 800, ""},
		{"KWD has three decimals", "KWD", "1.234", 1234, ""},
		{"largest int32 amount", "JPY", "2147483647", 2147483647, ""},
		{"above int32", "JPY", "2147483648", 0, "Payment amount is too high"},
		{"rounds to zero", "USD", "0.004", 0, "Payment amount is too low"},
		{"unknown currency", "XYZ", "8", 0, "Payment currency is not supported"},
		{"lowercase code is not configured", "usd", "8", 0, "Payment currency is not supported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := wooshpayMinorUnits(tc.currency, decimal.RequireFromString(tc.money))
			if tc.err != "" {
				require.EqualError(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// The Checkout Session is created before the order, carries the amounts the
// webhook later checks, and a gateway failure leaves no dangling order. The
// order stores the purchased units and the money actually charged.
func TestRequestWooshpayPayCreatesSessionBeforeOrder(t *testing.T) {
	var sessionBody map[string]any
	var idempotencyKey, authorization string
	fail := true
	setupWooshpayTest(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/checkout/sessions", r.URL.Path)
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, common.Unmarshal(raw, &sessionBody))
		idempotencyKey, authorization = r.Header.Get("Idempotency-Key"), r.Header.Get("Authorization")
		if fail {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"amount_too_small","message":"no"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"cs_test_1","url":"https://checkouttest.wooshpay.com/cs_test_1","status":"open","payment_intent":"pi_1"}`))
	})
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{2: 0.75}
	userID := createRebateTestUser(t, 0, common.RoleCommonUser)

	pay := func() map[string]any {
		return postJSON(t, RequestWooshpayPay, http.MethodPost, "/api/user/wooshpay/pay", `{"amount":2,"payment_method":"wooshpay"}`, userID)
	}
	out := pay()
	assert.Equal(t, "error", out["message"])
	var count int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
	assert.Zero(t, count, "no order without a Checkout Session")

	fail = false
	out = pay()
	require.Equal(t, "success", out["message"], out)
	assert.Equal(t, map[string]any{"pay_link": "https://checkouttest.wooshpay.com/cs_test_1"}, out["data"])

	tradeNo := sessionBody["client_reference_id"].(string)
	assert.True(t, strings.HasPrefix(tradeNo, "wsp_"))
	assert.Equal(t, tradeNo, idempotencyKey)
	assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("sk_live_wooshpay:")), authorization)
	expiresAt := int64(sessionBody["expires_at"].(float64))
	assert.InDelta(t, time.Now().Add(2*time.Hour).Unix(), expiresAt, 60)
	lineItem := sessionBody["line_items"].([]any)[0].(map[string]any)
	priceData := lineItem["price_data"].(map[string]any)
	assert.Equal(t, "USD", priceData["currency"])
	assert.EqualValues(t, 1200, priceData["unit_amount"], "2 units × 8 USD with the 0.75 preset discount")
	metadata := map[string]any{
		"new_api_trade_no": tradeNo, "new_api_amount_listed": "1600",
		"new_api_amount_charged": "1200", "new_api_currency": "USD",
	}
	assert.Equal(t, metadata, sessionBody["metadata"])
	intentData := sessionBody["payment_intent_data"].(map[string]any)
	assert.Equal(t, metadata, intentData["metadata"])
	assert.Equal(t, tradeNo, intentData["merchant_order_id"])

	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
	assert.Equal(t, model.PaymentProviderWooshpay, order.PaymentProvider)
	assert.Equal(t, int64(2), order.Amount)
	assert.InDelta(t, 12.0, order.Money, 1e-9)
}

// With the Tokens display the amount is entered in tokens. It is bounded and
// priced by the whole units it buys, the top-up group ratio scales only the
// price, and the order credits exactly the requested tokens.
func TestWooshpayTokensDisplayOrderCreditsRequestedTokens(t *testing.T) {
	oldTopupGroupRatio := common.TopupGroupRatio2JSONString()
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"vip":2}`))
	t.Cleanup(func() { require.NoError(t, common.UpdateTopupGroupRatioByJSONString(oldTopupGroupRatio)) })
	var sessionBody map[string]any
	setupWooshpayTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/checkout/sessions" {
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, common.Unmarshal(raw, &sessionBody))
			_, _ = w.Write([]byte(`{"id":"cs_tokens","url":"https://checkout.wooshpay.com/cs_tokens","status":"open","payment_intent":"pi_tokens"}`))
			return
		}
		tradeNo := sessionBody["client_reference_id"].(string)
		intent := strings.ReplaceAll(wooshpayTestIntent("pi_tokens", tradeNo), "8000", "1600")
		_, _ = w.Write([]byte(intent))
	})
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeTokens
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{1_000_000: 0.5}
	userID := createRebateTestUser(t, 0, common.RoleCommonUser)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Update("group", "vip").Error)

	assert.Error(t, checkWooshpayTopUpAmount(499_999), "below one unit of tokens")
	assert.NoError(t, checkWooshpayTopUpAmount(wooshpayMaxTopUpUnits*500_000))
	assert.Error(t, checkWooshpayTopUpAmount((wooshpayMaxTopUpUnits+1)*500_000), "the maximum applies to purchased units")

	_, payMoney := getWooshpayPayMoney(1_000_000, "vip")
	assert.Equal(t, "16", payMoney.String(), "2 units × 8 USD × group ratio 2 × discount 0.5")
	out := postJSON(t, RequestWooshpayPay, http.MethodPost, "/api/user/wooshpay/pay", `{"amount":1000000,"payment_method":"wooshpay"}`, userID)
	require.Equal(t, "success", out["message"], out)

	tradeNo := sessionBody["client_reference_id"].(string)
	priceData := sessionBody["line_items"].([]any)[0].(map[string]any)["price_data"].(map[string]any)
	assert.EqualValues(t, 1600, priceData["unit_amount"])
	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, int64(2), order.Amount, "the order stores purchased units")
	assert.InDelta(t, 16.0, order.Money, 1e-9)

	require.NoError(t, wooshpayFulfill(t.Context(), []byte(`{"id":"pi_tokens"}`), "127.0.0.1"))
	assert.Equal(t, common.TopUpStatusSuccess, model.GetTopUpByTradeNo(tradeNo).Status)
	quota, err := model.GetUserQuota(userID, true)
	require.NoError(t, err)
	assert.Equal(t, 1_000_000, quota, "credits the requested tokens, not scaled by the group ratio")
	var topupLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, model.LogTypeTopup).First(&topupLog).Error)
	assert.Contains(t, topupLog.Content, "16.00 USD", "the log shows the amount actually charged")
}

func TestWooshpayFulfillRejectsMismatchedIntent(t *testing.T) {
	const tradeNo = "wsp_mismatch_order"
	intents := map[string]string{
		"pi_processing":   strings.Replace(wooshpayTestIntent("pi_processing", tradeNo), `"succeeded"`, `"processing"`, 1),
		"pi_other_order":  strings.Replace(wooshpayTestIntent("pi_other_order", tradeNo), `"merchant_order_id":"`+tradeNo, `"merchant_order_id":"wsp_other`, 1),
		"pi_underpaid":    strings.Replace(wooshpayTestIntent("pi_underpaid", tradeNo), `"amount_received":8000`, `"amount_received":7999`, 1),
		"pi_currency":     strings.Replace(wooshpayTestIntent("pi_currency", tradeNo), `"currency":"usd"`, `"currency":"eur"`, 1),
		"pi_test_mode":    strings.Replace(wooshpayTestIntent("pi_test_mode", tradeNo), `"livemode":true`, `"livemode":false`, 1),
		"pi_no_metadata":  fmt.Sprintf(`{"id":"pi_no_metadata","status":"succeeded","amount_received":8000,"currency":"usd","livemode":true,"merchant_order_id":%q}`, tradeNo),
		"pi_answers_else": wooshpayTestIntent("pi_someone_else", tradeNo),
	}
	setupWooshpayTest(t, func(w http.ResponseWriter, r *http.Request) {
		body, ok := intents[strings.TrimPrefix(r.URL.Path, "/v1/payment_intents/")]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	userID := createRebateTestUser(t, 0, common.RoleCommonUser)
	insertWooshpayTestOrder(t, userID, tradeNo)

	for id := range intents {
		err := wooshpayFulfill(t.Context(), []byte(`{"id":"`+id+`","status":"succeeded","metadata":{"new_api_trade_no":"`+tradeNo+`"}}`), "127.0.0.1")
		assert.NoError(t, err, id)
	}
	assert.NoError(t, wooshpayFulfill(t.Context(), []byte(`{"id":"cs_not_an_intent"}`), "127.0.0.1"))
	assert.ErrorIs(t, wooshpayFulfill(t.Context(), []byte(`{"id":"pi_api_down"}`), "127.0.0.1"), errWooshpayRetry,
		"a failed re-query must be redelivered")

	assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(tradeNo).Status)
	quota, err := model.GetUserQuota(userID, true)
	require.NoError(t, err)
	assert.Zero(t, quota)
}

func TestWooshpayFulfillIsIdempotent(t *testing.T) {
	const paid, expired, canceled = "wsp_paid_order", "wsp_expired_order", "wsp_canceled_order"
	apiDown := false
	setupWooshpayTest(t, func(w http.ResponseWriter, r *http.Request) {
		if apiDown {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		switch r.URL.Path {
		case "/v1/payment_intents/pi_paid":
			_, _ = w.Write([]byte(wooshpayTestIntent("pi_paid", paid)))
		case "/v1/payment_intents/pi_late":
			_, _ = w.Write([]byte(wooshpayTestIntent("pi_late", expired)))
		default:
			t.Errorf("unexpected WooshPay API call %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	userID := createRebateTestUser(t, 0, common.RoleCommonUser)
	for _, tradeNo := range []string{paid, expired, canceled} {
		insertWooshpayTestOrder(t, userID, tradeNo)
	}
	deliver := func(eventType string, object string) int {
		payload := `{"id":"evt_1","type":"` + eventType + `","livemode":true,"data":{"object":` + object + `}}`
		return postWooshpayTestWebhook(t, payload, signWooshpayTestPayload("1767225600123", payload))
	}
	userQuota := func() int {
		quota, err := model.GetUserQuota(userID, true)
		require.NoError(t, err)
		return quota
	}
	succeeded := `{"id":"pi_paid","object":"payment_intent"}`

	apiDown = true
	assert.Equal(t, http.StatusInternalServerError, deliver("payment_intent.succeeded", succeeded))
	apiDown = false
	assert.Equal(t, http.StatusBadRequest, postWooshpayTestWebhook(t, `{"type":"payment_intent.succeeded"}`, "t=1,v1=00"))

	for range 2 {
		assert.Equal(t, http.StatusOK, deliver("payment_intent.succeeded", succeeded))
	}
	assert.Equal(t, common.TopUpStatusSuccess, model.GetTopUpByTradeNo(paid).Status)
	assert.Equal(t, 5_000_000, userQuota(), "credited once: Amount 10 × QuotaPerUnit")
	var snapshot model.RebateTopupPaid
	require.NoError(t, model.DB.Where("trade_no = ?", paid).First(&snapshot).Error)
	assert.Equal(t, int64(5_000_000), snapshot.PaidQuota, "Money 80 ÷ unit price 8 × QuotaPerUnit")

	// Only the session expiry closes an order; a canceled or failed
	// PaymentIntent leaves it pending.
	assert.Equal(t, http.StatusOK, deliver("checkout.session.expired", `{"id":"cs_1","object":"checkout.session","metadata":{"new_api_trade_no":"`+expired+`"}}`))
	assert.Equal(t, http.StatusOK, deliver("payment_intent.canceled", `{"id":"pi_c","metadata":{"new_api_trade_no":"`+canceled+`"}}`))
	assert.Equal(t, http.StatusOK, deliver("payment_intent.payment_failed", `{"id":"pi_x","metadata":{"new_api_trade_no":"`+canceled+`"}}`))
	assert.Equal(t, http.StatusOK, deliver("checkout.session.expired", `{"id":"cs_2","object":"checkout.session","metadata":{"new_api_trade_no":"`+paid+`"}}`))
	assert.Equal(t, common.TopUpStatusExpired, model.GetTopUpByTradeNo(expired).Status)
	assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(canceled).Status)
	assert.Equal(t, common.TopUpStatusSuccess, model.GetTopUpByTradeNo(paid).Status)

	// A verified payment that arrives after the session expired is still
	// credited, once.
	for range 2 {
		assert.Equal(t, http.StatusOK, deliver("payment_intent.succeeded", `{"id":"pi_late","object":"payment_intent"}`))
	}
	assert.Equal(t, common.TopUpStatusSuccess, model.GetTopUpByTradeNo(expired).Status)
	assert.Equal(t, 10_000_000, userQuota())
}

// Settings that would produce wrong checkout amounts, or send the secret key
// over plain HTTP, are refused when saved.
func TestWooshpayOptionsRejectInvalidValues(t *testing.T) {
	rejected := map[string][]string{
		"WooshpayCurrency":  {"", "usd", "US", "USDT", "XYZ"},
		"WooshpayUnitPrice": {"", "0", "-1", "NaN", "Inf", "abc"},
		"WooshpayMinTopUp":  {"", "0", "-1", "1.5"},
		"WooshpayApiBase":   {"", "http://api.wooshpay.com", "api.wooshpay.com"},
	}
	for key, values := range rejected {
		for _, value := range values {
			assert.Error(t, model.UpdateOption(key, value), "%s=%q", key, value)
		}
	}
	accepted := map[string]string{
		"WooshpayCurrency": "JPY", "WooshpayUnitPrice": "7.5", "WooshpayMinTopUp": "1",
		"WooshpayApiBase": "https://apitest.wooshpay.com",
	}
	for key, value := range accepted {
		assert.NoError(t, setting.ValidateWooshpayOption(key, value), "%s=%q", key, value)
	}

	setupRebateControllerTest(t)
	apiBase, optionMap := setting.WooshpayApiBase, common.OptionMap
	t.Cleanup(func() { setting.WooshpayApiBase, common.OptionMap = apiBase, optionMap })
	common.OptionMap = map[string]string{}
	require.NoError(t, model.UpdateOption("WooshpayApiBase", "https://apitest.wooshpay.com/"))
	assert.Equal(t, "https://apitest.wooshpay.com", setting.WooshpayApiBase, "a trailing slash is dropped when saved")
}
