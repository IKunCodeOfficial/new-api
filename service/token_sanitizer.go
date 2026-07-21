package service

import "strings"

// base64RunThreshold 判定为内嵌二进制数据的最小连续 base64 字符数。
// 2048 个字符约等于 1.5KB 二进制,正常自然语言/代码文本中几乎不会出现这么长的
// 不含空格与标点的 base64 字母表连续串;而内嵌的图片/文件动辄数 MB。
const base64RunThreshold = 2048

const base64OmittedPlaceholder = "[base64-omitted]"

// StripBase64LongRuns 把文本中超长的 base64 连续串替换为占位符,用于本地 token 估算。
// 图片/文件常以 base64 形式内嵌在 tool_result、tool_use 参数、data URI 或原始 JSON 中,
// 若按纯文本计数,一张 10MB 图片会被估算成数百万 token,导致预扣费与兜底计费严重失真。
// 该函数只影响本地估算,不影响发往上游的请求内容与上游真实计费。
func StripBase64LongRuns(text string) string {
	var b *strings.Builder
	runStart := -1 // 当前 base64 连续串的起始下标,-1 表示不在串中
	kept := 0      // 已写入 builder 的前缀长度(仅在 b != nil 时有意义)

	flush := func(end int) {
		runLen := end - runStart
		if runLen < base64RunThreshold {
			runStart = -1
			return
		}
		if b == nil {
			b = &strings.Builder{}
			b.Grow(len(text) - runLen + len(base64OmittedPlaceholder))
		}
		b.WriteString(text[kept:runStart])
		b.WriteString(base64OmittedPlaceholder)
		kept = end
		runStart = -1
	}

	for i := 0; i < len(text); i++ {
		if isBase64AlphabetChar(text[i]) {
			if runStart < 0 {
				runStart = i
			}
			continue
		}
		if runStart >= 0 {
			flush(i)
		}
	}
	if runStart >= 0 {
		flush(len(text))
	}

	if b == nil {
		return text
	}
	b.WriteString(text[kept:])
	return b.String()
}

// isBase64AlphabetChar 覆盖标准与 URL-safe base64 字母表(含填充 '=')。
// UTF-8 多字节字符的每个字节都带高位,天然会打断连续串,因此按字节扫描是安全的。
func isBase64AlphabetChar(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		c == '+' || c == '/' || c == '=' || c == '-' || c == '_'
}
