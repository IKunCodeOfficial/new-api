package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
)

func clearAutoDisableCounterTestState() {
	autoDisableCounters.Range(func(key, _ any) bool {
		autoDisableCounters.Delete(key)
		return true
	})
	autoDisableResetThrottles.Range(func(key, _ any) bool {
		autoDisableResetThrottles.Delete(key)
		return true
	})
}

func useMemoryAutoDisableCounters(t *testing.T, threshold int) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	previousThreshold := operation_setting.AutomaticDisableConsecutiveThresholdToString()
	clearAutoDisableCounterTestState()
	common.RedisEnabled = false
	common.RDB = nil
	operation_setting.AutomaticDisableConsecutiveThresholdFromString(fmt.Sprintf("%d", threshold))
	t.Cleanup(func() {
		clearAutoDisableCounterTestState()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
		operation_setting.AutomaticDisableConsecutiveThresholdFromString(previousThreshold)
	})
}

func useRedisAutoDisableCounters(t *testing.T, threshold int) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	previousThreshold := operation_setting.AutomaticDisableConsecutiveThresholdToString()
	clearAutoDisableCounterTestState()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	require.NoError(t, client.Ping(context.Background()).Err())
	common.RedisEnabled = true
	common.RDB = client
	operation_setting.AutomaticDisableConsecutiveThresholdFromString(fmt.Sprintf("%d", threshold))

	t.Cleanup(func() {
		_ = client.Close()
		clearAutoDisableCounterTestState()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
		operation_setting.AutomaticDisableConsecutiveThresholdFromString(previousThreshold)
	})
	return server, client
}

func TestMemoryAutoDisableCounterThresholdAndReset(t *testing.T) {
	useMemoryAutoDisableCounters(t, 3)
	key := autoDisableCounterKey(1001, false, "")

	reached, hits := recordAutoDisableHit(key, 3)
	assert.False(t, reached)
	assert.Equal(t, int64(1), hits)
	reached, hits = recordAutoDisableHit(key, 3)
	assert.False(t, reached)
	assert.Equal(t, int64(2), hits)
	reached, hits = recordAutoDisableHit(key, 3)
	assert.True(t, reached)
	assert.Equal(t, int64(3), hits)

	resetAutoDisableCounter(key)
	reached, hits = recordAutoDisableHit(key, 3)
	assert.False(t, reached)
	assert.Equal(t, int64(1), hits)
}

func TestChannelAutoDisableCounterSuccessReset(t *testing.T) {
	useMemoryAutoDisableCounters(t, 3)
	const channelID = 1002
	key := autoDisableCounterKey(channelID, false, "")

	_, _ = recordAutoDisableHit(key, 3)
	_, _ = recordAutoDisableHit(key, 3)
	ResetChannelAutoDisableCounter(channelID, false, "")

	reached, hits := recordAutoDisableHit(key, 3)
	assert.False(t, reached)
	assert.Equal(t, int64(1), hits)
}

func TestMemoryAutoDisableCounterExpiresAfterObservationWindow(t *testing.T) {
	useMemoryAutoDisableCounters(t, 3)
	key := autoDisableCounterKey(1003, false, "")
	_, _ = recordAutoDisableHit(key, 3)
	_, _ = recordAutoDisableHit(key, 3)

	value, ok := autoDisableCounters.Load(key)
	require.True(t, ok)
	entry := value.(*autoDisableCounterEntry)
	entry.lastHit.Store(time.Now().Add(-autoDisableCounterWindow - time.Second).Unix())

	reached, hits := recordAutoDisableHit(key, 3)
	assert.False(t, reached)
	assert.Equal(t, int64(1), hits)
}

func TestMemoryAutoDisableCounterConcurrentHitsAreNotLost(t *testing.T) {
	useMemoryAutoDisableCounters(t, 20)
	key := autoDisableCounterKey(1004, false, "")
	const workers = 64
	var reachedCount atomic.Int64
	var waitGroup sync.WaitGroup
	waitGroup.Add(workers)
	for range workers {
		go func() {
			defer waitGroup.Done()
			reached, _ := recordAutoDisableHit(key, 20)
			if reached {
				reachedCount.Add(1)
			}
		}()
	}
	waitGroup.Wait()

	value, ok := autoDisableCounters.Load(key)
	require.True(t, ok)
	assert.Equal(t, int64(workers), value.(*autoDisableCounterEntry).hits.Load())
	assert.Equal(t, int64(workers-20+1), reachedCount.Load())
}

func TestRedisAutoDisableCounterRefreshesTTLAndResets(t *testing.T) {
	server, _ := useRedisAutoDisableCounters(t, 3)
	key := autoDisableCounterKey(1005, false, "")

	reached, hits := recordAutoDisableHit(key, 3)
	assert.False(t, reached)
	assert.Equal(t, int64(1), hits)
	assert.Equal(t, autoDisableCounterWindow, server.TTL(key))

	server.FastForward(10 * time.Minute)
	reached, hits = recordAutoDisableHit(key, 3)
	assert.False(t, reached)
	assert.Equal(t, int64(2), hits)
	assert.Equal(t, autoDisableCounterWindow, server.TTL(key))

	reached, hits = recordAutoDisableHit(key, 3)
	assert.True(t, reached)
	assert.Equal(t, int64(3), hits)
	resetAutoDisableCounter(key)
	assert.False(t, server.Exists(key))
}

func TestRedisAutoDisableCounterFallsBackToMemory(t *testing.T) {
	_, client := useRedisAutoDisableCounters(t, 2)
	require.NoError(t, client.Close())
	key := autoDisableCounterKey(1006, false, "")

	reached, hits := recordAutoDisableHit(key, 2)
	assert.False(t, reached)
	assert.Equal(t, int64(1), hits)
	reached, hits = recordAutoDisableHit(key, 2)
	assert.True(t, reached)
	assert.Equal(t, int64(2), hits)
}

func TestAutoDisableCounterKeySeparatesMultiKeysWithoutExposingSecrets(t *testing.T) {
	plain := autoDisableCounterKey(1007, false, "secret-a")
	multiA := autoDisableCounterKey(1007, true, "secret-a")
	multiB := autoDisableCounterKey(1007, true, "secret-b")

	assert.Equal(t, "auto_disable_hits:1007", plain)
	assert.NotEqual(t, plain, multiA)
	assert.NotEqual(t, multiA, multiB)
	assert.False(t, strings.Contains(multiA, "secret-a"))
}

func TestProcessChannelDisableHitUsesThresholdAndClearsAfterDisable(t *testing.T) {
	useMemoryAutoDisableCounters(t, 3)
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
	})

	const channelID = 991001
	channel := &model.Channel{Id: channelID, Name: "threshold-test", Key: "sk-test", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(channel).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Delete(&model.Channel{}, channelID).Error)
	})
	channelError := types.ChannelError{ChannelId: channelID, ChannelName: channel.Name, AutoBan: true}

	ProcessChannelDisableHit(channelError, "upstream failure")
	ProcessChannelDisableHit(channelError, "upstream failure")
	require.NoError(t, model.DB.First(channel, channelID).Error)
	assert.Equal(t, common.ChannelStatusEnabled, channel.Status)

	ProcessChannelDisableHit(channelError, "upstream failure")
	require.NoError(t, model.DB.First(channel, channelID).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, channel.Status)

	require.NoError(t, model.DB.Model(channel).Update("status", common.ChannelStatusEnabled).Error)
	ProcessChannelDisableHit(channelError, "upstream failure")
	require.NoError(t, model.DB.First(channel, channelID).Error)
	assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
}

func TestThresholdOneDisablesImmediatelyWithoutCounterState(t *testing.T) {
	useMemoryAutoDisableCounters(t, 1)
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCacheEnabled
	})

	const channelID = 991002
	channel := &model.Channel{Id: channelID, Name: "immediate-test", Key: "sk-test", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(channel).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Delete(&model.Channel{}, channelID).Error)
	})
	channelError := types.ChannelError{ChannelId: channelID, ChannelName: channel.Name, AutoBan: true}

	ProcessChannelDisableHit(channelError, "upstream failure")
	require.NoError(t, model.DB.First(channel, channelID).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, channel.Status)
	_, counterExists := autoDisableCounters.Load(autoDisableCounterKey(channelID, false, ""))
	_, throttleExists := autoDisableResetThrottles.Load(autoDisableCounterKey(channelID, false, ""))
	assert.False(t, counterExists)
	assert.False(t, throttleExists)
}

func TestRequestPolicyResetsDisableHitsOnlyAfterRealSuccess(t *testing.T) {
	for _, tc := range []struct {
		name      string
		outcome   relaycommon.ResponseOutcome
		wantReset bool
	}{
		{"completed", relaycommon.ResponseOutcomeCompleted, true},
		{"failed HTTP 200 stream", relaycommon.ResponseOutcomeFailed, false},
		{"incomplete", relaycommon.ResponseOutcomeIncomplete, false},
		{"missing terminal", relaycommon.ResponseOutcomeUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useMemoryAutoDisableCounters(t, 3)
			const channelID = 9103
			key := autoDisableCounterKey(channelID, false, "")
			_, _ = recordAutoDisableHit(key, 3)
			_, _ = recordAutoDisableHit(key, 3)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set("channel_id", channelID)
			stream := relaycommon.NewStreamStatus()
			stream.RequireTerminal()
			stream.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
			switch tc.outcome {
			case relaycommon.ResponseOutcomeCompleted:
				stream.MarkCompleted()
			case relaycommon.ResponseOutcomeFailed:
				stream.MarkFailed("server_error", "server_error", 500)
			case relaycommon.ResponseOutcomeIncomplete:
				stream.MarkIncomplete("max_output_tokens")
			}
			MarkRequestPolicySuccess(c, stream)
			assert.Equal(t, tc.wantReset, RequestPolicy(c).Successful)
			reached, hits := recordAutoDisableHit(key, 3)
			assert.Equal(t, !tc.wantReset, reached)
			if tc.wantReset {
				assert.Equal(t, int64(1), hits)
			} else {
				assert.Equal(t, int64(3), hits)
			}
		})
	}
}
