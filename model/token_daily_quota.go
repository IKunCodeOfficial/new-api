package model

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
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

func tokenDailyUsageDate() string {
	return time.Now().Format("2006-01-02")
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

// AddTokenDailyUsage adds delta (quota units; negative for refunds) to today's usage
// counter for the token. Best-effort: failures are logged, never returned, so token
// billing is never blocked by daily-quota bookkeeping. The counter is floored at 0 so a
// refund whose original charge landed on an earlier day cannot create a negative "credit"
// that would under-enforce the next day's cap.
func AddTokenDailyUsage(tokenId int, delta int64) {
	if delta == 0 {
		return
	}
	if common.RedisEnabled {
		ctx := context.Background()
		key := tokenDailyUsageRedisKey(tokenId)
		newVal, err := common.RDB.IncrBy(ctx, key, delta).Result()
		if err != nil {
			common.SysLog("failed to incr token daily usage: " + err.Error())
			return
		}
		// An absent key reads as 0, so the increment that just created the key returns a
		// value equal to delta; that is the one (and only) time we must set the expiry.
		if newVal == delta {
			if err := common.RDB.Expire(ctx, key, tokenDailyUsageTTL).Err(); err != nil {
				common.SysLog("failed to set token daily usage ttl: " + err.Error())
			}
		}
		// Floor at 0 (best-effort; INCRBY preserves the TTL).
		if newVal < 0 {
			if err := common.RDB.IncrBy(ctx, key, -newVal).Err(); err != nil {
				common.SysLog("failed to floor token daily usage: " + err.Error())
			}
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
			// Missing key (redis.Nil) or any transient error reads as 0.
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
func GetTokenDailyQuotaLimit(tokenId int) int {
	if common.RedisEnabled {
		key := tokenDailyLimitRedisKey(tokenId)
		if val, err := common.RedisGet(key); err == nil {
			if limit, e := strconv.Atoi(val); e == nil {
				return limit
			}
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
		return
	}
	tokenDailyLimitMu.Lock()
	delete(tokenDailyLimitCache, tokenId)
	tokenDailyLimitMu.Unlock()
}

// SetTokenDailyQuotaLimit persists a token's daily quota limit (quota units). limit <= 0
// clears the limit (deletes the row, token becomes unlimited). Write-through: the DB row
// is committed first, then the read cache is refreshed.
func SetTokenDailyQuotaLimit(tokenId int, limit int) error {
	key := tokenDailyQuotaOptionKey(tokenId)
	if limit <= 0 {
		if err := DB.Where(commonKeyCol+" = ?", key).Delete(&Option{}).Error; err != nil {
			return err
		}
		writeTokenDailyQuotaLimitCache(tokenId, 0)
		return nil
	}
	option := Option{Key: key}
	if err := DB.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
		return err
	}
	option.Value = strconv.Itoa(limit)
	if err := DB.Save(&option).Error; err != nil {
		return err
	}
	writeTokenDailyQuotaLimitCache(tokenId, limit)
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
		writeTokenDailyQuotaLimitCache(id, normalized)
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
// the given tokens for API responses. Limits are read in one batched DB query; usage is
// read per token from the (fast) cache.
func AttachTokenDailyQuota(tokens []*Token) {
	if len(tokens) == 0 {
		return
	}
	limits := batchGetTokenDailyQuotaLimits(tokens)
	for _, token := range tokens {
		if token == nil {
			continue
		}
		limit := limits[token.Id]
		used := GetTokenDailyUsage(token.Id)
		token.DailyQuotaLimit = &limit
		token.DailyQuotaUsed = &used
	}
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
	}
}
