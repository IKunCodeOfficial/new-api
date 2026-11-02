package system_setting

import "github.com/QuantumNous/new-api/setting/config"

const (
	SystemBannerLevelNormal    = "normal"
	SystemBannerLevelImportant = "important"
	SystemBannerTextMaxRunes   = 500
)

// SystemBannerSettings is the site-wide banner shown above every page.
// PublishedAt (Unix seconds) is maintained by the model layer, never by callers.
type SystemBannerSettings struct {
	Enabled     bool   `json:"enabled"`
	Level       string `json:"level"`
	TextZh      string `json:"text_zh"`
	TextEn      string `json:"text_en"`
	PublishedAt int64  `json:"published_at"`
}

var defaultSystemBannerSettings = SystemBannerSettings{
	Level: SystemBannerLevelNormal,
}

func init() {
	config.GlobalConfig.Register("system_banner", &defaultSystemBannerSettings)
}

func GetSystemBannerSettings() *SystemBannerSettings {
	return &defaultSystemBannerSettings
}
