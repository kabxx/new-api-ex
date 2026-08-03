package model

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelAvailabilityStateTestDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousCache := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	DB = db
	common.MemoryCacheEnabled = false
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ChannelAvailabilityState{}, &ChannelAvailabilityNotificationEvent{}))
	t.Cleanup(func() {
		DB = previousDB
		common.MemoryCacheEnabled = previousCache
	})
}

func TestClaimChannelAvailabilityTransitionEstablishesBaselineAndClaimsEdges(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)

	transition, err := ClaimChannelAvailabilityTransition()
	require.NoError(t, err)
	assert.Nil(t, transition)

	channel := Channel{Name: "primary", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&channel).Error)
	transition, err = ClaimChannelAvailabilityTransition()
	require.NoError(t, err)
	require.NotNil(t, transition)
	assert.False(t, transition.FromAvailable)
	assert.True(t, transition.ToAvailable)
	assert.Equal(t, int64(1), transition.Snapshot.EnabledCount)

	transition, err = ClaimChannelAvailabilityTransition()
	require.NoError(t, err)
	assert.Nil(t, transition)

	require.NoError(t, DB.Model(&channel).Update("status", common.ChannelStatusAutoDisabled).Error)
	transition, err = ClaimChannelAvailabilityTransition()
	require.NoError(t, err)
	require.NotNil(t, transition)
	assert.True(t, transition.FromAvailable)
	assert.False(t, transition.ToAvailable)
}

func TestClaimChannelAvailabilityTransitionCASAllowsOneWinner(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)
	require.NoError(t, InitializeChannelAvailabilityState())
	require.NoError(t, DB.Create(&Channel{Name: "primary", Status: common.ChannelStatusEnabled}).Error)

	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			transition, err := ClaimChannelAvailabilityTransition()
			assert.NoError(t, err)
			if transition != nil {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), winners.Load())
	var eventCount int64
	require.NoError(t, DB.Model(&ChannelAvailabilityNotificationEvent{}).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
}

func TestReconcileNoChangeLeavesPersistedStateUntouched(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)
	require.NoError(t, InitializeChannelAvailabilityState())

	var before ChannelAvailabilityState
	require.NoError(t, DB.First(&before, "id = ?", channelAvailabilityStateID).Error)
	event, err := ReconcileChannelAvailabilityNotification(ChannelAvailabilityNotificationInput{
		Notify:         true,
		RecipientsJSON: `["admin@example.com"]`,
	})
	require.NoError(t, err)
	assert.Nil(t, event)

	var after ChannelAvailabilityState
	require.NoError(t, DB.First(&after, "id = ?", channelAvailabilityStateID).Error)
	assert.Equal(t, before, after)
}

func TestReconcileQueuesRapidOppositeEdgesInOrder(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)
	require.NoError(t, InitializeChannelAvailabilityState())

	channel := Channel{Name: "primary", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&channel).Error)
	recovery, err := ReconcileChannelAvailabilityNotification(ChannelAvailabilityNotificationInput{
		Notify:              true,
		Source:              "channel created",
		RecipientsJSON:      `["admin@example.com"]`,
		RelatedChannelsJSON: `[{"id":1,"name":"primary"}]`,
	})
	require.NoError(t, err)
	require.NotNil(t, recovery)
	assert.False(t, recovery.FromAvailable)
	assert.True(t, recovery.ToAvailable)
	assert.Equal(t, int64(1), recovery.NotificationRevision)

	require.NoError(t, DB.Model(&channel).Update("status", common.ChannelStatusAutoDisabled).Error)
	outage, err := ReconcileChannelAvailabilityNotification(ChannelAvailabilityNotificationInput{
		Notify:         true,
		Source:         "automatic channel disable",
		RecipientsJSON: `["admin@example.com"]`,
	})
	require.NoError(t, err)
	require.NotNil(t, outage)
	assert.True(t, outage.FromAvailable)
	assert.False(t, outage.ToAvailable)
	assert.Equal(t, int64(2), outage.NotificationRevision)

	var events []ChannelAvailabilityNotificationEvent
	require.NoError(t, DB.Order("notification_revision asc").Find(&events).Error)
	require.Len(t, events, 2)
	assert.Equal(t, int64(1), events[0].NotificationRevision)
	assert.False(t, events[0].FromAvailable)
	assert.True(t, events[0].ToAvailable)
	assert.Equal(t, int64(2), events[1].NotificationRevision)
	assert.True(t, events[1].FromAvailable)
	assert.False(t, events[1].ToAvailable)

	var state ChannelAvailabilityState
	require.NoError(t, DB.First(&state, "id = ?", channelAvailabilityStateID).Error)
	assert.False(t, state.Available)
	assert.False(t, state.NotifiedAvailable)
}

func TestReconcileQueuesOutageAndRecoveryInOrder(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)
	channel := Channel{Name: "primary", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&channel).Error)
	require.NoError(t, InitializeChannelAvailabilityState())

	require.NoError(t, DB.Model(&channel).Update("status", common.ChannelStatusAutoDisabled).Error)
	outage, err := ReconcileChannelAvailabilityNotification(ChannelAvailabilityNotificationInput{
		Notify:         true,
		Source:         "automatic channel disable",
		RecipientsJSON: `["admin@example.com"]`,
	})
	require.NoError(t, err)
	require.NotNil(t, outage)
	assert.True(t, outage.FromAvailable)
	assert.False(t, outage.ToAvailable)

	require.NoError(t, DB.Model(&channel).Update("status", common.ChannelStatusEnabled).Error)
	recovery, err := ReconcileChannelAvailabilityNotification(ChannelAvailabilityNotificationInput{
		Notify:         true,
		Source:         "channel state change",
		RecipientsJSON: `["admin@example.com"]`,
	})
	require.NoError(t, err)
	require.NotNil(t, recovery)
	assert.False(t, recovery.FromAvailable)
	assert.True(t, recovery.ToAvailable)
	assert.Equal(t, int64(2), recovery.NotificationRevision)
}

func TestSyncPreservesPendingNotificationEvents(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)
	require.NoError(t, InitializeChannelAvailabilityState())
	channel := Channel{Name: "primary", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&channel).Error)
	_, err := ReconcileChannelAvailabilityNotification(ChannelAvailabilityNotificationInput{
		Notify:         true,
		RecipientsJSON: `["admin@example.com"]`,
	})
	require.NoError(t, err)

	require.NoError(t, DB.Model(&channel).Update("status", common.ChannelStatusAutoDisabled).Error)
	require.NoError(t, SyncChannelAvailabilityState())

	var state ChannelAvailabilityState
	require.NoError(t, DB.First(&state, "id = ?", channelAvailabilityStateID).Error)
	assert.False(t, state.Available)
	assert.False(t, state.NotifiedAvailable)
	assert.Equal(t, int64(1), state.NotificationRevision)
	var events []ChannelAvailabilityNotificationEvent
	require.NoError(t, DB.Find(&events).Error)
	assert.Len(t, events, 1)
}

func TestClaimNotificationEventsRespectsOrderAndLeases(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)
	require.NoError(t, InitializeChannelAvailabilityState())
	channel := Channel{Name: "primary", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&channel).Error)
	first, err := ReconcileChannelAvailabilityNotification(ChannelAvailabilityNotificationInput{
		Notify:         true,
		RecipientsJSON: `["admin@example.com"]`,
	})
	require.NoError(t, err)
	require.NotNil(t, first)
	require.NoError(t, DB.Model(&channel).Update("status", common.ChannelStatusAutoDisabled).Error)
	second, err := ReconcileChannelAvailabilityNotification(ChannelAvailabilityNotificationInput{
		Notify:         true,
		RecipientsJSON: `["admin@example.com"]`,
	})
	require.NoError(t, err)
	require.NotNil(t, second)

	claimed, err := ClaimNextChannelAvailabilityNotificationEvent("owner-a", 100)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, first.ID, claimed.ID)
	blocked, err := ClaimNextChannelAvailabilityNotificationEvent("owner-b", 101)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	retryAt, err := GetChannelAvailabilityNotificationRetryAt(101)
	require.NoError(t, err)
	assert.Equal(t, claimed.LeaseUntil, retryAt)

	require.NoError(t, CompleteChannelAvailabilityNotificationEvent(first.ID, "owner-a", `[]`))
	claimed, err = ClaimNextChannelAvailabilityNotificationEvent("owner-b", 101)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, second.ID, claimed.ID)

	expired, err := ClaimNextChannelAvailabilityNotificationEvent("owner-c", claimed.LeaseUntil+1)
	require.NoError(t, err)
	require.NotNil(t, expired)
	assert.Equal(t, second.ID, expired.ID)
}

func TestCancelledNotificationEventIsTerminal(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)
	require.NoError(t, InitializeChannelAvailabilityState())
	require.NoError(t, DB.Create(&Channel{Name: "primary", Status: common.ChannelStatusEnabled}).Error)
	event, err := ReconcileChannelAvailabilityNotification(ChannelAvailabilityNotificationInput{
		Notify:         true,
		RecipientsJSON: `["admin@example.com"]`,
	})
	require.NoError(t, err)
	require.NotNil(t, event)

	require.NoError(t, DB.Model(&ChannelAvailabilityNotificationEvent{}).
		Where("id = ?", event.ID).
		Updates(map[string]any{
			"status":      ChannelAvailabilityEventProcessing,
			"owner":       "stopped-instance",
			"lease_until": int64(10_000),
		}).Error)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return CancelPendingChannelAvailabilityNotificationEventsWithDB(tx)
	}))
	claimed, err := ClaimNextChannelAvailabilityNotificationEvent("owner", 100)
	require.NoError(t, err)
	assert.Nil(t, claimed)
	require.NoError(t, DB.First(event, event.ID).Error)
	assert.Equal(t, ChannelAvailabilityEventCancelled, event.Status)
}

func TestClaimChannelAvailabilityTransitionUpdatesCountsWithoutEmittingEdge(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)
	require.NoError(t, DB.Create(&Channel{Name: "primary", Status: common.ChannelStatusEnabled}).Error)
	require.NoError(t, InitializeChannelAvailabilityState())

	require.NoError(t, DB.Create(&Channel{Name: "disabled", Status: common.ChannelStatusManuallyDisabled}).Error)
	transition, err := ClaimChannelAvailabilityTransition()
	require.NoError(t, err)
	assert.Nil(t, transition)

	var state ChannelAvailabilityState
	require.NoError(t, DB.First(&state, "id = ?", channelAvailabilityStateID).Error)
	assert.True(t, state.Available)
	assert.Equal(t, int64(1), state.EnabledCount)
	assert.Equal(t, int64(2), state.TotalCount)
}

func TestMultiKeyOnlyChangesOverallAvailabilityWhenAllKeysDisabled(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)
	channel := Channel{
		Name:   "multi",
		Key:    "key-one\nkey-two",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
		},
	}
	require.NoError(t, DB.Create(&channel).Error)
	require.NoError(t, InitializeChannelAvailabilityState())

	assert.True(t, UpdateChannelStatus(channel.Id, "key-one", common.ChannelStatusAutoDisabled, "failed"))
	transition, err := ClaimChannelAvailabilityTransition()
	require.NoError(t, err)
	assert.Nil(t, transition)

	assert.True(t, UpdateChannelStatus(channel.Id, "key-two", common.ChannelStatusAutoDisabled, "failed"))
	transition, err = ClaimChannelAvailabilityTransition()
	require.NoError(t, err)
	require.NotNil(t, transition)
	assert.False(t, transition.ToAvailable)
}

func TestMultiKeyEditPreservesManualChannelDisable(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)
	channel := Channel{
		Name:   "multi",
		Key:    "key-one\nkey-two",
		Status: common.ChannelStatusManuallyDisabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusManuallyDisabled,
				1: common.ChannelStatusManuallyDisabled,
			},
		},
	}
	require.NoError(t, DB.Create(&channel).Error)

	update := Channel{
		Id:          channel.Id,
		Name:        "renamed",
		ChannelInfo: channel.ChannelInfo,
	}
	require.NoError(t, update.Update())

	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
}

func TestMultiKeyEditPreservesWholeChannelAutoDisable(t *testing.T) {
	setupChannelAvailabilityStateTestDB(t)
	channel := Channel{
		Name:   "multi",
		Key:    "key-one\nkey-two",
		Status: common.ChannelStatusAutoDisabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
		},
	}
	require.NoError(t, DB.Create(&channel).Error)

	update := Channel{
		Id:          channel.Id,
		Name:        "renamed",
		ChannelInfo: channel.ChannelInfo,
	}
	require.NoError(t, update.Update())

	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
}
