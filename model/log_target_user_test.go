package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetTargetUserLogTestRows(t *testing.T) {
	t.Helper()
	require.NoError(t, LOG_DB.Exec("DELETE FROM logs").Error)
	t.Cleanup(func() {
		require.NoError(t, LOG_DB.Exec("DELETE FROM logs").Error)
	})
}

func insertTargetUserLogTestRows(t *testing.T) {
	t.Helper()
	logs := []*Log{
		{
			UserId:    42,
			CreatedAt: 100,
			Type:      LogTypeManage,
			Content:   "old-owner",
			Username:  "target-42",
			Ip:        "198.51.100.42",
		},
		{
			UserId:    1,
			CreatedAt: 110,
			Type:      LogTypeManage,
			Content:   "new-target-comma",
			Username:  "admin-1",
			Ip:        "198.51.100.1",
			Other: common.MapToJsonStr(map[string]interface{}{
				"admin_info": map[string]interface{}{"admin_id": 1, "admin_username": "admin-1"},
				"audit_info": map[string]interface{}{"route": "/api/user/"},
				"op": map[string]interface{}{
					"action": "user.update",
					"params": map[string]interface{}{"target_user_id": 42, "username": "target-42"},
				},
			}),
		},
		{
			UserId:    1,
			CreatedAt: 120,
			Type:      LogTypeManage,
			Content:   "new-target-object-end",
			Username:  "admin-1",
			Ip:        "198.51.100.2",
			Other:     `{"op":{"params":{"target_user_id":42}}}`,
		},
		{
			UserId:    1,
			CreatedAt: 130,
			Type:      LogTypeManage,
			Content:   "different-target-prefix",
			Other:     `{"op":{"params":{"target_user_id":420}}}`,
		},
		{
			UserId:    1,
			CreatedAt: 140,
			Type:      LogTypeManage,
			Content:   "similar-key",
			Other:     `{"op":{"params":{"targetXuserYid":42}}}`,
		},
		{
			UserId:    1,
			CreatedAt: 150,
			Type:      LogTypeConsume,
			Content:   "non-manage-target",
			Other:     `{"op":{"params":{"target_user_id":42}}}`,
		},
		{
			UserId:    42,
			CreatedAt: 155,
			Type:      LogTypeManage,
			Content:   "new-operator-resource",
			Username:  "target-42",
			Other:     `{"admin_info":{"admin_id":42},"op":{"action":"channel.update","params":{"id":9}}}`,
		},
		{
			UserId:    42,
			CreatedAt: 156,
			Type:      LogTypeManage,
			Content:   "new-operator-different-target",
			Username:  "target-42",
			Other:     `{"admin_info":{"admin_id":42},"op":{"action":"user.update","params":{"target_user_id":7}}}`,
		},
		{
			UserId:    42,
			CreatedAt: 160,
			Type:      LogTypeConsume,
			Content:   "owned-consume",
			Username:  "target-42",
		},
	}
	require.NoError(t, LOG_DB.Create(&logs).Error)
}

func logContents(logs []*Log) []string {
	contents := make([]string, 0, len(logs))
	for _, log := range logs {
		contents = append(contents, log.Content)
	}
	return contents
}

func TestGetAllLogsFiltersManagementLogsByTargetUser(t *testing.T) {
	resetTargetUserLogTestRows(t)
	insertTargetUserLogTestRows(t)

	logs, total, err := GetAllLogs(LogTypeManage, 0, 0, "", "", "", 0, 100, 0, "", "", "", 42)

	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.ElementsMatch(t, []string{"old-owner", "new-target-comma", "new-target-object-end"}, logContents(logs))
	for _, log := range logs {
		assert.Equal(t, LogTypeManage, log.Type)
	}
	assert.NotContains(t, logContents(logs), "new-operator-resource")
	assert.NotContains(t, logContents(logs), "new-operator-different-target")
}

func TestGetUserLogsIncludesOldAndNewManagementLogFormats(t *testing.T) {
	resetTargetUserLogTestRows(t)
	insertTargetUserLogTestRows(t)

	manageLogs, manageTotal, err := GetUserLogs(42, LogTypeManage, 0, 0, "", "", 0, 100, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, int64(3), manageTotal)
	assert.ElementsMatch(t, []string{"old-owner", "new-target-comma", "new-target-object-end"}, logContents(manageLogs))

	allLogs, allTotal, err := GetUserLogs(42, LogTypeUnknown, 0, 0, "", "", 0, 100, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, int64(4), allTotal)
	assert.ElementsMatch(t, []string{"old-owner", "new-target-comma", "new-target-object-end", "owned-consume"}, logContents(allLogs))
	assert.NotContains(t, logContents(allLogs), "new-operator-resource")
	assert.NotContains(t, logContents(allLogs), "new-operator-different-target")

	consumeLogs, consumeTotal, err := GetUserLogs(42, LogTypeConsume, 0, 0, "", "", 0, 100, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, int64(1), consumeTotal)
	assert.Equal(t, []string{"owned-consume"}, logContents(consumeLogs))
}

func TestGetUserLogsAllTypesAppliesFiltersToOwnerAndTargetBranches(t *testing.T) {
	resetTargetUserLogTestRows(t)
	logs := []*Log{
		{
			UserId:    42,
			CreatedAt: 200,
			Type:      LogTypeConsume,
			Content:   "matching-owner",
			ModelName: "wanted-model",
			TokenName: "wanted-token",
			RequestId: "owner-match",
		},
		{
			UserId:    1,
			CreatedAt: 210,
			Type:      LogTypeManage,
			Content:   "matching-target",
			ModelName: "wanted-model",
			TokenName: "wanted-token",
			RequestId: "target-match",
			Other:     `{"op":{"params":{"target_user_id":42}}}`,
		},
		{
			UserId:    42,
			CreatedAt: 100,
			Type:      LogTypeConsume,
			Content:   "owner-before-range",
			ModelName: "wanted-model",
			TokenName: "wanted-token",
			RequestId: "owner-before",
		},
		{
			UserId:    1,
			CreatedAt: 400,
			Type:      LogTypeManage,
			Content:   "target-after-range",
			ModelName: "wanted-model",
			TokenName: "wanted-token",
			RequestId: "target-after",
			Other:     `{"op":{"params":{"target_user_id":42}}}`,
		},
		{
			UserId:    42,
			CreatedAt: 220,
			Type:      LogTypeConsume,
			Content:   "owner-wrong-model",
			ModelName: "other-model",
			TokenName: "wanted-token",
			RequestId: "owner-model",
		},
		{
			UserId:    1,
			CreatedAt: 230,
			Type:      LogTypeManage,
			Content:   "target-wrong-token",
			ModelName: "wanted-model",
			TokenName: "other-token",
			RequestId: "target-token",
			Other:     `{"op":{"params":{"target_user_id":42}}}`,
		},
	}
	require.NoError(t, LOG_DB.Create(&logs).Error)

	filteredLogs, total, err := GetUserLogs(
		42,
		LogTypeUnknown,
		190,
		300,
		"wanted-model",
		"wanted-token",
		0,
		100,
		"",
		"",
		"",
	)

	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.ElementsMatch(t, []string{"matching-owner", "matching-target"}, logContents(filteredLogs))
}

func TestGetUserLogsRedactsOperatorIdentityFromTargetManagementLogs(t *testing.T) {
	resetTargetUserLogTestRows(t)
	insertTargetUserLogTestRows(t)

	logs, _, err := GetUserLogs(42, LogTypeManage, 0, 0, "", "", 0, 100, "", "", "")
	require.NoError(t, err)

	byContent := make(map[string]*Log, len(logs))
	for _, log := range logs {
		byContent[log.Content] = log
	}
	newLog := byContent["new-target-comma"]
	require.NotNil(t, newLog)
	assert.Equal(t, 42, newLog.UserId)
	assert.Equal(t, "target-42", newLog.Username)
	assert.Empty(t, newLog.Ip)

	other, err := common.StrToMap(newLog.Other)
	require.NoError(t, err)
	assert.NotContains(t, other, "admin_info")
	assert.NotContains(t, other, "audit_info")
	op, ok := other["op"].(map[string]interface{})
	require.True(t, ok)
	params, ok := op["params"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(42), params["target_user_id"])

	oldLog := byContent["old-owner"]
	require.NotNil(t, oldLog)
	assert.Empty(t, oldLog.Ip)
}

func TestFormatUserLogsDropsMalformedTargetLogMetadata(t *testing.T) {
	logs := []*Log{{
		UserId:   1,
		Type:     LogTypeManage,
		Username: "admin-1",
		Ip:       "198.51.100.1",
		Other:    `{"admin_info":{"admin_id":1},"target_user_id":42`,
	}}

	formatUserLogs(logs, 0, 42)

	assert.Equal(t, 42, logs[0].UserId)
	assert.Empty(t, logs[0].Username)
	assert.Empty(t, logs[0].Ip)
	assert.Equal(t, "null", logs[0].Other)
}
