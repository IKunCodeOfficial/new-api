package setting

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// groupUnavailableMessages maps a group name to the message returned to callers
// when the group cannot serve a request because every candidate channel is
// unavailable. It is the fallback used when no disabled channel carries its own
// message, and is configured by administrators via the GroupUnavailableMessage
// option.
var groupUnavailableMessages = map[string]string{}
var groupUnavailableMessagesMutex sync.RWMutex

func GetGroupUnavailableMessage(group string) string {
	groupUnavailableMessagesMutex.RLock()
	defer groupUnavailableMessagesMutex.RUnlock()

	return strings.TrimSpace(groupUnavailableMessages[group])
}

func GroupUnavailableMessage2JSONString() string {
	groupUnavailableMessagesMutex.RLock()
	defer groupUnavailableMessagesMutex.RUnlock()

	jsonBytes, err := common.Marshal(groupUnavailableMessages)
	if err != nil {
		common.SysError("error marshalling group unavailable messages: " + err.Error())
		return "{}"
	}
	return string(jsonBytes)
}

func UpdateGroupUnavailableMessageByJSONString(jsonStr string) error {
	parsed, err := ParseGroupUnavailableMessages(jsonStr)
	if err != nil {
		return err
	}

	groupUnavailableMessagesMutex.Lock()
	defer groupUnavailableMessagesMutex.Unlock()
	groupUnavailableMessages = parsed
	return nil
}

// ParseGroupUnavailableMessages decodes the GroupUnavailableMessage option value.
// Option updates are validated with it before the value reaches the database, so a
// rejected payload never gets persisted and reloaded on every restart. Unmarshalling
// straight into map[string]string is not enough: JSON `null` would decode without
// error and silently wipe the configured messages.
func ParseGroupUnavailableMessages(jsonStr string) (map[string]string, error) {
	if strings.TrimSpace(jsonStr) == "" {
		return map[string]string{}, nil
	}

	var decoded any
	if err := common.UnmarshalJsonStr(jsonStr, &decoded); err != nil {
		return nil, err
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("group unavailable message must be a JSON object mapping group names to messages")
	}

	messages := make(map[string]string, len(object))
	for group, message := range object {
		text, ok := message.(string)
		if !ok {
			return nil, fmt.Errorf("group unavailable message for group %s must be a string", group)
		}
		messages[group] = text
	}
	return messages, nil
}
