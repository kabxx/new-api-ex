package operation_setting

import (
	"fmt"
	"math"
	"net/mail"
	"os"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

type MonitorSetting struct {
	AutoTestChannelEnabled              bool     `json:"auto_test_channel_enabled"`
	AutoTestChannelMinutes              float64  `json:"auto_test_channel_minutes"`
	ChannelTestMode                     string   `json:"channel_test_mode"`
	ZeroTokenAsFailure                  bool     `json:"zero_token_as_failure"`
	ChannelAvailabilityNotifyEnabled    bool     `json:"channel_availability_notify_enabled"`
	ChannelAvailabilityNotifyRecipients []string `json:"channel_availability_notify_recipients"`
}

const (
	ChannelTestModeScheduledAll    = "scheduled_all"
	ChannelTestModePassiveRecovery = "passive_recovery"
)

// 默认配置
var monitorSetting = MonitorSetting{
	AutoTestChannelEnabled:              false,
	AutoTestChannelMinutes:              10,
	ChannelTestMode:                     ChannelTestModeScheduledAll,
	ZeroTokenAsFailure:                  false,
	ChannelAvailabilityNotifyEnabled:    false,
	ChannelAvailabilityNotifyRecipients: []string{},
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("monitor_setting", &monitorSetting)
}

func GetMonitorSetting() *MonitorSetting {
	setting := GetMonitorSettingSnapshot()
	if os.Getenv("CHANNEL_TEST_FREQUENCY") != "" {
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY"))
		if err == nil && frequency > 0 {
			setting.AutoTestChannelEnabled = true
			setting.AutoTestChannelMinutes = float64(frequency)
			setting.ChannelTestMode = ChannelTestModeScheduledAll
		}
	}
	if enabled, ok := os.LookupEnv("CHANNEL_TEST_ENABLED"); ok {
		parsed, err := strconv.ParseBool(enabled)
		if err == nil {
			setting.AutoTestChannelEnabled = parsed
		}
	}
	if setting.ChannelTestMode != ChannelTestModePassiveRecovery {
		setting.ChannelTestMode = ChannelTestModeScheduledAll
	}
	return &setting
}

func GetMonitorSettingSnapshot() MonitorSetting {
	setting := MonitorSetting{}
	if config.GlobalConfig.CopyInto("monitor_setting", &setting) {
		setting.ChannelAvailabilityNotifyRecipients = append([]string(nil), setting.ChannelAvailabilityNotifyRecipients...)
		return setting
	}
	setting = monitorSetting
	setting.ChannelAvailabilityNotifyRecipients = append([]string(nil), monitorSetting.ChannelAvailabilityNotifyRecipients...)
	return setting
}

func NormalizeChannelAvailabilityNotifyRecipients(values []string) ([]string, error) {
	recipients := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		parts := strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r'
		})
		for _, part := range parts {
			candidate := strings.TrimSpace(part)
			if candidate == "" {
				continue
			}
			parsed, err := mail.ParseAddress(candidate)
			if err != nil || !strings.EqualFold(parsed.Address, candidate) {
				return nil, fmt.Errorf("invalid notification email address: %s", candidate)
			}
			dedupeKey := strings.ToLower(parsed.Address)
			if _, ok := seen[dedupeKey]; ok {
				continue
			}
			seen[dedupeKey] = struct{}{}
			recipients = append(recipients, parsed.Address)
		}
	}
	if len(recipients) == 0 {
		return nil, fmt.Errorf("at least one notification email address is required")
	}
	return recipients, nil
}

func ValidateMonitorOption(key, value string) error {
	switch key {
	case "monitor_setting.auto_test_channel_enabled", "monitor_setting.zero_token_as_failure":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s must be a boolean", key)
		}
	case "monitor_setting.auto_test_channel_minutes":
		minutes, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes <= 0 {
			return fmt.Errorf("%s must be a positive finite number", key)
		}
	case "monitor_setting.channel_test_mode":
		if value != ChannelTestModeScheduledAll && value != ChannelTestModePassiveRecovery {
			return fmt.Errorf("%s has an invalid mode", key)
		}
	case "monitor_setting.channel_availability_notify_enabled":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s must be a boolean", key)
		}
	case "monitor_setting.channel_availability_notify_recipients":
		var recipients []string
		if err := common.UnmarshalJsonStr(value, &recipients); err != nil {
			return fmt.Errorf("%s must be a JSON string array", key)
		}
		if len(recipients) == 0 {
			return nil
		}
		_, err := NormalizeChannelAvailabilityNotifyRecipients(recipients)
		return err
	default:
		return fmt.Errorf("unknown monitor setting: %s", key)
	}
	return nil
}
