package operation_setting

import (
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
)

func TestRetrySettingDefaultsPreserveLegacyBehavior(t *testing.T) {
	setting := GetRetrySetting()
	assert.Zero(t, setting.TimeBudgetSeconds)
	assert.Equal(t, RetryDelayImmediate, setting.DelayStrategy)
	assert.Zero(t, setting.FixedDelayMilliseconds)
	assert.Equal(t, int64(250), setting.ExponentialBaseDelayMilliseconds)
	assert.Equal(t, int64(10000), setting.ExponentialMaximumDelayMilliseconds)
	assert.Equal(t, float64(20), setting.JitterPercent)
	assert.False(t, setting.RespectRetryAfter)
	assert.Equal(t, RetryChannelLegacy, setting.ChannelStrategy)
	assert.Equal(t, RetryExhaustedStop, setting.ExhaustedAction)
	assert.False(t, setting.TryOtherKeys)
}

func TestValidateRetryOptionUsesTechnicalBoundsOnly(t *testing.T) {
	assert.NoError(t, ValidateRetryOption("retry_setting.time_budget_seconds", strconv.FormatInt(int64(^uint64(0)>>1), 10)))
	assert.NoError(t, ValidateRetryOption("retry_setting.jitter_percent", "1000000"))
	assert.NoError(t, ValidateRetryOption("retry_setting.delay_strategy", RetryDelayImmediate))
	assert.Error(t, ValidateRetryOption("retry_setting.time_budget_seconds", "-1"))
	assert.Error(t, ValidateRetryOption("retry_setting.fixed_delay_milliseconds", "1.5"))
	assert.Error(t, ValidateRetryOption("retry_setting.jitter_percent", "NaN"))
	assert.Error(t, ValidateRetryOption("retry_setting.delay_strategy", "random"))
	assert.Error(t, ValidateRetryOption("retry_setting.channel_strategy", "round_robin"))
	assert.Error(t, ValidateRetryOption("retry_setting.unknown", "0"))
}

func TestRetrySettingConcurrentReadsObserveCompleteSnapshots(t *testing.T) {
	previous := GetRetrySetting()
	config.GlobalConfig.Update("retry_setting", map[string]string{
		"exponential_base_delay_milliseconds": "1",
		"exponential_max_delay_milliseconds":  "1",
	})
	t.Cleanup(func() {
		config.GlobalConfig.Update("retry_setting", map[string]string{
			"exponential_base_delay_milliseconds": strconv.FormatInt(previous.ExponentialBaseDelayMilliseconds, 10),
			"exponential_max_delay_milliseconds":  strconv.FormatInt(previous.ExponentialMaximumDelayMilliseconds, 10),
		})
	})

	var wg sync.WaitGroup
	start := make(chan struct{})
	errorsFound := make(chan string, 8)
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for iteration := 0; iteration < 200; iteration++ {
				snapshot := GetRetrySetting()
				if snapshot.ExponentialBaseDelayMilliseconds != snapshot.ExponentialMaximumDelayMilliseconds {
					errorsFound <- fmt.Sprintf("mixed retry snapshot: base=%d max=%d", snapshot.ExponentialBaseDelayMilliseconds, snapshot.ExponentialMaximumDelayMilliseconds)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for iteration := 0; iteration < 200; iteration++ {
			value := strconv.Itoa(1 + iteration%2)
			if !config.GlobalConfig.Update("retry_setting", map[string]string{
				"exponential_base_delay_milliseconds": value,
				"exponential_max_delay_milliseconds":  value,
			}) {
				errorsFound <- "retry setting update failed"
				return
			}
		}
	}()
	close(start)
	wg.Wait()
	close(errorsFound)
	for message := range errorsFound {
		assert.Empty(t, message)
	}
}
