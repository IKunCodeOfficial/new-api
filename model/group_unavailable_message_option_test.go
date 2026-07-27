package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// UpdateOption writes to the option table before updateOptionMap parses the value,
// so a payload that only fails at parse time would still be persisted and reloaded
// on every restart. GroupUnavailableMessage must therefore be rejected up front.
func TestUpdateOptionRejectsInvalidGroupUnavailableMessage(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)
	previousOptionMap := common.OptionMap
	common.OptionMap = make(map[string]string)
	require.NoError(t, setting.UpdateGroupUnavailableMessageByJSONString(`{"cc":"maintenance"}`))
	t.Cleanup(func() {
		common.OptionMap = previousOptionMap
		require.NoError(t, setting.UpdateGroupUnavailableMessageByJSONString("{}"))
	})

	for _, value := range []string{"null", "[]", `{"cc":1}`, `{"cc":`} {
		assert.Error(t, UpdateOption("GroupUnavailableMessage", value), "value %q must be rejected", value)

		var option Option
		assert.ErrorIs(t, db.Where(&Option{Key: "GroupUnavailableMessage"}).First(&option).Error, gorm.ErrRecordNotFound)
		assert.Equal(t, "maintenance", setting.GetGroupUnavailableMessage("cc"))
	}

	require.NoError(t, UpdateOption("GroupUnavailableMessage", `{"cc":"back soon"}`))
	assert.Equal(t, `{"cc":"back soon"}`, requireOptionValue(t, db, "GroupUnavailableMessage"))
	assert.Equal(t, "back soon", setting.GetGroupUnavailableMessage("cc"))
}
