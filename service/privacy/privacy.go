// Package privacy integrates an external gRPC "privacy-filter" service that
// redacts PII / secrets from user text before a request is relayed upstream to
// a model provider.
//
// The whole feature is best-effort and FAIL-OPEN: a disabled per-user toggle,
// a missing/unreachable service, an RPC error, a timeout (1s) or a panic all
// leave the request untouched so the relay is never broken.
//
// Pass-through channels (model_setting.PassThroughRequestEnabled /
// ChannelSetting.PassThroughBodyEnabled) forward the raw cached request bytes
// instead of the parsed DTO. To keep them redacted too, once any text is
// redacted the cached JSON body is rewritten in place (see rewriteBody).
//
// Still out of scope (best-effort, fail-open): non-JSON bodies such as multipart
// image edits, the rerank and audio endpoints, and the Gemini embedding handler
// (which parses its own body); these are forwarded unredacted.
package privacy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"

	pb "github.com/QuantumNous/new-api/service/privacy/filterpb"

	"github.com/gin-gonic/gin"
)

// redactTimeout bounds the gRPC call; on deadline we forward unredacted.
const redactTimeout = time.Second

// segment is one piece of user text to redact together with a setter that
// writes the redacted result back into the request in place.
type segment struct {
	text string
	set  func(string)
}

// Apply redacts PII / secrets in the user's request text in place, before the
// request is relayed upstream. It must never break the relay: every failure
// mode is fail-open (request left unchanged).
func Apply(c *gin.Context, request dto.Request) {
	defer func() {
		if r := recover(); r != nil {
			common.SysError(fmt.Sprintf("privacy-filter: recovered from panic, forwarding unredacted: %v", r))
		}
	}()

	if c == nil || request == nil {
		return
	}

	// Per-user gate (default off). Read from the gin context so this package
	// stays decoupled from relay/common.
	us, ok := common.GetContextKeyType[dto.UserSetting](c, constant.ContextKeyUserSetting)
	if !ok || !us.PrivacyFilterEnabled {
		return
	}

	cl := getClient()
	if cl == nil {
		return
	}

	segments, commit := collectSegments(request)
	if len(segments) == 0 {
		return
	}

	texts := make([]string, len(segments))
	for i := range segments {
		texts[i] = segments[i].text
	}

	baseCtx := context.Background()
	if c.Request != nil && c.Request.Context() != nil {
		baseCtx = c.Request.Context()
	}
	ctx, cancel := context.WithTimeout(baseCtx, redactTimeout)
	defer cancel()

	resp, err := cl.RedactBatch(ctx, &pb.RedactBatchRequest{Texts: texts})
	if err != nil {
		logger.LogWarn(c, "privacy-filter: redact failed, forwarding unredacted: "+err.Error())
		return
	}

	results := resp.GetResults()
	if len(results) != len(segments) {
		logger.LogWarn(c, fmt.Sprintf("privacy-filter: result count mismatch (got %d, want %d), forwarding unredacted", len(results), len(segments)))
		return
	}

	redacted := 0
	for i := range results {
		// Only rewrite when the service actually changed the text, so requests
		// without any PII keep their exact original bytes.
		if results[i] != nil && results[i].GetHit() {
			segments[i].set(results[i].GetRedacted())
			redacted++
		}
	}
	if redacted > 0 {
		// Finalize formats that buffer mutations (e.g. Responses re-serializes
		// its parsed input tree). Run once, after every segment is applied, so
		// the request is never left partially redacted. If it fails, the parsed
		// field keeps its original bytes; forward unredacted (fail-open) and skip
		// the body rewrite so the cached body stays internally consistent.
		if commit != nil {
			if err := commit(); err != nil {
				logger.LogWarn(c, "privacy-filter: failed to re-serialize redacted request, forwarding unredacted: "+err.Error())
				return
			}
		}
		// Also rewrite the cached request body so pass-through channels (which
		// forward the raw bytes, bypassing the parsed DTO) send redacted text.
		// No-op for non-JSON bodies and harmless for the non-pass-through path,
		// which marshals the already-redacted DTO.
		rewriteBody(c, request)
		logger.LogDebug(c, "privacy-filter: redacted %d text segment(s)", redacted)
	}
}

// collectSegments extracts the user-authored text segments per request format.
// The optional returned func is a one-shot commit run after all segments are
// applied (used by formats that buffer mutations, e.g. Responses). Unknown
// request types yield no segments (no-op, fail-open).
//
// Not redacted (out of scope): top-level tool/function definitions
// (GeneralOpenAIRequest.Tools/Functions, GeminiChatRequest.Tools, …) and
// non-text content blocks such as Claude tool_result — they are developer-
// authored structured schemas where blind redaction could corrupt tool calling.
func collectSegments(request dto.Request) ([]segment, func() error) {
	switch r := request.(type) {
	case *dto.GeneralOpenAIRequest:
		return openAISegments(r), nil
	case *dto.ImageRequest:
		return imageSegments(r), nil
	case *dto.EmbeddingRequest:
		return embeddingSegments(r), nil
	case *dto.ClaudeRequest:
		return claudeSegments(r), nil
	case *dto.GeminiChatRequest:
		return geminiSegments(r), nil
	case *dto.OpenAIResponsesRequest:
		return responsesSegments(r)
	default:
		return nil, nil
	}
}

// appendText adds a segment, skipping blank text (nothing to redact).
func appendText(segs []segment, text string, set func(string)) []segment {
	if strings.TrimSpace(text) == "" {
		return segs
	}
	return append(segs, segment{text: text, set: set})
}

// appendScalarOrSlice handles an `any` field that is either a string or a
// []any of strings (OpenAI prompt, embedding input). Strings are mutated in
// place so the surrounding JSON shape is preserved exactly.
func appendScalarOrSlice(segs []segment, p *any) []segment {
	if p == nil || *p == nil {
		return segs
	}
	switch v := (*p).(type) {
	case string:
		segs = appendText(segs, v, func(s string) { *p = s })
	case []any:
		for k := range v {
			str, ok := v[k].(string)
			if !ok {
				continue
			}
			idx := k
			segs = appendText(segs, str, func(s string) { v[idx] = s })
		}
	}
	return segs
}

// appendArrayTextSegments redacts the text parts of an array-style content
// value (`[]any` of `map[string]any`, e.g. OpenAI/Claude multimodal content).
// Text is mutated directly on the underlying map so non-text parts (images,
// audio, tool calls, …) are preserved byte-for-byte.
func appendArrayTextSegments(segs []segment, content any) []segment {
	arr, ok := content.([]any)
	if !ok {
		return segs
	}
	for _, item := range arr {
		mp, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := mp["type"].(string); t != dto.ContentTypeText {
			continue
		}
		txt, ok := mp["text"].(string)
		if !ok {
			continue
		}
		m := mp // capture for the closure
		segs = appendText(segs, txt, func(s string) { m["text"] = s })
	}
	return segs
}

func openAISegments(r *dto.GeneralOpenAIRequest) []segment {
	if r == nil {
		return nil
	}
	var segs []segment
	// GeneralOpenAIRequest is shared across modes: chat (messages), completions
	// (prompt), moderations/edits (input), edits (instruction) and FIM
	// (prefix/suffix) all carry user text.
	segs = appendScalarOrSlice(segs, &r.Prompt)
	segs = appendScalarOrSlice(segs, &r.Input)
	segs = appendScalarOrSlice(segs, &r.Prefix)
	segs = appendScalarOrSlice(segs, &r.Suffix)
	segs = appendText(segs, r.Instruction, func(s string) { r.Instruction = s })
	for i := range r.Messages {
		msg := &r.Messages[i]
		if msg.Content == nil {
			continue
		}
		if msg.IsStringContent() {
			idx := i
			segs = appendText(segs, msg.StringContent(), func(s string) {
				r.Messages[idx].SetStringContent(s)
			})
			continue
		}
		segs = appendArrayTextSegments(segs, msg.Content)
	}
	return segs
}

func imageSegments(r *dto.ImageRequest) []segment {
	if r == nil {
		return nil
	}
	return appendText(nil, r.Prompt, func(s string) { r.Prompt = s })
}

func embeddingSegments(r *dto.EmbeddingRequest) []segment {
	if r == nil {
		return nil
	}
	return appendScalarOrSlice(nil, &r.Input)
}

func claudeSegments(r *dto.ClaudeRequest) []segment {
	if r == nil {
		return nil
	}
	var segs []segment
	if r.System != nil {
		if r.IsStringSystem() {
			segs = appendText(segs, r.GetStringSystem(), func(s string) { r.SetStringSystem(s) })
		} else {
			segs = appendArrayTextSegments(segs, r.System)
		}
	}
	for i := range r.Messages {
		msg := &r.Messages[i]
		if msg.Content == nil {
			continue
		}
		if msg.IsStringContent() {
			idx := i
			segs = appendText(segs, msg.GetStringContent(), func(s string) {
				r.Messages[idx].SetStringContent(s)
			})
			continue
		}
		segs = appendArrayTextSegments(segs, msg.Content)
	}
	return segs
}

func geminiSegments(r *dto.GeminiChatRequest) []segment {
	if r == nil {
		return nil
	}
	var segs []segment
	// Batch requests: GetAndValidateGeminiRequest accepts a body carrying only
	// `requests` (no top-level `contents`), so recurse into each sub-request.
	for i := range r.Requests {
		segs = append(segs, geminiSegments(&r.Requests[i])...)
	}
	for i := range r.Contents {
		for j := range r.Contents[i].Parts {
			p := &r.Contents[i].Parts[j]
			if p.Text == "" {
				continue
			}
			segs = appendText(segs, p.Text, func(s string) { p.Text = s })
		}
	}
	if r.SystemInstructions != nil {
		for j := range r.SystemInstructions.Parts {
			p := &r.SystemInstructions.Parts[j]
			if p.Text == "" {
				continue
			}
			segs = appendText(segs, p.Text, func(s string) { p.Text = s })
		}
	}
	return segs
}

// responsesSegments redacts user text in an OpenAI Responses API request.
// `instructions` and `input` are json.RawMessage. The string forms re-marshal
// the redacted value directly. The array form of `input` mutates a shared
// parsed tree; the returned commit re-serializes that tree into r.Input exactly
// once (run by Apply after every segment is applied), so the request is never
// left partially redacted.
func responsesSegments(r *dto.OpenAIResponsesRequest) ([]segment, func() error) {
	if r == nil {
		return nil, nil
	}
	var segs []segment
	var commit func() error

	// instructions: a plain string system prompt.
	if common.GetJsonType(r.Instructions) == "string" {
		var s string
		if err := common.Unmarshal(r.Instructions, &s); err == nil {
			segs = appendText(segs, s, func(v string) {
				if b, err := common.Marshal(v); err == nil {
					r.Instructions = b
				}
			})
		}
	}

	// input: either a bare string, or an array of message items.
	switch common.GetJsonType(r.Input) {
	case "string":
		var s string
		if err := common.Unmarshal(r.Input, &s); err == nil {
			segs = appendText(segs, s, func(v string) {
				if b, err := common.Marshal(v); err == nil {
					r.Input = b
				}
			})
		}
	case "array":
		var tree []any
		if err := common.Unmarshal(r.Input, &tree); err == nil {
			before := len(segs)
			for _, itemAny := range tree {
				item, ok := itemAny.(map[string]any)
				if !ok {
					continue
				}
				segs = appendResponsesContent(segs, item)
			}
			// Only re-serialize when the array actually yielded redactable text.
			if len(segs) > before {
				commit = func() error {
					b, err := common.Marshal(tree)
					if err != nil {
						return err
					}
					r.Input = b
					return nil
				}
			}
		}
	}
	return segs, commit
}

// appendResponsesContent redacts the `content` of one Responses input item,
// which is either a string or an array of `{type: input_text, text}` parts.
// Setters mutate the shared parsed tree in place; the caller's commit serializes
// it. Non-text parts (input_image, input_file, …) are left untouched.
func appendResponsesContent(segs []segment, item map[string]any) []segment {
	switch content := item["content"].(type) {
	case string:
		segs = appendText(segs, content, func(s string) { item["content"] = s })
	case []any:
		for _, partAny := range content {
			part, ok := partAny.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := part["type"].(string); t != "input_text" {
				continue
			}
			txt, ok := part["text"].(string)
			if !ok {
				continue
			}
			p := part
			segs = appendText(segs, txt, func(s string) { p["text"] = s })
		}
	}
	return segs
}
