package controller

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func manageAddQuota(t *testing.T, userId int, extra string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":10000%s}`, userId, extra)
	return postJSON(t, ManageUser, "POST", "/api/user/manage", body, 1)
}

func userQuota(t *testing.T, userId int) int {
	t.Helper()
	var u model.User
	require.NoError(t, model.DB.First(&u, userId).Error)
	return u.Quota
}

func rechargeRows(t *testing.T, userId int) []model.RebateAdminRecharge {
	t.Helper()
	var rows []model.RebateAdminRecharge
	require.NoError(t, model.DB.Where("user_id = ?", userId).Order("id").Find(&rows).Error)
	return rows
}

func TestManageAddQuotaRechargeRejectsBadPaidQuota(t *testing.T) {
	setupRebateControllerTest(t)
	target := createRebateTestUser(t, 1000, common.RoleCommonUser)

	for _, extra := range []string{
		`,"recharge":true,"paid_quota":0`,
		`,"recharge":true,"paid_quota":-5`,
		`,"recharge":true,"paid_quota":10001`,
	} {
		res := manageAddQuota(t, target, extra)
		assert.Equal(t, false, res["success"], extra)
	}
	assert.Equal(t, 1000, userQuota(t, target), "越界时额度不得变化")
	assert.Empty(t, rechargeRows(t, target))
}

func TestManageAddQuotaRechargeDefaultsPaidToValue(t *testing.T) {
	setupRebateControllerTest(t)
	target := createRebateTestUser(t, 1000, common.RoleCommonUser)

	res := manageAddQuota(t, target, `,"recharge":true`)
	require.Equal(t, true, res["success"], res["message"])
	assert.Equal(t, 11000, userQuota(t, target))
	rows := rechargeRows(t, target)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(10000), rows[0].Quota)
	assert.Equal(t, int64(10000), rows[0].PaidQuota)
	assert.Equal(t, 1, rows[0].OperatorId)
}

func TestManageAddQuotaRechargeWithDiscount(t *testing.T) {
	setupRebateControllerTest(t)
	target := createRebateTestUser(t, 1000, common.RoleCommonUser)

	res := manageAddQuota(t, target, `,"recharge":true,"paid_quota":9000`)
	require.Equal(t, true, res["success"], res["message"])
	rows := rechargeRows(t, target)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(9000), rows[0].PaidQuota)
}

func TestManageAddQuotaWithoutRechargeWritesNoRow(t *testing.T) {
	setupRebateControllerTest(t)
	target := createRebateTestUser(t, 1000, common.RoleCommonUser)

	// paid_quota 在未勾选充值时被忽略
	res := manageAddQuota(t, target, `,"paid_quota":1`)
	require.Equal(t, true, res["success"], res["message"])
	assert.Equal(t, 11000, userQuota(t, target))
	assert.Empty(t, rechargeRows(t, target))
}

func TestManageAddQuotaRechargeRespectsWalletCeiling(t *testing.T) {
	setupRebateControllerTest(t)
	target := createRebateTestUser(t, common.MaxWalletQuota-1, common.RoleCommonUser)

	res := manageAddQuota(t, target, `,"recharge":true`)
	require.Equal(t, false, res["success"])
	assert.Equal(t, common.MaxWalletQuota-1, userQuota(t, target))
	assert.Empty(t, rechargeRows(t, target), "额度未到账时不得留下充值记录")
}

func TestManageAddQuotaRechargeRollsBackWithPaidRecord(t *testing.T) {
	db := setupRebateControllerTest(t)
	target := createRebateTestUser(t, 1000, common.RoleCommonUser)
	require.NoError(t, db.Migrator().DropTable(&model.RebateAdminRecharge{}))
	response := manageAddQuota(t, target, `,"recharge":true,"paid_quota":9000`)
	require.Equal(t, false, response["success"])
	assert.Equal(t, 1000, userQuota(t, target))
	var audit model.AuditLog
	require.NoError(t, model.LOG_DB.Where("category = ?", model.AuditCategoryOperation).First(&audit).Error)
	assert.False(t, audit.Success)
	encoded, err := common.Marshal(audit.Other.Op.Params)
	require.NoError(t, err)
	var params map[string]any
	require.NoError(t, common.Unmarshal(encoded, &params))
	assert.Equal(t, "database_error", params["failure_reason"])
	var topups int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", model.LogTypeTopup).Count(&topups).Error)
	assert.Zero(t, topups)
}

func TestManageAddQuotaRechargeEnforcesAdjustmentPermissions(t *testing.T) {
	setupRebateControllerTest(t)
	target := createRebateTestUser(t, 1000, common.RoleAdminUser)
	_, err := model.AdjustUserQuota(target, common.RoleAdminUser, "add", 10000, &model.UserQuotaRecharge{OperatorID: 1, PaidQuota: 9000})
	require.ErrorIs(t, err, model.ErrUserQuotaPermission)
	assert.Equal(t, 1000, userQuota(t, target))
	assert.Empty(t, rechargeRows(t, target))
	for _, mode := range []string{"subtract", "override"} {
		_, err = model.AdjustUserQuota(target, common.RoleRootUser, mode, 10000, &model.UserQuotaRecharge{OperatorID: 1, PaidQuota: 9000})
		require.ErrorIs(t, err, model.ErrInvalidUserQuotaAdjustment)
	}
	assert.Equal(t, 1000, userQuota(t, target))
	assert.Empty(t, rechargeRows(t, target))
}
