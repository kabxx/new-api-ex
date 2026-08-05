package service

import (
	"path/filepath"
	"strconv"
	"strings"
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
	databasePath := filepath.Join(t.TempDir(), "channel-failure.db") + "?_busy_timeout=30000"
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(16)
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.ChannelFailureState{}))
	require.True(t, config.GlobalConfig.Update("monitor_setting", values))
	t.Cleanup(func() {
		_ = sqlDB.Close()
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

func recordFailure(t *testing.T, channelID int, usingKey string, threshold int) bool {
	t.Helper()
	claimed, err := RecordChannelFailureWithError(channelID, usingKey, threshold)
	require.NoError(t, err)
	return claimed
}

func claimFailure(t *testing.T, channelID int, usingKey string, threshold int) string {
	t.Helper()
	claimToken, err := ClaimChannelFailureWithError(channelID, usingKey, threshold)
	require.NoError(t, err)
	return claimToken
}

func TestConsecutivePolicyClaimsExactlyAtConfiguredFailure(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyConsecutive,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "20",
		"auto_disable_rate_min_samples":       "10",
		"auto_disable_rate_threshold_percent": "60",
	})
	const channelID = 91001
	const usingKey = "key-a"

	assert.False(t, recordFailure(t, channelID, usingKey, 3))
	assert.False(t, recordFailure(t, channelID, usingKey, 3))
	claimToken := claimFailure(t, channelID, usingKey, 3)
	require.NotEmpty(t, claimToken)
	assert.False(t, recordFailure(t, channelID, usingKey, 3), "a claimed threshold must not queue duplicate disables")

	require.NoError(t, CompleteChannelAutoDisable(channelID, usingKey, claimToken, false))
	claimToken = claimFailure(t, channelID, usingKey, 3)
	require.NotEmpty(t, claimToken, "a failed state change must release the retained threshold")
	require.NoError(t, CompleteChannelAutoDisable(channelID, usingKey, claimToken, true))
	assert.False(t, recordFailure(t, channelID, usingKey, 3), "a completed disable starts with clean evidence after recovery")
}

func TestBusinessSuccessAfterFailedDisableClearsThreshold(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyConsecutive,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "20",
		"auto_disable_rate_min_samples":       "10",
		"auto_disable_rate_threshold_percent": "60",
	})
	const channelID = 91002
	const usingKey = "key-success"
	claimToken := claimFailure(t, channelID, usingKey, 1)
	require.NotEmpty(t, claimToken)
	require.NoError(t, CompleteChannelAutoDisable(channelID, usingKey, claimToken, false))
	require.NoError(t, RecordChannelSuccessWithError(channelID, usingKey))
	assert.True(t, recordFailure(t, channelID, usingKey, 1), "a later failure starts a new consecutive sequence")
}

func TestZeroToleranceClaimsFirstFailure(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyConsecutive,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "20",
		"auto_disable_rate_min_samples":       "10",
		"auto_disable_rate_threshold_percent": "60",
	})
	assert.True(t, recordFailure(t, 91000, "key-zero", 0))
}

func TestWindowPolicyExpiresOldFailuresAndKeepsEvidenceUntilCompletion(t *testing.T) {
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

	assert.False(t, recordFailure(t, channelID, usingKey, 0))
	now = now.Add(time.Minute)
	assert.False(t, recordFailure(t, channelID, usingKey, 0))
	now = now.Add(6 * time.Minute)
	assert.False(t, recordFailure(t, channelID, usingKey, 0), "expired failures must not count")
	now = now.Add(time.Minute)
	assert.False(t, recordFailure(t, channelID, usingKey, 0))
	now = now.Add(time.Minute)
	assert.True(t, recordFailure(t, channelID, usingKey, 0))

	var retained model.ChannelFailureState
	require.NoError(t, model.DB.Where("channel_id = ?", channelID).First(&retained).Error)
	assert.True(t, retained.ThresholdReached)
	assert.True(t, retained.Claimed)
	assert.Len(t, retained.KeyHash, 64)
	assert.NotContains(t, retained.KeyHash, usingKey)
	assert.NotContains(t, retained.WindowFailures, usingKey)
	assert.Less(t, len(retained.WindowFailures), 64)
}

func TestFailureRateUsesBusinessSuccessesSymmetricallyAcrossStrategyChange(t *testing.T) {
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

	assert.False(t, recordFailure(t, channelID, usingKey, 99))
	require.NoError(t, RecordChannelSuccessWithError(channelID, usingKey))
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"auto_disable_strategy": operation_setting.AutoDisableStrategyFailureRate,
	}))
	assert.False(t, recordFailure(t, channelID, usingKey, 99))
	assert.True(t, recordFailure(t, channelID, usingKey, 99), "three failures and one success reach 75 percent")

	var persisted model.ChannelFailureState
	require.NoError(t, model.DB.Where("channel_id = ?", channelID).First(&persisted).Error)
	assert.Equal(t, 4, persisted.RateCount)
	assert.LessOrEqual(t, len(persisted.RateSamples), 1700)
}

func TestRecoveryResetDoesNotAppendFailureRateSample(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyFailureRate,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "4",
		"auto_disable_rate_min_samples":       "4",
		"auto_disable_rate_threshold_percent": "75",
	})
	const channelID = 92003
	const usingKey = "health-key"
	assert.False(t, recordFailure(t, channelID, usingKey, 99))
	require.NoError(t, ResetChannelFailCountWithError(channelID, usingKey))
	var count int64
	require.NoError(t, model.DB.Model(&model.ChannelFailureState{}).Where("channel_id = ?", channelID).Count(&count).Error)
	assert.Zero(t, count)
}

func TestFailureRateSuccessCannotCreateAutoDisableClaim(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyFailureRate,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "2",
		"auto_disable_rate_min_samples":       "2",
		"auto_disable_rate_threshold_percent": "50",
	})
	const channelID = 92005
	const usingKey = "success-cannot-claim"

	assert.False(t, recordFailure(t, channelID, usingKey, 99))
	require.NoError(t, RecordChannelSuccessWithError(channelID, usingKey))
	state, err := model.LoadPersistentChannelFailureState(channelID, channelFailureKey(channelID, usingKey).keyHash)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.False(t, state.ThresholdReached)
	assert.False(t, state.Claimed)
}

func TestChangingFailurePolicyReevaluatesHistoryWithoutReusingClaim(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyConsecutive,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "3",
		"auto_disable_rate_sample_size":       "20",
		"auto_disable_rate_min_samples":       "10",
		"auto_disable_rate_threshold_percent": "60",
	})
	const channelID = 92006
	const usingKey = "policy-change"

	claimToken := claimFailure(t, channelID, usingKey, 1)
	require.NotEmpty(t, claimToken)
	require.NoError(t, CompleteChannelAutoDisable(channelID, usingKey, claimToken, false))
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"auto_disable_strategy": operation_setting.AutoDisableStrategyWindow,
	}))

	assert.False(t, recordFailure(t, channelID, usingKey, 1), "the old consecutive claim must not survive a policy change")
	state, err := model.LoadPersistentChannelFailureState(channelID, channelFailureKey(channelID, usingKey).keyHash)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.False(t, state.ThresholdReached)
	assert.False(t, state.Claimed)
}

func TestLegacyObservationRowsMigrateWithoutLosingHistory(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyFailureRate,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "4",
		"auto_disable_rate_min_samples":       "4",
		"auto_disable_rate_threshold_percent": "75",
	})
	const channelID = 92004
	const usingKey = "legacy-secret"
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	channelFailureNow = func() time.Time { return now }
	legacyJSON, err := common.Marshal([]model.ChannelFailureObservation{
		{At: now.Add(-3 * time.Minute).Unix(), Failed: true},
		{At: now.Add(-2 * time.Minute).Unix(), Failed: false},
		{At: now.Add(-time.Minute).Unix(), Failed: true},
	})
	require.NoError(t, err)
	key := channelFailureKey(channelID, usingKey)
	require.NoError(t, model.DB.Create(&model.ChannelFailureState{
		ChannelID:    channelID,
		KeyHash:      key.keyHash,
		Consecutive:  1,
		Observations: string(legacyJSON),
	}).Error)

	assert.True(t, recordFailure(t, channelID, usingKey, 99), "legacy history plus the new failure reaches 75 percent")
	var migrated model.ChannelFailureState
	require.NoError(t, model.DB.Where("channel_id = ?", channelID).First(&migrated).Error)
	assert.Empty(t, migrated.Observations)
	assert.Equal(t, 4, migrated.RateCount)
	assert.NotContains(t, migrated.RateSamples, usingKey)
}

func TestConcurrentFirstObservationsProduceOneClaimWithoutLosingFailures(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyConsecutive,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "20",
		"auto_disable_rate_min_samples":       "10",
		"auto_disable_rate_threshold_percent": "60",
	})
	const channelID = 93001
	const usingKey = "concurrent-secret"
	const failures = 8

	results := make(chan bool, failures)
	errorsChannel := make(chan error, failures)
	var waitGroup sync.WaitGroup
	waitGroup.Add(failures)
	for range failures {
		go func() {
			defer waitGroup.Done()
			claimed, err := RecordChannelFailureWithError(channelID, usingKey, failures)
			results <- claimed
			errorsChannel <- err
		}()
	}
	waitGroup.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		require.NoError(t, err)
	}
	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	assert.Equal(t, 1, claims)

	key := channelFailureKey(channelID, usingKey)
	state, err := model.LoadPersistentChannelFailureState(channelID, key.keyHash)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, failures, state.Consecutive)
	assert.True(t, state.ThresholdReached)
	assert.False(t, strings.Contains(state.RateSamples, usingKey))
}

func TestExpiredPersistentClaimRejectsStaleOwnerCompletion(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyConsecutive,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "20",
		"auto_disable_rate_min_samples":       "10",
		"auto_disable_rate_threshold_percent": "60",
	})
	const channelID = 94001
	const usingKey = "claim-owner-secret"
	now := time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC)
	channelFailureNow = func() time.Time { return now }

	staleToken := claimFailure(t, channelID, usingKey, 1)
	require.NotEmpty(t, staleToken)
	now = now.Add(3 * time.Minute)
	currentToken := claimFailure(t, channelID, usingKey, 1)
	require.NotEmpty(t, currentToken)
	assert.NotEqual(t, staleToken, currentToken)

	require.NoError(t, CompleteChannelAutoDisable(channelID, usingKey, staleToken, false))
	require.NoError(t, CompleteChannelAutoDisable(channelID, usingKey, staleToken, true))
	key := channelFailureKey(channelID, usingKey)
	state, err := model.LoadPersistentChannelFailureState(channelID, key.keyHash)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.Claimed)
	assert.Equal(t, currentToken, state.ClaimToken)

	require.NoError(t, CompleteChannelAutoDisable(channelID, usingKey, currentToken, true))
	state, err = model.LoadPersistentChannelFailureState(channelID, key.keyHash)
	require.NoError(t, err)
	assert.Nil(t, state)
}

func TestBusinessSuccessCannotCancelActivePersistentClaim(t *testing.T) {
	setupPersistentChannelToleranceTest(t, map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyConsecutive,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "20",
		"auto_disable_rate_min_samples":       "10",
		"auto_disable_rate_threshold_percent": "60",
	})
	const channelID = 94003
	const usingKey = "active-claim-secret"
	claimToken := claimFailure(t, channelID, usingKey, 1)
	require.NotEmpty(t, claimToken)

	require.NoError(t, RecordChannelSuccessWithError(channelID, usingKey))
	key := channelFailureKey(channelID, usingKey)
	state, err := model.LoadPersistentChannelFailureState(channelID, key.keyHash)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.Claimed)
	assert.Equal(t, claimToken, state.ClaimToken)

	require.NoError(t, CompleteChannelAutoDisable(channelID, usingKey, claimToken, true))
	state, err = model.LoadPersistentChannelFailureState(channelID, key.keyHash)
	require.NoError(t, err)
	assert.Nil(t, state)
}

func TestExpiredMemoryClaimRejectsStaleOwnerCompletion(t *testing.T) {
	previousDB := model.DB
	previousClock := channelFailureNow
	model.DB = nil
	channelFailCounts.Lock()
	channelFailCounts.counts = make(map[channelFailCountKey]*channelFailureState)
	channelFailCounts.Unlock()
	t.Cleanup(func() {
		model.DB = previousDB
		channelFailureNow = previousClock
		channelFailCounts.Lock()
		channelFailCounts.counts = make(map[channelFailCountKey]*channelFailureState)
		channelFailCounts.Unlock()
	})

	const channelID = 94002
	const usingKey = "memory-claim-secret"
	now := time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC)
	channelFailureNow = func() time.Time { return now }
	staleToken := claimFailure(t, channelID, usingKey, 1)
	require.NotEmpty(t, staleToken)
	now = now.Add(3 * time.Minute)
	currentToken := claimFailure(t, channelID, usingKey, 1)
	require.NotEmpty(t, currentToken)
	assert.NotEqual(t, staleToken, currentToken)

	require.NoError(t, CompleteChannelAutoDisable(channelID, usingKey, staleToken, false))
	require.NoError(t, CompleteChannelAutoDisable(channelID, usingKey, staleToken, true))
	key := channelFailureKey(channelID, usingKey)
	channelFailCounts.Lock()
	state := channelFailCounts.counts[key]
	channelFailCounts.Unlock()
	require.NotNil(t, state)
	assert.True(t, state.Claimed)
	assert.Equal(t, currentToken, state.ClaimToken)
}
