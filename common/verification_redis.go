package common

import (
	"errors"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

// Verification codes must be visible to every node in a cluster: the node
// that sends the email and the node that handles the verify request can
// differ behind a load balancer. When Redis is enabled it is the primary
// store; the in-memory map in verification.go remains the fallback for
// single-node deployments and transient Redis outages.

func redisVerificationKey(key string, purpose string) string {
	return fmt.Sprintf("verification:%s:%s", purpose, key)
}

func redisRegisterVerificationCode(key string, code string, purpose string) error {
	return RedisSet(redisVerificationKey(key, purpose), code, time.Duration(VerificationValidMinutes)*time.Minute)
}

func redisVerifyCode(key string, code string, purpose string) (matched bool, found bool, err error) {
	storedCode, err := RedisGet(redisVerificationKey(key, purpose))
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, false, nil
		}
		return false, false, err
	}
	return code == storedCode, true, nil
}

func redisDeleteVerificationCode(key string, purpose string) error {
	return RedisDel(redisVerificationKey(key, purpose))
}
