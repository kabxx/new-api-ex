package model

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	channelFailureStateVersion     = 1
	channelFailureRateCapacity     = 10000
	channelFailureRateStorageBytes = channelFailureRateCapacity / 8
	channelFailureStateMaxRetries  = 12
	channelFailureClaimLease       = 2 * time.Minute
	channelFailureRetryBaseDelay   = 4 * time.Millisecond
	channelFailureRetryMaxDelay    = 128 * time.Millisecond
)

// ChannelFailureState stores compact, bounded, key-hashed failure history so
// routing policy state is shared by all application instances and survives a
// restart. Observations is retained only to migrate rows written by the first
// unreleased implementation of this feature.
type ChannelFailureState struct {
	ChannelID        int    `gorm:"primaryKey"`
	KeyHash          string `gorm:"primaryKey;size:64"`
	Consecutive      int
	WindowFailures   string `gorm:"type:text"`
	RateSamples      string `gorm:"type:text"`
	RateCount        int
	RateCursor       int
	ThresholdReached bool
	Claimed          bool
	ClaimToken       string `gorm:"size:64"`
	ClaimedAtUnix    int64
	PolicySignature  string `gorm:"size:160"`
	Revision         int64
	FormatVersion    int
	Observations     string `gorm:"type:text"`
	UpdatedAtUnix    int64
}

// ChannelFailureObservation is the legacy JSON shape retained for migration.
type ChannelFailureObservation struct {
	At     int64 `json:"at"`
	Failed bool  `json:"failed"`
}

type ChannelFailurePolicy struct {
	Strategy             string
	ConsecutiveThreshold int
	WindowThreshold      int
	Window               time.Duration
	RateSampleSize       int
	RateMinSamples       int
	RateThresholdPercent float64
	PolicySignature      string
}

var channelFailureStateTableAvailability sync.Map

var channelFailureStateSleep = time.Sleep

func channelFailureRetryDelay(attempt int) time.Duration {
	delay := channelFailureRetryBaseDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay >= channelFailureRetryMaxDelay {
			return channelFailureRetryMaxDelay
		}
	}
	return delay
}

func waitForChannelFailureRetry(attempt int) {
	if attempt+1 < channelFailureStateMaxRetries {
		channelFailureStateSleep(channelFailureRetryDelay(attempt))
	}
}

func retrySQLiteBusyOperation(operation func() error) error {
	var err error
	for attempt := 0; attempt < channelFailureStateMaxRetries; attempt++ {
		err = operation()
		if err == nil || !isSQLiteChannelFailureBusy(err) {
			return err
		}
		waitForChannelFailureRetry(attempt)
	}
	return fmt.Errorf("sqlite operation remained busy after %d attempts: %w", channelFailureStateMaxRetries, err)
}

func ChannelFailureKeyHash(usingKey string) string {
	hash := sha256.Sum256([]byte(usingKey))
	return hex.EncodeToString(hash[:])
}

func PersistentChannelFailureStateAvailable() bool {
	if DB == nil {
		return false
	}
	if value, ok := channelFailureStateTableAvailability.Load(DB); ok {
		return value.(bool)
	}
	available := DB.Migrator().HasTable(&ChannelFailureState{})
	if available {
		channelFailureStateTableAvailability.Store(DB, true)
	}
	return available
}

// normalizeLegacyChannelFailureStates makes rows written before the compact
// state format safe for revision CAS. The legacy observations and format
// marker are deliberately preserved so the first outcome can still migrate
// their history instead of silently discarding it.
func normalizeLegacyChannelFailureStates() error {
	if DB == nil || !DB.Migrator().HasTable(&ChannelFailureState{}) {
		return nil
	}
	return retrySQLiteBusyOperation(func() error {
		defaults := []struct {
			column string
			value  any
		}{
			{column: "revision", value: int64(0)},
			{column: "format_version", value: 0},
			{column: "threshold_reached", value: false},
			{column: "claimed", value: false},
			{column: "claimed_at_unix", value: int64(0)},
			{column: "policy_signature", value: ""},
			{column: "rate_count", value: 0},
			{column: "rate_cursor", value: 0},
			{column: "updated_at_unix", value: int64(0)},
			{column: "window_failures", value: ""},
			{column: "rate_samples", value: ""},
			{column: "claim_token", value: ""},
		}
		for _, item := range defaults {
			if err := DB.Model(&ChannelFailureState{}).
				Where(item.column+" IS NULL").
				Update(item.column, item.value).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func deleteChannelFailureStatesTx(tx *gorm.DB, channelID int) error {
	if !tx.Migrator().HasTable(&ChannelFailureState{}) {
		return nil
	}
	return tx.Where("channel_id = ?", channelID).Delete(&ChannelFailureState{}).Error
}

func deleteChannelFailureStatesForKeysTx(tx *gorm.DB, channelID int, keys []string) error {
	if len(keys) == 0 || !tx.Migrator().HasTable(&ChannelFailureState{}) {
		return nil
	}
	hashes := make([]string, 0, len(keys))
	for _, key := range keys {
		hashes = append(hashes, ChannelFailureKeyHash(key))
	}
	return tx.Where("channel_id = ? AND key_hash IN ?", channelID, hashes).Delete(&ChannelFailureState{}).Error
}

func decodeWindowFailures(encoded string) ([]int64, error) {
	if encoded == "" {
		return nil, nil
	}
	parts := strings.Split(encoded, ",")
	timestamps := make([]int64, 0, len(parts))
	var previous int64
	for index, part := range parts {
		value, err := strconv.ParseInt(part, 36, 64)
		if err != nil || value < 0 {
			return nil, fmt.Errorf("invalid channel failure window state")
		}
		if index == 0 {
			previous = value
		} else {
			previous += value
		}
		timestamps = append(timestamps, previous)
	}
	return timestamps, nil
}

func encodeWindowFailures(timestamps []int64) string {
	if len(timestamps) == 0 {
		return ""
	}
	parts := make([]string, len(timestamps))
	parts[0] = strconv.FormatInt(timestamps[0], 36)
	for index := 1; index < len(timestamps); index++ {
		parts[index] = strconv.FormatInt(timestamps[index]-timestamps[index-1], 36)
	}
	return strings.Join(parts, ",")
}

func decodeRateSamples(encoded string) ([]byte, error) {
	bits := make([]byte, channelFailureRateStorageBytes)
	if encoded == "" {
		return bits, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != channelFailureRateStorageBytes {
		return nil, fmt.Errorf("invalid channel failure rate state")
	}
	copy(bits, decoded)
	return bits, nil
}

func rateBit(bits []byte, index int) bool {
	return bits[index/8]&(1<<uint(index%8)) != 0
}

func setRateBit(bits []byte, index int, failed bool) {
	mask := byte(1 << uint(index%8))
	if failed {
		bits[index/8] |= mask
	} else {
		bits[index/8] &^= mask
	}
}

func appendRateSample(state *ChannelFailureState, bits []byte, failed bool) {
	if state.RateCursor < 0 || state.RateCursor >= channelFailureRateCapacity {
		state.RateCursor = 0
	}
	setRateBit(bits, state.RateCursor, failed)
	state.RateCursor = (state.RateCursor + 1) % channelFailureRateCapacity
	if state.RateCount < channelFailureRateCapacity {
		state.RateCount++
	}
}

func countRecentRateFailures(state ChannelFailureState, bits []byte, sampleSize int) (int, int) {
	if sampleSize < 1 || sampleSize > channelFailureRateCapacity {
		sampleSize = channelFailureRateCapacity
	}
	count := state.RateCount
	if count > sampleSize {
		count = sampleSize
	}
	failed := 0
	start := state.RateCursor - count
	for start < 0 {
		start += channelFailureRateCapacity
	}
	for offset := 0; offset < count; offset++ {
		if rateBit(bits, (start+offset)%channelFailureRateCapacity) {
			failed++
		}
	}
	return failed, count
}

func migrateLegacyChannelFailureState(state *ChannelFailureState, now time.Time) ([]int64, []byte, error) {
	windowFailures, err := decodeWindowFailures(state.WindowFailures)
	if err != nil {
		return nil, nil, err
	}
	rateBits, err := decodeRateSamples(state.RateSamples)
	if err != nil {
		return nil, nil, err
	}
	if state.FormatVersion >= channelFailureStateVersion || state.Observations == "" {
		return windowFailures, rateBits, nil
	}
	var observations []ChannelFailureObservation
	if err := common.UnmarshalJsonStr(state.Observations, &observations); err != nil {
		return nil, nil, err
	}
	start := 0
	if len(observations) > channelFailureRateCapacity {
		start = len(observations) - channelFailureRateCapacity
	}
	for _, observation := range observations[start:] {
		appendRateSample(state, rateBits, observation.Failed)
		if observation.Failed {
			windowFailures = append(windowFailures, observation.At)
		}
	}
	if len(windowFailures) > channelFailureRateCapacity {
		windowFailures = windowFailures[len(windowFailures)-channelFailureRateCapacity:]
	}
	sort.Slice(windowFailures, func(i, j int) bool { return windowFailures[i] < windowFailures[j] })
	state.Observations = ""
	state.FormatVersion = channelFailureStateVersion
	state.UpdatedAtUnix = now.Unix()
	return windowFailures, rateBits, nil
}

func ensureChannelFailureState(channelID int, keyHash string) error {
	state := ChannelFailureState{
		ChannelID:     channelID,
		KeyHash:       keyHash,
		FormatVersion: channelFailureStateVersion,
	}
	for attempt := 0; attempt < channelFailureStateMaxRetries; attempt++ {
		err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error
		if err == nil {
			return nil
		}
		if !isSQLiteChannelFailureBusy(err) {
			return err
		}
		waitForChannelFailureRetry(attempt)
	}
	return fmt.Errorf("channel failure state creation remained busy after %d attempts", channelFailureStateMaxRetries)
}

func isSQLiteChannelFailureBusy(err error) bool {
	if err == nil || !common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "locked") || strings.Contains(message, "busy")
}

func updateChannelFailureState(channelID int, keyHash string, now time.Time, mutate func(*ChannelFailureState, *[]int64, []byte) (string, error)) (string, error) {
	if err := ensureChannelFailureState(channelID, keyHash); err != nil {
		return "", err
	}
	for attempt := 0; attempt < channelFailureStateMaxRetries; attempt++ {
		var state ChannelFailureState
		if err := DB.Where("channel_id = ? AND key_hash = ?", channelID, keyHash).First(&state).Error; err != nil {
			if isSQLiteChannelFailureBusy(err) {
				waitForChannelFailureRetry(attempt)
				continue
			}
			return "", err
		}
		windowFailures, rateBits, err := migrateLegacyChannelFailureState(&state, now)
		if err != nil {
			return "", err
		}
		claimToken, err := mutate(&state, &windowFailures, rateBits)
		if err != nil {
			return "", err
		}
		state.WindowFailures = encodeWindowFailures(windowFailures)
		state.RateSamples = base64.RawStdEncoding.EncodeToString(rateBits)
		state.FormatVersion = channelFailureStateVersion
		state.Observations = ""
		state.UpdatedAtUnix = now.Unix()
		previousRevision := state.Revision
		state.Revision++
		revisionCondition := "revision = ?"
		if previousRevision == 0 {
			revisionCondition = "(revision = ? OR revision IS NULL)"
		}
		result := DB.Model(&ChannelFailureState{}).
			Where("channel_id = ? AND key_hash = ?", channelID, keyHash).
			Where(revisionCondition, previousRevision).
			Updates(map[string]any{
				"consecutive":       state.Consecutive,
				"window_failures":   state.WindowFailures,
				"rate_samples":      state.RateSamples,
				"rate_count":        state.RateCount,
				"rate_cursor":       state.RateCursor,
				"threshold_reached": state.ThresholdReached,
				"claimed":           state.Claimed,
				"claim_token":       state.ClaimToken,
				"claimed_at_unix":   state.ClaimedAtUnix,
				"policy_signature":  state.PolicySignature,
				"revision":          state.Revision,
				"format_version":    state.FormatVersion,
				"observations":      state.Observations,
				"updated_at_unix":   state.UpdatedAtUnix,
			})
		if result.Error != nil {
			if isSQLiteChannelFailureBusy(result.Error) {
				waitForChannelFailureRetry(attempt)
				continue
			}
			return "", result.Error
		}
		if result.RowsAffected == 1 {
			return claimToken, nil
		}
		waitForChannelFailureRetry(attempt)
	}
	return "", fmt.Errorf("channel failure state update conflicted after %d attempts", channelFailureStateMaxRetries)
}

func appendFailureWindow(windowFailures *[]int64, now time.Time, window time.Duration, limit int) {
	cutoff := now.Add(-window).Unix()
	retained := (*windowFailures)[:0]
	for _, timestamp := range *windowFailures {
		if timestamp >= cutoff {
			retained = append(retained, timestamp)
		}
	}
	retained = append(retained, now.Unix())
	sort.Slice(retained, func(i, j int) bool { return retained[i] < retained[j] })
	if limit < 1 || limit > channelFailureRateCapacity {
		limit = channelFailureRateCapacity
	}
	if len(retained) > limit {
		retained = retained[len(retained)-limit:]
	}
	*windowFailures = retained
}

func channelFailureThresholdReached(state ChannelFailureState, windowFailures []int64, rateBits []byte, policy ChannelFailurePolicy) bool {
	switch policy.Strategy {
	case "window":
		return len(windowFailures) >= policy.WindowThreshold
	case "failure_rate":
		failed, count := countRecentRateFailures(state, rateBits, policy.RateSampleSize)
		return count >= policy.RateMinSamples && float64(failed)*100 >= policy.RateThresholdPercent*float64(count)
	default:
		threshold := policy.ConsecutiveThreshold
		if threshold < 1 {
			threshold = 1
		}
		return state.Consecutive >= threshold
	}
}

func RecordPersistentChannelOutcome(channelID int, keyHash string, now time.Time, failed bool, policy ChannelFailurePolicy) (string, error) {
	return updateChannelFailureState(channelID, keyHash, now, func(state *ChannelFailureState, windowFailures *[]int64, rateBits []byte) (string, error) {
		if failed && policy.PolicySignature != "" && state.PolicySignature != policy.PolicySignature {
			// Keep bounded observations across a configuration change, but never
			// let a claim created by the previous policy survive it.
			state.ThresholdReached = false
			state.Claimed = false
			state.ClaimToken = ""
			state.ClaimedAtUnix = 0
			state.PolicySignature = policy.PolicySignature
		}
		if state.ThresholdReached {
			claimActive := state.Claimed && state.ClaimedAtUnix > 0 && now.Unix()-state.ClaimedAtUnix < int64(channelFailureClaimLease/time.Second)
			if claimActive {
				return "", nil
			}
			if failed {
				claimToken, err := common.GenerateRandomCharsKey(32)
				if err != nil {
					return "", err
				}
				state.Claimed = true
				state.ClaimToken = claimToken
				state.ClaimedAtUnix = now.Unix()
				return claimToken, nil
			}
			state.ThresholdReached = false
			state.Claimed = false
			state.ClaimToken = ""
			state.ClaimedAtUnix = 0
			appendRateSample(state, rateBits, false)
			state.Consecutive = 0
			return "", nil
		}
		appendRateSample(state, rateBits, failed)
		if failed {
			state.Consecutive++
			appendFailureWindow(windowFailures, now, policy.Window, channelFailureRateCapacity)
		} else {
			state.Consecutive = 0
		}
		if !failed {
			return "", nil
		}
		if !channelFailureThresholdReached(*state, *windowFailures, rateBits, policy) {
			return "", nil
		}
		claimToken, err := common.GenerateRandomCharsKey(32)
		if err != nil {
			return "", err
		}
		state.ThresholdReached = true
		state.Claimed = true
		state.ClaimToken = claimToken
		state.ClaimedAtUnix = now.Unix()
		return claimToken, nil
	})
}

func CompletePersistentChannelAutoDisable(channelID int, keyHash string, claimToken string, succeeded bool, now time.Time) error {
	if claimToken == "" {
		return errors.New("channel failure claim token is required")
	}
	if succeeded {
		return retrySQLiteBusyOperation(func() error {
			return DB.Where(
				"channel_id = ? AND key_hash = ? AND threshold_reached = ? AND claimed = ? AND claim_token = ?",
				channelID, keyHash, true, true, claimToken,
			).Delete(&ChannelFailureState{}).Error
		})
	}
	for attempt := 0; attempt < channelFailureStateMaxRetries; attempt++ {
		var state ChannelFailureState
		err := DB.Where("channel_id = ? AND key_hash = ?", channelID, keyHash).First(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			if isSQLiteChannelFailureBusy(err) {
				waitForChannelFailureRetry(attempt)
				continue
			}
			return err
		}
		if !state.ThresholdReached || !state.Claimed || state.ClaimToken != claimToken {
			return nil
		}
		revisionCondition := "revision = ?"
		if state.Revision == 0 {
			revisionCondition = "(revision = ? OR revision IS NULL)"
		}
		result := DB.Model(&ChannelFailureState{}).
			Where("channel_id = ? AND key_hash = ?", channelID, keyHash).
			Where(revisionCondition, state.Revision).
			Where("threshold_reached = ? AND claimed = ? AND claim_token = ?", true, true, claimToken).
			Updates(map[string]any{
				"claimed":         false,
				"claim_token":     "",
				"claimed_at_unix": int64(0),
				"revision":        state.Revision + 1,
				"updated_at_unix": now.Unix(),
			})
		if result.Error != nil {
			if isSQLiteChannelFailureBusy(result.Error) {
				waitForChannelFailureRetry(attempt)
				continue
			}
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		waitForChannelFailureRetry(attempt)
	}
	return fmt.Errorf("channel failure claim release conflicted after %d attempts", channelFailureStateMaxRetries)
}

func validPersistentChannelFailureClaimTx(tx *gorm.DB, channelID int, keyHash string, claimToken string, nowUnix int64) (bool, error) {
	if claimToken == "" {
		return false, errors.New("channel failure claim token is required")
	}
	var state ChannelFailureState
	err := lockForUpdate(tx).
		Where("channel_id = ? AND key_hash = ?", channelID, keyHash).
		First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	claimActive := state.ThresholdReached && state.Claimed && state.ClaimToken == claimToken &&
		state.ClaimedAtUnix > 0 && nowUnix-state.ClaimedAtUnix < int64(channelFailureClaimLease/time.Second)
	return claimActive, nil
}

func ResetPersistentChannelFailureState(channelID int, keyHash string) error {
	return retrySQLiteBusyOperation(func() error {
		return DB.Where("channel_id = ? AND key_hash = ?", channelID, keyHash).Delete(&ChannelFailureState{}).Error
	})
}

func ResetPersistentChannelFailureStatesForChannel(channelID int) error {
	if DB == nil {
		return errors.New("main database is unavailable")
	}
	return retrySQLiteBusyOperation(func() error {
		return DB.Where("channel_id = ?", channelID).Delete(&ChannelFailureState{}).Error
	})
}

func LoadPersistentChannelFailureState(channelID int, keyHash string) (*ChannelFailureState, error) {
	var state ChannelFailureState
	if err := DB.Where("channel_id = ? AND key_hash = ?", channelID, keyHash).First(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &state, nil
}
