package model

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
