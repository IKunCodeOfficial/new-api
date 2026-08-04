package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type unavailableChannelFixture struct {
	id       int
	status   int
	group    string
	models   string
	priority int64
	message  string
}

func seedUnavailableChannels(t *testing.T, memoryCacheEnabled bool, fixtures []unavailableChannelFixture) {
	t.Helper()
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = memoryCacheEnabled
	require.NoError(t, DB.AutoMigrate(&Channel{}, &Ability{}))
	for _, table := range []string{"abilities", "channels"} {
		require.NoError(t, DB.Exec("DELETE FROM "+table).Error)
	}

	for _, fixture := range fixtures {
		channel := &Channel{
			Id:       fixture.id,
			Type:     1,
			Key:      fmt.Sprintf("key-%d", fixture.id),
			Name:     fmt.Sprintf("channel-%d", fixture.id),
			Status:   fixture.status,
			Group:    fixture.group,
			Models:   fixture.models,
			Priority: &fixture.priority,
		}
		channel.SetOtherSettings(dto.ChannelOtherSettings{UnavailableMessage: fixture.message})
		require.NoError(t, DB.Create(channel).Error)
		require.NoError(t, channel.AddAbilities(nil))
	}
	InitChannelCache()

	t.Cleanup(func() {
		for _, table := range []string{"abilities", "channels"} {
			require.NoError(t, DB.Exec("DELETE FROM "+table).Error)
		}
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		InitChannelCache()
	})
}

// A disabled channel is invisible to routing, so the message has to be recovered
// from the channels that publish the requested group and model. When several
// match, the one routing would have picked first (priority desc, then id asc)
// must win so administrators can predict what callers see.
func TestResolveChannelUnavailableMessagePicksRoutingOrder(t *testing.T) {
	for _, memoryCacheEnabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("memory_cache_%t", memoryCacheEnabled), func(t *testing.T) {
			seedUnavailableChannels(t, memoryCacheEnabled, []unavailableChannelFixture{
				{id: 1, status: common.ChannelStatusManuallyDisabled, group: "cc", models: "claude-opus-4-8", priority: 0, message: "low priority"},
				{id: 2, status: common.ChannelStatusAutoDisabled, group: "cc,vip", models: "claude-opus-4-8", priority: 10, message: "high priority"},
				{id: 3, status: common.ChannelStatusAutoDisabled, group: "cc", models: "claude-opus-4-8", priority: 10, message: "same priority, larger id"},
				{id: 4, status: common.ChannelStatusEnabled, group: "cc", models: "gpt-4", priority: 99, message: "enabled channel must not speak"},
			})

			assert.Equal(t, "high priority", ResolveChannelUnavailableMessage("cc", "claude-opus-4-8", ""))
			assert.Equal(t, "high priority", ResolveChannelUnavailableMessage("vip", "claude-opus-4-8", ""))
			// No unavailable channel publishes this pair, so the caller falls back.
			assert.Equal(t, "", ResolveChannelUnavailableMessage("cc", "gpt-4", ""))
			assert.Equal(t, "", ResolveChannelUnavailableMessage("other", "claude-opus-4-8", ""))
		})
	}
}

// A channel without a configured message must not shadow a lower-priority one
// that has a message, otherwise configuring a single channel would be pointless
// in a multi-channel group.
func TestResolveChannelUnavailableMessageSkipsChannelsWithoutMessage(t *testing.T) {
	seedUnavailableChannels(t, true, []unavailableChannelFixture{
		{id: 1, status: common.ChannelStatusAutoDisabled, group: "cc", models: "claude-opus-4-8", priority: 10, message: ""},
		{id: 2, status: common.ChannelStatusManuallyDisabled, group: "cc", models: "claude-opus-4-8", priority: 1, message: "maintenance"},
	})

	assert.Equal(t, "maintenance", ResolveChannelUnavailableMessage("cc", "claude-opus-4-8", ""))
}

// Routing tries the exact model name first and only falls back to the normalized
// name when no channel publishes the exact one. A higher-priority wildcard channel
// must therefore not speak for a request that an exact-name channel would have
// served once re-enabled.
func TestResolveChannelUnavailableMessagePrefersExactModelTier(t *testing.T) {
	for _, memoryCacheEnabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("memory_cache_%t", memoryCacheEnabled), func(t *testing.T) {
			seedUnavailableChannels(t, memoryCacheEnabled, []unavailableChannelFixture{
				{id: 1, status: common.ChannelStatusManuallyDisabled, group: "cc", models: "gpt-4-gizmo-x", priority: 0, message: "exact model channel"},
				{id: 2, status: common.ChannelStatusAutoDisabled, group: "cc", models: "gpt-4-gizmo-*", priority: 99, message: "wildcard channel"},
			})

			assert.Equal(t, "exact model channel", ResolveChannelUnavailableMessage("cc", "gpt-4-gizmo-x", ""))
			// No channel publishes this exact name, so the normalized tier applies.
			assert.Equal(t, "wildcard channel", ResolveChannelUnavailableMessage("cc", "gpt-4-gizmo-y", ""))
		})
	}
}

// The exact-model tier is authoritative: when it has candidates but none carries a
// message, routing would still pick one of them, so the wildcard tier must not be
// consulted for a message the caller would never have seen.
func TestResolveChannelUnavailableMessageStopsAtExactModelTierWithoutMessage(t *testing.T) {
	seedUnavailableChannels(t, true, []unavailableChannelFixture{
		{id: 1, status: common.ChannelStatusManuallyDisabled, group: "cc", models: "gpt-4-gizmo-x", priority: 0, message: ""},
		{id: 2, status: common.ChannelStatusAutoDisabled, group: "cc", models: "gpt-4-gizmo-*", priority: 99, message: "wildcard channel"},
	})

	assert.Equal(t, "", ResolveChannelUnavailableMessage("cc", "gpt-4-gizmo-x", ""))
}
