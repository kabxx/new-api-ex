package model

import (
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const channelFailureObservationLimit = 10000

// ChannelFailureState stores bounded, key-hashed failure history so a policy
// is shared by all application instances and survives a restart.
type ChannelFailureState struct {
	ChannelID     int    `gorm:"primaryKey"`
	KeyHash       string `gorm:"primaryKey;size:64"`
	Consecutive   int
	Observations  string `gorm:"type:text"`
	UpdatedAtUnix int64
}

type ChannelFailureObservation struct {
	At     int64 `json:"at"`
	Failed bool  `json:"failed"`
}

var channelFailureStateTableAvailability sync.Map

func PersistentChannelFailureStateAvailable() bool {
	if DB == nil {
		return false
	}
	if value, ok := channelFailureStateTableAvailability.Load(DB); ok {
		return value.(bool)
	}
	available := DB.Migrator().HasTable(&ChannelFailureState{})
	channelFailureStateTableAvailability.Store(DB, available)
	return available
}

func loadChannelFailureState(tx *gorm.DB, channelID int, keyHash string) (ChannelFailureState, []ChannelFailureObservation, error) {
	var state ChannelFailureState
	err := lockForUpdate(tx).Where("channel_id = ? AND key_hash = ?", channelID, keyHash).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ChannelFailureState{ChannelID: channelID, KeyHash: keyHash}, nil, nil
	}
	if err != nil {
		return state, nil, err
	}
	var observations []ChannelFailureObservation
	if state.Observations != "" {
		if err := common.UnmarshalJsonStr(state.Observations, &observations); err != nil {
			return state, nil, err
		}
	}
	return state, observations, nil
}

func saveChannelFailureState(tx *gorm.DB, state ChannelFailureState, observations []ChannelFailureObservation) error {
	encoded, err := common.Marshal(observations)
	if err != nil {
		return err
	}
	state.Observations = string(encoded)
	state.UpdatedAtUnix = time.Now().Unix()
	return tx.Save(&state).Error
}

func pruneFailureObservations(observations []ChannelFailureObservation, now time.Time, window time.Duration) []ChannelFailureObservation {
	if window > 0 {
		cutoff := now.Add(-window).Unix()
		first := 0
		for first < len(observations) && observations[first].At < cutoff {
			first++
		}
		observations = observations[first:]
	}
	if len(observations) > channelFailureObservationLimit {
		observations = observations[len(observations)-channelFailureObservationLimit:]
	}
	return append([]ChannelFailureObservation(nil), observations...)
}

func countFailureObservations(observations []ChannelFailureObservation) int {
	count := 0
	for _, observation := range observations {
		if observation.Failed {
			count++
		}
	}
	return count
}

func RecordPersistentChannelFailure(channelID int, keyHash string, now time.Time, strategy string, tolerance, windowFailures, rateSampleSize, rateMinSamples int, rateThresholdPercent float64, window time.Duration) (bool, error) {
	var triggered bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		state, observations, err := loadChannelFailureState(tx, channelID, keyHash)
		if err != nil {
			return err
		}
		observations = append(observations, ChannelFailureObservation{At: now.Unix(), Failed: true})
		observations = pruneFailureObservations(observations, now, window)
		state.Consecutive++
		switch strategy {
		case "window":
			triggered = countFailureObservations(observations) >= windowFailures
		case "failure_rate":
			sample := observations
			if len(sample) > rateSampleSize {
				sample = sample[len(sample)-rateSampleSize:]
			}
			triggered = len(sample) >= rateMinSamples &&
				float64(countFailureObservations(sample))*100 >= rateThresholdPercent*float64(len(sample))
		default:
			triggered = state.Consecutive > tolerance
		}
		if triggered {
			return tx.Delete(&ChannelFailureState{}, "channel_id = ? AND key_hash = ?", channelID, keyHash).Error
		}
		return saveChannelFailureState(tx, state, observations)
	})
	return triggered, err
}

func RecordPersistentChannelSuccess(channelID int, keyHash string, now time.Time, window time.Duration) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		state, observations, err := loadChannelFailureState(tx, channelID, keyHash)
		if err != nil {
			return err
		}
		state.Consecutive = 0
		observations = append(observations, ChannelFailureObservation{At: now.Unix(), Failed: false})
		observations = pruneFailureObservations(observations, now, window)
		return saveChannelFailureState(tx, state, observations)
	})
}

func ResetPersistentChannelFailureState(channelID int, keyHash string) error {
	return DB.Where("channel_id = ? AND key_hash = ?", channelID, keyHash).Delete(&ChannelFailureState{}).Error
}
