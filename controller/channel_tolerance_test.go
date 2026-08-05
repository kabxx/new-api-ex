package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProcessChannelErrorRecordsToleratedFailure(t *testing.T) {
	const channelId = 92001
	const usingKey = "key-a"

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousErrorLogEnabled := constant.ErrorLogEnabled
	previousAutomaticDisableEnabled := common.AutomaticDisableChannelEnabled
	previousTolerance := common.AutoDisableTolerance
	previousRedisEnabled := common.RedisEnabled
	previousMonitorSetting := operation_setting.GetMonitorSettingSnapshot()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	model.DB = db
	model.LOG_DB = db
	constant.ErrorLogEnabled = true
	common.AutomaticDisableChannelEnabled = true
	common.AutoDisableTolerance = 5
	common.RedisEnabled = false
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyConsecutive,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "20",
		"auto_disable_rate_min_samples":       "10",
		"auto_disable_rate_threshold_percent": "60",
	}))

	t.Cleanup(func() {
		service.ResetChannelFailCount(channelId, usingKey)
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		constant.ErrorLogEnabled = previousErrorLogEnabled
		common.AutomaticDisableChannelEnabled = previousAutomaticDisableEnabled
		common.AutoDisableTolerance = previousTolerance
		common.RedisEnabled = previousRedisEnabled
		config.GlobalConfig.Update("monitor_setting", map[string]string{
			"auto_disable_strategy":               previousMonitorSetting.AutoDisableStrategy,
			"auto_disable_window_minutes":         strconv.Itoa(previousMonitorSetting.AutoDisableWindowMinutes),
			"auto_disable_window_failures":        strconv.Itoa(previousMonitorSetting.AutoDisableWindowFailures),
			"auto_disable_rate_sample_size":       strconv.Itoa(previousMonitorSetting.AutoDisableRateSampleSize),
			"auto_disable_rate_min_samples":       strconv.Itoa(previousMonitorSetting.AutoDisableRateMinSamples),
			"auto_disable_rate_threshold_percent": strconv.FormatFloat(previousMonitorSetting.AutoDisableRateThresholdPercent, 'f', -1, 64),
		})
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			_ = sqlDB.Close()
		}
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("channel_id", channelId)
	ctx.Set(channelAvailabilityDeferredContextKey, true)
	errorToProcess := types.NewOpenAIError(errors.New("temporary upstream failure"), types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout)
	channelError := *types.NewChannelError(channelId, 1, "test channel", false, usingKey, true)

	for attempt := 1; attempt < 5; attempt++ {
		assert.False(t, processChannelError(ctx, channelError, errorToProcess), "attempt %d must remain below the configured threshold", attempt)
	}
	assert.True(t, processChannelError(ctx, channelError, errorToProcess), "the fifth real upstream failure must claim the configured threshold")
	var errorLogs []model.Log
	require.NoError(t, db.Where("type = ?", model.LogTypeError).Find(&errorLogs).Error)
	require.Len(t, errorLogs, 5)
	assert.Equal(t, channelId, errorLogs[0].ChannelId)
	assert.Contains(t, errorLogs[0].Content, "temporary upstream failure")
}

func TestProcessChannelErrorCountsHTTP524IndependentlyOfDisableStatusCodes(t *testing.T) {
	const channelID = 92003
	const usingKey = "key-524"
	previousDB := model.DB
	previousAutomaticDisableEnabled := common.AutomaticDisableChannelEnabled
	previousTolerance := common.AutoDisableTolerance
	previousErrorLogEnabled := constant.ErrorLogEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	common.AutomaticDisableChannelEnabled = true
	common.AutoDisableTolerance = 2
	constant.ErrorLogEnabled = false
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Set(channelAvailabilityDeferredContextKey, true)
	channelError := *types.NewChannelError(channelID, 1, "http 524", false, usingKey, true)
	upstreamError := types.NewOpenAIError(errors.New("upstream timeout"), types.ErrorCodeBadResponseStatusCode, 524)
	t.Cleanup(func() {
		service.ResetChannelFailCount(channelID, usingKey)
		model.DB = previousDB
		common.AutomaticDisableChannelEnabled = previousAutomaticDisableEnabled
		common.AutoDisableTolerance = previousTolerance
		constant.ErrorLogEnabled = previousErrorLogEnabled
	})

	assert.False(t, processChannelError(ctx, channelError, upstreamError))
	assert.True(t, processChannelError(ctx, channelError, upstreamError), "HTTP 524 must count even when retry/disable status-code filters omit it")
}

func TestAttributableFailureClassificationExcludesClientAndLocalErrors(t *testing.T) {
	assert.True(t, service.IsAttributableChannelFailure(context.Background(), types.NewOpenAIError(errors.New("timeout"), types.ErrorCodeBadResponseStatusCode, 524)))
	assert.True(t, service.IsAttributableChannelFailure(context.Background(), types.NewOpenAIError(errors.New("transport"), types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)))
	assert.False(t, service.IsAttributableChannelFailure(context.Background(), types.NewError(errors.New("convert"), types.ErrorCodeConvertRequestFailed)))
	assert.False(t, service.IsAttributableChannelFailure(context.Background(), types.NewError(errors.New("quota"), types.ErrorCodePreConsumeTokenQuotaFailed)))
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	assert.False(t, service.IsAttributableChannelFailure(cancelledContext, types.NewOpenAIError(errors.New("cancelled"), types.ErrorCodeDoRequestFailed, 499)))
}

func TestHealthCheckFailureUsesDirectDisableWithoutBusinessFailureSample(t *testing.T) {
	previousDB := model.DB
	previousAutomaticDisableEnabled := common.AutomaticDisableChannelEnabled
	previousErrorLogEnabled := constant.ErrorLogEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.ChannelFailureState{}))
	model.DB = db
	common.AutomaticDisableChannelEnabled = true
	constant.ErrorLogEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.AutomaticDisableChannelEnabled = previousAutomaticDisableEnabled
		constant.ErrorLogEnabled = previousErrorLogEnabled
	})

	channel := model.Channel{
		Name:    "health-check",
		Key:     "key-one\nkey-two",
		Status:  common.ChannelStatusEnabled,
		AutoBan: common.GetPointer(1),
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
		},
	}
	require.NoError(t, db.Create(&channel).Error)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/test", nil)
	ctx.Set(channelAvailabilityDeferredContextKey, true)
	channelError := *types.NewChannelError(channel.Id, 1, channel.Name, true, "key-one", true)
	upstreamError := types.NewOpenAIError(errors.New("health probe failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	assert.True(t, processHealthCheckChannelError(ctx, channelError, upstreamError))
	stored, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
	var sampleCount int64
	require.NoError(t, db.Model(&model.ChannelFailureState{}).Where("channel_id = ?", channel.Id).Count(&sampleCount).Error)
	assert.Zero(t, sampleCount)
}

func TestFinalizeSuccessfulRelayAttemptDoesNotResetZeroTokenFailure(t *testing.T) {
	const channelId = 92002
	const usingKey = "key-zero-token"

	previousErrorLogEnabled := constant.ErrorLogEnabled
	previousAutomaticDisableEnabled := common.AutomaticDisableChannelEnabled
	previousTolerance := common.AutoDisableTolerance
	previousMonitorSetting := operation_setting.GetMonitorSettingSnapshot()
	constant.ErrorLogEnabled = false
	common.AutomaticDisableChannelEnabled = true
	common.AutoDisableTolerance = 2
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"auto_disable_strategy":               operation_setting.AutoDisableStrategyConsecutive,
		"auto_disable_window_minutes":         "10",
		"auto_disable_window_failures":        "5",
		"auto_disable_rate_sample_size":       "20",
		"auto_disable_rate_min_samples":       "10",
		"auto_disable_rate_threshold_percent": "60",
	}))

	t.Cleanup(func() {
		service.ResetChannelFailCount(channelId, usingKey)
		constant.ErrorLogEnabled = previousErrorLogEnabled
		common.AutomaticDisableChannelEnabled = previousAutomaticDisableEnabled
		common.AutoDisableTolerance = previousTolerance
		config.GlobalConfig.Update("monitor_setting", map[string]string{
			"auto_disable_strategy":               previousMonitorSetting.AutoDisableStrategy,
			"auto_disable_window_minutes":         strconv.Itoa(previousMonitorSetting.AutoDisableWindowMinutes),
			"auto_disable_window_failures":        strconv.Itoa(previousMonitorSetting.AutoDisableWindowFailures),
			"auto_disable_rate_sample_size":       strconv.Itoa(previousMonitorSetting.AutoDisableRateSampleSize),
			"auto_disable_rate_min_samples":       strconv.Itoa(previousMonitorSetting.AutoDisableRateMinSamples),
			"auto_disable_rate_threshold_percent": strconv.FormatFloat(previousMonitorSetting.AutoDisableRateThresholdPercent, 'f', -1, 64),
		})
	})

	assert.False(t, service.RecordChannelFailure(channelId, usingKey, common.AutoDisableTolerance))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Set(channelAvailabilityDeferredContextKey, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, usingKey)
	common.SetContextKey(ctx, constant.ContextKeyZeroTokenFailure, true)
	channel := &model.Channel{
		Id:      channelId,
		Type:    1,
		Name:    "zero token channel",
		AutoBan: common.GetPointer(1),
	}

	finalizeSuccessfulRelayAttempt(ctx, channel)

	assert.Empty(t, recorder.Body.String())
	assert.True(t, service.RecordChannelFailure(channelId, usingKey, common.AutoDisableTolerance))
}
