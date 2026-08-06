package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFirstTokenTimeoutDoesNotMixAttempts(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(context.Background())
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeChatCompletions,
		IsStream:    true,
		ChannelMeta: &relaycommon.ChannelMeta{},
	}

	info.BeginAttempt()
	timeout := make(chan time.Time, 1)
	timeout <- time.Now()
	firstErr := runRelayAttemptWithFirstTokenTimeoutSignal(c, info, 3, timeout, func() *types.NewAPIError {
		helper.SetEventStreamHeaders(c)
		_ = helper.StringData(c, "attempt-one-control")
		<-c.Request.Context().Done()
		info.SetFirstResponseTime()
		_ = helper.StringData(c, "attempt-one-late-output")
		return nil
	})
	require.NotNil(t, firstErr)
	assert.Equal(t, types.ErrorCodeChannelResponseTimeExceeded, firstErr.GetErrorCode())
	assert.Empty(t, recorder.Body.String())

	info.BeginAttempt()
	secondErr := runRelayAttemptWithFirstTokenTimeoutSignal(c, info, 3, make(chan time.Time), func() *types.NewAPIError {
		helper.SetEventStreamHeaders(c)
		_ = helper.StringData(c, "attempt-two-control")
		require.True(t, info.SetFirstResponseTime())
		_ = helper.StringData(c, "attempt-two-output")
		return nil
	})
	require.Nil(t, secondErr)

	written := recorder.Body.String()
	assert.NotContains(t, written, "attempt-one")
	assert.Contains(t, written, "attempt-two-control")
	assert.Contains(t, written, "attempt-two-output")
	assert.Less(t, strings.Index(written, "attempt-two-control"), strings.Index(written, "attempt-two-output"))
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
}

func TestFirstTokenResponseBufferFailsClosedAtLimit(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	var overflowed atomic.Bool
	buffer := newFirstTokenResponseBuffer(c.Writer, 4, func() {
		overflowed.Store(true)
	}, nil)

	written, err := buffer.Write([]byte("12345"))

	assert.Zero(t, written)
	assert.ErrorIs(t, err, errFirstTokenResponseDiscarded)
	assert.True(t, overflowed.Load())
	require.NoError(t, buffer.Release())
	assert.Empty(t, recorder.Body.String())
}

type failingFirstTokenResponseWriter struct {
	gin.ResponseWriter
	err error
}

func (w *failingFirstTokenResponseWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestFirstTokenResponseBufferWriteFailureBlocksSettlement(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	wantErr := errors.New("client connection closed")
	underlying := &failingFirstTokenResponseWriter{ResponseWriter: c.Writer, err: wantErr}
	info := &relaycommon.RelayInfo{}
	info.BeginAttempt()
	var buffer *firstTokenResponseBuffer
	buffer = newFirstTokenResponseBuffer(underlying, 1024, nil, func(err error) {
		info.MarkAttemptDownstreamWriteError(err)
	})
	info.ConfigureAttemptFirstResponseTimeout(3, context.Background(), func() {
		_ = buffer.Release()
	})
	_, err := buffer.Write([]byte("data: control\n\n"))
	require.NoError(t, err)

	require.True(t, info.SetFirstResponseTime())
	apiErr := info.AttemptSettlementError()

	require.NotNil(t, apiErr)
	assert.Equal(t, 499, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeDoRequestFailed, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.False(t, types.IsChannelError(apiErr))
	assert.ErrorIs(t, buffer.Release(), wantErr)
}
