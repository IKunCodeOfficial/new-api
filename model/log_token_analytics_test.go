package model

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/clickhouse"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestTokenAnalyticsSeparateStores(t *testing.T) {
	cases := []struct {
		name, mainEnv, logEnv string
		mainType, logType     common.DatabaseType
	}{
		{"sqlite", "", "", common.DatabaseTypeSQLite, common.DatabaseTypeSQLite},
		{"mysql", "ANALYTICS_MYSQL_DSN", "ANALYTICS_MYSQL_LOG_DSN", common.DatabaseTypeMySQL, common.DatabaseTypeMySQL},
		{"postgres", "ANALYTICS_POSTGRES_DSN", "ANALYTICS_POSTGRES_LOG_DSN", common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL},
		{"clickhouse", "", "ANALYTICS_CLICKHOUSE_DSN", common.DatabaseTypeSQLite, common.DatabaseTypeClickHouse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.logEnv != "" && os.Getenv(tc.logEnv) == "" {
				t.Skip(tc.logEnv + " not configured")
			}
			oldDB, oldLog := DB, LOG_DB
			oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
			open := func(typ common.DatabaseType, env, suffix string) *gorm.DB {
				var driver gorm.Dialector
				switch typ {
				case common.DatabaseTypeMySQL:
					driver = mysql.Open(os.Getenv(env))
				case common.DatabaseTypePostgreSQL:
					driver = postgres.Open(os.Getenv(env))
				case common.DatabaseTypeClickHouse:
					driver = clickhouse.Open(os.Getenv(env))
				default:
					driver = sqlite.Open(fmt.Sprintf("file:%s%s?mode=memory&cache=shared", tc.name, suffix))
				}
				db, err := gorm.Open(driver, &gorm.Config{})
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
				return db
			}
			DB, LOG_DB = open(tc.mainType, tc.mainEnv, "main"), open(tc.logType, tc.logEnv, "logs")
			common.SetDatabaseTypes(tc.mainType, tc.logType)
			initCol()
			t.Cleanup(func() { DB, LOG_DB = oldDB, oldLog; common.SetDatabaseTypes(oldMainType, oldLogType); initCol() })
			require.NoError(t, DB.Migrator().DropTable(&Token{}))
			require.NoError(t, DB.AutoMigrate(&Token{}))
			require.NoError(t, LOG_DB.Migrator().DropTable(&Log{}))
			if tc.logType == common.DatabaseTypeClickHouse {
				require.NoError(t, LOG_DB.Exec(clickHouseLogCreateTableSQL(0)).Error)
			} else {
				require.NoError(t, LOG_DB.AutoMigrate(&Log{}))
			}
			versionSQL := "SELECT version()"
			if tc.logType == common.DatabaseTypeSQLite {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, LOG_DB.Raw(versionSQL).Scan(&version).Error)
			t.Logf("log database version: %s", version)
			tokens := []Token{{Id: 11, UserId: 42, Key: "analytics11", Name: "alpha", CreatedTime: 100}, {Id: 12, UserId: 42, Key: "analytics12", Name: "literal%_!", CreatedTime: 200}, {Id: 13, UserId: 42, Key: "analytics13", Name: "unused", CreatedTime: 300}, {Id: 14, UserId: 7, Key: "analytics14", Name: "foreign", CreatedTime: 400}}
			require.NoError(t, DB.Create(&tokens).Error)
			const start int64 = 1704067200
			rows := []map[string]any{
				{"id": 1, "user_id": 42, "created_at": start + 10, "type": LogTypeConsume, "token_id": 11, "token_name": "old-alpha", "quota": 20, "prompt_tokens": 3, "completion_tokens": 2},
				{"id": 2, "user_id": 42, "created_at": start + 20, "type": LogTypeConsume, "token_id": 12, "token_name": "old-literal", "quota": 30, "prompt_tokens": 4, "completion_tokens": 3},
				{"id": 3, "user_id": 42, "created_at": start + 3600, "type": LogTypeConsume, "token_id": 11, "token_name": "old-alpha", "quota": 20, "prompt_tokens": 5, "completion_tokens": 4},
				{"id": 4, "user_id": 7, "created_at": start + 10, "type": LogTypeConsume, "token_id": 11, "token_name": "foreign", "quota": 1000, "prompt_tokens": 100, "completion_tokens": 100},
				{"id": 5, "user_id": 42, "created_at": start - 1, "type": LogTypeConsume, "token_id": 11, "token_name": "old-alpha", "quota": 1000, "prompt_tokens": 100, "completion_tokens": 100},
				{"id": 6, "user_id": 42, "created_at": start + 20, "type": LogTypeError, "token_id": 11, "token_name": "old-alpha", "quota": 1000, "prompt_tokens": 100, "completion_tokens": 100},
				{"id": 7, "user_id": 42, "created_at": start + 20, "type": LogTypeConsume, "token_id": 99, "token_name": "deleted", "quota": 5, "prompt_tokens": 1, "completion_tokens": 1},
			}
			for _, row := range rows {
				require.NoError(t, LOG_DB.Table("logs").Create(row).Error)
			}
			t.Run("target audit and legacy export", func(t *testing.T) {
				require.NoError(t, LOG_DB.Migrator().DropTable(&AuditLog{}))
				for range 2 {
					require.NoError(t, MigrateAuditLogs())
				}
				for _, target := range []int{42, 420} {
					RecordAuditLog(nil, AuditLog{EventId: fmt.Sprint(target), UserId: 1, Username: "operator", ActorRole: common.RoleAdminUser, CreatedAt: start, Category: AuditCategoryOperation, Other: AuditOther{Op: &AuditOperation{Action: "user.quota_add", Params: AuditFields{"target_user_id": target}}}})
				}
				audits, total, err := GetAuditLogs(AuditLogFilter{TargetUserId: 42}, 0, 10, common.RoleAdminUser)
				require.NoError(t, err)
				assert.Equal(t, int64(1), total)
				require.Len(t, audits, 1)
				assert.Equal(t, "42", audits[0].EventId)
				_, _, err = GetAuditLogs(AuditLogFilter{TargetUserId: 42, SelfView: true}, 0, 10, common.RoleAdminUser)
				require.Error(t, err)
				for i, target := range []int{42, 420} {
					require.NoError(t, createLog(&Log{Id: 100 + i, UserId: 1, Username: "operator", CreatedAt: start, Type: LogTypeManage, Ip: "192.0.2.1", Content: fmt.Sprint(target), Other: fmt.Sprintf(`{"admin_info":{"admin_id":1},"op":{"params":{"target_user_id":%d,"username":"target"}}}`, target)}))
				}
				filter := LogFilter{Type: LogTypeManage, TargetUserId: 42}
				logs, total, err := GetAllLogs(filter, 0, 10)
				require.NoError(t, err)
				assert.Equal(t, int64(1), total)
				require.Len(t, logs, 1)
				assert.Equal(t, "42", logs[0].Content)
				var exported []*Log
				require.NoError(t, StreamAllLogs(context.Background(), filter, common.RoleAdminUser, func(log *Log) error { exported = append(exported, log); return nil }))
				require.Len(t, exported, 1)
				assert.Equal(t, logs[0].Content, exported[0].Content)
				exported = nil
				require.NoError(t, StreamUserLogs(context.Background(), 42, LogFilter{Type: LogTypeManage}, func(log *Log) error { exported = append(exported, log); return nil }))
				require.Len(t, exported, 1)
				assert.Equal(t, 42, exported[0].UserId)
				assert.Empty(t, exported[0].Ip)
				assert.NotContains(t, exported[0].Other, "admin_info")
			})

			t.Run("list", func(t *testing.T) {
				items, total, err := GetUserTokenAnalytics(42, start, start+7200, "", "quota_sum", "desc", 0, 1)
				require.NoError(t, err)
				assert.Equal(t, int64(3), total)
				require.Len(t, items, 1)
				assert.Equal(t, 11, items[0].TokenId)
				assert.Equal(t, int64(40), items[0].QuotaSum)
				assert.Equal(t, int64(8), items[0].PromptTokensSum)
				items, _, err = GetUserTokenAnalytics(42, start, start+7200, "", "quota_sum", "desc", 1, 1)
				require.NoError(t, err)
				require.Len(t, items, 1)
				assert.Equal(t, 12, items[0].TokenId)
				items, _, err = GetUserTokenAnalytics(42, start, start+7200, "", "quota_sum", "asc", 0, 10)
				require.NoError(t, err)
				require.Len(t, items, 3)
				assert.Equal(t, 13, items[0].TokenId)
				assert.Zero(t, items[0].RequestCount)
				items, total, err = GetUserTokenAnalytics(42, start, start+7200, "%_!", "token_id", "asc", 0, 10)
				require.NoError(t, err)
				assert.Equal(t, int64(1), total)
				require.Len(t, items, 1)
				assert.Equal(t, 12, items[0].TokenId)
				items, total, err = GetUserTokenAnalytics(42, start, start+7200, "14", "token_id", "asc", 0, 10)
				require.NoError(t, err)
				assert.Zero(t, total)
				assert.Empty(t, items)
			})
			t.Run("sort order and pagination", func(t *testing.T) {
				for _, tc := range []struct {
					sort string
					asc  []int
				}{
					{"quota_sum", []int{13, 12, 11}}, {"request_count", []int{13, 12, 11}}, {"last_used_at", []int{13, 12, 11}},
					{"prompt_tokens_sum", []int{13, 12, 11}}, {"completion_tokens_sum", []int{13, 12, 11}},
					{"token_created_time", []int{11, 12, 13}}, {"token_name", []int{11, 12, 13}}, {"token_id", []int{11, 12, 13}},
					{"token_status", []int{13, 12, 11}}, {"invalid column", []int{13, 12, 11}},
				} {
					items, _, err := GetUserTokenAnalytics(42, start, start+7200, "", tc.sort, "asc", 0, 10)
					require.NoError(t, err)
					ids := make([]int, len(items))
					for i, item := range items {
						ids[i] = item.TokenId
					}
					assert.Equal(t, tc.asc, ids, tc.sort)
				}
				items, total, err := GetUserTokenAnalytics(42, start, start+7200, "", "quota_sum", "desc", 100, 10)
				require.NoError(t, err)
				assert.Equal(t, int64(3), total)
				assert.Empty(t, items)
			})
			t.Run("trend", func(t *testing.T) {
				points, top, granularity, err := GetUserTokenAnalyticsTrend(42, start, start+7200, "")
				require.NoError(t, err)
				assert.Equal(t, "hour", granularity)
				require.Len(t, points, 2)
				assert.Equal(t, int64(55), points[0].QuotaSum)
				assert.Equal(t, int64(20), points[1].QuotaSum)
				require.Len(t, top, 3)
				assert.Equal(t, "alpha", top[0].TokenName)
				assert.Equal(t, "deleted", top[2].TokenName)
				points, top, granularity, err = GetUserTokenAnalyticsTrend(42, start, start+3*86400, "12")
				require.NoError(t, err)
				assert.Equal(t, "day", granularity)
				require.Len(t, points, 1)
				assert.Equal(t, int64(30), points[0].QuotaSum)
				require.Len(t, top, 1)
				assert.Equal(t, "literal%_!", top[0].TokenName)
			})
		})
	}
}
