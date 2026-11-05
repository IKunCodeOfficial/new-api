package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
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
// (undiscounted) amount is only recorded for reconciliation. Amounts are in
// fen.
const (
	wooshpayTradeNoMetadataKey       = "new_api_trade_no"
	wooshpayAmountListedMetadataKey  = "new_api_amount_listed"
	wooshpayAmountChargedMetadataKey = "new_api_amount_charged"
)

// wooshpayChargeCurrency is the currency every WooshPay checkout is charged
// in. Its amounts are in fen, 2 decimal places.
const wooshpayChargeCurrency = "CNY"

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

// wooshpayAccount is the secret key and API base one request works with. It
// is read once per request, so a settings change while the request runs
// cannot pair the key of one environment with the other's API base or mode.
type wooshpayAccount struct {
	secretKey string
	apiBase   string
}

func currentWooshpayAccount() wooshpayAccount {
	return wooshpayAccount{
		secretKey: strings.TrimSpace(setting.WooshpaySecretKey),
		apiBase:   setting.NormalizeWooshpayApiBase(setting.WooshpayApiBase),
	}
}

// testMode reports whether the account's key belongs to the WooshPay test
// environment, where payments move no real money.
func (account wooshpayAccount) testMode() bool {
	return setting.IsWooshpayTestSecretKey(account.secretKey)
}

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
	_, payMoney := getWooshpayPayMoney(req.Amount, group, setting.WooshpayUnitPrice)
	charged, err := wooshpayFen(payMoney)
	if err != nil {
		paymentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{
		"amount": decimal.New(charged, -2).StringFixed(2),
	}})
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

	// The price and the account are read once: the checkout, the order and
	// its rebate snapshot must all describe the same charge.
	account := currentWooshpayAccount()
	unitPrice := setting.WooshpayUnitPrice
	listedMoney, payMoney := getWooshpayPayMoney(req.Amount, user.Group, unitPrice)
	charged, err := wooshpayFen(payMoney)
	if err != nil {
		paymentError(c, err)
		return
	}

	// Like epay and Waffo Pancake, the order stores the purchased units and the
	// money actually charged, rounded to the fen.
	purchasedUnits := wooshpayPurchasedUnits(req.Amount)
	chargedMoney := decimal.New(charged, -2)

	reference := fmt.Sprintf("new-api-wsp-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	tradeNo := "wsp_" + common.Sha1([]byte(reference))
	metadata := map[string]string{
		wooshpayTradeNoMetadataKey:       tradeNo,
		wooshpayAmountChargedMetadataKey: strconv.FormatInt(charged, 10),
	}
	// Only the charged amount is bounded: a preset discount can bring an
	// undiscounted price above WooshPay's amount limit back into it. The
	// listed amount is only for reconciliation and is left out when it does
	// not fit.
	listedFen := listedMoney.Shift(2).Round(0)
	if listedFen.IsPositive() && listedFen.LessThanOrEqual(decimal.NewFromInt(setting.WooshpayMaxMinorAmount)) {
		metadata[wooshpayAmountListedMetadataKey] = listedFen.String()
	}
	params := map[string]any{
		"mode":                "payment",
		"success_url":         successURL,
		"cancel_url":          cancelURL,
		"client_reference_id": tradeNo,
		"line_items": []map[string]any{{
			"price_data": map[string]any{
				"currency":    wooshpayChargeCurrency,
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
	if err := wooshpayRequest(ctx, account, http.MethodPost, "/v1/checkout/sessions", tradeNo, params, &session); err != nil {
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
		Pricing:         &model.TopUpPricing{UnitPrice: unitPrice, TestMode: account.testMode()},
	}
	if err := topUp.Insert(); err != nil {
		logger.LogError(ctx, common.LogText("WooshPay Checkout Session created but top-up order insert failed, a payment on it will not be credited and needs manual handling user_id=%d trade_no=%s session_id=%s amount=%d error=%q", id, tradeNo, session.ID, req.Amount, err.Error()))
		paymentError(c, common.NewMessage("Failed to create order"))
		return
	}
	logger.LogInfo(ctx, common.LogText("WooshPay top-up order created user_id=%d trade_no=%s session_id=%s amount=%d units=%d money=%s", id, tradeNo, session.ID, req.Amount, purchasedUnits, chargedMoney.StringFixed(2)))
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"pay_link": session.URL,
		},
	})
}

// UpdateWooshpayEnvironment saves every changed WooshPay setting in one
// request. The resulting combination is validated as a whole and written in
// one transaction, so a refused save changes nothing, and moving between the
// test and live environments never passes through a mismatched pair. Absent
// fields keep their stored values, as do empty secrets.
func UpdateWooshpayEnvironment(c *gin.Context) {
	var req struct {
		Enabled       *bool    `json:"enabled"`
		SecretKey     *string  `json:"secret_key"`
		WebhookSecret *string  `json:"webhook_secret"`
		ApiBase       *string  `json:"api_base"`
		UnitPrice     *float64 `json:"unit_price"`
		MinTopUp      *float64 `json:"min_topup"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	values := map[string]string{}
	if req.Enabled != nil {
		values["WooshpayEnabled"] = strconv.FormatBool(*req.Enabled)
	}
	if req.SecretKey != nil && strings.TrimSpace(*req.SecretKey) != "" {
		values["WooshpaySecretKey"] = strings.TrimSpace(*req.SecretKey)
	}
	if req.WebhookSecret != nil && strings.TrimSpace(*req.WebhookSecret) != "" {
		values["WooshpayWebhookSecret"] = strings.TrimSpace(*req.WebhookSecret)
	}
	if req.ApiBase != nil {
		values["WooshpayApiBase"] = setting.NormalizeWooshpayApiBase(*req.ApiBase)
	}
	if req.UnitPrice != nil {
		values["WooshpayUnitPrice"] = strconv.FormatFloat(*req.UnitPrice, 'f', -1, 64)
	}
	if req.MinTopUp != nil {
		values["WooshpayMinTopUp"] = strconv.FormatFloat(*req.MinTopUp, 'f', -1, 64)
	}
	if len(values) == 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if err := model.UpdateOptionsBulk(values); err != nil {
		common.ApiError(c, err)
		return
	}
	// Only the names of the changed settings are recorded, never the values.
	keys := slices.Sorted(maps.Keys(values))
	recordManageAudit(c, "option.update", map[string]any{"key": strings.Join(keys, ", "), "keys": keys})
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
	// The secrets, the API base and the key mode are read once, so the
	// re-query and the mode check use the account the signature was
	// checked with.
	account := currentWooshpayAccount()
	webhookSecret := strings.TrimSpace(setting.WooshpayWebhookSecret)

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(ctx, common.LogText("WooshPay webhook failed to read request body path=%q client_ip=%s error=%q", c.Request.RequestURI, callerIp, err.Error()))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	if err := verifyWooshpaySignature(payload, c.GetHeader("Signature"), webhookSecret); err != nil {
		logger.LogWarn(ctx, wooshpaySignatureFailureLog(c.Request, payload, webhookSecret, callerIp, err))
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
		err = wooshpayFulfill(ctx, account, event.Data.Object, callerIp)
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

// wooshpaySignatureFailureLog is the warning logged when a webhook fails
// verification. Besides the error it records what the request looked like and
// which candidate signing schemes would have matched, so the scheme WooshPay
// really uses can be learned from the log. It holds the Signature header
// (only t and the MACs) and the secret's length and prefix, never the secret.
func wooshpaySignatureFailureLog(request *http.Request, payload []byte, secret string, callerIp string, verifyErr error) string {
	signature := request.Header.Get("Signature")
	bodySum := sha256.Sum256(payload)
	matches := strings.Join(wooshpaySignatureDiagnosis(payload, signature, secret), ",")
	if matches == "" {
		matches = "none"
	}
	return common.LogText("WooshPay webhook signature verification failed path=%q client_ip=%s error=%q signature=%q headers=%s content_type=%q content_encoding=%q body_length=%d body_sha256=%s body_head=%q secret_has_whsec_prefix=%t secret_length=%d matching_schemes=%s",
		request.RequestURI, callerIp, verifyErr.Error(), signature,
		strings.Join(slices.Sorted(maps.Keys(request.Header)), ","),
		request.Header.Get("Content-Type"), request.Header.Get("Content-Encoding"),
		len(payload), hex.EncodeToString(bodySum[:]), payload[:min(len(payload), 120)],
		strings.HasPrefix(secret, "whsec_"), len(secret), matches)
}

// wooshpaySignatureDiagnosis names the candidate signing schemes, as
// key/message/encoding, that produce one of the header's v1 values. It is only
// logged after verification failed, to learn how WooshPay signs; it never
// accepts a webhook.
func wooshpaySignatureDiagnosis(payload []byte, header string, secret string) []string {
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
	if secret == "" || len(signatures) == 0 {
		return nil
	}

	// A candidate is a key or a signed message under a scheme name.
	type candidate struct {
		name  string
		bytes []byte
	}
	stripped := strings.TrimPrefix(secret, "whsec_")
	keys := []candidate{
		{"secret", []byte(secret)},
		{"stripped_key", []byte(stripped)},
	}
	for _, encoding := range []struct {
		name     string
		encoding *base64.Encoding
	}{
		{"base64_std_key", base64.StdEncoding},
		{"base64_raw_std_key", base64.RawStdEncoding},
		{"base64_url_key", base64.URLEncoding},
		{"base64_raw_url_key", base64.RawURLEncoding},
	} {
		if decoded, err := encoding.encoding.DecodeString(stripped); err == nil {
			keys = append(keys, candidate{encoding.name, decoded})
		}
	}

	messages := []candidate{
		{"t.body", []byte(timestamp + "." + string(payload))},
		{"body", payload},
		{"tbody", []byte(timestamp + string(payload))},
	}
	if milliseconds, err := strconv.ParseInt(timestamp, 10, 64); err == nil {
		messages = append(messages, candidate{"t_seconds.body", []byte(strconv.FormatInt(milliseconds/1000, 10) + "." + string(payload))})
	}

	var matches []string
	for _, key := range keys {
		for _, message := range messages {
			mac := hmac.New(sha256.New, key.bytes)
			mac.Write(message.bytes)
			sum := mac.Sum(nil)
			hexSum := hex.EncodeToString(sum)
			base64Sum := base64.StdEncoding.EncodeToString(sum)
			for _, signature := range signatures {
				name := ""
				switch {
				case strings.EqualFold(signature, hexSum):
					name = key.name + "/" + message.name + "/hex"
				case signature == base64Sum:
					name = key.name + "/" + message.name + "/base64"
				}
				if name != "" && !slices.Contains(matches, name) {
					matches = append(matches, name)
				}
			}
		}
	}
	return matches
}

// wooshpayPaymentProof checks a succeeded PaymentIntent re-queried with
// account against the metadata this server wrote when it created the
// checkout: it must be the PaymentIntent asked for, charged in CNY, and its
// order number, received amount and mode must agree. A succeeded intent without
// amount_received was captured automatically, so its amount was received; one
// without livemode is taken to be in the account's mode. It returns the
// amount received and the mode, or the name of the first check that failed;
// "amount_unknown" means the intent states no positive amount at all.
func wooshpayPaymentProof(ctx context.Context, account wooshpayAccount, intent wooshpayPaymentIntent, requestedID string) (received int64, livemode bool, failed string) {
	tradeNo := intent.Metadata[wooshpayTradeNoMetadataKey]
	if intent.AmountReceived != nil {
		received = *intent.AmountReceived
	} else {
		received = intent.Amount
	}
	if received <= 0 {
		return 0, false, "amount_unknown"
	}
	if intent.Livemode != nil {
		livemode = *intent.Livemode
	} else {
		livemode = !account.testMode()
		logger.LogWarn(ctx, common.LogText("WooshPay PaymentIntent has no livemode, the mode of the secret key is assumed payment_intent=%s trade_no=%s livemode=%t", intent.ID, tradeNo, livemode))
	}
	expectedCharged, chargedErr := strconv.ParseInt(intent.Metadata[wooshpayAmountChargedMetadataKey], 10, 64)
	switch {
	case intent.ID != requestedID:
		return received, livemode, "payment_intent_id"
	case tradeNo != intent.MerchantOrderID:
		return received, livemode, "trade_no"
	case !strings.EqualFold(intent.Currency, wooshpayChargeCurrency):
		return received, livemode, "currency"
	case chargedErr != nil || received != expectedCharged:
		return received, livemode, "amount"
	case livemode == account.testMode():
		return received, livemode, "livemode"
	}
	return received, livemode, ""
}

// wooshpayPaymentAmount is the collected amount in yuan with its currency,
// as the top-up log shows it: "73.00 CNY".
func wooshpayPaymentAmount(receivedFen int64) string {
	return decimal.New(receivedFen, -2).StringFixed(2) + " " + wooshpayChargeCurrency
}

// wooshpayFulfill credits the order of a payment_intent.succeeded event. The
// event only names the PaymentIntent; status, order, amount, currency and mode
// are all taken from the API. It returns errWooshpayRetry only when a
// redelivery could succeed.
func wooshpayFulfill(ctx context.Context, account wooshpayAccount, object json.RawMessage, callerIp string) error {
	var eventIntent struct {
		ID string `json:"id"`
	}
	if err := common.Unmarshal(object, &eventIntent); err != nil || !strings.HasPrefix(eventIntent.ID, "pi_") {
		logger.LogWarn(ctx, common.LogText("WooshPay succeeded event has no PaymentIntent id, ignored client_ip=%s", callerIp))
		return nil
	}

	var intent wooshpayPaymentIntent
	if err := wooshpayRequest(ctx, account, http.MethodGet, "/v1/payment_intents/"+url.PathEscape(eventIntent.ID), "", nil, &intent); err != nil {
		logger.LogError(ctx, common.LogText("WooshPay failed to retrieve PaymentIntent payment_intent=%s client_ip=%s error=%q", eventIntent.ID, callerIp, err.Error()))
		return errWooshpayRetry
	}

	tradeNo := intent.Metadata[wooshpayTradeNoMetadataKey]
	if tradeNo == "" {
		logger.LogWarn(ctx, common.LogText("WooshPay PaymentIntent was not created by this site, ignored payment_intent=%s status=%s client_ip=%s", eventIntent.ID, intent.Status, callerIp))
		return nil
	}
	// A PaymentIntent that is still processing is redelivered until it is
	// conclusively paid.
	if intent.Status != "succeeded" {
		logger.LogWarn(ctx, common.LogText("WooshPay PaymentIntent is not conclusively paid yet, retry requested payment_intent=%s trade_no=%s status=%s client_ip=%s", eventIntent.ID, tradeNo, intent.Status, callerIp))
		return errWooshpayRetry
	}
	received, livemode, failed := wooshpayPaymentProof(ctx, account, intent, eventIntent.ID)
	if failed == "amount_unknown" {
		logger.LogError(ctx, common.LogText("WooshPay PaymentIntent succeeded without an amount, retry requested, needs manual handling if it persists payment_intent=%s trade_no=%s client_ip=%s", eventIntent.ID, tradeNo, callerIp))
		return errWooshpayRetry
	}
	if failed != "" {
		logger.LogError(ctx, common.LogText("WooshPay PaymentIntent succeeded but does not match the order, paid but not credited, needs manual handling payment_intent=%s trade_no=%s mismatch=%s client_ip=%s", eventIntent.ID, tradeNo, failed, callerIp))
		return nil
	}

	// The checkout amount was priced by this server and the received amount
	// equals it, so the order's paid snapshot already holds what was paid;
	// only a test-mode payment changes it.
	charge := model.GatewayCharge{Listed: received, Charged: received, Test: !livemode}
	paymentAmount := wooshpayPaymentAmount(received)

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

// completeWooshpayOrderByIntent completes a WooshPay order on an
// administrator's request once the PaymentIntent they name, re-queried with
// the configured key, proves the order was paid: it succeeded, it carries
// this order's number as metadata and merchant order id, and it received the
// amount this order charged in CNY, in the mode of the key. The order is then completed like a webhook would, exactly once.
// The caller holds the order lock.
func completeWooshpayOrderByIntent(ctx context.Context, order *model.TopUp, intentID string, callerIp string) error {
	intentID = strings.TrimSpace(intentID)
	if !strings.HasPrefix(intentID, "pi_") {
		return common.NewMessage("Enter the ID of the WooshPay PaymentIntent, which starts with pi_")
	}
	if !isWooshpayWebhookEnabled() {
		return common.NewMessage("WooshPay is not configured, so the payment cannot be verified")
	}
	account := currentWooshpayAccount()
	var intent wooshpayPaymentIntent
	if err := wooshpayRequest(ctx, account, http.MethodGet, "/v1/payment_intents/"+url.PathEscape(intentID), "", nil, &intent); err != nil {
		logger.LogError(ctx, common.LogText("WooshPay failed to retrieve PaymentIntent for manual completion payment_intent=%s trade_no=%s error=%q", intentID, order.TradeNo, err.Error()))
		return common.NewMessage("Failed to query the WooshPay PaymentIntent")
	}
	if intent.Status != "succeeded" {
		return common.NewMessage("The WooshPay PaymentIntent has not succeeded")
	}
	received, livemode, failed := wooshpayPaymentProof(ctx, account, intent, intentID)
	switch {
	case failed != "":
	case intent.Metadata[wooshpayTradeNoMetadataKey] != order.TradeNo:
		failed = "trade_no"
	case !decimal.NewFromFloat(order.Money).Shift(2).Round(0).Equal(decimal.NewFromInt(received)):
		failed = "amount"
	}
	if failed != "" {
		logger.LogWarn(ctx, common.LogText("WooshPay PaymentIntent does not prove the payment of the order, manual completion refused payment_intent=%s trade_no=%s mismatch=%s", intentID, order.TradeNo, failed))
		return common.NewMessage("The WooshPay PaymentIntent does not match this order: {{field}}", map[string]any{"field": failed})
	}

	paymentAmount := wooshpayPaymentAmount(received)
	err := model.RechargeWooshpay(order.TradeNo, callerIp, paymentAmount, model.GatewayCharge{Listed: received, Charged: received, Test: !livemode})
	if errors.Is(err, model.ErrTopUpStatusInvalid) {
		// Already completed, by the webhook or an earlier request.
		return nil
	}
	if err != nil {
		return err
	}
	logger.LogInfo(ctx, common.LogText("WooshPay order completed by an administrator with a verified PaymentIntent trade_no=%s payment_intent=%s amount_received=%s", order.TradeNo, intentID, paymentAmount))
	return nil
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

// wooshpayRequest calls the WooshPay API with the account's secret key and
// decodes a 2xx JSON response into out. Error responses are reported by
// type and code only.
func wooshpayRequest(ctx context.Context, account wooshpayAccount, method string, path string, idempotencyKey string, body any, out any) error {
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
	endpoint := account.apiBase + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth(account.secretKey, "")
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
// amount discount, both in yuan. As with epay, the
// top-up group ratio scales the price and the discount is keyed by the
// requested amount.
func getWooshpayPayMoney(amount int64, group string, unitPrice float64) (listed decimal.Decimal, charged decimal.Decimal) {
	topUpGroupRatio := common.GetTopupGroupRatio(group)
	if topUpGroupRatio == 0 {
		topUpGroupRatio = 1
	}
	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(amount)]; ok && ds > 0 {
		discount = ds
	}
	listed = decimal.NewFromInt(wooshpayPurchasedUnits(amount)).
		Mul(decimal.NewFromFloat(unitPrice)).
		Mul(decimal.NewFromFloat(topUpGroupRatio))
	return listed, listed.Mul(decimal.NewFromFloat(discount))
}

// wooshpayFen converts yuan into the fen WooshPay amounts use, bounded to the
// 8 digits WooshPay accepts.
func wooshpayFen(yuan decimal.Decimal) (int64, error) {
	fen := yuan.Shift(2).Round(0)
	if !fen.IsPositive() {
		return 0, common.NewMessage("Payment amount is too low")
	}
	if fen.GreaterThan(decimal.NewFromInt(setting.WooshpayMaxMinorAmount)) {
		return 0, common.NewMessage("Payment amount is too high")
	}
	return fen.IntPart(), nil
}

// getWooshpayMinTopup is the minimum top-up in display units: tokens with the
// Tokens quota display.
func getWooshpayMinTopup() int64 {
	minTopup := setting.WooshpayMinTopUp
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		quota, err := common.WalletQuotaFromDecimalStrict(decimal.NewFromInt(int64(minTopup)).Mul(decimal.NewFromFloat(common.QuotaPerUnit)))
		if err != nil {
			return common.MaxWalletQuota
		}
		minTopup = quota
	}
	return int64(minTopup)
}
