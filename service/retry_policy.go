package service

import (
	"context"
	"math"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

type RetryTraceEntry struct {
	Attempt          int    `json:"attempt"`
	ChannelID        int    `json:"channel_id"`
	ChannelName      string `json:"channel_name,omitempty"`
	Priority         int64  `json:"priority"`
	MultiKeyIndex    *int   `json:"multi_key_index,omitempty"`
	DurationMillis   int64  `json:"duration_ms"`
	StatusCode       int    `json:"status_code,omitempty"`
	ErrorCode        string `json:"error_code,omitempty"`
	DelayMillis      int64  `json:"delay_ms"`
	Decision         string `json:"decision"`
	Outcome          string `json:"outcome,omitempty"`
	attemptStartedAt time.Time
}

type RetryTrace struct {
	mu            sync.Mutex
	Entries       []RetryTraceEntry
	TotalAttempts int64
}

const (
	retryHistoryLimit = 100
	retryHistoryHead  = 20
)

func NewRetryParam(c *gin.Context, tokenGroup, modelName, requestPath string) *RetryParam {
	p := &RetryParam{
		Ctx:                  c,
		TokenGroup:           tokenGroup,
		ModelName:            modelName,
		RequestPath:          requestPath,
		Retry:                common.GetPointer(0),
		Setting:              operation_setting.GetRetrySetting(),
		StartedAt:            time.Now(),
		TriedChannels:        make(map[int]struct{}),
		TriedKeys:            make(map[int]map[int]struct{}),
		UnavailableChannels:  make(map[int]struct{}),
		PriorityIndexByGroup: make(map[string]int),
		Trace:                &RetryTrace{},
	}
	common.SetContextKey(c, constant.ContextKeyRetryTrace, p.Trace)
	return p
}

func (p *RetryParam) ensurePolicy() {
	if p.StartedAt.IsZero() {
		p.StartedAt = time.Now()
	}
	if p.Setting.DelayStrategy == "" {
		p.Setting = operation_setting.GetRetrySetting()
	}
	if p.TriedChannels == nil {
		p.TriedChannels = make(map[int]struct{})
	}
	if p.TriedKeys == nil {
		p.TriedKeys = make(map[int]map[int]struct{})
	}
	if p.UnavailableChannels == nil {
		p.UnavailableChannels = make(map[int]struct{})
	}
	if p.PriorityIndexByGroup == nil {
		p.PriorityIndexByGroup = make(map[string]int)
	}
	if p.Trace == nil {
		p.Trace = &RetryTrace{}
		if p.Ctx != nil {
			common.SetContextKey(p.Ctx, constant.ContextKeyRetryTrace, p.Trace)
		}
	}
}

func (p *RetryParam) UnlimitedForTask(_ bool) bool {
	p.ensurePolicy()
	return common.RetryTimes == -1
}

func (p *RetryParam) HasRetryAllowance(isTask bool) bool {
	p.ensurePolicy()
	if p.Ctx != nil && p.Ctx.Request != nil && p.Ctx.Request.Context().Err() != nil {
		return false
	}
	if !p.UnlimitedForTask(isTask) && p.GetRetry() >= common.RetryTimes {
		return false
	}
	return !p.BudgetExhausted()
}

func (p *RetryParam) BudgetExhausted() bool {
	p.ensurePolicy()
	budget := secondsDuration(p.Setting.TimeBudgetSeconds)
	return budget > 0 && time.Since(p.StartedAt) >= budget
}

func (p *RetryParam) CanStartRetryAttempt() bool {
	p.ensurePolicy()
	if p.Ctx != nil && p.Ctx.Request != nil && p.Ctx.Request.Context().Err() != nil {
		return false
	}
	return !p.BudgetExhausted()
}

func (p *RetryParam) NextDelay(err *types.NewAPIError) time.Duration {
	p.ensurePolicy()
	retryAfterMilliseconds := int64(0)
	if p.Setting.RespectRetryAfter && err != nil {
		retryAfterMilliseconds = err.GetRetryAfterMilliseconds()
	}
	retryOrdinal := p.GetRetry()
	if retryOrdinal < math.MaxInt {
		retryOrdinal++
	}
	return calculateRetryDelay(p.Setting, retryOrdinal, retryAfterMilliseconds, rand.Float64())
}

func (p *RetryParam) WaitBeforeRetry(ctx context.Context, delay time.Duration) bool {
	p.ensurePolicy()
	if ctx == nil {
		ctx = context.Background()
	}
	budget := secondsDuration(p.Setting.TimeBudgetSeconds)
	if budget > 0 {
		remaining := budget - time.Since(p.StartedAt)
		if remaining <= 0 || delay > remaining {
			return false
		}
	}
	if delay <= 0 {
		return ctx.Err() == nil && !p.BudgetExhausted()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return !p.BudgetExhausted()
	}
}

func (p *RetryParam) CanWaitForRetry(delay time.Duration) bool {
	p.ensurePolicy()
	budget := secondsDuration(p.Setting.TimeBudgetSeconds)
	if budget == 0 {
		return true
	}
	remaining := budget - time.Since(p.StartedAt)
	return remaining > 0 && delay <= remaining
}

func calculateRetryDelay(setting operation_setting.RetrySetting, retryOrdinal int, retryAfterMilliseconds int64, randomValue float64) time.Duration {
	configuredMilliseconds := int64(0)
	switch setting.DelayStrategy {
	case operation_setting.RetryDelayFixed:
		configuredMilliseconds = setting.FixedDelayMilliseconds
	case operation_setting.RetryDelayExponential:
		configuredMilliseconds = exponentialMilliseconds(setting.ExponentialBaseDelayMilliseconds, setting.ExponentialMaximumDelayMilliseconds, retryOrdinal)
	}
	if configuredMilliseconds < 0 {
		configuredMilliseconds = 0
	}
	delay := millisecondsDuration(configuredMilliseconds)
	if delay > 0 && setting.JitterPercent > 0 && setting.DelayStrategy == operation_setting.RetryDelayExponential {
		if randomValue < 0 {
			randomValue = 0
		} else if randomValue > 1 {
			randomValue = 1
		}
		factor := 1 + (randomValue*2-1)*(setting.JitterPercent/100)
		if factor < 0 {
			factor = 0
		}
		jittered := float64(delay) * factor
		if math.IsInf(jittered, 1) || jittered >= float64(time.Duration(math.MaxInt64)) {
			delay = time.Duration(math.MaxInt64)
		} else {
			delay = time.Duration(jittered)
		}
	}
	if setting.DelayStrategy == operation_setting.RetryDelayExponential && setting.ExponentialMaximumDelayMilliseconds > 0 {
		maximumDelay := millisecondsDuration(setting.ExponentialMaximumDelayMilliseconds)
		if delay > maximumDelay {
			delay = maximumDelay
		}
	}
	retryAfter := millisecondsDuration(retryAfterMilliseconds)
	if retryAfter > delay {
		return retryAfter
	}
	return delay
}

func exponentialMilliseconds(base, maximum int64, retryOrdinal int) int64 {
	if base <= 0 || retryOrdinal <= 0 {
		return 0
	}
	value := base
	if retryOrdinal > 63 {
		value = math.MaxInt64
	} else {
		for i := 1; i < retryOrdinal; i++ {
			if value > math.MaxInt64/2 {
				value = math.MaxInt64
				break
			}
			value *= 2
		}
	}
	if maximum > 0 && value > maximum {
		return maximum
	}
	return value
}

func millisecondsDuration(milliseconds int64) time.Duration {
	if milliseconds <= 0 {
		return 0
	}
	if milliseconds > math.MaxInt64/int64(time.Millisecond) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func secondsDuration(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	if seconds > math.MaxInt64/int64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(seconds) * time.Second
}

func (p *RetryParam) StartAttemptTrace(channel *model.Channel) {
	p.ensurePolicy()
	if channel == nil {
		return
	}
	attempt := p.GetRetry()
	if attempt < math.MaxInt {
		attempt++
	}
	entry := RetryTraceEntry{
		Attempt:          attempt,
		ChannelID:        channel.Id,
		ChannelName:      channel.Name,
		Priority:         channel.GetPriority(),
		Decision:         "attempting",
		attemptStartedAt: time.Now(),
	}
	if p.Ctx != nil && common.GetContextKeyBool(p.Ctx, constant.ContextKeyChannelIsMultiKey) {
		index := common.GetContextKeyInt(p.Ctx, constant.ContextKeyChannelMultiKeyIndex)
		entry.MultiKeyIndex = &index
	}
	p.Trace.mu.Lock()
	if p.Trace.TotalAttempts < math.MaxInt64 {
		p.Trace.TotalAttempts++
	}
	if len(p.Trace.Entries) < retryHistoryLimit {
		p.Trace.Entries = append(p.Trace.Entries, entry)
	} else {
		copy(p.Trace.Entries[retryHistoryHead:len(p.Trace.Entries)-1], p.Trace.Entries[retryHistoryHead+1:])
		p.Trace.Entries[len(p.Trace.Entries)-1] = entry
	}
	p.Trace.mu.Unlock()
}

func (p *RetryParam) FinishAttemptTrace(err *types.NewAPIError, delay time.Duration, decision, outcome string) {
	p.ensurePolicy()
	p.Trace.mu.Lock()
	defer p.Trace.mu.Unlock()
	if len(p.Trace.Entries) == 0 {
		return
	}
	entry := &p.Trace.Entries[len(p.Trace.Entries)-1]
	entry.DurationMillis = time.Since(entry.attemptStartedAt).Milliseconds()
	entry.DelayMillis = delay.Milliseconds()
	entry.Decision = decision
	entry.Outcome = outcome
	if err != nil {
		entry.StatusCode = err.StatusCode
		entry.ErrorCode = string(err.GetErrorCode())
	}
}

func retryTraceSnapshot(ctx *gin.Context, assumeCurrentSuccess bool) ([]RetryTraceEntry, int64) {
	if ctx == nil {
		return nil, 0
	}
	value, ok := common.GetContextKey(ctx, constant.ContextKeyRetryTrace)
	if !ok {
		return nil, 0
	}
	trace, ok := value.(*RetryTrace)
	if !ok || trace == nil {
		return nil, 0
	}
	trace.mu.Lock()
	entries := make([]RetryTraceEntry, len(trace.Entries))
	copy(entries, trace.Entries)
	total := trace.TotalAttempts
	trace.mu.Unlock()
	if assumeCurrentSuccess && len(entries) > 0 {
		entry := &entries[len(entries)-1]
		if entry.Decision == "attempting" {
			entry.DurationMillis = time.Since(entry.attemptStartedAt).Milliseconds()
			entry.Decision = "complete"
			entry.Outcome = "success"
		}
	}
	if len(entries) == 0 {
		return nil, total
	}
	for i := range entries {
		entries[i].attemptStartedAt = time.Time{}
	}
	return entries, total
}

func AppendRetryTraceAdminInfo(ctx *gin.Context, adminInfo map[string]interface{}, assumeCurrentSuccess bool) {
	if adminInfo == nil {
		return
	}
	entries, total := retryTraceSnapshot(ctx, assumeCurrentSuccess)
	if len(entries) == 0 {
		return
	}
	adminInfo["retry_trace"] = entries
	adminInfo["retry_trace_total"] = total
	adminInfo["retry_trace_omitted"] = total - int64(len(entries))
}

func AppendCurrentRetryTraceAdminInfo(ctx *gin.Context, adminInfo map[string]interface{}) {
	if adminInfo == nil {
		return
	}
	entries, _ := retryTraceSnapshot(ctx, false)
	if len(entries) == 0 {
		return
	}
	adminInfo["retry_trace"] = entries[len(entries)-1:]
	adminInfo["retry_trace_total"] = 1
	adminInfo["retry_trace_omitted"] = 0
}

func AddUsedChannel(ctx *gin.Context, channelID int) {
	if ctx == nil {
		return
	}
	channels := ctx.GetStringSlice("use_channel")
	total := ctx.GetInt("use_channel_total")
	if total < math.MaxInt {
		total++
	}
	value := strconv.Itoa(channelID)
	if len(channels) < retryHistoryLimit {
		channels = append(channels, value)
	} else {
		copy(channels[retryHistoryHead:len(channels)-1], channels[retryHistoryHead+1:])
		channels[len(channels)-1] = value
	}
	ctx.Set("use_channel", channels)
	ctx.Set("use_channel_total", total)
}

func AppendUsedChannelAdminInfo(ctx *gin.Context, adminInfo map[string]interface{}) {
	if ctx == nil || adminInfo == nil {
		return
	}
	channels := ctx.GetStringSlice("use_channel")
	if len(channels) == 0 {
		return
	}
	total := ctx.GetInt("use_channel_total")
	if total == 0 {
		total = len(channels)
	}
	adminInfo["use_channel"] = channels
	adminInfo["use_channel_total"] = total
	adminInfo["use_channel_omitted"] = total - len(channels)
}

func MarkRetryTraceFailure(ctx *gin.Context, err *types.NewAPIError, decision, outcome string) {
	if ctx == nil {
		return
	}
	value, ok := common.GetContextKey(ctx, constant.ContextKeyRetryTrace)
	if !ok {
		return
	}
	trace, ok := value.(*RetryTrace)
	if !ok || trace == nil {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if len(trace.Entries) == 0 {
		return
	}
	entry := &trace.Entries[len(trace.Entries)-1]
	entry.DurationMillis = time.Since(entry.attemptStartedAt).Milliseconds()
	entry.Decision = decision
	entry.Outcome = outcome
	if err != nil {
		entry.StatusCode = err.StatusCode
		entry.ErrorCode = string(err.GetErrorCode())
	}
}

func UpdateRetryTraceDecision(ctx *gin.Context, decision, outcome string) {
	if ctx == nil {
		return
	}
	value, ok := common.GetContextKey(ctx, constant.ContextKeyRetryTrace)
	if !ok {
		return
	}
	trace, ok := value.(*RetryTrace)
	if !ok || trace == nil {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if len(trace.Entries) == 0 {
		return
	}
	entry := &trace.Entries[len(trace.Entries)-1]
	entry.Decision = decision
	entry.Outcome = outcome
}
