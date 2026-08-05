package service

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

type channelFailCountKey struct {
	channelId int
	keyHash   string
}

type channelFailureState struct {
	Consecutive      int
	WindowFailures   []int64
	RateBits         []byte
	RateCount        int
	RateCursor       int
	ThresholdReached bool
	Claimed          bool
	ClaimToken       string
	ClaimedAt        time.Time
	PolicySignature  string
}

var channelFailCounts = struct {
	sync.Mutex
	counts map[channelFailCountKey]*channelFailureState
}{
	counts: make(map[channelFailCountKey]*channelFailureState),
}

var channelFailureNow = time.Now

func channelFailureKey(channelId int, usingKey string) channelFailCountKey {
	return channelFailCountKey{channelId: channelId, keyHash: model.ChannelFailureKeyHash(usingKey)}
}

func normalizeAutoDisableSetting(setting operation_setting.MonitorSetting) operation_setting.MonitorSetting {
	if setting.AutoDisableStrategy == "" {
		setting.AutoDisableStrategy = operation_setting.AutoDisableStrategyConsecutive
	}
	if setting.AutoDisableWindowMinutes < 1 {
		setting.AutoDisableWindowMinutes = 10
	}
	if setting.AutoDisableWindowFailures < 1 {
		setting.AutoDisableWindowFailures = 5
	}
	if setting.AutoDisableRateSampleSize < 1 {
		setting.AutoDisableRateSampleSize = 20
	}
	if setting.AutoDisableRateMinSamples < 1 {
		setting.AutoDisableRateMinSamples = 10
	}
	if setting.AutoDisableRateThresholdPercent <= 0 || setting.AutoDisableRateThresholdPercent > 100 {
		setting.AutoDisableRateThresholdPercent = 60
	}
	return setting
}

func currentChannelFailurePolicy(tolerance int) model.ChannelFailurePolicy {
	setting := normalizeAutoDisableSetting(operation_setting.GetMonitorSettingSnapshot())
	return model.ChannelFailurePolicy{
		Strategy:             setting.AutoDisableStrategy,
		ConsecutiveThreshold: tolerance,
		WindowThreshold:      setting.AutoDisableWindowFailures,
		Window:               time.Duration(setting.AutoDisableWindowMinutes) * time.Minute,
		RateSampleSize:       setting.AutoDisableRateSampleSize,
		RateMinSamples:       setting.AutoDisableRateMinSamples,
		RateThresholdPercent: setting.AutoDisableRateThresholdPercent,
		PolicySignature: fmt.Sprintf("%s|%d|%d|%d|%d|%d|%.9g",
			setting.AutoDisableStrategy,
			tolerance,
			setting.AutoDisableWindowMinutes,
			setting.AutoDisableWindowFailures,
			setting.AutoDisableRateSampleSize,
			setting.AutoDisableRateMinSamples,
			setting.AutoDisableRateThresholdPercent,
		),
	}
}

func appendMemoryRateSample(state *channelFailureState, failed bool) {
	if len(state.RateBits) == 0 {
		state.RateBits = make([]byte, operation_setting.MaxAutoDisableWindowSamples/8)
	}
	if state.RateCursor < 0 || state.RateCursor >= operation_setting.MaxAutoDisableWindowSamples {
		state.RateCursor = 0
	}
	mask := byte(1 << uint(state.RateCursor%8))
	if failed {
		state.RateBits[state.RateCursor/8] |= mask
	} else {
		state.RateBits[state.RateCursor/8] &^= mask
	}
	state.RateCursor = (state.RateCursor + 1) % operation_setting.MaxAutoDisableWindowSamples
	if state.RateCount < operation_setting.MaxAutoDisableWindowSamples {
		state.RateCount++
	}
}

func memoryRateFailures(state *channelFailureState, sampleSize int) (int, int) {
	if sampleSize < 1 || sampleSize > operation_setting.MaxAutoDisableWindowSamples {
		sampleSize = operation_setting.MaxAutoDisableWindowSamples
	}
	count := state.RateCount
	if count > sampleSize {
		count = sampleSize
	}
	start := state.RateCursor - count
	for start < 0 {
		start += operation_setting.MaxAutoDisableWindowSamples
	}
	failed := 0
	for offset := 0; offset < count; offset++ {
		index := (start + offset) % operation_setting.MaxAutoDisableWindowSamples
		if state.RateBits[index/8]&(1<<uint(index%8)) != 0 {
			failed++
		}
	}
	return failed, count
}

func appendMemoryWindowFailure(state *channelFailureState, now time.Time, window time.Duration, limit int) {
	cutoff := now.Add(-window).Unix()
	retained := state.WindowFailures[:0]
	for _, timestamp := range state.WindowFailures {
		if timestamp >= cutoff {
			retained = append(retained, timestamp)
		}
	}
	retained = append(retained, now.Unix())
	sort.Slice(retained, func(i, j int) bool { return retained[i] < retained[j] })
	if limit < 1 || limit > operation_setting.MaxAutoDisableWindowSamples {
		limit = operation_setting.MaxAutoDisableWindowSamples
	}
	if len(retained) > limit {
		retained = retained[len(retained)-limit:]
	}
	state.WindowFailures = retained
}

func recordMemoryChannelOutcome(key channelFailCountKey, now time.Time, failed bool, policy model.ChannelFailurePolicy) (string, error) {
	channelFailCounts.Lock()
	defer channelFailCounts.Unlock()
	state := channelFailCounts.counts[key]
	if state == nil {
		state = &channelFailureState{}
		channelFailCounts.counts[key] = state
	}
	if failed && policy.PolicySignature != "" && state.PolicySignature != policy.PolicySignature {
		state.ThresholdReached = false
		state.Claimed = false
		state.ClaimToken = ""
		state.ClaimedAt = time.Time{}
		state.PolicySignature = policy.PolicySignature
	}
	if state.ThresholdReached {
		claimActive := state.Claimed && !state.ClaimedAt.IsZero() && now.Sub(state.ClaimedAt) < 2*time.Minute
		if claimActive {
			return "", nil
		}
		if failed {
			claimToken, err := common.GenerateRandomCharsKey(32)
			if err != nil {
				return "", err
			}
			state.Claimed = true
			state.ClaimToken = claimToken
			state.ClaimedAt = now
			return claimToken, nil
		}
		state.ThresholdReached = false
		state.Claimed = false
		state.ClaimToken = ""
		state.ClaimedAt = time.Time{}
		appendMemoryRateSample(state, false)
		state.Consecutive = 0
		return "", nil
	}
	appendMemoryRateSample(state, failed)
	if failed {
		state.Consecutive++
		appendMemoryWindowFailure(state, now, policy.Window, operation_setting.MaxAutoDisableWindowSamples)
	} else {
		state.Consecutive = 0
	}
	if !failed {
		return "", nil
	}
	triggered := false
	switch policy.Strategy {
	case operation_setting.AutoDisableStrategyWindow:
		triggered = len(state.WindowFailures) >= policy.WindowThreshold
	case operation_setting.AutoDisableStrategyFailureRate:
		failedCount, sampleCount := memoryRateFailures(state, policy.RateSampleSize)
		triggered = sampleCount >= policy.RateMinSamples && float64(failedCount)*100 >= policy.RateThresholdPercent*float64(sampleCount)
	default:
		threshold := policy.ConsecutiveThreshold
		if threshold < 1 {
			threshold = 1
		}
		triggered = state.Consecutive >= threshold
	}
	if triggered {
		claimToken, err := common.GenerateRandomCharsKey(32)
		if err != nil {
			return "", err
		}
		state.ThresholdReached = true
		state.Claimed = true
		state.ClaimToken = claimToken
		state.ClaimedAt = now
		return claimToken, nil
	}
	return "", nil
}

// ClaimChannelFailureWithError records one attributable upstream business
// failure and returns the token that owns the auto-disable attempt. An empty
// token means the threshold is not reached or another owner holds the claim.
func ClaimChannelFailureWithError(channelId int, usingKey string, tolerance int) (string, error) {
	key := channelFailureKey(channelId, usingKey)
	now := channelFailureNow()
	policy := currentChannelFailurePolicy(tolerance)
	if model.PersistentChannelFailureStateAvailable() {
		return model.RecordPersistentChannelOutcome(channelId, key.keyHash, now, true, policy)
	}
	return recordMemoryChannelOutcome(key, now, true, policy)
}

// RecordChannelFailureWithError records one attributable upstream business
// failure and returns whether this caller owns the auto-disable attempt.
func RecordChannelFailureWithError(channelId int, usingKey string, tolerance int) (bool, error) {
	claimToken, err := ClaimChannelFailureWithError(channelId, usingKey, tolerance)
	return claimToken != "", err
}

// RecordChannelFailure keeps the established call contract. Production paths
// that can surface or retry persistence errors should use
// RecordChannelFailureWithError.
func RecordChannelFailure(channelId int, usingKey string, tolerance int) bool {
	claimed, err := RecordChannelFailureWithError(channelId, usingKey, tolerance)
	if err != nil {
		common.SysError("record channel failure state: " + err.Error())
	}
	return claimed
}

// RecordChannelSuccess records one successful business request. Health checks
// must use ResetChannelFailCount only after an actual automatic recovery so
// they cannot dilute the recent-request failure-rate sample.
func RecordChannelSuccessWithError(channelId int, usingKey string) error {
	key := channelFailureKey(channelId, usingKey)
	now := channelFailureNow()
	policy := currentChannelFailurePolicy(0)
	if model.PersistentChannelFailureStateAvailable() {
		_, err := model.RecordPersistentChannelOutcome(channelId, key.keyHash, now, false, policy)
		return err
	}
	_, err := recordMemoryChannelOutcome(key, now, false, policy)
	return err
}

func RecordChannelSuccess(channelId int, usingKey string) {
	if err := RecordChannelSuccessWithError(channelId, usingKey); err != nil {
		common.SysError("record channel success state: " + err.Error())
	}
}

// CompleteChannelAutoDisable confirms the claimed automatic state change.
// Failed changes release the claim without discarding the threshold evidence.
func CompleteChannelAutoDisable(channelId int, usingKey string, claimToken string, succeeded bool) error {
	if claimToken == "" {
		return fmt.Errorf("channel failure claim token is required")
	}
	key := channelFailureKey(channelId, usingKey)
	if model.PersistentChannelFailureStateAvailable() {
		return model.CompletePersistentChannelAutoDisable(channelId, key.keyHash, claimToken, succeeded, channelFailureNow())
	}
	channelFailCounts.Lock()
	defer channelFailCounts.Unlock()
	state := channelFailCounts.counts[key]
	if state == nil || !state.ThresholdReached || !state.Claimed || state.ClaimToken != claimToken {
		return nil
	}
	if succeeded {
		delete(channelFailCounts.counts, key)
	} else {
		state.Claimed = false
		state.ClaimToken = ""
		state.ClaimedAt = time.Time{}
	}
	return nil
}

func ResetChannelFailCountWithError(channelId int, usingKey string) error {
	key := channelFailureKey(channelId, usingKey)
	if model.PersistentChannelFailureStateAvailable() {
		if err := model.ResetPersistentChannelFailureState(channelId, key.keyHash); err != nil {
			return fmt.Errorf("reset persisted channel failure state: %w", err)
		}
	}
	channelFailCounts.Lock()
	delete(channelFailCounts.counts, key)
	channelFailCounts.Unlock()
	return nil
}

func ResetChannelFailCount(channelId int, usingKey string) {
	if err := ResetChannelFailCountWithError(channelId, usingKey); err != nil {
		common.SysError(err.Error())
	}
}

func ResetChannelFailureStatesForChannel(channelID int) error {
	if model.PersistentChannelFailureStateAvailable() {
		if err := model.ResetPersistentChannelFailureStatesForChannel(channelID); err != nil {
			return err
		}
	}
	channelFailCounts.Lock()
	for key := range channelFailCounts.counts {
		if key.channelId == channelID {
			delete(channelFailCounts.counts, key)
		}
	}
	channelFailCounts.Unlock()
	return nil
}

func ClearChannelFailureMemoryForChannel(channelID int) {
	channelFailCounts.Lock()
	for key := range channelFailCounts.counts {
		if key.channelId == channelID {
			delete(channelFailCounts.counts, key)
		}
	}
	channelFailCounts.Unlock()
}

// RecordChannelRecovery clears policy evidence after an automatic health
// check has safely restored a channel or key. It intentionally does not add a
// successful business sample to the failure-rate policy.
func RecordChannelRecovery(channelId int, usingKey string) error {
	return ResetChannelFailCountWithError(channelId, usingKey)
}
