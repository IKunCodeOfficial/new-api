package operation_setting

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

var automaticDisableConsecutiveThreshold atomic.Int32

func init() {
	automaticDisableConsecutiveThreshold.Store(1)
}

func GetAutomaticDisableConsecutiveThreshold() int {
	threshold := automaticDisableConsecutiveThreshold.Load()
	if threshold < 1 {
		return 1
	}
	return int(threshold)
}

func AutomaticDisableConsecutiveThresholdToString() string {
	return strconv.Itoa(GetAutomaticDisableConsecutiveThreshold())
}

func AutomaticDisableConsecutiveThresholdFromString(s string) {
	threshold, err := strconv.ParseInt(strings.TrimSpace(s), 10, 32)
	if err != nil || threshold < 1 {
		common.SysError(fmt.Sprintf("invalid AutomaticDisableConsecutiveThreshold %q, falling back to 1", s))
		automaticDisableConsecutiveThreshold.Store(1)
		return
	}
	automaticDisableConsecutiveThreshold.Store(int32(threshold))
}
