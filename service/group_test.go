package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
)

func TestGetUserUsableGroupsForRoleIncludesAllActiveGroupsForAdmins(t *testing.T) {
	userGroups := GetUserUsableGroupsForRole("", common.RoleCommonUser)
	adminGroups := GetUserUsableGroupsForRole("", common.RoleAdminUser)

	assert.NotContains(t, userGroups, "svip")
	assert.Contains(t, adminGroups, "svip")
}
