package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The /api/pricing response must not leak group names the caller cannot use:
// models available only through hidden groups are dropped, and enable_groups
// of the remaining models is trimmed to the caller's usable groups.
func TestFilterPricingByUsableGroupsHidesUnusableGroups(t *testing.T) {
	pricing := []model.Pricing{
		{ModelName: "m1", EnableGroup: []string{"default", "internal"}},
		{ModelName: "m2", EnableGroup: []string{"internal"}},
		{ModelName: "m3", EnableGroup: []string{"all"}},
	}
	usable := map[string]string{"default": "默认分组"}

	filtered := filterPricingByUsableGroups(pricing, usable, true)
	require.Len(t, filtered, 2)
	assert.Equal(t, "m1", filtered[0].ModelName)
	assert.Equal(t, []string{"default"}, filtered[0].EnableGroup)
	assert.Equal(t, "m3", filtered[1].ModelName)
	assert.Equal(t, []string{"all"}, filtered[1].EnableGroup)

	// The pricing slice is a shared cache; trimming must not mutate it.
	assert.Equal(t, []string{"default", "internal"}, pricing[0].EnableGroup)
}

func TestFilterPricingByUsableGroupsKeepsGroupsForAdmin(t *testing.T) {
	pricing := []model.Pricing{
		{ModelName: "m1", EnableGroup: []string{"default", "internal"}},
	}
	usable := map[string]string{"default": "", "internal": ""}

	filtered := filterPricingByUsableGroups(pricing, usable, false)
	require.Len(t, filtered, 1)
	assert.Equal(t, []string{"default", "internal"}, filtered[0].EnableGroup)
}

func TestFilterPricingByUsableGroupsDeniesAllWhenNoUsableGroup(t *testing.T) {
	pricing := []model.Pricing{
		{ModelName: "m1", EnableGroup: []string{"all"}},
	}

	assert.Empty(t, filterPricingByUsableGroups(pricing, map[string]string{}, true))
}
