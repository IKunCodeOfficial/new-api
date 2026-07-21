package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripBase64LongRuns(t *testing.T) {
	longB64 := strings.Repeat("iVBORw0KGgoAAAANSUhEUgAA", 200) // 4800 连续 base64 字符

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain text untouched",
			in:   "你好，世界。hello world! 这是一段正常的中英文混合文本。",
			want: "你好，世界。hello world! 这是一段正常的中英文混合文本。",
		},
		{
			name: "short base64 kept",
			in:   "prefix " + strings.Repeat("Zm9v", 100) + " suffix",
			want: "prefix " + strings.Repeat("Zm9v", 100) + " suffix",
		},
		{
			name: "long run replaced",
			in:   "before " + longB64 + " after",
			want: "before [base64-omitted] after",
		},
		{
			name: "data uri payload replaced",
			in:   "data:image/png;base64," + longB64,
			want: "data:image/png;base64,[base64-omitted]",
		},
		{
			name: "two runs replaced",
			in:   longB64 + " mid " + longB64,
			want: "[base64-omitted] mid [base64-omitted]",
		},
		{
			name: "claude tool_result embedded image replaced",
			in:   `{"type":"tool_result","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + longB64 + `"}}]}`,
			want: `{"type":"tool_result","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"[base64-omitted]"}}]}`,
		},
		{
			name: "exact threshold replaced",
			in:   strings.Repeat("A", base64RunThreshold),
			want: "[base64-omitted]",
		},
		{
			name: "below threshold kept",
			in:   strings.Repeat("A", base64RunThreshold-1),
			want: strings.Repeat("A", base64RunThreshold-1),
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, StripBase64LongRuns(tt.in))
		})
	}
}

// TestStripBase64LongRuns_BoundsEstimatedTokens 回归保护：一张以 base64 内嵌的
// “10MB 图片”不得再被本地估算成数百万 token（历史 bug：估算值可达模型上下文
// 上限的 10 倍，导致巨额预扣费与兜底扣费）。
func TestStripBase64LongRuns_BoundsEstimatedTokens(t *testing.T) {
	blob := strings.Repeat("QUJDREVGR0hJSktMTU5PUA==", 500_000) // ~12MB base64
	text := `请分析这张截图 {"data":"` + blob + `"} 谢谢`

	stripped := StripBase64LongRuns(text)
	assert.Less(t, len(stripped), 200, "stripped text must not retain the blob")

	tokens := EstimateTokenByModel("claude-sonnet-4", stripped)
	assert.Less(t, tokens, 100, "estimated tokens must be bounded after stripping")
}
