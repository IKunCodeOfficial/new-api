package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/tokenkit"
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

	drainedText := "world with enough additional words to distinguish the full response from the delivered prefix"
	worldChunk := strings.Replace(drainChunkWorld, "world", drainedText, 1)
	_, err := fmt.Fprintf(pw, "data: %s\ndata: %s\n", drainChunkHello, worldChunk)
	require.NoError(t, err)

	waitDrainStarted(t, c, info)

	// 上游异常结束：没有 usage、没有 [DONE]，直接 EOF
	require.NoError(t, pw.Close())

	waitHandlerDone(t, done)

	require.Nil(t, result.apiErr)
	require.NotNil(t, result.usage)
	assert.Equal(t, 11, result.usage.PromptTokens, "estimate fallback must use the pre-computed prompt token estimate")
	assert.Equal(t, tokenkit.Estimate("gpt-4o-mini", "hello"+drainedText), result.usage.CompletionTokens,
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

// brokenClientWriter breaks the client connection on the first write or flush
// of the chunk containing needle, cancelling the request context in the same
// call the way net/http's connection writer does when a write fails.
type brokenClientWriter struct {
	*httptest.ResponseRecorder
	needle   string
	failOn   string
	cancel   context.CancelFunc
	broken   bool
	flushErr error
}

var errClientBrokenPipe = errors.New("write tcp: broken pipe")

func (w *brokenClientWriter) Write(p []byte) (int, error) {
	if w.failOn == "write" && !w.broken && strings.Contains(string(p), w.needle) {
		w.broken = true
		w.cancel()
		return 0, errClientBrokenPipe
	}
	return w.ResponseRecorder.Write(p)
}

func (w *brokenClientWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *brokenClientWriter) Flush() {
	w.ResponseRecorder.Flush()
	if w.failOn == "flush" && !w.broken && strings.Contains(w.Body.String(), w.needle) {
		w.broken = true
		w.cancel()
		w.flushErr = errClientBrokenPipe
	}
}

func (w *brokenClientWriter) FlushError() error {
	return w.flushErr
}

// TestStreamHandlersDrainAfterClientWriteFailure pins drain billing for the
// OpenAI-package SSE handlers that stop on a write error (responses, chat,
// the two chat/responses conversions and the image stream): when a write or
// flush to the client fails, the upstream stream is still read to its end and
// its real usage is billed, instead of stopping on the write error and billing
// an estimate (or the requested image count).
func TestStreamHandlersDrainAfterClientWriteFailure(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	drainSetting := operation_setting.GetStreamDrainSetting()
	oldDrain := drainSetting.DrainOnClientDisconnect
	drainSetting.DrainOnClientDisconnect = true
	t.Cleanup(func() { drainSetting.DrainOnClientDisconnect = oldDrain })

	responsesHello := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
	responsesEnd := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":5,\"total_tokens\":12}}}\n\n"
	chatHello := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"
	// The chat handler holds each chunk until the next one arrives.
	chatHelloSent := chatHello + "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"}}]}\n\n"
	chatEnd := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":5,\"total_tokens\":12}}\n\ndata: [DONE]\n\n"
	imageHello := "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"hello\"}\n\n"
	imageEnd := "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"image\",\"usage\":{\"input_tokens\":7,\"output_tokens\":5,\"total_tokens\":12}}\n\ndata: [DONE]\n\n"
	handlers := []struct {
		name       string
		path       string
		format     types.RelayFormat
		handler    func(*gin.Context, *relaycommon.RelayInfo, *http.Response) (*dto.Usage, *types.NewAPIError)
		first      string
		rest       string
		wantImages int
	}{
		{"responses", "/v1/responses", types.RelayFormatOpenAIResponses, OaiResponsesStreamHandler, responsesHello, responsesEnd, 0},
		{"chat", "/v1/chat/completions", types.RelayFormatOpenAI, OaiStreamHandler, chatHelloSent, chatEnd, 0},
		{"chat via responses upstream", "/v1/chat/completions", types.RelayFormatOpenAI, OaiResponsesToChatStreamHandler, responsesHello, responsesEnd, 0},
		{"responses via chat upstream", "/v1/responses", types.RelayFormatOpenAIResponses, OaiChatToResponsesStreamHandler, chatHello, chatEnd, 0},
		{"image", "/v1/images/generations", types.RelayFormatOpenAIImage, OpenaiImageStreamHandler, imageHello, imageEnd, 1},
	}
	for _, h := range handlers {
		for _, failOn := range []string{"write", "flush"} {
			t.Run(h.name+"/"+failOn+" failure", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				pr, pw := io.Pipe()
				t.Cleanup(func() {
					_ = pr.Close()
					_ = pw.Close()
				})
				writer := &brokenClientWriter{ResponseRecorder: httptest.NewRecorder(), needle: "hello", failOn: failOn, cancel: cancel}
				c, _ := gin.CreateTestContext(writer)
				c.Request = httptest.NewRequest(http.MethodPost, h.path, nil).WithContext(ctx)
				resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{"Content-Type": {"text/event-stream"}}}
				info := &relaycommon.RelayInfo{
					OriginModelName: "gpt-4o", IsStream: true, DisablePing: true, StartTime: time.Now(),
					RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: h.format, ShouldIncludeUsage: true,
					ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o"},
				}
				// An estimate far from the real usage, as with images or files in the prompt.
				info.SetEstimatePromptTokens(90000)
				if h.wantImages > 0 {
					// Three images requested; upstream completes only one.
					requested := 3
					info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{EstimatedImageCount: &requested}
				}

				done := make(chan struct{})
				var usage *dto.Usage
				var apiErr *types.NewAPIError
				go func() {
					usage, apiErr = h.handler(c, info, resp)
					close(done)
				}()
				_, err := io.WriteString(pw, h.first)
				require.NoError(t, err)
				waitDrainStarted(t, c, info)
				_, err = io.WriteString(pw, h.rest)
				require.NoError(t, err, "the upstream body must stay open after the failed client write")
				require.NoError(t, pw.Close())
				waitHandlerDone(t, done)

				require.Nil(t, apiErr)
				require.NotNil(t, usage)
				assert.Equal(t, 7, usage.PromptTokens, "must bill the real upstream input tokens")
				assert.Equal(t, 5, usage.CompletionTokens, "must bill the real upstream output tokens")
				assert.Equal(t, 12, usage.TotalTokens)
				require.NotNil(t, info.StreamStatus)
				assert.True(t, info.StreamStatus.WasClientDisconnected())
				assert.False(t, info.StreamStatus.HasErrors(), "a client that left is not a stream error: %s", info.StreamStatus.Summary())
				assert.NotContains(t, writer.Body.String(), "\"total_tokens\":12", "nothing after the failure reaches the dead client")
				if h.wantImages > 0 {
					require.NotNil(t, info.BillingImageCount, "the drained stream bills the completed image count, not the requested n")
					assert.Equal(t, h.wantImages, *info.BillingImageCount)
				}
			})
		}
	}
}
