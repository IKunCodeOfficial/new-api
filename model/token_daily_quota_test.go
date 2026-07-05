package model

import (
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTokenDailyQuotaTest resets shared tables + the in-process caches and ensures the
// options table exists (TestMain does not migrate &Option{}). Call at the top of each test.
func setupTokenDailyQuotaTest(t *testing.T) {
	t.Helper()
	truncateTables(t) // registers cleanup for tokens/users/...
	require.NoError(t, DB.AutoMigrate(&Option{}))
	require.NoError(t, DB.Exec("DELETE FROM options").Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM options") })

	require.False(t, common.RedisEnabled, "tests assume the in-process (non-Redis) path")
	tokenDailyUsageMu.Lock()
	tokenDailyUsageDay = ""
	tokenDailyUsageData = map[int]int64{}
	tokenDailyUsageMu.Unlock()
	tokenDailyLimitMu.Lock()
	tokenDailyLimitCache = map[int]int{}
	tokenDailyLimitMu.Unlock()
	resetTokenDailyLimitMemo()
}

func seedTokenForQuota(t *testing.T, id, userId int) *Token {
	t.Helper()
	token := &Token{
		Id:             id,
		UserId:         userId,
		Name:           "tk-" + strconv.Itoa(id),
		Key:            "key-" + strconv.Itoa(id),
		Status:         common.TokenStatusEnabled,
		CreatedTime:    1,
		AccessedTime:   1,
		ExpiredTime:    -1,
		RemainQuota:    1000,
		UnlimitedQuota: false,
		Group:          "default",
	}
	require.NoError(t, DB.Create(token).Error)
	return token
}

func TestSetTokenDailyQuotaLimit_RoundTripAndClear(t *testing.T) {
	setupTokenDailyQuotaTest(t)
	tk := seedTokenForQuota(t, 101, 1)

	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 500))

	var opt Option
	require.NoError(t, DB.Where(commonKeyCol+" = ?", tokenDailyQuotaOptionKey(tk.Id)).First(&opt).Error)
	assert.Equal(t, "500", opt.Value)
	assert.Equal(t, 500, GetTokenDailyQuotaLimit(tk.Id))

	// Setting 0 clears the limit by deleting the row (token becomes unlimited).
	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 0))
	var count int64
	require.NoError(t, DB.Model(&Option{}).Where(commonKeyCol+" = ?", tokenDailyQuotaOptionKey(tk.Id)).Count(&count).Error)
	assert.Equal(t, int64(0), count)
	assert.Equal(t, 0, GetTokenDailyQuotaLimit(tk.Id))
}

func TestBatchSetTokenDailyQuotaLimit_OwnershipAndCount(t *testing.T) {
	setupTokenDailyQuotaTest(t)
	a1 := seedTokenForQuota(t, 201, 1)
	a2 := seedTokenForQuota(t, 202, 1)
	b1 := seedTokenForQuota(t, 203, 2) // belongs to a different user

	count, err := BatchSetTokenDailyQuotaLimit([]int{a1.Id, a2.Id, b1.Id}, 1, 800)
	require.NoError(t, err)
	assert.Equal(t, 2, count) // only user 1's tokens are updated

	assert.Equal(t, 800, GetTokenDailyQuotaLimit(a1.Id))
	assert.Equal(t, 800, GetTokenDailyQuotaLimit(a2.Id))
	assert.Equal(t, 0, GetTokenDailyQuotaLimit(b1.Id)) // other user's token untouched

	// The other user's token must have no persisted row either.
	var b1Count int64
	require.NoError(t, DB.Model(&Option{}).Where(commonKeyCol+" = ?", tokenDailyQuotaOptionKey(b1.Id)).Count(&b1Count).Error)
	assert.Equal(t, int64(0), b1Count)

	// Batch clear (limit 0) removes rows for the matched tokens.
	count, err = BatchSetTokenDailyQuotaLimit([]int{a1.Id, a2.Id}, 1, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Equal(t, 0, GetTokenDailyQuotaLimit(a1.Id))
	assert.Equal(t, 0, GetTokenDailyQuotaLimit(a2.Id))
}

func TestTokenDailyUsageAndCheckBoundaries(t *testing.T) {
	setupTokenDailyQuotaTest(t)
	tk := seedTokenForQuota(t, 301, 1)

	// No limit configured: usage is NOT recorded (unlimited tokens skip the write so the
	// billing hot path and Redis stay free of the unlimited majority), and the token is
	// never blocked. A limit set later therefore counts from that point forward.
	AddTokenDailyUsage(tk.Id, 100)
	assert.Equal(t, int64(0), GetTokenDailyUsage(tk.Id))
	exceeded, used, limit := CheckTokenDailyQuota(tk.Id)
	assert.False(t, exceeded)
	assert.Equal(t, int64(0), used)
	assert.Equal(t, 0, limit)

	// Once a limit is configured, usage accrues and refunds (negative deltas) decrement it.
	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 100))
	AddTokenDailyUsage(tk.Id, 70)
	assert.Equal(t, int64(70), GetTokenDailyUsage(tk.Id))
	AddTokenDailyUsage(tk.Id, -30)
	assert.Equal(t, int64(40), GetTokenDailyUsage(tk.Id))

	// used < limit -> pass.
	exceeded, used, limit = CheckTokenDailyQuota(tk.Id)
	assert.False(t, exceeded)
	assert.Equal(t, int64(40), used)
	assert.Equal(t, 100, limit)

	// used == limit -> block (>= boundary).
	AddTokenDailyUsage(tk.Id, 60)
	exceeded, _, _ = CheckTokenDailyQuota(tk.Id)
	assert.True(t, exceeded)

	// used > limit -> block.
	AddTokenDailyUsage(tk.Id, 5)
	exceeded, used, _ = CheckTokenDailyQuota(tk.Id)
	assert.True(t, exceeded)
	assert.Equal(t, int64(105), used)
}

func TestAddTokenDailyUsage_FloorsAtZero(t *testing.T) {
	setupTokenDailyQuotaTest(t)
	tk := seedTokenForQuota(t, 601, 1)

	// A limit must be configured for usage to be recorded at all.
	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 100))

	AddTokenDailyUsage(tk.Id, 50)
	assert.Equal(t, int64(50), GetTokenDailyUsage(tk.Id))

	// A refund larger than today's usage (its original charge was on a previous day) must
	// floor at 0, not create a negative credit that would under-enforce the next day's cap.
	AddTokenDailyUsage(tk.Id, -80)
	assert.Equal(t, int64(0), GetTokenDailyUsage(tk.Id))

	// Subsequent real usage accrues from 0, so the cap still enforces correctly.
	AddTokenDailyUsage(tk.Id, 100)
	exceeded, used, limit := CheckTokenDailyQuota(tk.Id)
	assert.True(t, exceeded)
	assert.Equal(t, int64(100), used)
	assert.Equal(t, 100, limit)
}

func TestSetTokenDailyQuotaLimit_ClearWipesTodayUsage(t *testing.T) {
	setupTokenDailyQuotaTest(t)
	tk := seedTokenForQuota(t, 701, 1)

	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 100))
	AddTokenDailyUsage(tk.Id, 60)
	assert.Equal(t, int64(60), GetTokenDailyUsage(tk.Id))

	// Clearing the limit (0) also drops today's usage counter, so a same-day re-enable starts
	// counting from 0 rather than resuming the stale 60.
	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 0))
	assert.Equal(t, int64(0), GetTokenDailyUsage(tk.Id))

	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 100))
	assert.Equal(t, int64(0), GetTokenDailyUsage(tk.Id))
	AddTokenDailyUsage(tk.Id, 40)
	exceeded, used, limit := CheckTokenDailyQuota(tk.Id)
	assert.False(t, exceeded)
	assert.Equal(t, int64(40), used)
	assert.Equal(t, 100, limit)
}

func TestTokenDailyUsage_InProcessMidnightReset(t *testing.T) {
	setupTokenDailyQuotaTest(t)
	prevClock := tokenDailyClock
	t.Cleanup(func() { tokenDailyClock = prevClock })

	now := time.Date(2026, 1, 1, 23, 0, 0, 0, time.Local)
	tokenDailyClock = func() time.Time { return now }

	tk := seedTokenForQuota(t, 901, 1)
	require.NoError(t, SetTokenDailyQuotaLimit(tk.Id, 100))
	AddTokenDailyUsage(tk.Id, 80)
	assert.Equal(t, int64(80), GetTokenDailyUsage(tk.Id))

	// Crossing local midnight rolls the in-process usage map to a new day, resetting today's
	// counter to 0 while the configured limit persists.
	now = now.Add(2 * time.Hour) // 2026-01-02 01:00
	assert.Equal(t, int64(0), GetTokenDailyUsage(tk.Id))
	exceeded, used, limit := CheckTokenDailyQuota(tk.Id)
	assert.False(t, exceeded)
	assert.Equal(t, int64(0), used)
	assert.Equal(t, 100, limit)

	AddTokenDailyUsage(tk.Id, 40)
	assert.Equal(t, int64(40), GetTokenDailyUsage(tk.Id))
}

func TestInsertAndUpdateTokenWithDailyQuotaLimit(t *testing.T) {
	setupTokenDailyQuotaTest(t)

	// Insert persists the token and its limit atomically.
	limit := 500
	tok := &Token{
		Id: 801, UserId: 1, Name: "atomic", Key: "atomic-key",
		Status: common.TokenStatusEnabled, CreatedTime: 1, AccessedTime: 1,
		ExpiredTime: -1, RemainQuota: 1000, Group: "default",
	}
	require.NoError(t, InsertTokenWithDailyQuotaLimit(tok, &limit))
	var got Token
	require.NoError(t, DB.Where("id = ?", tok.Id).First(&got).Error)
	assert.Equal(t, "atomic", got.Name)
	assert.Equal(t, 500, GetTokenDailyQuotaLimit(tok.Id))

	// Update rewrites core fields and the limit in one transaction.
	tok.Name = "atomic-renamed"
	newLimit := 900
	require.NoError(t, UpdateTokenWithDailyQuotaLimit(tok, &newLimit))
	require.NoError(t, DB.Where("id = ?", tok.Id).First(&got).Error)
	assert.Equal(t, "atomic-renamed", got.Name)
	assert.Equal(t, 900, GetTokenDailyQuotaLimit(tok.Id))

	// A nil limit updates only the core fields, leaving the existing limit untouched.
	tok.Name = "atomic-again"
	require.NoError(t, UpdateTokenWithDailyQuotaLimit(tok, nil))
	require.NoError(t, DB.Where("id = ?", tok.Id).First(&got).Error)
	assert.Equal(t, "atomic-again", got.Name)
	assert.Equal(t, 900, GetTokenDailyQuotaLimit(tok.Id))

	// Clearing via update (0) removes the persisted limit row.
	zero := 0
	require.NoError(t, UpdateTokenWithDailyQuotaLimit(tok, &zero))
	assert.Equal(t, 0, GetTokenDailyQuotaLimit(tok.Id))
	var count int64
	require.NoError(t, DB.Model(&Option{}).Where(commonKeyCol+" = ?", tokenDailyQuotaOptionKey(tok.Id)).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestLoadOptionsFromDatabase_SkipsDailyQuotaRows(t *testing.T) {
	setupTokenDailyQuotaTest(t)

	prefixedKey := tokenDailyQuotaOptionKey(401)
	const plainKey = "TdqTestPlainOption"

	require.NoError(t, DB.Create(&Option{Key: prefixedKey, Value: "500"}).Error)
	require.NoError(t, DB.Create(&Option{Key: plainKey, Value: "hello"}).Error)

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	delete(common.OptionMap, prefixedKey)
	delete(common.OptionMap, plainKey)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		delete(common.OptionMap, plainKey)
		common.OptionMapRWMutex.Unlock()
	})

	loadOptionsFromDatabase()

	common.OptionMapRWMutex.RLock()
	_, hasPrefixed := common.OptionMap[prefixedKey]
	plainVal, hasPlain := common.OptionMap[plainKey]
	common.OptionMapRWMutex.RUnlock()

	assert.False(t, hasPrefixed, "daily-quota option rows must NOT enter common.OptionMap")
	assert.True(t, hasPlain, "unrelated option rows must still load")
	assert.Equal(t, "hello", plainVal)
}

func TestAttachAndRemoveTokenDailyQuota(t *testing.T) {
	setupTokenDailyQuotaTest(t)
	t1 := seedTokenForQuota(t, 501, 1)
	t2 := seedTokenForQuota(t, 502, 1)

	require.NoError(t, SetTokenDailyQuotaLimit(t1.Id, 600))
	AddTokenDailyUsage(t1.Id, 150)

	AttachTokenDailyQuota([]*Token{t1, t2})

	require.NotNil(t, t1.DailyQuotaLimit)
	require.NotNil(t, t1.DailyQuotaUsed)
	assert.Equal(t, 600, *t1.DailyQuotaLimit)
	assert.Equal(t, int64(150), *t1.DailyQuotaUsed)

	// A token without a configured limit reports 0/0 (always populated for responses).
	require.NotNil(t, t2.DailyQuotaLimit)
	require.NotNil(t, t2.DailyQuotaUsed)
	assert.Equal(t, 0, *t2.DailyQuotaLimit)
	assert.Equal(t, int64(0), *t2.DailyQuotaUsed)

	RemoveTokenDailyQuotaConfig(t1.Id)
	assert.Equal(t, 0, GetTokenDailyQuotaLimit(t1.Id))
	var count int64
	require.NoError(t, DB.Model(&Option{}).Where(commonKeyCol+" = ?", tokenDailyQuotaOptionKey(t1.Id)).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}
