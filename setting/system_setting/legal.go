package system_setting

import "github.com/QuantumNous/new-api/setting/config"

type LegalSettings struct {
	UserAgreement string `json:"user_agreement"`
	PrivacyPolicy string `json:"privacy_policy"`
}

var defaultLegalSettings = LegalSettings{
	UserAgreement: "",
	PrivacyPolicy: "",
}

func init() {
	config.GlobalConfig.Register("legal", &defaultLegalSettings)
}

func GetLegalSettings() *LegalSettings {
	return &defaultLegalSettings
}

// ConsentRequired reports whether the operator has published a user agreement
// or privacy policy, i.e. whether new sign-ups must confirm the legal terms.
func (s *LegalSettings) ConsentRequired() bool {
	return s.UserAgreement != "" || s.PrivacyPolicy != ""
}
