package model

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func selectionTestChannels() []*Channel {
	zero, full := uint(0), uint(100)
	return []*Channel{
		{Id: 1, Weight: &zero},
		{Id: 2, Weight: &full},
	}
}

func TestSamePrioritySelectionStrategiesAndWarmup(t *testing.T) {
	previousDB := DB
	DB = nil
	ResetChannelSelectionMetrics()
	t.Cleanup(func() {
		DB = previousDB
		channelSelectionMetrics.Lock()
		channelSelectionMetrics.values = make(map[channelSelectionMetricKey]*channelSelectionMetric)
		channelSelectionMetrics.Unlock()
	})
	channels := selectionTestChannels()

	assert.Equal(t, 2, chooseSamePriorityChannel(channels, "model", operation_setting.SamePriorityWeightedRandom).Id)
	for range channelSelectionMetricWarmup {
		RecordChannelSelectionOutcome(1, "model", true, 100)
	}
	assert.Equal(t, 2, chooseSamePriorityChannel(channels, "model", operation_setting.SamePriorityStabilityFirst).Id, "an unknown channel receives bounded weighted warmup")
	for range channelSelectionMetricWarmup {
		RecordChannelSelectionOutcome(2, "model", false, 0)
	}
	assert.Equal(t, 1, chooseSamePriorityChannel(channels, "model", operation_setting.SamePriorityStabilityFirst).Id)

	ResetChannelSelectionMetrics()
	for range channelSelectionMetricWarmup {
		RecordChannelSelectionOutcome(1, "model", true, 100)
		RecordChannelSelectionOutcome(2, "model", false, 0)
	}
	assert.Equal(t, 1, chooseSamePriorityChannel(channels, "model", operation_setting.SamePriorityLatencyFirst).Id, "failed warmup without TTFT is ready and ranks worst")
}

func TestLatencyNearTieFallsBackToWeightAndPersists(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&ChannelSelectionMetricState{}))
	ResetChannelSelectionMetrics()
	t.Cleanup(func() {
		DB = previousDB
		channelSelectionMetrics.Lock()
		channelSelectionMetrics.values = make(map[channelSelectionMetricKey]*channelSelectionMetric)
		channelSelectionMetrics.Unlock()
	})
	channels := selectionTestChannels()
	for range channelSelectionMetricWarmup {
		RecordChannelSelectionOutcome(1, "model", true, 100)
		RecordChannelSelectionOutcome(2, "model", true, 120)
	}

	channelSelectionMetrics.Lock()
	channelSelectionMetrics.values = make(map[channelSelectionMetricKey]*channelSelectionMetric)
	channelSelectionMetrics.Unlock()
	selected := chooseSamePriorityChannel(channels, "model", operation_setting.SamePriorityLatencyFirst)
	require.NotNil(t, selected)
	assert.Equal(t, 2, selected.Id, "near-equal persisted TTFT candidates retain weighted distribution")

	var rows []ChannelSelectionMetricState
	require.NoError(t, DB.Order("channel_id").Find(&rows).Error)
	require.Len(t, rows, 2)
	assert.NotEmpty(t, rows[0].Observations)
}

func TestLatencyFirstDoesNotPreferPreviouslyFastChannelAfterRecentFailure(t *testing.T) {
	previousDB := DB
	DB = nil
	ResetChannelSelectionMetrics()
	t.Cleanup(func() {
		DB = previousDB
		channelSelectionMetrics.Lock()
		channelSelectionMetrics.values = make(map[channelSelectionMetricKey]*channelSelectionMetric)
		channelSelectionMetrics.Unlock()
	})

	channels := selectionTestChannels()
	for range channelSelectionMetricWarmup {
		RecordChannelSelectionOutcome(1, "model", true, 100)
		RecordChannelSelectionOutcome(2, "model", true, 120)
	}
	RecordChannelSelectionOutcome(1, "model", false, 0)

	selected := chooseSamePriorityChannel(channels, "model", operation_setting.SamePriorityLatencyFirst)
	require.NotNil(t, selected)
	assert.Equal(t, 2, selected.Id)
}

func TestResetChannelSelectionMetricsForChannelClearsPersistentAndMemoryState(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&ChannelSelectionMetricState{}))
	ResetChannelSelectionMetrics()
	t.Cleanup(func() {
		DB = previousDB
		channelSelectionMetrics.Lock()
		channelSelectionMetrics.values = make(map[channelSelectionMetricKey]*channelSelectionMetric)
		channelSelectionMetrics.Unlock()
	})

	RecordChannelSelectionOutcome(1, "model", true, 100)
	RecordChannelSelectionOutcome(2, "model", true, 200)
	require.NoError(t, ResetChannelSelectionMetricsForChannel(1))

	var removedCount, retainedCount int64
	require.NoError(t, db.Model(&ChannelSelectionMetricState{}).Where("channel_id = ?", 1).Count(&removedCount).Error)
	require.NoError(t, db.Model(&ChannelSelectionMetricState{}).Where("channel_id = ?", 2).Count(&retainedCount).Error)
	assert.Zero(t, removedCount)
	assert.Equal(t, int64(1), retainedCount)
	channelSelectionMetrics.RLock()
	_, removedInMemory := channelSelectionMetrics.values[channelSelectionMetricKey{channelID: 1, model: "model"}]
	_, retainedInMemory := channelSelectionMetrics.values[channelSelectionMetricKey{channelID: 2, model: "model"}]
	channelSelectionMetrics.RUnlock()
	assert.False(t, removedInMemory)
	assert.True(t, retainedInMemory)
}
