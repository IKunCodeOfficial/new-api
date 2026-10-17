package modelstatus

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/go-redis/redis/v8"
)

const (
	SlotSeconds int64 = 10
	slotCount         = 396 // 66 minutes of 10s slots

	slotWindowSeconds = slotCount * SlotSeconds
	redisSlotTTL      = 70 * time.Minute
	ringIdleTimeout   = 70 * time.Minute
	janitorInterval   = 10 * time.Minute
	redisSlotPrefix   = "model_status:10s:"
)

type PairKey struct{ Group, Model string }

type ringSlot struct {
	ts int64
	// c is allocated on first use of the slot index and reused afterwards, so
	// sparse pairs do not pay for 396 full histogram sets.
	c *Counters
}

type pairRing struct {
	mu        sync.Mutex
	lastWrite atomic.Int64 // wall-clock unix seconds, read by the janitor
	headTs    int64        // newest slot start written; older slots expire against it
	dead      bool         // set by the janitor when it unlinks the ring
	slots     [slotCount]ringSlot
}

var (
	rings       sync.Map // PairKey -> *pairRing
	janitorOnce sync.Once
)

func slotStart(ts int64) int64 {
	return ts - ts%SlotSeconds
}

func slotIndex(slotTs int64) int {
	return int((slotTs / SlotSeconds) % slotCount)
}

// RecordLocal adds a sample to the in-process ring of its pair. It is a no-op
// when Redis is enabled (Redis is authoritative then) or the module is off.
func RecordLocal(s Sample, now time.Time) {
	if common.RedisEnabled || !ModuleEnabled() {
		return
	}
	janitorOnce.Do(func() { go runJanitor() })

	key := PairKey{Group: s.Group, Model: s.Model}
	slotTs := slotStart(now.Unix())
	for {
		value, ok := rings.Load(key)
		if !ok {
			value, _ = rings.LoadOrStore(key, &pairRing{})
		}
		ring := value.(*pairRing)
		ring.mu.Lock()
		if ring.dead {
			// The janitor unlinked this ring between Load and Lock; retry
			// so the write lands in the ring that is now in the map.
			ring.mu.Unlock()
			continue
		}
		ring.add(s, slotTs, now.Unix())
		ring.mu.Unlock()
		return
	}
}

// add must be called with r.mu held.
func (r *pairRing) add(s Sample, slotTs, nowUnix int64) {
	r.lastWrite.Store(nowUnix)
	if slotTs <= r.headTs-slotWindowSeconds {
		return // older than the ring covers; writing would clobber a live slot
	}
	r.headTs = max(r.headTs, slotTs)
	slot := &r.slots[slotIndex(slotTs)]
	if slot.c == nil {
		slot.c = &Counters{}
	} else if slot.ts != slotTs {
		*slot.c = Counters{}
	}
	slot.ts = slotTs
	slot.c.AddSample(s)
}

func runJanitor() {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-ringIdleTimeout).Unix()
		rings.Range(func(key, value any) bool {
			ring := value.(*pairRing)
			if ring.lastWrite.Load() >= cutoff {
				return true
			}
			ring.mu.Lock()
			if ring.lastWrite.Load() < cutoff {
				ring.dead = true
				rings.CompareAndDelete(key, ring)
			}
			ring.mu.Unlock()
			return true
		})
	}
}

// AppendRedis queues the sample's increments on pipe; the caller executes it.
func AppendRedis(ctx context.Context, pipe redis.Pipeliner, s Sample, now time.Time) {
	if !ModuleEnabled() {
		return
	}
	var c Counters
	c.AddSample(s)

	key := redisSlotPrefix + strconv.FormatInt(slotStart(now.Unix()), 10)
	prefix := s.Group + "\x00" + s.Model + "\x00"
	incr := func(name string, value int64) {
		if value != 0 {
			pipe.HIncrBy(ctx, key, prefix+name, value)
		}
	}
	incr("req", c.Req)
	incr("ok", c.Ok)
	incr("lat", c.LatSum)
	incr("ttft", c.TtftSum)
	incr("ttft_n", c.TtftN)
	incr("out", c.Out)
	incr("gen", c.GenMs)
	// Each sample hits at most one bin per histogram and one failure kind.
	for i := range HistBins {
		if c.LatHist[i] != 0 {
			incr("lh"+strconv.Itoa(i), c.LatHist[i])
		}
		if c.TtftHist[i] != 0 {
			incr("th"+strconv.Itoa(i), c.TtftHist[i])
		}
	}
	for i, n := range c.Fail {
		incr("f"+strconv.Itoa(i+1), n)
	}
	pipe.Expire(ctx, key, redisSlotTTL)
}

// ReadSlots returns non-empty counters keyed by pair and slot start for slots
// starting in [fromTs, toTs). The range is clamped to the 66 minutes the
// collector retains. Redis is read when enabled, else process memory.
func ReadSlots(ctx context.Context, fromTs, toTs int64) (map[PairKey]map[int64]Counters, error) {
	first := slotStart(fromTs + SlotSeconds - 1)
	first = max(first, slotStart(toTs-1)-slotWindowSeconds+SlotSeconds)
	result := map[PairKey]map[int64]Counters{}
	if first >= toTs {
		return result, nil
	}
	if common.RedisEnabled {
		return readRedisSlots(ctx, first, toTs, result)
	}

	rings.Range(func(key, value any) bool {
		ring := value.(*pairRing)
		ring.mu.Lock()
		defer ring.mu.Unlock()
		for ts := first; ts < toTs; ts += SlotSeconds {
			if ts <= ring.headTs-slotWindowSeconds {
				continue
			}
			slot := ring.slots[slotIndex(ts)]
			if slot.c == nil || slot.ts != ts || slot.c.Req == 0 {
				continue
			}
			pair := key.(PairKey)
			if result[pair] == nil {
				result[pair] = map[int64]Counters{}
			}
			result[pair][ts] = *slot.c
		}
		return true
	})
	return result, nil
}

func readRedisSlots(ctx context.Context, first, toTs int64, result map[PairKey]map[int64]Counters) (map[PairKey]map[int64]Counters, error) {
	if common.RDB == nil {
		return result, nil
	}
	pipe := common.RDB.Pipeline()
	type slotRead struct {
		ts  int64
		cmd *redis.StringStringMapCmd
	}
	reads := make([]slotRead, 0, (toTs-first)/SlotSeconds+1)
	for ts := first; ts < toTs; ts += SlotSeconds {
		reads = append(reads, slotRead{ts: ts, cmd: pipe.HGetAll(ctx, redisSlotPrefix+strconv.FormatInt(ts, 10))})
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}

	for _, read := range reads {
		fields, err := read.cmd.Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			return nil, err
		}
		for field, raw := range fields {
			group, rest, ok := strings.Cut(field, "\x00")
			if !ok {
				continue
			}
			sep := strings.LastIndexByte(rest, 0)
			if sep < 0 {
				continue
			}
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				continue
			}
			pair := PairKey{Group: group, Model: rest[:sep]}
			if result[pair] == nil {
				result[pair] = map[int64]Counters{}
			}
			c := result[pair][read.ts]
			c.setField(rest[sep+1:], value)
			result[pair][read.ts] = c
		}
	}
	for pair, slots := range result {
		for ts, c := range slots {
			if c.Req == 0 {
				delete(slots, ts)
			}
		}
		if len(slots) == 0 {
			delete(result, pair)
		}
	}
	return result, nil
}

// setField applies one Redis hash field written by AppendRedis.
func (c *Counters) setField(name string, value int64) {
	switch name {
	case "req":
		c.Req = value
	case "ok":
		c.Ok = value
	case "lat":
		c.LatSum = value
	case "ttft":
		c.TtftSum = value
	case "ttft_n":
		c.TtftN = value
	case "out":
		c.Out = value
	case "gen":
		c.GenMs = value
	default:
		var bins *[HistBins]int64
		var idx string
		if rest, ok := strings.CutPrefix(name, "lh"); ok {
			bins, idx = &c.LatHist, rest
		} else if rest, ok := strings.CutPrefix(name, "th"); ok {
			bins, idx = &c.TtftHist, rest
		} else if rest, ok := strings.CutPrefix(name, "f"); ok {
			kind, err := strconv.Atoi(rest)
			if err == nil && kind >= int(Failure5xx) && kind <= int(FailureOther) {
				c.Fail[kind-1] = value
			}
			return
		} else {
			return
		}
		i, err := strconv.Atoi(idx)
		if err == nil && i >= 0 && i < HistBins {
			bins[i] = value
		}
	}
}
