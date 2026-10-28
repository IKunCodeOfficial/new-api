package openai

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/tokenkit"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// realtimeSession is a running OpenaiRealtimeHandler between a user websocket
// and a fake upstream that sends frames and then either hangs up or keeps the
// connection open.
type realtimeSession struct {
	info           *relaycommon.RelayInfo
	userConn       *websocket.Conn
	upstreamClosed chan struct{} // closed when the upstream sees the connection end
	done           chan *dto.RealtimeUsage
}

// realtimeUpstream is what the fake upstream sends once connected.
type realtimeUpstream struct {
	frames  []string
	hangsUp bool // close the connection after the frames
	// oneWrite sends all frames in a single TCP write, so they reach the
	// gateway's read buffer together.
	oneWrite bool
	// slowClient shrinks the client connection's socket buffers, so a large
	// frame forwarded to a client that is not reading blocks the forward. The
	// gateway's upstream read buffer is enlarged so the frames behind that
	// frame are already buffered when the upstream connection is closed.
	slowClient bool
}

// prepare, when set, runs against the session's database before the handler
// starts.
func startRealtimeSession(t *testing.T, walletQuota int, upstream realtimeUpstream, prepare func(*gorm.DB)) *realtimeSession {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // one private in-memory database per test run
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled = previousRedis
	})
	t.Setenv("LOG_SQL_DSN", "")
	previousMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, model.InitLogDB()) // dialect column quoting (tokens.key)
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "realtime", Quota: walletQuota, Status: common.UserStatusEnabled, AffCode: "rt01"}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: "realtime", Name: "rt", Status: common.TokenStatusEnabled, UnlimitedQuota: true}).Error)
	if prepare != nil {
		prepare(db)
	}

	session := &realtimeSession{upstreamClosed: make(chan struct{}), done: make(chan *dto.RealtimeUsage, 1)}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer close(session.upstreamClosed)
		defer conn.Close()
		if upstream.oneWrite {
			var raw []byte
			for _, frame := range upstream.frames {
				raw = append(raw, 0x81) // FIN, text frame; server frames are unmasked
				switch n := len(frame); {
				case n < 126:
					raw = append(raw, byte(n))
				case n < 1<<16:
					raw = append(raw, 126, byte(n>>8), byte(n))
				default:
					raw = append(raw, 127, 0, 0, 0, 0, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
				}
				raw = append(raw, frame...)
			}
			_, _ = conn.UnderlyingConn().Write(raw)
		} else {
			for _, frame := range upstream.frames {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(frame))
			}
		}
		if upstream.hangsUp {
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(upstreamServer.Close)
	serverSide := make(chan *websocket.Conn, 1)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if tcp, ok := conn.UnderlyingConn().(*net.TCPConn); ok && upstream.slowClient {
			_ = tcp.SetWriteBuffer(4096)
		}
		serverSide <- conn
	}))
	t.Cleanup(front.Close)

	targetDialer := *websocket.DefaultDialer
	if upstream.slowClient {
		targetDialer.ReadBufferSize = 4 << 20
	}
	targetConn, _, err := targetDialer.Dial("ws"+strings.TrimPrefix(upstreamServer.URL, "http"), nil)
	require.NoError(t, err)
	userDialer := *websocket.DefaultDialer
	if upstream.slowClient {
		userDialer.ReadBufferSize = 1024
		userDialer.NetDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetReadBuffer(4096)
			}
			return conn, err
		}
	}
	session.userConn, _, err = userDialer.Dial("ws"+strings.TrimPrefix(front.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.userConn.Close() })
	clientConn := <-serverSide

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	session.info = &relaycommon.RelayInfo{
		UserId:          1,
		TokenKey:        "sk-realtime",
		OriginModelName: "gpt-4o-realtime-preview",
		UsingGroup:      "default",
		ClientWs:        clientConn,
		TargetWs:        targetConn,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o-realtime-preview"},
	}
	go func() {
		_, usage := OpenaiRealtimeHandler(c, session.info)
		session.done <- usage
	}()
	return session
}

func (s *realtimeSession) wait(t *testing.T) *dto.RealtimeUsage {
	t.Helper()
	select {
	case usage := <-s.done:
		require.NotNil(t, usage)
		return usage
	case <-time.After(10 * time.Second):
		t.Fatal("realtime handler did not finish")
		return nil
	}
}

// Every turn is added to the session total that the final settlement bills,
// which subtracts only what was charged per turn: a turn whose per-turn charge
// fails (the wallet cannot cover it) is billed once by the final settlement, a
// turn whose usage omits total_tokens still counts, output delivered before the
// session ends without response.done is billed, and a response.done without
// usage charges the locally counted output.
func TestRealtimeSessionUsageCountsEveryTurnOnce(t *testing.T) {
	const transcript = "The quick brown fox jumps over the lazy dog."
	delta := `{"type":"response.audio_transcript.delta","delta":"` + transcript + `"}`
	transcriptTokens := tokenkit.Count("gpt-4o-realtime-preview", transcript)
	require.Positive(t, transcriptTokens)
	cases := []struct {
		name            string
		walletQuota     int
		frames          []string
		upstreamHangsUp bool
		wantTotal       int
		wantOutput      int
		wantTurnCharged bool
		// wantInput, when set, is the input breakdown a billing expression
		// prices: image and per-modality cached counts must survive.
		wantInput *dto.InputTokenDetails
	}{
		{
			name:        "failed per-turn charge",
			walletQuota: 100,
			frames:      []string{`{"type":"response.done","response":{"usage":{"total_tokens":15000,"input_tokens":15000,"output_tokens":0,"input_token_details":{"text_tokens":15000}}}}`},
			wantTotal:   15000,
		},
		{
			name:            "usage without total_tokens",
			walletQuota:     100_000_000,
			frames:          []string{`{"type":"response.done","response":{"usage":{"input_tokens":100,"output_tokens":50,"input_token_details":{"text_tokens":100},"output_token_details":{"text_tokens":50}}}}`},
			upstreamHangsUp: true,
			wantTotal:       150,
			wantOutput:      50,
			wantTurnCharged: true,
		},
		{
			// The upstream usage covers the turn, so the local estimate of
			// its streamed output must not be added on top.
			name:            "delta then response.done with usage",
			walletQuota:     100_000_000,
			frames:          []string{delta, `{"type":"response.done","response":{"usage":{"total_tokens":150,"input_tokens":100,"output_tokens":50,"input_token_details":{"text_tokens":100},"output_token_details":{"text_tokens":50}}}}`},
			upstreamHangsUp: true,
			wantTotal:       150,
			wantOutput:      50,
			wantTurnCharged: true,
		},
		{
			name:        "image and cached details are kept across turns",
			walletQuota: 100_000_000,
			frames: []string{
				`{"type":"response.done","response":{"usage":{"total_tokens":1100,"input_tokens":1000,"output_tokens":100,"input_token_details":{"text_tokens":200,"image_tokens":800,"cached_tokens":150,"cached_tokens_details":{"text_tokens":150}},"output_token_details":{"text_tokens":100}}}}`,
				`{"type":"response.done","response":{"usage":{"total_tokens":1100,"input_tokens":1000,"output_tokens":100,"input_token_details":{"text_tokens":400,"audio_tokens":600,"cached_tokens":700,"cached_tokens_details":{"text_tokens":300,"audio_tokens":400}},"output_token_details":{"text_tokens":100}}}}`,
			},
			upstreamHangsUp: true,
			wantTotal:       2200,
			wantOutput:      200,
			wantTurnCharged: true,
			wantInput: &dto.InputTokenDetails{TextTokens: 600, AudioTokens: 600, ImageTokens: 800, CachedTokens: 850,
				CachedTokensDetails: &dto.CachedTokenDetails{TextTokens: common.GetPointer(450), AudioTokens: common.GetPointer(400)}},
		},
		{
			name:            "output delivered before the session ends",
			walletQuota:     100_000_000,
			frames:          []string{delta},
			upstreamHangsUp: true,
			wantTotal:       transcriptTokens,
			wantOutput:      transcriptTokens,
		},
		{
			name:            "response.done without usage",
			walletQuota:     100_000_000,
			frames:          []string{delta, `{"type":"response.done","response":{}}`},
			upstreamHangsUp: true,
			wantTotal:       transcriptTokens,
			wantOutput:      transcriptTokens,
			wantTurnCharged: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := startRealtimeSession(t, tc.walletQuota, realtimeUpstream{frames: tc.frames, hangsUp: tc.upstreamHangsUp}, nil)
			usage := session.wait(t)
			assert.Equal(t, tc.wantTotal, usage.TotalTokens)
			assert.Equal(t, tc.wantOutput, usage.OutputTokens)
			if tc.wantInput != nil {
				assert.Equal(t, *tc.wantInput, usage.InputTokenDetails)
			}
			if !tc.wantTurnCharged {
				assert.Zero(t, session.info.WssChargedQuota, "nothing was charged per turn")
				return
			}
			assert.Positive(t, session.info.WssChargedQuota, "the turn was charged as it happened")
		})
	}
}

// When the user hangs up, the session must close the upstream connection
// itself instead of leaving it open until the reader-join timeout.
func TestRealtimeUserHangUpClosesUpstream(t *testing.T) {
	session := startRealtimeSession(t, 100_000_000, realtimeUpstream{}, nil)
	require.NoError(t, session.userConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")))

	select {
	case <-session.upstreamClosed:
	case <-time.After(realtimeReaderJoinTimeout):
		t.Fatal("upstream connection was left open after the user hung up")
	}
	session.wait(t)
}

// The final settlement bills the session total, which already includes a turn
// whose per-turn charge is still running. The handler must not return, letting
// the settlement run, until that charge has finished; otherwise the turn would
// be charged per turn after the settlement and billed twice. This holds while
// the readers are joined and also when the charge outlasts the join timeout,
// where the settlement lock waits for it.
func TestRealtimeHandlerWaitsForAnInFlightTurnCharge(t *testing.T) {
	for _, tc := range []struct {
		name        string
		joinTimeout time.Duration
	}{
		{name: "within the reader join timeout", joinTimeout: realtimeReaderJoinTimeout},
		{name: "beyond the reader join timeout", joinTimeout: 10 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previousTimeout := realtimeReaderJoinTimeout
			realtimeReaderJoinTimeout = tc.joinTimeout
			t.Cleanup(func() { realtimeReaderJoinTimeout = previousTimeout })
			blocked, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			turn := `{"type":"response.done","response":{"usage":{"total_tokens":150,"input_tokens":100,"output_tokens":50,"input_token_details":{"text_tokens":100},"output_token_details":{"text_tokens":50}}}}`
			session := startRealtimeSession(t, 100_000_000, realtimeUpstream{frames: []string{turn}}, func(db *gorm.DB) {
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("block_turn_charge", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						once.Do(func() {
							close(blocked)
							<-release
						})
					}
				}))
			})
			select {
			case <-blocked:
			case <-time.After(10 * time.Second):
				t.Fatal("the turn charge did not start")
			}
			require.NoError(t, session.userConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")))

			select {
			case <-session.done:
				t.Fatal("the handler returned while a turn charge was still running")
			case <-time.After(300 * time.Millisecond):
			}
			close(release)
			usage := session.wait(t)
			assert.Equal(t, 150, usage.TotalTokens)
			assert.Positive(t, session.info.WssChargedQuota, "the turn charge finished before the handler returned")
		})
	}
}

// A reader that outlives the join timeout keeps frames already in its read
// buffer after the session total has been taken. Those turns are not in the
// settled total, so the reader must neither charge nor deliver them: every
// turn the client received and every per-turn charge must be part of the
// total the final settlement bills, and no frame read after the settlement
// (such as streamed output) is delivered. Here the reader is stuck forwarding
// a large frame to a client that is not reading, with output and a turn
// behind it in its read buffer, while the session ends and settles.
func TestRealtimeSettledSessionNeitherChargesNorDeliversLaterTurns(t *testing.T) {
	previousRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o-realtime-preview":1}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios)) })
	previousTimeout := realtimeReaderJoinTimeout
	realtimeReaderJoinTimeout = 10 * time.Millisecond
	t.Cleanup(func() { realtimeReaderJoinTimeout = previousTimeout })
	// 150 input text tokens at model ratio 1 cost 150.
	turn := `{"type":"response.done","response":{"usage":{"total_tokens":150,"input_tokens":150,"output_tokens":0,"input_token_details":{"text_tokens":150}}}}`
	padding := `{"type":"rate_limits.updated","padding":"` + strings.Repeat("x", 1<<20) + `"}`
	output := `{"type":"response.audio_transcript.delta","delta":"unbilled output"}`
	session := startRealtimeSession(t, 100_000_000, realtimeUpstream{frames: []string{padding, output, turn}, oneWrite: true, slowClient: true}, nil)

	// The forward of the padding has started once its first bytes arrive.
	_, reader, err := session.userConn.NextReader()
	require.NoError(t, err)
	_, err = io.ReadFull(reader, make([]byte, 16))
	require.NoError(t, err)
	// A malformed client frame ends the session; the client connection stays
	// writable, so anything forwarded later would arrive.
	require.NoError(t, session.userConn.WriteMessage(websocket.TextMessage, []byte("not json")))
	usage := session.wait(t)

	// Unblock the forward; the reader then reaches the buffered turn.
	_, err = io.Copy(io.Discard, reader)
	require.NoError(t, err)
	var forwarded []string
	require.NoError(t, session.userConn.SetReadDeadline(time.Now().Add(time.Second)))
	for {
		_, message, err := session.userConn.ReadMessage()
		if err != nil {
			break
		}
		forwarded = append(forwarded, string(message))
	}
	assert.Empty(t, forwarded, "nothing read after the settlement is delivered")
	assert.LessOrEqual(t, session.info.WssChargedQuota, usage.TotalTokens, "every per-turn charge is in the settled total")
}
