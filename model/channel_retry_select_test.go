package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSelectSamePriorityChannelTriesUnusedCandidatesBeforeDescending(t *testing.T) {
	previousDB := DB
	previousCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	t.Cleanup(func() {
		DB = previousDB
		common.MemoryCacheEnabled = previousCache
		if DB != nil {
			InitChannelCache()
		}
	})

	priorityHigh, priorityLow := int64(100), int64(10)
	weight := uint(100)
	channels := []Channel{
		{Id: 1, Name: "a", Key: "key-a", Group: "default", Models: "model", Status: common.ChannelStatusEnabled, Priority: &priorityHigh, Weight: &weight},
		{Id: 2, Name: "b", Key: "key-b", Group: "default", Models: "model", Status: common.ChannelStatusEnabled, Priority: &priorityHigh, Weight: &weight},
		{Id: 3, Name: "c", Key: "key-c", Group: "default", Models: "model", Status: common.ChannelStatusEnabled, Priority: &priorityLow, Weight: &weight},
	}
	require.NoError(t, db.Create(&channels).Error)
	abilities := []Ability{
		{Group: "default", Model: "model", ChannelId: 1, Enabled: true, Priority: &priorityHigh, Weight: weight},
		{Group: "default", Model: "model", ChannelId: 2, Enabled: true, Priority: &priorityHigh, Weight: weight},
		{Group: "default", Model: "model", ChannelId: 3, Enabled: true, Priority: &priorityLow, Weight: weight},
	}
	require.NoError(t, db.Create(&abilities).Error)

	first, priorityCount, err := SelectSamePriorityChannel("default", "model", "", RetryChannelSelectionOptions{PriorityIndex: 0})
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, 2, priorityCount)
	tried := map[int]struct{}{first.Id: {}}
	second, _, err := SelectSamePriorityChannel("default", "model", "", RetryChannelSelectionOptions{PriorityIndex: 0, TriedChannels: tried})
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.NotEqual(t, first.Id, second.Id)
	tried[second.Id] = struct{}{}
	exhaustedHigh, _, err := SelectSamePriorityChannel("default", "model", "", RetryChannelSelectionOptions{PriorityIndex: 0, TriedChannels: tried})
	require.NoError(t, err)
	assert.Nil(t, exhaustedHigh)
	lower, _, err := SelectSamePriorityChannel("default", "model", "", RetryChannelSelectionOptions{PriorityIndex: 1, TriedChannels: tried})
	require.NoError(t, err)
	require.NotNil(t, lower)
	assert.Equal(t, 3, lower.Id)

	common.MemoryCacheEnabled = true
	InitChannelCache()
	cachedFirst, cachedPriorityCount, err := SelectSamePriorityChannel("default", "model", "", RetryChannelSelectionOptions{PriorityIndex: 0})
	require.NoError(t, err)
	require.NotNil(t, cachedFirst)
	assert.Equal(t, priorityCount, cachedPriorityCount)
	cachedSecond, _, err := SelectSamePriorityChannel("default", "model", "", RetryChannelSelectionOptions{
		PriorityIndex: 0,
		TriedChannels: map[int]struct{}{
			cachedFirst.Id: {},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, cachedSecond)
	assert.NotEqual(t, cachedFirst.Id, cachedSecond.Id)
}

func TestSelectSamePriorityChannelSkipsDisabledChannelWithoutCache(t *testing.T) {
	previousDB := DB
	previousCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	t.Cleanup(func() {
		DB = previousDB
		common.MemoryCacheEnabled = previousCache
		if DB != nil {
			InitChannelCache()
		}
	})

	priority := int64(100)
	weight := uint(100)
	require.NoError(t, db.Create(&Channel{Id: 1, Name: "disabled", Key: "key", Status: common.ChannelStatusManuallyDisabled, Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, db.Create(&Ability{Group: "default", Model: "model", ChannelId: 1, Enabled: true, Priority: &priority, Weight: weight}).Error)

	channel, priorityCount, err := SelectSamePriorityChannel("default", "model", "", RetryChannelSelectionOptions{})
	require.NoError(t, err)
	assert.Nil(t, channel)
	assert.Zero(t, priorityCount)
}

func TestSelectSamePriorityChannelCanTryOtherEnabledKeys(t *testing.T) {
	channel := &Channel{
		Id:  1,
		Key: "key-a\nkey-b",
		ChannelInfo: ChannelInfo{
			IsMultiKey:         true,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled, 1: common.ChannelStatusEnabled},
		},
	}
	assert.True(t, channel.HasEnabledKeyExcluding(map[int]struct{}{0: {}}))
	assert.False(t, channel.HasEnabledKeyExcluding(map[int]struct{}{0: {}, 1: {}}))
	key, index, err := channel.GetNextEnabledKeyExcluding(map[int]struct{}{0: {}})
	require.Nil(t, err)
	assert.Equal(t, "key-b", key)
	assert.Equal(t, 1, index)
}
