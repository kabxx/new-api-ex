package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeChannelAvailabilityNotifyRecipients(t *testing.T) {
	recipients, err := NormalizeChannelAvailabilityNotifyRecipients([]string{
		"First@Example.com; second@example.com",
		"first@example.com\nthird@example.com, SECOND@example.com",
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"First@Example.com", "second@example.com", "third@example.com"}, recipients)
}

func TestNormalizeChannelAvailabilityNotifyRecipientsRejectsEmptyAndInvalid(t *testing.T) {
	_, err := NormalizeChannelAvailabilityNotifyRecipients(nil)
	require.Error(t, err)

	_, err = NormalizeChannelAvailabilityNotifyRecipients([]string{"valid@example.com", "not-an-email"})
	require.Error(t, err)

	_, err = NormalizeChannelAvailabilityNotifyRecipients([]string{"Display Name <valid@example.com>"})
	require.Error(t, err)
}

func TestValidateMonitorAvailabilityNotificationOptions(t *testing.T) {
	require.NoError(t, ValidateMonitorOption("monitor_setting.channel_availability_notify_enabled", "true"))
	require.Error(t, ValidateMonitorOption("monitor_setting.channel_availability_notify_enabled", "sometimes"))
	require.NoError(t, ValidateMonitorOption("monitor_setting.channel_availability_notify_recipients", `[]`))
	require.NoError(t, ValidateMonitorOption("monitor_setting.channel_availability_notify_recipients", `["a@example.com"]`))
	require.Error(t, ValidateMonitorOption("monitor_setting.channel_availability_notify_recipients", `["invalid"]`))
	require.Error(t, ValidateMonitorOption("monitor_setting.channel_availability_notify_recipients", `"a@example.com"`))
	require.Error(t, ValidateMonitorOption("monitor_setting.auto_test_channel_minutes", "NaN"))
	require.Error(t, ValidateMonitorOption("monitor_setting.auto_test_channel_minutes", "+Inf"))
}
