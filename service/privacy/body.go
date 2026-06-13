package privacy

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"

	"github.com/gin-gonic/gin"
)

// rewriteBody re-serializes the redacted request and replaces the cached request
// body, so pass-through channels — which forward the raw cached bytes and bypass
// the parsed DTO (see compatible_handler.go / claude_handler.go / etc.) — also
// send the redacted text.
//
// It is a no-op unless the body is a JSON object. The redacted DTO is DEEP-merged
// over the original body via json.RawMessage (see deepMergeJSON) so that
//   - provider extensions the DTO does not model — at any depth, e.g. a
//     non-standard field inside a message object — are preserved byte-for-byte,
//     including large integers that a map[string]any round-trip would corrupt
//     (common.Unmarshal does not use json.Number);
//   - fields the DTO models that the user actually sent (incl. the redacted
//     message text) come from the redacted DTO;
//   - fields the user did NOT send are left out — validation defaults the DTO
//     picked up (e.g. dall-e-3 size/quality/n) are never injected, so the
//     pass-through body keeps exactly the user's keys with only privacy text
//     swapped.
//
// Every failure mode leaves the cached body untouched (fail-open).
func rewriteBody(c *gin.Context, request dto.Request) {
	if c == nil || c.Request == nil {
		return
	}
	// Only JSON bodies. Multipart (e.g. image edits) is out of scope.
	if !strings.HasPrefix(c.Request.Header.Get("Content-Type"), "application/json") {
		return
	}

	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return
	}
	orig, err := storage.Bytes()
	if err != nil || len(orig) == 0 {
		return
	}

	if common.GetJsonType(orig) != "object" {
		// Only object bodies can be safely merged (array/scalar are out of scope).
		return
	}
	redactedDTO, err := common.Marshal(request)
	if err != nil || common.GetJsonType(redactedDTO) != "object" {
		return
	}

	replaceBodyStorage(c, deepMergeJSON(orig, redactedDTO, 0))
}

// maxMergeDepth bounds deepMergeJSON recursion against pathological nesting.
const maxMergeDepth = 64

// normalizeMergeKey canonicalizes a JSON object key for alias-tolerant matching
// in deepMergeJSON: lowercased with underscores removed, so snake_case and
// camelCase spellings of the same field (e.g. Gemini system_instruction vs
// systemInstruction) collapse to one. Used only as a fallback when no exact-key
// match exists, so exact matches keep their current behaviour.
func normalizeMergeKey(k string) string {
	return strings.ToLower(strings.ReplaceAll(k, "_", ""))
}

// deepMergeJSON overlays redacted onto orig and returns the merged JSON.
// Objects merge key-wise; equal-length arrays merge element-wise. The rules:
//   - keys present only in orig — provider extensions the DTO does not model, at
//     ANY depth — are kept byte-for-byte (so e.g. a nested big integer keeps full
//     precision);
//   - keys present in both (matched exactly, else case/underscore-insensitively
//     so a re-marshaled alias like systemInstruction overlays system_instruction):
//     the redacted value wins and the user's original key name is kept;
//   - keys present only in redacted are DROPPED, not injected: they are validation
//     defaults (e.g. dall-e-3 size/quality/n) or DTO-modeled fields the user
//     omitted, and adding them would break the pass-through contract. Redacted
//     text always lands in a key the user actually sent, so this never drops a
//     redaction.
//
// On a type mismatch, or an array length mismatch, the redacted value wins: it is
// the authoritative redacted form, and PII-safety outranks pass-through fidelity
// in that rare case.
func deepMergeJSON(orig, redacted json.RawMessage, depth int) json.RawMessage {
	if depth >= maxMergeDepth {
		return redacted
	}
	ot := common.GetJsonType(orig)
	if ot != common.GetJsonType(redacted) {
		return redacted
	}

	switch ot {
	case "object":
		var om, rm map[string]json.RawMessage
		if common.Unmarshal(orig, &om) != nil || common.Unmarshal(redacted, &rm) != nil {
			return redacted
		}
		// Only overwrite keys the user actually sent (present in orig). Keys that
		// exist solely in the redacted DTO are validation defaults (e.g. dall-e-3
		// size/quality/n) or DTO-modeled fields the user omitted; injecting them
		// would break the pass-through contract (forward the raw body, only swap
		// privacy text). Redacted text always lands in a key present in orig, so
		// dropping redacted-only keys never loses a redaction.
		//
		// normIndex (built lazily, only on a miss) maps a normalized key back to
		// the original key in om, so a re-marshaled camelCase key (e.g. Gemini
		// "systemInstruction") still overlays a user body that used snake_case
		// ("system_instruction") — keeping the user's key name — instead of being
		// dropped while the un-redacted original survives. Mirrors the DTO layer's
		// lenient (case/underscore-insensitive) key matching.
		var normIndex map[string]string
		for k, rv := range rm {
			if ov, ok := om[k]; ok {
				om[k] = deepMergeJSON(ov, rv, depth+1)
				continue
			}
			if normIndex == nil {
				normIndex = make(map[string]string, len(om))
				for origK := range om {
					normIndex[normalizeMergeKey(origK)] = origK
				}
			}
			if origKey, ok := normIndex[normalizeMergeKey(k)]; ok {
				om[origKey] = deepMergeJSON(om[origKey], rv, depth+1)
			}
		}
		if b, err := common.Marshal(om); err == nil {
			return b
		}
		return redacted
	case "array":
		var oa, ra []json.RawMessage
		if common.Unmarshal(orig, &oa) != nil || common.Unmarshal(redacted, &ra) != nil {
			return redacted
		}
		if len(oa) != len(ra) {
			return redacted
		}
		for i := range ra {
			oa[i] = deepMergeJSON(oa[i], ra[i], depth+1)
		}
		if b, err := common.Marshal(oa); err == nil {
			return b
		}
		return redacted
	default:
		return redacted
	}
}

// replaceBodyStorage swaps the cached request body for data, releasing the
// previous (possibly disk-backed) storage. Downstream readers go through
// common.GetBodyStorage, which reads the KeyBodyStorage cache first.
func replaceBodyStorage(c *gin.Context, data []byte) {
	if old, ok := c.Get(common.KeyBodyStorage); ok && old != nil {
		if bs, ok := old.(common.BodyStorage); ok {
			_ = bs.Close()
		}
	}

	if storage, err := common.CreateBodyStorage(data); err == nil {
		c.Set(common.KeyBodyStorage, storage)
	} else {
		// Fall back to rebuilding from c.Request.Body below (set to data).
		c.Set(common.KeyBodyStorage, nil)
	}
	c.Set(common.KeyRequestBody, nil)

	c.Request.Body = io.NopCloser(bytes.NewReader(data))
	c.Request.ContentLength = int64(len(data))
}
