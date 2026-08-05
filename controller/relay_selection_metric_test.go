package controller

import (
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
)

func TestChannelSelectionFirstResponseMillisDoesNotUseStreamDuration(t *testing.T) {
	startedAt := time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC)

	streamInfo := &relaycommon.RelayInfo{IsStream: true}
	assert.Zero(t, channelSelectionFirstResponseMillis(streamInfo, startedAt))

	nonStreamInfo := &relaycommon.RelayInfo{}
	assert.Zero(t, channelSelectionFirstResponseMillis(nonStreamInfo, startedAt))
}
