package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupLogControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)

	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	model.DB = db
	model.LOG_DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)

	t.Cleanup(func() {
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			require.NoError(t, sqlDB.Close())
		}
	})
	return db
}

func TestGetAllLogsRejectsInvalidTargetUserId(t *testing.T) {
	for _, targetUserId := range []string{"", "0", "-1", "invalid", "92233720368547758070"} {
		t.Run(targetUserId, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log/?target_user_id="+targetUserId, nil)

			GetAllLogs(ctx)

			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.False(t, response.Success)
			assert.Equal(t, i18n.MsgInvalidParams, response.Message)
		})
	}
}

func TestGetAllLogsRequiresManageTypeForTargetUserId(t *testing.T) {
	for _, target := range []string{
		"/api/log/?target_user_id=42",
		"/api/log/?type=0&target_user_id=42",
		"/api/log/?type=2&target_user_id=42",
	} {
		t.Run(target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)

			GetAllLogs(ctx)

			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.False(t, response.Success)
			assert.Equal(t, i18n.MsgInvalidParams, response.Message)
		})
	}
}

func TestGetUserLogsIgnoresTargetUserIdAndReturnsOnlyOwnedLogs(t *testing.T) {
	db := setupLogControllerTestDB(t)
	logs := []*model.Log{
		{
			UserId:    1,
			CreatedAt: 100,
			Type:      model.LogTypeManage,
			Content:   "target-42",
			Username:  "admin-1",
			Ip:        "198.51.100.1",
			Other: common.MapToJsonStr(map[string]interface{}{
				"admin_info": map[string]interface{}{"admin_id": 1},
				"audit_info": map[string]interface{}{"route": "/api/user/"},
				"op": map[string]interface{}{
					"params": map[string]interface{}{"target_user_id": 42, "username": "target-42"},
				},
			}),
		},
		{
			UserId:    1,
			CreatedAt: 110,
			Type:      model.LogTypeManage,
			Content:   "target-7",
			Username:  "admin-1",
			Other:     `{"op":{"params":{"target_user_id":7,"username":"target-7"}}}`,
		},
	}
	require.NoError(t, db.Create(&logs).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log/self?type=3&target_user_id=7", nil)
	ctx.Set("id", 42)

	GetUserLogs(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Total int          `json:"total"`
			Items []*model.Log `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Zero(t, response.Data.Total)
	assert.Empty(t, response.Data.Items)
}
