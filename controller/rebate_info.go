package controller

// Fork-only file (ikun): public read-only endpoint exposing the invite-rebate
// switch and rate to the wallet card. The rebate itself is settled by an
// external job (ikun_rebate); this endpoint only mirrors its configuration.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

const ikunRebateConfigKey = "IkunRebateConfig"

const ikunRebateCacheTTL = 60 * time.Second

// ikunRebateConfig mirrors every field parse_business_config (ikun_rebate
// config.py) validates. Wrong JSON types fail Unmarshal, which is the same
// fail-closed outcome as the settler's type checks. Keep in sync with the
// settler when its schema changes.
type ikunRebateConfig struct {
	Enabled                       bool              `json:"enabled"`
	Rate                          *float64          `json:"rate"`
	Timezone                      string            `json:"timezone"`
	ActivityStart                 string            `json:"activity_start"`
	ProviderQuotaRules            map[string]string `json:"provider_quota_rules"`
	ExcludedInviterIds            []int64           `json:"excluded_inviter_ids"`
	ExcludedInviteeIds            []int64           `json:"excluded_invitee_ids"`
	IncludeRedemption             *bool             `json:"include_redemption"`
	DisplayInCurrency             *bool             `json:"display_in_currency"`
	DefaultLanguage               *string           `json:"default_language"`
	RedemptionExcludeNamePatterns []string          `json:"redemption_exclude_name_patterns"`
}

// ikunRebateConfigKeys are the fields the settler validates. An explicit JSON
// null on any of them is a ConfigError on the settler side, while Go's json
// decodes null into nil/zero values indistinguishable from an absent field —
// so null is rejected by inspecting the raw message.
var ikunRebateConfigKeys = []string{
	"enabled", "rate", "timezone", "activity_start", "provider_quota_rules",
	"excluded_inviter_ids", "excluded_invitee_ids", "include_redemption",
	"display_in_currency", "default_language", "redemption_exclude_name_patterns",
}

var ikunRebateValidProviderRules = map[string]bool{
	"amount": true, "money": true, "amount_raw": true,
}

var ikunRebateSupportedLanguages = map[string]bool{
	"en": true, "zh": true, "zh-TW": true, "fr": true, "ru": true, "ja": true, "vi": true,
}

// rebateRateIfUsable replicates the settlement job's fail-closed config
// validation: the endpoint must never answer enabled=true for a config the
// settler would refuse. activity_start is checked as RFC3339 (an offset is
// mandatory, matching the settler); the settler accepts a few more ISO-8601
// variants — a mismatch there only hides the promo, never advertises a dead
// activity. "Local" is rejected because Go would resolve it while the
// settler's ZoneInfo would not.
func rebateRateIfUsable(raw string) (float64, bool) {
	var cfg ikunRebateConfig
	if raw == "" || common.UnmarshalJsonStr(raw, &cfg) != nil || !cfg.Enabled {
		return 0, false
	}
	var fields map[string]json.RawMessage
	if common.UnmarshalJsonStr(raw, &fields) != nil {
		return 0, false
	}
	for _, key := range ikunRebateConfigKeys {
		if v, ok := fields[key]; ok && string(bytes.TrimSpace(v)) == "null" {
			return 0, false
		}
	}
	if cfg.Rate == nil || *cfg.Rate <= 0 || *cfg.Rate >= 1 {
		return 0, false
	}
	if cfg.Timezone == "" || cfg.Timezone == "Local" {
		return 0, false
	}
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return 0, false
	}
	if _, err := time.Parse(time.RFC3339, cfg.ActivityStart); err != nil {
		return 0, false
	}
	if len(cfg.ProviderQuotaRules) == 0 {
		return 0, false
	}
	for _, rule := range cfg.ProviderQuotaRules {
		if !ikunRebateValidProviderRules[rule] {
			return 0, false
		}
	}
	if cfg.DefaultLanguage != nil && !ikunRebateSupportedLanguages[*cfg.DefaultLanguage] {
		return 0, false
	}
	return *cfg.Rate, true
}

var ikunRebateCache struct {
	sync.Mutex
	fetchedAt time.Time
	enabled   bool
	rate      float64
}

// GetRebateInfo answers {"enabled":true,"rate":0.05} when the rebate activity
// is on. It reads the option row from the database (60s cache) instead of the
// in-memory OptionMap: the option sync loop never removes deleted rows, so a
// deleted config row would otherwise keep advertising a stopped activity until
// restart. Any failure — missing row, DB error, invalid config — answers
// {"enabled":false} with no rate.
func GetRebateInfo(c *gin.Context) {
	ikunRebateCache.Lock()
	if time.Since(ikunRebateCache.fetchedAt) > ikunRebateCacheTTL {
		raw := ""
		var opt model.Option
		if err := model.DB.Where(model.Option{Key: ikunRebateConfigKey}).First(&opt).Error; err == nil {
			raw = opt.Value
		}
		ikunRebateCache.rate, ikunRebateCache.enabled = rebateRateIfUsable(raw)
		ikunRebateCache.fetchedAt = time.Now()
	}
	enabled, rate := ikunRebateCache.enabled, ikunRebateCache.rate
	ikunRebateCache.Unlock()

	if !enabled {
		c.JSON(http.StatusOK, gin.H{"enabled": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": true, "rate": rate})
}
