package openai

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
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type responseWriteFailureWriter struct {
	gin.ResponseWriter
	match  string
	failed bool
}

type cancelAfterPayloadReadCloser struct {
	reader *strings.Reader
	cancel context.CancelFunc
}

func (reader *cancelAfterPayloadReadCloser) Read(buffer []byte) (int, error) {
	read, err := reader.reader.Read(buffer)
	if err == io.EOF {
		reader.cancel()
	}
	return read, err
}

func (reader *cancelAfterPayloadReadCloser) Close() error {
	return nil
}

func (w *responseWriteFailureWriter) Write(data []byte) (int, error) {
	if !w.failed && w.match != "" && strings.Contains(string(data), w.match) {
		w.failed = true
		return 0, errors.New("synthetic downstream write failure")
	}
	return w.ResponseWriter.Write(data)
}

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

	previous := operation_setting.GetMonitorSettingSnapshot().ZeroTokenAsFailure
	require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{"zero_token_as_failure": common.Interface2String(enabled)}))
	t.Cleanup(func() {
		require.True(t, config.GlobalConfig.Update("monitor_setting", map[string]string{"zero_token_as_failure": common.Interface2String(previous)}))
	})
}

func setResponsesStreamTimeoutForTest(t *testing.T) {
	t.Helper()
	previous := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previous })
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
		{name: "empty tool item scaffold", data: `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1"}}`, want: false},
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

func TestOaiResponsesStreamHandlerCompletesWithoutDoneSentinel(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	setResponsesStreamTimeoutForTest(t)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesStreamTestContext(t, body)

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 2, usage.PromptTokens)
	assert.Equal(t, 1, usage.CompletionTokens)
	assert.Equal(t, 3, usage.TotalTokens)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.Contains(t, recorder.Body.String(), `event: response.completed`)
}

func TestOaiResponsesStreamHandlerSettlesWhenClientCancelsAfterCompleted(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	setResponsesStreamTimeoutForTest(t)
	body := `data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}` + "\n"
	c, _, resp, info := newResponsesStreamTestContext(t, body)
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	resp.Body = &cancelAfterPayloadReadCloser{reader: strings.NewReader(body), cancel: cancel}

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 2, usage.PromptTokens)
	assert.Equal(t, 1, usage.CompletionTokens)
	assert.Equal(t, 3, usage.TotalTokens)
}

func TestOaiResponsesStreamHandlerCountsTerminalTextWhenUsageIsMissing(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	setResponsesStreamTimeoutForTest(t)

	body := `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"terminal text"}]}]}}` + "\n"
	c, _, resp, info := newResponsesStreamTestContext(t, body)
	info.UpstreamModelName = "compat-model"

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Greater(t, usage.CompletionTokens, 0)
	assert.Greater(t, usage.TotalTokens, 0)
}

func TestOaiResponsesStreamHandlerRejectsFailedTerminalEvent(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	setResponsesStreamTimeoutForTest(t)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		`data: {"type":"response.failed","response":{"status":"failed","error":{"type":"server_error","message":"upstream failed"}}}`,
		``,
	}, "\n")
	c, recorder, resp, info := newResponsesStreamTestContext(t, body)

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	assert.Contains(t, apiErr.Error(), "upstream failed")
	assert.Empty(t, recorder.Body.String())
}

func TestOaiResponsesStreamHandlerRecordsTTFTAtFirstValidOutput(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	setResponsesStreamTimeoutForTest(t)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		`data: {"type":"response.in_progress"}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
		``,
	}, "\n")
	c, _, resp, info := newResponsesStreamTestContext(t, body)
	info.StartTime = time.Now().Add(-time.Second)
	info.BeginAttempt()

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.False(t, info.AttemptFirstResponseTime().IsZero())
	assert.True(t, info.AttemptFirstResponseTime().After(info.StartTime))
}

func TestOaiResponsesStreamHandlerDoesNotRecordTTFTForControlOnlyCompletion(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	setResponsesStreamTimeoutForTest(t)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":0,"total_tokens":2}}}`,
		``,
	}, "\n")
	c, _, resp, info := newResponsesStreamTestContext(t, body)
	info.StartTime = time.Now().Add(-time.Second)
	info.BeginAttempt()

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.True(t, info.AttemptFirstResponseTime().IsZero())
}

func TestOaiResponsesStreamHandlerSettlesAfterCompletedTerminalWriteFailure(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	setResponsesStreamTimeoutForTest(t)
	body := `data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n"
	c, _, resp, info := newResponsesStreamTestContext(t, body)
	c.Writer = &responseWriteFailureWriter{ResponseWriter: c.Writer, match: "response.completed"}

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 2, usage.TotalTokens)
}

func TestOaiResponsesStreamHandlerRejectsWriteFailureBeforeCompleted(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	setResponsesStreamTimeoutForTest(t)
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		``,
	}, "\n")
	c, _, resp, info := newResponsesStreamTestContext(t, body)
	c.Writer = &responseWriteFailureWriter{ResponseWriter: c.Writer, match: "response.output_text.delta"}

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, 499, apiErr.StatusCode)
}

func TestOaiResponsesStreamHandlerPreservesTotalOnlyUsage(t *testing.T) {
	setZeroTokenFailureForResponsesTest(t, true)
	setResponsesStreamTimeoutForTest(t)
	body := `data: {"type":"response.completed","response":{"status":"completed","usage":{"total_tokens":7}}}` + "\n"
	c, _, resp, info := newResponsesStreamTestContext(t, body)

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Zero(t, usage.CompletionTokens)
	assert.Equal(t, 7, usage.TotalTokens)
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
