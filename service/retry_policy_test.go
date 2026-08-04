package service

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculateRetryDelayStrategiesAndRetryAfter(t *testing.T) {
	setting := operation_setting.GetRetrySetting()
	setting.JitterPercent = 0
	assert.Zero(t, calculateRetryDelay(setting, 100, 0, 0.5))

	setting.DelayStrategy = operation_setting.RetryDelayFixed
	setting.FixedDelayMilliseconds = 750
	setting.JitterPercent = 100
	assert.Equal(t, 750*time.Millisecond, calculateRetryDelay(setting, 1, 0, 0.5))
	assert.Equal(t, 750*time.Millisecond, calculateRetryDelay(setting, 1, 0, 1))

	setting.DelayStrategy = operation_setting.RetryDelayExponential
	setting.JitterPercent = 0
	setting.ExponentialBaseDelayMilliseconds = 250
	setting.ExponentialMaximumDelayMilliseconds = 1000
	assert.Equal(t, 250*time.Millisecond, calculateRetryDelay(setting, 1, 0, 0.5))
	assert.Equal(t, time.Second, calculateRetryDelay(setting, 10, 0, 0.5))
	assert.Equal(t, 1500*time.Millisecond, calculateRetryDelay(setting, 2, 1500, 0.5))
	setting.JitterPercent = 100
	assert.Equal(t, time.Second, calculateRetryDelay(setting, 10, 0, 1))

	setting.ExponentialMaximumDelayMilliseconds = 0
	setting.JitterPercent = 1e300
	assert.LessOrEqual(t, calculateRetryDelay(setting, math.MaxInt, 0, 1), time.Duration(math.MaxInt64))
}

func TestRetryAllowanceUsesNegativeRetryTimesForAllRequestTypes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	previous := common.RetryTimes
	common.RetryTimes = 10
	t.Cleanup(func() { common.RetryTimes = previous })

	p := NewRetryParam(c, "default", "model", "/v1/responses")
	p.SetRetry(10)
	assert.False(t, p.HasRetryAllowance(false))
	common.RetryTimes = -1
	assert.True(t, p.HasRetryAllowance(false))
	assert.True(t, p.HasRetryAllowance(true))
}

func TestRetryWaitHonorsCancellationAndBudget(t *testing.T) {
	p := &RetryParam{Setting: operation_setting.GetRetrySetting(), StartedAt: time.Now()}
	p.Setting.TimeBudgetSeconds = 1
	assert.False(t, p.CanWaitForRetry(2*time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.False(t, p.WaitBeforeRetry(ctx, time.Second))
}

func TestCanStartRetryAttemptChecksBudgetAndCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	requestContext, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestContext)
	p := NewRetryParam(c, "default", "model", "/v1/responses")
	assert.True(t, p.CanStartRetryAttempt())

	p.Setting.TimeBudgetSeconds = 1
	p.StartedAt = time.Now().Add(-2 * time.Second)
	assert.False(t, p.CanStartRetryAttempt())

	p.Setting.TimeBudgetSeconds = 0
	cancel()
	assert.False(t, p.CanStartRetryAttempt())
}

func TestRetryTraceDoesNotContainKeyMaterial(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, true)
	common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, 2)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "secret-upstream-key")
	p := NewRetryParam(c, "default", "model", "/v1/responses")
	priority := int64(9)
	p.StartAttemptTrace(&model.Channel{Id: 7, Name: "primary", Priority: &priority})
	p.FinishAttemptTrace(types.NewErrorWithStatusCode(assert.AnError, types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway), 250*time.Millisecond, "retry", "retry_scheduled")
	adminInfo := map[string]interface{}{}
	AppendRetryTraceAdminInfo(c, adminInfo, false)
	encoded, err := common.Marshal(adminInfo)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(encoded), "secret-upstream-key"))
	assert.Contains(t, string(encoded), "multi_key_index")
}

func TestUpdateRetryTraceDecisionAfterWaitStops(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	p := NewRetryParam(c, "default", "model", "/v1/responses")
	p.StartAttemptTrace(&model.Channel{Id: 7, Name: "primary"})
	p.FinishAttemptTrace(types.NewError(assert.AnError, types.ErrorCodeBadResponse), 0, "retry", "retry_scheduled")

	UpdateRetryTraceDecision(c, "client_cancelled", "cancelled")

	adminInfo := map[string]interface{}{}
	AppendRetryTraceAdminInfo(c, adminInfo, false)
	entries := adminInfo["retry_trace"].([]RetryTraceEntry)
	require.Len(t, entries, 1)
	assert.Equal(t, "client_cancelled", entries[0].Decision)
	assert.Equal(t, "cancelled", entries[0].Outcome)
}

func TestRetryCandidateIdentityAndCyclePreserveAbsoluteBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	p := NewRetryParam(c, "default", "model", "/v1/responses")
	p.Setting.ChannelStrategy = operation_setting.RetryChannelSamePriority
	p.RecordSelection(7, 1, true)
	_, channelTried := p.TriedChannels[7]
	assert.True(t, channelTried)

	p.ResetCandidateRound()
	p.Setting.TryOtherKeys = true
	p.RecordSelection(7, 1, true)
	_, keyTried := p.TriedKeys[7][1]
	assert.True(t, keyTried)
	_, channelTried = p.TriedChannels[7]
	assert.False(t, channelTried)

	p.SetRetry(8)
	startedAt := p.StartedAt
	p.ResetCandidateRound()
	assert.Equal(t, 8, p.GetRetry())
	assert.Equal(t, startedAt, p.StartedAt)
	assert.Empty(t, p.TriedKeys)
}

func TestRetryCounterAndTraceAttemptSaturateAtTechnicalLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	p := NewRetryParam(c, "default", "model", "/v1/responses")
	p.SetRetry(math.MaxInt)

	p.IncreaseRetry()
	p.StartAttemptTrace(&model.Channel{Id: 1})

	assert.Equal(t, math.MaxInt, p.GetRetry())
	adminInfo := map[string]interface{}{}
	AppendRetryTraceAdminInfo(c, adminInfo, true)
	entries := adminInfo["retry_trace"].([]RetryTraceEntry)
	require.Len(t, entries, 1)
	assert.Equal(t, math.MaxInt, entries[0].Attempt)
}

func TestRetryTraceAndUsedChannelsKeepBoundedHeadAndTail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	p := NewRetryParam(c, "default", "model", "/v1/responses")

	for index := 0; index < 120; index++ {
		p.SetRetry(index)
		channel := &model.Channel{Id: index + 1}
		p.StartAttemptTrace(channel)
		p.FinishAttemptTrace(nil, 0, "complete", "success")
		AddUsedChannel(c, channel.Id)
	}

	adminInfo := map[string]interface{}{}
	AppendRetryTraceAdminInfo(c, adminInfo, false)
	AppendUsedChannelAdminInfo(c, adminInfo)
	entries := adminInfo["retry_trace"].([]RetryTraceEntry)
	channels := adminInfo["use_channel"].([]string)
	require.Len(t, entries, retryHistoryLimit)
	require.Len(t, channels, retryHistoryLimit)
	assert.Equal(t, int64(120), adminInfo["retry_trace_total"])
	assert.Equal(t, int64(20), adminInfo["retry_trace_omitted"])
	assert.Equal(t, 120, adminInfo["use_channel_total"])
	assert.Equal(t, 20, adminInfo["use_channel_omitted"])
	assert.Equal(t, 1, entries[0].Attempt)
	assert.Equal(t, 20, entries[retryHistoryHead-1].Attempt)
	assert.Equal(t, 41, entries[retryHistoryHead].Attempt)
	assert.Equal(t, 120, entries[len(entries)-1].Attempt)
	assert.Equal(t, "1", channels[0])
	assert.Equal(t, "20", channels[retryHistoryHead-1])
	assert.Equal(t, "41", channels[retryHistoryHead])
	assert.Equal(t, "120", channels[len(channels)-1])
}

func TestCurrentRetryTraceAdminInfoDoesNotCopyHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	p := NewRetryParam(c, "default", "model", "/v1/responses")
	for index := 0; index < 3; index++ {
		p.SetRetry(index)
		p.StartAttemptTrace(&model.Channel{Id: index + 1})
		p.FinishAttemptTrace(types.NewError(assert.AnError, types.ErrorCodeBadResponse), 0, "retry", "retry_scheduled")
	}

	adminInfo := map[string]interface{}{}
	AppendCurrentRetryTraceAdminInfo(c, adminInfo)
	entries := adminInfo["retry_trace"].([]RetryTraceEntry)
	require.Len(t, entries, 1)
	assert.Equal(t, 3, entries[0].Attempt)
	assert.Equal(t, 1, adminInfo["retry_trace_total"])
	assert.Equal(t, 0, adminInfo["retry_trace_omitted"])
}
