// Package modelstatus aggregates relay samples into per-slot counters that back
// the model status page. It must not import pkg/perf_metrics.
package modelstatus

// FailureKind is the coarse reason a relay sample failed.
type FailureKind uint8

const (
	FailureNone FailureKind = iota
	Failure5xx
	Failure429
	FailureTimeout
	FailureOther
)

// Key returns the wire key of a failure kind; FailureNone has no key.
func (k FailureKind) Key() string {
	switch k {
	case Failure5xx:
		return "5xx"
	case Failure429:
		return "429"
	case FailureTimeout:
		return "timeout"
	case FailureOther:
		return "other"
	}
	return ""
}

// Sample is one finished relay that counts as a health sample.
type Sample struct {
	Model, Group string
	Success      bool
	LatencyMs    int64
	TtftMs       int64
	HasTtft      bool
	OutputTokens int64
	GenerationMs int64
	Failure      FailureKind
}

// Counters accumulates samples for one slot. Fail is indexed by kind-1.
type Counters struct {
	Req, Ok, LatSum, TtftSum, TtftN, Out, GenMs int64
	LatHist, TtftHist                           [HistBins]int64
	Fail                                        [4]int64
}

func (c *Counters) AddSample(s Sample) {
	c.Req++
	if s.Success {
		c.Ok++
	} else {
		kind := s.Failure
		if kind < Failure5xx || kind > FailureOther {
			kind = FailureOther
		}
		c.Fail[kind-1]++
	}
	if s.LatencyMs > 0 {
		c.LatSum += s.LatencyMs
	}
	c.LatHist[histIndex(s.LatencyMs)]++
	if s.HasTtft && s.TtftMs >= 0 {
		c.TtftSum += s.TtftMs
		c.TtftN++
		c.TtftHist[histIndex(s.TtftMs)]++
	}
	if s.OutputTokens > 0 && s.GenerationMs > 0 {
		c.Out += s.OutputTokens
		c.GenMs += s.GenerationMs
	}
}

func (c *Counters) Merge(o Counters) {
	c.Req += o.Req
	c.Ok += o.Ok
	c.LatSum += o.LatSum
	c.TtftSum += o.TtftSum
	c.TtftN += o.TtftN
	c.Out += o.Out
	c.GenMs += o.GenMs
	for i := range HistBins {
		c.LatHist[i] += o.LatHist[i]
		c.TtftHist[i] += o.TtftHist[i]
	}
	for i := range c.Fail {
		c.Fail[i] += o.Fail[i]
	}
}
