package service

import (
	"errors"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

type RetryParam struct {
	Ctx                  *gin.Context
	TokenGroup           string
	ModelName            string
	RequestPath          string
	Retry                *int
	Setting              operation_setting.RetrySetting
	StartedAt            time.Time
	TriedChannels        map[int]struct{}
	TriedKeys            map[int]map[int]struct{}
	UnavailableChannels  map[int]struct{}
	PriorityIndexByGroup map[string]int
	Trace                *RetryTrace
	AutoGroupIndex       int
	SelectedAutoGroup    string
	CandidateExhausted   bool
}

func (p *RetryParam) GetRetry() int {
	if p.Retry == nil {
		return 0
	}
	return *p.Retry
}

func (p *RetryParam) SetRetry(retry int) {
	p.Retry = &retry
}

func (p *RetryParam) IncreaseRetry() {
	if p.Retry == nil {
		p.Retry = new(int)
	}
	if *p.Retry < math.MaxInt {
		*p.Retry++
	}
}

// UsesAdaptiveSamePrioritySelection reports whether persisted channel outcome
// metrics participate in this request's routing decisions. Legacy and weighted
// random selection do not consume these metrics and should not pay their write
// cost on every upstream attempt.
func (p *RetryParam) UsesAdaptiveSamePrioritySelection() bool {
	if p == nil {
		return false
	}
	if p.Setting.ChannelStrategy != operation_setting.RetryChannelSamePriority {
		return false
	}
	return p.Setting.SamePriorityStrategy == operation_setting.SamePriorityStabilityFirst ||
		p.Setting.SamePriorityStrategy == operation_setting.SamePriorityLatencyFirst
}

func CacheGetRandomSatisfiedChannel(param *RetryParam) (*model.Channel, string, error) {
	param.ensurePolicy()
	param.CandidateExhausted = false
	if param.TokenGroup != "auto" {
		if param.Setting.ChannelStrategy == operation_setting.RetryChannelLegacy && !param.Setting.TryOtherKeys {
			channel, err := model.GetRandomSatisfiedChannel(param.TokenGroup, param.ModelName, param.GetRetry(), param.RequestPath)
			param.CandidateExhausted = err == nil && channel == nil
			return channel, param.TokenGroup, err
		}
		if param.Setting.ChannelStrategy == operation_setting.RetryChannelLegacy {
			channel, err := param.selectLegacyWithKeys(param.TokenGroup)
			param.CandidateExhausted = err == nil && channel == nil
			return channel, param.TokenGroup, err
		}
		channel, exhausted, err := param.selectSamePriorityInGroup(param.TokenGroup)
		if exhausted && param.Setting.ExhaustedAction == operation_setting.RetryExhaustedCycle {
			param.ResetCandidateRound()
			channel, exhausted, err = param.selectSamePriorityInGroup(param.TokenGroup)
		}
		param.CandidateExhausted = exhausted && err == nil
		return channel, param.TokenGroup, err
	}

	if len(setting.GetAutoGroups()) == 0 {
		return nil, param.TokenGroup, errors.New("auto groups is not enabled")
	}
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)
	autoGroups := GetUserAutoGroup(userGroup)
	if len(autoGroups) == 0 {
		return nil, param.TokenGroup, errors.New("no auto groups available for user")
	}
	crossGroupRetry := common.GetContextKeyBool(param.Ctx, constant.ContextKeyTokenCrossGroupRetry)
	if !crossGroupRetry {
		channel, group, exhausted, err := param.selectAutoWithoutCrossGroup(autoGroups)
		if exhausted && param.Setting.ChannelStrategy == operation_setting.RetryChannelSamePriority && param.Setting.ExhaustedAction == operation_setting.RetryExhaustedCycle {
			param.ResetCandidateRound()
			channel, group, exhausted, err = param.selectAutoWithoutCrossGroup(autoGroups)
		}
		param.CandidateExhausted = exhausted && err == nil
		return channel, group, err
	}

	var channel *model.Channel
	var exhausted bool
	var err error
	if param.Setting.ChannelStrategy == operation_setting.RetryChannelLegacy {
		channel, exhausted, err = param.selectLegacyAcrossAutoGroups(autoGroups)
	} else {
		channel, exhausted, err = param.selectSamePriorityFromGroups(autoGroups)
		if exhausted && param.Setting.ExhaustedAction == operation_setting.RetryExhaustedCycle {
			param.ResetCandidateRound()
			channel, exhausted, err = param.selectSamePriorityFromGroups(autoGroups)
		}
	}
	param.CandidateExhausted = exhausted && err == nil
	selectGroup := param.TokenGroup
	if param.AutoGroupIndex >= 0 && param.AutoGroupIndex < len(autoGroups) {
		selectGroup = autoGroups[param.AutoGroupIndex]
	}
	return channel, selectGroup, err
}

func (p *RetryParam) selectAutoWithoutCrossGroup(groups []string) (*model.Channel, string, bool, error) {
	start, end := 0, len(groups)
	if p.SelectedAutoGroup != "" {
		for index, group := range groups {
			if group == p.SelectedAutoGroup {
				start, end = index, index+1
				break
			}
		}
	}
	for index := start; index < end; index++ {
		group := groups[index]
		if p.Setting.ChannelStrategy == operation_setting.RetryChannelLegacy {
			var channel *model.Channel
			var err error
			if p.Setting.TryOtherKeys {
				channel, err = p.selectLegacyWithKeys(group)
			} else {
				channel, err = model.GetRandomSatisfiedChannel(group, p.ModelName, p.GetRetry(), p.RequestPath)
			}
			if err != nil {
				return nil, group, false, err
			}
			if channel != nil {
				p.setSelectedAutoGroup(group, index)
				return channel, group, false, nil
			}
			continue
		}
		channel, exhausted, err := p.selectSamePriorityInGroup(group)
		if err != nil {
			return nil, group, false, err
		}
		if channel != nil {
			p.setSelectedAutoGroup(group, index)
			return channel, group, false, nil
		}
		if !exhausted {
			return nil, group, false, nil
		}
	}
	group := p.TokenGroup
	if p.SelectedAutoGroup != "" {
		group = p.SelectedAutoGroup
	}
	return nil, group, true, nil
}

func (p *RetryParam) selectSamePriorityInGroup(group string) (*model.Channel, bool, error) {
	for {
		priorityIndex := p.PriorityIndexByGroup[group]
		channel, priorityCount, err := model.SelectSamePriorityChannel(group, p.ModelName, p.RequestPath, model.RetryChannelSelectionOptions{
			PriorityIndex:       priorityIndex,
			TriedChannels:       p.TriedChannels,
			TriedKeys:           p.TriedKeys,
			UnavailableChannels: p.UnavailableChannels,
			TryOtherKeys:        p.Setting.TryOtherKeys,
			SelectionStrategy:   p.Setting.SamePriorityStrategy,
		})
		if err != nil {
			return nil, false, err
		}
		if channel != nil {
			return channel, false, nil
		}
		if priorityIndex+1 >= priorityCount {
			return nil, true, nil
		}
		p.PriorityIndexByGroup[group] = priorityIndex + 1
	}
}

func (p *RetryParam) selectSamePriorityFromGroups(groups []string) (*model.Channel, bool, error) {
	for groupIndex := p.AutoGroupIndex; groupIndex < len(groups); groupIndex++ {
		group := groups[groupIndex]
		channel, exhausted, err := p.selectSamePriorityInGroup(group)
		if err != nil {
			return nil, false, err
		}
		if channel != nil {
			p.setSelectedAutoGroup(group, groupIndex)
			return channel, false, nil
		}
		if !exhausted {
			return nil, false, nil
		}
		p.AutoGroupIndex = groupIndex + 1
	}
	return nil, true, nil
}

func (p *RetryParam) selectLegacyAcrossAutoGroups(groups []string) (*model.Channel, bool, error) {
	for groupIndex := p.AutoGroupIndex; groupIndex < len(groups); groupIndex++ {
		group := groups[groupIndex]
		for {
			priorityIndex := p.PriorityIndexByGroup[group]
			options := model.RetryChannelSelectionOptions{PriorityIndex: priorityIndex}
			if p.Setting.TryOtherKeys {
				options.TriedKeys = p.TriedKeys
				options.UnavailableChannels = p.UnavailableChannels
				options.TryOtherKeys = true
			}
			channel, priorityCount, err := model.SelectSamePriorityChannel(group, p.ModelName, p.RequestPath, options)
			if err != nil {
				return nil, false, err
			}
			if channel != nil {
				p.PriorityIndexByGroup[group] = priorityIndex + 1
				p.setSelectedAutoGroup(group, groupIndex)
				return channel, false, nil
			}
			if priorityIndex+1 >= priorityCount {
				break
			}
			p.PriorityIndexByGroup[group] = priorityIndex + 1
		}
		p.AutoGroupIndex = groupIndex + 1
	}
	return nil, true, nil
}

func (p *RetryParam) selectLegacyWithKeys(group string) (*model.Channel, error) {
	priorityIndex := p.GetRetry()
	for {
		channel, priorityCount, err := model.SelectSamePriorityChannel(group, p.ModelName, p.RequestPath, model.RetryChannelSelectionOptions{
			PriorityIndex:       priorityIndex,
			TriedKeys:           p.TriedKeys,
			UnavailableChannels: p.UnavailableChannels,
			TryOtherKeys:        true,
		})
		if err != nil || channel != nil || priorityCount == 0 {
			return channel, err
		}
		if priorityIndex >= priorityCount {
			priorityIndex = priorityCount - 1
			continue
		}
		if priorityIndex+1 >= priorityCount {
			return nil, nil
		}
		priorityIndex++
	}
}

func (p *RetryParam) setSelectedAutoGroup(group string, index int) {
	p.AutoGroupIndex = index
	p.SelectedAutoGroup = group
	if p.Ctx != nil {
		common.SetContextKey(p.Ctx, constant.ContextKeyAutoGroup, group)
		common.SetContextKey(p.Ctx, constant.ContextKeyAutoGroupIndex, index)
	}
}

func (p *RetryParam) ExcludedKeyIndexes(channelID int) map[int]struct{} {
	p.ensurePolicy()
	if !p.Setting.TryOtherKeys {
		return nil
	}
	return p.TriedKeys[channelID]
}

func (p *RetryParam) RecordSelection(channelID, keyIndex int, isMultiKey bool) {
	p.ensurePolicy()
	selectedGroup := p.TokenGroup
	if p.TokenGroup == "auto" && p.GetRetry() == 0 && p.Ctx != nil {
		selectedGroup = common.GetContextKeyString(p.Ctx, constant.ContextKeyAutoGroup)
		if selectedGroup != "" {
			groups := GetUserAutoGroup(common.GetContextKeyString(p.Ctx, constant.ContextKeyUserGroup))
			for index, group := range groups {
				if group == selectedGroup {
					p.AutoGroupIndex = index
					p.SelectedAutoGroup = group
					break
				}
			}
		}
	}
	if p.GetRetry() == 0 && selectedGroup != "" && p.Ctx != nil {
		if priority, ok := common.GetContextKeyType[int64](p.Ctx, constant.ContextKeyChannelPriority); ok {
			priorityIndex, _, err := model.RetryChannelPriorityIndex(selectedGroup, p.ModelName, p.RequestPath, priority)
			if err == nil && priorityIndex >= 0 {
				if p.Setting.ChannelStrategy == operation_setting.RetryChannelSamePriority {
					p.PriorityIndexByGroup[selectedGroup] = priorityIndex
				} else if p.TokenGroup == "auto" && common.GetContextKeyBool(p.Ctx, constant.ContextKeyTokenCrossGroupRetry) {
					p.PriorityIndexByGroup[selectedGroup] = priorityIndex + 1
				}
			}
		}
	}
	if p.Setting.TryOtherKeys {
		if p.TriedKeys[channelID] == nil {
			p.TriedKeys[channelID] = make(map[int]struct{})
		}
		if !isMultiKey {
			keyIndex = 0
		}
		p.TriedKeys[channelID][keyIndex] = struct{}{}
		if p.Setting.ChannelStrategy != operation_setting.RetryChannelSamePriority {
			return
		}
		return
	}
	if p.Setting.ChannelStrategy != operation_setting.RetryChannelSamePriority {
		return
	}
	p.TriedChannels[channelID] = struct{}{}
}

func (p *RetryParam) MarkChannelUnavailable(channel *model.Channel) {
	p.ensurePolicy()
	if channel == nil {
		return
	}
	p.TriedChannels[channel.Id] = struct{}{}
	p.UnavailableChannels[channel.Id] = struct{}{}
	if p.Setting.TryOtherKeys {
		indexes := make(map[int]struct{})
		for index := range channel.GetKeys() {
			indexes[index] = struct{}{}
		}
		p.TriedKeys[channel.Id] = indexes
	}
}

func (p *RetryParam) MarkKeyUnavailable(channelID, keyIndex int) {
	p.ensurePolicy()
	if keyIndex < 0 {
		return
	}
	if p.TriedKeys[channelID] == nil {
		p.TriedKeys[channelID] = make(map[int]struct{})
	}
	p.TriedKeys[channelID][keyIndex] = struct{}{}
}

func (p *RetryParam) ResetCandidateRound() {
	p.ensurePolicy()
	p.TriedChannels = make(map[int]struct{})
	p.TriedKeys = make(map[int]map[int]struct{})
	p.PriorityIndexByGroup = make(map[string]int)
	p.AutoGroupIndex = 0
	if p.TokenGroup == "auto" && p.SelectedAutoGroup != "" && p.Ctx != nil && !common.GetContextKeyBool(p.Ctx, constant.ContextKeyTokenCrossGroupRetry) {
		groups := GetUserAutoGroup(common.GetContextKeyString(p.Ctx, constant.ContextKeyUserGroup))
		for index, group := range groups {
			if group == p.SelectedAutoGroup {
				p.AutoGroupIndex = index
				break
			}
		}
	}
}
