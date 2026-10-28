package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	clientSessionClaudeCode = "claude_code"
	clientSessionCodex      = "codex"
	clientSessionMaxLen     = 128
)

// ClientSession identifies the coding-agent conversation (Claude Code, Codex)
// that issued a relay request, so admins can group consume logs by session.
type ClientSession struct {
	Client    string `json:"client"`
	SessionID string `json:"session_id"`
	Source    string `json:"source"`
}

// DetectClientSession reads the session id that Claude Code or Codex attach to
// every request. Headers win; body fields are only consulted once the client
// has been recognized, so other SDKs that happen to send metadata are ignored.
func DetectClientSession(c *gin.Context) *ClientSession {
	if c == nil || c.Request == nil {
		return nil
	}
	header := c.Request.Header
	userAgent := strings.ToLower(header.Get("User-Agent"))
	originator := strings.ToLower(header.Get("Originator"))

	if id := header.Get("X-Claude-Code-Session-Id"); strings.TrimSpace(id) != "" {
		return newClientSession(clientSessionClaudeCode, id, "header:x-claude-code-session-id")
	}
	for _, name := range []string{"Session_id", "Session-Id"} {
		if id := header.Get(name); strings.TrimSpace(id) != "" {
			return newClientSession(clientSessionCodex, id, "header:"+strings.ToLower(name))
		}
	}

	isClaudeCode := strings.HasPrefix(userAgent, "claude-cli") || strings.EqualFold(header.Get("X-App"), "cli")
	isCodex := strings.Contains(originator, "codex") || strings.Contains(userAgent, "codex")
	if !isClaudeCode && !isCodex {
		return nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil
	}
	body, err := storage.Bytes()
	if err != nil || len(body) == 0 {
		return nil
	}

	if isClaudeCode {
		// metadata.user_id is either a JSON string carrying session_id (newer
		// releases) or "user_<hash>_account_<uuid>_session_<uuid>".
		userID := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String())
		if gjson.Valid(userID) {
			if id := gjson.Get(userID, "session_id").String(); strings.TrimSpace(id) != "" {
				return newClientSession(clientSessionClaudeCode, id, "body:metadata.user_id")
			}
		} else if _, id, ok := strings.Cut(userID, "_session_"); ok && strings.TrimSpace(id) != "" {
			return newClientSession(clientSessionClaudeCode, id, "body:metadata.user_id")
		}
		return nil
	}
	// Codex sets prompt_cache_key to its conversation id.
	if id := gjson.GetBytes(body, "prompt_cache_key").String(); strings.TrimSpace(id) != "" {
		return newClientSession(clientSessionCodex, id, "body:prompt_cache_key")
	}
	return nil
}

func newClientSession(client, id, source string) *ClientSession {
	id = strings.TrimSpace(id)
	if len(id) > clientSessionMaxLen {
		id = id[:clientSessionMaxLen]
	}
	return &ClientSession{Client: client, SessionID: id, Source: source}
}

// AppendClientSessionAdminInfo records the coding-agent session under
// other.admin_info.client_session; non-admin viewers never see it.
func AppendClientSessionAdminInfo(c *gin.Context, other *model.LogOther) {
	if other == nil {
		return
	}
	if session := DetectClientSession(c); session != nil {
		other.SetAdmin("client_session", session)
	}
}
