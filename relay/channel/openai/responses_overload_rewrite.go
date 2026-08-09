package openai

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// rewriteOverloadedResponsesFailure rewrites an in-stream `response.failed`
// event whose error code is server_is_overloaded / slow_down into a
// rate_limit_exceeded failure.
//
// Codex CLI maps those two codes (both in-stream and on HTTP 503) to a fatal,
// non-retryable ServerOverloaded error, while rate_limit_exceeded enters its
// retryable branch and the retry delay is parsed from the message with the
// regex `try again in <n>(s|ms|seconds)`. The regex takes the leftmost match,
// so the retry hint must come before the original upstream message in case it
// contains similar wording.
//
// Returns the rewritten event, the original error code, and whether a rewrite
// happened. The event is edited in place via sjson so every other byte of the
// upstream payload is preserved.
func rewriteOverloadedResponsesFailure(data string) (rewritten string, originalCode string, ok bool) {
	code := gjson.Get(data, "response.error.code").String()
	if code != "server_is_overloaded" && code != "slow_down" {
		return "", "", false
	}

	message := fmt.Sprintf("Please try again in 2s. Upstream overloaded (%s).", code)
	if origMessage := strings.TrimSpace(gjson.Get(data, "response.error.message").String()); origMessage != "" {
		message = fmt.Sprintf("Please try again in 2s. Upstream overloaded (%s): %s", code, origMessage)
	}

	out, err := sjson.Set(data, "response.error.code", "rate_limit_exceeded")
	if err != nil {
		return "", "", false
	}
	out, err = sjson.Set(out, "response.error.message", message)
	if err != nil {
		return "", "", false
	}
	return out, code, true
}
