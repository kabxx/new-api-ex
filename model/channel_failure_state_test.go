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

func setupChannelFailureStateModelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousDatabaseType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&ChannelFailureState{}))
	t.Cleanup(func() {
		_ = sqlDB.Close()
		DB = previousDB
		common.SetMainDatabaseType(previousDatabaseType)
	})
	return db
}

func TestNormalizeLegacyChannelFailureStatesPreservesObservations(t *testing.T) {
	db := setupChannelFailureStateModelTestDB(t)
	legacyJSON, err := common.Marshal([]ChannelFailureObservation{
		{At: 100, Failed: true},
		{At: 200, Failed: false},
	})
	require.NoError(t, err)
	legacy := ChannelFailureState{
		ChannelID:    101,
		KeyHash:      "legacy-hash",
		Consecutive:  1,
		Observations: string(legacyJSON),
	}
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.Exec(
		"UPDATE channel_failure_states SET revision = NULL, format_version = NULL, policy_signature = NULL, claimed = NULL, threshold_reached = NULL WHERE channel_id = ?",
		legacy.ChannelID,
	).Error)

	require.NoError(t, normalizeLegacyChannelFailureStates())
	var normalized ChannelFailureState
	require.NoError(t, db.First(&normalized, "channel_id = ?", legacy.ChannelID).Error)
	assert.Equal(t, int64(0), normalized.Revision)
	assert.Equal(t, 0, normalized.FormatVersion)
	assert.Equal(t, string(legacyJSON), normalized.Observations)

	claimToken, err := RecordPersistentChannelOutcome(
		legacy.ChannelID,
		legacy.KeyHash,
		time.Unix(300, 0),
		true,
		ChannelFailurePolicy{Strategy: "consecutive", ConsecutiveThreshold: 2},
	)
	require.NoError(t, err)
	assert.NotEmpty(t, claimToken)
	require.NoError(t, db.First(&normalized, "channel_id = ?", legacy.ChannelID).Error)
	assert.Empty(t, normalized.Observations)
	assert.Equal(t, channelFailureStateVersion, normalized.FormatVersion)
	assert.Equal(t, int64(1), normalized.Revision)
}

func TestRuntimeCASAcceptsLegacyNullRevisionWithoutStartupMigration(t *testing.T) {
	db := setupChannelFailureStateModelTestDB(t)
	state := ChannelFailureState{ChannelID: 102, KeyHash: "runtime-legacy", Consecutive: 0}
	require.NoError(t, db.Create(&state).Error)
	require.NoError(t, db.Exec("UPDATE channel_failure_states SET revision = NULL WHERE channel_id = ?", state.ChannelID).Error)

	claimToken, err := RecordPersistentChannelOutcome(
		state.ChannelID,
		state.KeyHash,
		time.Unix(100, 0),
		true,
		ChannelFailurePolicy{Strategy: "consecutive", ConsecutiveThreshold: 1},
	)
	require.NoError(t, err)
	assert.NotEmpty(t, claimToken)
	var updated ChannelFailureState
	require.NoError(t, db.First(&updated, "channel_id = ?", state.ChannelID).Error)
	assert.Equal(t, int64(1), updated.Revision)
	assert.Equal(t, 1, updated.Consecutive)
}

func TestNormalizeLegacyChannelFailureStatesIsIdempotentForNewRows(t *testing.T) {
	db := setupChannelFailureStateModelTestDB(t)
	state := ChannelFailureState{
		ChannelID:        103,
		KeyHash:          "new-state",
		Consecutive:      4,
		RateCount:        4,
		ThresholdReached: true,
		Claimed:          true,
		ClaimToken:       "current-owner",
		ClaimedAtUnix:    123,
		PolicySignature:  "new-policy",
		Revision:         7,
		FormatVersion:    channelFailureStateVersion,
		Observations:     "must-remain",
	}
	require.NoError(t, db.Create(&state).Error)
	require.NoError(t, db.Exec("UPDATE channel_failure_states SET policy_signature = NULL WHERE channel_id = ?", state.ChannelID).Error)
	require.NoError(t, normalizeLegacyChannelFailureStates())
	var unchanged ChannelFailureState
	require.NoError(t, db.First(&unchanged, "channel_id = ?", state.ChannelID).Error)
	assert.Empty(t, unchanged.PolicySignature)
	assert.Equal(t, state.Revision, unchanged.Revision)
	assert.Equal(t, state.FormatVersion, unchanged.FormatVersion)
	assert.Equal(t, state.Consecutive, unchanged.Consecutive)
	assert.Equal(t, state.RateCount, unchanged.RateCount)
	assert.Equal(t, state.ThresholdReached, unchanged.ThresholdReached)
	assert.Equal(t, state.Claimed, unchanged.Claimed)
	assert.Equal(t, state.ClaimToken, unchanged.ClaimToken)
	assert.Equal(t, state.ClaimedAtUnix, unchanged.ClaimedAtUnix)
	assert.Equal(t, state.Observations, unchanged.Observations)
}

func TestSQLiteBusyRetryIsBoundedAndCanRecover(t *testing.T) {
	previousSleep := channelFailureStateSleep
	t.Cleanup(func() { channelFailureStateSleep = previousSleep })
	sleepCalls := 0
	channelFailureStateSleep = func(time.Duration) { sleepCalls++ }

	attempts := 0
	err := retrySQLiteBusyOperation(func() error {
		attempts++
		if attempts < 3 {
			return errors.New("database is locked")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, attempts)
	assert.Equal(t, 2, sleepCalls)

	attempts = 0
	sleepCalls = 0
	err = retrySQLiteBusyOperation(func() error {
		attempts++
		return errors.New("database is locked")
	})
	require.Error(t, err)
	assert.Equal(t, channelFailureStateMaxRetries, attempts)
	assert.Equal(t, channelFailureStateMaxRetries-1, sleepCalls)
}
