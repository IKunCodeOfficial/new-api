package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/stretchr/testify/assert"
)

func TestPerfMetricsExcludeHiddenGroupsFromAggregates(t *testing.T) {
	t.Setenv("SQL_DSN", "")
	oldDB, oldPath, oldMaster := model.DB, common.SQLitePath, common.IsMasterNode
	oldType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SQLitePath, common.IsMasterNode = filepath.Join(t.TempDir(), "perf.db"), false
	t.Cleanup(func() {
		model.DB, common.SQLitePath, common.IsMasterNode = oldDB, oldPath, oldMaster
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
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(oldGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldRatios))
	})
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
