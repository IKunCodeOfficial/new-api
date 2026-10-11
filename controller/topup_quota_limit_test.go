package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.EqualError(t, err, fmt.Sprintf("单笔充值数量不能大于 %d", maxAmount))
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
	assert.JSONEq(t, fmt.Sprintf(`{"message":"error","data":"单笔充值数量不能大于 %d"}`, maxAmount), recorder.Body.String())
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
	assert.JSONEq(t, `{"message":"error","data":"top-up quota limit exceeded"}`, recorder.Body.String())
}

func TestValidateCreditedQuotaRejectsOverflow(t *testing.T) {
	_, err := validateCreditedQuota(decimal.NewFromInt(int64(common.MaxWalletQuota / 2)))
	require.NoError(t, err)
	_, err = validateCreditedQuota(decimal.Zero)
	require.EqualError(t, err, "充值额度必须大于 0")
	_, err = validateCreditedQuota(decimal.NewFromInt(common.MaxWalletQuota + 1))
	require.EqualError(
		t,
		err,
		"充值额度超出系统可表示范围",
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
	setting.WaffoEnabled, setting.WaffoSandbox = true, true
	setting.WaffoSandboxApiKey, setting.WaffoSandboxPrivateKey = "test-key", "invalid-test-private-key"
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
	}{
		{"epay rounds down", RequestEpay, .3333, "USD", .33, 165000, false},
		{"epay rounds up", RequestEpay, .6666, "USD", .67, 335000, false},
		{"epay half cent", RequestEpay, .125, "USD", .12, 60000, false},
		{"waffo rounds down", RequestWaffoPay, .3333, "USD", .33, 165000, false},
		{"waffo rounds up", RequestWaffoPay, .6666, "USD", .67, 335000, false},
		{"waffo half cent", RequestWaffoPay, .125, "USD", .12, 60000, false},
		{"waffo zero decimal currency", RequestWaffoPay, 1.6, "JPY", 2, 1000000, false},
		{"waffo rounded to zero", RequestWaffoPay, .4, "JPY", 0, 0, true},
		{"pancake rounds down", RequestWaffoPancakePay, .3333, "USD", .33, 165000, false},
		{"pancake rounds up", RequestWaffoPancakePay, .6666, "USD", .67, 335000, false},
		{"pancake half cent", RequestWaffoPancakePay, .125, "USD", .13, 65000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, common.UpdateTopupGroupRatioByJSONString(fmt.Sprintf(`{"default":%v}`, tc.ratio)))
			setting.WaffoCurrency = tc.currency
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
