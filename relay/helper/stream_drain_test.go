package helper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
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

type drainStreamFixture struct {
	c        *gin.Context
	recorder *httptest.ResponseRecorder
	info     *relaycommon.RelayInfo
	pr       *io.PipeReader
	pw       *io.PipeWriter
	cancel   context.CancelFunc
}

func newDrainStreamFixture(t *testing.T) *drainStreamFixture {
	t.Helper()

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

	return &drainStreamFixture{
		c:        c,
		recorder: recorder,
		info: &relaycommon.RelayInfo{
			DisablePing: true,
			ChannelMeta: &relaycommon.ChannelMeta{},
		},
		pr:     pr,
		pw:     pw,
		cancel: cancel,
	}
}

// TestStreamScannerHandler_DrainConsumesUpstreamAfterClientGone pins the drain
// contract: after the client disconnects, the scanner keeps consuming the
// upstream stream to its natural end ([DONE]), post-disconnect chunks are still
// processed (so real usage can be extracted) but never written to the dead
// client, the EndReason reflects the upstream ending, and the disconnect is
// recorded separately.
func TestStreamScannerHandler_DrainConsumesUpstreamAfterClientGone(t *testing.T) {
	fx := newDrainStreamFixture(t)

	var processed []string
	firstHandled := make(chan struct{})
	done := make(chan struct{})
	go func() {
		StreamScannerHandler(fx.c, &http.Response{Body: fx.pr}, fx.info, func(data string, sr *StreamResult) {
			processed = append(processed, data)
			_ = StringData(fx.c, data)
			if data == "first" {
				close(firstHandled)
			}
		})
		close(done)
	}()

	_, err := fmt.Fprint(fx.pw, "data: first\n")
	require.NoError(t, err)

	select {
	case <-firstHandled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first chunk")
	}

	fx.cancel()

	// 断开后上游继续输出：drain 必须继续消费直到 [DONE]
	_, err = fmt.Fprint(fx.pw, "data: second\n")
	require.NoError(t, err)
	_, err = fmt.Fprint(fx.pw, "data: [DONE]\n")
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after upstream [DONE]")
	}

	assert.Equal(t, []string{"first", "second"}, processed, "post-disconnect chunks must still be processed for usage")
	require.NotNil(t, fx.info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, fx.info.StreamStatus.EndReason, "EndReason must reflect the upstream ending, not the disconnect")
	assert.True(t, fx.info.StreamStatus.WasClientDisconnected())

	body := fx.recorder.Body.String()
	assert.Contains(t, body, "first")
	assert.NotContains(t, body, "second", "post-disconnect data must not be written to the dead client")
}

// TestStreamScannerHandler_DrainMaxWaitBoundsOrphanStream ensures an orphan
// stream cannot hold the upstream connection forever: once the drain max wait
// elapses, the handler gives up, closes the upstream body, and falls back to
// the legacy client_gone ending.
func TestStreamScannerHandler_DrainMaxWaitBoundsOrphanStream(t *testing.T) {
	setting := operation_setting.GetStreamDrainSetting()
	oldWait := setting.DrainMaxWaitSeconds
	setting.DrainMaxWaitSeconds = 1
	t.Cleanup(func() { setting.DrainMaxWaitSeconds = oldWait })

	fx := newDrainStreamFixture(t)

	firstHandled := make(chan struct{})
	done := make(chan struct{})
	go func() {
		StreamScannerHandler(fx.c, &http.Response{Body: fx.pr}, fx.info, func(data string, sr *StreamResult) {
			if data == "first" {
				close(firstHandled)
			}
		})
		close(done)
	}()

	_, err := fmt.Fprint(fx.pw, "data: first\n")
	require.NoError(t, err)

	select {
	case <-firstHandled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first chunk")
	}

	fx.cancel()

	// 上游此后保持沉默：drain 必须在 max wait（1s）内放弃
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not give up within max wait")
	}

	require.NotNil(t, fx.info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, fx.info.StreamStatus.EndReason)
	assert.True(t, fx.info.StreamStatus.WasClientDisconnected())

	// 放弃后必须关闭上游 body，让上游停止生成
	_, err = fmt.Fprint(fx.pw, "data: late\n")
	require.ErrorIs(t, err, io.ErrClosedPipe, "upstream body should be closed after drain gives up")
}

// TestClientGoneButDraining_FrozenEligibility verifies the write-guard
// predicate: it only trips when the request context is done AND drain
// eligibility was frozen at stream start.
func TestClientGoneButDraining_FrozenEligibility(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

	markStreamDrainEligible(c)
	assert.False(t, clientGoneButDraining(c), "client still connected")

	cancel()
	assert.True(t, clientGoneButDraining(c))
	assert.True(t, ClientGoneButDraining(c))

	// drain 关闭时冻结为不排空：断开后守卫不得触发
	disableStreamDrainForTest(t)
	recorder2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(recorder2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	c2.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx2)
	markStreamDrainEligible(c2)
	cancel2()
	assert.False(t, clientGoneButDraining(c2))
}
