package controller

import (
	"testing"

	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/stretchr/testify/assert"
)

func TestFilterPerfMetricGroupsByUsableGroupsHidesUnavailableGroups(t *testing.T) {
	groups := []perfmetrics.GroupResult{
		{Group: "default"},
		{Group: "internal"},
		{Group: "auto"},
	}
	usableGroups := map[string]struct{}{
		"default": {},
		"auto":    {},
	}

	filtered := filterPerfMetricGroupsByUsableGroups(groups, usableGroups)

	assert.Equal(t, []perfmetrics.GroupResult{
		{Group: "default"},
		{Group: "auto"},
	}, filtered)
}
