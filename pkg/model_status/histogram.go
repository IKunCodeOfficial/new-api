package modelstatus

import "math"

const HistBins = 20

// histBoundsMs holds the inclusive upper bound of every bin but the last,
// which is open-ended.
var histBoundsMs = [HistBins - 1]int64{100, 200, 300, 500, 750, 1000, 1500, 2000, 3000, 5000, 7500, 10000, 15000, 20000, 30000, 45000, 60000, 90000, 120000}

// histIndex returns the first bin whose bound is >= ms, else the last bin.
func histIndex(ms int64) int {
	for i, bound := range histBoundsMs {
		if ms <= bound {
			return i
		}
	}
	return HistBins - 1
}

// Percentile returns the upper bound of the bin holding rank ceil(p*total).
// The open-ended last bin reports the largest bound. Empty input returns 0.
func Percentile(hist [HistBins]int64, p float64) int64 {
	var total int64
	for _, n := range hist {
		total += n
	}
	if total == 0 {
		return 0
	}
	rank := max(int64(math.Ceil(p*float64(total))), 1)
	var seen int64
	for i, n := range hist {
		seen += n
		if seen >= rank {
			if i >= len(histBoundsMs) {
				return histBoundsMs[len(histBoundsMs)-1]
			}
			return histBoundsMs[i]
		}
	}
	return histBoundsMs[len(histBoundsMs)-1]
}
