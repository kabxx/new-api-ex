package model

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	channelSelectionMetricSampleLimit = 100
	channelSelectionMetricWarmup      = 3
)

type channelSelectionMetricKey struct {
	channelID int
	model     string
}

type channelSelectionObservation struct {
	Success             bool
	FirstResponseMillis int64
}

type channelSelectionMetric struct {
	Observations []channelSelectionObservation
	LatencyEWMA  float64
	LatencyCount int
}

// ChannelSelectionMetricState is a bounded per-channel/model history shared
// by instances. Model is part of the key because TTFT and success behavior can
// differ substantially between upstream models.
type ChannelSelectionMetricState struct {
	ChannelID     int    `gorm:"primaryKey"`
	Model         string `gorm:"primaryKey;size:191"`
	Observations  string `gorm:"type:text"`
	LatencyEWMA   float64
	LatencyCount  int
	UpdatedAtUnix int64
}

var channelSelectionMetrics = struct {
	sync.RWMutex
	values map[channelSelectionMetricKey]*channelSelectionMetric
}{
	values: make(map[channelSelectionMetricKey]*channelSelectionMetric),
}

var channelSelectionMetricTableAvailability sync.Map

func PersistentChannelSelectionMetricAvailable() bool {
	if DB == nil {
		return false
	}
	if value, ok := channelSelectionMetricTableAvailability.Load(DB); ok {
		return value.(bool)
	}
	available := DB.Migrator().HasTable(&ChannelSelectionMetricState{})
	channelSelectionMetricTableAvailability.Store(DB, available)
	return available
}

func loadPersistentChannelSelectionMetric(channelID int, modelName string) (channelSelectionMetric, error) {
	var state ChannelSelectionMetricState
	if err := DB.Where("channel_id = ? AND model = ?", channelID, modelName).First(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return channelSelectionMetric{}, nil
		}
		return channelSelectionMetric{}, err
	}
	metric := channelSelectionMetric{LatencyEWMA: state.LatencyEWMA, LatencyCount: state.LatencyCount}
	if state.Observations != "" {
		if err := common.UnmarshalJsonStr(state.Observations, &metric.Observations); err != nil {
			return channelSelectionMetric{}, err
		}
	}
	return metric, nil
}

func recordPersistentChannelSelectionOutcome(channelID int, modelName string, observation channelSelectionObservation) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var state ChannelSelectionMetricState
		query := lockForUpdate(tx).Where("channel_id = ? AND model = ?", channelID, modelName).First(&state)
		if query.Error != nil && query.Error != gorm.ErrRecordNotFound {
			return query.Error
		}
		metric := channelSelectionMetric{}
		if query.Error == nil {
			metric.LatencyEWMA = state.LatencyEWMA
			metric.LatencyCount = state.LatencyCount
			if state.Observations != "" {
				if err := common.UnmarshalJsonStr(state.Observations, &metric.Observations); err != nil {
					return err
				}
			}
		} else {
			state.ChannelID = channelID
			state.Model = modelName
		}
		metric.Observations = append(metric.Observations, observation)
		if observation.Success && observation.FirstResponseMillis > 0 {
			if metric.LatencyCount == 0 {
				metric.LatencyEWMA = float64(observation.FirstResponseMillis)
			} else {
				metric.LatencyEWMA = metric.LatencyEWMA*0.7 + float64(observation.FirstResponseMillis)*0.3
			}
			metric.LatencyCount++
		}
		if len(metric.Observations) > channelSelectionMetricSampleLimit {
			metric.Observations = append([]channelSelectionObservation(nil), metric.Observations[len(metric.Observations)-channelSelectionMetricSampleLimit:]...)
		}
		encoded, err := common.Marshal(metric.Observations)
		if err != nil {
			return err
		}
		state.Observations = string(encoded)
		state.LatencyEWMA = metric.LatencyEWMA
		state.LatencyCount = metric.LatencyCount
		state.UpdatedAtUnix = time.Now().Unix()
		return tx.Save(&state).Error
	})
}

// RecordChannelSelectionOutcome records only the outcome of a request that
// reached an upstream channel. Histories are bounded and persisted when the
// application database is available; the in-memory copy keeps selection fast.
func RecordChannelSelectionOutcome(channelID int, modelName string, success bool, firstResponseMillis int64) {
	key := channelSelectionMetricKey{channelID: channelID, model: modelName}
	observation := channelSelectionObservation{Success: success, FirstResponseMillis: firstResponseMillis}
	channelSelectionMetrics.Lock()
	metric := channelSelectionMetrics.values[key]
	if metric == nil {
		metric = &channelSelectionMetric{}
		channelSelectionMetrics.values[key] = metric
	}
	metric.Observations = append(metric.Observations, observation)
	if success && firstResponseMillis > 0 {
		if metric.LatencyCount == 0 {
			metric.LatencyEWMA = float64(firstResponseMillis)
		} else {
			metric.LatencyEWMA = metric.LatencyEWMA*0.7 + float64(firstResponseMillis)*0.3
		}
		metric.LatencyCount++
	}
	if len(metric.Observations) > channelSelectionMetricSampleLimit {
		metric.Observations = append([]channelSelectionObservation(nil), metric.Observations[len(metric.Observations)-channelSelectionMetricSampleLimit:]...)
	}
	channelSelectionMetrics.Unlock()
	if PersistentChannelSelectionMetricAvailable() {
		_ = recordPersistentChannelSelectionOutcome(channelID, modelName, observation)
	}
}

func ResetChannelSelectionMetrics() {
	channelSelectionMetrics.Lock()
	channelSelectionMetrics.values = make(map[channelSelectionMetricKey]*channelSelectionMetric)
	channelSelectionMetrics.Unlock()
	if PersistentChannelSelectionMetricAvailable() {
		_ = DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&ChannelSelectionMetricState{}).Error
	}
}

func getChannelSelectionMetric(channelID int, modelName string) channelSelectionMetric {
	if PersistentChannelSelectionMetricAvailable() {
		if metric, err := loadPersistentChannelSelectionMetric(channelID, modelName); err == nil && len(metric.Observations) > 0 {
			return metric
		}
	}
	channelSelectionMetrics.RLock()
	defer channelSelectionMetrics.RUnlock()
	metric := channelSelectionMetrics.values[channelSelectionMetricKey{channelID: channelID, model: modelName}]
	if metric == nil {
		return channelSelectionMetric{}
	}
	copyMetric := *metric
	copyMetric.Observations = append([]channelSelectionObservation(nil), metric.Observations...)
	return copyMetric
}

func channelSelectionReady(metric channelSelectionMetric) bool {
	return len(metric.Observations) >= channelSelectionMetricWarmup
}

func channelLatencyScore(metric channelSelectionMetric) float64 {
	if metric.LatencyCount == 0 {
		return math.Inf(1)
	}
	return metric.LatencyEWMA
}

func channelStabilityScore(metric channelSelectionMetric) float64 {
	if len(metric.Observations) == 0 {
		return 0.5
	}
	successes := 0
	for _, observation := range metric.Observations {
		if observation.Success {
			successes++
		}
	}
	return float64(successes) / float64(len(metric.Observations))
}

func chooseSamePriorityChannel(channels []*Channel, modelName, strategy string) *Channel {
	if len(channels) <= 1 || strategy == "" || strategy == "weighted_random" {
		return weightedRetryChannel(channels)
	}

	ready := make([]*Channel, 0, len(channels))
	unknown := make([]*Channel, 0, len(channels))
	metrics := make(map[int]channelSelectionMetric, len(channels))
	for _, channel := range channels {
		metric := getChannelSelectionMetric(channel.Id, modelName)
		metrics[channel.Id] = metric
		if channelSelectionReady(metric) {
			ready = append(ready, channel)
		} else {
			unknown = append(unknown, channel)
		}
	}
	if len(unknown) > 0 {
		return weightedRetryChannel(unknown)
	}
	if len(ready) == 0 {
		return weightedRetryChannel(channels)
	}

	best := ready[0]
	if strategy == "latency_first" {
		for _, channel := range ready[1:] {
			if channelLatencyScore(metrics[channel.Id]) < channelLatencyScore(metrics[best.Id]) {
				best = channel
			}
		}
		bestLatency := channelLatencyScore(metrics[best.Id])
		close := make([]*Channel, 0, len(ready))
		for _, channel := range ready {
			latency := channelLatencyScore(metrics[channel.Id])
			if latency <= bestLatency*1.15+50 {
				close = append(close, channel)
			}
		}
		return weightedRetryChannel(close)
	}

	for _, channel := range ready[1:] {
		if channelStabilityScore(metrics[channel.Id]) > channelStabilityScore(metrics[best.Id]) {
			best = channel
		}
	}
	bestScore := channelStabilityScore(metrics[best.Id])
	close := make([]*Channel, 0, len(ready))
	for _, channel := range ready {
		if channelStabilityScore(metrics[channel.Id])+0.02 >= bestScore {
			close = append(close, channel)
		}
	}
	return weightedRetryChannel(close)
}
