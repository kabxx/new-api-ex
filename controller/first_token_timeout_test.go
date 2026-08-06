package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newFirstTokenTimeoutTestContext(requestContext context.Context) *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	return c
}

func TestFirstTokenTimeoutCancelsOnlyCurrentAttempt(t *testing.T) {
	c := newFirstTokenTimeoutTestContext(context.Background())
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions}
	info.BeginAttempt()
	timeout := make(chan time.Time, 1)
	timeout <- time.Now()

	apiErr := runRelayAttemptWithFirstTokenTimeoutSignal(c, info, 3, timeout, func() *types.NewAPIError {
		<-c.Request.Context().Done()
		// A chunk that arrives after the timeout must not turn the timed-out
		// attempt back into a successful first response.
		info.SetFirstResponseTime()
		return types.NewOpenAIError(c.Request.Context().Err(), types.ErrorCodeDoRequestFailed, 499)
	})

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusGatewayTimeout, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeChannelResponseTimeExceeded, apiErr.GetErrorCode())
	assert.True(t, types.IsChannelError(apiErr))
	assert.NoError(t, c.Request.Context().Err(), "the client request context must survive an attempt timeout")
	assert.True(t, info.AttemptFirstResponseTimedOut())

	info.BeginAttempt()
	apiErr = runRelayAttemptWithFirstTokenTimeoutSignal(c, info, 3, make(chan time.Time), func() *types.NewAPIError {
		info.SetFirstResponseTime()
		return nil
	})
	require.Nil(t, apiErr)
	assert.False(t, info.AttemptFirstResponseTimedOut())
	assert.False(t, info.AttemptFirstResponseTime().IsZero())
}

func TestFirstTokenTimeoutPreservesClientCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	c := newFirstTokenTimeoutTestContext(parent)
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions}
	info.BeginAttempt()
	wantErr := types.NewOpenAIError(errors.New("client cancelled"), types.ErrorCodeDoRequestFailed, 499)

	apiErr := runRelayAttemptWithFirstTokenTimeoutSignal(c, info, 3, make(chan time.Time), func() *types.NewAPIError {
		return wantErr
	})

	assert.Same(t, wantErr, apiErr)
	assert.False(t, info.AttemptFirstResponseTimedOut())
}

func TestFirstTokenTimeoutAppliesOnlyToTextGenerationModes(t *testing.T) {
	tests := []struct {
		name        string
		mode        int
		relayFormat types.RelayFormat
		channelType int
		path        string
		request     dto.Request
		originModel string
		upstream    string
		want        bool
	}{
		{name: "chat", mode: relayconstant.RelayModeChatCompletions, relayFormat: types.RelayFormatOpenAI, want: true},
		{name: "responses", mode: relayconstant.RelayModeResponses, relayFormat: types.RelayFormatOpenAIResponses, want: true},
		{name: "responses format fallback", mode: relayconstant.RelayModeUnknown, relayFormat: types.RelayFormatOpenAIResponses, want: true},
		{name: "responses image generation tool", mode: relayconstant.RelayModeResponses, relayFormat: types.RelayFormatOpenAIResponses, request: &dto.OpenAIResponsesRequest{Tools: []byte(`[{"type":"image_generation"}]`)}, want: false},
		{name: "responses text tool", mode: relayconstant.RelayModeResponses, relayFormat: types.RelayFormatOpenAIResponses, request: &dto.OpenAIResponsesRequest{Tools: []byte(`[{"type":"function","name":"lookup"}]`)}, want: true},
		{name: "claude", mode: relayconstant.RelayModeUnknown, relayFormat: types.RelayFormatClaude, want: true},
		{name: "gemini", mode: relayconstant.RelayModeUnknown, relayFormat: types.RelayFormatGemini, want: true},
		{name: "gemini native generation", mode: relayconstant.RelayModeGemini, relayFormat: types.RelayFormatGemini, path: "/v1beta/models/gemini-2.5-pro:generateContent", want: true},
		{name: "gemini native embedding", mode: relayconstant.RelayModeGemini, relayFormat: types.RelayFormatGemini, path: "/v1beta/models/text-embedding-004:embedContent", want: false},
		{name: "chat audio modality", mode: relayconstant.RelayModeChatCompletions, relayFormat: types.RelayFormatOpenAI, request: &dto.GeneralOpenAIRequest{Modalities: []byte(`["text","audio"]`)}, want: false},
		{name: "chat audio config", mode: relayconstant.RelayModeChatCompletions, relayFormat: types.RelayFormatOpenAI, request: &dto.GeneralOpenAIRequest{Audio: []byte(`{"format":"wav"}`)}, want: false},
		{name: "responses audio model", mode: relayconstant.RelayModeResponses, relayFormat: types.RelayFormatOpenAIResponses, originModel: "gpt-4o-audio-preview", want: false},
		{name: "gemini image output", mode: relayconstant.RelayModeGemini, relayFormat: types.RelayFormatGemini, request: &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{ResponseModalities: []string{"TEXT", "IMAGE"}}}, want: false},
		{name: "gemini audio output", mode: relayconstant.RelayModeGemini, relayFormat: types.RelayFormatGemini, request: &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{ResponseModalities: []string{"AUDIO"}}}, want: false},
		{name: "gemini image model", mode: relayconstant.RelayModeGemini, relayFormat: types.RelayFormatGemini, request: &dto.GeminiChatRequest{}, upstream: "gemini-2.5-flash-image", want: false},
		{name: "openai chat converted to gemini image model", mode: relayconstant.RelayModeChatCompletions, relayFormat: types.RelayFormatOpenAI, channelType: constant.ChannelTypeGemini, request: &dto.GeneralOpenAIRequest{}, upstream: "gemini-2.5-flash-image", want: false},
		{name: "openai responses converted to vertex image model", mode: relayconstant.RelayModeResponses, relayFormat: types.RelayFormatOpenAIResponses, channelType: constant.ChannelTypeVertexAi, request: &dto.OpenAIResponsesRequest{}, upstream: "gemini-3-pro-image", want: false},
		{name: "openai chat converted to gemini text model", mode: relayconstant.RelayModeChatCompletions, relayFormat: types.RelayFormatOpenAI, channelType: constant.ChannelTypeGemini, request: &dto.GeneralOpenAIRequest{}, upstream: "gemini-2.5-pro", want: true},
		{name: "gemini multimodal input with text output", mode: relayconstant.RelayModeGemini, relayFormat: types.RelayFormatGemini, request: &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Parts: []dto.GeminiPart{{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "abc"}}}}}}, upstream: "gemini-2.5-pro", want: true},
		{name: "image", mode: relayconstant.RelayModeImagesGenerations, relayFormat: types.RelayFormatOpenAI, want: false},
		{name: "explicit image overrides format fallback", mode: relayconstant.RelayModeImagesGenerations, relayFormat: types.RelayFormatClaude, want: false},
		{name: "embedding", mode: relayconstant.RelayModeEmbeddings, relayFormat: types.RelayFormatOpenAI, want: false},
		{name: "audio", mode: relayconstant.RelayModeAudioSpeech, relayFormat: types.RelayFormatOpenAIAudio, want: false},
		{name: "realtime websocket", mode: relayconstant.RelayModeRealtime, relayFormat: types.RelayFormatOpenAIRealtime, want: false},
		{name: "async task", mode: relayconstant.RelayModeVideoSubmit, relayFormat: types.RelayFormatOpenAI, want: false},
		{name: "xunfei websocket upstream", mode: relayconstant.RelayModeChatCompletions, relayFormat: types.RelayFormatOpenAI, channelType: constant.ChannelTypeXunfei, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, firstTokenTimeoutApplies(&relaycommon.RelayInfo{
				RelayMode:       test.mode,
				RequestURLPath:  test.path,
				Request:         test.request,
				OriginModelName: test.originModel,
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType:       test.channelType,
					UpstreamModelName: test.upstream,
				},
			}, test.relayFormat))
		})
	}
}

func setFirstTokenTimeoutSelectedChannel(c *gin.Context, channelType int, originModel string, modelMapping string) {
	common.SetContextKey(c, constant.ContextKeyChannelType, channelType)
	common.SetContextKey(c, constant.ContextKeyOriginalModel, originModel)
	common.SetContextKey(c, constant.ContextKeyChannelModelMapping, modelMapping)
}

func TestFirstTokenTimeoutUsesCurrentSelectedChannelMetadata(t *testing.T) {
	c := newFirstTokenTimeoutTestContext(context.Background())
	request := &dto.GeneralOpenAIRequest{Model: "image-alias"}
	setFirstTokenTimeoutSelectedChannel(c, constant.ChannelTypeXunfei, request.Model, "")
	info, err := relaycommon.GenRelayInfo(c, types.RelayFormatOpenAI, request, nil)
	require.NoError(t, err)

	applies, apiErr := firstTokenTimeoutAppliesToCurrentAttempt(c, info, types.RelayFormatOpenAI)
	require.Nil(t, apiErr)
	assert.False(t, applies, "the first attempt must read Xunfei from the selected-channel context")

	setFirstTokenTimeoutSelectedChannel(c, constant.ChannelTypeGemini, request.Model, `{"image-alias":"gemini-2.5-flash-image"}`)
	applies, apiErr = firstTokenTimeoutAppliesToCurrentAttempt(c, info, types.RelayFormatOpenAI)
	require.Nil(t, apiErr)
	assert.False(t, applies, "a retry must use the current Gemini image mapping instead of stale Xunfei metadata")

	setFirstTokenTimeoutSelectedChannel(c, constant.ChannelTypeOpenAI, request.Model, `{"image-alias":"gpt-4o"}`)
	applies, apiErr = firstTokenTimeoutAppliesToCurrentAttempt(c, info, types.RelayFormatOpenAI)
	require.Nil(t, apiErr)
	assert.True(t, applies, "a later text-channel retry must not inherit the previous image exclusion")
	assert.Nil(t, info.ChannelMeta, "the applicability snapshot must not pre-initialize handler-owned channel metadata")
	assert.Equal(t, "image-alias", request.Model, "the applicability snapshot must not mutate the client request")
}

func TestFirstTokenTimeoutUsesResponsesModelMappingForCurrentVertexAttempt(t *testing.T) {
	c := newFirstTokenTimeoutTestContext(context.Background())
	c.Request.URL.Path = "/v1/responses"
	request := &dto.OpenAIResponsesRequest{Model: "responses-image-alias"}
	setFirstTokenTimeoutSelectedChannel(c, constant.ChannelTypeVertexAi, request.Model, `{"responses-image-alias":"gemini-3-pro-image"}`)
	info, err := relaycommon.GenRelayInfo(c, types.RelayFormatOpenAIResponses, request, nil)
	require.NoError(t, err)

	applies, apiErr := firstTokenTimeoutAppliesToCurrentAttempt(c, info, types.RelayFormatOpenAIResponses)

	require.Nil(t, apiErr)
	assert.False(t, applies)
	assert.Nil(t, info.ChannelMeta)
	assert.Equal(t, "responses-image-alias", request.Model)
}

func TestFirstTokenGateBuffersControlEventsUntilValidOutput(t *testing.T) {
	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions, IsStream: true, DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{}}
	info.BeginAttempt()
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":""}}]}`,
		`data: {"choices":[{"delta":{"content":"hello"}}]}`,
		"data: [DONE]",
		"",
	}, "\n")

	var writeErr error
	apiErr := runRelayAttemptWithFirstTokenTimeoutSignal(c, info, 3, make(chan time.Time), func() *types.NewAPIError {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
		helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
			if err := helper.StringData(c, data); err != nil {
				writeErr = err
			}
		})
		return nil
	})

	require.Nil(t, apiErr)
	require.NoError(t, writeErr)
	written := recorder.Body.String()
	assert.Contains(t, written, `"content":""`)
	assert.Contains(t, written, `"content":"hello"`)
	assert.Less(t, strings.Index(written, `"content":""`), strings.Index(written, `"content":"hello"`))
}
