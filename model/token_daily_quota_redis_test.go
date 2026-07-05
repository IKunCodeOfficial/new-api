package model

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTokenDailyQuotaRedisTest points common.RDB at an in-memory miniredis and flips
// common.RedisEnabled on for the duration of the test, so the production Redis path — the atomic
// Lua floor script, MGET batching, the limit cache/L1 memo, and the fail-open branches — is
// actually exercised. The in-process tests (setupTokenDailyQuotaTest) cover only the non-Redis
// fallback, which is not what real deployments run. Returns the miniredis handle so a test can
// Close it to simulate a Redis outage.
func setupTokenDailyQuotaRedisTest(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	truncateTables(t) // registers cleanup for tokens/users/...
	require.NoError(t, DB.AutoMigrate(&Option{}))
	require.NoError(t, DB.Exec("DELETE FROM options").Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM options") })

	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	prevRDB := common.RDB
	prevEnabled := common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{
		Addr:         mr.Addr(),
		DialTimeout:  time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	})
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB = prevRDB
		common.RedisEnabled = prevEnabled
	})

	resetTokenDailyLimitMemo()
	tokenDailyUsageMu.Lock()
	tokenDailyUsageDay = ""
	tokenDailyUsageData = map[int]int64{}
	tokenDailyUsageMu.Unlock()

	prevClock := tokenDailyClock
	t.Cleanup(func() { tokenDailyClock = prevClock })

	return mr
}

func resetTokenDailyLimitMemo() {
	tokenDailyLimitMemoMu.Lock()
	tokenDailyLimitMemo = map[int]tokenDailyLimitMemoEntry{}
	tokenDailyLimitMemoMu.Unlock()
}

func TestTokenDailyQuota_RedisLimitRoundTrip(t *testing.T) {
	setupTokenDailyQuotaRedisTest(t)
	tk := seedTokenForQuota(t, 1101, 1)

	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 500))
	assert.Equal(t, 500, GetTokenDailyQuotaLimit(tk.Id))
	// Persisted in the Redis limit cache (write-through).
	v, err := common.RDB.Get(context.Background(), tokenDailyLimitRedisKey(tk.Id)).Result()
	require.NoError(t, err)
	assert.Equal(t, "500", v)

	// Clearing deletes the option row and normalizes the cache to 0 (unlimited).
	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 0))
	assert.Equal(t, 0, GetTokenDailyQuotaLimit(tk.Id))
}

func TestTokenDailyQuota_RedisUsageFloorAndTTL(t *testing.T) {
	setupTokenDailyQuotaRedisTest(t)
	tk := seedTokenForQuota(t, 1102, 1)
	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 100))

	// The Lua floor script increments and (re)sets the 48h TTL on key creation.
	AddTokenDailyUsage(tk.Id, 70)
	assert.Equal(t, int64(70), GetTokenDailyUsage(tk.Id))
	ttl, err := common.RDB.TTL(context.Background(), tokenDailyUsageRedisKey(tk.Id)).Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0), "usage counter must carry a TTL so it self-expires")

	// A refund larger than today's usage (its charge landed on a previous day) floors at 0
	// rather than leaving a negative credit that would under-enforce the next day.
	AddTokenDailyUsage(tk.Id, -100)
	assert.Equal(t, int64(0), GetTokenDailyUsage(tk.Id))

	// Cap boundary is >= : usage == limit blocks.
	AddTokenDailyUsage(tk.Id, 100)
	exceeded, used, limit := CheckTokenDailyQuota(tk.Id)
	assert.True(t, exceeded)
	assert.Equal(t, int64(100), used)
	assert.Equal(t, 100, limit)
}

func TestTokenDailyQuota_RedisBatchUsageMGet(t *testing.T) {
	setupTokenDailyQuotaRedisTest(t)
	t1 := seedTokenForQuota(t, 1103, 1)
	t2 := seedTokenForQuota(t, 1104, 1)
	t3 := seedTokenForQuota(t, 1105, 1) // no limit -> no usage recorded

	require.NoError(t, SetTokenDailyQuotaLimit(t1.Id, 1000))
	require.NoError(t, SetTokenDailyQuotaLimit(t2.Id, 2000))
	AddTokenDailyUsage(t1.Id, 150)
	AddTokenDailyUsage(t2.Id, 300)
	AddTokenDailyUsage(t3.Id, 999) // ignored: t3 has no limit

	// AttachTokenDailyQuota reads all usage in a single MGET round-trip.
	AttachTokenDailyQuota([]*Token{t1, t2, t3})
	require.NotNil(t, t1.DailyQuotaUsed)
	require.NotNil(t, t2.DailyQuotaUsed)
	require.NotNil(t, t3.DailyQuotaUsed)
	assert.Equal(t, int64(150), *t1.DailyQuotaUsed)
	assert.Equal(t, int64(300), *t2.DailyQuotaUsed)
	assert.Equal(t, int64(0), *t3.DailyQuotaUsed)
	assert.Equal(t, 1000, *t1.DailyQuotaLimit)
	assert.Equal(t, 0, *t3.DailyQuotaLimit)
}

func TestTokenDailyQuota_RedisMidnightReset(t *testing.T) {
	mr := setupTokenDailyQuotaRedisTest(t)
	_ = mr
	tk := seedTokenForQuota(t, 1106, 1)

	day1 := time.Date(2026, 1, 1, 23, 0, 0, 0, time.Local)
	now := day1
	tokenDailyClock = func() time.Time { return now }

	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 100))
	AddTokenDailyUsage(tk.Id, 80)
	assert.Equal(t, int64(80), GetTokenDailyUsage(tk.Id))

	// Cross local midnight: today's usage is read from a new date-keyed counter, so it resets to
	// 0 automatically — the feature's headline behavior — while the limit (date-independent)
	// stays configured.
	now = day1.Add(2 * time.Hour) // 2026-01-02 01:00
	assert.Equal(t, int64(0), GetTokenDailyUsage(tk.Id))
	exceeded, used, limit := CheckTokenDailyQuota(tk.Id)
	assert.False(t, exceeded)
	assert.Equal(t, int64(0), used)
	assert.Equal(t, 100, limit)

	// New day's usage accrues from 0, independent of day 1's counter.
	AddTokenDailyUsage(tk.Id, 40)
	assert.Equal(t, int64(40), GetTokenDailyUsage(tk.Id))
}

func TestTokenDailyQuota_RedisFailOpen(t *testing.T) {
	mr := setupTokenDailyQuotaRedisTest(t)
	tk := seedTokenForQuota(t, 1107, 1)
	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 100))
	AddTokenDailyUsage(tk.Id, 150)
	exceeded, _, _ := CheckTokenDailyQuota(tk.Id)
	require.True(t, exceeded, "sanity: over limit while Redis is healthy")

	// Simulate a Redis outage. Both the limit and usage reads must fail OPEN (read as 0/unlimited)
	// so a Redis blip never hard-blocks traffic; the memo is cleared so we hit the real read path.
	mr.Close()
	resetTokenDailyLimitMemo()

	assert.Equal(t, 0, GetTokenDailyQuotaLimit(tk.Id), "limit read fails open to unlimited")
	assert.Equal(t, int64(0), GetTokenDailyUsage(tk.Id), "usage read fails open to 0")
	exceeded, _, _ = CheckTokenDailyQuota(tk.Id)
	assert.False(t, exceeded, "cap must not reject while Redis is unavailable")
}

func TestTokenDailyLimitMemo_IfAbsentDoesNotClobberWriteThrough(t *testing.T) {
	resetTokenDailyLimitMemo()

	// A write-through stores the authoritative value.
	setTokenDailyLimitMemo(7777, 500)

	// A slow read-through resolving a stale value (e.g. a DB read that raced a fresh write) must
	// NOT clobber it — mirrors the Redis SetNX read-through guard.
	setTokenDailyLimitMemoIfAbsent(7777, 0)
	got, ok := getTokenDailyLimitMemo(7777)
	require.True(t, ok)
	assert.Equal(t, 500, got)

	// A subsequent write-through is authoritative and still updates the memo.
	setTokenDailyLimitMemo(7777, 900)
	got, ok = getTokenDailyLimitMemo(7777)
	require.True(t, ok)
	assert.Equal(t, 900, got)
}

func TestTokenDailyQuota_RedisFailOpenDoesNotPoisonMemo(t *testing.T) {
	mr := setupTokenDailyQuotaRedisTest(t)
	tk := seedTokenForQuota(t, 1109, 1)
	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 300))
	require.Equal(t, 300, GetTokenDailyQuotaLimit(tk.Id))

	// Clear the memo so the next read must hit Redis, then simulate a transient Redis failure.
	resetTokenDailyLimitMemo()
	mr.Close()

	assert.Equal(t, 0, GetTokenDailyQuotaLimit(tk.Id), "limit read fails open")
	// The fail-open 0 must NOT be memoized: a one-request blip must not turn into a TTL-long
	// enforcement gap; a recovered Redis is consulted again on the very next request.
	_, ok := getTokenDailyLimitMemo(tk.Id)
	assert.False(t, ok, "fail-open result must not be cached in the L1 memo")
}

func TestTokenDailyQuota_RedisCorruptLimitValue(t *testing.T) {
	setupTokenDailyQuotaRedisTest(t)
	tk := seedTokenForQuota(t, 1108, 1)

	// A non-integer cached value (defensive; only integers are ever written) must be dropped and
	// repopulated from the DB rather than returned or re-read every request.
	require.NoError(t, common.RDB.Set(context.Background(), tokenDailyLimitRedisKey(tk.Id), "corrupt", time.Hour).Err())
	resetTokenDailyLimitMemo()

	assert.Equal(t, 0, GetTokenDailyQuotaLimit(tk.Id))
	// The corrupt key was deleted and re-seeded from the DB (which has no row -> "0").
	v, err := common.RDB.Get(context.Background(), tokenDailyLimitRedisKey(tk.Id)).Result()
	require.NoError(t, err)
	assert.Equal(t, "0", v)
}
