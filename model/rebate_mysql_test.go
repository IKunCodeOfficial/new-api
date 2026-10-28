package model

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These opt-in tests use a restored MySQL user's real schema and restore the
// user's financial state. The DSN must name a dedicated rebate_* test database.
func setupRebateMySQLTest(t *testing.T) *User {
	t.Helper()
	dsn := os.Getenv("REBATE_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("REBATE_MYSQL_TEST_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	var database string
	require.NoError(t, db.Raw("SELECT DATABASE()").Scan(&database).Error)
	require.True(t, strings.HasPrefix(database, "rebate_"), "requires a dedicated rebate_* test database")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB, previousLogDB := DB, LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	DB, LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeMySQL, common.DatabaseTypeMySQL)
	var user User
	require.NoError(t, DB.Where("status = ?", common.UserStatusEnabled).First(&user).Error)
	original := user
	t.Cleanup(func() {
		assert.NoError(t, db.Model(&User{}).Where("id = ?", original.Id).Updates(map[string]any{
			"quota": original.Quota, "aff_quota": original.AffQuota, "aff_history": original.AffHistoryQuota,
		}).Error)
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		assert.NoError(t, sqlDB.Close())
	})
	return &user
}

func TestRebateMySQLTransferRejectsWalletOverflow(t *testing.T) {
	user := setupRebateMySQLTest(t)
	withRebatePrices(t)
	require.NoError(t, DB.Model(user).Updates(map[string]any{
		"quota": common.MaxWalletQuota - 1, "aff_quota": 500000, "aff_history": 500000,
	}).Error)

	err := user.TransferAffQuotaToQuota(500000)

	assert.ErrorIs(t, err, ErrWalletQuotaLimitExceeded)
	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, common.MaxWalletQuota-1, got.Quota)
	assert.Equal(t, 500000, got.AffQuota)
	assert.Equal(t, 500000, got.AffHistoryQuota)
}

func TestRebateMySQLTransferAtWalletLimitPreservesHistory(t *testing.T) {
	user := setupRebateMySQLTest(t)
	withRebatePrices(t)
	require.NoError(t, DB.Model(user).Updates(map[string]any{
		"quota": common.MaxWalletQuota - 500000, "aff_quota": 600000, "aff_history": 900000,
	}).Error)

	require.NoError(t, user.TransferAffQuotaToQuota(500000))

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, common.MaxWalletQuota, got.Quota)
	assert.Equal(t, 100000, got.AffQuota)
	assert.Equal(t, 900000, got.AffHistoryQuota)
}

func TestRebateMySQLTransferPreservesHistoricalNullFields(t *testing.T) {
	user := setupRebateMySQLTest(t)
	withRebatePrices(t)
	original := map[string]any{}
	require.NoError(t, DB.Table("users").Select("remark,stripe_customer,discord_id,created_at").Where("id = ?", user.Id).Take(&original).Error)
	t.Cleanup(func() {
		assert.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Updates(original).Error)
	})
	require.NoError(t, DB.Model(user).Updates(map[string]any{
		"quota": 100, "aff_quota": 500000, "aff_history": 700000,
		"remark": nil, "stripe_customer": nil, "discord_id": nil, "created_at": nil,
	}).Error)
	before := map[string]any{}
	require.NoError(t, DB.Table("users").Where("id = ?", user.Id).Take(&before).Error)
	for _, column := range []string{"remark", "stripe_customer", "discord_id", "created_at"} {
		require.Nil(t, before[column], "fixture must retain SQL NULL for %s", column)
	}

	require.NoError(t, user.TransferAffQuotaToQuota(500000))

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 500100, got.Quota)
	assert.Zero(t, got.AffQuota)
	assert.Equal(t, 700000, got.AffHistoryQuota)
	after := map[string]any{}
	require.NoError(t, DB.Table("users").Where("id = ?", user.Id).Take(&after).Error)
	for column, value := range before {
		if column == "quota" || column == "aff_quota" {
			continue
		}
		assert.True(t, reflect.DeepEqual(value, after[column]), "unrelated user column %s changed", column)
	}
}

func TestRebateMySQLRechargeLifecycle(t *testing.T) {
	user := setupRebateMySQLTest(t)
	withRebatePrices(t)
	baseDB := DB
	tx := DB.Begin()
	require.NoError(t, tx.Error)
	DB, LOG_DB = tx, tx
	t.Cleanup(func() {
		assert.NoError(t, tx.Rollback().Error)
		DB, LOG_DB = baseDB, baseDB
	})
	initialQuota := user.Quota
	order := &TopUp{
		UserId: user.Id, Amount: 10, Money: 65.7, TradeNo: "rebate-mysql-" + common.GetRandomString(16),
		PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay,
		CreateTime: common.GetTimestamp(), Status: common.TopUpStatusPending,
	}
	require.NoError(t, order.Insert())
	var paid RebateTopupPaid
	require.NoError(t, DB.Where("trade_no = ?", order.TradeNo).First(&paid).Error)
	assert.Equal(t, int64(4500000), paid.PaidQuota)
	_, err := RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", "65.69")
	require.ErrorIs(t, err, ErrPaymentAmountTooLow, "a signed notification for less than the order is not credited")
	alreadyDone, err := RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", "65.70")
	require.NoError(t, err)
	assert.False(t, alreadyDone)
	alreadyDone, err = RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", "65.70")
	require.NoError(t, err)
	assert.True(t, alreadyDone)
	require.NoError(t, DB.First(order, order.Id).Error)
	assert.Positive(t, order.CompleteTime)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)

	_, err = AdjustUserQuota(user.Id, common.RoleRootUser, "add", 1000000, &UserQuotaRecharge{OperatorID: user.Id, PaidQuota: 900000})
	require.NoError(t, err)
	var recharge RebateAdminRecharge
	require.NoError(t, DB.Where("user_id = ?", user.Id).Order("id desc").First(&recharge).Error)
	assert.Equal(t, int64(1000000), recharge.Quota)
	assert.Equal(t, int64(900000), recharge.PaidQuota)
	_, err = AdjustUserQuota(user.Id, common.RoleRootUser, "add", 1000000, &UserQuotaRecharge{OperatorID: user.Id, PaidQuota: 1000001})
	assert.ErrorIs(t, err, ErrInvalidUserQuotaAdjustment)
	var rechargeCount int64
	require.NoError(t, DB.Model(&RebateAdminRecharge{}).Where("user_id = ?", user.Id).Count(&rechargeCount).Error)
	assert.Equal(t, int64(1), rechargeCount)

	// Redeem an actual unused code from the backup, then prove a retry cannot
	// credit it twice. The transaction restores both the code and user's quota.
	var code Redemption
	require.NoError(t, DB.Where("status = ? AND quota > 0 AND (expired_time = 0 OR expired_time > ?)",
		common.RedemptionCodeStatusEnabled, common.GetTimestamp()).First(&code).Error)
	quota, err := Redeem(code.Key, user.Id)
	require.NoError(t, err)
	assert.Equal(t, code.Quota, quota)
	_, err = Redeem(code.Key, user.Id)
	assert.ErrorIs(t, err, ErrRedeemCodeUsed)
	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, initialQuota+6000000+code.Quota, got.Quota)
}

// Interleave a real cache-miss reader between the database commit and the
// credit's Redis command, without relying on sleeps or scheduler timing.
type hydrateUserBeforeCreditHook struct {
	userID   int
	hydrated atomic.Bool
}

func (hook *hydrateUserBeforeCreditHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	if cmd.Name() == "eval" && hook.hydrated.CompareAndSwap(false, true) {
		_, err := GetUserCache(hook.userID)
		return ctx, err
	}
	return ctx, nil
}

func (*hydrateUserBeforeCreditHook) AfterProcess(context.Context, redis.Cmder) error {
	return nil
}

func (*hydrateUserBeforeCreditHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (*hydrateUserBeforeCreditHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func setupRebateRedisTest(t *testing.T, userID int) *redis.Client {
	t.Helper()
	redisURL := os.Getenv("REBATE_REDIS_TEST_URL")
	if redisURL == "" {
		t.Skip("REBATE_REDIS_TEST_URL is not configured")
	}
	redisOptions, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	require.Positive(t, redisOptions.DB, "requires a dedicated nonzero Redis test database")
	previousRedis, previousRedisEnabled := common.RDB, common.RedisEnabled
	client := redis.NewClient(redisOptions)
	ctx := context.Background()
	require.NoError(t, client.Ping(ctx).Err())
	common.RDB, common.RedisEnabled = client, true
	keys := []string{getUserCacheKey(userID), getUserAuthFenceKey(userID), getUserAuthVersionKey(userID)}
	require.NoError(t, client.Del(ctx, keys...).Err())
	t.Cleanup(func() {
		assert.NoError(t, client.Del(ctx, keys...).Err())
		common.RDB, common.RedisEnabled = previousRedis, previousRedisEnabled
		assert.NoError(t, client.Close())
	})
	return client
}

func TestRebateMySQLTransferCacheMissCannotDoubleCredit(t *testing.T) {
	for _, operation := range []string{"transfer", "admin", "epay"} {
		t.Run(operation, func(t *testing.T) {
			user := setupRebateMySQLTest(t)
			withRebatePrices(t)
			client := setupRebateRedisTest(t, user.Id)
			require.NoError(t, DB.Model(user).Updates(map[string]any{
				"quota": 100, "aff_quota": 500000, "aff_history": 500000,
			}).Error)
			var order *TopUp
			if operation == "epay" {
				order = &TopUp{
					UserId: user.Id, Amount: 1, Money: 7.3,
					TradeNo:       "rebate-cache-" + common.GetRandomString(16),
					PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay,
					Status: common.TopUpStatusPending,
				}
				require.NoError(t, order.Insert())
				t.Cleanup(func() {
					assert.NoError(t, DB.Where("trade_no = ?", order.TradeNo).Delete(&RebateTopupPaid{}).Error)
					assert.NoError(t, DB.Delete(order).Error)
				})
			}
			hook := &hydrateUserBeforeCreditHook{userID: user.Id}
			client.AddHook(hook)

			switch operation {
			case "transfer":
				require.NoError(t, user.TransferAffQuotaToQuota(500000))
			case "admin":
				var previousID int
				require.NoError(t, DB.Model(&RebateAdminRecharge{}).Select("COALESCE(MAX(id), 0)").Scan(&previousID).Error)
				t.Cleanup(func() {
					assert.NoError(t, DB.Where("id > ? AND user_id = ?", previousID, user.Id).Delete(&RebateAdminRecharge{}).Error)
				})
				_, err := AdjustUserQuota(user.Id, common.RoleRootUser, "add", 500000,
					&UserQuotaRecharge{OperatorID: user.Id, PaidQuota: 500000})
				require.NoError(t, err)
			case "epay":
				_, err := RechargeEpay(order.TradeNo, "alipay", "127.0.0.1", "7.30")
				require.NoError(t, err)
			}
			require.True(t, hook.hydrated.Load(), "the competing cache read must run at the credit boundary")
			cached, err := getUserQuotaCache(user.Id)
			require.NoError(t, err)
			assert.Equal(t, 500100, cached, "a cache created after commit already contains the credit")
			assert.Equal(t, 500100, getUserQuotaFromDB(t, user.Id))

			reserved, err := TryReserveUserQuota(user.Id, 500101)
			require.NoError(t, err)
			assert.False(t, reserved, "a credit must not authorize spending above the committed wallet balance")
			assert.Equal(t, 500100, getUserQuotaFromDB(t, user.Id), "a rejected reservation must not overdraw the wallet")
		})
	}
}

func TestRebateMySQLWalletMutationsStayAuthoritativeWithBatchAndRedisFailures(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, cacheState := range []string{"stale", "missing", "unavailable"} {
			t.Run(fmt.Sprintf("batch=%t/cache=%s", batch, cacheState), func(t *testing.T) {
				user := setupRebateMySQLTest(t)
				client := setupRebateRedisTest(t, user.Id)
				resetBatchUpdateTestState(t)
				common.BatchUpdateEnabled = batch
				require.NoError(t, DB.Model(user).Update("quota", 100).Error)
				if cacheState == "stale" {
					_, err := GetUserCache(user.Id)
					require.NoError(t, err)
					require.NoError(t, client.HSet(context.Background(), getUserCacheKey(user.Id), "Quota", 1000000).Err())
				}
				if cacheState == "unavailable" {
					unavailable := redis.NewClient(client.Options())
					require.NoError(t, unavailable.Close())
					common.RDB = unavailable
				}

				quota, err := GetUserQuota(user.Id, false)
				require.NoError(t, err)
				assert.Equal(t, 100, quota, "billing reads must use the committed wallet")
				reserved, err := TryReserveUserQuota(user.Id, 80)
				require.NoError(t, err)
				require.True(t, reserved)
				assert.Equal(t, 20, getUserQuotaFromDB(t, user.Id), "wallet reservations must be durable before returning")
				require.NoError(t, IncreaseUserQuota(user.Id, 10, false))
				assert.Equal(t, 30, getUserQuotaFromDB(t, user.Id), "refunds must be immediately available")
				require.NoError(t, DecreaseUserQuota(user.Id, 15, false))
				assert.Equal(t, 15, getUserQuotaFromDB(t, user.Id), "settlement differences must be durable before returning")
				reserved, err = TryReserveUserQuota(user.Id, 16)
				require.NoError(t, err)
				assert.False(t, reserved)
				require.NoError(t, IncreaseUserQuota(user.Id, 0, false), "zero-cost settlement must remain a successful no-op")
				batchUpdate()
				assert.Equal(t, 15, getUserQuotaFromDB(t, user.Id), "batch flush must not apply wallet mutations again")
			})
		}
	}
}

func TestRebateMySQLCheckinRespectsTheWalletCap(t *testing.T) {
	user := setupRebateMySQLTest(t)
	assertCheckinRespectsWalletCap(t, user.Id)
}
