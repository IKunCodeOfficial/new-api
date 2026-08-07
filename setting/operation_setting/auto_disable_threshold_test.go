package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAutomaticDisableConsecutiveThresholdParsing(t *testing.T) {
	original := AutomaticDisableConsecutiveThresholdToString()
	t.Cleanup(func() {
		AutomaticDisableConsecutiveThresholdFromString(original)
	})

	AutomaticDisableConsecutiveThresholdFromString(" 5 ")
	assert.Equal(t, 5, GetAutomaticDisableConsecutiveThreshold())
	assert.Equal(t, "5", AutomaticDisableConsecutiveThresholdToString())

	for _, invalid := range []string{"", "0", "-1", "2147483648", "not-a-number"} {
		AutomaticDisableConsecutiveThresholdFromString(invalid)
		assert.Equal(t, 1, GetAutomaticDisableConsecutiveThreshold(), invalid)
	}
}
