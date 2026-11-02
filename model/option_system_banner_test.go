package model

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type bannerStep struct {
	key, value string
	now        int64
}

func useSystemBannerFixture(t *testing.T) *gorm.DB {
	t.Helper()
	db := useFrontendOptionMigrationDB(t)
	settings := system_setting.GetSystemBannerSettings()
	previousSettings := *settings
	previousNow := systemBannerNow
	common.OptionMapRWMutex.Lock()
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	*settings = system_setting.SystemBannerSettings{Level: system_setting.SystemBannerLevelNormal}
	t.Cleanup(func() {
		*settings = previousSettings
		systemBannerNow = previousNow
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
	})
	return db
}

func applyBannerStep(t *testing.T, step bannerStep) {
	t.Helper()
	systemBannerNow = func() time.Time { return time.Unix(step.now, 0) }
	require.NoError(t, UpdateOption(step.key, step.value), step.key)
}

func TestSystemBannerRejectsInvalidValues(t *testing.T) {
	enabledSetup := []bannerStep{
		{"system_banner.text_zh", "维护", 1000},
		{"system_banner.text_en", "Maintenance", 1000},
		{"system_banner.enabled", "true", 1000},
	}
	tests := []struct {
		name  string
		setup []bannerStep
		run   func() error
	}{
		{"bad level", nil, func() error { return UpdateOption("system_banner.level", "urgent") }},
		{"text too long", nil, func() error {
			return UpdateOption("system_banner.text_zh", strings.Repeat("维", 501))
		}},
		{"bad enabled", nil, func() error { return UpdateOption("system_banner.enabled", "yes") }},
		{"published_at via single", nil, func() error { return UpdateOption("system_banner.published_at", "123") }},
		{"published_at via bulk", nil, func() error {
			return UpdateOptionsBulk(map[string]string{"system_banner.published_at": "123"})
		}},
		{"text via bulk", nil, func() error {
			return UpdateOptionsBulk(map[string]string{"system_banner.text_zh": "维护"})
		}},
		{"enable without text_en", []bannerStep{{"system_banner.text_zh", "维护", 1000}}, func() error {
			return UpdateOption("system_banner.enabled", "true")
		}},
		{"blank text while enabled", enabledSetup, func() error {
			return UpdateOption("system_banner.text_zh", "   ")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := useSystemBannerFixture(t)
			for _, step := range tt.setup {
				applyBannerStep(t, step)
			}
			var before []Option
			require.NoError(t, db.Order("`key`").Find(&before).Error)
			assert.Error(t, tt.run())
			var after []Option
			require.NoError(t, db.Order("`key`").Find(&after).Error)
			assert.Equal(t, before, after)
		})
	}
}

func TestSystemBannerPublishedAtRules(t *testing.T) {
	zh := bannerStep{"system_banner.text_zh", "维护", 1000}
	en := bannerStep{"system_banner.text_en", "Maintenance", 1000}
	enable := func(now int64) bannerStep { return bannerStep{"system_banner.enabled", "true", now} }
	disable := func(now int64) bannerStep { return bannerStep{"system_banner.enabled", "false", now} }
	tests := []struct {
		name  string
		steps []bannerStep
		want  int64
	}{
		{"enable bumps", []bannerStep{zh, en, enable(1000)}, 1000},
		{"re-enable with same text bumps", []bannerStep{zh, en, enable(1000), disable(1100), enable(1200)}, 1200},
		{"disable keeps value", []bannerStep{zh, en, enable(1000), disable(1100)}, 1000},
		{"text change while enabled bumps", []bannerStep{zh, en, enable(1000), {"system_banner.text_en", "New", 1300}}, 1300},
		{"same text while enabled does not bump", []bannerStep{zh, en, enable(1000), {"system_banner.text_zh", "维护", 1300}}, 1000},
		{"changing level does not bump", []bannerStep{zh, en, enable(1000), {"system_banner.level", "important", 1300}}, 1000},
		{"text change while disabled does not bump", []bannerStep{zh, en, enable(1000), disable(1100), {"system_banner.text_zh", "新维护", 1300}}, 1000},
		{"same second publish still increments", []bannerStep{zh, en, enable(1000), disable(1000), enable(1000)}, 1001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := useSystemBannerFixture(t)
			for _, step := range tt.steps {
				applyBannerStep(t, step)
			}
			assert.Equal(t, strconv.FormatInt(tt.want, 10), requireOptionValue(t, db, "system_banner.published_at"))
			assert.Equal(t, tt.want, system_setting.GetSystemBannerSettings().PublishedAt)
		})
	}

	t.Run("text edit before first enable leaves published_at unset", func(t *testing.T) {
		db := useSystemBannerFixture(t)
		applyBannerStep(t, zh)
		requireOptionMissing(t, db, "system_banner.published_at")
		assert.Equal(t, int64(0), system_setting.GetSystemBannerSettings().PublishedAt)
	})
}

func TestSystemBannerTrimsText(t *testing.T) {
	db := useSystemBannerFixture(t)
	applyBannerStep(t, bannerStep{"system_banner.text_zh", "  维护  ", 1000})
	assert.Equal(t, "维护", requireOptionValue(t, db, "system_banner.text_zh"))
	assert.Equal(t, "维护", system_setting.GetSystemBannerSettings().TextZh)
}

func TestSystemBannerKeysAppearInOptionMap(t *testing.T) {
	useSystemBannerFixture(t)
	InitOptionMap()
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, "normal", common.OptionMap["system_banner.level"])
	assert.Equal(t, "false", common.OptionMap["system_banner.enabled"])
}

func saveBanner(t *testing.T, now int64, input SystemBannerInput) error {
	t.Helper()
	systemBannerNow = func() time.Time { return time.Unix(now, 0) }
	_, err := UpdateSystemBanner(input)
	return err
}

func TestUpdateSystemBannerSavesTheWholeBannerAtOnce(t *testing.T) {
	db := useSystemBannerFixture(t)

	saved, err := UpdateSystemBanner(SystemBannerInput{Enabled: true, Level: "important", TextZh: "  维护  ", TextEn: "Maintenance"})
	require.NoError(t, err)

	assert.Equal(t, "true", requireOptionValue(t, db, "system_banner.enabled"))
	assert.Equal(t, "important", requireOptionValue(t, db, "system_banner.level"))
	assert.Equal(t, "维护", requireOptionValue(t, db, "system_banner.text_zh"))
	assert.Equal(t, "Maintenance", requireOptionValue(t, db, "system_banner.text_en"))
	assert.Equal(t, *system_setting.GetSystemBannerSettings(), saved)
	assert.Equal(t, requireOptionValue(t, db, "system_banner.published_at"), strconv.FormatInt(saved.PublishedAt, 10))
}

func TestUpdateSystemBannerPublishRules(t *testing.T) {
	on := SystemBannerInput{Enabled: true, Level: "normal", TextZh: "维护", TextEn: "Maintenance"}
	off := on
	off.Enabled = false
	important := on
	important.Level = "important"
	newText := on
	newText.TextEn = "New"
	cleared := SystemBannerInput{Level: "normal"}
	type save struct {
		now   int64
		input SystemBannerInput
	}
	tests := []struct {
		name  string
		saves []save
		want  int64
	}{
		{"enable publishes", []save{{1000, on}}, 1000},
		{"saving the same banner again does not republish", []save{{1000, on}, {1300, on}}, 1000},
		{"changing only the level does not republish", []save{{1000, on}, {1300, important}}, 1000},
		{"changing text while enabled republishes", []save{{1000, on}, {1300, newText}}, 1300},
		{"re-enabling the same text republishes", []save{{1000, on}, {1100, off}, {1200, on}}, 1200},
		{"disabling and clearing in one save keeps the old publish", []save{{1000, on}, {1100, cleared}}, 1000},
		{"same second publish still increments", []save{{1000, on}, {1000, off}, {1000, on}}, 1001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := useSystemBannerFixture(t)
			for _, step := range tt.saves {
				require.NoError(t, saveBanner(t, step.now, step.input))
			}
			assert.Equal(t, strconv.FormatInt(tt.want, 10), requireOptionValue(t, db, "system_banner.published_at"))
			assert.Equal(t, tt.want, system_setting.GetSystemBannerSettings().PublishedAt)
		})
	}
}

func TestUpdateSystemBannerRejectsInvalidBannersWithoutWriting(t *testing.T) {
	tests := []struct {
		name  string
		input SystemBannerInput
	}{
		{"enabled without English", SystemBannerInput{Enabled: true, Level: "normal", TextZh: "维护", TextEn: "  "}},
		{"enabled without Chinese", SystemBannerInput{Enabled: true, Level: "normal", TextEn: "Maintenance"}},
		{"unknown level", SystemBannerInput{Level: "urgent", TextZh: "维护", TextEn: "Maintenance"}},
		{"text too long", SystemBannerInput{Level: "normal", TextZh: strings.Repeat("维", 501), TextEn: "Maintenance"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := useSystemBannerFixture(t)
			_, err := UpdateSystemBanner(tt.input)
			assert.Error(t, err)
			var count int64
			require.NoError(t, db.Model(&Option{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

// Another instance may have saved banner keys that this instance's memory has
// not synced yet. Decisions must follow the database, not local memory. The
// read takes row locks, so it runs against every supported database.
func TestSystemBannerDecidesFromDatabaseNotStaleMemory(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "banner_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			var version string
			versionSQL := "SELECT version()"
			if dialect == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
			t.Logf("database version: %s", version)
			require.NoError(t, db.Migrator().DropTable(&Option{}))
			require.NoError(t, db.AutoMigrate(&Option{}))

			useSystemBannerFixture(t)
			previousDB, previousType := DB, common.MainDatabaseType()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			t.Cleanup(func() {
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				assert.NoError(t, db.Migrator().DropTable(&Option{}))
				assert.NoError(t, sqlDB.Close())
			})

			require.NoError(t, db.Create(&[]Option{
				{Key: "system_banner.text_zh", Value: "维护"},
				{Key: "system_banner.text_en", Value: "Maintenance"},
				{Key: "system_banner.published_at", Value: "2000"},
			}).Error)

			applyBannerStep(t, bannerStep{"system_banner.enabled", "true", 1000})

			assert.Equal(t, "true", requireOptionValue(t, db, "system_banner.enabled"))
			assert.Equal(t, "2001", requireOptionValue(t, db, "system_banner.published_at"))
			settings := system_setting.GetSystemBannerSettings()
			assert.True(t, settings.Enabled)
			assert.Equal(t, "维护", settings.TextZh)
			assert.Equal(t, "Maintenance", settings.TextEn)
			assert.Equal(t, int64(2001), settings.PublishedAt)

			// Another instance changes the English text behind this one's back;
			// a whole-banner save must compare against that, and republish.
			require.NoError(t, db.Model(&Option{}).Where(&Option{Key: "system_banner.text_en"}).Update("value", "Changed").Error)
			require.NoError(t, saveBanner(t, 1000, SystemBannerInput{Enabled: true, Level: "normal", TextZh: "维护", TextEn: "Maintenance"}))
			assert.Equal(t, "Maintenance", requireOptionValue(t, db, "system_banner.text_en"))
			assert.Equal(t, "2002", requireOptionValue(t, db, "system_banner.published_at"))
		})
	}
}
