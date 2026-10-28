package controller

import (
	"fmt"
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
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
	"gorm.io/gorm"
)

func TestTopUpQuotaValidation(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	oldDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	common.QuotaPerUnit = 500000
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplayType
	})

	testCases := []struct {
		name        string
		displayType string
		amount      int64
		wantQuota   int
		wantErr     bool
	}{
		{
			name:        "currency amount below limit",
			displayType: operation_setting.QuotaDisplayTypeUSD,
			amount:      4294,
			wantQuota:   2_147_000_000,
		},
		{
			name:        "currency amount above limit",
			displayType: operation_setting.QuotaDisplayTypeUSD,
			amount:      4295,
			wantQuota:   2_147_500_000,
		},
		{
			name:        "token amount preserves settlement truncation",
			displayType: operation_setting.QuotaDisplayTypeTokens,
			amount:      2_147_500_000,
			wantQuota:   2_147_500_000,
		},
		{
			name:        "token amount above legacy int32 range",
			displayType: operation_setting.QuotaDisplayTypeTokens,
			amount:      4_294_500_000,
			wantQuota:   4_294_500_000,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.GetGeneralSetting().QuotaDisplayType = tc.displayType
			quota, err := getTopUpQuota(tc.amount)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantQuota, quota)
		})
	}
}

func TestValidateTopUpQuotaReturnsMaximumAmount(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	oldDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	common.QuotaPerUnit = 500000
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplayType
	})

	maxAmount := decimal.NewFromInt(common.MaxWalletQuota).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		Floor().IntPart()

	_, err := validateTopUpQuota(maxAmount)
	require.NoError(t, err)
	_, err = validateTopUpQuota(maxAmount + 1)
	require.EqualError(t, err, fmt.Sprintf("Top-up amount cannot exceed %d", maxAmount))
}

func TestRequestAmountRejectsTopUpThatCannotBeSettled(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	oldDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	common.QuotaPerUnit = 500000
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplayType
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	maxAmount := decimal.NewFromInt(common.MaxWalletQuota).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		Floor().IntPart()
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/user/amount",
		strings.NewReader(fmt.Sprintf(`{"amount":%d}`, maxAmount+1)),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	RequestAmount(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, fmt.Sprintf(`{"message":"error","data":"Top-up amount cannot exceed %d","message_key":"Top-up amount cannot exceed {{max}}","message_params":{"max":%d}}`, maxAmount, maxAmount), recorder.Body.String())
}

func TestRequestAmountRejectsTopUpThatWouldOverflowWallet(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	oldDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	oldDB := model.DB
	common.QuotaPerUnit = 500000
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	model.DB = db
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplayType
		model.DB = oldDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	require.NoError(t, model.DB.Create(&model.User{
		Id:       42,
		Username: "topup_capacity_user",
		Quota:    common.MaxWalletQuota - 100_000,
		Status:   common.UserStatusEnabled,
	}).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 42)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/user/amount",
		strings.NewReader(`{"amount":1}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	RequestAmount(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{"message":"error","data":"Top-up quota limit exceeded","message_key":"Top-up quota limit exceeded"}`, recorder.Body.String())
}

func TestValidateCreditedQuotaRejectsOverflow(t *testing.T) {
	_, err := validateCreditedQuota(decimal.NewFromInt(int64(common.MaxWalletQuota / 2)))
	require.NoError(t, err)
	_, err = validateCreditedQuota(decimal.Zero)
	require.EqualError(t, err, "Top-up quota must be greater than 0")
	_, err = validateCreditedQuota(decimal.NewFromInt(common.MaxWalletQuota + 1))
	require.EqualError(
		t,
		err,
		"Top-up quota exceeds the range the system can represent",
	)
}

func TestStripeCreditedQuotaIncludesGroupRatio(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	oldTopupGroupRatio := common.TopupGroupRatio2JSONString()
	common.QuotaPerUnit = 500000
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"vip":2}`))
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(oldTopupGroupRatio))
	})

	_, err := validateCreditedQuota(getStripeCreditedQuota(2147, "vip"))
	require.NoError(t, err)
	_, err = validateCreditedQuota(getStripeCreditedQuota(2148, "vip"))
	require.NoError(t, err)
	_, err = validateCreditedQuota(getStripeCreditedQuota(int64(common.MaxWalletQuota), "vip"))
	require.Error(t, err)

	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"free":0}`))
	assert.True(t, decimal.NewFromInt(500000).Equal(getStripeCreditedQuota(1, "free")))
}

// The paid snapshot must use the amount sent to the gateway, including its
// currency rounding. Invalid Waffo keys stop checkout after the local insert;
// no external payment service is contacted by these cases.
func TestTopUpPaidSnapshotMatchesGatewayAmount(t *testing.T) {
	setupRebateControllerTest(t)
	// Initialize dialect-specific column quoting as normal application startup does.
	t.Setenv("LOG_SQL_DSN", "")
	oldMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = oldMaster })
	require.NoError(t, model.InitLogDB())
	userID := createRebateTestUser(t, 0, common.RoleCommonUser)
	oldPayment := *operation_setting.GetPaymentSetting()
	oldDisplay := operation_setting.GetGeneralSetting().QuotaDisplayType
	oldPrice, oldMin, oldQPU := operation_setting.Price, operation_setting.MinTopUp, common.QuotaPerUnit
	oldAddress, oldID, oldKey := operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey
	oldMethods := operation_setting.PayMethods
	oldRatios := common.TopupGroupRatio2JSONString()
	oldWaffoEnabled, oldWaffoSandbox := setting.WaffoEnabled, setting.WaffoSandbox
	oldWaffoKey, oldWaffoPrivate := setting.WaffoSandboxApiKey, setting.WaffoSandboxPrivateKey
	oldWaffoLiveKey, oldWaffoLivePrivate := setting.WaffoApiKey, setting.WaffoPrivateKey
	oldWaffoPrice, oldWaffoMin, oldCurrency := setting.WaffoUnitPrice, setting.WaffoMinTopUp, setting.WaffoCurrency
	oldPancakeMerchant, oldPancakePrivate, oldProduct := setting.WaffoPancakeMerchantID, setting.WaffoPancakePrivateKey, setting.WaffoPancakeProductID
	oldPancakePrice, oldPancakeMin := setting.WaffoPancakeUnitPrice, setting.WaffoPancakeMinTopUp
	t.Cleanup(func() {
		*operation_setting.GetPaymentSetting() = oldPayment
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplay
		operation_setting.Price, operation_setting.MinTopUp, common.QuotaPerUnit = oldPrice, oldMin, oldQPU
		operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = oldAddress, oldID, oldKey
		operation_setting.PayMethods = oldMethods
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(oldRatios))
		setting.WaffoEnabled, setting.WaffoSandbox = oldWaffoEnabled, oldWaffoSandbox
		setting.WaffoSandboxApiKey, setting.WaffoSandboxPrivateKey = oldWaffoKey, oldWaffoPrivate
		setting.WaffoApiKey, setting.WaffoPrivateKey = oldWaffoLiveKey, oldWaffoLivePrivate
		setting.WaffoUnitPrice, setting.WaffoMinTopUp, setting.WaffoCurrency = oldWaffoPrice, oldWaffoMin, oldCurrency
		setting.WaffoPancakeMerchantID, setting.WaffoPancakePrivateKey, setting.WaffoPancakeProductID = oldPancakeMerchant, oldPancakePrivate, oldProduct
		setting.WaffoPancakeUnitPrice, setting.WaffoPancakeMinTopUp = oldPancakePrice, oldPancakeMin
	})
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	operation_setting.Price, operation_setting.MinTopUp, common.QuotaPerUnit = 1, 1, 500000
	operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = "http://127.0.0.1", "1001", "test-key"
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{}
	operation_setting.GetPaymentSetting().ComplianceConfirmed = true
	operation_setting.GetPaymentSetting().ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	// Invalid keys make the Waffo SDK fail before any network call in both
	// environments; the live slot is used so the snapshot keeps the real price.
	setting.WaffoEnabled, setting.WaffoSandbox = true, false
	setting.WaffoSandboxApiKey, setting.WaffoSandboxPrivateKey = "test-key", "invalid-test-private-key"
	setting.WaffoApiKey, setting.WaffoPrivateKey = "test-key", "invalid-test-private-key"
	setting.WaffoUnitPrice, setting.WaffoMinTopUp = 1, 1
	setting.WaffoPancakeMerchantID, setting.WaffoPancakePrivateKey, setting.WaffoPancakeProductID = "test-merchant", "invalid-test-private-key", "test-product"
	setting.WaffoPancakeUnitPrice, setting.WaffoPancakeMinTopUp = 1, 1
	cases := []struct {
		name     string
		handler  gin.HandlerFunc
		ratio    float64
		currency string
		money    float64
		paid     int64
		rejected bool
		sandbox  bool
	}{
		{"epay rounds down", RequestEpay, .3333, "USD", .33, 165000, false, false},
		{"epay rounds up", RequestEpay, .6666, "USD", .67, 335000, false, false},
		{"epay half cent", RequestEpay, .125, "USD", .12, 60000, false, false},
		{"waffo rounds down", RequestWaffoPay, .3333, "USD", .33, 165000, false, false},
		{"waffo rounds up", RequestWaffoPay, .6666, "USD", .67, 335000, false, false},
		{"waffo half cent", RequestWaffoPay, .125, "USD", .12, 60000, false, false},
		{"waffo zero decimal currency", RequestWaffoPay, 1.6, "JPY", 2, 1000000, false, false},
		{"waffo rounded to zero", RequestWaffoPay, .4, "JPY", 0, 0, true, false},
		// Sandbox orders move no real money, so they must not count toward rebates.
		{"waffo sandbox pays nothing", RequestWaffoPay, 1, "USD", 1, 0, false, true},
		{"pancake rounds down", RequestWaffoPancakePay, .3333, "USD", .33, 165000, false, false},
		{"pancake rounds up", RequestWaffoPancakePay, .6666, "USD", .67, 335000, false, false},
		{"pancake half cent", RequestWaffoPancakePay, .125, "USD", .13, 65000, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, common.UpdateTopupGroupRatioByJSONString(fmt.Sprintf(`{"default":%v}`, tc.ratio)))
			setting.WaffoCurrency = tc.currency
			setting.WaffoSandbox = tc.sandbox
			var before int64
			require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&before).Error)
			response := postJSON(t, tc.handler, http.MethodPost, "/", `{"amount":1,"payment_method":"alipay"}`, userID)
			var after int64
			require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&after).Error)
			if tc.rejected {
				assert.Equal(t, "error", response["message"])
				assert.Equal(t, before, after, "zero-charge orders must not be created")
				return
			}
			require.Equal(t, before+1, after, response)
			var order model.TopUp
			require.NoError(t, model.DB.Order("id desc").First(&order).Error)
			assert.Equal(t, tc.money, order.Money)
			var paid model.RebateTopupPaid
			require.NoError(t, model.DB.Where("trade_no = ?", order.TradeNo).First(&paid).Error)
			assert.Equal(t, tc.paid, paid.PaidQuota)
			if strings.HasPrefix(tc.name, "epay") {
				require.Equal(t, "success", response["message"])
				assert.Equal(t, fmt.Sprintf("%.2f", tc.money), response["data"].(map[string]any)["money"])
			}
		})
	}
}

// A correctly signed epay notification must still cover the order amount the
// gateway was asked to collect (two decimals, as RequestEpay formats it). Less
// is never credited, so neither the wallet nor the rebate paid snapshot can
// count money that was not received. Orders stored before the gateway amount
// was rounded must still match what was actually sent.
func TestEpayNotifyRejectsUnderpayment(t *testing.T) {
	setupRebateControllerTest(t)
	confirmPaymentComplianceForTest(t)
	t.Setenv("LOG_SQL_DSN", "")
	oldMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = oldMaster })
	require.NoError(t, model.InitLogDB())
	oldAddress, oldID, oldKey, oldMethods := operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey, operation_setting.PayMethods
	oldQPU := common.QuotaPerUnit
	t.Cleanup(func() {
		operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey, operation_setting.PayMethods = oldAddress, oldID, oldKey, oldMethods
		common.QuotaPerUnit = oldQPU
	})
	operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = "https://pay.example.com", "epay_id", "test-key"
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}
	common.QuotaPerUnit = 500000
	userID := createRebateTestUser(t, 0, common.RoleCommonUser)
	router := gin.New()
	router.GET("/api/user/epay/notify", EpayNotify)
	notify := func(tradeNo, money string) string {
		params, err := epay.GenerateParamsWithError(map[string]string{
			"pid": "epay_id", "type": "alipay", "money": money, "name": "TUC1",
			"out_trade_no": tradeNo, "trade_no": "E" + tradeNo, "trade_status": epay.StatusTradeSuccess,
		}, "test-key")
		require.NoError(t, err)
		query := url.Values{}
		for k, v := range params {
			query.Set(k, v)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/user/epay/notify?"+query.Encode(), nil))
		return recorder.Body.String()
	}
	cases := []struct {
		tradeNo string
		money   float64
		low     string
		exact   string
	}{
		{"USR-epay-underpaid", 1.00, "0.99", "1.00"},
		// A legacy order kept the unrounded price; the gateway was sent "6.93".
		{"USR-epay-legacy", 6.935, "6.92", "6.93"},
	}
	for _, tc := range cases {
		require.NoError(t, model.DB.Create(&model.TopUp{
			UserId: userID, Amount: 1, Money: tc.money, TradeNo: tc.tradeNo, PaymentMethod: "alipay",
			PaymentProvider: model.PaymentProviderEpay, Status: common.TopUpStatusPending,
		}).Error)
		before, err := model.GetUserQuota(userID, true)
		require.NoError(t, err)

		assert.Equal(t, "fail", notify(tc.tradeNo, tc.low), tc.tradeNo)
		order := model.GetTopUpByTradeNo(tc.tradeNo)
		require.NotNil(t, order)
		assert.Equal(t, common.TopUpStatusPending, order.Status)
		quota, err := model.GetUserQuota(userID, true)
		require.NoError(t, err)
		assert.Equal(t, before, quota, "an underpaid notification credits nothing")

		assert.Equal(t, "success", notify(tc.tradeNo, tc.exact), tc.tradeNo)
		assert.Equal(t, "success", notify(tc.tradeNo, tc.exact), "a replay is acknowledged")
		quota, err = model.GetUserQuota(userID, true)
		require.NoError(t, err)
		assert.Equal(t, before+500000, quota, "the order is credited exactly once")
	}
}

// Stripe reports amounts in minor units. GetObjectValue renders 1,000,000 and
// above as "1e+06", which silently dropped the promotion-code discount from the
// rebate paid snapshot on large orders; the typed session keeps them exact.
func TestStripeGatewayChargeReadsLargeAmounts(t *testing.T) {
	live := func(raw string) stripe.Event {
		return stripe.Event{Livemode: true, Data: &stripe.EventData{Raw: []byte(raw)}}
	}
	cases := []struct {
		name  string
		event stripe.Event
		want  model.GatewayCharge
	}{
		{"large discounted order", live(`{"object":"checkout.session","amount_subtotal":1460000,"amount_total":1168000}`),
			model.GatewayCharge{Listed: 1460000, Charged: 1168000}},
		{"small order without discount", live(`{"object":"checkout.session","amount_subtotal":1000,"amount_total":1000}`),
			model.GatewayCharge{Listed: 1000, Charged: 1000}},
		{"test mode", stripe.Event{Livemode: false, Data: &stripe.EventData{Raw: []byte(`{"amount_subtotal":1000,"amount_total":1000}`)}},
			model.GatewayCharge{Test: true}},
		{"unreadable session counts nothing", live(`not json`), model.GatewayCharge{Listed: 1, Charged: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, stripeGatewayCharge(t.Context(), tc.event))
		})
	}
	// The value the old code parsed shows the regression this guards against.
	legacy := live(`{"amount_subtotal":1460000}`)
	require.NoError(t, common.Unmarshal(legacy.Data.Raw, &legacy.Data.Object))
	assert.Equal(t, "1.46e+06", legacy.GetObjectValue("amount_subtotal"))
}

// client_reference_id can be set by a buyer on any Payment Link of the same
// Stripe account, so a cheap payment there could complete an expensive order.
// Only a session carrying this server's order metadata may complete, expire
// or fail an order.
func TestStripeFulfillmentRequiresTheSessionCreatedForTheOrder(t *testing.T) {
	setupRebateControllerTest(t)
	confirmPaymentComplianceForTest(t)
	oldQPU := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = oldQPU })
	userID := createRebateTestUser(t, 0, common.RoleCommonUser)
	const tradeNo = "ref_stripe_bound_order"
	require.NoError(t, model.DB.Create(&model.TopUp{
		UserId: userID, Amount: 10, Money: 10, TradeNo: tradeNo, PaymentMethod: model.PaymentMethodStripe,
		PaymentProvider: model.PaymentProviderStripe, Status: common.TopUpStatusPending,
	}).Error)
	session := func(status, metadata string) stripe.Event {
		raw := `{"object":"checkout.session","client_reference_id":"` + tradeNo + `","status":"` + status +
			`","payment_status":"paid","amount_subtotal":1000,"amount_total":1000` + metadata + `}`
		event := stripe.Event{ID: "evt_test", Livemode: true, Data: &stripe.EventData{Raw: []byte(raw)}}
		require.NoError(t, common.Unmarshal(event.Data.Raw, &event.Data.Object))
		return event
	}

	for name, metadata := range map[string]string{
		"payment link without metadata": "",
		"session of another order":      `,"metadata":{"new_api_trade_no":"ref_other"}`,
	} {
		sessionCompleted(t.Context(), session("complete", metadata), "127.0.0.1")
		order := model.GetTopUpByTradeNo(tradeNo)
		require.NotNil(t, order)
		assert.Equal(t, common.TopUpStatusPending, order.Status, name)
		sessionExpired(t.Context(), session("expired", metadata))
		assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(tradeNo).Status, name)
		sessionAsyncPaymentFailed(t.Context(), session("complete", metadata), "127.0.0.1")
		assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(tradeNo).Status, name)
	}

	sessionCompleted(t.Context(), session("complete", `,"metadata":{"new_api_trade_no":"`+tradeNo+`"}`), "127.0.0.1")
	assert.Equal(t, common.TopUpStatusSuccess, model.GetTopUpByTradeNo(tradeNo).Status)
	quota, err := model.GetUserQuota(userID, true)
	require.NoError(t, err)
	assert.Equal(t, 5_000_000, quota)

	// What the completed session collected reaches the rebate paid snapshot:
	// a promotion code lowers it by amount_total / amount_subtotal, and a
	// test-mode payment moves no money, so it counts nothing. Both still credit.
	for _, tc := range []struct {
		tradeNo        string
		livemode       bool
		total          int
		wantPaid       int64
		wantCreditedTo int
	}{
		{tradeNo: "ref_stripe_promo", livemode: true, total: 250, wantPaid: 1_250_000, wantCreditedTo: 10_000_000},
		{tradeNo: "ref_stripe_test_mode", total: 1000, wantPaid: 0, wantCreditedTo: 15_000_000},
	} {
		require.NoError(t, model.DB.Create(&model.TopUp{
			UserId: userID, Amount: 10, Money: 10, TradeNo: tc.tradeNo, PaymentMethod: model.PaymentMethodStripe,
			PaymentProvider: model.PaymentProviderStripe, Status: common.TopUpStatusPending,
		}).Error)
		raw := fmt.Sprintf(`{"object":"checkout.session","client_reference_id":%q,"status":"complete","payment_status":"paid",`+
			`"amount_subtotal":1000,"amount_total":%d,"metadata":{"new_api_trade_no":%q}}`, tc.tradeNo, tc.total, tc.tradeNo)
		event := stripe.Event{ID: "evt_" + tc.tradeNo, Livemode: tc.livemode, Data: &stripe.EventData{Raw: []byte(raw)}}
		require.NoError(t, common.Unmarshal(event.Data.Raw, &event.Data.Object))

		sessionCompleted(t.Context(), event, "127.0.0.1")

		assert.Equal(t, common.TopUpStatusSuccess, model.GetTopUpByTradeNo(tc.tradeNo).Status, tc.tradeNo)
		var paid model.RebateTopupPaid
		require.NoError(t, model.DB.Where("trade_no = ?", tc.tradeNo).First(&paid).Error, tc.tradeNo)
		assert.Equal(t, tc.wantPaid, paid.PaidQuota, tc.tradeNo)
		quota, err = model.GetUserQuota(userID, true)
		require.NoError(t, err)
		assert.Equal(t, tc.wantCreditedTo, quota, tc.tradeNo)
	}
}

// The administrator's stated collection sets the rebate paid snapshot of a
// hand-completed order, so it must be a non-negative amount of money in the
// currency the gateway charged.
func TestAdminCompleteTopUpValidatesPaidMoney(t *testing.T) {
	setupRebateControllerTest(t)
	oldQPU, oldStripePrice := common.QuotaPerUnit, setting.StripeUnitPrice
	common.QuotaPerUnit, setting.StripeUnitPrice = 500000, 8
	t.Cleanup(func() { common.QuotaPerUnit, setting.StripeUnitPrice = oldQPU, oldStripePrice })
	userID := createRebateTestUser(t, 0, common.RoleCommonUser)
	order := &model.TopUp{
		UserId: userID, Amount: 10, Money: 10, TradeNo: "ref_admin_complete", PaymentMethod: model.PaymentMethodStripe,
		PaymentProvider: model.PaymentProviderStripe, Status: common.TopUpStatusPending,
	}
	require.NoError(t, order.Insert())

	for _, body := range []string{
		`{"trade_no":"ref_admin_complete","paid_money":-1}`,
		`{"trade_no":"ref_admin_complete","paid_money":"40"}`,
	} {
		res := postJSON(t, AdminCompleteTopUp, http.MethodPost, "/api/user/topup/complete", body, 1)
		assert.Equal(t, false, res["success"], body)
		assert.Equal(t, "Invalid parameters", res["message"], body)
		assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo("ref_admin_complete").Status, body)
	}

	// Checkout lists 10 units at 8; collecting 40 halves the 10-unit snapshot.
	res := postJSON(t, AdminCompleteTopUp, http.MethodPost, "/api/user/topup/complete", `{"trade_no":"ref_admin_complete","paid_money":40}`, 1)
	require.Equal(t, true, res["success"], res["message"])
	var paid model.RebateTopupPaid
	require.NoError(t, model.DB.Where("trade_no = ?", "ref_admin_complete").First(&paid).Error)
	assert.Equal(t, int64(2_500_000), paid.PaidQuota)
}
