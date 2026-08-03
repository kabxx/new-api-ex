package service

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func formatNotifyType(channelId int, status int) string {
	return fmt.Sprintf("%s_%d_%d", dto.NotifyTypeChannelUpdate, channelId, status)
}

// disable & notify
func DisableChannel(channelError types.ChannelError, reason string) {
	disableChannel(channelError, reason, true)
}

func DisableChannelDeferredAvailability(channelError types.ChannelError, reason string) bool {
	return disableChannel(channelError, reason, false)
}

func disableChannel(channelError types.ChannelError, reason string, evaluateAvailability bool) bool {
	common.SysLog(fmt.Sprintf("通道「%s」（#%d）发生错误，准备禁用，原因：%s", channelError.ChannelName, channelError.ChannelId, common.LocalLogPreview(reason)))

	// 检查是否启用自动禁用功能
	if !channelError.AutoBan {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）未启用自动禁用功能，跳过禁用操作", channelError.ChannelName, channelError.ChannelId))
		return false
	}

	success := model.UpdateChannelStatus(channelError.ChannelId, channelError.UsingKey, common.ChannelStatusAutoDisabled, reason)
	if success {
		subject := fmt.Sprintf("通道「%s」（#%d）已被禁用", channelError.ChannelName, channelError.ChannelId)
		content := fmt.Sprintf("通道「%s」（#%d）已被禁用，原因：%s", channelError.ChannelName, channelError.ChannelId, reason)
		NotifyRootUser(formatNotifyType(channelError.ChannelId, common.ChannelStatusAutoDisabled), subject, content)
		if evaluateAvailability {
			_, err := EvaluateChannelAvailability(ChannelAvailabilitySourceAutomaticDisable, []ChannelAvailabilityRelatedChannel{{ID: channelError.ChannelId, Name: channelError.ChannelName}})
			if err != nil {
				common.SysLog("failed to evaluate channel availability after disable: " + err.Error())
			}
		}
	}
	return success
}

func EnableChannel(channelId int, usingKey string, channelName string) {
	enableChannel(channelId, usingKey, channelName, true)
}

func EnableChannelDeferredAvailability(channelId int, usingKey string, channelName string) bool {
	return enableChannel(channelId, usingKey, channelName, false)
}

func enableChannel(channelId int, usingKey string, channelName string, evaluateAvailability bool) bool {
	success := model.UpdateChannelStatus(channelId, usingKey, common.ChannelStatusEnabled, "")
	if success {
		subject := fmt.Sprintf("通道「%s」（#%d）已被启用", channelName, channelId)
		content := fmt.Sprintf("通道「%s」（#%d）已被启用", channelName, channelId)
		NotifyRootUser(formatNotifyType(channelId, common.ChannelStatusEnabled), subject, content)
		if evaluateAvailability {
			_, err := EvaluateChannelAvailability(ChannelAvailabilitySourceOther, []ChannelAvailabilityRelatedChannel{{ID: channelId, Name: channelName}})
			if err != nil {
				common.SysLog("failed to evaluate channel availability after enable: " + err.Error())
			}
		}
	}
	return success
}

func ShouldDisableChannel(err *types.NewAPIError) bool {
	if !common.AutomaticDisableChannelEnabled {
		return false
	}
	if err == nil {
		return false
	}
	if types.IsChannelError(err) {
		return true
	}
	if types.IsSkipRetryError(err) {
		return false
	}
	if operation_setting.ShouldDisableByStatusCode(err.StatusCode) {
		return true
	}

	lowerMessage := strings.ToLower(err.Error())
	search, _ := AcSearch(lowerMessage, operation_setting.AutomaticDisableKeywords, true)
	return search
}

func ShouldEnableChannel(newAPIError *types.NewAPIError, status int) bool {
	if !common.AutomaticEnableChannelEnabled {
		return false
	}
	if newAPIError != nil {
		return false
	}
	if status != common.ChannelStatusAutoDisabled {
		return false
	}
	return true
}
