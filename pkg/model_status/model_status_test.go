package modelstatus

import (
	"testing"

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
