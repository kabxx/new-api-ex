package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLockedTaskRetryUsesUntriedKeysAndCyclesWithoutResettingAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/remix", nil)
	channel := &model.Channel{
		Id:     7,
		Name:   "locked",
		Key:    "key-a\nkey-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:         true,
			MultiKeyMode:       constant.MultiKeyModeRandom,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled, 1: common.ChannelStatusEnabled},
		},
	}
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "model"))
	firstIndex := common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)

	p := service.NewRetryParam(c, "default", "model", c.Request.URL.Path)
	p.Setting.ChannelStrategy = operation_setting.RetryChannelSamePriority
	p.Setting.TryOtherKeys = true
	p.Setting.ExhaustedAction = operation_setting.RetryExhaustedStop
	p.SetRetry(1)
	p.RecordSelection(channel.Id, firstIndex, true)

	exhausted, setupErr := setupLockedTaskRetryChannel(c, channel, "model", p)
	require.Nil(t, setupErr)
	assert.False(t, exhausted)
	secondIndex := common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
	assert.NotEqual(t, firstIndex, secondIndex)
	p.RecordSelection(channel.Id, secondIndex, true)
	p.IncreaseRetry()

	exhausted, setupErr = setupLockedTaskRetryChannel(c, channel, "model", p)
	require.Nil(t, setupErr)
	assert.True(t, exhausted)

	attemptBeforeCycle := p.GetRetry()
	startedAtBeforeCycle := p.StartedAt
	p.Setting.ExhaustedAction = operation_setting.RetryExhaustedCycle
	exhausted, setupErr = setupLockedTaskRetryChannel(c, channel, "model", p)
	require.Nil(t, setupErr)
	assert.False(t, exhausted)
	assert.Equal(t, attemptBeforeCycle, p.GetRetry())
	assert.Equal(t, startedAtBeforeCycle, p.StartedAt)
	assert.Contains(t, []int{0, 1}, common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex))
}

func TestLockedTaskSamePriorityTreatsChannelAsCandidateWhenOtherKeysDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/remix", nil)
	channel := &model.Channel{Id: 7, Name: "locked", Key: "key-a\nkey-b", ChannelInfo: model.ChannelInfo{IsMultiKey: true}}
	p := service.NewRetryParam(c, "default", "model", c.Request.URL.Path)
	p.Setting.ChannelStrategy = operation_setting.RetryChannelSamePriority
	p.Setting.TryOtherKeys = false
	p.SetRetry(1)

	exhausted, setupErr := setupLockedTaskRetryChannel(c, channel, "model", p)
	require.Nil(t, setupErr)
	assert.True(t, exhausted)

	p.Setting.ExhaustedAction = operation_setting.RetryExhaustedCycle
	attemptBeforeCycle := p.GetRetry()
	startedAtBeforeCycle := p.StartedAt
	exhausted, setupErr = setupLockedTaskRetryChannel(c, channel, "model", p)
	require.Nil(t, setupErr)
	assert.False(t, exhausted)
	assert.Equal(t, attemptBeforeCycle, p.GetRetry())
	assert.Equal(t, startedAtBeforeCycle, p.StartedAt)
}

func TestLockedTaskLegacyIgnoresHiddenCandidateCycleSetting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/remix", nil)
	channel := &model.Channel{
		Id:     7,
		Name:   "locked",
		Key:    "key-a",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:         true,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled},
		},
	}
	p := service.NewRetryParam(c, "default", "model", c.Request.URL.Path)
	p.Setting.ChannelStrategy = operation_setting.RetryChannelLegacy
	p.Setting.TryOtherKeys = true
	p.Setting.ExhaustedAction = operation_setting.RetryExhaustedCycle
	p.SetRetry(1)
	p.RecordSelection(channel.Id, 0, true)

	exhausted, setupErr := setupLockedTaskRetryChannel(c, channel, "model", p)
	require.Nil(t, setupErr)
	assert.True(t, exhausted)
}

func TestTaskErrorAsAPIErrorCarriesRetryAfter(t *testing.T) {
	apiErr := taskErrorAsAPIError(&taskdto.TaskError{
		Message:                "busy",
		StatusCode:             http.StatusTooManyRequests,
		RetryAfterMilliseconds: 2500,
	})
	require.NotNil(t, apiErr)
	assert.Equal(t, int64(2500), apiErr.GetRetryAfterMilliseconds())
}
