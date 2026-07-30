package openai

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := dto.Usage{}
	if responsesResponse.Usage != nil {
		usage.PromptTokens = responsesResponse.Usage.InputTokens
		usage.CompletionTokens = responsesResponse.Usage.OutputTokens
		usage.TotalTokens = responsesResponse.Usage.TotalTokens
		if responsesResponse.Usage.InputTokensDetails != nil {
			usage.PromptTokensDetails.CachedTokens = responsesResponse.Usage.InputTokensDetails.CachedTokens
			usage.PromptTokensDetails.CacheWriteTokens = responsesResponse.Usage.InputTokensDetails.CacheWriteTokens
		}
	}
	// Count actual tool invocations from Output (not tool declarations).
	for _, output := range responsesResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}

	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	if !relaycommon.IsNonBillableResponsesStatus(responsesResponse.Status) {
		for i := range responsesResponse.Output {
			idx := i
			imageCounter.Observe(&responsesResponse.Output[i], &idx)
		}
	}
	imageCounter.Commit(info)

	return &usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	var usage = &dto.Usage{}
	var responseTextBuilder strings.Builder
	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	imageCommitted := false
	var downstreamWriteErr error
	strictOutputGate := operation_setting.GetMonitorSetting().ZeroTokenAsFailure
	scannerOptions := helper.StreamScannerOptions{}
	if strictOutputGate {
		scannerOptions.StartResponseWhen = responsesStreamHasMeaningfulOutput
	}

	scannerOutcome := helper.StreamScannerHandlerWithOptions(c, resp, info, scannerOptions, func(data string, sr *helper.StreamResult) {

		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, "failed to unmarshal stream response: "+err.Error())
			sr.Error(err)
			return
		}
		if err := sendResponsesStreamData(c, streamResponse, data); err != nil {
			downstreamWriteErr = err
			sr.Stop(err)
			return
		}
		switch streamResponse.Type {
		case "response.completed", "response.done":
			if streamResponse.Response != nil {
				if streamResponse.Response.Usage != nil {
					if streamResponse.Response.Usage.InputTokens != 0 {
						usage.PromptTokens = streamResponse.Response.Usage.InputTokens
					}
					if streamResponse.Response.Usage.OutputTokens != 0 {
						usage.CompletionTokens = streamResponse.Response.Usage.OutputTokens
					}
					if streamResponse.Response.Usage.TotalTokens != 0 {
						usage.TotalTokens = streamResponse.Response.Usage.TotalTokens
					}
					if streamResponse.Response.Usage.InputTokensDetails != nil {
						usage.PromptTokensDetails.CachedTokens = streamResponse.Response.Usage.InputTokensDetails.CachedTokens
						usage.PromptTokensDetails.CacheWriteTokens = streamResponse.Response.Usage.InputTokensDetails.CacheWriteTokens
					}
				}
				if !imageCommitted {
					if relaycommon.IsNonBillableResponsesStatus(streamResponse.Response.Status) {
						imageCounter.Reset()
						imageCounter.Commit(info)
						imageCommitted = true
					} else {
						for i := range streamResponse.Response.Output {
							idx := i
							imageCounter.Observe(&streamResponse.Response.Output[i], &idx)
						}
						imageCounter.Commit(info)
						imageCommitted = true
					}
				}
			} else if !imageCommitted {
				imageCounter.Commit(info)
				imageCommitted = true
			}
		case "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
			if !imageCommitted {
				imageCounter.Reset()
				imageCounter.Commit(info)
				imageCommitted = true
			}
		case "response.output_text.delta":
			// 处理输出文本
			responseTextBuilder.WriteString(streamResponse.Delta)
		case dto.ResponsesOutputTypeItemDone:
			if streamResponse.Item != nil {
				switch streamResponse.Item.Type {
				case dto.BuildInCallWebSearchCall:
					info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
				case dto.BuildInCallFileSearchCall:
					info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
				case dto.BuildInCallFunctionCall:
					info.CountBillableToolCall(dto.BuildInCallFunctionCall, streamResponse.Item.Name)
				case dto.ResponsesOutputTypeImageGenerationCall:
					if !imageCommitted {
						imageCounter.Observe(streamResponse.Item, streamResponse.OutputIndex)
					}
				}
			}
		}
	})
	if downstreamWriteErr != nil || (strictOutputGate && c.Request.Context().Err() != nil) {
		err := downstreamWriteErr
		if err == nil {
			err = c.Request.Context().Err()
		}
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("downstream disconnected while streaming response: %w", err),
			types.ErrorCodeDoRequestFailed,
			499,
			types.ErrOptionWithSkipRetry(),
			types.ErrOptionWithNoRecordErrorLog(),
		)
	}
	if strictOutputGate && !scannerOutcome.ResponseStarted {
		streamSummary := "unknown"
		if info.StreamStatus != nil {
			streamSummary = info.StreamStatus.Summary()
		}
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("upstream response stream ended before meaningful output: %s", streamSummary),
			types.ErrorCodeChannelZeroToken,
			http.StatusBadGateway,
		)
	}

	if usage.CompletionTokens == 0 {
		// 计算输出文本的 token 数量
		tempStr := responseTextBuilder.String()
		if len(tempStr) > 0 {
			// 非正常结束，使用输出文本的 token 数量
			completionTokens := service.CountTextToken(tempStr, info.UpstreamModelName)
			usage.CompletionTokens = completionTokens
		}
	}

	if usage.PromptTokens == 0 && usage.CompletionTokens != 0 {
		usage.PromptTokens = info.GetEstimatePromptTokens()
	}

	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

	return usage, nil
}

type responsesStreamGateEvent struct {
	Type            string                       `json:"type"`
	Delta           string                       `json:"delta"`
	Text            string                       `json:"text"`
	Arguments       string                       `json:"arguments"`
	Refusal         string                       `json:"refusal"`
	PartialImageB64 string                       `json:"partial_image_b64"`
	Response        *responsesStreamGateResponse `json:"response"`
	Item            *responsesStreamGateItem     `json:"item"`
	Part            *responsesStreamGateContent  `json:"part"`
}

type responsesStreamGateResponse struct {
	Output []responsesStreamGateItem `json:"output"`
	Usage  *dto.Usage                `json:"usage"`
}

type responsesStreamGateItem struct {
	Type             string                       `json:"type"`
	Name             string                       `json:"name"`
	CallID           string                       `json:"call_id"`
	Arguments        string                       `json:"arguments"`
	EncryptedContent string                       `json:"encrypted_content"`
	Content          []responsesStreamGateContent `json:"content"`
	Summary          []responsesStreamGateContent `json:"summary"`
}

type responsesStreamGateContent struct {
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

func responsesStreamHasMeaningfulOutput(data string) bool {
	var event responsesStreamGateEvent
	if err := common.UnmarshalJsonStr(data, &event); err != nil {
		return false
	}
	if event.Delta != "" || event.Text != "" || event.Arguments != "" || event.Refusal != "" || event.PartialImageB64 != "" {
		return true
	}
	switch event.Type {
	case dto.ResponsesOutputTypeItemDone:
		return responsesOutputItemHasMeaningfulOutput(event.Item)
	case "response.content_part.done":
		return event.Part != nil && (event.Part.Text != "" || event.Part.Refusal != "")
	case "response.completed", "response.done":
		if event.Response == nil {
			return false
		}
		if dto.HasPositiveOpenAIUsageTokens(event.Response.Usage) {
			return true
		}
		for i := range event.Response.Output {
			if responsesOutputItemHasMeaningfulOutput(&event.Response.Output[i]) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func responsesOutputItemHasMeaningfulOutput(item *responsesStreamGateItem) bool {
	if item == nil {
		return false
	}
	if item.Name != "" || item.CallID != "" || item.Arguments != "" || item.EncryptedContent != "" {
		return true
	}
	for _, content := range item.Content {
		if content.Text != "" || content.Refusal != "" {
			return true
		}
	}
	for _, summary := range item.Summary {
		if summary.Text != "" || summary.Refusal != "" {
			return true
		}
	}
	switch item.Type {
	case "", "message", "reasoning":
		return false
	default:
		return true
	}
}
