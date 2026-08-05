package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutomaticChannelTestTargetsRecoverDisabledMultiKeys(t *testing.T) {
	enabledWithDisabledKey := &model.Channel{
		Id: 1, Key: "one\ntwo", Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{1: common.ChannelStatusAutoDisabled}},
	}
	allKeysDisabled := &model.Channel{
		Id: 2, Key: "one\ntwo", Status: common.ChannelStatusAutoDisabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{0: common.ChannelStatusAutoDisabled, 1: common.ChannelStatusAutoDisabled}},
	}
	wholeChannelDisabled := &model.Channel{
		Id: 3, Key: "one\ntwo", Status: common.ChannelStatusAutoDisabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2},
	}
	manuallyDisabledKeys := &model.Channel{
		Id: 4, Key: "one\ntwo", Status: common.ChannelStatusAutoDisabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{
			0: common.ChannelStatusManuallyDisabled,
			1: common.ChannelStatusManuallyDisabled,
		}},
	}
	manual := &model.Channel{Id: 5, Key: "key", Status: common.ChannelStatusManuallyDisabled}

	targets := automaticChannelTestTargets([]*model.Channel{enabledWithDisabledKey, allKeysDisabled, wholeChannelDisabled, manuallyDisabledKeys, manual})
	require.Len(t, targets, 5)
	assert.Nil(t, targets[0].forcedKeyIndex)
	require.NotNil(t, targets[1].forcedKeyIndex)
	assert.Equal(t, 1, *targets[1].forcedKeyIndex)
	require.NotNil(t, targets[2].forcedKeyIndex)
	assert.Equal(t, 0, *targets[2].forcedKeyIndex)
	require.NotNil(t, targets[3].forcedKeyIndex)
	assert.Equal(t, 1, *targets[3].forcedKeyIndex)
	assert.True(t, targets[4].recoveryUsesWholeKey)
	assert.Nil(t, targets[4].forcedKeyIndex)
	for _, target := range targets {
		assert.NotEqual(t, manuallyDisabledKeys.Id, target.channel.Id)
	}
}
