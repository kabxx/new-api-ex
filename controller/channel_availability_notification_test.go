package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelAvailabilityNotificationTestRejectsInvalidRecipients(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/option/channel-availability-notification/test",
		strings.NewReader(`{"recipients":["not-an-email"]}`),
	)

	TestChannelAvailabilityNotification(context)

	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.False(t, payload.Success)
	assert.Contains(t, payload.Message, "invalid notification email address")
}

func setupChannelAvailabilityMutationControllerTest(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ChannelAvailabilityState{}, &model.ChannelAvailabilityNotificationEvent{}))
	previousSetting := operation_setting.GetMonitorSettingSnapshot()
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"channel_availability_notify_enabled":    "false",
		"channel_availability_notify_recipients": `[]`,
	}))
	t.Cleanup(func() {
		recipients, _ := common.Marshal(previousSetting.ChannelAvailabilityNotifyRecipients)
		config.GlobalConfig.Update("monitor_setting", map[string]string{
			"channel_availability_notify_enabled":    common.Interface2String(previousSetting.ChannelAvailabilityNotifyEnabled),
			"channel_availability_notify_recipients": string(recipients),
		})
	})
	return db
}

func requirePersistedChannelAvailability(t *testing.T, db *gorm.DB, available bool) {
	t.Helper()
	var state model.ChannelAvailabilityState
	require.NoError(t, db.First(&state, "id = ?", 1).Error)
	assert.Equal(t, available, state.Available)
}

func TestChannelMutationHandlersSynchronizeAvailabilityState(t *testing.T) {
	t.Run("add first enabled channel", func(t *testing.T) {
		db := setupChannelAvailabilityMutationControllerTest(t)
		require.NoError(t, model.InitializeChannelAvailabilityState())
		payload, err := common.Marshal(AddChannelRequest{
			Mode: "single",
			Channel: &model.Channel{
				Name:   "primary",
				Type:   1,
				Key:    "sk-test",
				Status: common.ChannelStatusEnabled,
				Models: "gpt-test",
				Group:  "default",
			},
		})
		require.NoError(t, err)
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPost, "/api/channel/", strings.NewReader(string(payload)))

		AddChannel(context)

		assert.Equal(t, http.StatusOK, response.Code)
		requirePersistedChannelAvailability(t, db, true)
	})

	t.Run("manual status and delete", func(t *testing.T) {
		db := setupChannelAvailabilityMutationControllerTest(t)
		channel := model.Channel{Name: "primary", Status: common.ChannelStatusEnabled}
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, model.InitializeChannelAvailabilityState())

		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", channel.Id)}}
		context.Request = httptest.NewRequest(http.MethodPost, "/api/channel/status", strings.NewReader(`{"status":2}`))
		UpdateChannelStatus(context)
		requirePersistedChannelAvailability(t, db, false)

		require.NoError(t, db.Model(&channel).Update("status", common.ChannelStatusEnabled).Error)
		require.NoError(t, model.SyncChannelAvailabilityState())
		response = httptest.NewRecorder()
		context, _ = gin.CreateTestContext(response)
		context.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", channel.Id)}}
		context.Request = httptest.NewRequest(http.MethodDelete, "/api/channel/", nil)
		DeleteChannel(context)
		requirePersistedChannelAvailability(t, db, false)
	})

	t.Run("tag and batch operations", func(t *testing.T) {
		db := setupChannelAvailabilityMutationControllerTest(t)
		tag := "critical"
		channels := []model.Channel{
			{Name: "one", Status: common.ChannelStatusEnabled, Tag: &tag},
			{Name: "two", Status: common.ChannelStatusEnabled, Tag: &tag},
		}
		require.NoError(t, db.Create(&channels).Error)
		require.NoError(t, model.InitializeChannelAvailabilityState())

		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPost, "/api/channel/tag/disabled", strings.NewReader(`{"tag":"critical"}`))
		DisableTagChannels(context)
		requirePersistedChannelAvailability(t, db, false)

		response = httptest.NewRecorder()
		context, _ = gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPost, "/api/channel/tag/enabled", strings.NewReader(`{"tag":"critical"}`))
		EnableTagChannels(context)
		requirePersistedChannelAvailability(t, db, true)

		ids := []int{channels[0].Id, channels[1].Id}
		payload, err := common.Marshal(ChannelStatusBatchRequest{Ids: ids, Status: common.ChannelStatusManuallyDisabled})
		require.NoError(t, err)
		response = httptest.NewRecorder()
		context, _ = gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPost, "/api/channel/status/batch", strings.NewReader(string(payload)))
		BatchUpdateChannelStatus(context)
		requirePersistedChannelAvailability(t, db, false)
	})
}

func TestCancelledChannelHealthCycleClaimsPersistedAvailabilityEdge(t *testing.T) {
	db := setupChannelAvailabilityMutationControllerTest(t)
	channel := model.Channel{Name: "partially-checked", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, model.InitializeChannelAvailabilityState())
	require.NoError(t, db.Model(&channel).Update("status", common.ChannelStatusAutoDisabled).Error)

	finalizeChannelHealthAvailability(false, []service.ChannelAvailabilityRelatedChannel{{ID: 1, Name: "partially-checked"}})

	requirePersistedChannelAvailability(t, db, false)
	transition, err := model.ClaimChannelAvailabilityTransition()
	require.NoError(t, err)
	assert.Nil(t, transition)
}

func TestUpdateOptionAcceptsRecipientArray(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.ChannelAvailabilityState{}, &model.ChannelAvailabilityNotificationEvent{}))
	previousOptionMap := common.OptionMap
	previousSetting := operation_setting.GetMonitorSettingSnapshot()
	common.OptionMap = make(map[string]string)
	t.Cleanup(func() {
		common.OptionMap = previousOptionMap
		recipients, _ := common.Marshal(previousSetting.ChannelAvailabilityNotifyRecipients)
		config.GlobalConfig.Update("monitor_setting", map[string]string{
			"channel_availability_notify_enabled":    common.Interface2String(previousSetting.ChannelAvailabilityNotifyEnabled),
			"channel_availability_notify_recipients": string(recipients),
		})
	})

	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/",
		strings.NewReader(`{"key":"monitor_setting.channel_availability_notify_recipients","value":["First@example.com","second@example.com"]}`),
	)

	UpdateOption(context)

	var payload struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	var option model.Option
	require.NoError(t, db.Where("key = ?", "monitor_setting.channel_availability_notify_recipients").First(&option).Error)
	assert.JSONEq(t, `["First@example.com","second@example.com"]`, option.Value)
}

func TestUpdateOptionsBulkEnablesAvailabilityNotificationAtomically(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.ChannelAvailabilityState{}, &model.ChannelAvailabilityNotificationEvent{}))
	previousOptionMap := common.OptionMap
	previousSetting := operation_setting.GetMonitorSettingSnapshot()
	common.OptionMap = map[string]string{
		"monitor_setting.channel_availability_notify_enabled":    "false",
		"monitor_setting.channel_availability_notify_recipients": `[]`,
	}
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"channel_availability_notify_enabled":    "false",
		"channel_availability_notify_recipients": `[]`,
	}))
	t.Cleanup(func() {
		common.OptionMap = previousOptionMap
		recipients, _ := common.Marshal(previousSetting.ChannelAvailabilityNotifyRecipients)
		config.GlobalConfig.Update("monitor_setting", map[string]string{
			"channel_availability_notify_enabled":    common.Interface2String(previousSetting.ChannelAvailabilityNotifyEnabled),
			"channel_availability_notify_recipients": string(recipients),
		})
	})

	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/bulk",
		strings.NewReader(`{"options":{"monitor_setting.channel_availability_notify_enabled":"true","monitor_setting.channel_availability_notify_recipients":"[\"First@Example.com\"]"}}`),
	)

	UpdateOptionsBulk(context)

	var payload struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	setting := operation_setting.GetMonitorSettingSnapshot()
	assert.True(t, setting.ChannelAvailabilityNotifyEnabled)
	assert.Equal(t, []string{"First@Example.com"}, setting.ChannelAvailabilityNotifyRecipients)

	var options []model.Option
	require.NoError(t, db.Order("key").Find(&options).Error)
	require.Len(t, options, 2)
}

func TestUpdateOptionsBulkRejectsUnknownOption(t *testing.T) {
	previousOptionMap := common.OptionMap
	common.OptionMap = map[string]string{"SystemName": "existing"}
	t.Cleanup(func() { common.OptionMap = previousOptionMap })

	for _, body := range []string{
		`{"options":{"unknown":"value"}}`,
		`{"options":{"SystemName":"not-allowed-even-when-existing"}}`,
		`{"options":{}}`,
	} {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPut, "/api/option/bulk", strings.NewReader(body))
		UpdateOptionsBulk(context)

		var payload struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
		assert.False(t, payload.Success)
		assert.NotEmpty(t, payload.Message)
	}
	assert.Equal(t, "existing", common.OptionMap["SystemName"])
}

func TestUpdateOptionsBulkRejectsInvalidValueWithoutPersistence(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.ChannelAvailabilityState{}, &model.ChannelAvailabilityNotificationEvent{}))
	previousOptionMap := common.OptionMap
	previousRetryTimes := common.RetryTimes
	common.OptionMap = map[string]string{"RetryTimes": "10"}
	common.RetryTimes = 10
	t.Cleanup(func() {
		common.OptionMap = previousOptionMap
		common.RetryTimes = previousRetryTimes
	})

	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/bulk",
		strings.NewReader(`{"options":{"RetryTimes":"17","AutomaticDisableStatusCodes":"99"}}`),
	)
	UpdateOptionsBulk(context)

	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.False(t, payload.Success)
	assert.NotEmpty(t, payload.Message)
	var count int64
	require.NoError(t, db.Model(&model.Option{}).Count(&count).Error)
	assert.Zero(t, count)
	assert.Equal(t, 10, common.RetryTimes)
	assert.Equal(t, "10", common.OptionMap["RetryTimes"])
}

func TestChannelAvailabilityNotificationTestUsesSavedRecipientsWhenOmitted(t *testing.T) {
	db := setupChannelAvailabilityMutationControllerTest(t)
	require.NoError(t, model.InitializeChannelAvailabilityState())
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{
		"channel_availability_notify_recipients": `["saved@example.com"]`,
	}))
	previousSMTPServer := common.SMTPServer
	previousSMTPAccount := common.SMTPAccount
	previousSMTPFrom := common.SMTPFrom
	common.SMTPServer = ""
	common.SMTPAccount = ""
	common.SMTPFrom = ""
	t.Cleanup(func() {
		common.SMTPServer = previousSMTPServer
		common.SMTPAccount = previousSMTPAccount
		common.SMTPFrom = previousSMTPFrom
	})

	var before model.ChannelAvailabilityState
	require.NoError(t, db.First(&before, "id = ?", 1).Error)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/option/channel-availability-notification/test",
		http.NoBody,
	)
	context.Request.ContentLength = -1

	TestChannelAvailabilityNotification(context)

	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			Failed  int `json:"failed"`
			Results []struct {
				Recipient string `json:"recipient"`
				Error     string `json:"error"`
			} `json:"results"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.False(t, payload.Success)
	assert.Equal(t, 1, payload.Data.Failed)
	require.Len(t, payload.Data.Results, 1)
	assert.Equal(t, "saved@example.com", payload.Data.Results[0].Recipient)
	assert.Equal(t, "email delivery failed", payload.Data.Results[0].Error)
	var after model.ChannelAvailabilityState
	require.NoError(t, db.First(&after, "id = ?", 1).Error)
	assert.Equal(t, before, after)
}
