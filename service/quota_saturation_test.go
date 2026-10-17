package service

import (
	"fmt"
	"math"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAttachQuotaSaturationNestsUnderAdminInfo verifies the saturation marker
// is nested under other.admin_info.quota_saturation so it is admin-only (the
// log formatter strips admin_info for non-admin viewers).
func TestAttachQuotaSaturationNestsUnderAdminInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)

	relayInfo := &relaycommon.RelayInfo{
		UserId:          7,
		OriginModelName: "gpt-image-1",
		QuotaClamp: &common.QuotaClamp{
			Op:       "QuotaFromDecimal",
			Kind:     common.QuotaClampOverflow,
			Original: 1.8e19,
			Clamped:  common.MaxQuota,
		},
	}

	other := model.NewLogOther()
	other.SetPublic("model_price", 0.004)
	attachQuotaSaturation(ctx, relayInfo, other)

	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok, "admin_info should be created")
	sat, ok := adminInfo["quota_saturation"].(map[string]any)
	require.True(t, ok, "quota_saturation should be nested under admin_info")
	require.Equal(t, "QuotaFromDecimal", sat["op"])
	require.Equal(t, common.QuotaClampOverflow, sat["kind"])
	require.Equal(t, common.MaxQuota, sat["clamped"])
}

func TestCalcViolationFeeQuotaSaturates(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	require.Equal(t, common.MaxQuota, calcViolationFeeQuota(1e20, 1))
}

func TestCalcOpenRouterCacheCreateTokensDoesNotWrap(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	got := CalcOpenRouterCacheCreateTokens(dto.Usage{Cost: math.Inf(1)}, hosttypes.PriceData{
		ModelRatio:         1,
		CacheCreationRatio: 2,
		CacheRatio:         1,
		CompletionRatio:    1,
	})
	require.Equal(t, -1, got)
}

// TestAttachQuotaSaturationPreservesExistingAdminInfo verifies the marker is
// merged into a pre-existing admin_info map without clobbering it.
func TestAttachQuotaSaturationPreservesExistingAdminInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)

	relayInfo := &relaycommon.RelayInfo{
		QuotaClamp: &common.QuotaClamp{Op: "QuotaFromFloat", Kind: common.QuotaClampUnderflow, Clamped: common.MinQuota},
	}
	other := model.NewLogOther()
	other.SetAdmin("admin_username", "root")
	attachQuotaSaturation(ctx, relayInfo, other)

	adminInfo := other.Snapshot()["admin_info"].(map[string]any)
	require.Equal(t, "root", adminInfo["admin_username"], "existing admin_info fields preserved")
	require.NotNil(t, adminInfo["quota_saturation"])
}

// TestAttachQuotaSaturationNoClampNoMarker verifies the common case (no
// saturation) leaves the log untouched.
func TestAttachQuotaSaturationNoClampNoMarker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)

	relayInfo := &relaycommon.RelayInfo{QuotaClamp: nil}
	other := model.NewLogOther()
	other.SetPublic("model_price", 0.004)
	attachQuotaSaturation(ctx, relayInfo, other)

	_, hasAdmin := other.Snapshot()["admin_info"]
	require.False(t, hasAdmin, "no admin_info should be added when there is no clamp")
}

func TestPreConsumeBillingRejectsSaturatedQuotaBeforeDeduction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	info := &relaycommon.RelayInfo{
		QuotaClamp: &common.QuotaClamp{
			Op:       "QuotaFromFloat",
			Kind:     common.QuotaClampOverflow,
			Original: 1e30,
			Clamped:  common.MaxQuota,
		},
	}

	apiErr := PreConsumeBilling(c, common.MaxQuota, info)

	require.NotNil(t, apiErr)
	require.Equal(t, types.ErrorCodeModelPriceError, apiErr.GetErrorCode())
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	require.Same(t, info.QuotaClamp, apiErr.Err)
	var clamp *common.QuotaClamp
	require.ErrorAs(t, apiErr, &clamp)
	require.Same(t, info.QuotaClamp, clamp)
	require.Nil(t, info.Billing)
}

func TestPreConsumeBillingRejectsNegativeQuotaBeforeDeduction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	info := &relaycommon.RelayInfo{}

	apiErr := PreConsumeBilling(c, -1, info)

	require.NotNil(t, apiErr)
	require.Equal(t, types.ErrorCodeModelPriceError, apiErr.GetErrorCode())
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	require.Nil(t, info.Billing)
}

// Reservations and settlement must contribute the final charge exactly once, for
// both finite and unlimited tokens and for wallet/subscription funding.
func TestTokenDailyUsageThroughPreConsumeAndSettlement(t *testing.T) {
	for _, source := range []string{BillingSourceWallet, BillingSourceSubscription} {
		for _, unlimited := range []bool{false, true} {
			for _, actual := range []int{0, 40, 60, 80} {
				t.Run(fmt.Sprintf("%s/unlimited=%v/actual=%d", source, unlimited, actual), func(t *testing.T) {
					truncate(t)
					require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
					t.Setenv("LOG_SQL_DSN", "")
					require.NoError(t, model.InitLogDB())
					seedUser(t, 9401, 1000)
					seedToken(t, 9401, 9401, "daily-billing-token", 1000)
					require.NoError(t, model.SetTokenDailyQuotaLimit(9401, 0))
					require.NoError(t, model.SetTokenDailyQuotaLimit(9401, 500))
					t.Cleanup(func() { require.NoError(t, model.SetTokenDailyQuotaLimit(9401, 0)) })
					info := &relaycommon.RelayInfo{UserId: 9401, TokenId: 9401, TokenKey: "daily-billing-token", TokenUnlimited: unlimited, BillingSource: source}
					if source == BillingSourceSubscription {
						seedSubscription(t, 9401, 9401, 1000, 60)
						info.SubscriptionId = 9401
					}
					require.NoError(t, PreConsumeTokenQuota(info, 60))
					assert.Equal(t, int64(60), model.GetTokenDailyUsage(9401), "successful reservation counts immediately")
					require.NoError(t, PostConsumeQuota(info, actual-60, 60, false))
					assert.Equal(t, int64(actual), model.GetTokenDailyUsage(9401))
					var token model.Token
					require.NoError(t, model.DB.First(&token, 9401).Error)
					assert.Equal(t, actual, token.UsedQuota)
					assert.Equal(t, 1000-actual, token.RemainQuota)
				})
			}
		}
	}
}
