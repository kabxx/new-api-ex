package service

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecordChannelFailureDisablesImmediatelyAtZeroTolerance(t *testing.T) {
	const channelId = 91000
	const usingKey = "key-a"
	t.Cleanup(func() { ResetChannelFailCount(channelId, usingKey) })

	assert.True(t, RecordChannelFailure(channelId, usingKey, 0))
	assert.True(t, RecordChannelFailure(channelId, usingKey, 0))
}

func TestRecordChannelFailureHonorsToleranceAndResets(t *testing.T) {
	const channelId = 91001
	const usingKey = "key-a"
	t.Cleanup(func() { ResetChannelFailCount(channelId, usingKey) })

	assert.False(t, RecordChannelFailure(channelId, usingKey, 2))
	assert.False(t, RecordChannelFailure(channelId, usingKey, 2))
	assert.True(t, RecordChannelFailure(channelId, usingKey, 2))
	assert.False(t, RecordChannelFailure(channelId, usingKey, 2))

	ResetChannelFailCount(channelId, usingKey)
	assert.False(t, RecordChannelFailure(channelId, usingKey, 2))
}

func TestRecordChannelFailureTracksKeysIndependently(t *testing.T) {
	const channelId = 91002
	const firstKey = "key-a"
	const secondKey = "key-b"
	t.Cleanup(func() {
		ResetChannelFailCount(channelId, firstKey)
		ResetChannelFailCount(channelId, secondKey)
	})

	assert.False(t, RecordChannelFailure(channelId, firstKey, 1))
	assert.False(t, RecordChannelFailure(channelId, secondKey, 1))
	assert.True(t, RecordChannelFailure(channelId, firstKey, 1))
	assert.True(t, RecordChannelFailure(channelId, secondKey, 1))
}

func TestRecordChannelFailureCrossesThresholdOnceConcurrently(t *testing.T) {
	const channelId = 91003
	const usingKey = "key-a"
	const failures = 8
	t.Cleanup(func() { ResetChannelFailCount(channelId, usingKey) })

	results := make(chan bool, failures)
	var waitGroup sync.WaitGroup
	waitGroup.Add(failures)
	for range failures {
		go func() {
			defer waitGroup.Done()
			results <- RecordChannelFailure(channelId, usingKey, failures-1)
		}()
	}
	waitGroup.Wait()
	close(results)

	thresholdCrossings := 0
	for crossed := range results {
		if crossed {
			thresholdCrossings++
		}
	}
	assert.Equal(t, 1, thresholdCrossings)
	assert.False(t, RecordChannelFailure(channelId, usingKey, failures-1))
}
