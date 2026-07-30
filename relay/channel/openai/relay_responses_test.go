package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newResponsesStreamTestContext(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "responses-stream-test")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAIResponses,
		RelayMode:   relayconstant.RelayModeResponses,
		DisablePing: true,
	}
	return c, recorder, resp, info
}

func setZeroTokenFailureForResponsesTest(t *testing.T, enabled bool) {
	t.Helper()

	setting := operation_setting.GetMonitorSetting()
	previous := setting.ZeroTokenAsFailure
	setting.ZeroTokenAsFailure = enabled
	t.Cleanup(func() {
		setting.ZeroTokenAsFailure = previous
	})
}

func TestResponsesStreamMeaningfulOutput(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{name: "created metadata", data: `{"type":"response.created","response":{"id":"resp_1"}}`, want: false},
		{name: "in progress metadata", data: `{"type":"response.in_progress"}`, want: false},
		{name: "output item scaffold", data: `{"type":"response.output_item.added","item":{"type":"message"}}`, want: false},
		{name: "empty text delta", data: `{"type":"response.output_text.delta","delta":""}`, want: false},
		{name: "whitespace text delta", data: `{"type":"response.output_text.delta","delta":" "}`, want: true},
		{name: "function arguments delta", data: `{"type":"response.function_call_arguments.delta","delta":"{}"}`, want: true},
		{name: "reasoning delta", data: `{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`, want: true},
		{name: "empty text done", data: `{"type":"response.output_text.done","text":""}`, want: false},
		{name: "text done", data: `{"type":"response.output_text.done","text":"done"}`, want: true},
		{name: "completed output item", data: `{"type":"response.output_item.done","item":{"type":"function_call","name":"lookup"}}`, want: true},
		{name: "completed refusal item", data: `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"refusal","refusal":"denied"}]}}`, want: true},
		{name: "completed reasoning summary", data: `{"type":"response.completed","response":{"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"analysis"}]}]}}`, want: true},
		{name: "empty completed message", data: `{"type":"response.completed","response":{"output":[{"type":"message","content":[]}]}}`, want: false},
		{name: "completed response with output", data: `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`, want: true},
		{name: "completed response with usage", data: `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`, want: true},
		{name: "done response with usage", data: `{"type":"response.done","response":{"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`, want: true},
		{name: "empty completed response", data: `{"type":"response.completed","response":{"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`, want: false},
		{name: "invalid json", data: `{`, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, responsesStreamHasMeaningfulOutput(test.data))
		})
	}
}

func TestOaiResponsesStreamHandlerRejectsMetadataOnlyStreamBeforeWriting(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		`data: {"type":"response.in_progress"}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesStreamTestContext(t, body)

	usage, err := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, usage)
	require.NotNil(t, err)
	assert.Equal(t, types.ErrorCodeChannelZeroToken, err.GetErrorCode())
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	assert.True(t, types.IsChannelError(err))
	assert.Empty(t, recorder.Body.String())
	assert.False(t, recorder.Flushed)
	assert.Empty(t, recorder.Header().Get("Content-Type"))
}

func TestOaiResponsesStreamHandlerFlushesPreludeWithFirstMeaningfulOutput(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		`data: {"type":"response.in_progress"}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesStreamTestContext(t, body)

	usage, err := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 3, usage.TotalTokens)
	assert.True(t, recorder.Flushed)
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	requireOrderedSubstrings(t, recorder.Body.String(),
		`event: response.created`,
		`event: response.in_progress`,
		`event: response.output_text.delta`,
		`event: response.completed`,
	)
}

func TestOaiResponsesStreamHandlerKeepsLegacyMetadataOnlyBehaviorWhenDisabled(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, false)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesStreamTestContext(t, body)

	usage, err := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Zero(t, usage.TotalTokens)
	assert.Contains(t, recorder.Body.String(), `event: response.created`)
	assert.True(t, recorder.Flushed)
}

func TestOaiResponsesStreamHandlerDoesNotRetryCanceledClient(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := "data: {\"type\":\"response.created\"}\ndata: [DONE]\n"
	c, recorder, resp, info := newResponsesStreamTestContext(t, body)
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	cancel()

	usage, err := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, usage)
	require.NotNil(t, err)
	assert.False(t, types.IsChannelError(err))
	assert.True(t, types.IsSkipRetryError(err))
	assert.Equal(t, types.ErrorCodeDoRequestFailed, err.GetErrorCode())
	assert.Empty(t, recorder.Body.String())
}
