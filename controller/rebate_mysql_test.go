package controller

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestRebateMySQLControllerContracts(t *testing.T) {
	dsn := os.Getenv("REBATE_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("REBATE_MYSQL_TEST_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	var database string
	require.NoError(t, db.Raw("SELECT DATABASE()").Scan(&database).Error)
	require.True(t, strings.HasPrefix(database, "rebate_"), "requires a dedicated rebate_* test database")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	tx := db.Begin()
	require.NoError(t, tx.Error)
	model.DB, model.LOG_DB = tx, tx
	common.SetDatabaseTypes(common.DatabaseTypeMySQL, common.DatabaseTypeMySQL)
	common.RedisEnabled = false
	resetRebateInfoCache()
	t.Cleanup(func() {
		assert.NoError(t, tx.Rollback().Error)
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled = previousRedis
		resetRebateInfoCache()
	})
	// The restored database has been initialized by the Python settler. Query
	// that actual schema through the Go endpoint, including disabled metadata.
	require.NoError(t, tx.Model(&model.Option{}).Where(map[string]any{"key": "IkunRebateEnabled"}).Update("value", "true").Error)
	require.NoError(t, tx.Model(&model.Option{}).Where(map[string]any{"key": "IkunRebateRate"}).Update("value", "0.05").Error)
	info := postJSON(t, GetRebateInfo, "GET", "/api/rebate_info", "", 0)
	assert.Equal(t, true, info["enabled"])
	assert.Equal(t, 0.05, info["rate"])
	assert.Equal(t, "Asia/Shanghai", info["timezone"])
	require.NoError(t, tx.Model(&model.Option{}).Where(map[string]any{"key": "IkunRebateEnabled"}).Update("value", "false").Error)
	resetRebateInfoCache()
	info = postJSON(t, GetRebateInfo, "GET", "/api/rebate_info", "", 0)
	assert.Equal(t, false, info["enabled"])
	assert.NotEmpty(t, info["activity_start"])

	var target model.User
	require.NoError(t, tx.Where("role = ? AND status = ?", common.RoleCommonUser, common.UserStatusEnabled).First(&target).Error)
	initialQuota := target.Quota
	for _, paid := range []int{0, -1, 10001} {
		body := fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":10000,"recharge":true,"paid_quota":%d}`, target.Id, paid)
		out := postJSON(t, ManageUser, "POST", "/api/user/manage", body, 1)
		assert.Equal(t, false, out["success"])
	}
	require.NoError(t, tx.First(&target, target.Id).Error)
	assert.Equal(t, initialQuota, target.Quota)
	body := fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":10000,"recharge":true}`, target.Id)
	out := postJSON(t, ManageUser, "POST", "/api/user/manage", body, 1)
	require.Equal(t, true, out["success"], out["message"])
	var recharge model.RebateAdminRecharge
	require.NoError(t, tx.Where("user_id = ?", target.Id).Order("id desc").First(&recharge).Error)
	assert.Equal(t, int64(10000), recharge.Quota)
	assert.Equal(t, int64(10000), recharge.PaidQuota)
	require.NoError(t, tx.First(&target, target.Id).Error)
	assert.Equal(t, initialQuota+10000, target.Quota)
}
