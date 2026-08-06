package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorSettingZeroTokenAsFailureDefaultsFalse(t *testing.T) {
	assert.False(t, monitorSetting.ZeroTokenAsFailure)
	assert.Zero(t, monitorSetting.FirstTokenTimeoutSeconds)
}

func TestValidateMonitorOptionFirstTokenTimeoutBounds(t *testing.T) {
	for _, value := range []string{"0", "1", "600"} {
		require.NoError(t, ValidateMonitorOption("monitor_setting.first_token_timeout_seconds", value))
	}
	for _, value := range []string{"-1", "601", "1.5", "NaN", ""} {
		require.Error(t, ValidateMonitorOption("monitor_setting.first_token_timeout_seconds", value))
	}
}

func TestGetMonitorSetting_ChannelTestEnabledEnvOverridesEnabledConfig(t *testing.T) {
	orig := monitorSetting
	t.Cleanup(func() { monitorSetting = orig })

	t.Setenv("CHANNEL_TEST_ENABLED", "false")
	t.Setenv("CHANNEL_TEST_FREQUENCY", "5")
	monitorSetting = MonitorSetting{
		AutoTestChannelEnabled: true,
		AutoTestChannelMinutes: 20,
	}

	setting := GetMonitorSetting()

	require.NotNil(t, setting)
	assert.False(t, setting.AutoTestChannelEnabled)
	assert.Equal(t, float64(5), setting.AutoTestChannelMinutes)
}

func TestGetMonitorSetting_ChannelTestEnabledEnvCanEnableDisabledConfig(t *testing.T) {
	orig := monitorSetting
	t.Cleanup(func() { monitorSetting = orig })

	t.Setenv("CHANNEL_TEST_ENABLED", "true")
	monitorSetting = MonitorSetting{
		AutoTestChannelEnabled: false,
		AutoTestChannelMinutes: 12,
	}

	setting := GetMonitorSetting()

	require.NotNil(t, setting)
	assert.True(t, setting.AutoTestChannelEnabled)
	assert.Equal(t, float64(12), setting.AutoTestChannelMinutes)
}
