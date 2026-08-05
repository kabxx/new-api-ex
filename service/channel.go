package service

import (
	"context"
	"fmt"
	"strings"
	"time"

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
func DisableChannel(channelError types.ChannelError, reason string) bool {
	return disableChannel(channelError, reason, true)
}

func DisableChannelDeferredAvailability(channelError types.ChannelError, reason string) bool {
	return disableChannel(channelError, reason, false)
}

func DisableChannelWithClaim(channelError types.ChannelError, reason string, claimToken string, evaluateAvailability bool) bool {
	return disableChannelClaimed(channelError, reason, evaluateAvailability, claimToken)
}

func disableChannel(channelError types.ChannelError, reason string, evaluateAvailability bool) bool {
	return disableChannelClaimed(channelError, reason, evaluateAvailability, "")
}

func disableChannelClaimed(channelError types.ChannelError, reason string, evaluateAvailability bool, claimToken string) bool {
	common.SysLog(fmt.Sprintf("通道「%s」（#%d）发生错误，准备禁用，原因：%s", channelError.ChannelName, channelError.ChannelId, common.LocalLogPreview(reason)))

	// The callers decide whether the automatic policy is enabled. This service
	// function remains a direct status mutation API and only honors the channel
	// level AutoBan flag, preserving existing callers and tests.
	if !channelError.AutoBan {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）未启用自动禁用功能，跳过禁用操作", channelError.ChannelName, channelError.ChannelId))
		return false
	}

	var mutation model.ChannelStatusMutation
	var err error
	if claimToken == "" {
		mutation, err = model.ApplyAutomaticChannelStatus(channelError.ChannelId, channelError.UsingKey, common.ChannelStatusAutoDisabled, reason)
	} else if model.PersistentChannelFailureStateAvailable() {
		mutation, err = model.ApplyAutomaticChannelStatusWithClaim(channelError.ChannelId, channelError.UsingKey, common.ChannelStatusAutoDisabled, reason, claimToken)
	} else {
		key := channelFailureKey(channelError.ChannelId, channelError.UsingKey)
		channelFailCounts.Lock()
		state := channelFailCounts.counts[key]
		claimActive := state != nil && state.ThresholdReached && state.Claimed && state.ClaimToken == claimToken &&
			!state.ClaimedAt.IsZero() && channelFailureNow().Sub(state.ClaimedAt) < 2*time.Minute
		if claimActive {
			mutation, err = model.ApplyAutomaticChannelStatus(channelError.ChannelId, channelError.UsingKey, common.ChannelStatusAutoDisabled, reason)
		}
		channelFailCounts.Unlock()
	}
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to auto-disable channel #%d: %v", channelError.ChannelId, err))
		return false
	}
	if mutation.ChannelChanged {
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
	return mutation.Changed()
}

func EnableChannel(channelId int, usingKey string, channelName string) {
	enableChannel(channelId, usingKey, channelName, true)
}

func EnableChannelDeferredAvailability(channelId int, usingKey string, channelName string) bool {
	return enableChannel(channelId, usingKey, channelName, false)
}

func enableChannel(channelId int, usingKey string, channelName string, evaluateAvailability bool) bool {
	mutation, err := model.ApplyAutomaticChannelStatus(channelId, usingKey, common.ChannelStatusEnabled, "")
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to auto-enable channel #%d: %v", channelId, err))
		return false
	}
	if mutation.ChannelChanged {
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
	return mutation.Changed()
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

// IsAttributableChannelFailure separates an upstream attempt outcome from
// retry policy.  Retryability is deliberately not used here: a 524, a 401,
// and a provider transport error can all be evidence against a channel,
// while request construction and client cancellation cannot.
func IsAttributableChannelFailure(ctx context.Context, err *types.NewAPIError) bool {
	if err == nil || (ctx != nil && ctx.Err() != nil) {
		return false
	}
	switch err.GetErrorCode() {
	case types.ErrorCodeInvalidRequest,
		types.ErrorCodeSensitiveWordsDetected,
		types.ErrorCodeCountTokenFailed,
		types.ErrorCodeModelPriceError,
		types.ErrorCodeInvalidApiType,
		types.ErrorCodeJsonMarshalFailed,
		types.ErrorCodeGetChannelFailed,
		types.ErrorCodeGenRelayInfoFailed,
		types.ErrorCodeReadRequestBodyFailed,
		types.ErrorCodeConvertRequestFailed,
		types.ErrorCodeAccessDenied,
		types.ErrorCodeBadRequestBody,
		types.ErrorCodePreConsumeTokenQuotaFailed:
		return false
	case types.ErrorCodeDoRequestFailed:
		return err.StatusCode != 499
	default:
		return true
	}
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
