package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFirstTokenTimeoutRetrySettlesAndRecordsConsumeOnce(t *testing.T) {
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousBatchUpdateEnabled := common.BatchUpdateEnabled
	previousLogConsumeEnabled := common.LogConsumeEnabled
	previousRedisEnabled := common.RedisEnabled
	previousStreamingTimeout := constant.StreamingTimeout

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Log{}))
	model.DB = db
	model.LOG_DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = true
	common.RedisEnabled = false
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		common.BatchUpdateEnabled = previousBatchUpdateEnabled
		common.LogConsumeEnabled = previousLogConsumeEnabled
		common.RedisEnabled = previousRedisEnabled
		constant.StreamingTimeout = previousStreamingTimeout
		require.NoError(t, sqlDB.Close())
	})

	const (
		userID       = 97101
		channelID    = 97201
		initialQuota = 1_000_000
		preConsumed  = 5
		modelName    = "billing-test-model"
	)
	require.NoError(t, db.Create(&model.User{
		Id:       userID,
		Username: "timeout-billing-user",
		Status:   common.UserStatusEnabled,
		Quota:    initialQuota - preConsumed,
		Group:    "default",
	}).Error)
	require.NoError(t, db.Create(&model.Channel{
		Id:     channelID,
		Name:   "timeout-billing-channel",
		Key:    "sk-test",
		Status: common.ChannelStatusEnabled,
	}).Error)

	var upstreamAttempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamAttempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-success\",\"object\":\"chat.completion.chunk\",\"created\":2,\"model\":\"billing-test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-success\",\"object\":\"chat.completion.chunk\",\"created\":2,\"model\":\"billing-test-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(upstream.Close)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Accept", "text/event-stream")
	c.Set("username", "timeout-billing-user")
	c.Set("token_name", "timeout-billing-token")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(c, constant.ContextKeyChannelId, channelID)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "sk-test")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, modelName)
	common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{})
	common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, dto.ChannelOtherSettings{})

	stream := true
	info := &relaycommon.RelayInfo{
		TokenId:      0,
		UserId:       userID,
		UserQuota:    initialQuota,
		UsingGroup:   "default",
		UserGroup:    "default",
		StartTime:    time.Now(),
		IsStream:     true,
		IsPlayground: true,
		RelayMode:    relayconstant.RelayModeChatCompletions,
		// Exclude this fixture from the unrelated asynchronous performance-metrics
		// path so cleanup never races with a late RedisEnabled read.
		OriginModelName:       "",
		RequestURLPath:        "/v1/chat/completions",
		RelayFormat:           relaytypes.RelayFormatOpenAI,
		FinalPreConsumedQuota: preConsumed,
		Request: &dto.GeneralOpenAIRequest{
			Model:  modelName,
			Stream: &stream,
			Messages: []dto.Message{{
				Role:    "user",
				Content: "hello",
			}},
		},
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			CacheRatio:      1,
			GroupRatioInfo: hosttypes.GroupRatioInfo{
				GroupRatio: 1,
			},
		},
	}

	info.BeginAttempt()
	info.ConfigureAttemptFirstResponseTimeout(3, context.Background(), nil)
	require.True(t, info.MarkAttemptFirstResponseTimeout())
	firstErr := relay.TextHelper(c, info)
	require.NotNil(t, firstErr)
	assert.Equal(t, relaytypes.ErrorCodeChannelResponseTimeExceeded, firstErr.GetErrorCode())
	assert.True(t, shouldRetry(c, firstErr, 1))

	var user model.User
	require.NoError(t, db.First(&user, userID).Error)
	assert.Equal(t, initialQuota-preConsumed, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	var channel model.Channel
	require.NoError(t, db.First(&channel, channelID).Error)
	assert.Zero(t, channel.UsedQuota)
	var consumeCount int64
	require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&consumeCount).Error)
	assert.Zero(t, consumeCount)

	info.BeginAttempt()
	info.ConfigureAttemptFirstResponseTimeout(3, context.Background(), nil)
	secondErr := relay.TextHelper(c, info)
	require.Nil(t, secondErr)
	assert.EqualValues(t, 2, upstreamAttempts.Load())
	assert.Contains(t, recorder.Body.String(), `"content":"ok"`)

	require.NoError(t, db.First(&user, userID).Error)
	assert.Equal(t, initialQuota-preConsumed, user.Quota)
	assert.Equal(t, 5, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	require.NoError(t, db.First(&channel, channelID).Error)
	assert.EqualValues(t, 5, channel.UsedQuota)
	var consumeLogs []model.Log
	require.NoError(t, db.Where("type = ?", model.LogTypeConsume).Find(&consumeLogs).Error)
	require.Len(t, consumeLogs, 1)
	assert.Equal(t, 5, consumeLogs[0].Quota)
	assert.Equal(t, 3, consumeLogs[0].PromptTokens)
	assert.Equal(t, 2, consumeLogs[0].CompletionTokens)
}
