package modelstatus

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

func TestHistIndexAndPercentile(t *testing.T) {
	require.Equal(t, 0, histIndex(0))
	require.Equal(t, 0, histIndex(100))
	require.Equal(t, 1, histIndex(101))
	require.Equal(t, 19, histIndex(999999))

	var h [HistBins]int64
	require.Equal(t, int64(0), Percentile(h, 0.5))
	h[histIndex(250)] = 90
	h[histIndex(4000)] = 10
	require.Equal(t, int64(300), Percentile(h, 0.5))
	require.Equal(t, int64(5000), Percentile(h, 0.95))
	require.Equal(t, int64(5000), Percentile(h, 0.99))

	var last [HistBins]int64
	last[HistBins-1] = 1
	require.Equal(t, int64(120000), Percentile(last, 0.5))
}

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		status   int
		code     string
		errType  string
		timedOut bool
		want     FailureKind
	}{
		{429, "", "", false, Failure429},
		{0, "rate_limit_exceeded", "", false, Failure429},
		{0, "", "insufficient_quota", false, Failure429},
		{504, "", "", false, FailureTimeout},
		{524, "", "", false, FailureTimeout},
		{0, "", "", true, FailureTimeout},
		{502, "", "", false, Failure5xx},
		{0, "", "overloaded_error", false, Failure5xx},
		{401, "invalid_api_key", "", false, FailureOther},
		{0, "", "", false, FailureOther},
		{429, "", "", true, FailureTimeout},
		{503, "rate_limit_error", "", false, Failure429},
		{0, "Request_Timeout", "", false, FailureTimeout},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, ClassifyFailure(tt.status, tt.code, tt.errType, tt.timedOut), "%+v", tt)
	}
	require.Equal(t, "5xx", Failure5xx.Key())
	require.Equal(t, "429", Failure429.Key())
	require.Equal(t, "timeout", FailureTimeout.Key())
	require.Equal(t, "other", FailureOther.Key())
}

func TestCountersAddSample(t *testing.T) {
	var c Counters
	c.AddSample(Sample{Success: false, LatencyMs: 50, Failure: FailureNone})
	require.Equal(t, int64(1), c.Req)
	require.Equal(t, int64(0), c.Ok)
	require.Equal(t, int64(1), c.Fail[FailureOther-1])

	c.AddSample(Sample{Success: true, LatencyMs: 200, Failure: Failure5xx})
	require.Equal(t, int64(1), c.Ok)
	require.Equal(t, int64(0), c.Fail[Failure5xx-1])
	require.Equal(t, int64(250), c.LatSum)
	require.Equal(t, int64(0), c.TtftN)
	require.Equal(t, [HistBins]int64{}, c.TtftHist)

	c.AddSample(Sample{Success: true, LatencyMs: 10, HasTtft: true, TtftMs: 150, OutputTokens: 5})
	require.Equal(t, int64(1), c.TtftN)
	require.Equal(t, int64(150), c.TtftSum)
	require.Equal(t, int64(1), c.TtftHist[histIndex(150)])
	require.Equal(t, int64(0), c.Out)
	c.AddSample(Sample{Success: true, OutputTokens: 5, GenerationMs: 100})
	require.Equal(t, int64(5), c.Out)
	require.Equal(t, int64(100), c.GenMs)

	var d Counters
	d.Merge(c)
	d.Merge(c)
	require.Equal(t, c.Req*2, d.Req)
	require.Equal(t, c.Fail[FailureOther-1]*2, d.Fail[FailureOther-1])
	require.Equal(t, c.LatHist[0]*2, d.LatHist[0])
}

// setModuleState pins Redis and the HeaderNavModules option for one test and
// forces ModuleEnabled to re-read the option.
func setModuleState(t *testing.T, redisEnabled bool, rdb *redis.Client, navModules string) {
	t.Helper()
	oldRedis, oldRDB := common.RedisEnabled, common.RDB
	common.OptionMapRWMutex.Lock()
	oldMap := common.OptionMap
	oldNav, hadNav := oldMap["HeaderNavModules"]
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	common.OptionMap["HeaderNavModules"] = navModules
	common.OptionMapRWMutex.Unlock()
	common.RedisEnabled, common.RDB = redisEnabled, rdb
	moduleCheckedAt.Store(0)
	t.Cleanup(func() {
		common.RedisEnabled, common.RDB = oldRedis, oldRDB
		common.OptionMapRWMutex.Lock()
		if oldMap == nil {
			common.OptionMap = nil
		} else if hadNav {
			common.OptionMap["HeaderNavModules"] = oldNav
		} else {
			delete(common.OptionMap, "HeaderNavModules")
		}
		common.OptionMapRWMutex.Unlock()
		moduleCheckedAt.Store(0)
	})
}

func TestLocalSlotsRecordAndRead(t *testing.T) {
	setModuleState(t, false, nil, "")
	pair := PairKey{Group: "local-g", Model: "local-model"}
	t0 := time.Unix(1_760_000_000, 0) // slot-aligned
	sample := Sample{Model: pair.Model, Group: pair.Group, Success: true, LatencyMs: 250}

	RecordLocal(Sample{Model: pair.Model, Group: pair.Group, LatencyMs: 900}, t0.Add(-70*time.Minute))
	for range 3 {
		RecordLocal(sample, t0)
	}
	RecordLocal(sample, t0.Add(10*time.Second))

	slots, err := ReadSlots(context.Background(), t0.Unix()-60, t0.Unix()+20)
	require.NoError(t, err)
	got := slots[pair]
	require.Len(t, got, 2)
	assert.Equal(t, int64(3), got[t0.Unix()].Req)
	assert.Equal(t, int64(3), got[t0.Unix()].Ok)
	assert.Equal(t, int64(3), got[t0.Unix()].LatHist[histIndex(250)])
	assert.Equal(t, int64(1), got[t0.Unix()+10].Req)

	old, err := ReadSlots(context.Background(), t0.Unix()-70*60, t0.Unix()-70*60+10)
	require.NoError(t, err)
	assert.NotContains(t, old, pair)

	disabled := PairKey{Group: "local-g", Model: "disabled-model"}
	setModuleState(t, false, nil, `{"status":false}`)
	RecordLocal(Sample{Model: disabled.Model, Group: disabled.Group, Success: true}, t0)
	slots, err = ReadSlots(context.Background(), t0.Unix(), t0.Unix()+10)
	require.NoError(t, err)
	assert.NotContains(t, slots, disabled)
}

func TestRedisSlotsRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	setModuleState(t, true, rdb, "")

	ctx := context.Background()
	now := time.Unix(1_760_000_005, 0)
	slotTs := now.Unix() - now.Unix()%SlotSeconds
	pipe := rdb.TxPipeline()
	AppendRedis(ctx, pipe, Sample{Model: "a:b", Group: "g:1", Success: true, LatencyMs: 250, HasTtft: true, TtftMs: 80, OutputTokens: 100, GenerationMs: 2000}, now)
	AppendRedis(ctx, pipe, Sample{Model: "a:b", Group: "g:1", LatencyMs: 250, Failure: Failure429}, now)
	_, err := pipe.Exec(ctx)
	require.NoError(t, err)

	// Local memory is bypassed when Redis is enabled.
	RecordLocal(Sample{Model: "a:b", Group: "g:1", Success: true}, now)

	slots, err := ReadSlots(ctx, slotTs-60, slotTs+SlotSeconds)
	require.NoError(t, err)
	require.Len(t, slots, 1)
	got := slots[PairKey{Group: "g:1", Model: "a:b"}]
	require.Len(t, got, 1)
	want := Counters{Req: 2, Ok: 1, LatSum: 500, TtftSum: 80, TtftN: 1, Out: 100, GenMs: 2000}
	want.LatHist[histIndex(250)] = 2
	want.TtftHist[histIndex(80)] = 1
	want.Fail[Failure429-1] = 1
	assert.Equal(t, want, got[slotTs])

	assert.Greater(t, mr.TTL("model_status:10s:1760000000"), time.Duration(0))

	setModuleState(t, true, rdb, `{"status":{"enabled":false}}`)
	pipe = rdb.TxPipeline()
	AppendRedis(ctx, pipe, Sample{Model: "a:b", Group: "g:1", Success: true}, now)
	assert.Zero(t, pipe.Len())
}

func TestModuleEnabledParsing(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"", true},
		{`{"status":false}`, false},
		{`{"status":{"enabled":false}}`, false},
		{`{"status":{"enabled":true,"requireAuth":true}}`, true},
		{`{"status":"false"}`, false},
		{`{"status":0}`, false},
		{`{"rankings":false}`, true},
		{`not json`, true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, parseStatusModuleEnabled(tt.raw), tt.raw)
	}

	setModuleState(t, false, nil, `{"status":false}`)
	assert.False(t, ModuleEnabled())
}
