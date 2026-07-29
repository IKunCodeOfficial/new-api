package helper

import (
	"time"

	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// streamDrainEligibleKey 在流开始时冻结"客户端断开后是否排空上游"的决定。
// 必须在任何流 goroutine 启动前写入且此后不再变更，使写路径守卫与主循环
// 在整个流生命周期内观察到一致的值（否则断开瞬间缓冲中的 chunk 写入
// 会与守卫判定竞争，把 drain 流误导入致命写错误 → 退款重试路径）。
const streamDrainEligibleKey = "stream_drain_eligible"

func markStreamDrainEligible(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(streamDrainEligibleKey, operation_setting.GetStreamDrainSetting().DrainOnClientDisconnect)
}

// clientGoneButDraining 为真表示客户端已断开、但本次流处于排空模式：
// 继续消费上游数据以获取真实 usage，同时跳过所有对下游的写入。
func clientGoneButDraining(c *gin.Context) bool {
	return requestContextDone(c) && c.GetBool(streamDrainEligibleKey)
}

// ClientGoneButDraining 暴露给绕过本包写入 helper、直接 c.Render 的调用方
// （openai 包的 gemini 格式路径）。
func ClientGoneButDraining(c *gin.Context) bool {
	return clientGoneButDraining(c)
}

// waitForUpstreamAfterClientGone 在客户端断开后继续等待上游流自然结束，
// 以便按真实 usage 计费。等待有三重出口：上游结束（stopChan，EndReason 已由
// 触发方设为 done/eof 等）、上游空闲超时（ticker，即 STREAMING_TIMEOUT 看门狗，
// scanner 每收到一个 chunk 都会重置它）、以及排空最大等待时长兜底。
func waitForUpstreamAfterClientGone(c *gin.Context, info *relaycommon.RelayInfo, stopChan <-chan bool, ticker *time.Ticker) {
	info.StreamStatus.MarkClientDisconnected()
	logger.LogInfo(c, "client disconnected, draining upstream stream for real usage")

	maxWait := time.Duration(operation_setting.GetStreamDrainSetting().GetDrainMaxWaitSeconds()) * time.Second
	drainTimer := time.NewTimer(maxWait)
	defer drainTimer.Stop()

	select {
	case <-stopChan:
	case <-ticker.C:
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
	case <-drainTimer.C:
		logger.LogError(c, "stream drain max wait exceeded, aborting upstream")
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
	}
}
