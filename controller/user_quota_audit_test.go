package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManageUserQuotaAuditIncludesTargetIdentity(t *testing.T) {
	tests := []struct {
		name           string
		mode           string
		expectedAction string
	}{
		{name: "add", mode: "add", expectedAction: "user.quota_add"},
		{name: "subtract", mode: "subtract", expectedAction: "user.quota_subtract"},
		{name: "override", mode: "override", expectedAction: "user.quota_override"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			originalRedisEnabled := common.RedisEnabled
			common.RedisEnabled = false
			t.Cleanup(func() {
				common.RedisEnabled = originalRedisEnabled
			})
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.AuditLog{}))
			require.NoError(t, db.Create(&model.User{
				Id:       21,
				Username: "sharpeng",
				Password: "test-password",
				Role:     common.RoleRootUser,
				Status:   common.UserStatusEnabled,
				AffCode:  "admin-21",
			}).Error)
			require.NoError(t, db.Create(&model.User{
				Id:       42,
				Username: "target-user",
				Password: "test-password",
				Role:     common.RoleCommonUser,
				Status:   common.UserStatusEnabled,
				Quota:    1000,
				AffCode:  "target-42",
			}).Error)

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(
				http.MethodPost,
				"/api/user/manage",
				strings.NewReader(fmt.Sprintf(`{"id":42,"action":"add_quota","value":500,"mode":%q}`, tt.mode)),
			)
			ctx.Set("id", 21)
			ctx.Set("username", "sharpeng")
			ctx.Set("role", common.RoleRootUser)

			ManageUser(ctx)

			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Success bool `json:"success"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.True(t, response.Success)

			var log model.AuditLog
			require.NoError(t, model.LOG_DB.Where("category = ?", model.AuditCategoryOperation).First(&log).Error)
			require.NotNil(t, log.Other.Op)
			assert.Equal(t, tt.expectedAction, log.Other.Op.Action)
			encoded, err := common.Marshal(log.Other.Op.Params)
			require.NoError(t, err)
			var params map[string]any
			require.NoError(t, common.Unmarshal(encoded, &params))
			assert.Equal(t, float64(42), params["target_user_id"])
			assert.Equal(t, "target-user", params["target_username"])
			assert.Contains(t, log.Content, "target-user (ID: 42)")
			logs, count, err := model.GetAuditLogs(model.AuditLogFilter{TargetUserId: 42}, 0, 20, common.RoleRootUser)
			require.NoError(t, err)
			assert.Equal(t, int64(1), count)
			require.Len(t, logs, 1)
			_, _, err = model.GetAuditLogs(model.AuditLogFilter{TargetUserId: 42, SelfView: true}, 0, 20, common.RoleRootUser)
			require.Error(t, err)

		})
	}
}

func TestAuditTargetUserFilter(t *testing.T) {
	setupManageUserTestDB(t)
	for _, entry := range []model.AuditLog{
		{EventId: "target-42-admin", UserId: 21, ActorRole: common.RoleAdminUser, Category: model.AuditCategoryOperation, Other: model.AuditOther{Op: &model.AuditOperation{Action: "user.quota_add", Params: model.AuditFields{"target_user_id": 42}}}},
		{EventId: "target-420-admin", UserId: 21, ActorRole: common.RoleAdminUser, Category: model.AuditCategoryOperation, Other: model.AuditOther{Op: &model.AuditOperation{Action: "user.quota_add", Params: model.AuditFields{"target_user_id": 420}}}},
		{EventId: "target-42-root", UserId: 22, ActorRole: common.RoleRootUser, Category: model.AuditCategoryOperation, Other: model.AuditOther{Op: &model.AuditOperation{Action: "user.quota_add", Params: model.AuditFields{"target_user_id": 42}}}},
	} {
		require.NoError(t, model.LOG_DB.Create(&entry).Error)
	}
	for _, tc := range []struct {
		name, path, query string
		role              int
		success           bool
		total             int
	}{
		{"root", "/api/audit", "42", common.RoleRootUser, true, 2},
		{"admin", "/api/audit", "42", common.RoleAdminUser, true, 1},
		{"exact ID", "/api/audit", "420", common.RoleRootUser, true, 1},
		{"self", "/api/audit/self", "42", common.RoleRootUser, false, 0},
		{"common", "/api/audit", "42", common.RoleCommonUser, false, 0},
		{"negative", "/api/audit", "-1", common.RoleRootUser, false, 0},
		{"malformed", "/api/audit", "abc", common.RoleRootUser, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.GET(tc.path, func(c *gin.Context) { c.Set("role", tc.role); c.Set("id", 21); GetAuditLogs(c) })
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path+"?category=operation&target_user_id="+tc.query, nil))
			var result struct {
				Success bool `json:"success"`
				Data    struct {
					Total int `json:"total"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
			assert.Equal(t, tc.success, result.Success)
			assert.Equal(t, tc.total, result.Data.Total)
		})
	}
}
