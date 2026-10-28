package openai

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func OpenaiRealtimeHandler(c *gin.Context, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.RealtimeUsage) {
	if info == nil || info.ClientWs == nil || info.TargetWs == nil {
		return types.NewError(fmt.Errorf("invalid websocket connection"), types.ErrorCodeBadResponse), nil
	}

	info.IsStream = true
	clientConn := info.ClientWs
	targetConn := info.TargetWs

	clientClosed := make(chan struct{})
	targetClosed := make(chan struct{})
	sendChan := make(chan []byte, 100)
	receiveChan := make(chan []byte, 100)
	// Each reader reports at most a read/parse error and a panic; the buffer
	// keeps them from blocking so both can always be joined below.
	errChan := make(chan error, 8)
	clientDone := make(chan struct{})
	targetDone := make(chan struct{})

	// usageMu guards the usage counters and the session fields both readers
	// touch (tools, audio formats); it is held only for in-memory work.
	// chargeMu serializes per-turn charges with the final settlement, so
	// billing I/O never blocks message forwarding. Once settled is set the
	// session total has been taken: a reader still running (its connection
	// closed but frames left in its read buffer) must neither charge nor
	// deliver anything more, since the settlement would bill neither.
	var usageMu, chargeMu sync.Mutex
	var settled atomic.Bool
	errSessionSettled := errors.New("realtime session already settled")
	usage := &dto.RealtimeUsage{}
	localUsage := &dto.RealtimeUsage{}
	sumUsage := &dto.RealtimeUsage{}
	// chargeTurn charges a turn that is already part of sumUsage. A turn whose
	// charge fails stays in sumUsage and is billed by the final settlement,
	// which subtracts only what was charged per turn.
	chargeTurn := func(turn, session *dto.RealtimeUsage) error {
		chargeMu.Lock()
		defer chargeMu.Unlock()
		if settled.Load() {
			return errSessionSettled
		}
		return service.PreWssConsumeQuota(c, info, turn, session)
	}

	gopool.Go(func() {
		defer close(clientDone)
		defer func() {
			if r := recover(); r != nil {
				errChan <- fmt.Errorf("panic in client reader: %v", r)
			}
		}()
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := clientConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- fmt.Errorf("error reading from client: %v", err)
					}
					close(clientClosed)
					return
				}

				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- fmt.Errorf("error unmarshalling message: %v", err)
					return
				}

				err = func() error {
					usageMu.Lock()
					defer usageMu.Unlock()
					if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdate && realtimeEvent.Session != nil && realtimeEvent.Session.Tools != nil {
						info.RealtimeTools = realtimeEvent.Session.Tools
					}
					textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
					if err != nil {
						return fmt.Errorf("error counting text token: %v", err)
					}
					logger.LogInfo(c, common.LogText("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
					localUsage.TotalTokens += textToken + audioToken
					localUsage.InputTokens += textToken + audioToken
					localUsage.InputTokenDetails.TextTokens += textToken
					localUsage.InputTokenDetails.AudioTokens += audioToken
					return nil
				}()
				if err != nil {
					errChan <- err
					return
				}

				err = helper.WssString(c, targetConn, string(message))
				if err != nil {
					errChan <- fmt.Errorf("error writing to target: %v", err)
					return
				}

				select {
				case sendChan <- message:
				default:
				}
			}
		}
	})

	gopool.Go(func() {
		defer close(targetDone)
		defer func() {
			if r := recover(); r != nil {
				errChan <- fmt.Errorf("panic in target reader: %v", r)
			}
		}()
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := targetConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- fmt.Errorf("error reading from target: %v", err)
					}
					close(targetClosed)
					return
				}
				if settled.Load() {
					return
				}
				info.SetFirstResponseTime()
				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- fmt.Errorf("error unmarshalling message: %v", err)
					return
				}

				if realtimeEvent.Type == dto.RealtimeEventTypeResponseDone {
					// session is the session total including this turn; a
					// billing expression prices the session, not the turn.
					turn, session, err := func() (*dto.RealtimeUsage, *dto.RealtimeUsage, error) {
						usageMu.Lock()
						defer usageMu.Unlock()
						var realtimeUsage *dto.RealtimeUsage
						if realtimeEvent.Response != nil {
							realtimeUsage = realtimeEvent.Response.Usage
						}
						if realtimeUsage != nil {
							addRealtimeUsage(usage, realtimeUsage)
							if realtimeUsage.TotalTokens == 0 {
								// Some compatible upstreams omit total_tokens; the
								// session total must still count this billed turn.
								usage.TotalTokens += realtimeUsage.InputTokens + realtimeUsage.OutputTokens
							}
							turn := usage
							usage = &dto.RealtimeUsage{}
							localUsage = &dto.RealtimeUsage{}
							addRealtimeUsage(sumUsage, turn)
							session := *sumUsage
							return turn, &session, nil
						}
						textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
						if err != nil {
							return nil, nil, fmt.Errorf("error counting text token: %v", err)
						}
						logger.LogInfo(c, common.LogText("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
						localUsage.TotalTokens += textToken + audioToken
						info.IsFirstRequest = false
						localUsage.InputTokens += textToken + audioToken
						localUsage.InputTokenDetails.TextTokens += textToken
						localUsage.InputTokenDetails.AudioTokens += audioToken
						turn := localUsage
						localUsage = &dto.RealtimeUsage{}
						addRealtimeUsage(sumUsage, turn)
						session := *sumUsage
						return turn, &session, nil
					}()
					if err == nil {
						err = chargeTurn(turn, session)
					}
					if errors.Is(err, errSessionSettled) {
						return
					}
					if err != nil {
						errChan <- fmt.Errorf("error consume usage: %v", err)
						return
					}

				} else if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdated || realtimeEvent.Type == dto.RealtimeEventTypeSessionCreated {
					realtimeSession := realtimeEvent.Session
					if realtimeSession != nil {
						// update audio format
						usageMu.Lock()
						info.InputAudioFormat = common.GetStringIfEmpty(realtimeSession.InputAudioFormat, info.InputAudioFormat)
						info.OutputAudioFormat = common.GetStringIfEmpty(realtimeSession.OutputAudioFormat, info.OutputAudioFormat)
						usageMu.Unlock()
					}
				} else {
					err := func() error {
						usageMu.Lock()
						defer usageMu.Unlock()
						textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
						if err != nil {
							return fmt.Errorf("error counting text token: %v", err)
						}
						logger.LogInfo(c, common.LogText("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
						localUsage.TotalTokens += textToken + audioToken
						localUsage.OutputTokens += textToken + audioToken
						localUsage.OutputTokenDetails.TextTokens += textToken
						localUsage.OutputTokenDetails.AudioTokens += audioToken
						return nil
					}()
					if err != nil {
						errChan <- err
						return
					}
				}

				if settled.Load() {
					return
				}
				err = helper.WssString(c, clientConn, string(message))
				if err != nil {
					errChan <- fmt.Errorf("error writing to client: %v", err)
					return
				}

				select {
				case receiveChan <- message:
				default:
				}
			}
		}
	})

	select {
	case <-clientClosed:
	case <-targetClosed:
	case err := <-errChan:
		//return service.OpenAIErrorWrapper(err, "realtime_error", http.StatusInternalServerError), nil
		logger.LogError(c, common.LogText("realtime error: %s", err.Error()))
	case <-c.Done():
	}

	// Stop both readers before settling: a response.done arriving after this
	// point would otherwise charge a turn concurrently with, or after, the
	// session's final settlement.
	_ = targetConn.Close()
	_ = clientConn.SetReadDeadline(time.Now())
	for _, done := range []chan struct{}{clientDone, targetDone} {
		select {
		case <-done:
		case <-time.After(realtimeReaderJoinTimeout):
			logger.LogError(c, common.LogText("realtime reader did not stop within %s", realtimeReaderJoinTimeout))
		}
	}

	// From here on no reader may charge or deliver: what is in the session
	// total and not charged yet is billed once by the final settlement, and a
	// frame read later is neither billed nor forwarded.
	chargeMu.Lock()
	settled.Store(true)
	chargeMu.Unlock()

	usageMu.Lock()
	defer usageMu.Unlock()
	addRealtimeUsage(sumUsage, usage)
	addRealtimeUsage(sumUsage, localUsage)
	total := *sumUsage
	return nil, &total
}

// realtimeReaderJoinTimeout bounds how long a finished session waits for its
// readers to observe the closed connections. A charge still running after it
// is waited for by the settlement lock instead. A variable so tests can reach
// that case without waiting seconds.
var realtimeReaderJoinTimeout = 5 * time.Second

// addRealtimeUsage adds one turn's usage to the session total, including the
// image and per-modality cached counts a billing expression prices separately.
func addRealtimeUsage(total *dto.RealtimeUsage, turn *dto.RealtimeUsage) {
	total.TotalTokens += turn.TotalTokens
	total.InputTokens += turn.InputTokens
	total.OutputTokens += turn.OutputTokens
	total.InputTokenDetails.CachedTokens += turn.InputTokenDetails.CachedTokens
	total.InputTokenDetails.TextTokens += turn.InputTokenDetails.TextTokens
	total.InputTokenDetails.AudioTokens += turn.InputTokenDetails.AudioTokens
	total.InputTokenDetails.ImageTokens += turn.InputTokenDetails.ImageTokens
	total.OutputTokenDetails.TextTokens += turn.OutputTokenDetails.TextTokens
	total.OutputTokenDetails.AudioTokens += turn.OutputTokenDetails.AudioTokens
	total.OutputTokenDetails.ImageTokens += turn.OutputTokenDetails.ImageTokens
	if cached := turn.InputTokenDetails.CachedTokensDetails; cached != nil {
		// A new value each time: session snapshots taken earlier share the
		// previous pointer and must not change under a per-turn charge.
		merged := dto.CachedTokenDetails{}
		if previous := total.InputTokenDetails.CachedTokensDetails; previous != nil {
			merged = *previous
		}
		for _, count := range []struct {
			total **int
			turn  *int
		}{
			{&merged.TextTokens, cached.TextTokens},
			{&merged.AudioTokens, cached.AudioTokens},
			{&merged.ImageTokens, cached.ImageTokens},
		} {
			if count.turn == nil {
				continue
			}
			sum := *count.turn
			if *count.total != nil {
				sum += **count.total
			}
			*count.total = &sum
		}
		total.InputTokenDetails.CachedTokensDetails = &merged
	}
}
