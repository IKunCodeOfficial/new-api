package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
	"gorm.io/gorm"
)

// WooshPay metadata keys. They are written by this server with its secret key
// and read back from the API, so the webhook can trust them. The listed
// (undiscounted) amount is only recorded for reconciliation.
const (
	wooshpayTradeNoMetadataKey       = "new_api_trade_no"
	wooshpayAmountListedMetadataKey  = "new_api_amount_listed"
	wooshpayAmountChargedMetadataKey = "new_api_amount_charged"
	wooshpayCurrencyMetadataKey      = "new_api_currency"
)

const wooshpayHTTPTimeout = 15 * time.Second

// WooshpayPayRequest is the wallet top-up request for WooshPay Checkout.
type WooshpayPayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
	SuccessURL    string `json:"success_url,omitempty"`
	CancelURL     string `json:"cancel_url,omitempty"`
}

type wooshpaySession struct {
	ID            string            `json:"id"`
	URL           string            `json:"url"`
	Status        string            `json:"status"`
	PaymentIntent string            `json:"payment_intent"`
	Livemode      bool              `json:"livemode"`
	Metadata      map[string]string `json:"metadata"`
}

type wooshpayPaymentIntent struct {
	ID              string            `json:"id"`
	Status          string            `json:"status"`
	Amount          int64             `json:"amount"`
	AmountReceived  *int64            `json:"amount_received"`
	Currency        string            `json:"currency"`
	Livemode        *bool             `json:"livemode"`
	MerchantOrderID string            `json:"merchant_order_id"`
	Metadata        map[string]string `json:"metadata"`
}

type wooshpayAPIError struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Param   string `json:"param"`
	} `json:"error"`
}

// errWooshpayRetry marks a webhook failure WooshPay should redeliver.
var errWooshpayRetry = errors.New("wooshpay webhook should be retried")

func RequestWooshpayAmount(c *gin.Context) {
	var req WooshpayPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		paymentError(c, common.NewMessage("Invalid parameters"))
		return
	}
	if err := checkWooshpayTopUpAmount(req.Amount); err != nil {
		paymentError(c, err)
		return
	}
	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		paymentError(c, common.NewMessage("Failed to get the user group"))
		return
	}
	if rejectInvalidCreditedQuota(c, id, getWooshpayCreditedQuota(req.Amount)) {
		return
	}
	_, payMoney := getWooshpayPayMoney(req.Amount, group)
	if _, err := wooshpayMinorUnits(setting.WooshpayCurrency, payMoney); err != nil {
		paymentError(c, err)
		return
	}
	digits := setting.WooshpayCurrencyMinorDigits[setting.WooshpayCurrency]
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": payMoney.StringFixed(digits)})
}

func RequestWooshpayPay(c *gin.Context) {
	var req WooshpayPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		paymentError(c, common.NewMessage("Invalid parameters"))
		return
	}
	if !isWooshpayTopUpEnabled() {
		paymentError(c, common.NewMessage("WooshPay payment is not enabled"))
		return
	}
	if req.PaymentMethod != model.PaymentMethodWooshpay {
		paymentError(c, common.NewMessage("Unsupported payment channel"))
		return
	}
	if err := checkWooshpayTopUpAmount(req.Amount); err != nil {
		paymentError(c, err)
		return
	}
	if req.SuccessURL != "" && common.ValidateRedirectURL(req.SuccessURL) != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Payment success redirect URL is not in the trusted domain list"), gin.H{"data": ""})
		return
	}
	if req.CancelURL != "" && common.ValidateRedirectURL(req.CancelURL) != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Payment cancel redirect URL is not in the trusted domain list"), gin.H{"data": ""})
		return
	}
	successURL := req.SuccessURL
	if successURL == "" {
		successURL = paymentReturnPath("/usage-logs")
	}
	cancelURL := req.CancelURL
	if cancelURL == "" {
		cancelURL = paymentReturnPath("/wallet")
	}

	id := c.GetInt("id")
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		paymentError(c, common.NewMessage("User does not exist"))
		return
	}
	if rejectInvalidCreditedQuota(c, id, getWooshpayCreditedQuota(req.Amount)) {
		return
	}

	currency := setting.WooshpayCurrency
	listedMoney, payMoney := getWooshpayPayMoney(req.Amount, user.Group)
	charged, err := wooshpayMinorUnits(currency, payMoney)
	if err != nil {
		paymentError(c, err)
		return
	}

	// Like epay and Waffo Pancake, the order stores the purchased units and the
	// money actually charged, after currency rounding.
	purchasedUnits := wooshpayPurchasedUnits(req.Amount)
	chargedMoney := decimal.New(charged, -setting.WooshpayCurrencyMinorDigits[currency])

	reference := fmt.Sprintf("new-api-wsp-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	tradeNo := "wsp_" + common.Sha1([]byte(reference))
	metadata := map[string]string{
		wooshpayTradeNoMetadataKey:       tradeNo,
		wooshpayAmountChargedMetadataKey: strconv.FormatInt(charged, 10),
		wooshpayCurrencyMetadataKey:      currency,
	}
	// Only the charged amount is bounded: a preset discount can bring an
	// undiscounted price above the int32 range back into it. The listed amount
	// is only for reconciliation and is left out when it does not fit.
	listedMinor := listedMoney.Shift(setting.WooshpayCurrencyMinorDigits[currency]).Round(0)
	if listedMinor.IsPositive() && listedMinor.LessThanOrEqual(decimal.NewFromInt(math.MaxInt32)) {
		metadata[wooshpayAmountListedMetadataKey] = listedMinor.String()
	}
	params := map[string]any{
		"mode":                "payment",
		"success_url":         successURL,
		"cancel_url":          cancelURL,
		"client_reference_id": tradeNo,
		"line_items": []map[string]any{{
			"price_data": map[string]any{
				"currency":    currency,
				"unit_amount": charged,
				"product_data": map[string]any{
					"name":        "Account top-up",
					"description": fmt.Sprintf("%d x top-up", purchasedUnits),
				},
			},
			"quantity": 1,
		}},
		"payment_intent_data": map[string]any{
			"merchant_order_id": tradeNo,
			"merchant_user_id":  strconv.Itoa(user.Id),
			"metadata":          metadata,
		},
		"metadata": metadata,
	}
	if user.Email != "" {
		params["customer_email"] = user.Email
	}

	ctx := c.Request.Context()
	var session wooshpaySession
	if err := wooshpayRequest(ctx, http.MethodPost, "/v1/checkout/sessions", tradeNo, params, &session); err != nil {
		logger.LogError(ctx, common.LogText("WooshPay failed to create Checkout Session user_id=%d trade_no=%s amount=%d error=%q", id, tradeNo, req.Amount, err.Error()))
		paymentError(c, common.NewMessage("Failed to start payment"))
		return
	}
	if session.URL == "" {
		logger.LogError(ctx, common.LogText("WooshPay Checkout Session has no payment URL user_id=%d trade_no=%s session_id=%s", id, tradeNo, session.ID))
		paymentError(c, common.NewMessage("Failed to start payment"))
		return
	}

	topUp := &model.TopUp{
		UserId:          id,
		Amount:          purchasedUnits,
		Money:           chargedMoney.InexactFloat64(),
		TradeNo:         tradeNo,
		PaymentMethod:   model.PaymentMethodWooshpay,
		PaymentProvider: model.PaymentProviderWooshpay,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := topUp.Insert(); err != nil {
		logger.LogError(ctx, common.LogText("WooshPay Checkout Session created but top-up order insert failed, a payment on it will not be credited and needs manual handling user_id=%d trade_no=%s session_id=%s amount=%d error=%q", id, tradeNo, session.ID, req.Amount, err.Error()))
		paymentError(c, common.NewMessage("Failed to create order"))
		return
	}
	logger.LogInfo(ctx, common.LogText("WooshPay top-up order created user_id=%d trade_no=%s session_id=%s amount=%d units=%d money=%s currency=%s", id, tradeNo, session.ID, req.Amount, purchasedUnits, chargedMoney.String(), currency))
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"pay_link": session.URL,
		},
	})
}

// UpdateWooshpayEnvironment saves the WooshPay secret key together with the
// API base of its environment. Saved one at a time, moving between the test
// and live environments would first pair either value with the other
// environment's and be refused.
func UpdateWooshpayEnvironment(c *gin.Context) {
	var req struct {
		SecretKey string `json:"secret_key"`
		ApiBase   string `json:"api_base"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil || strings.TrimSpace(req.SecretKey) == "" {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if err := model.UpdateOptionsBulk(map[string]string{
		"WooshpaySecretKey": strings.TrimSpace(req.SecretKey),
		"WooshpayApiBase":   strings.TrimSpace(req.ApiBase),
	}); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "option.update", map[string]any{"keys": []string{"WooshpaySecretKey", "WooshpayApiBase"}})
	common.ApiSuccess(c, nil)
}

func WooshpayWebhook(c *gin.Context) {
	// WooshPay may drop the connection before the re-query and the database
	// work finish; they must still complete, so they do not inherit the
	// request's cancellation.
	ctx := context.WithoutCancel(c.Request.Context())
	callerIp := c.ClientIP()
	if !isWooshpayWebhookEnabled() {
		logger.LogWarn(ctx, common.LogText("WooshPay webhook rejected reason=webhook_disabled path=%q client_ip=%s", c.Request.RequestURI, callerIp))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(ctx, common.LogText("WooshPay webhook failed to read request body path=%q client_ip=%s error=%q", c.Request.RequestURI, callerIp, err.Error()))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	if err := verifyWooshpaySignature(payload, c.GetHeader("Signature"), strings.TrimSpace(setting.WooshpayWebhookSecret)); err != nil {
		logger.LogWarn(ctx, common.LogText("WooshPay webhook signature verification failed path=%q client_ip=%s error=%q", c.Request.RequestURI, callerIp, err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	var event struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Livemode bool   `json:"livemode"`
		Data     struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := common.Unmarshal(payload, &event); err != nil {
		logger.LogWarn(ctx, common.LogText("WooshPay webhook body is not a valid event client_ip=%s error=%q", callerIp, err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	logger.LogInfo(ctx, common.LogText("WooshPay webhook signature verified event_id=%s event_type=%s livemode=%t client_ip=%s", event.ID, event.Type, event.Livemode, callerIp))

	switch event.Type {
	case "payment_intent.succeeded":
		err = wooshpayFulfill(ctx, event.Data.Object, callerIp)
	case "checkout.session.expired":
		err = wooshpayExpireOrder(ctx, event.Data.Object)
	case "payment_intent.payment_failed", "payment_intent.canceled":
		// A failed attempt can be retried with another card in the same
		// Checkout Session, and a canceled intent does not prove the session
		// cannot still be paid, so only the session expiry closes the order.
		logger.LogInfo(ctx, common.LogText("WooshPay payment attempt did not succeed, order left pending event_id=%s event_type=%s client_ip=%s", event.ID, event.Type, callerIp))
	default:
		logger.LogInfo(ctx, common.LogText("WooshPay webhook ignored event event_id=%s event_type=%s client_ip=%s", event.ID, event.Type, callerIp))
	}
	if errors.Is(err, errWooshpayRetry) {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusOK)
}

// verifyWooshpaySignature checks the Signature header ("t=<ms>,v1=<hex>"),
// which signs t + "." + body with HMAC-SHA256 keyed by the whole webhook
// secret (including its whsec_ prefix). t is only a salt: it is used as the
// raw header string and is not checked against the clock. Replays are
// harmless because fulfillment re-queries the API and only completes pending
// orders.
func verifyWooshpaySignature(payload []byte, header string, secret string) error {
	if secret == "" {
		return errors.New("webhook secret is not configured")
	}
	var timestamp string
	var signatures []string
	for part := range strings.SplitSeq(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			timestamp = value
		case "v1":
			signatures = append(signatures, value)
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return errors.New("signature header is missing t or v1")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := []byte(hex.EncodeToString(mac.Sum(nil)))
	for _, signature := range signatures {
		if hmac.Equal(expected, []byte(signature)) {
			return nil
		}
	}
	return errors.New("no matching v1 signature")
}

// wooshpayFulfill credits the order of a payment_intent.succeeded event. The
// event only names the PaymentIntent; status, order, amount, currency and mode
// are all taken from the API. It returns errWooshpayRetry only when a
// redelivery could succeed.
func wooshpayFulfill(ctx context.Context, object json.RawMessage, callerIp string) error {
	var eventIntent struct {
		ID string `json:"id"`
	}
	if err := common.Unmarshal(object, &eventIntent); err != nil || !strings.HasPrefix(eventIntent.ID, "pi_") {
		logger.LogWarn(ctx, common.LogText("WooshPay succeeded event has no PaymentIntent id, ignored client_ip=%s", callerIp))
		return nil
	}

	var intent wooshpayPaymentIntent
	if err := wooshpayRequest(ctx, http.MethodGet, "/v1/payment_intents/"+url.PathEscape(eventIntent.ID), "", nil, &intent); err != nil {
		logger.LogError(ctx, common.LogText("WooshPay failed to retrieve PaymentIntent payment_intent=%s client_ip=%s error=%q", eventIntent.ID, callerIp, err.Error()))
		return errWooshpayRetry
	}

	tradeNo := intent.Metadata[wooshpayTradeNoMetadataKey]
	if tradeNo == "" {
		logger.LogWarn(ctx, common.LogText("WooshPay PaymentIntent was not created by this site, ignored payment_intent=%s status=%s client_ip=%s", eventIntent.ID, intent.Status, callerIp))
		return nil
	}
	// A PaymentIntent that is not conclusively paid yet (still processing,
	// or missing the received amount or mode) is redelivered until it is.
	if intent.Status != "succeeded" || intent.AmountReceived == nil || intent.Livemode == nil {
		logger.LogWarn(ctx, common.LogText("WooshPay PaymentIntent is not conclusively paid yet, retry requested payment_intent=%s trade_no=%s status=%s client_ip=%s", eventIntent.ID, tradeNo, intent.Status, callerIp))
		return errWooshpayRetry
	}
	amountReceived, livemode := *intent.AmountReceived, *intent.Livemode
	expectedCharged, chargedErr := strconv.ParseInt(intent.Metadata[wooshpayAmountChargedMetadataKey], 10, 64)
	mismatch := ""
	switch {
	case intent.ID != eventIntent.ID:
		mismatch = "payment_intent_id"
	case tradeNo != intent.MerchantOrderID:
		mismatch = "trade_no"
	case !strings.EqualFold(intent.Currency, intent.Metadata[wooshpayCurrencyMetadataKey]):
		mismatch = "currency"
	case chargedErr != nil || amountReceived != expectedCharged:
		mismatch = "amount"
	case livemode == setting.IsWooshpayTestKey():
		mismatch = "livemode"
	}
	if mismatch != "" {
		logger.LogError(ctx, common.LogText("WooshPay PaymentIntent succeeded but does not match the order, paid but not credited, needs manual handling payment_intent=%s trade_no=%s mismatch=%s client_ip=%s", eventIntent.ID, tradeNo, mismatch, callerIp))
		return nil
	}

	// The checkout amount was priced by this server and the received amount
	// equals it, so the order's paid snapshot already holds what was paid;
	// only a test-mode payment changes it.
	charge := model.GatewayCharge{Listed: expectedCharged, Charged: amountReceived, Test: !livemode}
	currency := strings.ToUpper(intent.Currency)
	digits := setting.WooshpayCurrencyMinorDigits[currency]
	paymentAmount := decimal.New(amountReceived, -digits).StringFixed(digits) + " " + currency

	LockOrder(tradeNo)
	defer UnlockOrder(tradeNo)
	err := model.RechargeWooshpay(tradeNo, callerIp, paymentAmount, charge)
	switch {
	case err == nil:
		logger.LogInfo(ctx, common.LogText("WooshPay top-up succeeded trade_no=%s payment_intent=%s amount_received=%s client_ip=%s", tradeNo, intent.ID, paymentAmount, callerIp))
		return nil
	case errors.Is(err, model.ErrTopUpStatusInvalid):
		logger.LogInfo(ctx, common.LogText("WooshPay order already completed, duplicate notification ignored trade_no=%s payment_intent=%s client_ip=%s", tradeNo, intent.ID, callerIp))
		return nil
	case errors.Is(err, model.ErrTopUpNotFound), errors.Is(err, model.ErrPaymentMethodMismatch), errors.Is(err, gorm.ErrRecordNotFound):
		// gorm.ErrRecordNotFound here means the order's user no longer
		// exists; a redelivery cannot change that.
		logger.LogError(ctx, common.LogText("WooshPay order cannot be completed, paid but not credited, needs manual handling trade_no=%s payment_intent=%s client_ip=%s error=%q", tradeNo, intent.ID, callerIp, err.Error()))
		return nil
	case errors.Is(err, model.ErrInvalidTopUpQuota), errors.Is(err, model.ErrTopUpQuotaLimitExceeded):
		logger.LogError(ctx, common.LogText("WooshPay paid order needs manual completion trade_no=%s payment_intent=%s client_ip=%s error=%q", tradeNo, intent.ID, callerIp, err.Error()))
		return nil
	default:
		logger.LogError(ctx, common.LogText("WooshPay top-up processing failed trade_no=%s payment_intent=%s client_ip=%s error=%q", tradeNo, intent.ID, callerIp, err.Error()))
		return errWooshpayRetry
	}
}

// wooshpayExpireOrder marks the pending order named in an expired Checkout
// Session's metadata as expired. A payment that still arrives later completes
// the order anyway (see model.RechargeWooshpay). It returns errWooshpayRetry
// only for database failures.
func wooshpayExpireOrder(ctx context.Context, object json.RawMessage) error {
	var session wooshpaySession
	if err := common.Unmarshal(object, &session); err != nil || session.Metadata[wooshpayTradeNoMetadataKey] == "" {
		logger.LogWarn(ctx, common.LogText("WooshPay expired session has no order number, ignored session_id=%s", session.ID))
		return nil
	}
	tradeNo := session.Metadata[wooshpayTradeNoMetadataKey]

	LockOrder(tradeNo)
	defer UnlockOrder(tradeNo)
	err := model.UpdatePendingTopUpStatus(tradeNo, model.PaymentProviderWooshpay, common.TopUpStatusExpired)
	switch {
	case err == nil:
		logger.LogInfo(ctx, common.LogText("WooshPay top-up order expired trade_no=%s session_id=%s", tradeNo, session.ID))
		return nil
	case errors.Is(err, model.ErrTopUpStatusInvalid):
		logger.LogInfo(ctx, common.LogText("WooshPay order is no longer pending, expiry ignored trade_no=%s session_id=%s", tradeNo, session.ID))
		return nil
	case errors.Is(err, model.ErrTopUpNotFound), errors.Is(err, model.ErrPaymentMethodMismatch):
		logger.LogWarn(ctx, common.LogText("WooshPay order cannot be expired trade_no=%s session_id=%s error=%q", tradeNo, session.ID, err.Error()))
		return nil
	default:
		logger.LogError(ctx, common.LogText("WooshPay failed to expire top-up order trade_no=%s session_id=%s error=%q", tradeNo, session.ID, err.Error()))
		return errWooshpayRetry
	}
}

// wooshpayRequest calls the WooshPay API with the configured secret key and
// decodes a 2xx JSON response into out. Error responses are reported by
// type and code only.
func wooshpayRequest(ctx context.Context, method string, path string, idempotencyKey string, body any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, wooshpayHTTPTimeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		data, err := common.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	endpoint := setting.NormalizeWooshpayApiBase(setting.WooshpayApiBase) + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth(strings.TrimSpace(setting.WooshpaySecretKey), "")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := service.GetHttpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var apiErr wooshpayAPIError
		_ = common.Unmarshal(respBody, &apiErr)
		return fmt.Errorf("wooshpay api status=%d type=%s code=%s", resp.StatusCode, apiErr.Error.Type, apiErr.Error.Code)
	}
	return common.Unmarshal(respBody, out)
}

// checkWooshpayTopUpAmount bounds the requested amount, which is in display
// units (tokens with the Tokens quota display), by the purchased units it buys.
func checkWooshpayTopUpAmount(amount int64) error {
	tokensDisplay := operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens
	quotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
	// Tokens buy whole units only; a remainder would be paid for nothing.
	if tokensDisplay && quotaPerUnit.IsPositive() && !decimal.NewFromInt(amount).Mod(quotaPerUnit).IsZero() {
		return common.NewMessage("With the Tokens display the top-up amount must be a multiple of {{unit}} tokens", map[string]any{"unit": quotaPerUnit.String()})
	}
	units := wooshpayPurchasedUnits(amount)
	if amount < getWooshpayMinTopup() || units < int64(setting.WooshpayMinTopUp) {
		return common.NewMessage("Top-up amount cannot be less than {{min}}", map[string]any{"min": getWooshpayMinTopup()})
	}
	if units > setting.WooshpayMaxTopUpUnits {
		maxAmount := decimal.NewFromInt(setting.WooshpayMaxTopUpUnits)
		if tokensDisplay {
			maxAmount = maxAmount.Mul(quotaPerUnit).Floor()
		}
		return common.NewMessage("Top-up amount cannot exceed {{max}}", map[string]any{"max": maxAmount.IntPart()})
	}
	return nil
}

// wooshpayPurchasedUnits converts the requested amount into the whole
// currency units the order buys and credits. With the Tokens quota display
// the amount is in tokens; checkWooshpayTopUpAmount only accepts whole units.
func wooshpayPurchasedUnits(amount int64) int64 {
	if operation_setting.GetQuotaDisplayType() != operation_setting.QuotaDisplayTypeTokens {
		return amount
	}
	return decimal.NewFromInt(amount).Div(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart()
}

// getWooshpayCreditedQuota is the quota an order credits: the purchased units
// at QuotaPerUnit. The top-up group ratio only scales the price.
func getWooshpayCreditedQuota(amount int64) decimal.Decimal {
	return decimal.NewFromInt(wooshpayPurchasedUnits(amount)).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
}

// getWooshpayPayMoney returns the list price and the price after the preset
// amount discount, both in WooshpayCurrency major units. As with epay, the
// top-up group ratio scales the price and the discount is keyed by the
// requested amount.
func getWooshpayPayMoney(amount int64, group string) (listed decimal.Decimal, charged decimal.Decimal) {
	topUpGroupRatio := common.GetTopupGroupRatio(group)
	if topUpGroupRatio == 0 {
		topUpGroupRatio = 1
	}
	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(amount)]; ok && ds > 0 {
		discount = ds
	}
	listed = decimal.NewFromInt(wooshpayPurchasedUnits(amount)).
		Mul(decimal.NewFromFloat(setting.WooshpayUnitPrice)).
		Mul(decimal.NewFromFloat(topUpGroupRatio))
	return listed, listed.Mul(decimal.NewFromFloat(discount))
}

// wooshpayMinorUnits converts money in major units of currency into the
// minor unit WooshPay amounts use, bounded to a positive int32.
func wooshpayMinorUnits(currency string, money decimal.Decimal) (int64, error) {
	digits, ok := setting.WooshpayCurrencyMinorDigits[currency]
	if !ok {
		return 0, common.NewMessage("Payment currency is not supported")
	}
	minor := money.Shift(digits).Round(0)
	if !minor.IsPositive() {
		return 0, common.NewMessage("Payment amount is too low")
	}
	if minor.GreaterThan(decimal.NewFromInt(math.MaxInt32)) {
		return 0, common.NewMessage("Payment amount is too high")
	}
	return minor.IntPart(), nil
}

func getWooshpayMinTopup() int64 {
	minTopup := setting.WooshpayMinTopUp
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		minTopup = minTopup * int(common.QuotaPerUnit)
	}
	return int64(minTopup)
}
