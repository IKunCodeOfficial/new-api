package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// openRedemptionTestDB points model.DB / model.LOG_DB at an empty test database of
// the given dialect with the user, redemption and audit tables, and restores the
// previous state on cleanup. MySQL and PostgreSQL run only when their DSN is set.
func openRedemptionTestDB(t *testing.T, dialect string) (*gorm.DB, *gorm.DB) {
	t.Helper()
	var driver, logDriver gorm.Dialector
	dbType := common.DatabaseTypeSQLite
	switch dialect {
	case "sqlite":
		driver = sqlite.Open(":memory:")
		logDriver = sqlite.Open(":memory:")
	case "mysql":
		dsn := os.Getenv("TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("TEST_MYSQL_DSN is not configured")
		}
		driver = mysql.Open(dsn)
		logDSN := os.Getenv("TEST_MYSQL_LOG_DSN")
		if logDSN == "" {
			logDSN = dsn
		}
		logDriver = mysql.Open(logDSN)
		dbType = common.DatabaseTypeMySQL
	case "postgres":
		dsn := os.Getenv("TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN is not configured")
		}
		driver = postgres.Open(dsn)
		logDSN := os.Getenv("TEST_POSTGRES_LOG_DSN")
		if logDSN == "" {
			logDSN = dsn
		}
		logDriver = postgres.Open(logDSN)
		dbType = common.DatabaseTypePostgreSQL
	}
	db, err := gorm.Open(driver, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	var version string
	query := "SELECT version()"
	if dialect == "sqlite" {
		query = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(query).Scan(&version).Error)
	t.Logf("database version: %s", version)

	logDB, err := gorm.Open(logDriver, &gorm.Config{})
	require.NoError(t, err)
	logSQL, err := logDB.DB()
	require.NoError(t, err)
	logSQL.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, logSQL.Close()) })
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	model.DB, model.LOG_DB = db, logDB
	common.SetDatabaseTypes(dbType, dbType)
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled = previousRedis
	})
	for _, table := range []any{&model.User{}, &model.Redemption{}, &model.Option{}} {
		require.False(t, db.Migrator().HasTable(table), "use an empty test database")
		require.NoError(t, db.AutoMigrate(table))
		t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(table)) })
	}
	// The admin authenticates with a legacy access token, which is accepted only
	// inside the transition window recorded at startup.
	require.NoError(t, model.EnsureLegacyAccessTokenRetireAt(time.Now().Unix()))
	require.False(t, logDB.Migrator().HasTable(&model.AuditLog{}), "use an empty test log database")
	require.NoError(t, logDB.AutoMigrate(&model.AuditLog{}))
	t.Cleanup(func() { require.NoError(t, logDB.Migrator().DropTable(&model.AuditLog{})) })
	return db, logDB
}

func TestDeleteRedemptionBatch(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db, logDB := openRedemptionTestDB(t, dialect)
			token := "redemption-audit-test-token"
			admin := model.User{Username: "redemption-audit-admin", Password: "unused", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AccessToken: &token}
			require.NoError(t, db.Create(&admin).Error)
			codes := make([]model.Redemption, 16)
			for index := range codes {
				codes[index] = model.Redemption{Name: "selected", Key: fmt.Sprintf("%032d", index+1), Quota: 100, Status: common.RedemptionCodeStatusEnabled}
			}
			codes[1].Status = common.RedemptionCodeStatusUsed
			codes[15].Name = "unselected"
			codes[15].Status = common.RedemptionCodeStatusDisabled
			require.NoError(t, model.DB.Create(&codes).Error)
			router := gin.New()
			router.Use(middleware.RequestId())
			router.POST("/api/redemption/batch", middleware.AdminAuth(), DeleteRedemptionBatch)

			overLimit := make([]int, 1001)
			for index := range overLimit {
				overLimit[index] = codes[0].Id
			}
			oversized, err := common.Marshal(map[string]any{"ids": overLimit})
			require.NoError(t, err)
			for _, body := range []string{"{}", `{"ids":[]}`, `{"ids":null}`, `{"ids":[0]}`, `{"ids":[1,-1]}`, `{"ids":["1"]}`, "{", string(oversized)} {
				t.Run("invalid_"+body[:min(len(body), 30)], func(t *testing.T) {
					response := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodPost, "/api/redemption/batch", bytes.NewBufferString(body))
					request.Header.Set("Authorization", "Bearer "+token)
					router.ServeHTTP(response, request)
					var result struct {
						Success bool `json:"success"`
					}
					require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
					assert.False(t, result.Success)
					var count int64
					require.NoError(t, model.DB.Model(&model.Redemption{}).Count(&count).Error)
					assert.EqualValues(t, 16, count)
					var events []model.AuditLog
					require.NoError(t, logDB.Where("request_id = ? AND category = ?", response.Header().Get(common.RequestIdKey), model.AuditCategoryOperation).Find(&events).Error)
					require.Len(t, events, 1)
					assert.False(t, events[0].Success)
					assert.Equal(t, "redemption.delete_batch", events[0].Action)
				})
			}
			_, err = model.BatchDeleteRedemptions(nil)
			require.Error(t, err)
			requestedIDs := make([]int, 0, 17)
			for _, code := range codes[:15] {
				requestedIDs = append(requestedIDs, code.Id)
			}
			requestedIDs = append(requestedIDs, codes[0].Id, 999999)
			payload, err := common.Marshal(map[string]any{"ids": requestedIDs})
			require.NoError(t, err)
			for _, expectedCount := range []int64{15, 0} {
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/api/redemption/batch", bytes.NewReader(payload))
				request.Header.Set("Authorization", "Bearer "+token)
				router.ServeHTTP(response, request)
				assert.Equal(t, http.StatusOK, response.Code)
				var result struct {
					Success bool  `json:"success"`
					Data    int64 `json:"data"`
				}
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
				assert.True(t, result.Success)
				assert.Equal(t, expectedCount, result.Data)
				var events []model.AuditLog
				require.NoError(t, logDB.Where("request_id = ? AND category = ?", response.Header().Get(common.RequestIdKey), model.AuditCategoryOperation).Find(&events).Error)
				require.Len(t, events, 1, "one operation event, without a duplicate single-delete fallback")
				event := events[0]
				assert.Equal(t, "redemption.delete_batch", event.Action)
				assert.Equal(t, fmt.Sprintf("Batch deleted %d redemption codes", expectedCount), event.Content)
				assert.True(t, event.Success)
				assert.Equal(t, admin.Id, event.UserId)
				assert.Equal(t, "/api/redemption/batch", event.Route)
				require.NotNil(t, event.Other.Op)
				encoded, err := common.Marshal(event.Other.Op.Params)
				require.NoError(t, err)
				var params struct {
					Count int64 `json:"count"`
					Total int   `json:"total"`
					IDs   []int `json:"requested_redemption_ids"`
				}
				require.NoError(t, common.Unmarshal(encoded, &params))
				assert.Equal(t, expectedCount, params.Count)
				assert.Equal(t, len(requestedIDs), params.Total)
				assert.Equal(t, requestedIDs, params.IDs)
				encoded, err = common.Marshal(event)
				require.NoError(t, err)
				assert.NotContains(t, string(encoded), token)
				for _, code := range codes {
					assert.NotContains(t, string(encoded), code.Key)
				}
			}
			var active []model.Redemption
			require.NoError(t, model.DB.Find(&active).Error)
			require.Len(t, active, 1)
			assert.Equal(t, codes[15], active[0])
			var all []model.Redemption
			require.NoError(t, model.DB.Unscoped().Order("id").Find(&all).Error)
			require.Len(t, all, 16)
			for _, code := range all[:15] {
				assert.True(t, code.DeletedAt.Valid)
			}
			assert.False(t, all[15].DeletedAt.Valid)
		})
	}
}

// TestUpdateRedemptionRefusesRedeemedCodes protects the rebate settler's record
// of redemption recharges: a redeemed code (status, quota, redeemed time and
// user) can no longer be edited or re-enabled, including when the redeem
// commits after the admin request read the code.
func TestUpdateRedemptionRefusesRedeemedCodes(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db, _ := openRedemptionTestDB(t, dialect)
			token := "redemption-update-test-token"
			admin := model.User{Username: "redemption-update-admin", Password: "unused", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AccessToken: &token}
			require.NoError(t, db.Create(&admin).Error)
			enabled := model.Redemption{Name: "enabled", Key: fmt.Sprintf("%032d", 1), Quota: 100, Status: common.RedemptionCodeStatusEnabled}
			used := model.Redemption{Name: "used", Key: fmt.Sprintf("%032d", 2), Quota: 200, Status: common.RedemptionCodeStatusUsed, UsedUserId: admin.Id, RedeemedTime: 1700000000}
			raced := model.Redemption{Name: "raced", Key: fmt.Sprintf("%032d", 3), Quota: 300, Status: common.RedemptionCodeStatusEnabled}
			require.NoError(t, db.Create(&[]*model.Redemption{&enabled, &used, &raced}).Error)
			router := gin.New()
			router.Use(middleware.RequestId())
			router.PUT("/api/redemption/", middleware.AdminAuth(), UpdateRedemption)
			update := func(query string, body map[string]any) (bool, string) {
				payload, err := common.Marshal(body)
				require.NoError(t, err)
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPut, "/api/redemption/"+query, bytes.NewReader(payload))
				request.Header.Set("Authorization", "Bearer "+token)
				router.ServeHTTP(response, request)
				require.Equal(t, http.StatusOK, response.Code)
				var result struct {
					Success    bool   `json:"success"`
					MessageKey string `json:"message_key"`
				}
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
				return result.Success, result.MessageKey
			}
			reload := func(id int) model.Redemption {
				var row model.Redemption
				require.NoError(t, db.First(&row, id).Error)
				return row
			}

			for _, tc := range []struct {
				name    string
				query   string
				body    map[string]any
				wantKey string
			}{
				{name: "edit", body: map[string]any{"id": used.Id, "name": "edited", "quota": 900, "expired_time": 0}, wantKey: "This redemption code has already been used"},
				{name: "re-enable", query: "?status_only=true", body: map[string]any{"id": used.Id, "status": common.RedemptionCodeStatusEnabled}, wantKey: "This redemption code has already been used"},
				{name: "disable", query: "?status_only=true", body: map[string]any{"id": used.Id, "status": common.RedemptionCodeStatusDisabled}, wantKey: "This redemption code has already been used"},
			} {
				t.Run("redeemed code "+tc.name, func(t *testing.T) {
					ok, key := update(tc.query, tc.body)
					assert.False(t, ok)
					assert.Equal(t, tc.wantKey, key)
					assert.Equal(t, used, reload(used.Id))
				})
			}

			t.Run("status only update cannot mark a code redeemed", func(t *testing.T) {
				ok, key := update("?status_only=true", map[string]any{"id": enabled.Id, "status": common.RedemptionCodeStatusUsed})
				assert.False(t, ok)
				assert.Equal(t, "Invalid parameters", key)
				assert.Equal(t, enabled, reload(enabled.Id))
			})

			t.Run("enabled code stays editable", func(t *testing.T) {
				ok, _ := update("", map[string]any{"id": enabled.Id, "name": "edited", "quota": 150, "expired_time": 0})
				require.True(t, ok)
				// An update that changes nothing reports zero affected rows on MySQL.
				ok, _ = update("", map[string]any{"id": enabled.Id, "name": "edited", "quota": 150, "expired_time": 0})
				require.True(t, ok)
				ok, _ = update("?status_only=true", map[string]any{"id": enabled.Id, "status": common.RedemptionCodeStatusDisabled})
				require.True(t, ok)
				row := reload(enabled.Id)
				assert.Equal(t, "edited", row.Name)
				assert.Equal(t, 150, row.Quota)
				assert.Equal(t, common.RedemptionCodeStatusDisabled, row.Status)
			})

			t.Run("redeem between the read and the write wins", func(t *testing.T) {
				stale, err := model.GetRedemptionById(raced.Id)
				require.NoError(t, err)
				// The same compare-and-swap model.Redeem commits.
				require.NoError(t, db.Model(&model.Redemption{}).Where("id = ? AND status = ?", raced.Id, common.RedemptionCodeStatusEnabled).
					Updates(map[string]any{"status": common.RedemptionCodeStatusUsed, "used_user_id": admin.Id, "redeemed_time": 1700000001}).Error)
				stale.Quota = 999
				require.ErrorIs(t, stale.Update(), model.ErrRedeemCodeUsed)
				row := reload(raced.Id)
				assert.Equal(t, common.RedemptionCodeStatusUsed, row.Status)
				assert.Equal(t, 300, row.Quota)
				assert.Equal(t, admin.Id, row.UsedUserId)
				assert.EqualValues(t, 1700000001, row.RedeemedTime)
			})
		})
	}
}
