package model

import (
	"context"
	"errors"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm/schema"
	"math"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func createReserveTestUser(t *testing.T, quota int) User {
	t.Helper()
	user := User{
		Username:    "reserve-user-" + common.GetRandomString(6),
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
		Quota:       quota,
		AffCode:     "reserve-aff-" + common.GetRandomString(8),
	}
	require.NoError(t, DB.Create(&user).Error)
	return user
}

func createReserveTestToken(t *testing.T, remainQuota int) Token {
	t.Helper()
	token := Token{
		UserId:      1,
		Key:         "reserve-token-" + common.GetRandomString(8),
		Name:        "reserve-test",
		Status:      common.TokenStatusEnabled,
		ExpiredTime: -1,
		RemainQuota: remainQuota,
	}
	require.NoError(t, token.Insert())
	return token
}

func getUserQuotaFromDB(t *testing.T, id int) int {
	t.Helper()
	var user User
	require.NoError(t, DB.Select("quota").First(&user, id).Error)
	return user.Quota
}

func getTokenFromDB(t *testing.T, id int) Token {
	t.Helper()
	var token Token
	require.NoError(t, DB.First(&token, id).Error)
	return token
}

func resetBatchUpdateTestState(t *testing.T) {
	t.Helper()
	oldBatchEnabled := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = false
	for i := range BatchUpdateTypeCount {
		batchUpdateLocks[i].Lock()
		batchUpdateStores[i] = make(map[int]int)
		batchUpdateLocks[i].Unlock()
	}
	t.Cleanup(func() {
		common.BatchUpdateEnabled = oldBatchEnabled
		for i := range BatchUpdateTypeCount {
			batchUpdateLocks[i].Lock()
			batchUpdateStores[i] = make(map[int]int)
			batchUpdateLocks[i].Unlock()
		}
	})
}

func TestTryReserveQuotaWithoutRedis(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)

	user := createReserveTestUser(t, 100)
	reserved, err := TryReserveUserQuota(user.Id, 60)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 40, getUserQuotaFromDB(t, user.Id))

	reserved, err = TryReserveUserQuota(user.Id, 41)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 40, getUserQuotaFromDB(t, user.Id))

	token := createReserveTestToken(t, 80)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 25, false)
	require.NoError(t, err)
	assert.True(t, reserved)
	reloaded := getTokenFromDB(t, token.Id)
	assert.Equal(t, 55, reloaded.RemainQuota)
	assert.Equal(t, 25, reloaded.UsedQuota)

	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 56, false)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 55, getTokenFromDB(t, token.Id).RemainQuota)
}

func TestRedisBatchReserveNeverFallsBackToStaleDatabaseBalance(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true

	user := createReserveTestUser(t, 10)
	reserved, err := TryReserveUserQuota(user.Id, 8)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 10, getUserQuotaFromDB(t, user.Id), "batch delta is not flushed yet")

	reserved, err = TryReserveUserQuota(user.Id, 3)
	require.NoError(t, err)
	assert.False(t, reserved, "stale DB balance must not authorize a second spend")
	cachedUser, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 2, cachedUser.Quota)

	token := createReserveTestToken(t, 9)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 7, false)
	require.NoError(t, err)
	assert.True(t, reserved)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 3, false)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 9, getTokenFromDB(t, token.Id).RemainQuota)

	batchUpdate()
	assert.Equal(t, 2, getUserQuotaFromDB(t, user.Id))
	reloadedToken := getTokenFromDB(t, token.Id)
	assert.Equal(t, 2, reloadedToken.RemainQuota)
	assert.Equal(t, 7, reloadedToken.UsedQuota)
}

func TestBatchUpdateAccumulatesTwoMaximumRequestCharges(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	common.BatchUpdateEnabled = true

	user := createReserveTestUser(t, common.MaxQuota*2+100)
	require.NoError(t, DecreaseUserQuota(user.Id, common.MaxQuota, false))
	require.NoError(t, DecreaseUserQuota(user.Id, common.MaxQuota, false))

	batchUpdate()
	assert.Equal(t, 100, getUserQuotaFromDB(t, user.Id))
}

func TestBatchUpdateAccumulatorSaturatesOverflow(t *testing.T) {
	resetBatchUpdateTestState(t)

	addNewRecord(BatchUpdateTypeUserQuota, 1, math.MaxInt)
	addNewRecord(BatchUpdateTypeUserQuota, 1, 1)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	assert.Equal(t, math.MaxInt, batchUpdateStores[BatchUpdateTypeUserQuota][1])
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()

	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	batchUpdateStores[BatchUpdateTypeUserQuota] = make(map[int]int)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
	addNewRecord(BatchUpdateTypeUserQuota, 1, math.MinInt)
	addNewRecord(BatchUpdateTypeUserQuota, 1, -1)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	assert.Equal(t, math.MinInt, batchUpdateStores[BatchUpdateTypeUserQuota][1])
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
}

func TestReserveFallsBackToDatabaseWhenRedisIsUnavailable(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)

	user := createReserveTestUser(t, 20)
	require.NoError(t, populateUserCache(user))
	server.Close()

	// Redis 故障时降级为数据库条件更新：服务保持可用且不会超扣。
	reserved, err := TryReserveUserQuota(user.Id, 5)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 15, getUserQuotaFromDB(t, user.Id))

	reserved, err = TryReserveUserQuota(user.Id, 16)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 15, getUserQuotaFromDB(t, user.Id))
}

func TestSynchronousReserveCompensatesCacheWhenPersistenceFails(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)

	user := createReserveTestUser(t, 10)
	require.NoError(t, populateUserCache(user))
	require.NoError(t, DB.Delete(&user).Error)

	reserved, err := TryReserveUserQuota(user.Id, 6)
	assert.False(t, reserved)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	cached, cacheErr := cacheGetUserBase(user.Id)
	require.NoError(t, cacheErr)
	assert.Equal(t, 10, cached.Quota)

	token := createReserveTestToken(t, 12)
	_, err = GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	require.NoError(t, DB.Delete(&token).Error)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 7, false)
	assert.False(t, reserved)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	cachedToken, cacheErr := cacheGetTokenByKey(token.Key)
	require.NoError(t, cacheErr)
	assert.Equal(t, 12, cachedToken.RemainQuota)
	assert.Zero(t, cachedToken.UsedQuota)
}

func TestTokenCacheInitPreservesLiveQuotaAndFenceBlocksStaleSnapshot(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)

	token := createReserveTestToken(t, 100)
	loaded, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	stale := *loaded

	result, err := cacheApplyTokenQuotaDelta(token.Id, token.Key, -70)
	require.NoError(t, err)
	require.Equal(t, cacheQuotaOK, result)

	// 已存在的哈希只刷新 TTL：数据库快照不得覆盖已被原子预扣的余额。
	code, err := cacheInitToken(stale)
	require.NoError(t, err)
	assert.Equal(t, 2, code)
	cached, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, 30, cached.RemainQuota)

	// 变更期间：fence 删除缓存并拦截并发读者手中的过期快照。
	require.NoError(t, invalidateTokenCacheForMutation(token.Key))
	code, err = cacheInitToken(stale)
	require.NoError(t, err)
	assert.Zero(t, code, "the pre-mutation snapshot must not be published while fenced")
	_, err = cacheGetTokenByKey(token.Key)
	assert.Error(t, err)

	// fence 过期后可重新从数据库水合。
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	fresh, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, 100, fresh.RemainQuota)
	cached, err = cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, 100, cached.RemainQuota)
}

func TestDailyQuotaReservationDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "daily_reserve_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			oldDB, oldType := DB, common.MainDatabaseType()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			initCol()
			resetBatchUpdateTestState(t)
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&Token{}, &Option{}))
				DB = oldDB
				common.SetMainDatabaseType(oldType)
				initCol()
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&Token{}, &Option{}))
			require.NoError(t, db.AutoMigrate(&Token{}, &Option{}))
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			for _, useRedis := range []bool{false, true} {
				t.Run(fmt.Sprintf("redis=%v", useRedis), func(t *testing.T) {
					prevRedis, prevRDB := common.RedisEnabled, common.RDB
					common.RedisEnabled = useRedis
					t.Cleanup(func() { common.RedisEnabled, common.RDB = prevRedis, prevRDB })
					if useRedis {
						addr := os.Getenv("TEST_REDIS_ADDR")
						if addr == "" {
							t.Skip("TEST_REDIS_ADDR not configured")
						}
						client := redis.NewClient(&redis.Options{Addr: addr, DB: 13})
						common.RDB = client
						require.NoError(t, client.Ping(context.Background()).Err())
						t.Cleanup(func() { require.NoError(t, client.Close()) })
					}
					token := createReserveTestToken(t, 100)
					t.Cleanup(func() {
						require.NoError(t, SetTokenDailyQuotaLimit(token.Id, 0))
						require.NoError(t, DB.Delete(&token).Error)
						if useRedis {
							require.NoError(t, common.RDB.Del(context.Background(), getTokenCacheKey(token.Key), tokenDailyLimitRedisKey(token.Id), tokenDailyUsageRedisKey(token.Id)).Err())
						}
						invalidateTokenDailyQuotaLimitCache(token.Id)
					})
					require.NoError(t, SetTokenDailyQuotaLimit(token.Id, 80))
					token.AllowUserQuotaQuery = true
					token.Name = "atomic-quota-update"
					limit := 90
					require.NoError(t, UpdateTokenWithDailyQuotaLimit(&token, &limit))
					assert.True(t, getTokenFromDB(t, token.Id).AllowUserQuotaQuery)
					require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("daily_matrix:option_fail", func(tx *gorm.DB) {
						if tx.Statement.Table == "daily_reserve_options" {
							tx.AddError(errors.New("option write rejected"))
						}
					}))
					token.AllowUserQuotaQuery = false
					token.Name = "must-roll-back"
					limit = 100
					require.Error(t, UpdateTokenWithDailyQuotaLimit(&token, &limit))
					require.NoError(t, DB.Callback().Update().Remove("daily_matrix:option_fail"))
					saved := getTokenFromDB(t, token.Id)
					assert.True(t, saved.AllowUserQuotaQuery)
					assert.Equal(t, "atomic-quota-update", saved.Name)
					assert.Equal(t, 90, GetTokenDailyQuotaLimit(token.Id))
					require.NoError(t, UpdateTokenWithDailyQuotaLimit(&token, nil))
					assert.False(t, getTokenFromDB(t, token.Id).AllowUserQuotaQuery)
					assert.Equal(t, 90, GetTokenDailyQuotaLimit(token.Id))
					// Expire the deliberate mutation fence before testing live cache compensation.
					if useRedis {
						require.NoError(t, common.RDB.Del(context.Background(), getTokenCacheFenceKey(token.Key)).Err())
					}
					require.NoError(t, SetTokenDailyQuotaLimit(token.Id, 80))
					reserved, err := TryReserveTokenQuota(token.Id, token.Key, 60, false)
					require.NoError(t, err)
					require.True(t, reserved)
					assert.Equal(t, int64(60), GetTokenDailyUsage(token.Id))
					reserved, err = TryReserveTokenQuota(token.Id, token.Key, 41, false)
					require.NoError(t, err)
					assert.False(t, reserved)
					assert.Equal(t, int64(60), GetTokenDailyUsage(token.Id))
					require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("daily_matrix:fail", func(tx *gorm.DB) {
						if tx.Statement.Table == "daily_reserve_tokens" {
							tx.AddError(errors.New("write rejected"))
						}
					}))
					t.Cleanup(func() { _ = DB.Callback().Update().Remove("daily_matrix:fail") })
					for _, operation := range []func() error{
						func() error { return DecreaseTokenQuota(token.Id, token.Key, 7) },
						func() error { return IncreaseTokenQuota(token.Id, token.Key, 10) },
						func() error { _, err := TryReserveTokenQuota(token.Id, token.Key, 5, false); return err },
					} {
						require.Error(t, operation())
						assert.Equal(t, int64(60), GetTokenDailyUsage(token.Id))
						if useRedis {
							cached, err := cacheGetTokenByKey(token.Key)
							require.NoError(t, err)
							assert.Equal(t, 40, cached.RemainQuota)
							assert.Equal(t, 60, cached.UsedQuota)
						}
					}
					require.NoError(t, DB.Callback().Update().Remove("daily_matrix:fail"))
					require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 20))
					assert.Equal(t, int64(40), GetTokenDailyUsage(token.Id))
					reserved, err = TryReserveTokenQuota(token.Id, token.Key, 50, true)
					require.NoError(t, err)
					require.True(t, reserved)
					assert.Equal(t, int64(90), GetTokenDailyUsage(token.Id), "unlimited reservation counted once")
					exceeded, _, _ := CheckTokenDailyQuota(token.Id)
					assert.True(t, exceeded)
					assert.Equal(t, 90, getTokenFromDB(t, token.Id).UsedQuota)
					require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 90))
					assert.Zero(t, GetTokenDailyUsage(token.Id))
					assert.Equal(t, 100, getTokenFromDB(t, token.Id).RemainQuota)
				})
			}
		})
	}
}
