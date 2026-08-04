package operation_setting

import (
	"fmt"
	"math"
	"strconv"

	"github.com/QuantumNous/new-api/setting/config"
)

const (
	RetryDelayImmediate   = "immediate"
	RetryDelayFixed       = "fixed"
	RetryDelayExponential = "exponential"

	RetryChannelLegacy       = "legacy"
	RetryChannelSamePriority = "same_priority"

	SamePriorityWeightedRandom = "weighted_random"
	SamePriorityStabilityFirst = "stability_first"
	SamePriorityLatencyFirst   = "latency_first"

	RetryExhaustedStop  = "stop"
	RetryExhaustedCycle = "cycle"
)

type RetrySetting struct {
	TimeBudgetSeconds                   int64   `json:"time_budget_seconds"`
	DelayStrategy                       string  `json:"delay_strategy"`
	FixedDelayMilliseconds              int64   `json:"fixed_delay_milliseconds"`
	ExponentialBaseDelayMilliseconds    int64   `json:"exponential_base_delay_milliseconds"`
	ExponentialMaximumDelayMilliseconds int64   `json:"exponential_max_delay_milliseconds"`
	JitterPercent                       float64 `json:"jitter_percent"`
	RespectRetryAfter                   bool    `json:"respect_retry_after"`
	ChannelStrategy                     string  `json:"channel_strategy"`
	SamePriorityStrategy                string  `json:"same_priority_strategy"`
	ExhaustedAction                     string  `json:"exhausted_action"`
	TryOtherKeys                        bool    `json:"try_other_keys"`
}

var retrySetting = RetrySetting{
	TimeBudgetSeconds:                   0,
	DelayStrategy:                       RetryDelayImmediate,
	FixedDelayMilliseconds:              0,
	ExponentialBaseDelayMilliseconds:    250,
	ExponentialMaximumDelayMilliseconds: 10000,
	JitterPercent:                       20,
	RespectRetryAfter:                   false,
	ChannelStrategy:                     RetryChannelLegacy,
	SamePriorityStrategy:                SamePriorityWeightedRandom,
	ExhaustedAction:                     RetryExhaustedStop,
	TryOtherKeys:                        false,
}

func init() {
	config.GlobalConfig.Register("retry_setting", &retrySetting)
}

func GetRetrySetting() RetrySetting {
	setting := RetrySetting{}
	if config.GlobalConfig.CopyInto("retry_setting", &setting) {
		return setting
	}
	return retrySetting
}

func ValidateRetryOption(key, value string) error {
	switch key {
	case "retry_setting.respect_retry_after", "retry_setting.try_other_keys":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s must be a boolean", key)
		}
	case "retry_setting.time_budget_seconds", "retry_setting.fixed_delay_milliseconds", "retry_setting.exponential_base_delay_milliseconds", "retry_setting.exponential_max_delay_milliseconds":
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			return fmt.Errorf("%s must be a non-negative integer", key)
		}
	case "retry_setting.jitter_percent":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed < 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return fmt.Errorf("%s must be a non-negative finite number", key)
		}
	case "retry_setting.delay_strategy":
		if value != RetryDelayImmediate && value != RetryDelayFixed && value != RetryDelayExponential {
			return fmt.Errorf("%s must be immediate, fixed, or exponential", key)
		}
	case "retry_setting.channel_strategy":
		if value != RetryChannelLegacy && value != RetryChannelSamePriority {
			return fmt.Errorf("%s must be legacy or same_priority", key)
		}
	case "retry_setting.same_priority_strategy":
		if value != SamePriorityWeightedRandom && value != SamePriorityStabilityFirst && value != SamePriorityLatencyFirst {
			return fmt.Errorf("%s must be weighted_random, stability_first, or latency_first", key)
		}
	case "retry_setting.exhausted_action":
		if value != RetryExhaustedStop && value != RetryExhaustedCycle {
			return fmt.Errorf("%s must be stop or cycle", key)
		}
	default:
		return fmt.Errorf("unknown retry setting: %s", key)
	}
	return nil
}
