package dto

import (
	"strings"
	"testing"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tool-result images must be excluded from estimation; documents retain FileMeta.
func TestClaudeGetTokenCountMetaToolResultMediaMetadata(t *testing.T) {
	screenshot := strings.Repeat("iVBORw0KGgoAAAANSUhEUgAA", 20000)
	raw := `{
		"model":"claude-opus-4-1",
		"max_tokens":1024,
		"messages":[{"role":"user","content":[{
			"type":"tool_result",
			"tool_use_id":"toolu_1",
			"content":[
				{"type":"text","text":"screenshot taken"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + screenshot + `"}},
				{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}},
				{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + screenshot + `"}},
				{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"}}
			]
		}]}]
	}`
	var req ClaudeRequest
	require.NoError(t, kitutil.UnmarshalJsonStr(raw, &req))

	meta := req.GetTokenCountMeta()

	assert.Contains(t, meta.CombineText, "screenshot taken")
	assert.NotContains(t, meta.CombineText, "iVBORw0KGgo")
	assert.NotContains(t, meta.CombineText, "example.com")
	require.Len(t, meta.Files, 2)
	for _, file := range meta.Files {
		assert.Equal(t, types.FileTypeFile, file.FileType)
	}
}

func TestClaudeGetTokenCountMetaMessageImageRetained(t *testing.T) {
	var req ClaudeRequest
	require.NoError(t, kitutil.UnmarshalJsonStr(`{"model":"claude-opus-4-1","messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]}`, &req))

	meta := req.GetTokenCountMeta()

	require.Len(t, meta.Files, 1)
	assert.Equal(t, types.FileTypeImage, meta.Files[0].FileType)
	assert.NotContains(t, meta.CombineText, "example.com")
}

func TestClaudeGetTokenCountMetaToolResultTextForms(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantText []string
	}{
		{name: "string content", content: `"ls output: a.go b.go"`, wantText: []string{"ls output: a.go b.go"}},
		{name: "text blocks", content: `[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]`, wantText: []string{"line one", "line two"}},
		{name: "plain text document", content: `[{"type":"document","source":{"type":"text","media_type":"text/plain","data":"doc body"}}]`, wantText: []string{"doc body"}},
		{name: "unknown block kept as json", content: `[{"type":"search_result","title":"result title"}]`, wantText: []string{"result title"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"model":"claude-opus-4-1","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":` + tt.content + `}]}]}`
			var req ClaudeRequest
			require.NoError(t, kitutil.UnmarshalJsonStr(raw, &req))

			meta := req.GetTokenCountMeta()

			for _, want := range tt.wantText {
				assert.Contains(t, meta.CombineText, want)
			}
			assert.Empty(t, meta.Files)
		})
	}
}
