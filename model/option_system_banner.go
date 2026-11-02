package model

import (
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"gorm.io/gorm"
)

const (
	systemBannerEnabledKey     = "system_banner.enabled"
	systemBannerLevelKey       = "system_banner.level"
	systemBannerTextZhKey      = "system_banner.text_zh"
	systemBannerTextEnKey      = "system_banner.text_en"
	systemBannerPublishedAtKey = "system_banner.published_at"
)

// systemBannerNow is the publish clock; tests override it.
var systemBannerNow = time.Now

var systemBannerMutex sync.Mutex

// IsSystemBannerOption reports whether key is one of the four editable banner keys.
func IsSystemBannerOption(key string) bool {
	switch key {
	case systemBannerEnabledKey, systemBannerLevelKey, systemBannerTextZhKey, systemBannerTextEnKey:
		return true
	}
	return false
}

// updateSystemBannerOption validates and stores one banner key. It also
// maintains published_at: every publish (enable, or a text change while
// enabled) gets a strictly larger value than the previous one, so a visitor's
// dismissal of an older publish never hides a newer one.
func updateSystemBannerOption(key, value string) error {
	systemBannerMutex.Lock()
	defer systemBannerMutex.Unlock()

	cur := *system_setting.GetSystemBannerSettings()
	next := cur
	isText := key == systemBannerTextZhKey || key == systemBannerTextEnKey
	if isText {
		value = strings.TrimSpace(value)
		if utf8.RuneCountInString(value) > system_setting.SystemBannerTextMaxRunes {
			return common.NewMessage("System banner text must be at most 500 characters")
		}
	}
	switch key {
	case systemBannerEnabledKey:
		if value != "true" && value != "false" {
			return common.NewMessage("System banner enabled must be true or false")
		}
		next.Enabled = value == "true"
	case systemBannerLevelKey:
		if value != system_setting.SystemBannerLevelNormal && value != system_setting.SystemBannerLevelImportant {
			return common.NewMessage("System banner level must be normal or important")
		}
		next.Level = value
	case systemBannerTextZhKey:
		next.TextZh = value
	case systemBannerTextEnKey:
		next.TextEn = value
	}
	if next.Enabled && (next.TextZh == "" || next.TextEn == "") {
		return common.NewMessage("System banner needs both Chinese and English text while enabled")
	}

	textChanged := (key == systemBannerTextZhKey && value != cur.TextZh) ||
		(key == systemBannerTextEnKey && value != cur.TextEn)
	writes := map[string]string{key: value}
	if (!cur.Enabled && next.Enabled) || (next.Enabled && textChanged) {
		publishedAt := max(systemBannerNow().Unix(), cur.PublishedAt+1)
		writes[systemBannerPublishedAtKey] = strconv.FormatInt(publishedAt, 10)
	}

	err := DB.Transaction(func(tx *gorm.DB) error {
		for k, v := range writes {
			option := Option{Key: k}
			if err := tx.FirstOrCreate(&option, Option{Key: k}).Error; err != nil {
				return err
			}
			option.Value = v
			if err := tx.Save(&option).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for k, v := range writes {
		if err := updateOptionMap(k, v); err != nil {
			return err
		}
	}
	return nil
}
