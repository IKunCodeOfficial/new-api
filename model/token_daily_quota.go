package model

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

// TokenDailyQuotaOptionPrefix is the `options` table key prefix used to persist each
// token's daily quota limit (one row per limited token, value = limit in quota units).
// Rows with this prefix MUST be excluded from common.OptionMap; see the skip added in
// loadOptionsFromDatabase (option.go).
const TokenDailyQuotaOptionPrefix = "token_daily_quota:"

const (
	tokenDailyUsageKeyPrefix = "tdq:used:"
	tokenDailyLimitKeyPrefix = "tdq:limit:"
	// tokenDailyUsageTTL keeps the per-day usage counter around long enough to survive
	// the current day plus clock skew. The date is part of the key, so the counter resets
	// automatically at local midnight regardless of the TTL.
	tokenDailyUsageTTL = 48 * time.Hour
	// tokenDailyLimitCacheTTL bounds how long a limit lookup stays cached in Redis.
	tokenDailyLimitCacheTTL = 30 * time.Minute
)

func tokenDailyQuotaOptionKey(tokenId int) string {
	return TokenDailyQuotaOptionPrefix + strconv.Itoa(tokenId)
}

// tokenDailyClock returns "now". It is a package var (not a direct time.Now call) so tests can
// simulate the local-midnight date rollover deterministically; production always uses time.Now.
var tokenDailyClock = time.Now

func tokenDailyUsageDate() string {
	return tokenDailyClock().Format("2006-01-02")
}

func tokenDailyUsageRedisKey(tokenId int) string {
	return tokenDailyUsageKeyPrefix + tokenDailyUsageDate() + ":" + strconv.Itoa(tokenId)
}

func tokenDailyLimitRedisKey(tokenId int) string {
	return tokenDailyLimitKeyPrefix + strconv.Itoa(tokenId)
}

// In-process fallback state, used only when Redis is disabled (single-instance accuracy).
var (
	tokenDailyUsageMu   sync.Mutex
	tokenDailyUsageDay  string
	tokenDailyUsageData = map[int]int64{}

	tokenDailyLimitMu    sync.RWMutex
	tokenDailyLimitCache = map[int]int{}
)

// tokenDailyLimitMemo is a short-TTL, size-bounded in-process L1 cache sitting in front of the
// Redis limit lookup (Redis path only). Every relay request resolves a token's limit several
// times — the TokenAuth cap check plus the pre-consume and settle billing chokepoints
// (AddTokenDailyUsage) — and a hot token is resolved by many concurrent requests. Without this
// memo each of those is a synchronous Redis GET on the billing path, paid even by the unlimited
// majority. The memo collapses them to ~1 GET per token per TTL. Staleness on a limit change is
// bounded by the TTL, and the writing instance refreshes/clears the memo immediately (see
// writeTokenDailyQuotaLimitCache / invalidateTokenDailyQuotaLimitCache), which is acceptable for
// a soft daily cap.
const (
	tokenDailyLimitMemoTTL        = 3 * time.Second
	tokenDailyLimitMemoMaxEntries = 100000
)

type tokenDailyLimitMemoEntry struct {
	limit    int
	expireAt time.Time
}

var (
	tokenDailyLimitMemoMu sync.Mutex
	tokenDailyLimitMemo   = map[int]tokenDailyLimitMemoEntry{}
)

func getTokenDailyLimitMemo(tokenId int) (int, bool) {
	tokenDailyLimitMemoMu.Lock()
	defer tokenDailyLimitMemoMu.Unlock()
	entry, ok := tokenDailyLimitMemo[tokenId]
	if !ok {
		return 0, false
	}
	if time.Now().After(entry.expireAt) {
		delete(tokenDailyLimitMemo, tokenId)
		return 0, false
	}
	return entry.limit, true
}

// setTokenDailyLimitMemo unconditionally writes the memo. Used by the write-through path
// (writeTokenDailyQuotaLimitCache), which is authoritative for the value it just persisted.
func setTokenDailyLimitMemo(tokenId int, limit int) {
	tokenDailyLimitMemoMu.Lock()
	storeTokenDailyLimitMemoLocked(tokenId, limit)
	tokenDailyLimitMemoMu.Unlock()
}

// setTokenDailyLimitMemoIfAbsent populates the memo only when no live entry exists, mirroring the
// Redis SetNX read-through: a value resolved by GetTokenDailyQuotaLimit must never clobber a fresh
// write-through that a concurrent SetTokenDailyQuotaLimit may have stored in between, or a
// slow read-through returning a stale DB value would mask the just-set limit for the whole TTL.
func setTokenDailyLimitMemoIfAbsent(tokenId int, limit int) {
	tokenDailyLimitMemoMu.Lock()
	if entry, ok := tokenDailyLimitMemo[tokenId]; !ok || time.Now().After(entry.expireAt) {
		storeTokenDailyLimitMemoLocked(tokenId, limit)
	}
	tokenDailyLimitMemoMu.Unlock()
}

// storeTokenDailyLimitMemoLocked writes an entry, bounding memory by dropping the whole memo when
// it grows past the cap — it is only an accelerator, so a wholesale drop just causes a brief miss
// burst, never an incorrect result. Caller holds tokenDailyLimitMemoMu.
func storeTokenDailyLimitMemoLocked(tokenId int, limit int) {
	if len(tokenDailyLimitMemo) >= tokenDailyLimitMemoMaxEntries {
		tokenDailyLimitMemo = make(map[int]tokenDailyLimitMemoEntry, tokenDailyLimitMemoMaxEntries)
	}
	tokenDailyLimitMemo[tokenId] = tokenDailyLimitMemoEntry{limit: limit, expireAt: time.Now().Add(tokenDailyLimitMemoTTL)}
}

func deleteTokenDailyLimitMemo(tokenId int) {
	tokenDailyLimitMemoMu.Lock()
	delete(tokenDailyLimitMemo, tokenId)
	tokenDailyLimitMemoMu.Unlock()
}

// ensureTokenDailyUsageDayLocked swaps the in-process usage map when the local day rolls
// over, implementing the automatic midnight reset for the non-Redis path. Caller holds
// tokenDailyUsageMu.
func ensureTokenDailyUsageDayLocked() {
	day := tokenDailyUsageDate()
	if day != tokenDailyUsageDay {
		tokenDailyUsageDay = day
		tokenDailyUsageData = map[int]int64{}
	}
}

// tokenDailyUsageFloorScript atomically increments today's usage counter, clamps it at 0,
// and (re)sets the 48h TTL when missing — all in a single round-trip. It replaces a
// non-atomic INCRBY -> read newVal -> INCRBY(-newVal) sequence in which two concurrent
// refunds could each add their correction back and permanently drop usage.
var tokenDailyUsageFloorScript = redis.NewScript(`
local v = redis.call('INCRBY', KEYS[1], ARGV[1])
if v < 0 then
    redis.call('SET', KEYS[1], 0)
    v = 0
end
if redis.call('TTL', KEYS[1]) < 0 then
    redis.call('EXPIRE', KEYS[1], ARGV[2])
end
return v
`)

// AddTokenDailyUsage adds delta (quota units; negative for refunds) to today's usage
// counter for the token. Best-effort: failures are logged, never returned, so token
// billing is never blocked by daily-quota bookkeeping. Usage is only recorded for tokens
// that actually have a daily limit configured — unlimited tokens (the vast majority) skip
// the write entirely, so no never-read tdq:used:* key is created for them. The counter is
// floored at 0 so a refund whose original charge landed on an earlier day cannot create a
// negative "credit" that would under-enforce the next day's cap.
func AddTokenDailyUsage(tokenId int, delta int64) {
	if delta == 0 {
		return
	}
	// Unlimited tokens (no configured limit) skip the write entirely.
	if GetTokenDailyQuotaLimit(tokenId) <= 0 {
		return
	}
	// The increment runs SYNCHRONOUSLY in the caller's goroutine (not via gopool). A request's
	// charge is applied during pre-consume, which always happens-before that same request's
	// later refund/settle. Deferring the increment to a pool goroutine would drop that ordering,
	// and combined with the floor-at-0 clamp a refund that executed first would discard its
	// negative correction, permanently over-counting the day's usage and prematurely tripping
	// the cap.
	if common.RedisEnabled {
		ttlSeconds := int64(tokenDailyUsageTTL / time.Second)
		if err := tokenDailyUsageFloorScript.Run(context.Background(), common.RDB,
			[]string{tokenDailyUsageRedisKey(tokenId)}, delta, ttlSeconds).Err(); err != nil {
			common.SysLog("failed to add token daily usage: " + err.Error())
		}
		return
	}
	tokenDailyUsageMu.Lock()
	ensureTokenDailyUsageDayLocked()
	tokenDailyUsageData[tokenId] += delta
	if tokenDailyUsageData[tokenId] < 0 {
		tokenDailyUsageData[tokenId] = 0
	}
	tokenDailyUsageMu.Unlock()
}

// GetTokenDailyUsage returns today's consumed quota units for the token (0 if none).
func GetTokenDailyUsage(tokenId int) int64 {
	if common.RedisEnabled {
		val, err := common.RedisGet(tokenDailyUsageRedisKey(tokenId))
		if err != nil {
			// Soft cap: redis.Nil is the normal "no usage yet today" case. Any other error
			// means Redis is unreliable — fail OPEN (usage reads as 0, so the cap does not
			// reject), matching how RemainQuota tolerates cache lag, but log it so a silent
			// enforcement gap is at least observable.
			if !errors.Is(err, redis.Nil) {
				common.SysLog("failed to read token daily usage: " + err.Error())
			}
			return 0
		}
		used, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return 0
		}
		return used
	}
	tokenDailyUsageMu.Lock()
	ensureTokenDailyUsageDayLocked()
	used := tokenDailyUsageData[tokenId]
	tokenDailyUsageMu.Unlock()
	return used
}

// GetTokenDailyQuotaLimit returns the token's daily quota limit in quota units. 0 means
// unlimited (no limit configured). Reads are served from a short-lived cache (Redis or
// in-process) and fall back to the options table on a miss.
func GetTokenDailyQuotaLimit(tokenId int) (result int) {
	if common.RedisEnabled {
		// Serve from the L1 memo first; on a miss, memoize whatever value this call resolves
		// (via the named return) so the auth check + pre-consume + settle within a request, and
		// concurrent requests for the same token, share a single Redis round-trip. The store uses
		// SetNX semantics (if-absent) so a slow read-through never clobbers a concurrent
		// write-through; the fail-open branch opts out (memoize=false) so a transient Redis blip
		// does not poison the memo with 0 for the whole TTL.
		if limit, ok := getTokenDailyLimitMemo(tokenId); ok {
			return limit
		}
		memoize := true
		defer func() {
			if memoize {
				setTokenDailyLimitMemoIfAbsent(tokenId, result)
			}
		}()
		key := tokenDailyLimitRedisKey(tokenId)
		val, err := common.RedisGet(key)
		if err == nil {
			if limit, e := strconv.Atoi(val); e == nil {
				return limit
			}
			// Corrupt cached value (defensive; we only ever store integers): drop it so the
			// SetNX below repopulates from the DB rather than re-reading it every request.
			if delErr := common.RedisDel(key); delErr != nil {
				common.SysLog("failed to drop corrupt token daily quota limit cache: " + delErr.Error())
			}
		} else if !errors.Is(err, redis.Nil) {
			// Redis is unhealthy (not a normal cache miss). Do NOT fall through to the DB —
			// on every request that would hammer the options table while Redis is down. Fail
			// OPEN (treat as unlimited), consistent with GetTokenDailyUsage's soft-cap behavior.
			common.SysLog("token daily quota limit cache unavailable: " + err.Error())
			memoize = false
			return 0
		}
		limit := loadTokenDailyQuotaLimitFromDB(tokenId)
		// Populate with SetNX (not SET): a slow read-through must never clobber a fresh
		// write-through value that SetTokenDailyQuotaLimit may have set in between — that
		// would pin a stale limit for the whole TTL and silently mis-enforce the cap.
		if err := common.RDB.SetNX(context.Background(), key, strconv.Itoa(limit), tokenDailyLimitCacheTTL).Err(); err != nil {
			common.SysLog("failed to cache token daily quota limit: " + err.Error())
		}
		return limit
	}
	tokenDailyLimitMu.RLock()
	limit, ok := tokenDailyLimitCache[tokenId]
	tokenDailyLimitMu.RUnlock()
	if ok {
		return limit
	}
	limit = loadTokenDailyQuotaLimitFromDB(tokenId)
	tokenDailyLimitMu.Lock()
	// Only populate if still absent, so a concurrent write-through isn't clobbered.
	if _, exists := tokenDailyLimitCache[tokenId]; !exists {
		tokenDailyLimitCache[tokenId] = limit
	}
	tokenDailyLimitMu.Unlock()
	return limit
}

// loadTokenDailyQuotaLimitFromDB reads the persisted limit row; a missing row is treated
// as unlimited (0). Uses Find (not First) so a missing row is not logged as an error by
// GORM — this runs on the hot path for every uncached token, including unlimited ones.
func loadTokenDailyQuotaLimitFromDB(tokenId int) int {
	var option Option
	if err := DB.Where(commonKeyCol+" = ?", tokenDailyQuotaOptionKey(tokenId)).Limit(1).Find(&option).Error; err != nil {
		common.SysLog("failed to load token daily quota limit: " + err.Error())
		return 0
	}
	limit, err := strconv.Atoi(option.Value)
	if err != nil || limit < 0 {
		return 0
	}
	return limit
}

func writeTokenDailyQuotaLimitCache(tokenId int, limit int) {
	if common.RedisEnabled {
		if err := common.RedisSet(tokenDailyLimitRedisKey(tokenId), strconv.Itoa(limit), tokenDailyLimitCacheTTL); err != nil {
			common.SysLog("failed to cache token daily quota limit: " + err.Error())
		}
		// Refresh the L1 memo so the writing instance never serves a stale limit it just changed.
		setTokenDailyLimitMemo(tokenId, limit)
		return
	}
	tokenDailyLimitMu.Lock()
	tokenDailyLimitCache[tokenId] = limit
	tokenDailyLimitMu.Unlock()
}

func invalidateTokenDailyQuotaLimitCache(tokenId int) {
	if common.RedisEnabled {
		if err := common.RedisDel(tokenDailyLimitRedisKey(tokenId)); err != nil {
			common.SysLog("failed to invalidate token daily quota limit cache: " + err.Error())
		}
		deleteTokenDailyLimitMemo(tokenId)
		return
	}
	tokenDailyLimitMu.Lock()
	delete(tokenDailyLimitCache, tokenId)
	tokenDailyLimitMu.Unlock()
}

// setTokenDailyQuotaLimitTx performs the DB half of a limit write inside the given
// transaction (no cache side effects). limit <= 0 deletes the row (token becomes unlimited).
func setTokenDailyQuotaLimitTx(tx *gorm.DB, tokenId int, limit int) error {
	key := tokenDailyQuotaOptionKey(tokenId)
	if limit <= 0 {
		return tx.Where(commonKeyCol+" = ?", key).Delete(&Option{}).Error
	}
	option := Option{Key: key}
	if err := tx.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
		return err
	}
	option.Value = strconv.Itoa(limit)
	return tx.Save(&option).Error
}

// clearTokenDailyUsage drops today's usage counter for the token. Used when a limit is
// cleared or a token is removed; only today's key is touched (older keys expire on TTL).
func clearTokenDailyUsage(tokenId int) {
	if common.RedisEnabled {
		if err := common.RedisDel(tokenDailyUsageRedisKey(tokenId)); err != nil {
			common.SysLog("failed to clear token daily usage: " + err.Error())
		}
		return
	}
	tokenDailyUsageMu.Lock()
	ensureTokenDailyUsageDayLocked()
	delete(tokenDailyUsageData, tokenId)
	tokenDailyUsageMu.Unlock()
}

// finalizeTokenDailyLimitWrite refreshes the read cache after a limit has been persisted and,
// when the limit was cleared (normalized to 0), drops today's usage counter so a later
// re-enable starts counting from 0 instead of resuming a stale figure.
func finalizeTokenDailyLimitWrite(tokenId int, normalizedLimit int) {
	writeTokenDailyQuotaLimitCache(tokenId, normalizedLimit)
	if normalizedLimit <= 0 {
		clearTokenDailyUsage(tokenId)
	}
}

// SetTokenDailyQuotaLimit persists a token's daily quota limit (quota units). limit <= 0
// clears the limit (deletes the row, token becomes unlimited). Write-through: the DB row
// is committed first, then the read cache is refreshed.
func SetTokenDailyQuotaLimit(tokenId int, limit int) error {
	if err := setTokenDailyQuotaLimitTx(DB, tokenId, limit); err != nil {
		return err
	}
	normalized := limit
	if normalized < 0 {
		normalized = 0
	}
	finalizeTokenDailyLimitWrite(tokenId, normalized)
	return nil
}

// InsertTokenWithDailyQuotaLimit creates the token and, atomically in the same transaction,
// persists its daily quota limit when dailyLimit != nil. Either both commit or neither, so a
// created token is never reported as having (or lacking) a limit that was not actually saved.
func InsertTokenWithDailyQuotaLimit(token *Token, dailyLimit *int) error {
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(token).Error; err != nil {
			return err
		}
		if dailyLimit != nil {
			return setTokenDailyQuotaLimitTx(tx, token.Id, *dailyLimit)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if dailyLimit != nil {
		normalized := *dailyLimit
		if normalized < 0 {
			normalized = 0
		}
		finalizeTokenDailyLimitWrite(token.Id, normalized)
	}
	return nil
}

// UpdateTokenWithDailyQuotaLimit updates the token's core fields and, atomically in the same
// transaction, persists its daily quota limit when dailyLimit != nil, then refreshes caches.
// The single transaction avoids the partial commit that two separate writes would leave if
// one of them failed.
func UpdateTokenWithDailyQuotaLimit(token *Token, dailyLimit *int) error {
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(token).Select(tokenUpdateFields).Updates(token).Error; err != nil {
			return err
		}
		if dailyLimit != nil {
			return setTokenDailyQuotaLimitTx(tx, token.Id, *dailyLimit)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if shouldUpdateRedis(true, nil) {
		gopool.Go(func() {
			if err := cacheSetToken(*token); err != nil {
				common.SysLog("failed to update token cache: " + err.Error())
			}
		})
	}
	if dailyLimit != nil {
		normalized := *dailyLimit
		if normalized < 0 {
			normalized = 0
		}
		finalizeTokenDailyLimitWrite(token.Id, normalized)
	}
	return nil
}

// BatchSetTokenDailyQuotaLimit sets the daily quota limit for the caller's own tokens
// only. It verifies ownership (user_id + id IN ids), applies the change in one
// transaction, refreshes caches, and returns the number of tokens actually updated.
// limit <= 0 clears the limit for the matched tokens.
func BatchSetTokenDailyQuotaLimit(ids []int, userId int, limit int) (int, error) {
	if len(ids) == 0 {
		return 0, errors.New("ids 不能为空！")
	}
	var ownedIds []int
	if err := DB.Model(&Token{}).Where("user_id = ? AND id IN (?)", userId, ids).Pluck("id", &ownedIds).Error; err != nil {
		return 0, err
	}
	if len(ownedIds) == 0 {
		return 0, nil
	}
	keys := make([]string, len(ownedIds))
	for i, id := range ownedIds {
		keys[i] = tokenDailyQuotaOptionKey(id)
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if limit <= 0 {
			return tx.Where(commonKeyCol+" IN (?)", keys).Delete(&Option{}).Error
		}
		value := strconv.Itoa(limit)
		for _, key := range keys {
			option := Option{Key: key}
			if err := tx.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
				return err
			}
			option.Value = value
			if err := tx.Save(&option).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	normalized := limit
	if normalized < 0 {
		normalized = 0
	}
	for _, id := range ownedIds {
		finalizeTokenDailyLimitWrite(id, normalized)
	}
	return len(ownedIds), nil
}

// CheckTokenDailyQuota reports whether the token has reached its daily quota limit. A
// limit of 0 means unlimited, so exceeded is always false in that case. used/limit are
// returned so callers can build a descriptive (and localized) rejection message.
func CheckTokenDailyQuota(tokenId int) (exceeded bool, used int64, limit int) {
	limit = GetTokenDailyQuotaLimit(tokenId)
	if limit <= 0 {
		return false, 0, 0
	}
	used = GetTokenDailyUsage(tokenId)
	return used >= int64(limit), used, limit
}

// AttachTokenDailyQuota populates the transient DailyQuotaLimit / DailyQuotaUsed fields on
// the given tokens for API responses. Both limits and usage are read in one batched call
// each (limits: one DB query; usage: one Redis MGET / one locked map read), so a list page
// no longer fans out to N per-token round-trips.
func AttachTokenDailyQuota(tokens []*Token) {
	if len(tokens) == 0 {
		return
	}
	limits := batchGetTokenDailyQuotaLimits(tokens)
	ids := make([]int, 0, len(tokens))
	for _, token := range tokens {
		if token != nil {
			ids = append(ids, token.Id)
		}
	}
	usages := batchGetTokenDailyUsage(ids)
	for _, token := range tokens {
		if token == nil {
			continue
		}
		limit := limits[token.Id]
		used := usages[token.Id]
		token.DailyQuotaLimit = &limit
		token.DailyQuotaUsed = &used
	}
}

// batchGetTokenDailyUsage reads today's usage for many tokens in one round-trip (Redis
// MGET, or a single locked read of the in-process map), returning tokenId -> used units.
// Missing/unparseable entries map to 0.
func batchGetTokenDailyUsage(tokenIds []int) map[int]int64 {
	result := make(map[int]int64, len(tokenIds))
	if len(tokenIds) == 0 {
		return result
	}
	if common.RedisEnabled {
		keys := make([]string, len(tokenIds))
		for i, id := range tokenIds {
			keys[i] = tokenDailyUsageRedisKey(id)
		}
		vals, err := common.RDB.MGet(context.Background(), keys...).Result()
		if err != nil {
			if !errors.Is(err, redis.Nil) {
				common.SysLog("failed to batch read token daily usage: " + err.Error())
			}
			return result
		}
		for i, v := range vals {
			s, ok := v.(string)
			if !ok {
				continue
			}
			used, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				continue
			}
			result[tokenIds[i]] = used
		}
		return result
	}
	tokenDailyUsageMu.Lock()
	ensureTokenDailyUsageDayLocked()
	for _, id := range tokenIds {
		result[id] = tokenDailyUsageData[id]
	}
	tokenDailyUsageMu.Unlock()
	return result
}

// batchGetTokenDailyQuotaLimits reads persisted limits for many tokens in a single query,
// returning a tokenId -> limit map (tokens without a row map to 0).
func batchGetTokenDailyQuotaLimits(tokens []*Token) map[int]int {
	result := make(map[int]int, len(tokens))
	keyToId := make(map[string]int, len(tokens))
	keys := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token == nil {
			continue
		}
		key := tokenDailyQuotaOptionKey(token.Id)
		if _, ok := keyToId[key]; ok {
			continue
		}
		keyToId[key] = token.Id
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return result
	}
	var options []Option
	if err := DB.Where(commonKeyCol+" IN (?)", keys).Find(&options).Error; err != nil {
		common.SysLog("failed to batch load token daily quota limits: " + err.Error())
		return result
	}
	for _, option := range options {
		id, ok := keyToId[option.Key]
		if !ok {
			continue
		}
		limit, err := strconv.Atoi(option.Value)
		if err != nil || limit < 0 {
			continue
		}
		result[id] = limit
	}
	return result
}

// RemoveTokenDailyQuotaConfig deletes the persisted daily-limit rows and cache entries for
// the given tokens. Best-effort: errors are logged, not returned (used on token delete).
func RemoveTokenDailyQuotaConfig(tokenIds ...int) {
	if len(tokenIds) == 0 {
		return
	}
	keys := make([]string, len(tokenIds))
	for i, id := range tokenIds {
		keys[i] = tokenDailyQuotaOptionKey(id)
	}
	if err := DB.Where(commonKeyCol+" IN (?)", keys).Delete(&Option{}).Error; err != nil {
		common.SysLog("failed to remove token daily quota config: " + err.Error())
	}
	for _, id := range tokenIds {
		invalidateTokenDailyQuotaLimitCache(id)
		clearTokenDailyUsage(id)
	}
}
