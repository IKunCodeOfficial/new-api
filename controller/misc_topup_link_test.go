package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
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

func TestUpdateSystemBannerAuditsTheKeyAndRejectsPartialBodies(t *testing.T) {
	user, _ := setupAccessTokenAudit(t)
	banner := system_setting.GetSystemBannerSettings()
	original := *banner
	common.OptionMapRWMutex.Lock()
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		*banner = original
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
	})

	save := func(body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPut, "/api/option/system_banner", strings.NewReader(body))
		context.Set("id", user.Id)
		context.Set("role", common.RoleRootUser)
		UpdateSystemBanner(context)
		return response
	}

	partial := save(`{"level":"normal","text_zh":"维护"}`)
	assert.Equal(t, http.StatusBadRequest, partial.Code)

	saved := save(`{"enabled":true,"level":"important","text_zh":"维护","text_en":"Maintenance"}`)
	require.Equal(t, http.StatusOK, saved.Code)
	var payload struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(saved.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	assert.Equal(t, true, payload.Data["enabled"])

	var audit model.AuditLog
	require.NoError(t, model.LOG_DB.Where("category = ? AND action = ?", model.AuditCategoryOperation, "option.update").First(&audit).Error)
	encoded, err := common.Marshal(audit)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "system_banner")
	assert.NotContains(t, string(encoded), "Maintenance")
}
