package modelstatus

import (
	"context"
	"errors"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// ClassifyFailure maps error attributes to a failure kind. Precedence is
// timeout, then 429, then 5xx, then other.
func ClassifyFailure(status int, code, errorType string, timedOut bool) FailureKind {
	code, errorType = strings.ToLower(code), strings.ToLower(errorType)
	if timedOut || status == 408 || status == 504 || status == 524 || strings.Contains(code, "timeout") {
		return FailureTimeout
	}
	for _, v := range []string{code, errorType} {
		switch v {
		case "rate_limit_exceeded", "rate_limit_error", "insufficient_quota", "quota_exceeded", "resource_exhausted":
			return Failure429
		}
	}
	if status == 429 {
		return Failure429
	}
	for _, v := range []string{code, errorType} {
		switch v {
		case "overloaded_error", "server_error", "internal_error", "service_unavailable":
			return Failure5xx
		}
	}
	if status >= 500 {
		return Failure5xx
	}
	return FailureOther
}

// FailureKindOf extracts the same inputs perf_metrics uses to classify a
// relay outcome and maps them to a failure kind.
func FailureKindOf(info *relaycommon.RelayInfo, apiErr *types.NewAPIError) FailureKind {
	if info == nil {
		return FailureOther
	}
	stream := info.StreamStatus.OutcomeSnapshot()
	timedOut := info.StreamStatus != nil &&
		(errors.Is(info.StreamStatus.EndError, context.DeadlineExceeded) || stream.EndReason == relaycommon.StreamEndReasonTimeout)
	if stream.Response == relaycommon.ResponseOutcomeFailed {
		return ClassifyFailure(stream.ErrorStatus, stream.ErrorCode, stream.ErrorType, timedOut)
	}
	if apiErr != nil {
		root := apiErr
		for {
			var inner *types.NewAPIError
			if !errors.As(root.Unwrap(), &inner) || inner == root {
				break
			}
			root = inner
		}
		return ClassifyFailure(root.StatusCode, string(root.GetErrorCode()), root.ToOpenAIError().Type, timedOut)
	}
	return ClassifyFailure(0, "", "", timedOut)
}
