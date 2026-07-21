package service

import "sync"

type channelFailCountKey struct {
	channelId int
	usingKey  string
}

var channelFailCounts = struct {
	sync.Mutex
	counts map[channelFailCountKey]int
}{
	counts: make(map[channelFailCountKey]int),
}

// RecordChannelFailure increments a key's consecutive failure count and
// atomically clears it when the configured tolerance has been exceeded.
func RecordChannelFailure(channelId int, usingKey string, tolerance int) bool {
	key := channelFailCountKey{channelId: channelId, usingKey: usingKey}

	channelFailCounts.Lock()
	defer channelFailCounts.Unlock()

	failCount := channelFailCounts.counts[key] + 1
	if failCount > tolerance {
		delete(channelFailCounts.counts, key)
		return true
	}
	channelFailCounts.counts[key] = failCount
	return false
}

func ResetChannelFailCount(channelId int, usingKey string) {
	channelFailCounts.Lock()
	delete(channelFailCounts.counts, channelFailCountKey{channelId: channelId, usingKey: usingKey})
	channelFailCounts.Unlock()
}
