package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

// ResolveUnavailableMessage returns the administrator-configured message that
// replaces the default "no available channel" text for group/modelName, or an
// empty string when nothing is configured.
//
// Resolution order, first non-empty wins:
//  1. the unavailable channel that would have served the request (see
//     model.ResolveChannelUnavailableMessage)
//  2. the group-level fallback message
//
// The "auto" group is expanded into the user's auto groups in configured order,
// so a request routed through auto still surfaces the message of the concrete
// group that went dark; "auto" itself stays in the list as a last resort.
func ResolveUnavailableMessage(c *gin.Context, group string, modelName string, requestPath string) string {
	groups := []string{group}
	if group == "auto" {
		groups = append(GetUserAutoGroup(common.GetContextKeyString(c, constant.ContextKeyUserGroup)), group)
	}

	for _, candidateGroup := range groups {
		if message := model.ResolveChannelUnavailableMessage(candidateGroup, modelName, requestPath); message != "" {
			return message
		}
	}
	for _, candidateGroup := range groups {
		if message := setting.GetGroupUnavailableMessage(candidateGroup); message != "" {
			return message
		}
	}
	return ""
}
