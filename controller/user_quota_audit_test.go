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
			db := setupLogControllerTestDB(t)
			originalRedisEnabled := common.RedisEnabled
			common.RedisEnabled = false
			t.Cleanup(func() {
				common.RedisEnabled = originalRedisEnabled
			})
			require.NoError(t, db.AutoMigrate(&model.User{}))
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

			var log model.Log
			require.NoError(t, db.Where("type = ?", model.LogTypeManage).First(&log).Error)
			other, err := common.StrToMap(log.Other)
			require.NoError(t, err)
			op, ok := other["op"].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, tt.expectedAction, op["action"])
			params, ok := op["params"].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, float64(42), params["target_user_id"])
			assert.Equal(t, "target-user", params["username"])
			assert.Contains(t, log.Content, "target-user (ID: 42)")
		})
	}
}
