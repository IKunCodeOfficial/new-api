package controller

// Fork-only file (ikun): the /api/rebate_info fail-closed contract — the
// endpoint must never advertise a rebate the external settlement job
// (ikun_rebate parse_business_config) would refuse to settle.

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rebateConfigJSON(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	cfg := map[string]any{
		"enabled":                          true,
		"rate":                             0.05,
		"activity_start":                   "2026-08-30T00:00:00+08:00",
		"timezone":                         "Asia/Shanghai",
		"excluded_inviter_ids":             []any{1247},
		"excluded_invitee_ids":             []any{},
		"include_redemption":               true,
		"redemption_exclude_name_patterns": []any{},
		"provider_quota_rules": map[string]any{
			"epay": "amount", "waffo": "amount", "waffo_pancake": "amount",
			"stripe": "money", "creem": "amount_raw",
		},
		"default_language":    "zh",
		"display_in_currency": true,
	}
	if mutate != nil {
		mutate(cfg)
	}
	data, err := common.Marshal(cfg)
	require.NoError(t, err)
	return string(data)
}

func TestRebateRateIfUsable(t *testing.T) {
	rate, ok := rebateRateIfUsable(rebateConfigJSON(t, nil))
	require.True(t, ok)
	assert.Equal(t, 0.05, rate)

	// Minimal config without the optional fields is also valid for the settler.
	rate, ok = rebateRateIfUsable(rebateConfigJSON(t, func(cfg map[string]any) {
		delete(cfg, "excluded_inviter_ids")
		delete(cfg, "excluded_invitee_ids")
		delete(cfg, "include_redemption")
		delete(cfg, "redemption_exclude_name_patterns")
		delete(cfg, "default_language")
		delete(cfg, "display_in_currency")
	}))
	require.True(t, ok)
	assert.Equal(t, 0.05, rate)

	refused := map[string]func(map[string]any){
		"enabled false":            func(cfg map[string]any) { cfg["enabled"] = false },
		"enabled wrong type":       func(cfg map[string]any) { cfg["enabled"] = 1 },
		"rate missing":             func(cfg map[string]any) { delete(cfg, "rate") },
		"rate zero":                func(cfg map[string]any) { cfg["rate"] = 0 },
		"rate one":                 func(cfg map[string]any) { cfg["rate"] = 1 },
		"rate wrong type":          func(cfg map[string]any) { cfg["rate"] = true },
		"timezone missing":         func(cfg map[string]any) { delete(cfg, "timezone") },
		"timezone invalid":         func(cfg map[string]any) { cfg["timezone"] = "Mars/Base" },
		"timezone Local":           func(cfg map[string]any) { cfg["timezone"] = "Local" },
		"activity_start missing":   func(cfg map[string]any) { delete(cfg, "activity_start") },
		"activity_start invalid":   func(cfg map[string]any) { cfg["activity_start"] = "bad" },
		"activity_start no offset": func(cfg map[string]any) { cfg["activity_start"] = "2026-08-30T00:00:00" },
		"rules missing":            func(cfg map[string]any) { delete(cfg, "provider_quota_rules") },
		"rules empty":              func(cfg map[string]any) { cfg["provider_quota_rules"] = map[string]any{} },
		"rule value invalid": func(cfg map[string]any) {
			cfg["provider_quota_rules"] = map[string]any{"epay": "bad"}
		},
		"language unsupported":     func(cfg map[string]any) { cfg["default_language"] = "xx" },
		"inviter ids wrong type":   func(cfg map[string]any) { cfg["excluded_inviter_ids"] = []any{"x"} },
		"redemption flag not bool": func(cfg map[string]any) { cfg["include_redemption"] = "yes" },
	}
	for name, mutate := range refused {
		_, ok := rebateRateIfUsable(rebateConfigJSON(t, mutate))
		assert.False(t, ok, name)
	}

	// Explicit null is a settler-side ConfigError on every field and must not
	// be treated as "field absent" (json decodes both to nil/zero values).
	for _, key := range ikunRebateConfigKeys {
		key := key
		_, ok := rebateRateIfUsable(rebateConfigJSON(t, func(cfg map[string]any) {
			cfg[key] = nil
		}))
		assert.False(t, ok, "explicit null "+key)
	}

	_, ok = rebateRateIfUsable("")
	assert.False(t, ok, "missing option row")
	_, ok = rebateRateIfUsable("{not json")
	assert.False(t, ok, "invalid JSON")
}
