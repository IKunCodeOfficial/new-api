package controller

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createExclusionTarget inserts the user an exclusion points at; adding an
// exclusion for an ID that no account holds is rejected.
func createExclusionTarget(t *testing.T, id int) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.User{
		Id: id, Username: "excl" + common.Interface2String(id), Password: "12345678",
		Status: common.UserStatusEnabled, AffCode: "x" + common.Interface2String(id),
	}).Error)
}

func TestRebateExclusionAddListDelete(t *testing.T) {
	setupRebateControllerTest(t)
	createExclusionTarget(t, 1247)

	listed := postJSON(t, ListRebateExclusions, "GET", "/api/rebate/exclusions", "", 1)
	require.Equal(t, true, listed["success"])
	assert.Equal(t, []any{}, listed["data"], "an empty list must be [] rather than null")

	for _, body := range []string{
		`{"user_id":1247,"role":"both"}`,
		`{"user_id":1247,"role":""}`,
		`{"user_id":0,"role":"inviter"}`,
		`{"user_id":-3,"role":"invitee"}`,
		`not json`,
	} {
		res := postJSON(t, AddRebateExclusion, "POST", "/api/rebate/exclusions", body, 1)
		assert.Equal(t, false, res["success"], body)
	}

	missing := postJSON(t, AddRebateExclusion, "POST", "/api/rebate/exclusions", `{"user_id":4242,"role":"inviter"}`, 1)
	assert.Equal(t, "User does not exist", missing["message"], "a mistyped ID must not succeed silently")

	createExclusionTarget(t, 4343)
	require.NoError(t, model.DB.Delete(&model.User{}, 4343).Error)
	gone := postJSON(t, AddRebateExclusion, "POST", "/api/rebate/exclusions", `{"user_id":4343,"role":"invitee"}`, 1)
	assert.Equal(t, true, gone["success"], "soft-deleted users can still be excluded: %v", gone["message"])
	require.NoError(t, model.DB.Where("user_id = ?", 4343).Delete(&model.RebateExclusion{}).Error)

	res := postJSON(t, AddRebateExclusion, "POST", "/api/rebate/exclusions", `{"user_id":1247,"role":"inviter","note":"内部账号"}`, 1)
	require.Equal(t, true, res["success"], res["message"])

	dup := postJSON(t, AddRebateExclusion, "POST", "/api/rebate/exclusions", `{"user_id":1247,"role":"inviter"}`, 1)
	assert.Equal(t, false, dup["success"], "the same (user_id, role) must not repeat")

	other := postJSON(t, AddRebateExclusion, "POST", "/api/rebate/exclusions", `{"user_id":1247,"role":"invitee"}`, 1)
	assert.Equal(t, true, other["success"], "the same user may be excluded in the other role")

	listed = postJSON(t, ListRebateExclusions, "GET", "/api/rebate/exclusions", "", 1)
	rows := listed["data"].([]any)
	require.Len(t, rows, 2)
	row := rows[1].(map[string]any)
	assert.Equal(t, float64(1247), row["user_id"])
	assert.Equal(t, "inviter", row["role"])
	assert.Equal(t, "内部账号", row["note"])

	id := common.Interface2String(row["id"])
	out := postJSON(t, DeleteRebateExclusion, "DELETE", "/api/rebate/exclusions/"+id, "", 1, gin.Param{Key: "id", Value: id})
	require.Equal(t, true, out["success"], out["message"])

	var left []model.RebateExclusion
	require.NoError(t, model.DB.Find(&left).Error)
	require.Len(t, left, 1)
	assert.Equal(t, "invitee", left[0].Role)
}

func TestRebateExclusionDeleteRejectsBadIds(t *testing.T) {
	setupRebateControllerTest(t)

	for _, id := range []string{"0", "-1", "abc", "999"} {
		out := postJSON(t, DeleteRebateExclusion, "DELETE", "/api/rebate/exclusions/"+id, "", 1, gin.Param{Key: "id", Value: id})
		assert.Equal(t, false, out["success"], id)
		assert.Equal(t, "Invalid parameters", out["message"], "an unknown id is a parameter error, not a raw driver message")
	}
}

func TestRebateExclusionNoteLimitCountsCharacters(t *testing.T) {
	setupRebateControllerTest(t)

	tests := []struct {
		name    string
		note    string
		success bool
	}{
		{name: "255 CJK characters", note: strings.Repeat("中", 255), success: true},
		{name: "256 CJK characters", note: strings.Repeat("中", 256), success: false},
		{name: "255 ASCII characters", note: strings.Repeat("a", 255), success: true},
		{name: "256 ASCII characters", note: strings.Repeat("a", 256), success: false},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			createExclusionTarget(t, 100+i)
			body, err := common.Marshal(map[string]any{"user_id": 100 + i, "role": "inviter", "note": tt.note})
			require.NoError(t, err)
			res := postJSON(t, AddRebateExclusion, "POST", "/api/rebate/exclusions", string(body), 1)
			assert.Equal(t, tt.success, res["success"], res["message"])
		})
	}
}

func TestRebateExclusionWritesAuditLogs(t *testing.T) {
	db := setupRebateControllerTest(t)
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "root", Password: "12345678", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AffCode: "root-aff"}).Error)
	createExclusionTarget(t, 1247)

	res := postJSON(t, AddRebateExclusion, "POST", "/api/rebate/exclusions", `{"user_id":1247,"role":"inviter"}`, 1)
	require.Equal(t, true, res["success"], res["message"])
	id := common.Interface2String(res["data"].(map[string]any)["id"])
	out := postJSON(t, DeleteRebateExclusion, "DELETE", "/api/rebate/exclusions/"+id, "", 1, gin.Param{Key: "id", Value: id})
	require.Equal(t, true, out["success"], out["message"])

	var logs []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("category = ?", model.AuditCategoryOperation).Order("id asc").Find(&logs).Error)
	require.Len(t, logs, 2)
	assert.Equal(t, "Excluded user 1247 from rebates as inviter", logs[0].Content)
	assert.Equal(t, "Removed user 1247 (inviter) from the rebate exclusion list", logs[1].Content)
}
