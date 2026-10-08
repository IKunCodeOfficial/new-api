package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRelayDoesNotWriteErrorAfterClientDisconnect(t *testing.T) {
	for _, disconnected := range []bool{false, true} {
		name := "connected"
		if disconnected {
			name = "disconnected"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if disconnected {
				cancel()
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":`)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			Relay(c, types.RelayFormatOpenAI)
			if disconnected {
				assert.Empty(t, recorder.Body.String())
				assert.False(t, c.Writer.Written())
			} else {
				assert.Equal(t, http.StatusBadRequest, recorder.Code)
				assert.Contains(t, recorder.Body.String(), "error")
			}
		})
	}
}
