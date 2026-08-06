package model

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelStatusTransitionTest(t *testing.T, memoryCache bool) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousCache := common.MemoryCacheEnabled
	previousDatabaseType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ChannelFailureState{}, &ChannelSelectionMetricState{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = memoryCache
	if memoryCache {
		InitChannelCache()
	}
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousDatabaseType)
		common.MemoryCacheEnabled = previousCache
		_ = sqlDB.Close()
	})
	return db
}

func TestAutomaticChannelStatusCannotOverrideManualDisable(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	channel := Channel{Name: "manual", Key: "key", Status: common.ChannelStatusManuallyDisabled}
	require.NoError(t, db.Create(&channel).Error)

	disable, err := ApplyAutomaticChannelStatus(channel.Id, "", common.ChannelStatusAutoDisabled, "late failure")
	require.NoError(t, err)
	assert.False(t, disable.Changed())

	recover, err := ApplyAutomaticChannelStatus(channel.Id, "", common.ChannelStatusEnabled, "")
	require.NoError(t, err)
	assert.False(t, recover.Changed())

	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
}

func TestAutomaticMultiKeyMutationPreservesManualChannelStatus(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	channel := Channel{
		Name:   "manual-multi",
		Key:    "key-one\nkey-two",
		Status: common.ChannelStatusManuallyDisabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
		},
	}
	require.NoError(t, db.Create(&channel).Error)

	disable, err := ApplyAutomaticChannelStatus(channel.Id, "key-one", common.ChannelStatusAutoDisabled, "upstream failure")
	require.NoError(t, err)
	assert.True(t, disable.KeyChanged)
	assert.False(t, disable.ChannelChanged)

	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[0])

	recover, err := ApplyAutomaticChannelStatus(channel.Id, "key-one", common.ChannelStatusEnabled, "")
	require.NoError(t, err)
	assert.True(t, recover.KeyChanged)
	assert.False(t, recover.ChannelChanged)

	stored, err = GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
	_, exists := stored.ChannelInfo.MultiKeyStatusList[0]
	assert.False(t, exists)
}

func TestAutomaticMultiKeyStatusChangesChannelOnlyAtAvailabilityBoundary(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	channel := Channel{
		Name:   "multi",
		Key:    "key-one\nkey-two",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
		},
	}
	require.NoError(t, db.Create(&channel).Error)

	first, err := ApplyAutomaticChannelStatus(channel.Id, "key-one", common.ChannelStatusAutoDisabled, "failure one")
	require.NoError(t, err)
	assert.True(t, first.KeyChanged)
	assert.False(t, first.ChannelChanged)
	assert.Equal(t, common.ChannelStatusEnabled, first.CurrentStatus)

	last, err := ApplyAutomaticChannelStatus(channel.Id, "key-two", common.ChannelStatusAutoDisabled, "failure two")
	require.NoError(t, err)
	assert.True(t, last.KeyChanged)
	assert.True(t, last.ChannelChanged)
	assert.Equal(t, common.ChannelStatusAutoDisabled, last.CurrentStatus)

	recover, err := ApplyAutomaticChannelStatus(channel.Id, "key-one", common.ChannelStatusEnabled, "")
	require.NoError(t, err)
	assert.True(t, recover.KeyChanged)
	assert.True(t, recover.ChannelChanged)
	assert.Equal(t, common.ChannelStatusEnabled, recover.CurrentStatus)
}

func TestManualStatusUpdateDoesNotPolluteCacheWhenDatabaseWriteFails(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, true)
	channel := Channel{Name: "cached", Key: "key", Status: common.ChannelStatusEnabled, Group: "default", Models: "model"}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&Ability{ChannelId: channel.Id, Group: "default", Model: "model", Enabled: true}).Error)
	InitChannelCache()

	require.NoError(t, db.Migrator().DropTable(&Ability{}))
	_, err := ApplyManualChannelStatus(channel.Id, common.ChannelStatusManuallyDisabled, "manual operation")
	assert.Error(t, err)

	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	cached, err := CacheGetChannel(channel.Id)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, cached.Status)
}

func TestManualStatusUpdateClearsFailureEvidenceAtomically(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	channel := Channel{Name: "manual-reset", Key: "key", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&Ability{ChannelId: channel.Id, Group: "default", Model: "model", Enabled: true}).Error)
	require.NoError(t, db.Create(&ChannelFailureState{
		ChannelID:        channel.Id,
		KeyHash:          ChannelFailureKeyHash(channel.Key),
		Consecutive:      5,
		ThresholdReached: true,
		Claimed:          true,
		ClaimToken:       "owner",
	}).Error)

	mutation, err := ApplyManualChannelStatus(channel.Id, common.ChannelStatusManuallyDisabled, "manual operation")
	require.NoError(t, err)
	assert.True(t, mutation.ChannelChanged)
	var stateCount int64
	require.NoError(t, db.Model(&ChannelFailureState{}).Where("channel_id = ?", channel.Id).Count(&stateCount).Error)
	assert.Zero(t, stateCount)
}

func TestManualMultiKeyStatusIsStrongAgainstAutomaticRecovery(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	channel := Channel{
		Name:   "manual-key",
		Key:    "key-one\nkey-two",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
		},
	}
	require.NoError(t, db.Create(&channel).Error)
	keyIndex := 0
	manual, err := ApplyManualMultiKeyMutation(channel.Id, "disable_key", &keyIndex)
	require.NoError(t, err)
	assert.True(t, manual.KeyChanged)

	automatic, err := ApplyAutomaticChannelStatus(channel.Id, "key-one", common.ChannelStatusEnabled, "")
	require.NoError(t, err)
	assert.False(t, automatic.Changed())
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
}

func TestManualMultiKeyDeletePersistsUpdatedKeyList(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	channel := Channel{
		Name:   "delete-key",
		Key:    "key-one\nkey-two",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
		},
	}
	require.NoError(t, db.Create(&channel).Error)

	keyIndex := 0
	mutation, err := ApplyManualMultiKeyMutation(channel.Id, "delete_key", &keyIndex)
	require.NoError(t, err)
	assert.Equal(t, 1, mutation.ChangedKeys)

	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"key-two"}, stored.GetKeys())
	assert.Equal(t, 1, stored.ChannelInfo.MultiKeySize)
}

func TestAutomaticStatusMutatesEveryDuplicateCredential(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	channel := Channel{
		Name:   "duplicate-key",
		Key:    "same-key\nsame-key",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
		},
	}
	require.NoError(t, db.Create(&channel).Error)

	mutation, err := ApplyAutomaticChannelStatus(channel.Id, "same-key", common.ChannelStatusAutoDisabled, "failed")
	require.NoError(t, err)
	assert.True(t, mutation.ChannelChanged)
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[1])
}

func TestManualBatchStatusRollsBackOnAbilityFailure(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	first := Channel{Name: "first", Key: "one", Status: common.ChannelStatusEnabled}
	second := Channel{Name: "second", Key: "two", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&first).Error)
	require.NoError(t, db.Create(&second).Error)
	require.NoError(t, db.Migrator().DropTable(&Ability{}))

	_, err := ApplyManualChannelStatuses([]int{first.Id, second.Id}, common.ChannelStatusManuallyDisabled, "manual batch")
	assert.Error(t, err)
	var channels []Channel
	require.NoError(t, db.Order("id").Find(&channels).Error)
	require.Len(t, channels, 2)
	assert.Equal(t, common.ChannelStatusEnabled, channels[0].Status)
	assert.Equal(t, common.ChannelStatusEnabled, channels[1].Status)
}

func TestDeleteChannelRemovesReliabilityState(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	channel := Channel{Name: "delete", Key: "key", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&ChannelFailureState{ChannelID: channel.Id, KeyHash: ChannelFailureKeyHash(channel.Key)}).Error)
	require.NoError(t, db.Create(&ChannelSelectionMetricState{ChannelID: channel.Id, Model: "model"}).Error)

	require.NoError(t, channel.Delete())
	var failureCount, metricCount int64
	require.NoError(t, db.Model(&ChannelFailureState{}).Where("channel_id = ?", channel.Id).Count(&failureCount).Error)
	require.NoError(t, db.Model(&ChannelSelectionMetricState{}).Where("channel_id = ?", channel.Id).Count(&metricCount).Error)
	assert.Zero(t, failureCount)
	assert.Zero(t, metricCount)
}

func TestValidateChannelAttemptUsesCurrentDatabaseState(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	channel := Channel{
		Name:   "multi",
		Key:    "key-one\nkey-two",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:         true,
			MultiKeySize:       2,
			MultiKeyStatusList: map[int]int{1: common.ChannelStatusAutoDisabled},
		},
	}
	require.NoError(t, db.Create(&channel).Error)

	validated, err := ValidateChannelAttempt(channel.Id, 0, "key-one")
	require.NoError(t, err)
	assert.Equal(t, channel.Id, validated.Id)

	_, err = ValidateChannelAttempt(channel.Id, 1, "key-two")
	assert.ErrorIs(t, err, ErrChannelKeyDisabled)
	_, err = ValidateChannelAttempt(channel.Id, 0, "stale-key")
	assert.ErrorIs(t, err, ErrChannelKeyChanged)

	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Update("status", common.ChannelStatusManuallyDisabled).Error)
	_, err = ValidateChannelAttempt(channel.Id, 0, "key-one")
	assert.ErrorIs(t, err, ErrChannelAttemptDisabled)

	single := Channel{Name: "single", Key: "new-key", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&single).Error)
	_, err = ValidateChannelAttempt(single.Id, 0, "old-key")
	assert.ErrorIs(t, err, ErrChannelKeyChanged)
}

func TestClaimedAutomaticDisableRequiresCurrentFailureOwner(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	channel := Channel{
		Name:    "claimed-disable",
		Key:     "key",
		Status:  common.ChannelStatusEnabled,
		AutoBan: common.GetPointer(1),
	}
	require.NoError(t, db.Create(&channel).Error)

	staleToken := "stale-owner"
	require.NoError(t, db.Create(&ChannelFailureState{
		ChannelID:        channel.Id,
		KeyHash:          ChannelFailureKeyHash(channel.Key),
		ThresholdReached: true,
		Claimed:          true,
		ClaimToken:       staleToken,
		ClaimedAtUnix:    common.GetTimestamp(),
	}).Error)
	require.NoError(t, db.Where("channel_id = ?", channel.Id).Delete(&ChannelFailureState{}).Error)

	mutation, err := ApplyAutomaticChannelStatusWithClaim(channel.Id, channel.Key, common.ChannelStatusAutoDisabled, "late failure", staleToken)
	require.NoError(t, err)
	assert.False(t, mutation.Changed())

	currentToken := "current-owner"
	require.NoError(t, db.Create(&ChannelFailureState{
		ChannelID:        channel.Id,
		KeyHash:          ChannelFailureKeyHash(channel.Key),
		ThresholdReached: true,
		Claimed:          true,
		ClaimToken:       currentToken,
		ClaimedAtUnix:    common.GetTimestamp(),
	}).Error)
	mutation, err = ApplyAutomaticChannelStatusWithClaim(channel.Id, channel.Key, common.ChannelStatusAutoDisabled, "late failure", staleToken)
	require.NoError(t, err)
	assert.False(t, mutation.Changed())

	mutation, err = ApplyAutomaticChannelStatusWithClaim(channel.Id, channel.Key, common.ChannelStatusAutoDisabled, "current failure", currentToken)
	require.NoError(t, err)
	assert.True(t, mutation.ChannelChanged)
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
}

func TestAutomaticDisableRetriesSQLiteBusyTransaction(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	previousSleep := channelFailureStateSleep
	channelFailureStateSleep = func(time.Duration) {}
	t.Cleanup(func() { channelFailureStateSleep = previousSleep })
	channel := Channel{
		Name:    "busy-retry",
		Key:     "key",
		Status:  common.ChannelStatusEnabled,
		AutoBan: common.GetPointer(1),
	}
	require.NoError(t, db.Create(&channel).Error)
	claimToken := "busy-owner"
	require.NoError(t, db.Create(&ChannelFailureState{
		ChannelID:        channel.Id,
		KeyHash:          ChannelFailureKeyHash(channel.Key),
		ThresholdReached: true,
		Claimed:          true,
		ClaimToken:       claimToken,
		ClaimedAtUnix:    common.GetTimestamp(),
	}).Error)

	updateAttempts := 0
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:auto-disable-busy-once", func(tx *gorm.DB) {
		if tx.Statement.Table != "channels" {
			return
		}
		updateAttempts++
		if updateAttempts == 1 {
			tx.AddError(errors.New("database is locked"))
		}
	}))

	mutation, err := ApplyAutomaticChannelStatusWithClaim(channel.Id, channel.Key, common.ChannelStatusAutoDisabled, "upstream failure", claimToken)
	require.NoError(t, err)
	assert.True(t, mutation.ChannelChanged)
	assert.Equal(t, 2, updateAttempts)
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
}

func TestAutomaticDisableBusyFailureRetainsClaimEvidenceForRetry(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, false)
	previousSleep := channelFailureStateSleep
	channelFailureStateSleep = func(time.Duration) {}
	t.Cleanup(func() { channelFailureStateSleep = previousSleep })
	channel := Channel{
		Name:    "busy-failure",
		Key:     "key",
		Status:  common.ChannelStatusEnabled,
		AutoBan: common.GetPointer(1),
	}
	require.NoError(t, db.Create(&channel).Error)
	claimToken := "first-owner"
	require.NoError(t, db.Create(&ChannelFailureState{
		ChannelID:        channel.Id,
		KeyHash:          ChannelFailureKeyHash(channel.Key),
		Consecutive:      1,
		ThresholdReached: true,
		Claimed:          true,
		ClaimToken:       claimToken,
		ClaimedAtUnix:    common.GetTimestamp(),
		PolicySignature:  "consecutive|1",
	}).Error)

	const callbackName = "test:auto-disable-always-busy"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "channels" {
			tx.AddError(errors.New("database is locked"))
		}
	}))
	_, err := ApplyAutomaticChannelStatusWithClaim(channel.Id, channel.Key, common.ChannelStatusAutoDisabled, "upstream failure", claimToken)
	require.Error(t, err)
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	state, err := LoadPersistentChannelFailureState(channel.Id, ChannelFailureKeyHash(channel.Key))
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.ThresholdReached)
	assert.Equal(t, claimToken, state.ClaimToken)

	db.Callback().Update().Remove(callbackName)
	require.NoError(t, CompletePersistentChannelAutoDisable(channel.Id, ChannelFailureKeyHash(channel.Key), claimToken, false, time.Now()))
	newClaim, err := RecordPersistentChannelOutcome(channel.Id, ChannelFailureKeyHash(channel.Key), time.Now(), true, ChannelFailurePolicy{
		Strategy:             "consecutive",
		ConsecutiveThreshold: 1,
		PolicySignature:      "consecutive|1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, newClaim)
	assert.NotEqual(t, claimToken, newClaim)
}

func TestAutomaticDisableBusyRetryDoesNotPublishRolledBackMutation(t *testing.T) {
	db := setupChannelStatusTransitionTest(t, true)
	channel := Channel{
		Name:    "busy-stale-mutation",
		Key:     "key-one\nkey-two",
		Status:  common.ChannelStatusEnabled,
		AutoBan: common.GetPointer(1),
		Group:   "default",
		Models:  "test-model",
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
		},
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&Ability{
		Group:     channel.Group,
		Model:     channel.Models,
		ChannelId: channel.Id,
		Enabled:   true,
	}).Error)
	claimToken := "original-owner"
	require.NoError(t, db.Create(&ChannelFailureState{
		ChannelID:        channel.Id,
		KeyHash:          ChannelFailureKeyHash("key-one"),
		Consecutive:      3,
		ThresholdReached: true,
		Claimed:          true,
		ClaimToken:       claimToken,
		ClaimedAtUnix:    common.GetTimestamp(),
	}).Error)
	InitChannelCache()

	previousSleep := channelFailureStateSleep
	transitionApplied := false
	var transitionErr error
	channelFailureStateSleep = func(time.Duration) {
		if transitionApplied {
			return
		}
		transitionApplied = true
		transitionErr = db.Model(&ChannelFailureState{}).
			Where("channel_id = ? AND key_hash = ?", channel.Id, ChannelFailureKeyHash("key-one")).
			Updates(map[string]any{
				"claim_token":     "replacement-owner",
				"claimed_at_unix": common.GetTimestamp(),
			}).Error
		if transitionErr != nil {
			return
		}
		transitionErr = db.Model(&Channel{}).Where("id = ?", channel.Id).
			Update("status", common.ChannelStatusManuallyDisabled).Error
		if transitionErr == nil {
			InitChannelCache()
		}
	}
	t.Cleanup(func() { channelFailureStateSleep = previousSleep })
	transactionAttempts := 0
	transaction := func(operation func(*gorm.DB) error) error {
		transactionAttempts++
		return db.Transaction(func(tx *gorm.DB) error {
			err := operation(tx)
			if err == nil && transactionAttempts == 1 {
				return errors.New("database is locked")
			}
			return err
		})
	}

	mutation, err := applyAutomaticChannelStatusWithTransaction(
		channel.Id,
		"key-one",
		common.ChannelStatusAutoDisabled,
		"upstream failure",
		claimToken,
		transaction,
	)
	require.NoError(t, transitionErr)
	require.NoError(t, err)
	assert.True(t, transitionApplied)
	assert.Equal(t, 2, transactionAttempts)
	assert.False(t, mutation.Changed())

	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
	assert.Empty(t, stored.ChannelInfo.MultiKeyStatusList)
	cached, err := CacheGetChannel(channel.Id)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, cached.Status)
	assert.Empty(t, cached.ChannelInfo.MultiKeyStatusList)
	state, err := LoadPersistentChannelFailureState(channel.Id, ChannelFailureKeyHash("key-one"))
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "replacement-owner", state.ClaimToken)
	assert.True(t, state.ThresholdReached)
}
