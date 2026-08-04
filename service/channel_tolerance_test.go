package service

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupPersistentChannelToleranceTest(t *testing.T, values map[string]string) {
	t.Helper()
	previousDB := model.DB
	previousDatabaseType := common.MainDatabaseType()
	previousSetting := operation_setting.GetMonitorSettingSnapshot()
	previousClock := channelFailureNow
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.ChannelFailureState{}))
	require.True(t, config.GlobalConfig.Update("monitor_setting", values))
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousDatabaseType)
		channelFailureNow = previousClock
		config.GlobalConfig.Update("monitor_setting", map[string]string{
			"auto_disable_strategy":               previousSetting.AutoDisableStrategy,
			"auto_disable_window_minutes":         strconv.Itoa(previousSetting.AutoDisableWindowMinutes),
			"auto_disable_window_failures":        strconv.Itoa(previousSetting.AutoDisableWindowFailures),
			"auto_disable_rate_sample_size":       strconv.Itoa(previousSetting.AutoDisableRateSampleSize),
			"auto_disable_rate_min_samples":       strconv.Itoa(previousSetting.AutoDisableRateMinSamples),
			"auto_disable_rate_threshold_percent": strconv.FormatFloat(previousSetting.AutoDisableRateThresholdPercent, 'f', -1, 64),
		})
	})
}

func TestRecordChannelFailureDisablesImmediatelyAtZeroTolerance(t *testing.T) {
	const channelId = 91000
	const usingKey = "key-a"
	t.Cleanup(func() { ResetChannelFailCount(channelId, usingKey) })

	assert.True(t, RecordChannelFailure(channelId, usingKey, 0))
	assert.True(t, RecordChannelFailure(channelId, usingKey, 0))
}

func TestRecordChannelFailureHonorsToleranceAndResets(t *testing.T) {
	const channelId = 91001
	const usingKey = "key-a"
	t.Cleanup(func() { ResetChannelFailCount(channelId, usingKey) })

	assert.False(t, RecordChannelFailure(channelId, usingKey, 2))
	assert.False(t, RecordChannelFailure(channelId, usingKey, 2))
	assert.True(t, RecordChannelFailure(channelId, usingKey, 2))
	assert.False(t, RecordChannelFailure(channelId, usingKey, 2))

	ResetChannelFailCount(channelId, usingKey)
	assert.False(t, RecordChannelFailure(channelId, usingKey, 2))
}

func TestRecordChannelFailureTracksKeysIndependently(t *testing.T) {
	const channelId = 91002
	const firstKey = "key-a"
	const secondKey = "key-b"
	t.Cleanup(func() {
		ResetChannelFailCount(channelId, firstKey)
		ResetChannelFailCount(channelId, secondKey)
	})

	assert.False(t, RecordChannelFailure(channelId, firstKey, 1))
	assert.False(t, RecordChannelFailure(channelId, secondKey, 1))
	assert.True(t, RecordChannelFailure(channelId, firstKey, 1))
	assert.True(t, RecordChannelFailure(channelId, secondKey, 1))
}

func TestRecordChannelFailureCrossesThresholdOnceConcurrently(t *testing.T) {
	const channelId = 91003
	const usingKey = "key-a"
	const failures = 8
	t.Cleanup(func() { ResetChannelFailCount(channelId, usingKey) })

	results := make(chan bool, failures)
	var waitGroup sync.WaitGroup
	waitGroup.Add(failures)
	for range failures {
		go func() {
			defer waitGroup.Done()
			results <- RecordChannelFailure(channelId, usingKey, failures-1)
		}()
	}
	waitGroup.Wait()
	close(results)

	thresholdCrossings := 0
	for crossed := range results {
		if crossed {
			thresholdCrossings++
		}
	}
	assert.Equal(t, 1, thresholdCrossings)
	assert.False(t, RecordChannelFailure(channelId, usingKey, failures-1))
}

func TestWindowFailurePolicyExpiresOldFailuresAndPersistsAcrossMemoryReset(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyWindow,
		"auto_disable_window_minutes":         "5",
		"auto_disable_window_failures":        "3",
		"auto_disable_rate_sample_size":       "20",
		"auto_disable_rate_min_samples":       "10",
		"auto_disable_rate_threshold_percent": "60",
	})
	const channelID = 92001
	const usingKey = "secret-key"
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	channelFailureNow = func() time.Time { return now }

	assert.False(t, RecordChannelFailure(channelID, usingKey, 0))
	now = now.Add(time.Minute)
	assert.False(t, RecordChannelFailure(channelID, usingKey, 0))
	var retained model.ChannelFailureState
	require.NoError(t, model.DB.Where("channel_id = ?", channelID).First(&retained).Error)
	assert.Len(t, retained.KeyHash, 64)
	assert.NotContains(t, retained.KeyHash, usingKey)
	assert.NotContains(t, retained.Observations, usingKey)

	channelFailCounts.Lock()
	channelFailCounts.counts = make(map[channelFailCountKey]*channelFailureState)
	channelFailCounts.Unlock()
	now = now.Add(6 * time.Minute)
	assert.False(t, RecordChannelFailure(channelID, usingKey, 0), "expired failures must not count")
	now = now.Add(time.Minute)
	assert.False(t, RecordChannelFailure(channelID, usingKey, 0))
	now = now.Add(time.Minute)
	assert.True(t, RecordChannelFailure(channelID, usingKey, 0))

	var rows []model.ChannelFailureState
	require.NoError(t, model.DB.Find(&rows).Error)
	assert.Empty(t, rows, "crossing the threshold consumes the retained state")
}

func TestFailureRatePolicyUsesMinimumSampleAndRetainedHistory(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyConsecutive,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "4",
		"auto_disable_rate_min_samples":       "4",
		"auto_disable_rate_threshold_percent": "75",
	})
	const channelID = 92002
	const usingKey = "MixedCase-Secret"
	now := time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)
	channelFailureNow = func() time.Time { return now }

	assert.False(t, RecordChannelFailure(channelID, usingKey, 99))
	RecordChannelSuccess(channelID, usingKey)
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"auto_disable_strategy": operation_setting.AutoDisableStrategyFailureRate,
	}))
	assert.False(t, RecordChannelFailure(channelID, usingKey, 99), "the rate policy waits for its minimum sample size")
	assert.True(t, RecordChannelFailure(channelID, usingKey, 99), "the retained four-sample window reaches 75 percent")

	var persisted model.ChannelFailureState
	err := model.DB.Where("channel_id = ?", channelID).First(&persisted).Error
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
