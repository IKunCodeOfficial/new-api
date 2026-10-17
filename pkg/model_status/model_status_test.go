package modelstatus

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
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

// setupStatusDB points model.DB at a fresh SQLite perf_metrics table.
func setupStatusDB(t *testing.T) {
	t.Helper()
	t.Setenv("SQL_DSN", "")
	oldDB, oldPath, oldMaster, oldRedis := model.DB, common.SQLitePath, common.IsMasterNode, common.RedisEnabled
	oldType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SQLitePath, common.IsMasterNode, common.RedisEnabled = filepath.Join(t.TempDir(), "status.db"), false, false
	t.Cleanup(func() {
		model.DB, common.SQLitePath, common.IsMasterNode, common.RedisEnabled = oldDB, oldPath, oldMaster, oldRedis
		common.SetDatabaseTypes(oldType, oldLogType)
	})
	require.NoError(t, model.InitDB())
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, model.DB.AutoMigrate(&model.PerfMetric{}))
}

func TestBuildSnapshotWindowShape(t *testing.T) {
	setupStatusDB(t)
	setModuleState(t, false, nil, "")
	tests := []struct {
		key           string
		buckets       int
		bucketSeconds int64
		ttl           time.Duration
		detail        bool
	}{
		{"5m", 30, 10, 10 * time.Second, true},
		{"1h", 60, 60, 30 * time.Second, true},
		{"24h", 24, 3600, 300 * time.Second, false},
		{"7d", 42, 14400, 1800 * time.Second, false},
		{"30d", 60, 43200, 3600 * time.Second, false},
	}
	now := time.Unix(1_760_000_005, 0)
	for _, tt := range tests {
		spec, ok := ParseRange(tt.key)
		require.True(t, ok, tt.key)
		assert.Equal(t, RangeSpec{Key: tt.key, Buckets: tt.buckets, BucketSeconds: tt.bucketSeconds, TTL: tt.ttl, Detail: tt.detail}, spec)

		snap, err := BuildSnapshot(context.Background(), spec, now)
		require.NoError(t, err, tt.key)
		assert.Equal(t, spec, snap.Range)
		assert.Equal(t, now.Unix(), snap.UpdatedAt)
		assert.Zero(t, snap.WindowStart%spec.BucketSeconds, tt.key)
		end := snap.WindowStart + int64(spec.Buckets)*spec.BucketSeconds
		assert.Greater(t, end, now.Unix(), tt.key)
		assert.LessOrEqual(t, end, now.Unix()+spec.BucketSeconds, tt.key)
		for pair, series := range snap.Pairs {
			assert.Len(t, series.Buckets, spec.Buckets, "%s %+v", tt.key, pair)
		}
	}
	_, ok := ParseRange("2h")
	assert.False(t, ok)
}

func TestBuildSnapshotShortRange(t *testing.T) {
	setModuleState(t, false, nil, "")
	pair := PairKey{Group: "short-g", Model: "short-model"}
	now := time.Unix(1_760_000_005, 0)
	RecordLocal(Sample{Model: pair.Model, Group: pair.Group, Success: true, LatencyMs: 250}, now.Add(-25*time.Second))
	RecordLocal(Sample{Model: pair.Model, Group: pair.Group, LatencyMs: 900, Failure: Failure429}, now.Add(-5*time.Second))
	RecordLocal(Sample{Model: pair.Model, Group: pair.Group, Success: true, LatencyMs: 3000}, now.Add(-5*time.Second))
	RecordLocal(Sample{Model: pair.Model, Group: "auto", Success: true}, now)
	RecordLocal(Sample{Model: pair.Model, Group: "", Success: true}, now)

	spec, _ := ParseRange("5m")
	snap, err := BuildSnapshot(context.Background(), spec, now)
	require.NoError(t, err)
	assert.NotContains(t, snap.Pairs, PairKey{Group: "auto", Model: pair.Model})
	assert.NotContains(t, snap.Pairs, PairKey{Group: "", Model: pair.Model})
	series := snap.Pairs[pair]
	require.NotNil(t, series)
	require.Len(t, series.Buckets, 30)
	assert.Equal(t, int64(1), series.Buckets[27].Req)
	assert.Equal(t, int64(2), series.Buckets[29].Req)
	assert.Equal(t, int64(1), series.Buckets[29].Fail[Failure429-1])
	assert.Equal(t, int64(3), series.Total.Req)
	assert.Equal(t, int64(2), series.Total.Ok)
	assert.Equal(t, int64(1), series.Total.LatHist[histIndex(250)])
	assert.Equal(t, int64(1), series.Total.LatHist[histIndex(900)])
	assert.Equal(t, int64(1), series.Total.Fail[Failure429-1])
}

func TestBuildSnapshotLongRangeUsesDBForCompletedHours(t *testing.T) {
	setupStatusDB(t)
	setModuleState(t, false, nil, "")
	pair := PairKey{Group: "long-g", Model: "long-model"}
	now := time.Unix(1_760_000_005, 0)
	curHour := now.Unix() - now.Unix()%3600
	for _, row := range []model.PerfMetric{
		// Minute-level rows (sub-hour bucket_time) of one completed hour sum into one row.
		{ModelName: pair.Model, Group: pair.Group, BucketTs: curHour - 3600, RequestCount: 7, SuccessCount: 6, TotalLatencyMs: 3000, TtftSumMs: 500, TtftCount: 5, OutputTokens: 300, GenerationMs: 3000},
		{ModelName: pair.Model, Group: pair.Group, BucketTs: curHour - 3540, RequestCount: 3, SuccessCount: 3, TotalLatencyMs: 2000, TtftSumMs: 300, TtftCount: 3, OutputTokens: 100, GenerationMs: 1000},
		{ModelName: pair.Model, Group: pair.Group, BucketTs: curHour, RequestCount: 99, SuccessCount: 99},
		{ModelName: pair.Model, Group: "auto", BucketTs: curHour - 3600, RequestCount: 5, SuccessCount: 5},
	} {
		require.NoError(t, model.UpsertPerfMetric(&row))
	}
	RecordLocal(Sample{Model: pair.Model, Group: pair.Group, Success: true, LatencyMs: 250}, now.Add(-30*time.Second))
	RecordLocal(Sample{Model: pair.Model, Group: pair.Group, Success: true, LatencyMs: 250}, now.Add(-30*time.Second))

	spec, _ := ParseRange("24h")
	snap, err := BuildSnapshot(context.Background(), spec, now)
	require.NoError(t, err)
	assert.NotContains(t, snap.Pairs, PairKey{Group: "auto", Model: pair.Model})
	series := snap.Pairs[pair]
	require.NotNil(t, series)
	require.Len(t, series.Buckets, 24)
	last := series.Buckets[23]
	assert.Equal(t, int64(2), last.Req)
	assert.Equal(t, int64(2), last.Ok)
	assert.Equal(t, int64(500), last.LatSum)
	assert.Equal(t, Counters{Req: 10, Ok: 9, LatSum: 5000, TtftSum: 800, TtftN: 8, Out: 400, GenMs: 4000}, series.Buckets[22])
	assert.Equal(t, int64(12), series.Total.Req)
	assert.Equal(t, int64(11), series.Total.Ok)
	assert.Equal(t, [HistBins]int64{}, series.Total.LatHist)
	assert.Equal(t, [4]int64{}, series.Total.Fail)

	rows, err := model.GetPerfMetricRows(curHour-3600, curHour)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		if row.Group == pair.Group {
			assert.Equal(t, model.PerfMetric{ModelName: pair.Model, Group: pair.Group, BucketTs: curHour - 3600, RequestCount: 10, SuccessCount: 9, TotalLatencyMs: 5000, TtftSumMs: 800, TtftCount: 8, OutputTokens: 400, GenerationMs: 4000}, row)
		}
	}
}

func TestBuildSnapshotRedisErrorDegrades(t *testing.T) {
	setupStatusDB(t)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	mr.Close()
	setModuleState(t, true, rdb, "")

	pair := PairKey{Group: "redis-down-g", Model: "redis-down-model"}
	now := time.Unix(1_760_000_005, 0)
	curHour := now.Unix() - now.Unix()%3600
	require.NoError(t, model.UpsertPerfMetric(&model.PerfMetric{ModelName: pair.Model, Group: pair.Group, BucketTs: curHour - 7200, RequestCount: 4, SuccessCount: 4}))

	long, _ := ParseRange("24h")
	snap, err := BuildSnapshot(context.Background(), long, now)
	require.NoError(t, err)
	require.NotNil(t, snap.Pairs[pair])
	assert.Equal(t, int64(4), snap.Pairs[pair].Buckets[21].Req)
	assert.Equal(t, int64(4), snap.Pairs[pair].Total.Req)

	short, _ := ParseRange("5m")
	snap, err = BuildSnapshot(context.Background(), short, now)
	require.NoError(t, err)
	assert.Empty(t, snap.Pairs)
}

func TestGetSnapshotCaches(t *testing.T) {
	setModuleState(t, false, nil, "")
	spec, _ := ParseRange("5m")
	snapshotCache.Delete(spec.Key)
	t.Cleanup(func() { snapshotCache.Delete(spec.Key) })

	first, err := GetSnapshot(context.Background(), spec)
	require.NoError(t, err)
	second, err := GetSnapshot(context.Background(), spec)
	require.NoError(t, err)
	assert.Same(t, first, second)

	entry, ok := snapshotCache.Load(spec.Key)
	require.True(t, ok)
	entry.(*cacheEntry).current.Store(nil)
	third, err := GetSnapshot(context.Background(), spec)
	require.NoError(t, err)
	assert.NotSame(t, first, third)
}

func newTestSnapshot(t *testing.T, rangeKey string) *Snapshot {
	t.Helper()
	spec, ok := ParseRange(rangeKey)
	require.True(t, ok)
	return &Snapshot{Range: spec, WindowStart: 1000, UpdatedAt: 2000, Pairs: map[PairKey]*Series{}}
}

func TestBuildResponseVisibilityAndNoCounts(t *testing.T) {
	snap := newTestSnapshot(t, "1h")
	snap.add(PairKey{Group: "vip", Model: "m3"}, 5, Counters{Req: 7, Ok: 7})
	snap.add(PairKey{Group: "default", Model: "m1"}, 5, Counters{Req: 4, Ok: 3, LatSum: 400, Fail: [4]int64{1, 0, 0, 0}})
	snap.add(PairKey{Group: "default", Model: "gone"}, 5, Counters{Req: 9, Ok: 9})
	pricing := []model.Pricing{
		{ModelName: "m1", EnableGroup: []string{"default", "vip"}},
		{ModelName: "m2", EnableGroup: []string{"all"}},
		{ModelName: "m3", EnableGroup: []string{"vip"}},
	}

	resp := BuildResponse(snap, pricing, nil, Viewer{
		Groups: map[string]string{"default": "Public"},
		Ratios: map[string]float64{"default": 0.8},
	})

	require.Len(t, resp.Groups, 1)
	group := resp.Groups[0]
	assert.Equal(t, "default", group.Key)
	assert.Equal(t, "Public", group.Desc)
	assert.Equal(t, 0.8, group.Ratio)
	require.Len(t, group.Models, 2)
	assert.Equal(t, "m1", group.Models[0].Name)
	assert.Equal(t, "m2", group.Models[1].Name)
	assert.Nil(t, group.Models[1].Availability)
	require.Len(t, group.Models[1].Buckets, 60)
	for _, bucket := range group.Models[1].Buckets {
		assert.Nil(t, bucket)
	}

	body, err := common.Marshal(resp)
	require.NoError(t, err)
	for _, key := range []string{`"req"`, `"total"`, `"count"`, `"success"`} {
		assert.NotContains(t, string(body), key+":")
	}
	assert.NotContains(t, string(body), "m3")
	assert.NotContains(t, string(body), "gone")
	assert.NotContains(t, string(body), `"vip"`)
}

func TestBuildResponseRatesAndOrdering(t *testing.T) {
	snap := newTestSnapshot(t, "1h")
	m1, m2 := PairKey{Group: "default", Model: "m1"}, PairKey{Group: "default", Model: "m2"}
	// Bucket 54 lies just outside the last max(3, ceil(60*0.08)) = 5 buckets.
	snap.add(m1, 54, Counters{Req: 50, Ok: 49, Fail: [4]int64{1, 0, 0, 0}})
	snap.add(m1, 55, Counters{Req: 50, Ok: 50})
	var latHist [HistBins]int64
	latHist[histIndex(900)] = 300
	snap.add(m2, 10, Counters{Req: 300, Ok: 300, LatSum: 300000, TtftSum: 60000, TtftN: 300, Out: 6000, GenMs: 100000, LatHist: latHist})
	pricing := []model.Pricing{
		{ModelName: "m1", EnableGroup: []string{"default"}},
		{ModelName: "m2", EnableGroup: []string{"default"}},
	}
	viewer := Viewer{Groups: map[string]string{"default": "Public"}, Ratios: map[string]float64{"default": 1}}

	resp := BuildResponse(snap, pricing, nil, viewer)

	assert.Equal(t, "1h", resp.Range)
	assert.Equal(t, int64(60), resp.BucketSeconds)
	assert.Equal(t, int64(1000), resp.WindowStart)
	assert.Equal(t, int64(2000), resp.UpdatedAt)
	assert.True(t, resp.HasDetail)
	require.Len(t, resp.Groups, 1)
	group := resp.Groups[0]
	require.NotNil(t, group.Availability)
	assert.InDelta(t, 399.0/400.0, *group.Availability, 1e-9)
	require.Len(t, group.Models, 2)

	busy, light := group.Models[0], group.Models[1]
	assert.Equal(t, "m2", busy.Name)
	assert.Equal(t, "m1", light.Name)
	require.NotNil(t, light.Availability)
	assert.InDelta(t, 0.99, *light.Availability, 1e-9)
	require.NotNil(t, light.Current)
	assert.Equal(t, 1.0, *light.Current)
	require.NotNil(t, light.Detail)
	assert.Equal(t, map[string]float64{"5xx": 1, "429": 0, "timeout": 0, "other": 0}, light.Detail.FailShare)

	assert.Nil(t, busy.Current)
	assert.Equal(t, int64(1000), busy.LatencyAvgMs)
	assert.Equal(t, int64(200), busy.TtftAvgMs)
	assert.Equal(t, 60.0, busy.Tps)
	require.NotNil(t, busy.Buckets[10])
	assert.Equal(t, BucketView{Rate: 1, TtftMs: 200}, *busy.Buckets[10])
	require.NotNil(t, busy.Detail)
	assert.Equal(t, int64(1000), busy.Detail.LatencyP50)
	assert.Nil(t, busy.Detail.FailShare)

	long := newTestSnapshot(t, "24h")
	long.add(m1, 3, Counters{Req: 10, Ok: 10})
	resp = BuildResponse(long, pricing, nil, viewer)
	assert.False(t, resp.HasDetail)
	require.Len(t, resp.Groups, 1)
	for _, m := range resp.Groups[0].Models {
		assert.Nil(t, m.Detail)
	}
}

func TestBuildResponseVendorIcon(t *testing.T) {
	snap := newTestSnapshot(t, "5m")
	pricing := []model.Pricing{
		{ModelName: "a", Icon: "Own", VendorID: 1, EnableGroup: []string{"default"}},
		{ModelName: "b", VendorID: 1, EnableGroup: []string{"default"}},
		{ModelName: "c", EnableGroup: []string{"default"}},
	}
	vendors := []model.PricingVendor{{ID: 1, Name: "OpenAI", Icon: "OpenAI"}}

	resp := BuildResponse(snap, pricing, vendors, Viewer{Groups: map[string]string{"default": ""}})

	require.Len(t, resp.Groups, 1)
	icons := map[string]string{}
	for _, m := range resp.Groups[0].Models {
		icons[m.Name] = m.VendorIcon
	}
	assert.Equal(t, map[string]string{"a": "Own", "b": "OpenAI", "c": ""}, icons)
}

func TestBuildResponseGroupOrderSkipsHiddenAndEmptyGroups(t *testing.T) {
	snap := newTestSnapshot(t, "5m")
	pricing := []model.Pricing{{ModelName: "m", EnableGroup: []string{"zeta", "beta", "default", "auto", ""}}}
	viewer := Viewer{Groups: map[string]string{"zeta": "", "beta": "", "default": "", "auto": "", "": "", "empty": ""}}

	resp := BuildResponse(snap, pricing, nil, viewer)

	keys := make([]string, 0, len(resp.Groups))
	for _, g := range resp.Groups {
		keys = append(keys, g.Key)
	}
	assert.Equal(t, []string{"default", "beta", "zeta"}, keys)
}
