package model

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// These opt-in tests use a restored MySQL user's real schema and restore the
// user's financial state. The DSN must name a dedicated rebate_* test database.
func setupRebateMySQLTest(t *testing.T) *User {
	t.Helper()
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
	previousDB, previousLogDB := DB, LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	DB, LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeMySQL, common.DatabaseTypeMySQL)
	var user User
	require.NoError(t, DB.Where("status = ?", common.UserStatusEnabled).First(&user).Error)
	original := user
	t.Cleanup(func() {
		assert.NoError(t, db.Model(&User{}).Where("id = ?", original.Id).Updates(map[string]any{
			"quota": original.Quota, "aff_quota": original.AffQuota, "aff_history": original.AffHistoryQuota,
		}).Error)
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		assert.NoError(t, sqlDB.Close())
	})
	return &user
}

func TestRebateMySQLTransferRejectsWalletOverflow(t *testing.T) {
	user := setupRebateMySQLTest(t)
	withRebatePrices(t)
	require.NoError(t, DB.Model(user).Updates(map[string]any{
		"quota": common.MaxWalletQuota - 1, "aff_quota": 500000, "aff_history": 500000,
	}).Error)

	err := user.TransferAffQuotaToQuota(500000)

	assert.ErrorIs(t, err, ErrWalletQuotaLimitExceeded)
	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, common.MaxWalletQuota-1, got.Quota)
	assert.Equal(t, 500000, got.AffQuota)
	assert.Equal(t, 500000, got.AffHistoryQuota)
}

func TestRebateMySQLTransferAtWalletLimitPreservesHistory(t *testing.T) {
	user := setupRebateMySQLTest(t)
	withRebatePrices(t)
	require.NoError(t, DB.Model(user).Updates(map[string]any{
		"quota": common.MaxWalletQuota - 500000, "aff_quota": 600000, "aff_history": 900000,
	}).Error)

	require.NoError(t, user.TransferAffQuotaToQuota(500000))

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, common.MaxWalletQuota, got.Quota)
	assert.Equal(t, 100000, got.AffQuota)
	assert.Equal(t, 900000, got.AffHistoryQuota)
}

func TestRebateMySQLRechargeLifecycle(t *testing.T) {
	user := setupRebateMySQLTest(t)
	withRebatePrices(t)
	baseDB := DB
	tx := DB.Begin()
	require.NoError(t, tx.Error)
	DB, LOG_DB = tx, tx
	t.Cleanup(func() {
		assert.NoError(t, tx.Rollback().Error)
		DB, LOG_DB = baseDB, baseDB
	})
	initialQuota := user.Quota
	order := &TopUp{
		UserId: user.Id, Amount: 10, Money: 65.7, TradeNo: "rebate-mysql-" + common.GetRandomString(16),
		PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay,
		CreateTime: common.GetTimestamp(), Status: common.TopUpStatusPending,
	}
	require.NoError(t, order.Insert())
	var paid RebateTopupPaid
	require.NoError(t, DB.Where("trade_no = ?", order.TradeNo).First(&paid).Error)
	assert.Equal(t, int64(4500000), paid.PaidQuota)
	alreadyDone, err := RechargeEpay(order.TradeNo, "alipay", "127.0.0.1")
	require.NoError(t, err)
	assert.False(t, alreadyDone)
	alreadyDone, err = RechargeEpay(order.TradeNo, "alipay", "127.0.0.1")
	require.NoError(t, err)
	assert.True(t, alreadyDone)
	require.NoError(t, DB.First(order, order.Id).Error)
	assert.Positive(t, order.CompleteTime)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)

	_, err = AdjustUserQuota(user.Id, common.RoleRootUser, "add", 1000000, &UserQuotaRecharge{OperatorID: user.Id, PaidQuota: 900000})
	require.NoError(t, err)
	var recharge RebateAdminRecharge
	require.NoError(t, DB.Where("user_id = ?", user.Id).Order("id desc").First(&recharge).Error)
	assert.Equal(t, int64(1000000), recharge.Quota)
	assert.Equal(t, int64(900000), recharge.PaidQuota)
	_, err = AdjustUserQuota(user.Id, common.RoleRootUser, "add", 1000000, &UserQuotaRecharge{OperatorID: user.Id, PaidQuota: 1000001})
	assert.ErrorIs(t, err, ErrInvalidUserQuotaAdjustment)
	var rechargeCount int64
	require.NoError(t, DB.Model(&RebateAdminRecharge{}).Where("user_id = ?", user.Id).Count(&rechargeCount).Error)
	assert.Equal(t, int64(1), rechargeCount)

	// Redeem an actual unused code from the backup, then prove a retry cannot
	// credit it twice. The transaction restores both the code and user's quota.
	var code Redemption
	require.NoError(t, DB.Where("status = ? AND quota > 0 AND (expired_time = 0 OR expired_time > ?)",
		common.RedemptionCodeStatusEnabled, common.GetTimestamp()).First(&code).Error)
	quota, err := Redeem(code.Key, user.Id)
	require.NoError(t, err)
	assert.Equal(t, code.Quota, quota)
	_, err = Redeem(code.Key, user.Id)
	assert.ErrorIs(t, err, ErrRedeemCodeUsed)
	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, initialQuota+6000000+code.Quota, got.Quota)
}
