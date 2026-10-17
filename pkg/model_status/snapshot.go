package modelstatus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// RangeSpec describes one selectable time range of the status page.
type RangeSpec struct {
	Key           string
	Buckets       int
	BucketSeconds int64
	TTL           time.Duration
	// Detail ranges keep histograms and failure kinds; long ranges do not.
	Detail bool
}

var ranges = map[string]RangeSpec{
	"5m":  {Key: "5m", Buckets: 30, BucketSeconds: 10, TTL: 10 * time.Second, Detail: true},
	"1h":  {Key: "1h", Buckets: 60, BucketSeconds: 60, TTL: 30 * time.Second, Detail: true},
	"24h": {Key: "24h", Buckets: 24, BucketSeconds: 3600, TTL: 300 * time.Second},
	"7d":  {Key: "7d", Buckets: 42, BucketSeconds: 14400, TTL: 1800 * time.Second},
	"30d": {Key: "30d", Buckets: 60, BucketSeconds: 43200, TTL: 3600 * time.Second},
}

func ParseRange(key string) (RangeSpec, bool) {
	spec, ok := ranges[key]
	return spec, ok
}

// Series holds one pair's counters per bucket plus their sum.
type Series struct {
	Buckets []Counters
	Total   Counters
}

// Snapshot is the unfiltered aggregate of one range. It is shared between
// requests through the cache and must not be modified after BuildSnapshot.
type Snapshot struct {
	Range       RangeSpec
	WindowStart int64
	UpdatedAt   int64
	Pairs       map[PairKey]*Series
	// partial is set when a slot read failed and the data is incomplete.
	partial bool
}

// add merges c into the bucket of pair, skipping groups that are never shown.
func (s *Snapshot) add(pair PairKey, bucket int, c Counters) {
	if pair.Group == "" || pair.Group == "auto" || bucket < 0 || bucket >= s.Range.Buckets {
		return
	}
	series := s.Pairs[pair]
	if series == nil {
		series = &Series{Buckets: make([]Counters, s.Range.Buckets)}
		s.Pairs[pair] = series
	}
	series.Buckets[bucket].Merge(c)
	series.Total.Merge(c)
}

// BuildSnapshot aggregates the range whose last bucket contains now. Short
// ranges read only the slots of their bucket size. Long ranges read completed
// hours from perf_metrics and the current hour from the 1m slots, which are
// shared across instances when Redis is enabled. Slot read failures are logged
// and the snapshot is built from the remaining data.
func BuildSnapshot(ctx context.Context, spec RangeSpec, now time.Time) (*Snapshot, error) {
	nowTs := now.Unix()
	end := nowTs - nowTs%spec.BucketSeconds + spec.BucketSeconds
	snap := &Snapshot{
		Range:       spec,
		WindowStart: end - int64(spec.Buckets)*spec.BucketSeconds,
		UpdatedAt:   nowTs,
		Pairs:       map[PairKey]*Series{},
	}

	if spec.Detail {
		slots, err := ReadSlots(ctx, snap.WindowStart, end, spec.BucketSeconds)
		if err != nil {
			snap.partial = true
			common.SysError(fmt.Sprintf("model status: read %s slots: %v", spec.Key, err))
		}
		for pair, bySlot := range slots {
			for slotTs, c := range bySlot {
				snap.add(pair, int((slotTs-snap.WindowStart)/spec.BucketSeconds), c)
			}
		}
		return snap, nil
	}

	curHour := nowTs - nowTs%3600
	rows, err := model.GetPerfMetricRows(ctx, snap.WindowStart, curHour)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		snap.add(PairKey{Group: row.Group, Model: row.ModelName}, int((row.BucketTs-snap.WindowStart)/spec.BucketSeconds), Counters{
			Req:     row.RequestCount,
			Ok:      row.SuccessCount,
			LatSum:  row.TotalLatencyMs,
			TtftSum: row.TtftSumMs,
			TtftN:   row.TtftCount,
			Out:     row.OutputTokens,
			GenMs:   row.GenerationMs,
		})
	}

	// The current hour is not flushed yet; it always lies in the last bucket
	// because bucket sizes are whole multiples of an hour.
	slots, err := ReadSlots(ctx, curHour, nowTs+1, MinuteSeconds)
	if err != nil {
		snap.partial = true
		common.SysError(fmt.Sprintf("model status: read %s current-hour slots: %v", spec.Key, err))
	}
	last := spec.Buckets - 1
	for pair, bySlot := range slots {
		for _, c := range bySlot {
			c.LatHist, c.TtftHist, c.Fail = [HistBins]int64{}, [HistBins]int64{}, [4]int64{}
			snap.add(pair, last, c)
		}
	}
	return snap, nil
}

type cachedSnapshot struct {
	snap      *Snapshot
	expiresAt time.Time
}

// cacheEntry serializes builds of one range; current is read without the lock.
type cacheEntry struct {
	mu      sync.Mutex
	current atomic.Pointer[cachedSnapshot]
}

var snapshotCache sync.Map // range key -> *cacheEntry

// GetSnapshot returns the cached snapshot of spec. Once it expires, at most one
// build per range runs at a time and concurrent callers wait for its result.
// The build is detached from the caller, whose cancellation would otherwise
// leave an empty snapshot in the shared cache.
func GetSnapshot(ctx context.Context, spec RangeSpec) (*Snapshot, error) {
	value, _ := snapshotCache.LoadOrStore(spec.Key, &cacheEntry{})
	entry := value.(*cacheEntry)
	if cached := entry.current.Load(); cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.snap, nil
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if cached := entry.current.Load(); cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.snap, nil
	}
	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	now := time.Now()
	snap, err := BuildSnapshot(buildCtx, spec, now)
	if err != nil {
		return nil, err
	}
	ttl := spec.TTL
	if snap.partial {
		// Incomplete data is cached briefly so a Redis outage heals within seconds.
		ttl = min(spec.TTL, 5*time.Second)
	}
	entry.current.Store(&cachedSnapshot{snap: snap, expiresAt: now.Add(ttl)})
	return snap, nil
}
