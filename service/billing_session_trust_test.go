package service

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
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
