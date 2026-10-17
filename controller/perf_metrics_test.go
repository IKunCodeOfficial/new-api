package controller

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	modelstatus "github.com/QuantumNous/new-api/pkg/model_status"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupPerfMetricsTestDB opens a fresh SQLite main database without Redis and
// restores the group settings the tests change.
func setupPerfMetricsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("SQL_DSN", "")
	oldDB, oldPath, oldMaster, oldRedis := model.DB, common.SQLitePath, common.IsMasterNode, common.RedisEnabled
	oldType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SQLitePath, common.IsMasterNode, common.RedisEnabled = filepath.Join(t.TempDir(), "perf.db"), false, false
	t.Cleanup(func() {
		model.DB, common.SQLitePath, common.IsMasterNode, common.RedisEnabled = oldDB, oldPath, oldMaster, oldRedis
		common.SetDatabaseTypes(oldType, oldLogType)
	})
	require.NoError(t, model.InitDB())
	db := model.DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.PerfMetric{}))
	oldGroups := setting.UserUsableGroups2JSONString()
	oldRatios := ratio_setting.GroupRatio2JSONString()
	oldGroupGroupRatios := ratio_setting.GroupGroupRatio2JSONString()
	oldSpecialGroups := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.ReadAll()
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(oldGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldRatios))
		require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(oldGroupGroupRatios))
		specialGroups := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup
		specialGroups.Clear()
		specialGroups.AddAll(oldSpecialGroups)
	})
	return db
}

func TestPerfMetricsExcludeHiddenGroupsFromAggregates(t *testing.T) {
	db := setupPerfMetricsTestDB(t)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Public"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"internal":1}`))
	timestamp := time.Now().Unix()
	timestamp -= timestamp % 3600
	require.NoError(t, db.Create(&[]model.PerfMetric{
		{ModelName: "visibility-model", Group: "default", BucketTs: timestamp, RequestCount: 1, SuccessCount: 1, TotalLatencyMs: 100},
		{ModelName: "visibility-model", Group: "internal", BucketTs: timestamp, RequestCount: 9, SuccessCount: 0, TotalLatencyMs: 90000},
	}).Error)
	for _, group := range []string{"", "internal"} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/pricing/performance?model=visibility-model&group="+group, nil)
		GetPerfMetrics(c)
		var response struct {
			Success bool                    `json:"success"`
			Data    perfmetrics.QueryResult `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.True(t, response.Success)
		if group == "internal" {
			assert.Empty(t, response.Data.Groups)
			assert.Nil(t, response.Data.Summary)
			continue
		}
		require.NotNil(t, response.Data.Summary)
		assert.Equal(t, float64(100), response.Data.Summary.SuccessRate)
		assert.Equal(t, int64(100), response.Data.Summary.AvgLatencyMs)
		require.Len(t, response.Data.Series, 1)
		assert.Equal(t, float64(100), response.Data.Series[0].SuccessRate)
	}
}

func TestModelStatusViewerScopesGroupsAndRatios(t *testing.T) {
	db := setupPerfMetricsTestDB(t)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Public"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":2,"internal":3}`))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{"vip":{"default":0.5}}`))
	specialGroups := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup
	specialGroups.Clear()
	specialGroups.Set("vip", map[string]string{"+:vip": "VIP"})
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, db.Create(&[]model.User{
		{Id: 9101, Username: "status-vip", Password: "password", AffCode: "status-vip", Group: "vip", Role: common.RoleCommonUser, Status: common.UserStatusEnabled},
		{Id: 9102, Username: "status-admin", Password: "password", AffCode: "status-admin", Group: "default", Role: common.RoleAdminUser, Status: common.UserStatusEnabled},
	}).Error)

	// The auth middleware sets id, role and group together; the viewer reads
	// role and group from there. Without them it falls back to the user lookup.
	viewerFor := func(userID int, fromMiddleware bool) modelstatus.Viewer {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/model_status", nil)
		if userID != 0 {
			c.Set("id", userID)
		}
		if userID != 0 && fromMiddleware {
			var user model.User
			require.NoError(t, db.First(&user, userID).Error)
			c.Set("role", user.Role)
			c.Set("group", user.Group)
		}
		return modelStatusViewer(c)
	}

	guest := viewerFor(0, true)
	assert.Equal(t, map[string]string{"default": "Public"}, guest.Groups)
	assert.Equal(t, map[string]float64{"default": 1}, guest.Ratios)

	assert.Equal(t, viewerFor(9101, false), viewerFor(9101, true))
	assert.Equal(t, viewerFor(9102, false), viewerFor(9102, true))

	vip := viewerFor(9101, true)
	assert.ElementsMatch(t, []string{"default", "vip"}, slices.Collect(maps.Keys(vip.Groups)))
	assert.Equal(t, "VIP", vip.Groups["vip"])
	assert.Equal(t, "Public", vip.Groups["default"])
	assert.Equal(t, map[string]float64{"default": 0.5, "vip": 2}, vip.Ratios)

	admin := viewerFor(9102, true)
	assert.ElementsMatch(t, []string{"default", "vip", "internal"}, slices.Collect(maps.Keys(admin.Groups)))
	assert.Equal(t, "Public", admin.Groups["default"])
	assert.Equal(t, map[string]float64{"default": 1, "vip": 2, "internal": 3}, admin.Ratios)

	// Ratios use the group from the auth context, not a second user lookup.
	// Visibility still comes from getUsablePerfMetricGroups.
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/model_status", nil)
	c.Set("id", 9199) // not in the users table
	c.Set("role", common.RoleCommonUser)
	c.Set("group", "vip")
	assert.Equal(t, map[string]float64{"default": 0.5}, modelStatusViewer(c).Ratios)
}

func TestGetModelStatusRejectsBadRange(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/model_status?range=2h", nil)

	GetModelStatus(c)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	var response struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
}
