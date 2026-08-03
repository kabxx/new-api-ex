package model

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateOptionsBulkValidatesAvailabilityRecipientsBeforePersisting(t *testing.T) {
	previousDB := DB
	previousOptionMap := common.OptionMap
	previousSetting := operation_setting.GetMonitorSettingSnapshot()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	common.OptionMap = make(map[string]string)
	require.NoError(t, db.AutoMigrate(&Option{}, &Channel{}, &ChannelAvailabilityState{}, &ChannelAvailabilityNotificationEvent{}))
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"channel_availability_notify_enabled":    "false",
		"channel_availability_notify_recipients": `[]`,
	}))
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousOptionMap
		recipients, _ := common.Marshal(previousSetting.ChannelAvailabilityNotifyRecipients)
		config.GlobalConfig.Update("monitor_setting", map[string]string{
			"channel_availability_notify_enabled":    common.Interface2String(previousSetting.ChannelAvailabilityNotifyEnabled),
			"channel_availability_notify_recipients": string(recipients),
		})
	})

	err = UpdateOption("monitor_setting.channel_availability_notify_recipients", `["invalid"]`)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&Option{}).Count(&count).Error)
	assert.Zero(t, count)
	err = UpdateOption("monitor_setting.channel_availability_notify_enabled", "true")
	require.Error(t, err)
	require.NoError(t, db.Model(&Option{}).Count(&count).Error)
	assert.Zero(t, count)

	err = UpdateOptionsBulk(map[string]string{
		"monitor_setting.channel_availability_notify_enabled":    "true",
		"monitor_setting.channel_availability_notify_recipients": `["invalid"]`,
	})
	require.Error(t, err)
	require.NoError(t, db.Model(&Option{}).Count(&count).Error)
	assert.Zero(t, count)
	require.Error(t, UpdateOptionsBulk(map[string]string{
		"monitor_setting.channel_availability_notify_enabled":    "true",
		"monitor_setting.channel_availability_notify_recipients": `[]`,
	}))
	require.NoError(t, db.Model(&Option{}).Count(&count).Error)
	assert.Zero(t, count)

	require.NoError(t, UpdateOptionsBulk(map[string]string{
		"monitor_setting.channel_availability_notify_enabled":    "true",
		"monitor_setting.channel_availability_notify_recipients": `["admin@example.com"]`,
	}))
	setting := operation_setting.GetMonitorSettingSnapshot()
	assert.True(t, setting.ChannelAvailabilityNotifyEnabled)
	assert.Equal(t, []string{"admin@example.com"}, setting.ChannelAvailabilityNotifyRecipients)
	var state ChannelAvailabilityState
	require.NoError(t, db.First(&state).Error)

	require.NoError(t, db.Create(&Channel{Name: "primary", Status: common.ChannelStatusEnabled}).Error)
	require.NoError(t, UpdateOptionsBulk(map[string]string{
		"monitor_setting.channel_availability_notify_enabled": "true",
	}))
	transition, err := ClaimChannelAvailabilityTransition()
	require.NoError(t, err)
	require.NotNil(t, transition, "saving an already-enabled option must not silently consume a new availability edge")
	assert.True(t, transition.ToAvailable)

	require.NoError(t, UpdateOptionsBulk(map[string]string{
		"monitor_setting.channel_availability_notify_enabled": "false",
	}))
	assert.False(t, operation_setting.GetMonitorSettingSnapshot().ChannelAvailabilityNotifyEnabled)
	var event ChannelAvailabilityNotificationEvent
	require.NoError(t, db.First(&event).Error)
	assert.Equal(t, ChannelAvailabilityEventCancelled, event.Status)
	claimed, err := ClaimNextChannelAvailabilityNotificationEvent("owner", 100)
	require.NoError(t, err)
	assert.Nil(t, claimed)
}

func TestUpdateOptionsBulkRollsBackEnableWhenBaselineFails(t *testing.T) {
	previousDB := DB
	previousOptionMap := common.OptionMap
	previousSetting := operation_setting.GetMonitorSettingSnapshot()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	common.OptionMap = map[string]string{
		"monitor_setting.channel_availability_notify_enabled":    "false",
		"monitor_setting.channel_availability_notify_recipients": `[]`,
	}
	require.NoError(t, db.AutoMigrate(&Option{}, &Channel{}))
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"channel_availability_notify_enabled":    "false",
		"channel_availability_notify_recipients": `[]`,
	}))
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousOptionMap
		recipients, _ := common.Marshal(previousSetting.ChannelAvailabilityNotifyRecipients)
		config.GlobalConfig.Update("monitor_setting", map[string]string{
			"channel_availability_notify_enabled":    common.Interface2String(previousSetting.ChannelAvailabilityNotifyEnabled),
			"channel_availability_notify_recipients": string(recipients),
		})
	})

	err = UpdateOptionsBulk(map[string]string{
		"monitor_setting.channel_availability_notify_enabled":    "true",
		"monitor_setting.channel_availability_notify_recipients": `["admin@example.com"]`,
	})
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&Option{}).Count(&count).Error)
	assert.Zero(t, count)
	assert.False(t, operation_setting.GetMonitorSettingSnapshot().ChannelAvailabilityNotifyEnabled)
}

func TestUpdateOptionsBulkPublishesMonitorSnapshotAtomically(t *testing.T) {
	previousDB := DB
	previousOptionMap := common.OptionMap
	previousSetting := operation_setting.GetMonitorSettingSnapshot()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	common.OptionMap = make(map[string]string)
	require.NoError(t, db.AutoMigrate(&Option{}, &Channel{}, &ChannelAvailabilityState{}, &ChannelAvailabilityNotificationEvent{}))
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"channel_availability_notify_enabled":    "false",
		"channel_availability_notify_recipients": `[]`,
	}))
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousOptionMap
		recipients, _ := common.Marshal(previousSetting.ChannelAvailabilityNotifyRecipients)
		config.GlobalConfig.Update("monitor_setting", map[string]string{
			"channel_availability_notify_enabled":    common.Interface2String(previousSetting.ChannelAvailabilityNotifyEnabled),
			"channel_availability_notify_recipients": string(recipients),
		})
	})

	done := make(chan struct{})
	invalidSnapshot := make(chan struct{}, 1)
	go func() {
		defer close(done)
		for i := 0; i < 40; i++ {
			setting := operation_setting.GetMonitorSettingSnapshot()
			if setting.ChannelAvailabilityNotifyEnabled && len(setting.ChannelAvailabilityNotifyRecipients) == 0 {
				invalidSnapshot <- struct{}{}
				return
			}
		}
	}()

	require.NoError(t, UpdateOptionsBulk(map[string]string{
		"monitor_setting.channel_availability_notify_enabled":    "true",
		"monitor_setting.channel_availability_notify_recipients": `["admin@example.com"]`,
	}))
	<-done
	select {
	case <-invalidSnapshot:
		t.Fatal("reader observed enabled notifications without recipients")
	default:
	}
}

func TestRoutingReliabilityBulkValidatesBeforeCommitAndPublishesCompletePayload(t *testing.T) {
	previousDB := DB
	previousMonitor := operation_setting.GetMonitorSettingSnapshot()
	previousRetry := operation_setting.GetRetrySetting()
	previousRetryTimes := common.RetryTimes
	previousThreshold := common.ChannelDisableThreshold
	previousDisableEnabled := common.AutomaticDisableChannelEnabled
	previousEnableEnabled := common.AutomaticEnableChannelEnabled
	previousTolerance := common.AutoDisableTolerance
	previousKeywords := append([]string(nil), operation_setting.AutomaticDisableKeywords...)
	previousDisableRanges := append([]operation_setting.StatusCodeRange(nil), operation_setting.AutomaticDisableStatusCodeRanges...)
	previousRetryRanges := append([]operation_setting.StatusCodeRange(nil), operation_setting.AutomaticRetryStatusCodeRanges...)
	common.OptionMapRWMutex.Lock()
	previousOptionMap := common.OptionMap
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
	common.RetryTimes = 0

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&Option{}, &Channel{}, &ChannelAvailabilityState{}, &ChannelAvailabilityNotificationEvent{}))
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"auto_test_channel_enabled":              "false",
		"auto_test_channel_minutes":              "10",
		"channel_test_mode":                      operation_setting.ChannelTestModeScheduledAll,
		"zero_token_as_failure":                  "false",
		"channel_availability_notify_enabled":    "false",
		"channel_availability_notify_recipients": `[]`,
	}))
	t.Cleanup(func() {
		DB = previousDB
		common.RetryTimes = previousRetryTimes
		common.ChannelDisableThreshold = previousThreshold
		common.AutomaticDisableChannelEnabled = previousDisableEnabled
		common.AutomaticEnableChannelEnabled = previousEnableEnabled
		common.AutoDisableTolerance = previousTolerance
		operation_setting.AutomaticDisableKeywords = previousKeywords
		operation_setting.AutomaticDisableStatusCodeRanges = previousDisableRanges
		operation_setting.AutomaticRetryStatusCodeRanges = previousRetryRanges
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
		monitorRecipients, _ := common.Marshal(previousMonitor.ChannelAvailabilityNotifyRecipients)
		config.GlobalConfig.Update("monitor_setting", map[string]string{
			"auto_test_channel_enabled":              strconv.FormatBool(previousMonitor.AutoTestChannelEnabled),
			"auto_test_channel_minutes":              strconv.FormatFloat(previousMonitor.AutoTestChannelMinutes, 'f', -1, 64),
			"channel_test_mode":                      previousMonitor.ChannelTestMode,
			"zero_token_as_failure":                  strconv.FormatBool(previousMonitor.ZeroTokenAsFailure),
			"channel_availability_notify_enabled":    strconv.FormatBool(previousMonitor.ChannelAvailabilityNotifyEnabled),
			"channel_availability_notify_recipients": string(monitorRecipients),
		})
		config.GlobalConfig.Update("retry_setting", map[string]string{
			"unlimited":                           strconv.FormatBool(previousRetry.Unlimited),
			"time_budget_seconds":                 strconv.FormatInt(previousRetry.TimeBudgetSeconds, 10),
			"delay_strategy":                      previousRetry.DelayStrategy,
			"fixed_delay_milliseconds":            strconv.FormatInt(previousRetry.FixedDelayMilliseconds, 10),
			"exponential_base_delay_milliseconds": strconv.FormatInt(previousRetry.ExponentialBaseDelayMilliseconds, 10),
			"exponential_max_delay_milliseconds":  strconv.FormatInt(previousRetry.ExponentialMaximumDelayMilliseconds, 10),
			"jitter_percent":                      strconv.FormatFloat(previousRetry.JitterPercent, 'f', -1, 64),
			"respect_retry_after":                 strconv.FormatBool(previousRetry.RespectRetryAfter),
			"channel_strategy":                    previousRetry.ChannelStrategy,
			"exhausted_action":                    previousRetry.ExhaustedAction,
			"try_other_keys":                      strconv.FormatBool(previousRetry.TryOtherKeys),
			"unlimited_task_retries":              strconv.FormatBool(previousRetry.UnlimitedTaskRetries),
		})
	})

	invalidCases := []map[string]string{
		{"RetryTimes": "17", "AutomaticDisableStatusCodes": "99"},
		{"RetryTimes": "17", "ChannelDisableThreshold": "NaN"},
		{"RetryTimes": "17", "monitor_setting.auto_test_channel_minutes": "Inf"},
	}
	for _, values := range invalidCases {
		require.Error(t, UpdateRoutingReliabilityOptionsBulk(values))
		var count int64
		require.NoError(t, db.Model(&Option{}).Count(&count).Error)
		assert.Zero(t, count)
		assert.Zero(t, common.RetryTimes)
	}

	values := map[string]string{
		"RetryTimes":                                             "17",
		"ChannelDisableThreshold":                                "0.75",
		"AutomaticDisableChannelEnabled":                         "true",
		"AutoDisableTolerance":                                   "3",
		"AutomaticEnableChannelEnabled":                          "true",
		"AutomaticDisableKeywords":                               "Quota exhausted\nPermission denied",
		"AutomaticDisableStatusCodes":                            "401,429,500-599",
		"AutomaticRetryStatusCodes":                              "429,500-503",
		"retry_setting.unlimited":                                "true",
		"retry_setting.time_budget_seconds":                      "30",
		"retry_setting.delay_strategy":                           operation_setting.RetryDelayExponential,
		"retry_setting.fixed_delay_milliseconds":                 "15",
		"retry_setting.exponential_base_delay_milliseconds":      "250",
		"retry_setting.exponential_max_delay_milliseconds":       "5000",
		"retry_setting.jitter_percent":                           "12.5",
		"retry_setting.respect_retry_after":                      "true",
		"retry_setting.channel_strategy":                         operation_setting.RetryChannelSamePriority,
		"retry_setting.exhausted_action":                         operation_setting.RetryExhaustedCycle,
		"retry_setting.try_other_keys":                           "true",
		"retry_setting.unlimited_task_retries":                   "true",
		"monitor_setting.auto_test_channel_enabled":              "true",
		"monitor_setting.auto_test_channel_minutes":              "5",
		"monitor_setting.channel_test_mode":                      operation_setting.ChannelTestModePassiveRecovery,
		"monitor_setting.zero_token_as_failure":                  "true",
		"monitor_setting.channel_availability_notify_enabled":    "true",
		"monitor_setting.channel_availability_notify_recipients": `["Admin@Example.com","admin@example.com"]`,
	}
	require.Len(t, values, 26)
	require.NoError(t, UpdateRoutingReliabilityOptionsBulk(values))

	var count int64
	require.NoError(t, db.Model(&Option{}).Count(&count).Error)
	assert.Equal(t, int64(26), count)
	assert.Equal(t, 17, common.RetryTimes)
	assert.Equal(t, 0.75, common.ChannelDisableThreshold)
	assert.True(t, common.AutomaticDisableChannelEnabled)
	assert.True(t, common.AutomaticEnableChannelEnabled)
	assert.Equal(t, 3, common.AutoDisableTolerance)
	assert.Equal(t, []string{"quota exhausted", "permission denied"}, operation_setting.AutomaticDisableKeywords)
	assert.Equal(t, []operation_setting.StatusCodeRange{{Start: 401, End: 401}, {Start: 429, End: 429}, {Start: 500, End: 599}}, operation_setting.AutomaticDisableStatusCodeRanges)

	retry := operation_setting.GetRetrySetting()
	assert.True(t, retry.Unlimited)
	assert.Equal(t, int64(30), retry.TimeBudgetSeconds)
	assert.Equal(t, operation_setting.RetryDelayExponential, retry.DelayStrategy)
	assert.Equal(t, operation_setting.RetryChannelSamePriority, retry.ChannelStrategy)
	assert.Equal(t, operation_setting.RetryExhaustedCycle, retry.ExhaustedAction)
	assert.True(t, retry.TryOtherKeys)
	monitor := operation_setting.GetMonitorSettingSnapshot()
	assert.True(t, monitor.AutoTestChannelEnabled)
	assert.Equal(t, float64(5), monitor.AutoTestChannelMinutes)
	assert.Equal(t, operation_setting.ChannelTestModePassiveRecovery, monitor.ChannelTestMode)
	assert.True(t, monitor.ZeroTokenAsFailure)
	assert.True(t, monitor.ChannelAvailabilityNotifyEnabled)
	assert.Equal(t, []string{"Admin@Example.com"}, monitor.ChannelAvailabilityNotifyRecipients)

	var recipientsOption Option
	require.NoError(t, db.First(&recipientsOption, "key = ?", "monitor_setting.channel_availability_notify_recipients").Error)
	assert.JSONEq(t, `["Admin@Example.com"]`, recipientsOption.Value)
}
