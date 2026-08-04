package controller

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func relayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	switch info.RelayMode {
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits:
		err = relay.ImageHelper(c, info)
	case relayconstant.RelayModeAudioSpeech:
		fallthrough
	case relayconstant.RelayModeAudioTranslation:
		fallthrough
	case relayconstant.RelayModeAudioTranscription:
		err = relay.AudioHelper(c, info)
	case relayconstant.RelayModeRerank:
		err = relay.RerankHelper(c, info)
	case relayconstant.RelayModeEmbeddings:
		err = relay.EmbeddingHelper(c, info)
	case relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
		err = relay.ResponsesHelper(c, info)
	case relayconstant.RelayModeAlphaSearch:
		err = relay.AlphaSearchHelper(c, info)
	default:
		err = relay.TextHelper(c, info)
	}
	return err
}

func geminiRelayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	if strings.Contains(c.Request.URL.Path, "embed") {
		err = relay.GeminiEmbeddingHandler(c, info)
	} else {
		err = relay.GeminiHelper(c, info)
	}
	return err
}

func Relay(c *gin.Context, relayFormat types.RelayFormat) {

	requestId := c.GetString(common.RequestIdKey)
	//group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	//originalModel := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)

	var (
		newAPIError *types.NewAPIError
		ws          *websocket.Conn
	)

	if relayFormat == types.RelayFormatOpenAIRealtime {
		var err error
		ws, err = upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			helper.WssError(c, ws, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry()).ToOpenAIError())
			return
		}
		defer ws.Close()
	}

	defer func() {
		if newAPIError != nil {
			logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
			newAPIError.SetMessage(common.MessageWithRequestId(newAPIError.Error(), requestId))
			switch relayFormat {
			case types.RelayFormatOpenAIRealtime:
				helper.WssError(c, ws, newAPIError.ToOpenAIError())
			case types.RelayFormatClaude:
				c.JSON(newAPIError.StatusCode, gin.H{
					"type":  "error",
					"error": newAPIError.ToClaudeError(),
				})
			default:
				c.JSON(newAPIError.StatusCode, gin.H{
					"error": newAPIError.ToOpenAIError(),
				})
			}
		}
	}()

	request, err := helper.GetAndValidateRequest(c, relayFormat)
	if err != nil {
		// Map "request body too large" to 413 so clients can handle it correctly
		if common.IsRequestBodyTooLargeError(err) || errors.Is(err, common.ErrRequestBodyTooLarge) {
			newAPIError = types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
		} else {
			newAPIError = types.NewError(err, types.ErrorCodeInvalidRequest)
		}
		return
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, relayFormat, request, ws)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeGenRelayInfoFailed)
		return
	}

	needSensitiveCheck := setting.ShouldCheckPromptSensitive()
	needCountToken := constant.CountToken
	// Avoid building huge CombineText (strings.Join) when token counting and sensitive check are both disabled.
	var meta *types.TokenCountMeta
	if needSensitiveCheck || needCountToken {
		meta = request.GetTokenCountMeta()
	} else {
		meta = fastTokenCountMetaForPricing(request)
	}

	if needSensitiveCheck && meta != nil {
		contains, words := service.CheckSensitiveText(meta.CombineText)
		if contains {
			logger.LogWarn(c, fmt.Sprintf("user sensitive words detected: %s", strings.Join(words, ", ")))
			newAPIError = types.NewError(err, types.ErrorCodeSensitiveWordsDetected)
			return
		}
	}

	tokens, err := service.EstimateRequestToken(c, meta, relayInfo)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeCountTokenFailed)
		return
	}

	relayInfo.SetEstimatePromptTokens(tokens)

	priceData, err := helper.ModelPriceHelper(c, relayInfo, tokens, meta)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest))
		return
	}

	// common.SetContextKey(c, constant.ContextKeyTokenCountMeta, meta)

	if priceData.FreeModel {
		logger.LogInfo(c, fmt.Sprintf("模型 %s 免费，跳过预扣费", relayInfo.OriginModelName))
	} else {
		newAPIError = service.PreConsumeBilling(c, priceData.QuotaToPreConsume, relayInfo)
		if newAPIError != nil {
			return
		}
	}

	defer func() {
		// Only return quota if downstream failed and quota was actually pre-consumed
		if newAPIError != nil {
			newAPIError = service.NormalizeViolationFeeError(newAPIError)
			if relayInfo.Billing != nil {
				relayInfo.Billing.Refund(c)
			}
			service.ChargeViolationFeeIfNeeded(c, relayInfo, newAPIError)
		}
	}()

	retryParam := service.NewRetryParam(c, relayInfo.TokenGroup, relayInfo.OriginModelName, c.Request.URL.Path)
	relayInfo.RetryIndex = 0
	relayInfo.LastError = nil
	var lastChannelError *types.ChannelError
	lastMultiKeyIndex := 0

	for {
		relayInfo.RetryIndex = retryParam.GetRetry()
		if retryParam.GetRetry() > 0 && !retryParam.CanStartRetryAttempt() {
			newAPIError = relayInfo.LastError
			if c.Request.Context().Err() != nil {
				service.UpdateRetryTraceDecision(c, "client_cancelled", "cancelled")
			} else {
				service.UpdateRetryTraceDecision(c, "time_budget_exhausted", "failed")
				recordFinalRetrySummary(c, lastChannelError, lastMultiKeyIndex, newAPIError)
			}
			break
		}
		channel, channelErr := getChannel(c, relayInfo, retryParam)
		if channelErr != nil {
			if retryParam.CandidateExhausted && relayInfo.LastError != nil {
				service.UpdateRetryTraceDecision(c, "candidate_exhausted", "failed")
				newAPIError = relayInfo.LastError
				recordFinalRetrySummary(c, lastChannelError, lastMultiKeyIndex, newAPIError)
			} else {
				if retryParam.GetRetry() > 0 {
					service.UpdateRetryTraceDecision(c, "selection_failed", "failed")
				}
				logger.LogError(c, channelErr.Error())
				newAPIError = channelErr
			}
			break
		}
		if retryParam.GetRetry() > 0 && !retryParam.CanStartRetryAttempt() {
			newAPIError = relayInfo.LastError
			if c.Request.Context().Err() != nil {
				service.UpdateRetryTraceDecision(c, "client_cancelled", "cancelled")
			} else {
				service.UpdateRetryTraceDecision(c, "time_budget_exhausted", "failed")
				recordFinalRetrySummary(c, lastChannelError, lastMultiKeyIndex, newAPIError)
			}
			break
		}

		retryParam.RecordSelection(channel.Id, common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex), common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey))
		service.AddUsedChannel(c, channel.Id)
		lastMultiKeyIndex = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
		lastChannelError = types.NewChannelError(channel.Id, channel.Type, channel.Name, common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey), common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan())
		retryParam.StartAttemptTrace(channel)
		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			// Ensure consistent 413 for oversized bodies even when error occurs later (e.g., retry path)
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
			} else {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
			retryParam.FinishAttemptTrace(newAPIError, 0, "stop", "request_error")
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)
		attemptStartedAt := time.Now()

		switch relayFormat {
		case types.RelayFormatOpenAIRealtime:
			newAPIError = relay.WssHelper(c, relayInfo)
		case types.RelayFormatClaude:
			newAPIError = relay.ClaudeHelper(c, relayInfo)
		case types.RelayFormatGemini:
			newAPIError = geminiRelayHandler(c, relayInfo)
		default:
			newAPIError = relayHandler(c, relayInfo)
		}
		zeroTokenFailure := newAPIError == nil && common.GetContextKeyBool(c, constant.ContextKeyZeroTokenFailure)
		if newAPIError == nil || zeroTokenFailure || !types.IsSkipRetryError(newAPIError) {
			firstResponseMillis := int64(0)
			if relayInfo.FirstResponseTime.After(attemptStartedAt) {
				firstResponseMillis = relayInfo.FirstResponseTime.Sub(attemptStartedAt).Milliseconds()
			} else if newAPIError == nil && !zeroTokenFailure {
				// Non-stream responses have no earlier token callback; the complete
				// valid response is their first observable output.
				firstResponseMillis = time.Since(attemptStartedAt).Milliseconds()
			}
			model.RecordChannelSelectionOutcome(channel.Id, relayInfo.OriginModelName, newAPIError == nil && !zeroTokenFailure, firstResponseMillis)
		}

		if newAPIError == nil {
			relayInfo.LastError = nil
			retryParam.FinishAttemptTrace(nil, 0, "complete", "success")
			finalizeSuccessfulRelayAttempt(c, channel)
			return
		}

		newAPIError = service.NormalizeViolationFeeError(newAPIError)
		relayInfo.LastError = newAPIError

		retryable := shouldRetry(c, newAPIError, 1)
		willRetry := retryable && retryParam.HasRetryAllowance(false)
		delay := time.Duration(0)
		decision := "not_retryable"
		outcome := "failed"
		if c.Request.Context().Err() != nil {
			decision = "client_cancelled"
			outcome = "cancelled"
		} else if retryable && !willRetry {
			decision = "retry_limit"
			if retryParam.BudgetExhausted() {
				decision = "time_budget_exhausted"
			}
		}
		if willRetry {
			delay = retryParam.NextDelay(newAPIError)
			if retryParam.CanWaitForRetry(delay) {
				decision = "retry"
				outcome = "retry_scheduled"
			} else {
				willRetry = false
				decision = "time_budget_exhausted"
			}
		}
		retryParam.FinishAttemptTrace(newAPIError, delay, decision, outcome)
		processChannelError(c, *lastChannelError, newAPIError)

		if !willRetry {
			if retryParam.GetRetry() > 0 && c.Request.Context().Err() == nil {
				recordFinalRetrySummary(c, lastChannelError, lastMultiKeyIndex, newAPIError)
			}
			break
		}
		if !retryParam.WaitBeforeRetry(c.Request.Context(), delay) {
			if c.Request.Context().Err() != nil {
				service.UpdateRetryTraceDecision(c, "client_cancelled", "cancelled")
			} else {
				service.UpdateRetryTraceDecision(c, "time_budget_exhausted", "failed")
				recordFinalRetrySummary(c, lastChannelError, lastMultiKeyIndex, newAPIError)
			}
			break
		}
		retryParam.IncreaseRetry()
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}
	if newAPIError != nil {
		gopool.Go(func() {
			perfmetrics.RecordRelaySample(relayInfo, false, 0)
		})
	}
}

func finalizeSuccessfulRelayAttempt(c *gin.Context, channel *model.Channel) {
	usingKey := common.GetContextKeyString(c, constant.ContextKeyChannelKey)
	if common.GetContextKeyBool(c, constant.ContextKeyZeroTokenFailure) {
		err := types.NewErrorWithStatusCode(
			errors.New("upstream returned zero token usage"),
			types.ErrorCodeChannelZeroToken,
			http.StatusBadGateway,
		)
		service.MarkRetryTraceFailure(c, err, "no_retry_after_output", "channel_failure")
		processChannelError(c, *types.NewChannelError(channel.Id, channel.Type, channel.Name, common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey), usingKey, channel.GetAutoBan()), err)
		return
	}
	service.RecordChannelSuccess(channel.Id, usingKey)
}

var upgrader = websocket.Upgrader{
	Subprotocols: []string{"realtime"}, // WS 握手支持的协议，如果有使用 Sec-WebSocket-Protocol，则必须在此声明对应的 Protocol TODO add other protocol
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域
	},
}

func fastTokenCountMetaForPricing(request dto.Request) *types.TokenCountMeta {
	if request == nil {
		return &types.TokenCountMeta{}
	}
	meta := &types.TokenCountMeta{
		TokenType: types.TokenTypeTokenizer,
	}
	switch r := request.(type) {
	case *dto.GeneralOpenAIRequest:
		maxCompletionTokens := lo.FromPtrOr(r.MaxCompletionTokens, uint(0))
		maxTokens := lo.FromPtrOr(r.MaxTokens, uint(0))
		if maxCompletionTokens > maxTokens {
			meta.MaxTokens = int(maxCompletionTokens)
		} else {
			meta.MaxTokens = int(maxTokens)
		}
	case *dto.OpenAIResponsesRequest:
		meta.MaxTokens = int(lo.FromPtrOr(r.MaxOutputTokens, uint(0)))
	case *dto.ClaudeRequest:
		meta.MaxTokens = int(lo.FromPtr(r.MaxTokens))
	case *dto.ImageRequest:
		// Pricing for image requests depends on ImagePriceRatio; safe to compute even when CountToken is disabled.
		return r.GetTokenCountMeta()
	default:
		// Best-effort: leave CombineText empty to avoid large allocations.
	}
	return meta
}

func getChannel(c *gin.Context, info *relaycommon.RelayInfo, retryParam *service.RetryParam) (*model.Channel, *types.NewAPIError) {
	if info.ChannelMeta == nil {
		autoBan := c.GetBool("auto_ban")
		autoBanInt := 1
		if !autoBan {
			autoBanInt = 0
		}
		priority := int64(0)
		if value, ok := common.GetContextKey(c, constant.ContextKeyChannelPriority); ok {
			if stored, typeOK := value.(int64); typeOK {
				priority = stored
			}
		}
		return &model.Channel{
			Id:       c.GetInt("channel_id"),
			Type:     c.GetInt("channel_type"),
			Name:     c.GetString("channel_name"),
			AutoBan:  &autoBanInt,
			Priority: &priority,
		}, nil
	}
	for {
		channel, selectGroup, err := service.CacheGetRandomSatisfiedChannel(retryParam)

		info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)

		if err != nil {
			return nil, types.NewError(fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: %s", selectGroup, info.OriginModelName, err.Error()), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		if channel == nil {
			return nil, types.NewError(fmt.Errorf("分组 %s 下模型 %s 的可用渠道不存在（retry）", selectGroup, info.OriginModelName), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}

		newAPIError := middleware.SetupContextForSelectedChannelWithKeyExclusions(c, channel, info.OriginModelName, retryParam.ExcludedKeyIndexes(channel.Id))
		if newAPIError == nil {
			return channel, nil
		}
		canSkipUnavailableChannel := retryParam.Setting.ChannelStrategy == operation_setting.RetryChannelSamePriority || retryParam.Setting.TryOtherKeys
		if !canSkipUnavailableChannel || newAPIError.GetErrorCode() != types.ErrorCodeChannelNoAvailableKey {
			return nil, newAPIError
		}
		retryParam.MarkChannelUnavailable(channel)
	}
}

func shouldRetry(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	if openaiErr == nil {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if types.IsChannelError(openaiErr) {
		return true
	}
	if types.IsSkipRetryError(openaiErr) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	code := openaiErr.StatusCode
	if code >= 200 && code < 300 {
		return false
	}
	if code < 100 || code > 599 {
		return true
	}
	if operation_setting.IsAlwaysSkipRetryCode(openaiErr.GetErrorCode()) {
		return false
	}
	return operation_setting.ShouldRetryByStatusCode(code)
}

func processChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError) bool {
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.Error())))
	// 不要使用context获取渠道信息，异步处理时可能会出现渠道信息不一致的情况
	// do not use context to get channel info, there may be inconsistent channel info when processing asynchronously
	clientGone := c.Request != nil && c.Request.Context().Err() != nil && err.StatusCode == 499 && err.GetErrorCode() == types.ErrorCodeDoRequestFailed
	shouldDisable := !clientGone && service.ShouldDisableChannel(err) && channelError.AutoBan &&
		service.RecordChannelFailure(channelError.ChannelId, channelError.UsingKey, common.AutoDisableTolerance)
	if shouldDisable {
		if c.GetBool(channelAvailabilityDeferredContextKey) {
			service.DisableChannelDeferredAvailability(channelError, err.ErrorWithStatusCode())
		} else {
			gopool.Go(func() {
				service.DisableChannel(channelError, err.ErrorWithStatusCode())
			})
		}
	}
	recordChannelErrorLog(c, channelError, err, false, common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex))
	return shouldDisable
}

func recordChannelErrorLog(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, finalSummary bool, multiKeyIndex int) {
	if c == nil || err == nil {
		return
	}
	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		// 保存错误日志到mysql中
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		channelId := channelError.ChannelId
		other := make(map[string]interface{})
		if c.Request != nil && c.Request.URL != nil {
			other["request_path"] = c.Request.URL.Path
		}
		other["error_type"] = err.GetErrorType()
		other["error_code"] = err.GetErrorCode()
		other["status_code"] = err.StatusCode
		other["channel_id"] = channelId
		other["channel_name"] = channelError.ChannelName
		other["channel_type"] = channelError.ChannelType
		adminInfo := make(map[string]interface{})
		if finalSummary {
			service.AppendUsedChannelAdminInfo(c, adminInfo)
			service.AppendRetryTraceAdminInfo(c, adminInfo, false)
			adminInfo["retry_summary"] = true
		} else {
			adminInfo["use_channel"] = []string{fmt.Sprintf("%d", channelError.ChannelId)}
			adminInfo["use_channel_total"] = 1
			adminInfo["use_channel_omitted"] = 0
			service.AppendCurrentRetryTraceAdminInfo(c, adminInfo)
		}
		isMultiKey := channelError.IsMultiKey
		if isMultiKey {
			adminInfo["is_multi_key"] = true
			adminInfo["multi_key_index"] = multiKeyIndex
		}
		service.AppendChannelAffinityAdminInfo(c, adminInfo)
		other["admin_info"] = adminInfo
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}
}

func recordFinalRetrySummary(c *gin.Context, channelError *types.ChannelError, multiKeyIndex int, err *types.NewAPIError) {
	if c == nil || channelError == nil || err == nil {
		return
	}
	recordChannelErrorLog(c, *channelError, err, true, multiKeyIndex)
}

func RelayMidjourney(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatMjProxy, nil, nil)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"description": fmt.Sprintf("failed to generate relay info: %s", err.Error()),
			"type":        "upstream_error",
			"code":        4,
		})
		return
	}

	var mjErr *taskdto.MidjourneyResponse
	switch relayInfo.RelayMode {
	case relayconstant.RelayModeMidjourneyNotify:
		mjErr = relay.RelayMidjourneyNotify(c)
	case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		mjErr = relay.RelayMidjourneyTask(c, relayInfo.RelayMode)
	case relayconstant.RelayModeMidjourneyTaskImageSeed:
		mjErr = relay.RelayMidjourneyTaskImageSeed(c)
	case relayconstant.RelayModeSwapFace:
		mjErr = relay.RelaySwapFace(c, relayInfo)
	default:
		mjErr = relay.RelayMidjourneySubmit(c, relayInfo)
	}
	//err = relayMidjourneySubmit(c, relayMode)
	log.Println(mjErr)
	if mjErr != nil {
		statusCode := http.StatusBadRequest
		if mjErr.Code == 30 {
			mjErr.Result = "当前分组负载已饱和，请稍后再试，或升级账户以提升服务质量。"
			statusCode = http.StatusTooManyRequests
		}
		c.JSON(statusCode, gin.H{
			"description": fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result),
			"type":        "upstream_error",
			"code":        mjErr.Code,
		})
		channelId := c.GetInt("channel_id")
		logger.LogError(c, fmt.Sprintf("relay error (channel #%d, status code %d): %s", channelId, statusCode, fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result)))
	}
}

func RelayNotImplemented(c *gin.Context) {
	err := types.OpenAIError{
		Message: "API not implemented",
		Type:    "new_api_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

func RelayNotFound(c *gin.Context) {
	err := types.OpenAIError{
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}

func RelayTaskFetch(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &taskdto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}
	if taskErr := relay.RelayTaskFetch(c, relayInfo.RelayMode); taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

func RelayTask(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &taskdto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}

	if taskErr := relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
		respondTaskError(c, taskErr)
		return
	}

	var result *relay.TaskSubmitResult
	var taskErr *taskdto.TaskError
	defer func() {
		if taskErr != nil && relayInfo.Billing != nil {
			relayInfo.Billing.Refund(c)
		}
	}()

	retryParam := service.NewRetryParam(c, relayInfo.TokenGroup, relayInfo.OriginModelName, c.Request.URL.Path)
	var lastChannelError *types.ChannelError
	lastMultiKeyIndex := 0

	for {
		if retryParam.GetRetry() > 0 && !retryParam.CanStartRetryAttempt() {
			if c.Request.Context().Err() != nil {
				service.UpdateRetryTraceDecision(c, "client_cancelled", "cancelled")
			} else {
				service.UpdateRetryTraceDecision(c, "time_budget_exhausted", "failed")
				recordFinalRetrySummary(c, lastChannelError, lastMultiKeyIndex, taskErrorAsAPIError(taskErr))
			}
			break
		}
		var channel *model.Channel

		if lockedCh, ok := relayInfo.LockedChannel.(*model.Channel); ok && lockedCh != nil {
			channel = lockedCh
			exhausted, setupErr := setupLockedTaskRetryChannel(c, channel, relayInfo.OriginModelName, retryParam)
			if exhausted {
				service.UpdateRetryTraceDecision(c, "candidate_exhausted", "failed")
				recordFinalRetrySummary(c, lastChannelError, lastMultiKeyIndex, taskErrorAsAPIError(taskErr))
				break
			}
			if setupErr != nil {
				taskErr = service.TaskErrorWrapperLocal(setupErr.Err, "setup_locked_channel_failed", http.StatusInternalServerError)
				break
			}
		} else {
			var channelErr *types.NewAPIError
			channel, channelErr = getChannel(c, relayInfo, retryParam)
			if channelErr != nil {
				if retryParam.CandidateExhausted && taskErr != nil {
					service.UpdateRetryTraceDecision(c, "candidate_exhausted", "failed")
					recordFinalRetrySummary(c, lastChannelError, lastMultiKeyIndex, taskErrorAsAPIError(taskErr))
				} else {
					if retryParam.GetRetry() > 0 {
						service.UpdateRetryTraceDecision(c, "selection_failed", "failed")
					}
					logger.LogError(c, channelErr.Error())
					taskErr = service.TaskErrorWrapperLocal(channelErr.Err, "get_channel_failed", http.StatusInternalServerError)
				}
				break
			}
		}
		if retryParam.GetRetry() > 0 && !retryParam.CanStartRetryAttempt() {
			if c.Request.Context().Err() != nil {
				service.UpdateRetryTraceDecision(c, "client_cancelled", "cancelled")
			} else {
				service.UpdateRetryTraceDecision(c, "time_budget_exhausted", "failed")
				recordFinalRetrySummary(c, lastChannelError, lastMultiKeyIndex, taskErrorAsAPIError(taskErr))
			}
			break
		}

		retryParam.RecordSelection(channel.Id, common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex), common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey))
		service.AddUsedChannel(c, channel.Id)
		lastMultiKeyIndex = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
		lastChannelError = types.NewChannelError(channel.Id, channel.Type, channel.Name, common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey), common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan())
		retryParam.StartAttemptTrace(channel)
		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusRequestEntityTooLarge)
			} else {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusBadRequest)
			}
			retryParam.FinishAttemptTrace(types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, taskErr.StatusCode), 0, "stop", "request_error")
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

		attemptStartedAt := time.Now()
		result, taskErr = relay.RelayTaskSubmit(c, relayInfo)
		if taskErr == nil {
			retryParam.FinishAttemptTrace(nil, 0, "complete", "success")
			service.RecordChannelSuccess(channel.Id, common.GetContextKeyString(c, constant.ContextKeyChannelKey))
			model.RecordChannelSelectionOutcome(channel.Id, relayInfo.OriginModelName, true, time.Since(attemptStartedAt).Milliseconds())
			break
		}

		taskAPIError := taskErrorAsAPIError(taskErr)
		if !taskErr.LocalError && service.ShouldDisableChannel(taskAPIError) {
			model.RecordChannelSelectionOutcome(channel.Id, relayInfo.OriginModelName, false, 0)
		}
		retryable := shouldRetryTaskRelay(c, channel.Id, taskErr, 1)
		willRetry := retryable && retryParam.HasRetryAllowance(true)
		delay := time.Duration(0)
		decision := "not_retryable"
		outcome := "failed"
		if c.Request.Context().Err() != nil {
			decision = "client_cancelled"
			outcome = "cancelled"
		} else if retryable && !willRetry {
			decision = "retry_limit"
			if retryParam.BudgetExhausted() {
				decision = "time_budget_exhausted"
			}
		}
		if willRetry {
			delay = retryParam.NextDelay(taskAPIError)
			if retryParam.CanWaitForRetry(delay) {
				decision = "retry"
				outcome = "retry_scheduled"
			} else {
				willRetry = false
				decision = "time_budget_exhausted"
			}
		}
		retryParam.FinishAttemptTrace(taskAPIError, delay, decision, outcome)

		if !taskErr.LocalError {
			processChannelError(c, *lastChannelError, taskAPIError)
		}

		if !willRetry {
			if retryParam.GetRetry() > 0 && c.Request.Context().Err() == nil {
				recordFinalRetrySummary(c, lastChannelError, lastMultiKeyIndex, taskAPIError)
			}
			break
		}
		if !retryParam.WaitBeforeRetry(c.Request.Context(), delay) {
			if c.Request.Context().Err() != nil {
				service.UpdateRetryTraceDecision(c, "client_cancelled", "cancelled")
			} else {
				service.UpdateRetryTraceDecision(c, "time_budget_exhausted", "failed")
				recordFinalRetrySummary(c, lastChannelError, lastMultiKeyIndex, taskAPIError)
			}
			break
		}
		retryParam.IncreaseRetry()
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}

	// ── 成功：结算 + 日志 + 插入任务 ──
	if taskErr == nil {
		if settleErr := service.SettleBilling(c, relayInfo, result.Quota); settleErr != nil {
			common.SysError("settle task billing error: " + settleErr.Error())
		}
		service.LogTaskConsumption(c, relayInfo)

		task := model.InitTask(result.Platform, relayInfo)
		task.PrivateData.UpstreamTaskID = result.UpstreamTaskID
		task.PrivateData.BillingSource = relayInfo.BillingSource
		task.PrivateData.SubscriptionId = relayInfo.SubscriptionId
		task.PrivateData.TokenId = relayInfo.TokenId
		task.PrivateData.NodeName = common.NodeName
		task.PrivateData.BillingContext = &model.TaskBillingContext{
			ModelPrice:      relayInfo.PriceData.ModelPrice,
			GroupRatio:      relayInfo.PriceData.GroupRatioInfo.GroupRatio,
			ModelRatio:      relayInfo.PriceData.ModelRatio,
			OtherRatios:     relayInfo.PriceData.OtherRatios(),
			OriginModelName: relayInfo.OriginModelName,
			PerCallBilling:  common.StringsContains(constant.TaskPricePatches, relayInfo.OriginModelName) || relayInfo.PriceData.UsePrice,
		}
		task.Quota = result.Quota
		task.Data = result.TaskData
		task.Action = relayInfo.Action
		if insertErr := task.Insert(); insertErr != nil {
			common.SysError("insert task error: " + insertErr.Error())
		}
	}

	if taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

func taskErrorAsAPIError(taskErr *taskdto.TaskError) *types.NewAPIError {
	if taskErr == nil {
		return nil
	}
	err := taskErr.Error
	if err == nil {
		err = errors.New(taskErr.Message)
	}
	apiErr := types.NewOpenAIError(err, types.ErrorCodeBadResponseStatusCode, taskErr.StatusCode)
	apiErr.SetRetryAfterMilliseconds(taskErr.RetryAfterMilliseconds)
	return apiErr
}

func setupLockedTaskRetryChannel(c *gin.Context, channel *model.Channel, modelName string, retryParam *service.RetryParam) (bool, *types.NewAPIError) {
	if retryParam.GetRetry() == 0 {
		return false, nil
	}
	if retryParam.Setting.ChannelStrategy == operation_setting.RetryChannelSamePriority && !retryParam.Setting.TryOtherKeys {
		if retryParam.Setting.ExhaustedAction != operation_setting.RetryExhaustedCycle {
			return true, nil
		}
		retryParam.ResetCandidateRound()
		return false, middleware.SetupContextForSelectedChannelWithKeyExclusions(c, channel, modelName, nil)
	}
	setupErr := middleware.SetupContextForSelectedChannelWithKeyExclusions(c, channel, modelName, retryParam.ExcludedKeyIndexes(channel.Id))
	if setupErr == nil || setupErr.GetErrorCode() != types.ErrorCodeChannelNoAvailableKey || !retryParam.Setting.TryOtherKeys {
		return false, setupErr
	}
	if retryParam.Setting.ChannelStrategy != operation_setting.RetryChannelSamePriority || retryParam.Setting.ExhaustedAction != operation_setting.RetryExhaustedCycle {
		return true, nil
	}
	retryParam.ResetCandidateRound()
	setupErr = middleware.SetupContextForSelectedChannelWithKeyExclusions(c, channel, modelName, retryParam.ExcludedKeyIndexes(channel.Id))
	if setupErr != nil && setupErr.GetErrorCode() == types.ErrorCodeChannelNoAvailableKey {
		return true, nil
	}
	return false, setupErr
}

// respondTaskError 统一输出 Task 错误响应（含 429 限流提示改写）
func respondTaskError(c *gin.Context, taskErr *taskdto.TaskError) {
	if taskErr.StatusCode == http.StatusTooManyRequests {
		taskErr.Message = "当前分组上游负载已饱和，请稍后再试"
	}
	c.JSON(taskErr.StatusCode, taskErr)
}

func shouldRetryTaskRelay(c *gin.Context, channelId int, taskErr *taskdto.TaskError, retryTimes int) bool {
	if taskErr == nil {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	if taskErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if taskErr.StatusCode == 307 {
		return true
	}
	if taskErr.StatusCode/100 == 5 {
		// 超时不重试
		if operation_setting.IsAlwaysSkipRetryStatusCode(taskErr.StatusCode) {
			return false
		}
		return true
	}
	if taskErr.StatusCode == http.StatusBadRequest {
		return false
	}
	if taskErr.StatusCode == 408 {
		// azure处理超时不重试
		return false
	}
	if taskErr.LocalError {
		return false
	}
	if taskErr.StatusCode/100 == 2 {
		return false
	}
	return true
}
