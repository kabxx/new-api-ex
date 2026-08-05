package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelAvailabilityNotificationTest(t *testing.T, enabled bool, recipients []string) {
	t.Helper()
	previousDB := model.DB
	previousSender := sendChannelAvailabilityEmail
	previousSetting := operationSettingSnapshotForTest()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelAvailabilityState{}, &model.ChannelAvailabilityNotificationEvent{}))
	recipientsJSON, err := common.Marshal(recipients)
	require.NoError(t, err)
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"channel_availability_notify_enabled":    common.Interface2String(enabled),
		"channel_availability_notify_recipients": string(recipientsJSON),
	}))
	t.Cleanup(func() {
		model.DB = previousDB
		sendChannelAvailabilityEmail = previousSender
		restoreOperationSettingForTest(previousSetting)
	})
}

type availabilityMonitorSettingSnapshot struct {
	Enabled    bool
	Recipients []string
}

func operationSettingSnapshotForTest() availabilityMonitorSettingSnapshot {
	setting := operation_setting.GetMonitorSettingSnapshot()
	return availabilityMonitorSettingSnapshot{Enabled: setting.ChannelAvailabilityNotifyEnabled, Recipients: setting.ChannelAvailabilityNotifyRecipients}
}

func restoreOperationSettingForTest(snapshot availabilityMonitorSettingSnapshot) {
	recipientsJSON, _ := common.Marshal(snapshot.Recipients)
	config.GlobalConfig.Update("monitor_setting", map[string]string{
		"channel_availability_notify_enabled":    common.Interface2String(snapshot.Enabled),
		"channel_availability_notify_recipients": string(recipientsJSON),
	})
}

func TestEvaluateChannelAvailabilityDisabledStillAdvancesState(t *testing.T) {
	setupChannelAvailabilityNotificationTest(t, false, []string{"admin@example.com"})
	require.NoError(t, model.InitializeChannelAvailabilityState())
	require.NoError(t, model.DB.Create(&model.Channel{Name: "primary", Status: common.ChannelStatusEnabled}).Error)
	sendChannelAvailabilityEmail = func(string, string, string) error {
		t.Fatal("email must not be sent while notifications are disabled")
		return nil
	}

	result, err := EvaluateChannelAvailability(ChannelAvailabilitySourceCreate, nil)
	require.NoError(t, err)
	assert.Nil(t, result.Transition)
	assert.Empty(t, result.Deliveries)
	var state model.ChannelAvailabilityState
	require.NoError(t, model.DB.First(&state).Error)
	assert.True(t, state.Available)
	assert.True(t, state.NotifiedAvailable)
	var eventCount int64
	require.NoError(t, model.DB.Model(&model.ChannelAvailabilityNotificationEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount)

	settingJSON, err := common.Marshal([]string{"admin@example.com"})
	require.NoError(t, err)
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"channel_availability_notify_enabled":    "true",
		"channel_availability_notify_recipients": string(settingJSON),
	}))
	result, err = EvaluateChannelAvailability(ChannelAvailabilitySourceOther, nil)
	require.NoError(t, err)
	assert.Nil(t, result.Transition)
}

func TestEvaluateChannelAvailabilitySendsSeparatelyAndContinuesAfterFailure(t *testing.T) {
	setupChannelAvailabilityNotificationTest(t, true, []string{"first@example.com", "second@example.com"})
	require.NoError(t, model.InitializeChannelAvailabilityState())
	require.NoError(t, model.DB.Create(&model.Channel{Name: "primary", Status: common.ChannelStatusEnabled}).Error)

	var recipients []string
	var capturedContent string
	sendChannelAvailabilityEmail = func(subject, recipient, content string) error {
		recipients = append(recipients, recipient)
		capturedContent = content
		assert.Contains(t, subject, "路由已恢复")
		if recipient == "second@example.com" {
			return errors.New("smtp password=secret")
		}
		return nil
	}

	result, err := EvaluateChannelAvailability(ChannelAvailabilitySourceCreate, []ChannelAvailabilityRelatedChannel{{ID: 1, Name: "<script>alert(1)</script>"}})
	require.NoError(t, err)
	require.Len(t, result.Deliveries, 2)
	assert.Equal(t, []string{"first@example.com", "second@example.com"}, recipients)
	assert.True(t, result.Deliveries[0].Success)
	assert.False(t, result.Deliveries[1].Success)
	assert.Equal(t, "email delivery failed", result.Deliveries[1].Error)
	assert.NotContains(t, capturedContent, "<script>")
	assert.Contains(t, capturedContent, "&lt;script&gt;")
	assert.NotContains(t, result.Deliveries[1].Error, "secret")
	var event model.ChannelAvailabilityNotificationEvent
	require.NoError(t, model.DB.First(&event).Error)
	assert.Equal(t, model.ChannelAvailabilityEventCompleted, event.Status)
	assert.NotContains(t, event.ResultJSON, "secret")
	assert.NotContains(t, event.RelatedChannelsJSON, "key")
}

func TestDrainChannelAvailabilityEventsPreservesRapidEdgeOrder(t *testing.T) {
	setupChannelAvailabilityNotificationTest(t, true, []string{"admin@example.com"})
	require.NoError(t, model.InitializeChannelAvailabilityState())
	channel := model.Channel{Name: "primary", Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(1)}
	require.NoError(t, model.DB.Create(&channel).Error)
	recipientsJSON, err := common.Marshal([]string{"admin@example.com"})
	require.NoError(t, err)
	recovery, err := model.ReconcileChannelAvailabilityNotification(model.ChannelAvailabilityNotificationInput{
		Notify:         true,
		Source:         ChannelAvailabilitySourceCreate,
		RecipientsJSON: string(recipientsJSON),
	})
	require.NoError(t, err)
	require.NotNil(t, recovery)
	require.NoError(t, model.DB.Model(&channel).Update("status", common.ChannelStatusAutoDisabled).Error)
	outage, err := model.ReconcileChannelAvailabilityNotification(model.ChannelAvailabilityNotificationInput{
		Notify:         true,
		Source:         ChannelAvailabilitySourceAutomaticDisable,
		RecipientsJSON: string(recipientsJSON),
	})
	require.NoError(t, err)
	require.NotNil(t, outage)
	occurredAt := time.Unix(1_700_000_000, 0)
	require.NoError(t, model.DB.Model(&model.ChannelAvailabilityNotificationEvent{}).
		Where("id = ?", recovery.ID).
		Update("created_at", occurredAt.Unix()).Error)

	var subjects []string
	var contents []string
	sendChannelAvailabilityEmail = func(subject, recipient, content string) error {
		subjects = append(subjects, subject)
		contents = append(contents, content)
		return nil
	}
	results, err := DrainChannelAvailabilityNotificationEvents()
	require.NoError(t, err)
	assert.Len(t, results, 2)
	require.Len(t, subjects, 2)
	assert.Contains(t, subjects[0], "路由已恢复")
	assert.Contains(t, subjects[1], "所有路由不可用")
	assert.Contains(t, contents[0], occurredAt.Format(time.RFC1123Z))
	var events []model.ChannelAvailabilityNotificationEvent
	require.NoError(t, model.DB.Order("notification_revision asc").Find(&events).Error)
	require.Len(t, events, 2)
	assert.Equal(t, model.ChannelAvailabilityEventCompleted, events[0].Status)
	assert.Equal(t, model.ChannelAvailabilityEventCompleted, events[1].Status)
}

func TestDrainChannelAvailabilityEventsDoesNotSendWhileDisabled(t *testing.T) {
	setupChannelAvailabilityNotificationTest(t, false, []string{"admin@example.com"})
	require.NoError(t, model.InitializeChannelAvailabilityState())
	require.NoError(t, model.DB.Create(&model.Channel{Name: "primary", Status: common.ChannelStatusEnabled}).Error)
	event, err := model.ReconcileChannelAvailabilityNotification(model.ChannelAvailabilityNotificationInput{
		Notify:         true,
		RecipientsJSON: `["admin@example.com"]`,
	})
	require.NoError(t, err)
	require.NotNil(t, event)
	sendChannelAvailabilityEmail = func(string, string, string) error {
		t.Fatal("disabled notifications must not be delivered")
		return nil
	}

	results, err := DrainChannelAvailabilityNotificationEvents()
	require.NoError(t, err)
	assert.Empty(t, results)
	require.NoError(t, model.DB.First(event, event.ID).Error)
	assert.Equal(t, model.ChannelAvailabilityEventPending, event.Status)
}

func TestResumeChannelAvailabilityEventsRecoversExpiredLease(t *testing.T) {
	setupChannelAvailabilityNotificationTest(t, true, []string{"admin@example.com"})
	require.NoError(t, model.InitializeChannelAvailabilityState())
	require.NoError(t, model.DB.Create(&model.Channel{Name: "primary", Status: common.ChannelStatusEnabled}).Error)
	recipientsJSON, err := common.Marshal([]string{"admin@example.com"})
	require.NoError(t, err)
	event, err := model.ReconcileChannelAvailabilityNotification(model.ChannelAvailabilityNotificationInput{
		Notify:         true,
		Source:         ChannelAvailabilitySourceCreate,
		RecipientsJSON: string(recipientsJSON),
	})
	require.NoError(t, err)
	require.NotNil(t, event)
	require.NoError(t, model.DB.Model(&model.ChannelAvailabilityNotificationEvent{}).
		Where("id = ?", event.ID).
		Updates(map[string]any{
			"status":      model.ChannelAvailabilityEventProcessing,
			"owner":       "stopped-instance",
			"lease_until": time.Now().Add(-time.Minute).Unix(),
		}).Error)
	sendChannelAvailabilityEmail = func(subject, recipient, content string) error { return nil }

	resumeChannelAvailabilityNotificationEvents()

	var stored model.ChannelAvailabilityNotificationEvent
	require.NoError(t, model.DB.First(&stored, event.ID).Error)
	assert.Equal(t, model.ChannelAvailabilityEventCompleted, stored.Status)
}

func TestSendChannelAvailabilityTestEmailsDoesNotChangeState(t *testing.T) {
	setupChannelAvailabilityNotificationTest(t, true, []string{"saved@example.com"})
	require.NoError(t, model.InitializeChannelAvailabilityState())
	var before model.ChannelAvailabilityState
	require.NoError(t, model.DB.First(&before).Error)
	sendChannelAvailabilityEmail = func(subject, recipient, content string) error {
		assert.Contains(t, subject, "测试")
		assert.Equal(t, "test@example.com", recipient)
		assert.True(t, strings.Contains(content, "测试邮件"))
		return nil
	}

	results, err := SendChannelAvailabilityTestEmails([]string{"test@example.com"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].Success)
	var after model.ChannelAvailabilityState
	require.NoError(t, model.DB.First(&after).Error)
	assert.Equal(t, before, after)
	var eventCount int64
	require.NoError(t, model.DB.Model(&model.ChannelAvailabilityNotificationEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
}

func TestDeferredChannelChangesAreEvaluatedOnceAfterHealthCycle(t *testing.T) {
	setupChannelAvailabilityNotificationTest(t, true, []string{"admin@example.com"})
	previousAutomaticDisable := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = previousAutomaticDisable })
	channels := []model.Channel{
		{Name: "primary", Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(1)},
		{Name: "secondary", Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(1)},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.InitializeChannelAvailabilityState())
	deliveries := 0
	sendChannelAvailabilityEmail = func(subject, recipient, content string) error {
		deliveries++
		assert.Contains(t, subject, "所有路由不可用")
		return nil
	}

	assert.True(t, DisableChannelDeferredAvailability(
		*types.NewChannelError(channels[0].Id, channels[0].Type, channels[0].Name, false, "", true),
		"health check failed",
	))
	assert.True(t, DisableChannelDeferredAvailability(
		*types.NewChannelError(channels[1].Id, channels[1].Type, channels[1].Name, false, "", true),
		"health check failed",
	))
	assert.Zero(t, deliveries)

	result, err := EvaluateChannelAvailability(ChannelAvailabilitySourceHealthCheck, []ChannelAvailabilityRelatedChannel{
		{ID: channels[0].Id, Name: channels[0].Name},
		{ID: channels[1].Id, Name: channels[1].Name},
	})
	require.NoError(t, err)
	require.NotNil(t, result.Transition)
	assert.False(t, result.Transition.ToAvailable)
	assert.Equal(t, 1, deliveries)

	result, err = EvaluateChannelAvailability(ChannelAvailabilitySourceHealthCheck, nil)
	require.NoError(t, err)
	assert.Nil(t, result.Transition)
	assert.Equal(t, 1, deliveries)
}

func TestPartialHealthCheckDeliversPersistedOutageOnce(t *testing.T) {
	setupChannelAvailabilityNotificationTest(t, true, []string{"admin@example.com"})
	channel := model.Channel{Name: "last-enabled", Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(1)}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.InitializeChannelAvailabilityState())
	require.NoError(t, model.DB.Model(&channel).Update("status", common.ChannelStatusAutoDisabled).Error)

	deliveries := 0
	sendChannelAvailabilityEmail = func(subject, recipient, content string) error {
		deliveries++
		assert.Contains(t, subject, "所有路由不可用")
		assert.Contains(t, content, "已取消或部分完成的渠道健康检查")
		return nil
	}

	result, err := EvaluateChannelAvailability(ChannelAvailabilitySourceHealthCheckPartial, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Transition)
	assert.Equal(t, 1, deliveries)

	result, err = EvaluateChannelAvailability(ChannelAvailabilitySourceHealthCheckPartial, nil)
	require.NoError(t, err)
	assert.Nil(t, result.Transition)
	assert.Equal(t, 1, deliveries)
}

func TestAutomaticDisableSynchronizesOverallAvailability(t *testing.T) {
	setupChannelAvailabilityNotificationTest(t, false, []string{"admin@example.com"})
	require.NoError(t, model.DB.AutoMigrate(&model.User{}))
	require.NoError(t, model.DB.Create(&model.User{
		Username: "root",
		Role:     common.RoleRootUser,
		Status:   common.UserStatusEnabled,
	}).Error)
	channel := model.Channel{Name: "primary", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.InitializeChannelAvailabilityState())
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })

	changed := disableChannel(*types.NewChannelError(channel.Id, channel.Type, channel.Name, false, "", true), "upstream unavailable", true)

	assert.True(t, changed)
	var state model.ChannelAvailabilityState
	require.NoError(t, model.DB.First(&state).Error)
	assert.False(t, state.Available)
}
