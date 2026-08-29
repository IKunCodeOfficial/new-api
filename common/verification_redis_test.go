package common

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useVerificationMiniRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()

	previousRedisEnabled := RedisEnabled
	previousRedisClient := RDB
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	require.NoError(t, redisClient.Ping(context.Background()).Err())

	RedisEnabled = true
	RDB = redisClient
	t.Cleanup(func() {
		_ = redisClient.Close()
		RedisEnabled = previousRedisEnabled
		RDB = previousRedisClient
	})

	return redisServer
}

func clearVerificationMemoryStore() {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	verificationMap = make(map[string]verificationValue)
}

func TestVerificationCodeSharedAcrossNodesViaRedis(t *testing.T) {
	useVerificationMiniRedis(t)
	clearVerificationMemoryStore()
	t.Cleanup(clearVerificationMemoryStore)

	RegisterVerificationCodeWithKey("user@example.com", "123456", EmailVerificationPurpose)

	// Simulate the verify request landing on a different node: the local
	// in-memory store is empty and only Redis holds the code.
	clearVerificationMemoryStore()

	assert.True(t, VerifyCodeWithKey("user@example.com", "123456", EmailVerificationPurpose))
	assert.False(t, VerifyCodeWithKey("user@example.com", "654321", EmailVerificationPurpose))
	assert.False(t, VerifyCodeWithKey("user@example.com", "123456", PasswordResetPurpose))
	assert.False(t, VerifyCodeWithKey("other@example.com", "123456", EmailVerificationPurpose))
}

func TestVerificationCodeExpiresInRedis(t *testing.T) {
	redisServer := useVerificationMiniRedis(t)
	clearVerificationMemoryStore()
	t.Cleanup(clearVerificationMemoryStore)

	RegisterVerificationCodeWithKey("user@example.com", "123456", EmailVerificationPurpose)

	redisKey := redisVerificationKey("user@example.com", EmailVerificationPurpose)
	require.True(t, redisServer.Exists(redisKey))
	require.Greater(t, redisServer.TTL(redisKey), time.Duration(0))

	redisServer.FastForward(time.Duration(VerificationValidMinutes)*time.Minute + time.Second)

	assert.False(t, VerifyCodeWithKey("user@example.com", "123456", EmailVerificationPurpose))
}

func TestDeleteKeyRemovesCodeFromRedis(t *testing.T) {
	useVerificationMiniRedis(t)
	clearVerificationMemoryStore()
	t.Cleanup(clearVerificationMemoryStore)

	RegisterVerificationCodeWithKey("user@example.com", "123456", PasswordResetPurpose)
	DeleteKey("user@example.com", PasswordResetPurpose)

	assert.False(t, VerifyCodeWithKey("user@example.com", "123456", PasswordResetPurpose))
}

func TestVerificationFallsBackToMemoryWhenRedisUnavailable(t *testing.T) {
	redisServer := useVerificationMiniRedis(t)
	clearVerificationMemoryStore()
	t.Cleanup(clearVerificationMemoryStore)

	// Simulate a Redis outage: register and verify must still work on a
	// single node through the in-memory fallback.
	redisServer.Close()

	RegisterVerificationCodeWithKey("user@example.com", "123456", EmailVerificationPurpose)

	assert.True(t, VerifyCodeWithKey("user@example.com", "123456", EmailVerificationPurpose))
	assert.False(t, VerifyCodeWithKey("user@example.com", "999999", EmailVerificationPurpose))
}
