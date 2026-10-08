package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveUnavailableMessageHonorsTokenAutoGroups(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","vip"]`))
	previousMessages := setting.GroupUnavailableMessage2JSONString()
	t.Cleanup(func() { require.NoError(t, setting.UpdateGroupUnavailableMessageByJSONString(previousMessages)) })
	require.NoError(t, setting.UpdateGroupUnavailableMessageByJSONString(`{"default":"default group unavailable","vip":"vip group unavailable","auto":"auto unavailable"}`))
	for _, group := range []string{"default", "vip"} {
		channel := &model.Channel{
			Type: constant.ChannelTypeOpenAI, Key: "fixture-key", Name: group,
			Status: common.ChannelStatusAutoDisabled, Group: group, Models: "channel-message-model",
		}
		channel.SetOtherSettings(dto.ChannelOtherSettings{UnavailableMessage: group + " channel unavailable"})
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, channel.AddAbilities(nil))
	}
	model.InitChannelCache()

	for _, tc := range []struct {
		name      string
		groups    []string
		snapshot  bool
		wantGroup string
	}{
		{name: "inherited global order", wantGroup: "default"},
		{name: "token restriction excludes first global group", groups: []string{"vip"}, snapshot: true, wantGroup: "vip"},
		{name: "token order overrides global order", groups: []string{"vip", "default"}, snapshot: true, wantGroup: "vip"},
		{name: "explicit empty snapshot never inherits global groups", groups: []string{}, snapshot: true, wantGroup: "auto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newRequestAutoGroupsContext()
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			if tc.snapshot {
				common.SetContextKey(c, constant.ContextKeyTokenAutoGroups, tc.groups)
			}
			wantChannel := tc.wantGroup + " channel unavailable"
			wantGroup := tc.wantGroup + " group unavailable"
			if tc.wantGroup == "auto" {
				wantChannel, wantGroup = "auto unavailable", "auto unavailable"
			}
			assert.Equal(t, wantChannel, ResolveUnavailableMessage(c, "auto", "channel-message-model", "/v1/chat/completions"))
			assert.Equal(t, wantGroup, ResolveUnavailableMessage(c, "auto", "group-message-model", "/v1/chat/completions"))
		})
	}
}
