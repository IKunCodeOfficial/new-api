package model

import (
	"slices"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// ResolveChannelUnavailableMessage returns the administrator-configured message of
// the unavailable channel that would have served group/modelName if it were still
// enabled. Channel selection drops disabled channels from both the memory cache and
// the ability table, so the candidates have to be recovered here by walking the
// unavailable channels that publish the requested group and model.
//
// Selection order mirrors GetRandomSatisfiedChannel: the exact model name is tried
// first and the normalized name only when no channel publishes the exact one, then
// highest priority wins, then lowest id. Once a tier has candidates the search stops
// there even if none of them carries a message, because routing would never have
// reached the next tier either. Returns an empty string when nothing matches, so
// callers can fall back to the group message or the default localized text.
func ResolveChannelUnavailableMessage(group string, modelName string, requestPath string) string {
	modelKeys := []string{modelName}
	if normalized := ratio_setting.FormatMatchingModelName(modelName); normalized != modelName {
		modelKeys = append(modelKeys, normalized)
	}

	for _, modelKey := range modelKeys {
		candidates := unavailableChannelsServing(group, modelKey, modelName, requestPath)
		if len(candidates) == 0 {
			continue
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].GetPriority() != candidates[j].GetPriority() {
				return candidates[i].GetPriority() > candidates[j].GetPriority()
			}
			return candidates[i].Id < candidates[j].Id
		})
		for _, channel := range candidates {
			if message := channel.GetOtherSettings().UnavailableMessage; message != "" {
				return message
			}
		}
		return ""
	}
	return ""
}

// unavailableChannelsServing collects the channels that are not enabled but publish
// modelKey in group and can serve requestPath. modelName is the model the client
// actually asked for, which Advanced Custom (type 58) routes are matched against.
func unavailableChannelsServing(group string, modelKey string, modelName string, requestPath string) []*Channel {
	var candidates []*Channel

	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		for _, channel := range channelsIDM {
			if channel.Status == common.ChannelStatusEnabled {
				continue
			}
			if !slices.Contains(channel.GetGroups(), group) || !slices.Contains(channel.GetModels(), modelKey) {
				continue
			}
			candidates = append(candidates, channel)
		}
		channelSyncLock.RUnlock()
	} else {
		var channelIds []int
		err := DB.Model(&Ability{}).
			Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, modelKey, false).
			Pluck("channel_id", &channelIds).Error
		if err != nil || len(channelIds) == 0 {
			return nil
		}
		if err = DB.Where("id in ? and status <> ?", channelIds, common.ChannelStatusEnabled).Omit("key").Find(&candidates).Error; err != nil {
			return nil
		}
	}

	if requestPath == "" {
		return candidates
	}
	// Advanced Custom channels only serve paths their routes declare, so a channel
	// that could not have handled this request must not speak for it.
	return slices.DeleteFunc(candidates, func(channel *Channel) bool {
		if channel.Type != constant.ChannelTypeAdvancedCustom {
			return false
		}
		config := channel.GetOtherSettings().AdvancedCustom
		return config == nil || !config.SupportsPathForModel(requestPath, modelName)
	})
}
