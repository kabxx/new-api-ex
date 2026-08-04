package service

import (
	"crypto/sha256"
	"encoding/hex"
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

type channelFailureObservation struct {
	At     time.Time
	Failed bool
}

type channelFailureState struct {
	Consecutive int
	Events      []channelFailureObservation
}

var channelFailCounts = struct {
	sync.Mutex
	counts map[channelFailCountKey]*channelFailureState
}{
	counts: make(map[channelFailCountKey]*channelFailureState),
}

// Tests can replace this clock without making rolling-window behavior depend
// on wall-clock scheduling. Production always uses the real clock.
var channelFailureNow = time.Now

func channelFailureKey(channelId int, usingKey string) channelFailCountKey {
	hash := sha256.Sum256([]byte(usingKey))
	return channelFailCountKey{channelId: channelId, keyHash: hex.EncodeToString(hash[:])}
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

func pruneChannelFailureEvents(state *channelFailureState, now time.Time, window time.Duration) {
	if window > 0 {
		cutoff := now.Add(-window)
		first := 0
		for first < len(state.Events) && state.Events[first].At.Before(cutoff) {
			first++
		}
		if first > 0 {
			state.Events = append([]channelFailureObservation(nil), state.Events[first:]...)
		}
	}
	if len(state.Events) > operation_setting.MaxAutoDisableWindowSamples {
		start := len(state.Events) - operation_setting.MaxAutoDisableWindowSamples
		state.Events = append([]channelFailureObservation(nil), state.Events[start:]...)
	}
}

func countFailedEvents(events []channelFailureObservation) int {
	failed := 0
	for _, event := range events {
		if event.Failed {
			failed++
		}
	}
	return failed
}

// RecordChannelFailure records one already-classified channel failure. The
// selected policy controls when this observation crosses the auto-disable
// threshold; the key identity is hashed so API keys never remain in the map.
func RecordChannelFailure(channelId int, usingKey string, tolerance int) bool {
	key := channelFailureKey(channelId, usingKey)
	setting := normalizeAutoDisableSetting(operation_setting.GetMonitorSettingSnapshot())
	now := channelFailureNow()
	window := time.Duration(0)
	if setting.AutoDisableStrategy == operation_setting.AutoDisableStrategyWindow {
		window = time.Duration(setting.AutoDisableWindowMinutes) * time.Minute
	}
	if model.PersistentChannelFailureStateAvailable() {
		triggered, err := model.RecordPersistentChannelFailure(
			channelId, key.keyHash, now, setting.AutoDisableStrategy, tolerance,
			setting.AutoDisableWindowFailures, setting.AutoDisableRateSampleSize,
			setting.AutoDisableRateMinSamples, setting.AutoDisableRateThresholdPercent, window,
		)
		if err == nil {
			return triggered
		}
		common.SysLog("failed to persist channel failure observation: " + err.Error())
		return false
	}

	channelFailCounts.Lock()
	defer channelFailCounts.Unlock()

	state := channelFailCounts.counts[key]
	if state == nil {
		state = &channelFailureState{}
		channelFailCounts.counts[key] = state
	}
	state.Events = append(state.Events, channelFailureObservation{At: now, Failed: true})
	state.Consecutive++
	pruneChannelFailureEvents(state, now, window)

	triggered := false
	switch setting.AutoDisableStrategy {
	case operation_setting.AutoDisableStrategyWindow:
		triggered = countFailedEvents(state.Events) >= setting.AutoDisableWindowFailures
	case operation_setting.AutoDisableStrategyFailureRate:
		observations := state.Events
		if len(observations) > setting.AutoDisableRateSampleSize {
			observations = observations[len(observations)-setting.AutoDisableRateSampleSize:]
		}
		failed := countFailedEvents(observations)
		triggered = len(observations) >= setting.AutoDisableRateMinSamples &&
			float64(failed)*100 >= setting.AutoDisableRateThresholdPercent*float64(len(observations))
	default:
		triggered = state.Consecutive > tolerance
	}
	if triggered {
		delete(channelFailCounts.counts, key)
	}
	return triggered
}

// RecordChannelSuccess keeps rolling-policy samples while resetting the
// consecutive counter. It deliberately does not clear history so changing a
// policy can immediately use observations already collected in the window.
func RecordChannelSuccess(channelId int, usingKey string) {
	key := channelFailureKey(channelId, usingKey)
	setting := normalizeAutoDisableSetting(operation_setting.GetMonitorSettingSnapshot())
	now := channelFailureNow()
	window := time.Duration(0)
	if setting.AutoDisableStrategy == operation_setting.AutoDisableStrategyWindow {
		window = time.Duration(setting.AutoDisableWindowMinutes) * time.Minute
	}
	if model.PersistentChannelFailureStateAvailable() {
		if err := model.RecordPersistentChannelSuccess(channelId, key.keyHash, now, window); err == nil {
			return
		} else {
			common.SysLog("failed to persist channel success observation: " + err.Error())
			return
		}
	}

	channelFailCounts.Lock()
	defer channelFailCounts.Unlock()

	state := channelFailCounts.counts[key]
	if state == nil {
		state = &channelFailureState{}
		channelFailCounts.counts[key] = state
	}
	state.Consecutive = 0
	state.Events = append(state.Events, channelFailureObservation{At: now, Failed: false})
	pruneChannelFailureEvents(state, now, window)
}

// ResetChannelFailCount clears all retained policy observations. It is used
// after a channel is actually restored or by tests that need a clean state.
func ResetChannelFailCount(channelId int, usingKey string) {
	key := channelFailureKey(channelId, usingKey)
	if model.PersistentChannelFailureStateAvailable() {
		if err := model.ResetPersistentChannelFailureState(channelId, key.keyHash); err != nil {
			common.SysLog("failed to reset persisted channel failure state: " + err.Error())
		}
	}
	channelFailCounts.Lock()
	delete(channelFailCounts.counts, key)
	channelFailCounts.Unlock()
}
