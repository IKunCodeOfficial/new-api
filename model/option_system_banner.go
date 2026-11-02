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
	"gorm.io/gorm/clause"
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

// systemBannerStoredKeys lists every banner option row, published_at first so
// memory learns a new publish time before it learns the banner is enabled.
var systemBannerStoredKeys = []string{
	systemBannerPublishedAtKey,
	systemBannerLevelKey,
	systemBannerTextZhKey,
	systemBannerTextEnKey,
	systemBannerEnabledKey,
}

// SystemBannerInput is a whole-banner save from the admin form.
type SystemBannerInput struct {
	Enabled bool
	Level   string
	TextZh  string
	TextEn  string
}

// UpdateSystemBanner validates and stores the whole banner in one transaction
// and returns the saved banner, including its publish time.
func UpdateSystemBanner(input SystemBannerInput) (system_setting.SystemBannerSettings, error) {
	return saveSystemBanner(func(next *system_setting.SystemBannerSettings) {
		next.Enabled = input.Enabled
		next.Level = input.Level
		next.TextZh = input.TextZh
		next.TextEn = input.TextEn
	})
}

// updateSystemBannerOption stores one banner key sent through the generic
// option API.
func updateSystemBannerOption(key, value string) error {
	if key == systemBannerEnabledKey && value != "true" && value != "false" {
		return common.NewMessage("System banner enabled must be true or false")
	}
	_, err := saveSystemBanner(func(next *system_setting.SystemBannerSettings) {
		switch key {
		case systemBannerEnabledKey:
			next.Enabled = value == "true"
		case systemBannerLevelKey:
			next.Level = value
		case systemBannerTextZhKey:
			next.TextZh = value
		case systemBannerTextEnKey:
			next.TextEn = value
		}
	})
	return err
}

// saveSystemBanner applies an edit to the stored banner. It also maintains
// published_at: every publish (enable, or a text change while enabled) gets a
// strictly larger value than the previous one, so a visitor's dismissal of an
// older publish never hides a newer one.
//
// The current banner is read from the database inside the transaction, not
// from memory: with several instances, the one handling this request may not
// have synced keys another instance just saved.
func saveSystemBanner(edit func(next *system_setting.SystemBannerSettings)) (system_setting.SystemBannerSettings, error) {
	systemBannerMutex.Lock()
	defer systemBannerMutex.Unlock()

	var next system_setting.SystemBannerSettings
	err := DB.Transaction(func(tx *gorm.DB) error {
		// Make sure every banner row exists before locking: a row lock cannot
		// cover a row that is not there yet, so two first-time saves could
		// otherwise both read "disabled" and break the both-texts rule.
		seed := make([]Option, 0, len(systemBannerStoredKeys))
		for k, v := range systemBannerOptionValues(system_setting.SystemBannerSettings{Level: system_setting.SystemBannerLevelNormal}) {
			seed = append(seed, Option{Key: k, Value: v})
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
			return err
		}
		var rows []Option
		if err := lockForUpdate(tx).Where(map[string]any{"key": systemBannerStoredKeys}).Find(&rows).Error; err != nil {
			return err
		}
		stored := make(map[string]string, len(rows))
		cur := system_setting.SystemBannerSettings{Level: system_setting.SystemBannerLevelNormal}
		for _, row := range rows {
			stored[row.Key] = row.Value
			switch row.Key {
			case systemBannerEnabledKey:
				cur.Enabled = row.Value == "true"
			case systemBannerLevelKey:
				if row.Value != "" {
					cur.Level = row.Value
				}
			case systemBannerTextZhKey:
				cur.TextZh = row.Value
			case systemBannerTextEnKey:
				cur.TextEn = row.Value
			case systemBannerPublishedAtKey:
				cur.PublishedAt, _ = strconv.ParseInt(row.Value, 10, 64)
			}
		}

		next = cur
		edit(&next)
		next.TextZh = strings.TrimSpace(next.TextZh)
		next.TextEn = strings.TrimSpace(next.TextEn)
		if next.Level != system_setting.SystemBannerLevelNormal && next.Level != system_setting.SystemBannerLevelImportant {
			return common.NewMessage("System banner level must be normal or important")
		}
		if utf8.RuneCountInString(next.TextZh) > system_setting.SystemBannerTextMaxRunes ||
			utf8.RuneCountInString(next.TextEn) > system_setting.SystemBannerTextMaxRunes {
			return common.NewMessage("System banner text must be at most 500 characters")
		}
		if next.Enabled && (next.TextZh == "" || next.TextEn == "") {
			return common.NewMessage("System banner needs both Chinese and English text while enabled")
		}

		textChanged := next.TextZh != cur.TextZh || next.TextEn != cur.TextEn
		if (!cur.Enabled && next.Enabled) || (next.Enabled && textChanged) {
			next.PublishedAt = max(systemBannerNow().Unix(), cur.PublishedAt+1)
		}
		// Compare with the raw stored values so a non-canonical row (for
		// example an empty level) is rewritten to what memory will hold.
		for k, v := range systemBannerOptionValues(next) {
			if v == stored[k] {
				continue
			}
			if err := tx.Model(&Option{}).Where(&Option{Key: k}).Update("value", v).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return system_setting.SystemBannerSettings{}, err
	}

	// Bring this instance's memory up to the committed banner, including keys
	// another instance saved, in systemBannerStoredKeys order.
	values := systemBannerOptionValues(next)
	for _, k := range systemBannerStoredKeys {
		if err := updateOptionMap(k, values[k]); err != nil {
			return system_setting.SystemBannerSettings{}, err
		}
	}
	return next, nil
}

func systemBannerOptionValues(banner system_setting.SystemBannerSettings) map[string]string {
	return map[string]string{
		systemBannerPublishedAtKey: strconv.FormatInt(banner.PublishedAt, 10),
		systemBannerLevelKey:       banner.Level,
		systemBannerTextZhKey:      banner.TextZh,
		systemBannerTextEnKey:      banner.TextEn,
		systemBannerEnabledKey:     strconv.FormatBool(banner.Enabled),
	}
}
