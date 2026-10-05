package controller

// Fork-only file (ikun): shared in-memory DB fixture for invite-rebate tests.
// It reuses setupLogControllerTestDB (controller/log_test.go) for the DB
// lifecycle and only adds the models rebate controllers touch.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupRebateControllerTest(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupLogControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Option{}, &model.TopUp{},
		&model.RebateAdminRecharge{}, &model.RebateTopupPaid{}, &model.RebateExclusion{},
	))

	// The rebate paths credit the user quota cache; tests must not reach a real
	// Redis instance.
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })

	return db
}

func createRebateTestUser(t *testing.T, quota int, role int) int {
	t.Helper()
	user := &model.User{
		Username: "rb" + common.GetRandomString(8),
		Password: "12345678",
		Quota:    quota,
		Role:     role,
		Status:   common.UserStatusEnabled,
		Group:    "default",
		AffCode:  common.GetRandomString(8), // aff_code 有唯一索引，不能留空重复
	}
	require.NoError(t, model.DB.Create(user).Error)
	return user.Id
}

// postJSON calls handler as a root user and decodes the JSON response body.
// params sets route parameters such as :id.
func postJSON(t *testing.T, handler gin.HandlerFunc, method, path, body string, operatorId int, params ...gin.Param) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("id", operatorId)
	c.Set("role", common.RoleRootUser)
	handler(c)
	require.Equal(t, http.StatusOK, w.Code)
	var out map[string]any
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &out))
	return out
}
