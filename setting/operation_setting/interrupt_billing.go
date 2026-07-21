package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

// InterruptBillingSetting 控制请求中断（客户端断开）时的计费行为。
type InterruptBillingSetting struct {
	// DrainOnClientDisconnect: 客户端断开（client_gone，可能是主动取消也可能是网络抖动）时,
	// 不再立即关闭上游连接,而是继续把上游流读完,拿到上游真实 usage 后按真实用量结算。
	// 关闭时恢复旧行为：立即中断上游并按本地估算扣费。
	// 本地估算只覆盖净化后的文本（内嵌 base64 与文件/图片/音视频均不参与估算）。
	DrainOnClientDisconnect bool `json:"drain_on_client_disconnect"`
}

var interruptBillingSetting = InterruptBillingSetting{
	DrainOnClientDisconnect: true,
}

func init() {
	config.GlobalConfig.Register("interrupt_billing_setting", &interruptBillingSetting)
}

func GetInterruptBillingSetting() *InterruptBillingSetting {
	return &interruptBillingSetting
}
