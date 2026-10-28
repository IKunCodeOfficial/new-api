package controller

// Fork-only file (ikun): /api/rebate_info must never advertise a rebate the
// external settler would refuse, and must survive rebate_meta not existing yet
// (the settler creates it from `init`).

import (
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRebateMetaIfUsable(t *testing.T) {
	start, timezone, ok := rebateMetaIfUsable(map[string]string{"activity_start": "2026-09-01", "timezone": "Asia/Shanghai"})
	require.True(t, ok)
	assert.Equal(t, "2026-09-01", start)
	assert.Equal(t, "Asia/Shanghai", timezone)

	refused := map[string]map[string]string{
		"missing start":    {"timezone": "Asia/Shanghai"},
		"missing timezone": {"activity_start": "2026-09-01"},
		"empty":            {},
		"unpadded start":   {"activity_start": "2026-9-1", "timezone": "UTC"},
		"start is RFC3339": {"activity_start": "2026-09-01T00:00:00+08:00", "timezone": "UTC"},
		"impossible date":  {"activity_start": "2026-02-30", "timezone": "UTC"},
		"timezone Local":   {"activity_start": "2026-09-01", "timezone": "Local"},
		"unknown timezone": {"activity_start": "2026-09-01", "timezone": "Nowhere/Zone"},
	}
	for name, values := range refused {
		t.Run(name, func(t *testing.T) {
			_, _, ok := rebateMetaIfUsable(values)
			assert.False(t, ok)
		})
	}
}

func seedRebateOptions(t *testing.T, values map[string]string) {
	t.Helper()
	for key, value := range values {
		require.NoError(t, model.DB.Create(&model.Option{Key: key, Value: value}).Error)
	}
}

// createRebateMetaTable mimics what the settler's `init` creates: rebate_meta is
// Python-owned, so the Go side never migrates it and the tests must not either.
func createRebateMetaTable(t *testing.T, rows ...string) {
	t.Helper()
	require.NoError(t, model.DB.Exec("CREATE TABLE rebate_meta (meta_key VARCHAR(64) PRIMARY KEY, meta_value VARCHAR(255) NOT NULL)").Error)
	for _, row := range rows {
		require.NoError(t, model.DB.Exec("INSERT INTO rebate_meta VALUES "+row).Error)
	}
}

func TestGetRebateInfo(t *testing.T) {
	window := []string{"('activity_start','2026-09-01')", "('timezone','Asia/Shanghai')"}

	tests := []struct {
		name    string
		options map[string]string
		meta    []string // nil: the settler has not created rebate_meta yet
		want    map[string]any
	}{
		{
			name:    "enabled",
			options: map[string]string{"IkunRebateEnabled": "true", "IkunRebateRate": "0.05"},
			meta:    window,
			want: map[string]any{
				"enabled": true, "rate": 0.05, "activity_start": "2026-09-01", "timezone": "Asia/Shanghai",
			},
		},
		{
			name:    "rebate_meta not created yet",
			options: map[string]string{"IkunRebateEnabled": "true", "IkunRebateRate": "0.05"},
			want:    map[string]any{"enabled": false},
		},
		{
			name:    "rebate_meta without window rows",
			options: map[string]string{"IkunRebateEnabled": "true", "IkunRebateRate": "0.05"},
			meta:    []string{},
			want:    map[string]any{"enabled": false},
		},
		{
			name:    "window without a timezone",
			options: map[string]string{"IkunRebateEnabled": "true", "IkunRebateRate": "0.05"},
			meta:    []string{"('activity_start','2026-09-01')"},
			want:    map[string]any{"enabled": false},
		},
		{
			name:    "unusable timezone",
			options: map[string]string{"IkunRebateEnabled": "true", "IkunRebateRate": "0.05"},
			meta:    []string{"('activity_start','2026-09-01')", "('timezone','Nowhere/Zone')"},
			want:    map[string]any{"enabled": false},
		},
		{
			name:    "switch off still reports the window",
			options: map[string]string{"IkunRebateEnabled": "false", "IkunRebateRate": "0.05"},
			meta:    window,
			want: map[string]any{
				"enabled": false, "activity_start": "2026-09-01", "timezone": "Asia/Shanghai",
			},
		},
		{
			name:    "missing switch",
			options: map[string]string{"IkunRebateRate": "0.05"},
			meta:    window,
			want: map[string]any{
				"enabled": false, "activity_start": "2026-09-01", "timezone": "Asia/Shanghai",
			},
		},
		{
			name:    "unusable rate still reports the window",
			options: map[string]string{"IkunRebateEnabled": "true", "IkunRebateRate": "0"},
			meta:    window,
			want: map[string]any{
				"enabled": false, "activity_start": "2026-09-01", "timezone": "Asia/Shanghai",
			},
		},
		{
			name:    "consume logging off",
			options: map[string]string{"IkunRebateEnabled": "true", "IkunRebateRate": "0.05", "LogConsumeEnabled": "false"},
			meta:    window,
			want: map[string]any{
				"enabled": false, "activity_start": "2026-09-01", "timezone": "Asia/Shanghai",
			},
		},
		{
			name:    "consume logging explicitly on",
			options: map[string]string{"IkunRebateEnabled": "true", "IkunRebateRate": "0.05", "LogConsumeEnabled": "true"},
			meta:    window,
			want: map[string]any{
				"enabled": true, "rate": 0.05, "activity_start": "2026-09-01", "timezone": "Asia/Shanghai",
			},
		},
		{
			name:    "rate in a form only Go parses",
			options: map[string]string{"IkunRebateEnabled": "true", "IkunRebateRate": "0x1p-4"},
			meta:    window,
			want: map[string]any{
				"enabled": false, "activity_start": "2026-09-01", "timezone": "Asia/Shanghai",
			},
		},
		{
			name:    "rate is not a number",
			options: map[string]string{"IkunRebateEnabled": "true", "IkunRebateRate": "abc"},
			meta:    window,
			want: map[string]any{
				"enabled": false, "activity_start": "2026-09-01", "timezone": "Asia/Shanghai",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setupRebateControllerTest(t)
			resetRebateInfoCache()
			seedRebateOptions(t, tc.options)
			if tc.meta != nil {
				createRebateMetaTable(t, tc.meta...)
			}

			assert.Equal(t, tc.want, postJSON(t, GetRebateInfo, "GET", "/api/rebate_info", "", 0))
		})
	}
}

// The payload is read on every wallet/console page load, so it is cached for a
// minute instead of hitting the database each time.
func TestGetRebateInfoServesCachedPayload(t *testing.T) {
	setupRebateControllerTest(t)
	resetRebateInfoCache()
	seedRebateOptions(t, map[string]string{"IkunRebateEnabled": "true", "IkunRebateRate": "0.05"})
	createRebateMetaTable(t, "('activity_start','2026-09-01')", "('timezone','Asia/Shanghai')")

	first := postJSON(t, GetRebateInfo, "GET", "/api/rebate_info", "", 0)
	require.Equal(t, true, first["enabled"])

	require.NoError(t, model.DB.Model(&model.Option{}).
		Where(map[string]any{"key": "IkunRebateEnabled"}).
		Update("value", "false").Error)
	assert.Equal(t, first, postJSON(t, GetRebateInfo, "GET", "/api/rebate_info", "", 0))

	resetRebateInfoCache()
	after := postJSON(t, GetRebateInfo, "GET", "/api/rebate_info", "", 0)
	assert.Equal(t, false, after["enabled"])
	assert.Equal(t, first["activity_start"], after["activity_start"], "the window survives the switch")
}
