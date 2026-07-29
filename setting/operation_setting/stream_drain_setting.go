package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

const (
	defaultDrainMaxWaitSeconds = 600
	maxDrainMaxWaitSeconds     = 3600
)

// StreamDrainSetting 控制客户端断开后的上游排空（drain）行为：
// 客户端断开（无论主动取消还是网络抖动）时继续读取上游流直到自然结束，
// 用上游返回的真实 usage 计费；仅在拿不到真实 usage 时才回退到估算计费。
type StreamDrainSetting struct {
	DrainOnClientDisconnect bool `json:"drain_on_client_disconnect"`
	DrainMaxWaitSeconds     int  `json:"drain_max_wait_seconds"`
}

var streamDrainSetting = StreamDrainSetting{
	DrainOnClientDisconnect: true,
	DrainMaxWaitSeconds:     defaultDrainMaxWaitSeconds,
}

func init() {
	config.GlobalConfig.Register("stream_drain_setting", &streamDrainSetting)
}

func GetStreamDrainSetting() *StreamDrainSetting {
	return &streamDrainSetting
}

// GetDrainMaxWaitSeconds 返回排空等待时长上限（秒）。
// 非法配置回退默认值，并整体钳制在 1 小时内，防止孤儿流长期占用连接。
func (s *StreamDrainSetting) GetDrainMaxWaitSeconds() int {
	if s.DrainMaxWaitSeconds <= 0 {
		return defaultDrainMaxWaitSeconds
	}
	if s.DrainMaxWaitSeconds > maxDrainMaxWaitSeconds {
		return maxDrainMaxWaitSeconds
	}
	return s.DrainMaxWaitSeconds
}
