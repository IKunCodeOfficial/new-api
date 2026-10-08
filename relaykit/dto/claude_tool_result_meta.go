package dto

import (
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// appendClaudeToolResultMeta keeps media out of text tokenization while retaining
// FileMeta for media estimation. Unknown tool-result blocks preserve their fields.
func appendClaudeToolResultMeta(content any, texts []string, files []*types.FileMeta) ([]string, []*types.FileMeta) {
	if content == nil {
		return texts, files
	}
	if text, ok := content.(string); ok {
		return append(texts, text), files
	}
	blocks, err := kitutil.Any2Type[[]map[string]any](content)
	if err != nil {
		data, _ := kitutil.Marshal(content)
		return append(texts, string(data)), files
	}
	for _, block := range blocks {
		switch block["type"] {
		case ContentTypeText:
			if text, ok := block["text"].(string); ok {
				texts = append(texts, text)
			}
		case "image", "document":
			part, err := kitutil.Any2Type[ClaudeMediaMessage](block)
			if err != nil {
				continue
			}
			if part.Source != nil && part.Source.Type == ContentTypeText {
				if text, ok := part.Source.Data.(string); ok {
					texts = append(texts, text)
				}
				continue
			}
			if source := part.ToFileSource(); source != nil {
				fileType := types.FileTypeFile
				if part.Type == "image" {
					fileType = types.FileTypeImage
				}
				files = append(files, &types.FileMeta{FileType: fileType, Source: source})
			}
		default:
			data, _ := kitutil.Marshal(block)
			texts = append(texts, string(data))
		}
	}
	return texts, files
}
