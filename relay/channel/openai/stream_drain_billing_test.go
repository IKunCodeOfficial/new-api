package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// disableStreamDrainForTest 将测试固定到 drain 关闭时的 legacy 断开行为。
func disableStreamDrainForTest(t *testing.T) {
	t.Helper()
	setting := operation_setting.GetStreamDrainSetting()
	old := setting.DrainOnClientDisconnect
	setting.DrainOnClientDisconnect = false
	t.Cleanup(func() { setting.DrainOnClientDisconnect = old })
}

const (
	drainChunkHello = `{"id":"chatcmpl-x","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"hello"}}]}`
	drainChunkWorld = `{"id":"chatcmpl-x","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"world"}}]}`
	drainChunkUsage = `{"id":"chatcmpl-x","created":1,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`
)

type drainHandlerResult struct {
	usage  *dto.Usage
	apiErr *types.NewAPIError
}

// newDrainChatFixture builds an OaiStreamHandler fixture whose client
// "disconnects" (request context cancelled) right after the payload containing
// needle has been written to it, while the upstream stays writable via the
// returned pipe writer.
func newDrainChatFixture(t *testing.T, relayFormat types.RelayFormat, needle string) (*gin.Context, *httptest.ResponseRecorder, *io.PipeWriter, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pr.Close()
		_ = pw.Close()
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	c.Writer = &cancelAfterWriter{ResponseWriter: c.Writer, needle: needle, cancel: cancel}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o-mini"},
		IsStream:    true,
		DisablePing: true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: relayFormat,
	}
	info.SetEstimatePromptTokens(11)
	return c, recorder, pw, resp, info
}

func runOaiStreamHandlerAsync(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (<-chan struct{}, *drainHandlerResult) {
	result := &drainHandlerResult{}
	done := make(chan struct{})
	go func() {
		result.usage, result.apiErr = OaiStreamHandler(c, info, resp)
		close(done)
	}()
	return done, result
}

// waitDrainStarted blocks until the scanner main loop has observed the client
// disconnect and entered drain mode. Writing the upstream ending only after
// this point makes the drain-vs-stop select in the scanner deterministic.
func waitDrainStarted(t *testing.T, c *gin.Context, info *relaycommon.RelayInfo) {
	t.Helper()
	select {
	case <-c.Request.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("client disconnect was not triggered")
	}
	require.Eventually(t, func() bool {
		return info.StreamStatus != nil && info.StreamStatus.WasClientDisconnected()
	}, 2*time.Second, 5*time.Millisecond, "scanner main loop did not enter drain mode")
}

func waitHandlerDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after upstream ended")
	}
}

// TestOaiStreamHandlerDrainBillsRealUsage pins the core drain billing
// contract: after the client disconnects mid-stream, the upstream stream is
// consumed to completion and the REAL upstream usage is returned for billing —
// not an estimate — while nothing more is written to the dead client.
func TestOaiStreamHandlerDrainBillsRealUsage(t *testing.T) {
	c, recorder, pw, resp, info := newDrainChatFixture(t, types.RelayFormatOpenAI, "hello")

	done, result := runOaiStreamHandlerAsync(c, info, resp)

	// chunk1 排队，chunk2 触发 chunk1 的下行写出（"hello"）→ 客户端断开
	_, err := fmt.Fprintf(pw, "data: %s\ndata: %s\n", drainChunkHello, drainChunkWorld)
	require.NoError(t, err)

	waitDrainStarted(t, c, info)

	// 断开之后上游继续输出真实 usage 并正常结束
	_, err = fmt.Fprintf(pw, "data: %s\ndata: [DONE]\n", drainChunkUsage)
	require.NoError(t, err)

	waitHandlerDone(t, done)

	require.Nil(t, result.apiErr)
	require.NotNil(t, result.usage)
	assert.Equal(t, 10, result.usage.PromptTokens, "must bill the real upstream prompt tokens")
	assert.Equal(t, 20, result.usage.CompletionTokens, "must bill the real upstream completion tokens")
	assert.Equal(t, 30, result.usage.TotalTokens)

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.WasClientDisconnected())

	body := recorder.Body.String()
	assert.Contains(t, body, "hello")
	assert.NotContains(t, body, "world", "post-disconnect chunks must not be written to the dead client")
}

// TestOaiStreamHandlerDrainFallsBackToEstimateWithoutUsage pins the fallback:
// when the drained upstream stream never reports usage, billing falls back to
// the estimator — over the FULL drained response text, not just the part the
// client received.
func TestOaiStreamHandlerDrainFallsBackToEstimateWithoutUsage(t *testing.T) {
	c, _, pw, resp, info := newDrainChatFixture(t, types.RelayFormatOpenAI, "hello")

	done, result := runOaiStreamHandlerAsync(c, info, resp)

	_, err := fmt.Fprintf(pw, "data: %s\ndata: %s\n", drainChunkHello, drainChunkWorld)
	require.NoError(t, err)

	waitDrainStarted(t, c, info)

	// 上游异常结束：没有 usage、没有 [DONE]，直接 EOF
	require.NoError(t, pw.Close())

	waitHandlerDone(t, done)

	require.Nil(t, result.apiErr)
	require.NotNil(t, result.usage)
	assert.Equal(t, 11, result.usage.PromptTokens, "estimate fallback must use the pre-computed prompt token estimate")
	assert.Equal(t, service.EstimateTokenByModel("gpt-4o-mini", "helloworld"), result.usage.CompletionTokens,
		"estimate must cover the full drained text, including post-disconnect chunks")
	assert.Equal(t, result.usage.PromptTokens+result.usage.CompletionTokens, result.usage.TotalTokens)

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonEOF, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.WasClientDisconnected())
}

// TestOaiStreamHandlerDrainSkipsGeminiWritesAfterDisconnect pins the
// gemini-format direct-Render guard: those paths bypass the unified SSE write
// helpers, and without the guard a drain would keep writing converted chunks
// to the dead connection.
func TestOaiStreamHandlerDrainSkipsGeminiWritesAfterDisconnect(t *testing.T) {
	c, recorder, pw, resp, info := newDrainChatFixture(t, types.RelayFormatGemini, "hello")

	done, result := runOaiStreamHandlerAsync(c, info, resp)

	_, err := fmt.Fprintf(pw, "data: %s\ndata: %s\n", drainChunkHello, drainChunkWorld)
	require.NoError(t, err)

	waitDrainStarted(t, c, info)

	_, err = fmt.Fprintf(pw, "data: %s\ndata: [DONE]\n", drainChunkUsage)
	require.NoError(t, err)

	waitHandlerDone(t, done)

	require.Nil(t, result.apiErr)
	require.NotNil(t, result.usage)
	assert.Equal(t, 30, result.usage.TotalTokens, "real usage must survive the gemini-format drain")

	body := recorder.Body.String()
	assert.Contains(t, body, "hello")
	assert.NotContains(t, body, "world", "gemini direct-Render path must skip writes while draining")
}
