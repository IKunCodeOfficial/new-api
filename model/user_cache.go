package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

const userCacheSchemaVersion = 2

type UserBase struct {
	Id             int    `json:"id"`
	Group          string `json:"group"`
	Email          string `json:"email"`
	Quota          int    `json:"quota"`
	Status         int    `json:"status"`
	Role           int    `json:"role"`
	Username       string `json:"username"`
	Setting        string `json:"setting"`
	AuthVersion    int64  `json:"-"`
	CacheSchema    int    `json:"-"`
	CacheExpiresAt int64  `json:"-"`
	CacheMAC       string `json:"-"`
}

func (user UserBase) cacheMAC() (string, error) {
	user.Quota = 0
	return authCacheMAC(getUserCacheKey(user.Id), struct {
		User           UserBase
		AuthVersion    int64
		CacheSchema    int
		CacheExpiresAt int64
	}{user, user.AuthVersion, user.CacheSchema, user.CacheExpiresAt})
}

func (user *UserBase) WriteContext(c *gin.Context) {
	common.SetContextKey(c, constant.ContextKeyUserGroup, user.Group)
	common.SetContextKey(c, constant.ContextKeyUserQuota, user.Quota)
	common.SetContextKey(c, constant.ContextKeyUserStatus, user.Status)
	common.SetContextKey(c, constant.ContextKeyUserEmail, user.Email)
	common.SetContextKey(c, constant.ContextKeyUserName, user.Username)
	common.SetContextKey(c, constant.ContextKeyUserSetting, user.GetSetting())
}

func (user *UserBase) GetSetting() dto.UserSetting {
	setting := dto.UserSetting{}
	if user.Setting != "" {
		err := common.Unmarshal([]byte(user.Setting), &setting)
		if err != nil {
			common.SysLog(common.LogText("failed to unmarshal setting: %s", err.Error()))
		}
	}
	return setting
}

// getUserCacheKey returns the key for user cache
func getUserCacheKey(userId int) string {
	return fmt.Sprintf("user:%d", userId)
}

func userCacheTTLSeconds() int {
	ttl := common.RedisKeyCacheSeconds()
	if ttl <= 0 {
		return 60
	}
	return ttl
}

// invalidateUserCache clears user cache
func invalidateUserCache(userId int) error {
	if !common.RedisEnabled {
		return nil
	}
	return common.RedisDelKey(getUserCacheKey(userId))
}

func populateUserCache(user User) error {
	if !common.RedisEnabled {
		return nil
	}
	return writeUserCache(user.ToBaseUser(), true)
}

// updateUserCache refreshes non-quota user cache fields.
// Quota is a best-effort database snapshot, refreshed by wallet mutations.
// Profile/settings updates must not replace it with their older user snapshot.
func updateUserCache(user User) error {
	if !common.RedisEnabled {
		return nil
	}
	return writeUserCache(user.ToBaseUser(), false)
}

// GetUserCache gets complete user cache from hash
func GetUserCache(userId int) (*UserBase, error) {
	// Try getting from Redis first
	userCache, err := cacheGetUserBase(userId)
	if err == nil {
		return userCache, nil
	}

	// Redis misses and read failures both fall back to the shared database. A
	// version fence newer than the database is the one exception: allowing that
	// snapshot would re-authorize a user while a restrictive update is pending.
	user, err := GetUserById(userId, false)
	if err != nil {
		return nil, err
	}
	if common.RedisEnabled {
		floor, floorErr := getUserAuthVersionFloor(userId)
		if floorErr == nil && floor > user.AuthVersion {
			return nil, ErrUserAuthCachePending
		}
		if err := populateUserCache(*user); err != nil {
			if errors.Is(err, ErrUserAuthCachePending) {
				return nil, err
			}
			common.SysLog(common.LogText("failed to synchronously populate user cache: %s", err.Error()))
		}
	}
	return user.ToBaseUser(), nil
}

func cacheGetUserBase(userId int) (*UserBase, error) {
	if !common.RedisEnabled {
		return nil, fmt.Errorf("redis is not enabled")
	}
	var userCache UserBase
	// Try getting from Redis first
	err := common.RedisHGetObj(getUserCacheKey(userId), &userCache)
	if err != nil {
		return nil, err
	}
	if userCache.Id != userId || userCache.CacheSchema != userCacheSchemaVersion || userCache.AuthVersion <= 0 {
		return nil, fmt.Errorf("user cache schema is stale")
	}
	mac, err := userCache.cacheMAC()
	if err != nil || !validAuthCacheMAC(mac, userCache.CacheMAC, userCache.CacheExpiresAt) {
		return nil, fmt.Errorf("user cache integrity check failed")
	}
	floor, err := getUserAuthVersionFloor(userId)
	if err != nil {
		return nil, err
	}
	if floor > userCache.AuthVersion {
		return nil, ErrUserAuthCachePending
	}
	return &userCache, nil
}

// refreshUserQuotaCache publishes a display snapshot only. Wallet reads and
// reservations always use the database, so delayed cache writes cannot grant
// spending authority. A newly hydrated hash may already include a credit.
func refreshUserQuotaCache(userId int) error {
	if !common.RedisEnabled {
		return nil
	}
	quota, err := GetUserQuota(userId, true)
	if err != nil {
		return err
	}
	const script = `
if tonumber(redis.call('HGET', KEYS[1], 'Id') or '0') ~= tonumber(ARGV[2])
  or tonumber(redis.call('HGET', KEYS[1], 'CacheSchema') or '0') ~= tonumber(ARGV[3])
  or redis.call('HEXISTS', KEYS[1], 'Quota') == 0 then
  return 0
end
redis.call('HSET', KEYS[1], 'Quota', ARGV[1])
return 1`
	return common.RDB.Eval(context.Background(), script,
		[]string{getUserCacheKey(userId)}, quota, userId, userCacheSchemaVersion).Err()
}

// syncCreditUserQuotaCache refreshes the optional snapshot after a committed
// recharge or transfer. Cache failures never undo or repeat the database credit.
func syncCreditUserQuotaCache(userId int, quota int, operation string) {
	if quota <= 0 {
		return
	}
	if err := refreshUserQuotaCache(userId); err != nil {
		common.SysLog(common.LogText("failed to sync %s credit to user quota cache: %s", operation, err.Error()))
	}
}

// Helper functions to get individual fields if needed
func getUserGroupCache(userId int) (string, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return "", err
	}
	return cache.Group, nil
}

func getUserQuotaCache(userId int) (int, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return 0, err
	}
	return cache.Quota, nil
}

func getUserNameCache(userId int) (string, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return "", err
	}
	return cache.Username, nil
}

func getUserSettingCache(userId int) (dto.UserSetting, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return dto.UserSetting{}, err
	}
	return cache.GetSetting(), nil
}

// RefreshUserGroupCache publishes current user metadata after a group change.
func RefreshUserGroupCache(userId int) error {
	return PublishUserAuthCache(userId)
}

// GetUserLanguage returns the user's language preference from cache
// Uses the existing GetUserCache mechanism for efficiency
func GetUserLanguage(userId int) string {
	userCache, err := GetUserCache(userId)
	if err != nil {
		return ""
	}
	return userCache.GetSetting().Language
}
