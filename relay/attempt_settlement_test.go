package relay

import (
	"context"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettleCompletedAttemptRejectsTimeoutBeforeBilling(t *testing.T) {
	info := &relaycommon.RelayInfo{}
	info.BeginAttempt()
	info.ConfigureAttemptFirstResponseTimeout(3, context.Background(), nil)
	require.True(t, info.MarkAttemptFirstResponseTimeout())
	called := false

	err := settleCompletedAttempt(info, func() {
		called = true
	})

	require.NotNil(t, err)
	assert.Equal(t, types.ErrorCodeChannelResponseTimeExceeded, err.GetErrorCode())
	assert.True(t, types.IsChannelError(err))
	assert.False(t, called)
}

func TestSettleCompletedAttemptRejectsCompletedStreamWithoutOutput(t *testing.T) {
	info := &relaycommon.RelayInfo{}
	info.BeginAttempt()
	info.ConfigureAttemptFirstResponseTimeout(3, context.Background(), nil)
	called := false

	err := settleCompletedAttempt(info, func() {
		called = true
	})

	require.NotNil(t, err)
	assert.Equal(t, types.ErrorCodeChannelResponseTimeExceeded, err.GetErrorCode())
	assert.False(t, called)
}

func TestSettleCompletedAttemptAllowsValidCompletedOutput(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	info := &relaycommon.RelayInfo{}
	info.BeginAttempt()
	info.ConfigureAttemptFirstResponseTimeout(3, parent, nil)
	require.True(t, info.SetFirstResponseTime())
	cancel()
	called := false

	err := settleCompletedAttempt(info, func() {
		called = true
	})

	require.Nil(t, err)
	assert.True(t, called)
}

func TestSettleCompletedAttemptTreatsClientCancelAsNonChannelFailure(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	info := &relaycommon.RelayInfo{}
	info.BeginAttempt()
	info.ConfigureAttemptFirstResponseTimeout(3, parent, nil)
	called := false

	err := settleCompletedAttempt(info, func() {
		called = true
	})

	require.NotNil(t, err)
	assert.Equal(t, 499, err.StatusCode)
	assert.False(t, types.IsChannelError(err))
	assert.False(t, called)
}
