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

func TestRetryOptionValidationPrecedesBulkPersistence(t *testing.T) {
	previousDB := DB
	previousRetryTimes := common.RetryTimes
	common.OptionMapRWMutex.Lock()
	previousOptionMap := common.OptionMap
	common.OptionMap = map[string]string{"RetryTimes": "10"}
	common.OptionMapRWMutex.Unlock()
	common.RetryTimes = 10

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&Option{}))
	t.Cleanup(func() {
		DB = previousDB
		common.RetryTimes = previousRetryTimes
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	for _, value := range []string{"1.5", "invalid", "999999999999999999999999"} {
		assert.Error(t, UpdateOption("RetryTimes", value))
	}
	assert.Error(t, UpdateOption("RetryTimes", "-2"))
	assert.Error(t, UpdateOption("retry_setting.unlimited", "true"))
	assert.Error(t, UpdateOption("retry_setting.unlimited_task_retries", "true"))
	require.NoError(t, UpdateOption("RetryTimes", "-1"))
	assert.Equal(t, -1, common.RetryTimes)
	require.NoError(t, UpdateOption("RetryTimes", "10000"))
	assert.Equal(t, 10000, common.RetryTimes)

	err = UpdateOptionsBulk(map[string]string{
		"retry_setting.time_budget_seconds": "-1",
		"unrelated":                         "must-not-persist",
	})
	assert.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&Option{}).Where("key = ?", "unrelated").Count(&count).Error)
	assert.Zero(t, count)

	maxInt := int64(^uint(0) >> 1)
	assert.NoError(t, validateOptionValue("RetryTimes", strconv.FormatInt(maxInt, 10)))
}

func TestRetrySettingsBulkUpdatePublishesCompleteSnapshot(t *testing.T) {
	previousDB := DB
	previous := operation_setting.GetRetrySetting()
	common.OptionMapRWMutex.Lock()
	previousOptionMap := common.OptionMap
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&Option{}))
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
		config.GlobalConfig.Update("retry_setting", map[string]string{
			"time_budget_seconds":                 strconv.FormatInt(previous.TimeBudgetSeconds, 10),
			"exponential_base_delay_milliseconds": strconv.FormatInt(previous.ExponentialBaseDelayMilliseconds, 10),
			"exponential_max_delay_milliseconds":  strconv.FormatInt(previous.ExponentialMaximumDelayMilliseconds, 10),
		})
	})

	require.NoError(t, UpdateOptionsBulk(map[string]string{
		"retry_setting.time_budget_seconds":                 "17",
		"retry_setting.exponential_base_delay_milliseconds": "41",
		"retry_setting.exponential_max_delay_milliseconds":  "83",
	}))
	snapshot := operation_setting.GetRetrySetting()
	assert.Equal(t, int64(17), snapshot.TimeBudgetSeconds)
	assert.Equal(t, int64(41), snapshot.ExponentialBaseDelayMilliseconds)
	assert.Equal(t, int64(83), snapshot.ExponentialMaximumDelayMilliseconds)
}
