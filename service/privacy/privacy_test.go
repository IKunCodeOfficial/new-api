package privacy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"

	pb "github.com/QuantumNous/new-api/service/privacy/filterpb"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeClient is an in-memory pb.PrivacyFilterClient used to drive Apply without
// a real gRPC connection.
type fakeClient struct {
	mu       sync.Mutex
	calls    int
	delay    time.Duration
	err      error
	truncate int                         // if >0, return only this many results
	redact   func(string) (string, bool) // (redacted, hit)
}

func (f *fakeClient) Redact(ctx context.Context, in *pb.RedactRequest, opts ...grpc.CallOption) (*pb.RedactResponse, error) {
	red, hit := f.redactFn()(in.GetText())
	return &pb.RedactResponse{Redacted: red, Hit: hit}, nil
}

func (f *fakeClient) RedactBatch(ctx context.Context, in *pb.RedactBatchRequest, opts ...grpc.CallOption) (*pb.RedactBatchResponse, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}

	fn := f.redactFn()
	results := make([]*pb.RedactResponse, 0, len(in.GetTexts()))
	for _, t := range in.GetTexts() {
		red, hit := fn(t)
		results = append(results, &pb.RedactResponse{Redacted: red, Hit: hit})
	}
	if f.truncate > 0 && f.truncate < len(results) {
		results = results[:f.truncate]
	}
	return &pb.RedactBatchResponse{Results: results}, nil
}

func (f *fakeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeClient) redactFn() func(string) (string, bool) {
	if f.redact != nil {
		return f.redact
	}
	return defaultRedact
}

// defaultRedact mimics the privacy-filter: replace known PII/secret tokens with
// typed placeholders, and report whether anything changed.
func defaultRedact(s string) (string, bool) {
	r := strings.ReplaceAll(s, "a@b.com", "[邮箱]")
	r = strings.ReplaceAll(r, "SECRET", "[密钥]")
	return r, r != s
}

func newCtx(t *testing.T, setting *dto.UserSetting) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	require.NoError(t, err)
	c.Request = req
	if setting != nil {
		common.SetContextKey(c, constant.ContextKeyUserSetting, *setting)
	}
	return c
}

func install(t *testing.T, f *fakeClient) {
	t.Helper()
	SetClientForTest(f)
	t.Cleanup(ResetClientForTest)
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := common.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestApply_OpenAIStringMessage_Redacted(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "please email a@b.com"},
		},
	}
	Apply(c, req)

	assert.Equal(t, "please email [邮箱]", req.Messages[0].StringContent())
	assert.Equal(t, 1, f.callCount())
}

func TestApply_OpenAIPrompt_Redacted(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.GeneralOpenAIRequest{Model: "gpt-3.5-turbo-instruct", Prompt: "token SECRET here"}
	Apply(c, req)

	assert.Equal(t, "token [密钥] here", req.Prompt)
}

func TestApply_OpenAIInput_Redacted(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	// moderations: input as a string
	reqStr := &dto.GeneralOpenAIRequest{Model: "omni-moderation-latest", Input: "flag a@b.com please"}
	Apply(c, reqStr)
	assert.Equal(t, "flag [邮箱] please", reqStr.Input)

	// moderations: input as an array — shape preserved
	reqArr := &dto.GeneralOpenAIRequest{Model: "omni-moderation-latest", Input: []any{"a@b.com", "x SECRET"}}
	Apply(c, reqArr)
	assert.Equal(t, []any{"[邮箱]", "x [密钥]"}, reqArr.Input)
}

func TestApply_OpenAIEdits_InstructionAndInput_Redacted(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.GeneralOpenAIRequest{
		Model:       "gpt-4",
		Input:       "my key is SECRET",
		Instruction: "email the result to a@b.com",
	}
	Apply(c, req)

	assert.Equal(t, "my key is [密钥]", req.Input)
	assert.Equal(t, "email the result to [邮箱]", req.Instruction)
}

func TestApply_OpenAIFIM_PrefixSuffix_Redacted(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.GeneralOpenAIRequest{
		Model:  "deepseek-coder",
		Prefix: "const token = \"SECRET\"",
		Suffix: "// mail a@b.com",
	}
	Apply(c, req)

	assert.Equal(t, "const token = \"[密钥]\"", req.Prefix)
	assert.Equal(t, "// mail [邮箱]", req.Suffix)
}

func TestApply_Disabled_NoOp(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: false})

	req := &dto.GeneralOpenAIRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "email a@b.com"}},
	}
	before := marshal(t, req)
	Apply(c, req)

	assert.Equal(t, before, marshal(t, req), "request must be unchanged when disabled")
	assert.Equal(t, 0, f.callCount(), "service must not be called when disabled")
}

func TestApply_SettingAbsent_NoOp(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, nil) // no user setting in context

	req := &dto.GeneralOpenAIRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "email a@b.com"}},
	}
	before := marshal(t, req)
	Apply(c, req)

	assert.Equal(t, before, marshal(t, req))
	assert.Equal(t, 0, f.callCount())
}

func TestApply_RPCError_FailOpen(t *testing.T) {
	f := &fakeClient{err: status.Error(codes.Internal, "boom")}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.GeneralOpenAIRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "email a@b.com"}},
	}
	before := marshal(t, req)
	assert.NotPanics(t, func() { Apply(c, req) })

	assert.Equal(t, before, marshal(t, req), "request must be unchanged on RPC error")
}

func TestApply_Timeout_FailOpen(t *testing.T) {
	f := &fakeClient{delay: redactTimeout + 500*time.Millisecond}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.GeneralOpenAIRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "email a@b.com"}},
	}
	before := marshal(t, req)

	start := time.Now()
	Apply(c, req)
	elapsed := time.Since(start)

	assert.Equal(t, before, marshal(t, req), "request must be unchanged on timeout")
	assert.Less(t, elapsed, redactTimeout+300*time.Millisecond, "Apply must return ~timeout, not wait for the slow server")
}

func TestApply_NilClient_FailOpen(t *testing.T) {
	install(t, nil) // SetClientForTest(nil) -> getClient returns nil
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.GeneralOpenAIRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "email a@b.com"}},
	}
	before := marshal(t, req)
	assert.NotPanics(t, func() { Apply(c, req) })

	assert.Equal(t, before, marshal(t, req))
}

func TestApply_NoHit_BytesUnchanged(t *testing.T) {
	f := &fakeClient{redact: func(s string) (string, bool) { return s, false }}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.GeneralOpenAIRequest{
		Model:    "gpt-4",
		Messages: []dto.Message{{Role: "user", Content: "nothing sensitive here"}},
	}
	before := marshal(t, req)
	Apply(c, req)

	assert.Equal(t, before, marshal(t, req), "no-hit responses must leave the request untouched")
}

func TestApply_BatchMismatch_FailOpen(t *testing.T) {
	f := &fakeClient{truncate: 1}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-4",
		Messages: []dto.Message{
			{Role: "user", Content: "email a@b.com"},
			{Role: "user", Content: "key SECRET"},
		},
	}
	before := marshal(t, req)
	Apply(c, req)

	assert.Equal(t, before, marshal(t, req), "count mismatch must fail open")
}

func TestApply_ImagePrompt_Redacted(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.ImageRequest{Model: "dall-e-3", Prompt: "draw a@b.com on a sign"}
	Apply(c, req)

	assert.Equal(t, "draw [邮箱] on a sign", req.Prompt)
}

func TestApply_EmbeddingInput_StringAndArray(t *testing.T) {
	f := &fakeClient{}
	install(t, f)

	// string input
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})
	reqStr := &dto.EmbeddingRequest{Model: "text-embedding-3-small", Input: "mail a@b.com"}
	Apply(c, reqStr)
	assert.Equal(t, "mail [邮箱]", reqStr.Input)

	// array input — shape preserved
	reqArr := &dto.EmbeddingRequest{Model: "text-embedding-3-small", Input: []any{"a@b.com", "x SECRET", "clean"}}
	Apply(c, reqArr)
	arr, ok := reqArr.Input.([]any)
	require.True(t, ok, "input must remain a []any")
	assert.Equal(t, []any{"[邮箱]", "x [密钥]", "clean"}, arr)
}

func TestApply_OpenAIArrayContent_PreservesImage(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	imageURL := map[string]any{"url": "https://x/y.png", "detail": "high"}
	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-4o",
		Messages: []dto.Message{
			{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": "what is SECRET in"},
				map[string]any{"type": "image_url", "image_url": imageURL},
			}},
		},
	}
	Apply(c, req)

	arr, ok := req.Messages[0].Content.([]any)
	require.True(t, ok)
	require.Len(t, arr, 2)

	textMap := arr[0].(map[string]any)
	assert.Equal(t, "what is [密钥] in", textMap["text"])

	imgMap := arr[1].(map[string]any)
	gotURL := imgMap["image_url"].(map[string]any)
	assert.Equal(t, "https://x/y.png", gotURL["url"], "image part must be preserved")
	assert.Equal(t, "high", gotURL["detail"])
}

func TestApply_Claude(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.ClaudeRequest{
		Model:  "claude-3-5-sonnet",
		System: "system a@b.com",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": "key SECRET"},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "BASE64DATA"}},
			}},
		},
	}
	Apply(c, req)

	assert.Equal(t, "system [邮箱]", req.System)

	arr, ok := req.Messages[0].Content.([]any)
	require.True(t, ok)
	textMap := arr[0].(map[string]any)
	assert.Equal(t, "key [密钥]", textMap["text"])

	imgMap := arr[1].(map[string]any)
	src := imgMap["source"].(map[string]any)
	assert.Equal(t, "BASE64DATA", src["data"], "image source must be preserved")
}

func TestApply_ClaudeStringContent(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.ClaudeRequest{
		Model:    "claude-3-5-sonnet",
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello a@b.com"}},
	}
	Apply(c, req)
	assert.Equal(t, "hello [邮箱]", req.Messages[0].GetStringContent())
}

func TestApply_Gemini(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{Role: "user", Parts: []dto.GeminiPart{
				{Text: "summarize a@b.com"},
				{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "BASE64DATA"}},
			}},
		},
		SystemInstructions: &dto.GeminiChatContent{
			Parts: []dto.GeminiPart{{Text: "you are SECRET"}},
		},
	}
	Apply(c, req)

	assert.Equal(t, "summarize [邮箱]", req.Contents[0].Parts[0].Text)
	assert.Equal(t, "you are [密钥]", req.SystemInstructions.Parts[0].Text)
	require.NotNil(t, req.Contents[0].Parts[1].InlineData)
	assert.Equal(t, "BASE64DATA", req.Contents[0].Parts[1].InlineData.Data, "inline data must be preserved")
}

func TestApply_UnknownRequestType_NoOp(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.BaseRequest{}
	assert.NotPanics(t, func() { Apply(c, req) })
	assert.Equal(t, 0, f.callCount(), "unknown request types must not call the service")
}

func TestApply_NilArgs_NoOp(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	assert.NotPanics(t, func() { Apply(nil, &dto.GeneralOpenAIRequest{}) })
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})
	assert.NotPanics(t, func() { Apply(c, nil) })
}

// --- OpenAI Responses API ---

func TestApply_ResponsesInputString_Redacted(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.OpenAIResponsesRequest{Model: "gpt-4o", Input: []byte(`"email a@b.com"`)}
	Apply(c, req)

	assert.JSONEq(t, `"email [邮箱]"`, string(req.Input))
}

func TestApply_ResponsesInstructions_Redacted(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.OpenAIResponsesRequest{
		Model:        "gpt-4o",
		Instructions: []byte(`"you are SECRET"`),
		Input:        []byte(`"hi"`),
	}
	Apply(c, req)

	assert.JSONEq(t, `"you are [密钥]"`, string(req.Instructions))
}

func TestApply_ResponsesInputArray_PreservesImage(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	input := `[{"role":"user","content":[` +
		`{"type":"input_text","text":"what is SECRET"},` +
		`{"type":"input_image","image_url":"https://x/y.png"}]}]`
	req := &dto.OpenAIResponsesRequest{Model: "gpt-4o", Input: []byte(input)}
	Apply(c, req)

	got := string(req.Input)
	assert.Contains(t, got, "[密钥]")
	assert.NotContains(t, got, "SECRET")
	assert.Contains(t, got, "https://x/y.png", "image part must be preserved")

	// shape must remain a valid Responses input array
	inputs := req.ParseInput()
	require.NotEmpty(t, inputs)
	assert.Equal(t, "what is [密钥]", inputs[0].Text)
}

func TestApply_ResponsesInputArray_MultipleParts_AllRedactedOnce(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	// two items, three redactable text parts total + one image: the one-shot
	// commit must leave every part redacted and the image intact.
	input := `[` +
		`{"role":"system","content":[{"type":"input_text","text":"sys SECRET"}]},` +
		`{"role":"user","content":[` +
		`{"type":"input_text","text":"mail a@b.com"},` +
		`{"type":"input_image","image_url":"https://x/y.png"},` +
		`{"type":"input_text","text":"key SECRET"}]}` +
		`]`
	req := &dto.OpenAIResponsesRequest{
		Model:        "gpt-4o",
		Instructions: []byte(`"be SECRET"`),
		Input:        []byte(input),
	}
	Apply(c, req)

	assert.JSONEq(t, `"be [密钥]"`, string(req.Instructions))

	got := string(req.Input)
	assert.NotContains(t, got, "SECRET")
	assert.NotContains(t, got, "a@b.com")
	assert.Contains(t, got, "https://x/y.png", "image part must survive")

	inputs := req.ParseInput()
	require.Len(t, inputs, 4) // 1 sys text + 1 user text + 1 image + 1 user text
	assert.Equal(t, "sys [密钥]", inputs[0].Text)
	assert.Equal(t, "mail [邮箱]", inputs[1].Text)
	assert.Equal(t, "input_image", inputs[2].Type)
	assert.Equal(t, "key [密钥]", inputs[3].Text)
	assert.Equal(t, 1, f.callCount(), "all segments must be redacted in a single batch call")
}

func TestApply_ResponsesInputArray_StringContent(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	req := &dto.OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: []byte(`[{"role":"user","content":"mail a@b.com"}]`),
	}
	Apply(c, req)

	inputs := req.ParseInput()
	require.NotEmpty(t, inputs)
	assert.Equal(t, "mail [邮箱]", inputs[0].Text)
}

// --- Pass-through body rewrite ---

// newCtxWithBody builds a context whose cached request body (KeyBodyStorage) is
// primed with body, mirroring GetAndValidateRequest, so rewriteBody has a body
// to rewrite.
func newCtxWithBody(t *testing.T, setting *dto.UserSetting, contentType string, body []byte) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", io.NopCloser(bytes.NewReader(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(body))
	c.Request = req
	if setting != nil {
		common.SetContextKey(c, constant.ContextKeyUserSetting, *setting)
	}
	_, err = common.GetRequestBody(c) // prime the cached body storage
	require.NoError(t, err)
	return c
}

func storedBody(t *testing.T, c *gin.Context) string {
	t.Helper()
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	b, err := storage.Bytes()
	require.NoError(t, err)
	return string(b)
}

func TestApply_Passthrough_BodyRewritten_PreservesUnmodeledFields(t *testing.T) {
	f := &fakeClient{}
	install(t, f)

	// custom_big / vendor_flag are NOT modeled by GeneralOpenAIRequest — they
	// must survive the rewrite byte-exact (incl. the >2^53 integer).
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"email a@b.com"}],"custom_big":123456789012345678,"vendor_flag":true}`)
	c := newCtxWithBody(t, &dto.UserSetting{PrivacyFilterEnabled: true}, "application/json", body)

	var req dto.GeneralOpenAIRequest
	require.NoError(t, common.Unmarshal(body, &req))
	Apply(c, &req)

	// DTO redacted (covers the non-passthrough path)
	assert.Equal(t, "email [邮箱]", req.Messages[0].StringContent())

	// cached body redacted (covers the passthrough path) with extras preserved
	got := storedBody(t, c)
	assert.Contains(t, got, "[邮箱]")
	assert.NotContains(t, got, "a@b.com")
	assert.Contains(t, got, `"custom_big":123456789012345678`, "unmodeled big int must survive byte-exact")
	assert.Contains(t, got, `"vendor_flag":true`, "unmodeled field must be preserved")
}

func TestApply_Passthrough_PreservesNestedUnmodeledField(t *testing.T) {
	f := &fakeClient{}
	install(t, f)

	// x_provider lives INSIDE a message object and is not modeled by dto.Message;
	// pass-through must still forward it after a sibling field is redacted.
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"email a@b.com","x_provider":{"big":123456789012345678,"flag":true}}]}`)
	c := newCtxWithBody(t, &dto.UserSetting{PrivacyFilterEnabled: true}, "application/json", body)

	var req dto.GeneralOpenAIRequest
	require.NoError(t, common.Unmarshal(body, &req))
	Apply(c, &req)

	got := storedBody(t, c)
	assert.Contains(t, got, "[邮箱]")
	assert.NotContains(t, got, "a@b.com")
	assert.Contains(t, got, `"x_provider"`, "nested unmodeled field must survive redaction")
	assert.Contains(t, got, `123456789012345678`, "nested big int must survive byte-exact")
	assert.Contains(t, got, `"flag":true`)
}

func TestApply_Passthrough_NoHit_BodyByteIdentical(t *testing.T) {
	f := &fakeClient{redact: func(s string) (string, bool) { return s, false }}
	install(t, f)

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"nothing sensitive"}]}`)
	c := newCtxWithBody(t, &dto.UserSetting{PrivacyFilterEnabled: true}, "application/json", body)

	var req dto.GeneralOpenAIRequest
	require.NoError(t, common.Unmarshal(body, &req))
	Apply(c, &req)

	assert.Equal(t, string(body), storedBody(t, c), "no redaction must leave the cached body untouched")
}

func TestApply_Passthrough_NonJSONBody_NotRewritten(t *testing.T) {
	f := &fakeClient{}
	install(t, f)

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"email a@b.com"}]}`)
	c := newCtxWithBody(t, &dto.UserSetting{PrivacyFilterEnabled: true}, "multipart/form-data; boundary=x", body)

	var req dto.GeneralOpenAIRequest
	require.NoError(t, common.Unmarshal(body, &req))
	Apply(c, &req)

	// DTO still redacted, but a non-JSON cached body is left alone (out of scope).
	assert.Equal(t, "email [邮箱]", req.Messages[0].StringContent())
	assert.Equal(t, string(body), storedBody(t, c), "non-JSON body must not be rewritten")
}

func TestApply_Passthrough_Disabled_BodyUntouched(t *testing.T) {
	f := &fakeClient{}
	install(t, f)

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"email a@b.com"}]}`)
	c := newCtxWithBody(t, &dto.UserSetting{PrivacyFilterEnabled: false}, "application/json", body)

	var req dto.GeneralOpenAIRequest
	require.NoError(t, common.Unmarshal(body, &req))
	Apply(c, &req)

	assert.Equal(t, string(body), storedBody(t, c))
	assert.Equal(t, 0, f.callCount())
}

func TestApply_Passthrough_Image_OnlyPromptRewritten(t *testing.T) {
	f := &fakeClient{}
	install(t, f)

	// The user sent ONLY model + prompt, relying on the upstream's own defaults
	// for everything else.
	body := []byte(`{"model":"dall-e-3","prompt":"draw a@b.com on a sign"}`)
	c := newCtxWithBody(t, &dto.UserSetting{PrivacyFilterEnabled: true}, "application/json", body)

	// Mirror GetAndValidOpenAIImageRequest, which fills DTO defaults the user did
	// NOT send (dall-e-3 -> size/quality, n -> 1). On a pass-through channel these
	// must NOT leak into the forwarded body: only the privacy text may change.
	req := &dto.ImageRequest{}
	require.NoError(t, common.Unmarshal(body, req))
	req.Size = "1024x1024"
	req.Quality = "standard"
	req.N = common.GetPointer(uint(1))

	Apply(c, req)

	// Non-pass-through path is unaffected: the DTO prompt is redacted.
	assert.Equal(t, "draw [邮箱] on a sign", req.Prompt)

	// Pass-through path: prompt redacted, but validation defaults not injected.
	var out map[string]any
	require.NoError(t, common.Unmarshal([]byte(storedBody(t, c)), &out))
	assert.Equal(t, "draw [邮箱] on a sign", out["prompt"])
	assert.Equal(t, "dall-e-3", out["model"])

	_, hasSize := out["size"]
	_, hasQuality := out["quality"]
	_, hasN := out["n"]
	assert.False(t, hasSize, "validation default size must not be injected into pass-through body")
	assert.False(t, hasQuality, "validation default quality must not be injected into pass-through body")
	assert.False(t, hasN, "validation default n must not be injected into pass-through body")
}

func TestApply_Gemini_BatchRequests_Redacted(t *testing.T) {
	f := &fakeClient{}
	install(t, f)
	c := newCtx(t, &dto.UserSetting{PrivacyFilterEnabled: true})

	// A batch body: only `requests`, no top-level `contents`
	// (GetAndValidateGeminiRequest accepts this shape).
	req := &dto.GeminiChatRequest{
		Requests: []dto.GeminiChatRequest{
			{
				Contents: []dto.GeminiChatContent{
					{Role: "user", Parts: []dto.GeminiPart{{Text: "mail a@b.com"}}},
				},
				SystemInstructions: &dto.GeminiChatContent{
					Parts: []dto.GeminiPart{{Text: "you are SECRET"}},
				},
			},
			{
				Contents: []dto.GeminiChatContent{
					{Role: "user", Parts: []dto.GeminiPart{{Text: "key SECRET too"}}},
				},
			},
		},
	}
	Apply(c, req)

	assert.Equal(t, "mail [邮箱]", req.Requests[0].Contents[0].Parts[0].Text)
	assert.Equal(t, "you are [密钥]", req.Requests[0].SystemInstructions.Parts[0].Text)
	assert.Equal(t, "key [密钥] too", req.Requests[1].Contents[0].Parts[0].Text)
}

func TestApply_Passthrough_Gemini_SnakeCaseSystemInstruction(t *testing.T) {
	f := &fakeClient{}
	install(t, f)

	// The user sent snake_case system_instruction — a Gemini alias the DTO accepts
	// on input but re-marshals as camelCase systemInstruction.
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi a@b.com"}]}],"system_instruction":{"parts":[{"text":"you are SECRET"}]}}`)
	c := newCtxWithBody(t, &dto.UserSetting{PrivacyFilterEnabled: true}, "application/json", body)

	req := &dto.GeminiChatRequest{}
	require.NoError(t, common.Unmarshal(body, req))
	require.NotNil(t, req.SystemInstructions, "snake_case alias must parse into SystemInstructions")
	Apply(c, req)

	// Non-pass-through path: DTO redacted.
	assert.Equal(t, "hi [邮箱]", req.Contents[0].Parts[0].Text)
	assert.Equal(t, "you are [密钥]", req.SystemInstructions.Parts[0].Text)

	// Pass-through path: BOTH contents and the snake_case system_instruction must be
	// redacted in the forwarded body — no plaintext PII left behind.
	got := storedBody(t, c)
	assert.NotContains(t, got, "a@b.com")
	assert.NotContains(t, got, "SECRET", "snake_case system_instruction must be redacted in the pass-through body")
	assert.Contains(t, got, "[邮箱]")
	assert.Contains(t, got, "[密钥]")
}
