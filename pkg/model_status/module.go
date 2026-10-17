package modelstatus

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const moduleRefreshInterval = 10 * time.Second

var (
	// moduleDisabled is inverted so the zero value means enabled before the
	// first refresh.
	moduleDisabled  atomic.Bool
	moduleCheckedAt atomic.Int64 // unix nanos of the last refresh; 0 forces one
)

// ModuleEnabled reports whether the "status" header-nav module is enabled. It
// runs on the relay hot path, so the parsed option is cached for 10 seconds.
func ModuleEnabled() bool {
	now := time.Now().UnixNano()
	checked := moduleCheckedAt.Load()
	if checked != 0 && now-checked < int64(moduleRefreshInterval) {
		return !moduleDisabled.Load()
	}
	if !moduleCheckedAt.CompareAndSwap(checked, now) {
		return !moduleDisabled.Load()
	}
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["HeaderNavModules"]
	common.OptionMapRWMutex.RUnlock()
	enabled := parseStatusModuleEnabled(raw)
	moduleDisabled.Store(!enabled)
	return enabled
}

// parseStatusModuleEnabled mirrors middleware.parseHeaderNavAccess for the
// "status" module; middleware cannot be imported here (cycle via perf_metrics).
func parseStatusModuleEnabled(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	var parsed map[string]any
	if err := common.Unmarshal([]byte(raw), &parsed); err != nil {
		return true
	}
	value := parsed["status"]
	if access, ok := value.(map[string]any); ok {
		value = access["enabled"]
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "false", "0":
			return false
		}
	case float64:
		return v != 0
	}
	return true
}
