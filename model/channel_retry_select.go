package model

import (
	"fmt"
	"math/rand"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

type RetryChannelSelectionOptions struct {
	PriorityIndex       int
	TriedChannels       map[int]struct{}
	TriedKeys           map[int]map[int]struct{}
	UnavailableChannels map[int]struct{}
	TryOtherKeys        bool
	SelectionStrategy   string
}

// SelectSamePriorityChannel selects without replacement at one priority. The
// returned priority count lets the caller decide when to descend or exhaust.
func SelectSamePriorityChannel(group, modelName, requestPath string, options RetryChannelSelectionOptions) (*Channel, int, error) {
	channels, err := retryCandidateChannels(group, modelName, requestPath)
	if err != nil {
		return nil, 0, err
	}
	if len(channels) == 0 {
		return nil, 0, nil
	}

	priorities := retryChannelPriorities(channels)
	if options.PriorityIndex < 0 || options.PriorityIndex >= len(priorities) {
		return nil, len(priorities), nil
	}

	targetPriority := priorities[options.PriorityIndex]
	eligible := make([]*Channel, 0)
	for _, channel := range channels {
		if channel.GetPriority() != targetPriority {
			continue
		}
		if _, unavailable := options.UnavailableChannels[channel.Id]; unavailable {
			continue
		}
		if options.TryOtherKeys {
			if !channel.HasEnabledKeyExcluding(options.TriedKeys[channel.Id]) {
				continue
			}
		} else {
			if _, tried := options.TriedChannels[channel.Id]; tried {
				continue
			}
			if !channel.HasEnabledKeyExcluding(nil) {
				continue
			}
		}
		eligible = append(eligible, channel)
	}
	if len(eligible) == 0 {
		return nil, len(priorities), nil
	}
	return chooseSamePriorityChannel(eligible, modelName, options.SelectionStrategy), len(priorities), nil
}

func RetryChannelPriorityIndex(group, modelName, requestPath string, priority int64) (int, int, error) {
	channels, err := retryCandidateChannels(group, modelName, requestPath)
	if err != nil {
		return 0, 0, err
	}
	priorities := retryChannelPriorities(channels)
	for index, candidatePriority := range priorities {
		if candidatePriority == priority {
			return index, len(priorities), nil
		}
	}
	return -1, len(priorities), nil
}

func retryChannelPriorities(channels []*Channel) []int64 {
	prioritySet := make(map[int64]struct{})
	for _, channel := range channels {
		prioritySet[channel.GetPriority()] = struct{}{}
	}
	priorities := make([]int64, 0, len(prioritySet))
	for priority := range prioritySet {
		priorities = append(priorities, priority)
	}
	sort.Slice(priorities, func(i, j int) bool { return priorities[i] > priorities[j] })
	return priorities
}

func retryCandidateChannels(group, modelName, requestPath string) ([]*Channel, error) {
	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		defer channelSyncLock.RUnlock()
		ids := filterChannelsByRequestPathAndModel(group2model2channels[group][modelName], requestPath, modelName)
		if len(ids) == 0 {
			normalized := ratio_setting.FormatMatchingModelName(modelName)
			ids = filterChannelsByRequestPathAndModel(group2model2channels[group][normalized], requestPath, modelName)
		}
		channels := make([]*Channel, 0, len(ids))
		for _, id := range ids {
			channel, ok := channelsIDM[id]
			if !ok {
				return nil, fmt.Errorf("channel #%d is missing from cache", id)
			}
			channels = append(channels, channel)
		}
		return channels, nil
	}

	abilities, err := retryAbilities(group, modelName)
	if err != nil {
		return nil, err
	}
	if len(abilities) == 0 {
		normalized := ratio_setting.FormatMatchingModelName(modelName)
		if normalized != modelName {
			abilities, err = retryAbilities(group, normalized)
			if err != nil {
				return nil, err
			}
		}
	}
	abilities = filterAbilitiesByRequestPathAndModel(abilities, requestPath, modelName)
	if len(abilities) == 0 {
		return nil, nil
	}
	ids := make([]int, 0, len(abilities))
	for _, ability := range abilities {
		ids = append(ids, ability.ChannelId)
	}
	var channels []Channel
	if err := DB.Where("id IN ? AND status = ?", ids, common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return nil, err
	}
	byID := make(map[int]*Channel, len(channels))
	for i := range channels {
		byID[channels[i].Id] = &channels[i]
	}
	result := make([]*Channel, 0, len(abilities))
	for _, ability := range abilities {
		channel, ok := byID[ability.ChannelId]
		if !ok {
			continue
		}
		priority := int64(0)
		if ability.Priority != nil {
			priority = *ability.Priority
		}
		weight := uint(ability.Weight)
		channel.Priority = &priority
		channel.Weight = &weight
		result = append(result, channel)
	}
	return result, nil
}

func retryAbilities(group, modelName string) ([]Ability, error) {
	var abilities []Ability
	err := DB.Where(commonGroupCol+" = ? AND model = ? AND enabled = ?", group, modelName, true).Find(&abilities).Error
	return abilities, err
}

func weightedRetryChannel(channels []*Channel) *Channel {
	if len(channels) == 1 {
		return channels[0]
	}
	sum := 0
	for _, channel := range channels {
		weight := channel.GetWeight()
		if weight > 0 {
			sum += weight
		}
	}
	if sum == 0 {
		return channels[rand.Intn(len(channels))]
	}
	pick := rand.Intn(sum)
	for _, channel := range channels {
		weight := channel.GetWeight()
		if weight <= 0 {
			continue
		}
		if pick < weight {
			return channel
		}
		pick -= weight
	}
	return channels[len(channels)-1]
}
