package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStreamDrainSettingDefaults(t *testing.T) {
	s := GetStreamDrainSetting()
	assert.True(t, s.DrainOnClientDisconnect, "drain must default to enabled")
	assert.Equal(t, defaultDrainMaxWaitSeconds, s.GetDrainMaxWaitSeconds())
}

func TestStreamDrainMaxWaitSecondsBounds(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero falls back to default", 0, defaultDrainMaxWaitSeconds},
		{"negative falls back to default", -5, defaultDrainMaxWaitSeconds},
		{"in-range value kept", 30, 30},
		{"oversized value clamped to one hour", 86400, maxDrainMaxWaitSeconds},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &StreamDrainSetting{DrainOnClientDisconnect: true, DrainMaxWaitSeconds: tt.in}
			assert.Equal(t, tt.want, s.GetDrainMaxWaitSeconds())
		})
	}
}
