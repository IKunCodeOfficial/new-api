package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetStatusReturnsConfiguredTopUpLink(t *testing.T) {
	originalTopUpLink := common.TopUpLink
	common.TopUpLink = "https://billing.example.com/recharge"
	t.Cleanup(func() { common.TopUpLink = originalTopUpLink })

	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)

	GetStatus(context)

	var payload struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	assert.Equal(t, common.TopUpLink, payload.Data["topup_link"])
}

func TestGetSystemBannerReturnsBannerOnlyWhileEnabled(t *testing.T) {
	banner := system_setting.GetSystemBannerSettings()
	original := *banner
	t.Cleanup(func() { *banner = original })

	request := func() map[string]any {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodGet, "/api/system_banner", nil)
		GetSystemBanner(context)
		var payload struct {
			Success bool           `json:"success"`
			Data    map[string]any `json:"data"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
		require.True(t, payload.Success)
		return payload.Data
	}

	*banner = system_setting.SystemBannerSettings{Enabled: false, Level: "normal", TextZh: "维护", TextEn: "Maintenance", PublishedAt: 1000}
	assert.Nil(t, request())

	banner.Enabled = true
	assert.Equal(t, map[string]any{
		"level":        "normal",
		"text_zh":      "维护",
		"text_en":      "Maintenance",
		"published_at": float64(1000),
	}, request())
}
