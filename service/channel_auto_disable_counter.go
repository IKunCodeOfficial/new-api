package service

import (
	"context"
	"fmt"
	"hash/fnv"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/bytedance/gopkg/util/gopool"
)

const (
	autoDisableCounterWindow          = 30 * time.Minute
	autoDisableResetThrottle          = time.Second
	autoDisableCounterCleanupInterval = 10 * time.Minute
)

type autoDisableCounterEntry struct {
	hits    atomic.Int64
	lastHit atomic.Int64
}

var (
	autoDisableCounters           sync.Map
	autoDisableResetThrottles     sync.Map
	autoDisableCounterCleanupOnce sync.Once
)

func autoDisableCounterKey(channelId int, isMultiKey bool, usingKey string) string {
	key := "auto_disable_hits:" + strconv.Itoa(channelId)
	if !isMultiKey {
		return key
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(usingKey))
	return key + ":" + strconv.FormatUint(hash.Sum64(), 10)
}

func recordAutoDisableHit(key string, threshold int) (bool, int64) {
	startAutoDisableCounterCleanup()
	if common.RedisEnabled {
		if common.RDB == nil {
			common.SysError("record auto-disable hit in Redis failed: Redis client is nil")
		} else {
			ctx := context.Background()
			pipe := common.RDB.TxPipeline()
			incr := pipe.Incr(ctx, key)
			pipe.Expire(ctx, key, autoDisableCounterWindow)
			if _, err := pipe.Exec(ctx); err == nil {
				hits := incr.Val()
				return hits >= int64(threshold), hits
			} else {
				common.SysError("record auto-disable hit in Redis failed, falling back to memory: " + err.Error())
			}
		}
	}

	return recordMemoryAutoDisableHit(key, threshold)
}

func recordMemoryAutoDisableHit(key string, threshold int) (bool, int64) {
	value, _ := autoDisableCounters.LoadOrStore(key, &autoDisableCounterEntry{})
	entry := value.(*autoDisableCounterEntry)
	now := time.Now().Unix()
	windowSeconds := int64(autoDisableCounterWindow / time.Second)

	for {
		lastHit := entry.lastHit.Load()
		if lastHit < 0 {
			runtime.Gosched()
			continue
		}
		if lastHit > 0 && now-lastHit > windowSeconds {
			if !entry.lastHit.CompareAndSwap(lastHit, -now) {
				continue
			}
			entry.hits.Store(0)
			entry.lastHit.Store(now)
			break
		}
		if entry.lastHit.CompareAndSwap(lastHit, now) {
			break
		}
	}

	hits := entry.hits.Add(1)
	return hits >= int64(threshold), hits
}

func startAutoDisableCounterCleanup() {
	autoDisableCounterCleanupOnce.Do(func() {
		gopool.Go(func() {
			ticker := time.NewTicker(autoDisableCounterCleanupInterval)
			defer ticker.Stop()
			for now := range ticker.C {
				cutoff := now.Add(-autoDisableCounterWindow).Unix()
				resetCutoff := now.Add(-autoDisableCounterWindow).UnixNano()
				autoDisableCounters.Range(func(key, value any) bool {
					lastHit := value.(*autoDisableCounterEntry).lastHit.Load()
					if lastHit > 0 && lastHit < cutoff {
						autoDisableCounters.Delete(key)
					}
					return true
				})
				autoDisableResetThrottles.Range(func(key, value any) bool {
					lastReset := value.(*atomic.Int64).Load()
					if lastReset > 0 && lastReset < resetCutoff {
						autoDisableResetThrottles.Delete(key)
					}
					return true
				})
			}
		})
	})
}

func resetAutoDisableCounter(key string) {
	autoDisableCounters.Delete(key)
	if !common.RedisEnabled {
		return
	}
	if common.RDB == nil {
		common.SysError("reset auto-disable counter in Redis failed: Redis client is nil")
		return
	}
	if err := common.RDB.Del(context.Background(), key).Err(); err != nil {
		common.SysError("reset auto-disable counter in Redis failed: " + err.Error())
	}
}

func ResetChannelAutoDisableCounter(channelId int, isMultiKey bool, usingKey string) {
	if operation_setting.GetAutomaticDisableConsecutiveThreshold() <= 1 {
		return
	}
	startAutoDisableCounterCleanup()

	key := autoDisableCounterKey(channelId, isMultiKey, usingKey)
	if !common.RedisEnabled {
		if _, ok := autoDisableCounters.Load(key); !ok {
			return
		}
	}

	now := time.Now().UnixNano()
	value, ok := autoDisableResetThrottles.Load(key)
	if !ok {
		value, _ = autoDisableResetThrottles.LoadOrStore(key, &atomic.Int64{})
	}
	lastReset := value.(*atomic.Int64)
	for {
		previous := lastReset.Load()
		if previous > 0 && now-previous < autoDisableResetThrottle.Nanoseconds() {
			return
		}
		if lastReset.CompareAndSwap(previous, now) {
			break
		}
	}

	resetAutoDisableCounter(key)
}

func ProcessChannelDisableHit(channelError types.ChannelError, reason string) {
	threshold := operation_setting.GetAutomaticDisableConsecutiveThreshold()
	if threshold <= 1 {
		DisableChannel(channelError, reason)
		return
	}

	key := autoDisableCounterKey(channelError.ChannelId, channelError.IsMultiKey, channelError.UsingKey)
	reached, hits := recordAutoDisableHit(key, threshold)
	if !reached {
		common.SysLog(fmt.Sprintf("渠道 #%d 命中自动禁用规则 %d/%d 次：%s", channelError.ChannelId, hits, threshold, common.LocalLogPreview(reason)))
		return
	}

	resetAutoDisableCounter(key)
	DisableChannel(channelError, fmt.Sprintf("连续命中自动禁用规则 %d 次，最后错误：%s", hits, reason))
}
