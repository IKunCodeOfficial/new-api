package dto

import (
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

// appendClaudeToolResultTexts 从 tool_result 内容中提取用于 token 估算的文本。
// 不能把整个 content 序列化成 JSON 文本，否则截图等 base64 数据会被当成文字估算，
// 一张图就能被估成十几万 token，导致预扣费虚高。
// image/document 等媒体块不参与估算，也不放入 Files：Files 中的附件在估算阶段可能被
// 下载（URL）或完整解码（base64），会给每个请求带来额外开销和失败路径；
// 媒体的真实用量由结算阶段按上游 usage 补齐。
// 直接遍历已解析的 []any，只引用原有字符串，不做额外的序列化/反序列化。
func appendClaudeToolResultTexts(content any, texts []string) []string {
	switch v := content.(type) {
	case nil:
		return texts
	case string:
		if v != "" {
			texts = append(texts, v)
		}
		return texts
	case []any:
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case ContentTypeText:
				if text, _ := block["text"].(string); text != "" {
					texts = append(texts, text)
				}
			case "image":
			case "document":
				// 纯文本 document：source.data 就是文本本身，按文字估算
				source, _ := block["source"].(map[string]any)
				if source != nil && source["type"] == ContentTypeText {
					if data, _ := source["data"].(string); data != "" {
						texts = append(texts, data)
					}
				}
			default:
				b, _ := kitutil.Marshal(block)
				texts = append(texts, string(b))
			}
		}
		return texts
	default:
		b, _ := kitutil.Marshal(content)
		return append(texts, string(b))
	}
}
