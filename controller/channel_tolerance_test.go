package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
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

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	model.DB = db
	model.LOG_DB = db
	constant.ErrorLogEnabled = true
	common.AutomaticDisableChannelEnabled = true
	common.AutoDisableTolerance = 1
	common.RedisEnabled = false

	t.Cleanup(func() {
		service.ResetChannelFailCount(channelId, usingKey)
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		constant.ErrorLogEnabled = previousErrorLogEnabled
		common.AutomaticDisableChannelEnabled = previousAutomaticDisableEnabled
		common.AutoDisableTolerance = previousTolerance
		common.RedisEnabled = previousRedisEnabled
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			_ = sqlDB.Close()
		}
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("channel_id", channelId)
	errorToProcess := types.NewError(errors.New("temporary upstream failure"), types.ErrorCodeChannelNoAvailableKey)
	channelError := *types.NewChannelError(channelId, 1, "test channel", false, usingKey, true)

	disabled := processChannelError(ctx, channelError, errorToProcess)

	assert.False(t, disabled)
	var errorLogs []model.Log
	require.NoError(t, db.Where("type = ?", model.LogTypeError).Find(&errorLogs).Error)
	require.Len(t, errorLogs, 1)
	assert.Equal(t, channelId, errorLogs[0].ChannelId)
	assert.Contains(t, errorLogs[0].Content, "temporary upstream failure")
}

func TestFinalizeSuccessfulRelayAttemptDoesNotResetZeroTokenFailure(t *testing.T) {
	const channelId = 92002
	const usingKey = "key-zero-token"

	previousErrorLogEnabled := constant.ErrorLogEnabled
	previousAutomaticDisableEnabled := common.AutomaticDisableChannelEnabled
	previousTolerance := common.AutoDisableTolerance
	constant.ErrorLogEnabled = false
	common.AutomaticDisableChannelEnabled = true
	common.AutoDisableTolerance = 2

	t.Cleanup(func() {
		service.ResetChannelFailCount(channelId, usingKey)
		constant.ErrorLogEnabled = previousErrorLogEnabled
		common.AutomaticDisableChannelEnabled = previousAutomaticDisableEnabled
		common.AutoDisableTolerance = previousTolerance
	})

	assert.False(t, service.RecordChannelFailure(channelId, usingKey, common.AutoDisableTolerance))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
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
