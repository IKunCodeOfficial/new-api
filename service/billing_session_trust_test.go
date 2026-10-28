package service

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func newWalletOnlyRelayInfo(userId int, tokenId int, tokenKey string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserId:         userId,
		TokenId:        tokenId,
		TokenKey:       tokenKey,
		TokenUnlimited: true,
		UserSetting:    dto.UserSetting{BillingPreference: "wallet_only"},
	}
}

// 余额高于信任额度的用户不预扣，预扣估算超过余额也不应拒绝请求。
func TestPreConsumeBillingTrustedUserSkipsBalanceCheck(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

	balance := int(80 * common.QuotaPerUnit)
	preConsume := int(81 * common.QuotaPerUnit)
	require.Greater(t, balance, int(operation_setting.GetQuotaSetting().TrustQuotaUSD*common.QuotaPerUnit))
	seedUser(t, 9101, balance)
	seedToken(t, 9101, 9101, "trust-key", 0)

	info := newWalletOnlyRelayInfo(9101, 9101, "trust-key")
	apiErr := PreConsumeBilling(c, preConsume, info)

	require.Nil(t, apiErr)
	require.NotNil(t, info.Billing)
	assert.Equal(t, 0, info.Billing.GetPreConsumedQuota())
	assert.Equal(t, 0, info.FinalPreConsumedQuota)
	quota, err := model.GetUserQuota(9101, true)
	require.NoError(t, err)
	assert.Equal(t, balance, quota)
}

// 余额未达信任额度的用户仍需预扣，预扣额超过余额时拒绝请求。
func TestPreConsumeBillingUntrustedUserRejectsWhenBalanceInsufficient(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

	balance := int(operation_setting.GetQuotaSetting().TrustQuotaUSD*common.QuotaPerUnit) / 2
	seedUser(t, 9102, balance)
	seedToken(t, 9102, 9102, "untrusted-key", 0)

	info := newWalletOnlyRelayInfo(9102, 9102, "untrusted-key")
	apiErr := PreConsumeBilling(c, balance+1, info)

	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
	assert.Nil(t, info.Billing)
	quota, err := model.GetUserQuota(9102, true)
	require.NoError(t, err)
	assert.Equal(t, balance, quota)
}

func TestPreConsumeBillingTrustedUserRejectsSaturatedReservation(t *testing.T) {
	truncate(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	balance := int(80 * common.QuotaPerUnit)
	seedUser(t, 9103, balance)
	seedToken(t, 9103, 9103, "saturated-trust-key", 0)
	info := newWalletOnlyRelayInfo(9103, 9103, "saturated-trust-key")
	apiErr := PreConsumeBilling(c, common.MaxQuota, info)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
	assert.Nil(t, info.Billing)
	quota, err := model.GetUserQuota(9103, true)
	require.NoError(t, err)
	assert.Equal(t, balance, quota)
}

func TestPreConsumeBillingRejectsInflatedCachedWallet(t *testing.T) {
	previousDB := model.DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	db := previousDB
	if dsn := os.Getenv("TEST_FIXED_MYSQL_DSN"); dsn != "" {
		var err error
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
		var database string
		require.NoError(t, db.Raw("SELECT DATABASE()").Scan(&database).Error)
		require.True(t, strings.HasPrefix(database, "rebate_"), "requires a dedicated rebate_* test database")
		require.NoError(t, db.AutoMigrate(&model.User{}))
		common.SetDatabaseTypes(common.DatabaseTypeMySQL, previousLogType)
	}
	tx := db.Begin()
	require.NoError(t, tx.Error)
	model.DB = tx
	t.Cleanup(func() {
		assert.NoError(t, tx.Rollback().Error)
		model.DB = previousDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
	})

	var redisOptions *redis.Options
	if url := os.Getenv("REBATE_TEST_REDIS_URL"); url != "" {
		var err error
		redisOptions, err = redis.ParseURL(url)
		require.NoError(t, err)
		require.Positive(t, redisOptions.DB, "requires a dedicated nonzero Redis test database")
	} else {
		server := miniredis.RunT(t)
		redisOptions = &redis.Options{Addr: server.Addr()}
	}
	client := redis.NewClient(redisOptions)
	ctx := context.Background()
	require.NoError(t, client.Ping(ctx).Err())
	previousRedisEnabled, previousRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = true, client
	previousQPU, previousTrust := common.QuotaPerUnit, operation_setting.GetQuotaSetting().TrustQuotaUSD
	common.QuotaPerUnit, operation_setting.GetQuotaSetting().TrustQuotaUSD = 500000, 10
	const userID = 9104
	cacheKey := fmt.Sprintf("user:%d", userID)
	cacheKeys := []string{cacheKey, fmt.Sprintf("auth:user:fence:%d", userID), fmt.Sprintf("auth:user:version:%d", userID)}
	t.Cleanup(func() {
		assert.NoError(t, client.Del(ctx, cacheKeys...).Err())
		assert.NoError(t, client.Close())
		common.RedisEnabled, common.RDB = previousRedisEnabled, previousRDB
		common.QuotaPerUnit, operation_setting.GetQuotaSetting().TrustQuotaUSD = previousQPU, previousTrust
	})
	require.NoError(t, client.Del(ctx, cacheKeys...).Err())
	seedUser(t, userID, 1)
	_, err := model.GetUserCache(userID)
	require.NoError(t, err)
	// Reproduce a valid user hash inflated by a late cache credit after hydration.
	require.NoError(t, client.HSet(ctx, cacheKey, "Quota", 10000000).Err())
	cached, err := model.GetUserCache(userID)
	require.NoError(t, err)
	require.Equal(t, 10000000, cached.Quota)
	info := newWalletOnlyRelayInfo(userID, 0, "")
	info.UserQuota = 10000000
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

	apiErr := PreConsumeBilling(c, 2, info)

	require.NotNil(t, apiErr, "a cached wallet above the trust threshold must not authorize spending unavailable database funds")
	assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
	assert.Nil(t, info.Billing)
	quota, err := model.GetUserQuota(userID, true)
	require.NoError(t, err)
	assert.Equal(t, 1, quota)
}

// An image request that re-reserves on retry reserves the delta through
// WalletFunding.PreConsume. A failed request must refund the original
// pre-consume and every delta, or the first reservation is silently kept.
func TestWalletRefundReturnsEveryImageReservation(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)

	const userID = 9150
	seedUser(t, userID, 1000)
	seedToken(t, userID, userID, "image-refund-key", 0)
	info := newWalletOnlyRelayInfo(userID, userID, "image-refund-key")
	info.Request = &dto.ImageRequest{}

	require.Nil(t, PreConsumeBilling(c, 100, info))
	require.NoError(t, info.Billing.Reserve(400))
	quota, err := model.GetUserQuota(userID, true)
	require.NoError(t, err)
	require.Equal(t, 600, quota)

	info.Billing.Refund(c)
	assert.Eventually(t, func() bool {
		quota, err = model.GetUserQuota(userID, true)
		return err == nil && quota == 1000
	}, 3*time.Second, 20*time.Millisecond)
	assert.Equal(t, 1000, quota, "refund must return the pre-consume and the re-reservation")
}

// A refunded session must not be settled afterwards (it would charge only
// actual - preConsumed), and shutdown must be able to wait for the async
// refund instead of dropping it.
func TestRefundedSessionIsNotSettledAndShutdownWaitsForRefund(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	const userID = 9170
	seedUser(t, userID, 1000)
	seedToken(t, userID, userID, "refund-then-settle-key", 0)
	info := newWalletOnlyRelayInfo(userID, userID, "refund-then-settle-key")
	require.Nil(t, PreConsumeBilling(c, 100, info))

	info.Billing.Refund(c)
	require.True(t, WaitPendingRefunds(3*time.Second))
	quota, err := model.GetUserQuota(userID, true)
	require.NoError(t, err)
	require.Equal(t, 1000, quota)

	assert.Equal(t, 0, settleForConsumeLog(c, info, 300))
	assert.Equal(t, 300, info.UnsettledQuota)
	quota, err = model.GetUserQuota(userID, true)
	require.NoError(t, err)
	assert.Equal(t, 1000, quota, "settling after the refund must not charge the difference")
}

// A realtime session charges every turn as it happens (PreWssConsumeQuota)
// and settles the whole session at the end. The session must be billed once
// in total, matching its consume log: a ratio-priced turn is charged as it
// happens and subtracted from the final settlement; a per-request priced model
// is billed by the final settlement only; a tiered-expression model is charged
// each turn the growth of its session price, so its balance is checked as it
// runs and audio is priced as audio; a subscription-funded session is not
// refused because the wallet is empty; and a turn whose wallet charge
// committed before its token update failed is not charged to the wallet again.
func TestRealtimeSessionIsBilledOnce(t *testing.T) {
	previousRatios := ratio_setting.ModelRatio2JSONString()
	previousPrices := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"realtime-ratio-model":2}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"realtime-price-model":0.5}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices))
	})
	// Initialize dialect-specific column quoting (tokens.key) as startup does.
	t.Setenv("LOG_SQL_DSN", "")
	previousMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, model.InitLogDB())

	const startQuota = 10_000_000
	const longContext = `len <= 20000 ? tier("short", p * 2) : tier("long", p * 4)`
	cases := []struct {
		name      string
		modelName string
		usePrice  bool
		// expression, when set, prices the model with a billing expression.
		expression   string
		subscription bool
		// Each turn has 15000 input tokens, audioIn of them audio, unless
		// usage gives the single turn explicitly.
		turns, audioIn int
		usage          *dto.RealtimeUsage
		walletQuota    int // 0 means startQuota
		// refused means the wallet cannot cover a turn, so its per-turn
		// charge fails and the final settlement bills it.
		refused bool
		// tokenUpdateFails makes the per-turn token decrement fail after the
		// wallet charge has committed.
		tokenUpdateFails bool
		// cost is what the session is billed in total; turnCharged is the part
		// charged turn by turn before the final settlement.
		cost, turnCharged int
	}{
		// 15000 text tokens at model ratio 2 and group ratio 1.
		{name: "ratio priced", modelName: "realtime-ratio-model", cost: 30_000, turnCharged: 30_000},
		// $0.5 per request at 500000 quota per unit.
		{name: "per-request priced", modelName: "realtime-price-model", usePrice: true, cost: 250_000},
		// $2 per 1M prompt tokens: 15000 tokens cost $0.03.
		{name: "tiered expression", modelName: "realtime-tiered-model", expression: `tier("base", p * 2)`, cost: 15_000, turnCharged: 15_000},
		// 10000 text tokens at $2 plus 5000 audio tokens at $8 per 1M.
		{name: "tiered expression prices audio", modelName: "realtime-tiered-model", expression: `tier("base", p * 2 + ai * 8)`, audioIn: 5000, cost: 30_000, turnCharged: 30_000},
		// The session's 30000 input tokens reach the long tier: $4 per 1M.
		{name: "tiered expression over two turns", modelName: "realtime-tiered-model", expression: longContext, turns: 2, cost: 60_000, turnCharged: 60_000},
		// gpt-realtime prices. Of 10000 input tokens (2000 text, 8000 audio)
		// 9000 are cached (1900 text, 7100 audio); 2000 output (1800 audio):
		// 100*4 + 9000*0.4 + 900*32 + 200*16 + 1800*64 = 151200 micro-dollars.
		{name: "tiered expression prices cached audio and audio output once", modelName: "realtime-tiered-model",
			expression: `tier("base", p * 4 + cr * 0.4 + ai * 32 + c * 16 + ao * 64)`, usage: &dto.RealtimeUsage{
				TotalTokens: 12000, InputTokens: 10000, OutputTokens: 2000,
				InputTokenDetails: dto.InputTokenDetails{TextTokens: 2000, AudioTokens: 8000, CachedTokens: 9000,
					CachedTokensDetails: &dto.CachedTokenDetails{TextTokens: common.GetPointer(1900), AudioTokens: common.GetPointer(7100)}},
				OutputTokenDetails: dto.OutputTokenDetails{TextTokens: 200, AudioTokens: 1800},
			}, cost: 75_600, turnCharged: 75_600},
		// The same usage without cr: cached tokens keep their modality's price,
		// so all 8000 audio tokens are ai and the 2000 output tokens (no ao) c:
		// 2000*4 + 8000*32 + 2000*16 = 296000 micro-dollars.
		{name: "tiered expression without cr prices cached audio as audio", modelName: "realtime-tiered-model",
			expression: `tier("base", p * 4 + ai * 32 + c * 16)`, usage: &dto.RealtimeUsage{
				TotalTokens: 12000, InputTokens: 10000, OutputTokens: 2000,
				InputTokenDetails: dto.InputTokenDetails{TextTokens: 2000, AudioTokens: 8000, CachedTokens: 9000,
					CachedTokensDetails: &dto.CachedTokenDetails{TextTokens: common.GetPointer(1900), AudioTokens: common.GetPointer(7100)}},
				OutputTokenDetails: dto.OutputTokenDetails{TextTokens: 200, AudioTokens: 1800},
			}, cost: 148_000, turnCharged: 148_000},
		// 200 text and 800 image input tokens, 100 output: 200*4 + 800*5 + 100*16.
		{name: "tiered expression prices image input", modelName: "realtime-tiered-model",
			expression: `tier("base", p * 4 + img * 5 + c * 16)`, usage: &dto.RealtimeUsage{
				TotalTokens: 1100, InputTokens: 1000, OutputTokens: 100,
				InputTokenDetails:  dto.InputTokenDetails{TextTokens: 200, ImageTokens: 800},
				OutputTokenDetails: dto.OutputTokenDetails{TextTokens: 100},
			}, cost: 3200, turnCharged: 3200},
		{name: "tiered expression beyond the wallet", modelName: "realtime-tiered-model", expression: `tier("base", p * 2)`, walletQuota: 2000, refused: true, cost: 15_000},
		// Usage only in the details: the turn is charged by them, and the
		// session's zero total keeps that charge instead of refunding it.
		{name: "usage reported only in details", modelName: "realtime-ratio-model", usage: &dto.RealtimeUsage{
			InputTokenDetails: dto.InputTokenDetails{TextTokens: 15000},
		}, cost: 30_000, turnCharged: 30_000},
		{name: "subscription funded", modelName: "realtime-ratio-model", subscription: true, cost: 30_000, turnCharged: 30_000},
		{name: "token update fails after the wallet charge", modelName: "realtime-ratio-model", tokenUpdateFails: true, cost: 30_000, turnCharged: 30_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/v1/realtime", nil)

			const userID, subscriptionID = 9180, 9181
			walletQuota := startQuota
			if tc.walletQuota > 0 {
				walletQuota = tc.walletQuota
			}
			if tc.subscription {
				walletQuota = 0
				seedSubscription(t, subscriptionID, userID, startQuota, 0)
			}
			seedUser(t, userID, walletQuota)
			seedToken(t, userID, userID, "realtime-once-key", startQuota)
			seedChannel(t, userID)
			info := newWalletOnlyRelayInfo(userID, userID, "realtime-once-key")
			info.TokenKey = "sk-realtime-once-key"
			info.TokenUnlimited = false
			info.OriginModelName = tc.modelName
			info.UsingGroup, info.UserGroup = "default", "default"
			info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: userID}
			info.StartTime = time.Now()
			info.PriceData.GroupRatioInfo.GroupRatio = 1
			info.PriceData.ModelRatio = 2
			if tc.usePrice {
				info.PriceData.UsePrice, info.PriceData.ModelPrice = true, 0.5
			}
			if tc.expression != "" {
				info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ExprString: tc.expression,
					ExprHash: billingexpr.ExprHashString(tc.expression), QuotaPerUnit: common.QuotaPerUnit, GroupRatio: 1}
			}
			if tc.subscription {
				info.BillingSource, info.SubscriptionId = BillingSourceSubscription, subscriptionID
			} else {
				require.Nil(t, PreConsumeBilling(c, 1000, info))
			}

			session := &dto.RealtimeUsage{}
			for range max(tc.turns, 1) {
				turn := &dto.RealtimeUsage{TotalTokens: 15000, InputTokens: 15000}
				turn.InputTokenDetails.TextTokens = 15000 - tc.audioIn
				turn.InputTokenDetails.AudioTokens = tc.audioIn
				if tc.usage != nil {
					turn = tc.usage
				}
				session.TotalTokens += turn.TotalTokens
				session.InputTokens += turn.InputTokens
				session.OutputTokens += turn.OutputTokens
				session.InputTokenDetails.TextTokens += turn.InputTokenDetails.TextTokens
				session.InputTokenDetails.AudioTokens += turn.InputTokenDetails.AudioTokens
				session.InputTokenDetails.ImageTokens += turn.InputTokenDetails.ImageTokens
				session.InputTokenDetails.CachedTokens += turn.InputTokenDetails.CachedTokens
				session.InputTokenDetails.CachedTokensDetails = turn.InputTokenDetails.CachedTokensDetails
				session.OutputTokenDetails.TextTokens += turn.OutputTokenDetails.TextTokens
				session.OutputTokenDetails.AudioTokens += turn.OutputTokenDetails.AudioTokens
				sessionSoFar := *session
				switch {
				case tc.tokenUpdateFails:
					const callback = "realtime_token_update_fails"
					require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
						if tx.Statement.Table == "tokens" {
							_ = tx.AddError(errors.New("token update failed"))
						}
					}))
					require.Error(t, PreWssConsumeQuota(c, info, turn, &sessionSoFar))
					require.NoError(t, model.DB.Callback().Update().Remove(callback))
				case tc.refused:
					require.ErrorContains(t, PreWssConsumeQuota(c, info, turn, &sessionSoFar), "user quota is not enough")
				default:
					require.NoError(t, PreWssConsumeQuota(c, info, turn, &sessionSoFar))
				}
			}
			PostWssConsumeQuota(c, info, tc.modelName, session, nil)

			var logged int
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", userID, model.LogTypeConsume).
				Select("COALESCE(SUM(quota), 0)").Scan(&logged).Error)
			assert.Equal(t, tc.turnCharged, info.WssChargedQuota)
			assert.Equal(t, tc.cost, logged)
			quota, err := model.GetUserQuota(userID, true)
			require.NoError(t, err)
			var token model.Token
			require.NoError(t, model.DB.First(&token, userID).Error)
			wantToken := startQuota - tc.cost
			if tc.tokenUpdateFails {
				// The failed per-turn token decrement is not retried; the
				// final settlement charges the token only the remainder.
				wantToken = startQuota
			}
			assert.Equal(t, wantToken, token.RemainQuota, "the token is charged at most once")
			if !tc.subscription {
				assert.Equal(t, walletQuota-tc.cost, quota, "the wallet moves by the session cost exactly once")
				return
			}
			assert.Zero(t, quota, "a subscription-funded session leaves the wallet alone")
			var subscription model.UserSubscription
			require.NoError(t, model.DB.First(&subscription, subscriptionID).Error)
			assert.Equal(t, int64(tc.cost), subscription.AmountUsed, "the subscription is charged once")
		})
	}
}

// A legacy per-request priced audio model (TTS/STT) is billed its price
// times the group ratio once, whatever the token counts; without the model
// price the request would be billed zero and its reservation refunded.
func TestPerRequestPricedAudioIsBilledItsPrice(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/audio/speech", nil)
	t.Setenv("LOG_SQL_DSN", "")
	previousMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, model.InitLogDB())

	const userID, startQuota = 9185, 10_000_000
	seedUser(t, userID, startQuota)
	seedToken(t, userID, userID, "audio-price-key", startQuota)
	seedChannel(t, userID)
	info := newWalletOnlyRelayInfo(userID, userID, "audio-price-key")
	info.TokenUnlimited = false
	info.OriginModelName = "audio-price-model"
	info.UsingGroup, info.UserGroup = "default", "default"
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: userID}
	info.StartTime = time.Now()
	info.PriceData.UsePrice, info.PriceData.ModelPrice = true, 0.5
	info.PriceData.GroupRatioInfo.GroupRatio = 1
	// $0.5 at 500000 quota per unit.
	const price = 250_000
	require.Nil(t, PreConsumeBilling(c, price, info))

	usage := &dto.Usage{PromptTokens: 100, TotalTokens: 100}
	usage.PromptTokensDetails.AudioTokens = 100
	PostAudioConsumeQuota(c, info, usage, nil)

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, price, log.Quota)
	quota, err := model.GetUserQuota(userID, true)
	require.NoError(t, err)
	assert.Equal(t, startQuota-price, quota)
	var token model.Token
	require.NoError(t, model.DB.First(&token, userID).Error)
	assert.Equal(t, startQuota-price, token.RemainQuota)
}

// The consume-log writers must use what settlement actually charged. A
// request whose final wallet charge cannot be committed logs the pre-consumed
// amount and keeps the rest under admin_info, on the text, audio and realtime
// writers, and on both the billing-session path and the legacy path without a
// session. When the wallet charge committed and only the token update failed,
// the wallet paid the full cost, so the log records it in full.
func TestConsumeLogRecordsOnlyTheCommittedCharge(t *testing.T) {
	cases := []struct {
		path                        string
		withSession, accountDeleted bool
		tokenUpdateFails            bool
	}{
		{path: "text", withSession: true, accountDeleted: true}, {path: "text", accountDeleted: true}, {path: "text", withSession: true},
		{path: "audio", withSession: true, accountDeleted: true}, {path: "realtime", withSession: true, accountDeleted: true},
		{path: "text", withSession: true, tokenUpdateFails: true}, {path: "text", tokenUpdateFails: true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s,session=%v,deleted=%v,tokenFails=%v", tc.path, tc.withSession, tc.accountDeleted, tc.tokenUpdateFails), func(t *testing.T) {
			truncate(t)
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

			const userID = 9190
			seedUser(t, userID, 1_000_000)
			seedToken(t, userID, userID, "text-settle-fail-key", 0)
			seedChannel(t, userID)
			info := newWalletOnlyRelayInfo(userID, userID, "text-settle-fail-key")
			info.OriginModelName = "settle-fail-model"
			info.UsingGroup = "default"
			info.StartTime = time.Now()
			info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: userID}
			info.PriceData.ModelRatio, info.PriceData.CompletionRatio = 1, 1
			info.PriceData.GroupRatioInfo.GroupRatio = 1
			if tc.path != "text" {
				// $0.004 per request at 500000 quota per unit costs 2000, like
				// the text request's 1000 prompt and 1000 completion tokens.
				info.PriceData.UsePrice, info.PriceData.ModelPrice = true, 0.004
			}
			charged := 0
			if tc.withSession {
				require.Nil(t, PreConsumeBilling(c, 100, info))
				charged = 100
			}
			settle := func() {
				switch tc.path {
				case "audio":
					PostAudioConsumeQuota(c, info, &dto.Usage{PromptTokens: 1000, TotalTokens: 1000}, nil)
				case "realtime":
					PostWssConsumeQuota(c, info, info.OriginModelName, &dto.RealtimeUsage{TotalTokens: 1000, InputTokens: 1000}, nil)
				default:
					PostTextConsumeQuota(c, info, &dto.Usage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000}, nil)
				}
			}

			if !tc.accountDeleted {
				if tc.tokenUpdateFails {
					const callback = "settle_token_update_fails"
					require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
						if tx.Statement.Table == "tokens" {
							_ = tx.AddError(errors.New("token update failed"))
						}
					}))
					t.Cleanup(func() { require.NoError(t, model.DB.Callback().Update().Remove(callback)) })
				}
				settle()
				log := getLastLog(t)
				require.NotNil(t, log)
				assert.Equal(t, 2000, log.Quota, "a committed settlement logs the full cost")
				assert.NotContains(t, log.Other, "unsettled_quota")
				quota, err := model.GetUserQuota(userID, true)
				require.NoError(t, err)
				assert.Equal(t, 1_000_000-2000, quota)
				return
			}
			// The account disappears mid-request, so the difference can no longer be charged.
			require.NoError(t, model.DB.Delete(&model.User{}, userID).Error)
			settle()

			log := getLastLog(t)
			require.NotNil(t, log)
			assert.Equal(t, charged, log.Quota)
			assert.Contains(t, log.Other, fmt.Sprintf(`"unsettled_quota":%d`, 2000-charged))
		})
	}
}
