package service

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectClientSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		headers map[string]string
		body    string
		want    *ClientSession
	}{
		{
			name:    "claude code session header",
			headers: map[string]string{"User-Agent": "claude-cli/2.1.0 (external, cli)", "X-Claude-Code-Session-Id": "a1b2"},
			body:    `{"metadata":{"user_id":"user_x_account_y_session_other"}}`,
			want:    &ClientSession{Client: "claude_code", SessionID: "a1b2", Source: "header:x-claude-code-session-id"},
		},
		{
			name:    "claude code json metadata user_id",
			headers: map[string]string{"User-Agent": "claude-cli/2.0.0 (external, cli)"},
			body:    `{"metadata":{"user_id":"{\"device_id\":\"d\",\"session_id\":\"s-json\"}"}}`,
			want:    &ClientSession{Client: "claude_code", SessionID: "s-json", Source: "body:metadata.user_id"},
		},
		{
			name:    "claude code legacy metadata user_id",
			headers: map[string]string{"User-Agent": "claude-cli/1.0.0 (external, cli)"},
			body:    `{"metadata":{"user_id":"user_abc_account_def_session_6f1c-uuid"}}`,
			want:    &ClientSession{Client: "claude_code", SessionID: "6f1c-uuid", Source: "body:metadata.user_id"},
		},
		{
			name:    "codex session_id header",
			headers: map[string]string{"Originator": "codex_cli_rs", "session_id": "c-123"},
			body:    `{"prompt_cache_key":"other"}`,
			want:    &ClientSession{Client: "codex", SessionID: "c-123", Source: "header:session_id"},
		},
		{
			name:    "codex prompt_cache_key fallback",
			headers: map[string]string{"User-Agent": "codex_cli_rs/0.50.0"},
			body:    `{"prompt_cache_key":"c-456"}`,
			want:    &ClientSession{Client: "codex", SessionID: "c-456", Source: "body:prompt_cache_key"},
		},
		{
			name:    "unknown client body fields ignored",
			headers: map[string]string{"User-Agent": "python-httpx/0.27"},
			body:    `{"prompt_cache_key":"k","metadata":{"user_id":"user_a_session_b"}}`,
			want:    nil,
		},
		{
			name:    "overlong id truncated",
			headers: map[string]string{"X-Claude-Code-Session-Id": strings.Repeat("x", 200)},
			want:    &ClientSession{Client: "claude_code", SessionID: strings.Repeat("x", 128), Source: "header:x-claude-code-session-id"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(tt.body))
			for k, v := range tt.headers {
				c.Request.Header.Set(k, v)
			}
			got := DetectClientSession(c)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tt.want, *got)
		})
	}
}
