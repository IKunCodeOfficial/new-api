package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAbortTokenDailyQuotaExceededReturnsClientVisibleLocalizedError(t *testing.T) {
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		language string
		message  string
	}{
		{name: "English", language: "en", message: "Your token has reached today's limit: ＄1.000000."},
		{name: "Simplified Chinese", language: "zh-CN", message: "你的令牌已达到今日限额：＄1.000000。"},
		{name: "Traditional Chinese", language: "zh-TW", message: "你的令牌已達到今日限額：＄1.000000。"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			context.Request.Header.Set("Accept-Language", test.language)
			context.Set(common.RequestIdKey, "test-request-id")

			abortTokenDailyQuotaExceeded(context, int(common.QuotaPerUnit))

			require.Equal(t, http.StatusForbidden, recorder.Code)
			assert.JSONEq(t, `{
				"error": {
					"message": "`+test.message+` (request id: test-request-id)",
					"type": "new_api_error",
					"code": "`+string(types.ErrorCodeInsufficientUserQuota)+`"
				}
			}`, recorder.Body.String())
		})
	}
}
