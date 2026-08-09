package openai

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// codexRetryDelayRegex mirrors the Codex CLI regex that parses the retry delay
// from a rate_limit_exceeded message (codex-rs sse/responses.rs). The rewritten
// message must keep matching it, and the leftmost match must be our 2s hint.
var codexRetryDelayRegex = regexp.MustCompile(`(?i)try again in\s*(\d+(?:\.\d+)?)\s*(s|ms|seconds?)`)

func failedEvent(code, message string) string {
	return fmt.Sprintf(
		`{"type":"response.failed","sequence_number":3,"response":{"id":"resp_123","object":"response","created_at":1755041560,"status":"failed","error":{"code":%q,"message":%q},"usage":null}}`,
		code, message,
	)
}

func TestRewriteOverloadedResponsesFailure(t *testing.T) {
	tests := []struct {
		name        string
		data        string
		wantRewrite bool
		wantCode    string
	}{
		{
			name:        "server_is_overloaded is rewritten",
			data:        failedEvent("server_is_overloaded", "The server is currently overloaded."),
			wantRewrite: true,
			wantCode:    "server_is_overloaded",
		},
		{
			name:        "slow_down is rewritten",
			data:        failedEvent("slow_down", ""),
			wantRewrite: true,
			wantCode:    "slow_down",
		},
		{
			name:        "real rate limit error is left untouched",
			data:        failedEvent("rate_limit_exceeded", "Rate limit reached. Please try again in 11.054s."),
			wantRewrite: false,
		},
		{
			name:        "non-overload failure is left untouched",
			data:        failedEvent("context_length_exceeded", "too long"),
			wantRewrite: false,
		},
		{
			name:        "failed event without error object is left untouched",
			data:        `{"type":"response.failed","response":{"id":"resp_123","status":"failed"}}`,
			wantRewrite: false,
		},
		{
			name:        "invalid json is left untouched",
			data:        `data garbage`,
			wantRewrite: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rewritten, originalCode, ok := rewriteOverloadedResponsesFailure(tt.data)
			require.Equal(t, tt.wantRewrite, ok)
			if !tt.wantRewrite {
				assert.Empty(t, rewritten)
				assert.Empty(t, originalCode)
				return
			}

			assert.Equal(t, tt.wantCode, originalCode)
			assert.Equal(t, "rate_limit_exceeded", gjson.Get(rewritten, "response.error.code").String())

			message := gjson.Get(rewritten, "response.error.message").String()
			match := codexRetryDelayRegex.FindStringSubmatch(message)
			require.NotNil(t, match, "rewritten message must stay parseable by the Codex retry-delay regex")
			assert.Equal(t, "2", match[1], "leftmost delay match must be the injected 2s hint")
			assert.Contains(t, message, tt.wantCode, "original code must stay visible for diagnosis")

			// Everything except error.code / error.message must be preserved.
			assert.Equal(t, "response.failed", gjson.Get(rewritten, "type").String())
			assert.Equal(t, "resp_123", gjson.Get(rewritten, "response.id").String())
			assert.Equal(t, "failed", gjson.Get(rewritten, "response.status").String())
			assert.Equal(t, int64(1755041560), gjson.Get(rewritten, "response.created_at").Int())
		})
	}
}

// TestRewriteKeepsInjectedDelayLeftmost pins the ordering contract: when the
// upstream message itself contains a "try again in Ns" phrase, the injected 2s
// hint must still be what Codex parses (its regex takes the leftmost match).
func TestRewriteKeepsInjectedDelayLeftmost(t *testing.T) {
	data := failedEvent("server_is_overloaded", "Overloaded, try again in 300 seconds.")
	rewritten, _, ok := rewriteOverloadedResponsesFailure(data)
	require.True(t, ok)

	message := gjson.Get(rewritten, "response.error.message").String()
	match := codexRetryDelayRegex.FindStringSubmatch(message)
	require.NotNil(t, match)
	assert.Equal(t, "2", match[1])
}

func newResponsesStreamFixture(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *io.PipeWriter, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	// 流结束后的 completion-token 兜底统计需要已初始化的 tokenizer
	service.InitTokenEncoders()

	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pr.Close()
		_ = pw.Close()
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-5"},
		IsStream:    true,
		DisablePing: true,
		RelayMode:   relayconstant.RelayModeResponses,
		RelayFormat: types.RelayFormatOpenAI,
	}
	return c, recorder, pw, resp, info
}

func runResponsesStreamHandlerAsync(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) <-chan *types.NewAPIError {
	done := make(chan *types.NewAPIError, 1)
	go func() {
		_, apiErr := OaiResponsesStreamHandler(c, info, resp)
		done <- apiErr
	}()
	return done
}

func waitResponsesHandlerDone(t *testing.T, done <-chan *types.NewAPIError) *types.NewAPIError {
	t.Helper()
	select {
	case apiErr := <-done:
		return apiErr
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after upstream ended")
		return nil
	}
}

// TestOaiResponsesStreamHandlerRewritesOverloadBeforeOutput pins the
// end-to-end contract: an overload failure arriving before any output reaches
// the client is rewritten to a retryable rate_limit_exceeded event.
func TestOaiResponsesStreamHandlerRewritesOverloadBeforeOutput(t *testing.T) {
	c, recorder, pw, resp, info := newResponsesStreamFixture(t)

	done := runResponsesStreamHandlerAsync(c, info, resp)

	_, err := fmt.Fprintf(pw, "data: %s\n", failedEvent("server_is_overloaded", "The server is currently overloaded."))
	require.NoError(t, err)
	require.NoError(t, pw.Close())

	require.Nil(t, waitResponsesHandlerDone(t, done))

	body := recorder.Body.String()
	assert.Contains(t, body, "event: response.failed", "event type must stay response.failed")
	assert.Contains(t, body, `"code":"rate_limit_exceeded"`)
	assert.NotContains(t, body, `"code":"server_is_overloaded"`)
	assert.Contains(t, body, "Please try again in 2s.")
}

// TestOaiResponsesStreamHandlerKeepsOverloadAfterOutput pins the delivered-
// output gate: once any output item or delta has been sent downstream, the
// overload failure must pass through unchanged so the client does not replay
// a turn that already produced content.
func TestOaiResponsesStreamHandlerKeepsOverloadAfterOutput(t *testing.T) {
	c, recorder, pw, resp, info := newResponsesStreamFixture(t)

	done := runResponsesStreamHandlerAsync(c, info, resp)

	deltaEvent := `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hello"}`
	_, err := fmt.Fprintf(pw, "data: %s\ndata: %s\n", deltaEvent, failedEvent("server_is_overloaded", "The server is currently overloaded."))
	require.NoError(t, err)
	require.NoError(t, pw.Close())

	require.Nil(t, waitResponsesHandlerDone(t, done))

	body := recorder.Body.String()
	assert.Contains(t, body, `"delta":"hello"`)
	assert.Contains(t, body, `"code":"server_is_overloaded"`, "failure after delivered output must not be rewritten")
	assert.NotContains(t, body, "rate_limit_exceeded")
}
