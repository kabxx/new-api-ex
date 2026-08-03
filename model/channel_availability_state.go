package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	channelAvailabilityStateID           = 1
	channelAvailabilityReconcileAttempts = 16

	ChannelAvailabilityEventPending    = "pending"
	ChannelAvailabilityEventProcessing = "processing"
	ChannelAvailabilityEventCompleted  = "completed"
	ChannelAvailabilityEventCancelled  = "cancelled"
)

type ChannelAvailabilityState struct {
	ID                   int `gorm:"primaryKey"`
	Available            bool
	NotifiedAvailable    bool
	EnabledCount         int64
	TotalCount           int64
	Revision             int64
	NotificationRevision int64
	UpdatedAt            int64
}

type ChannelAvailabilityNotificationEvent struct {
	ID                   int64 `gorm:"primaryKey;autoIncrement"`
	NotificationRevision int64 `gorm:"uniqueIndex;not null"`
	FromAvailable        bool
	ToAvailable          bool
	EnabledCount         int64
	TotalCount           int64
	Source               string
	RelatedChannelsJSON  string
	RecipientsJSON       string
	Status               string `gorm:"index;not null"`
	Owner                string
	LeaseUntil           int64 `gorm:"index"`
	ResultJSON           string
	CreatedAt            int64
	CompletedAt          int64
}

type ChannelAvailabilitySnapshot struct {
	Available    bool
	EnabledCount int64
	TotalCount   int64
}

type ChannelAvailabilityTransition struct {
	FromAvailable bool
	ToAvailable   bool
	Snapshot      ChannelAvailabilitySnapshot
}

type ChannelAvailabilityNotificationInput struct {
	Notify              bool
	Source              string
	RelatedChannelsJSON string
	RecipientsJSON      string
}

var errChannelAvailabilityCASConflict = errors.New("channel availability state changed concurrently")

func getChannelAvailabilitySnapshot(db *gorm.DB) (ChannelAvailabilitySnapshot, error) {
	var snapshot ChannelAvailabilitySnapshot
	if err := db.Model(&Channel{}).
		Select(
			"COUNT(*) AS total_count, COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS enabled_count",
			common.ChannelStatusEnabled,
		).
		Scan(&snapshot).Error; err != nil {
		return snapshot, err
	}
	snapshot.Available = snapshot.EnabledCount > 0
	return snapshot, nil
}

func GetChannelAvailabilitySnapshot() (ChannelAvailabilitySnapshot, error) {
	return getChannelAvailabilitySnapshot(DB)
}

func availabilityStateFromSnapshot(snapshot ChannelAvailabilitySnapshot) ChannelAvailabilityState {
	return ChannelAvailabilityState{
		ID:                channelAvailabilityStateID,
		Available:         snapshot.Available,
		NotifiedAvailable: snapshot.Available,
		EnabledCount:      snapshot.EnabledCount,
		TotalCount:        snapshot.TotalCount,
		UpdatedAt:         time.Now().Unix(),
	}
}

func InitializeChannelAvailabilityState() error {
	snapshot, err := getChannelAvailabilitySnapshot(DB)
	if err != nil {
		return err
	}
	state := availabilityStateFromSnapshot(snapshot)
	if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; err != nil {
		return err
	}
	// The notification tables first ship together. This also makes development
	// databases created by an earlier uncommitted build establish a quiet baseline.
	return DB.Model(&ChannelAvailabilityState{}).
		Where("id = ? AND notification_revision = 0", channelAvailabilityStateID).
		Update("notified_available", gorm.Expr("available")).Error
}

func syncChannelAvailabilityStateWithDB(db *gorm.DB) error {
	var before ChannelAvailabilityState
	selected := lockForUpdate(db).Where("id = ?", channelAvailabilityStateID).Limit(1).Find(&before)
	if selected.Error != nil {
		return selected.Error
	}
	if selected.RowsAffected == 0 {
		snapshot, err := getChannelAvailabilitySnapshot(db)
		if err != nil {
			return err
		}
		state := availabilityStateFromSnapshot(snapshot)
		created := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&state)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected != 1 {
			return errChannelAvailabilityCASConflict
		}
		return nil
	}

	snapshot, err := getChannelAvailabilitySnapshot(db)
	if err != nil {
		return err
	}
	if before.Available == snapshot.Available &&
		before.NotifiedAvailable == snapshot.Available &&
		before.EnabledCount == snapshot.EnabledCount &&
		before.TotalCount == snapshot.TotalCount {
		return nil
	}
	revision := before.Revision
	if before.Available != snapshot.Available {
		revision++
	}
	updated := db.Model(&ChannelAvailabilityState{}).
		Where("id = ? AND revision = ? AND notification_revision = ?", channelAvailabilityStateID, before.Revision, before.NotificationRevision).
		Updates(map[string]any{
			"available":          snapshot.Available,
			"notified_available": snapshot.Available,
			"enabled_count":      snapshot.EnabledCount,
			"total_count":        snapshot.TotalCount,
			"revision":           revision,
			"updated_at":         time.Now().Unix(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return errChannelAvailabilityCASConflict
	}
	return nil
}

func SyncChannelAvailabilityStateWithDB(db *gorm.DB) error {
	return syncChannelAvailabilityStateWithDB(db)
}

func SyncChannelAvailabilityState() error {
	for range channelAvailabilityReconcileAttempts {
		err := DB.Transaction(func(tx *gorm.DB) error {
			return syncChannelAvailabilityStateWithDB(tx)
		})
		if errors.Is(err, errChannelAvailabilityCASConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("channel availability state did not stabilize")
}

func reconcileChannelAvailabilityNotificationOnce(db *gorm.DB, input ChannelAvailabilityNotificationInput) (*ChannelAvailabilityNotificationEvent, error) {
	var event *ChannelAvailabilityNotificationEvent
	err := db.Transaction(func(tx *gorm.DB) error {
		var before ChannelAvailabilityState
		selected := lockForUpdate(tx).Where("id = ?", channelAvailabilityStateID).Limit(1).Find(&before)
		if selected.Error != nil {
			return selected.Error
		}
		if selected.RowsAffected == 0 {
			snapshot, err := getChannelAvailabilitySnapshot(tx)
			if err != nil {
				return err
			}
			state := availabilityStateFromSnapshot(snapshot)
			created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&state)
			if created.Error != nil {
				return created.Error
			}
			if created.RowsAffected != 1 {
				return errChannelAvailabilityCASConflict
			}
			return nil
		}

		// The singleton lock serializes evaluators. A channel mutation committed
		// after this snapshot is a later edge and its evaluator queues the next event.
		snapshot, err := getChannelAvailabilitySnapshot(tx)
		if err != nil {
			return err
		}
		revision := before.Revision
		if before.Available != snapshot.Available {
			revision++
		}
		notifiedAvailable := before.NotifiedAvailable
		notificationRevision := before.NotificationRevision
		if !input.Notify {
			notifiedAvailable = snapshot.Available
		} else if before.NotifiedAvailable != snapshot.Available {
			notificationRevision++
			queued := &ChannelAvailabilityNotificationEvent{
				NotificationRevision: notificationRevision,
				FromAvailable:        before.NotifiedAvailable,
				ToAvailable:          snapshot.Available,
				EnabledCount:         snapshot.EnabledCount,
				TotalCount:           snapshot.TotalCount,
				Source:               input.Source,
				RelatedChannelsJSON:  input.RelatedChannelsJSON,
				RecipientsJSON:       input.RecipientsJSON,
				Status:               ChannelAvailabilityEventPending,
				CreatedAt:            time.Now().Unix(),
			}
			if err := tx.Create(queued).Error; err != nil {
				return err
			}
			notifiedAvailable = snapshot.Available
			event = queued
		}

		if event == nil &&
			before.Available == snapshot.Available &&
			before.NotifiedAvailable == notifiedAvailable &&
			before.EnabledCount == snapshot.EnabledCount &&
			before.TotalCount == snapshot.TotalCount {
			return nil
		}

		updated := tx.Model(&ChannelAvailabilityState{}).
			Where("id = ? AND revision = ? AND notification_revision = ?", channelAvailabilityStateID, before.Revision, before.NotificationRevision).
			Updates(map[string]any{
				"available":             snapshot.Available,
				"notified_available":    notifiedAvailable,
				"enabled_count":         snapshot.EnabledCount,
				"total_count":           snapshot.TotalCount,
				"revision":              revision,
				"notification_revision": notificationRevision,
				"updated_at":            time.Now().Unix(),
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errChannelAvailabilityCASConflict
		}
		return nil
	})
	return event, err
}

func ReconcileChannelAvailabilityNotification(input ChannelAvailabilityNotificationInput) (*ChannelAvailabilityNotificationEvent, error) {
	for range channelAvailabilityReconcileAttempts {
		event, err := reconcileChannelAvailabilityNotificationOnce(DB, input)
		if errors.Is(err, errChannelAvailabilityCASConflict) {
			continue
		}
		return event, err
	}
	return nil, fmt.Errorf("channel availability notification did not stabilize")
}

func ClaimNextChannelAvailabilityNotificationEvent(owner string, now int64) (*ChannelAvailabilityNotificationEvent, error) {
	var claimed *ChannelAvailabilityNotificationEvent
	err := DB.Transaction(func(tx *gorm.DB) error {
		var event ChannelAvailabilityNotificationEvent
		query := lockForUpdate(tx).
			Where("status IN ?", []string{ChannelAvailabilityEventPending, ChannelAvailabilityEventProcessing}).
			Order("notification_revision asc").
			Limit(1).
			Find(&event)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected == 0 {
			return nil
		}
		if event.Status == ChannelAvailabilityEventProcessing && event.LeaseUntil > now {
			return nil
		}
		var recipients []string
		_ = common.UnmarshalJsonStr(event.RecipientsJSON, &recipients)
		leaseSeconds := int64(60 + len(recipients)*35)
		updated := tx.Model(&ChannelAvailabilityNotificationEvent{}).
			Where("id = ? AND (status = ? OR (status = ? AND lease_until <= ?))", event.ID, ChannelAvailabilityEventPending, ChannelAvailabilityEventProcessing, now).
			Updates(map[string]any{
				"status":      ChannelAvailabilityEventProcessing,
				"owner":       owner,
				"lease_until": now + leaseSeconds,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return nil
		}
		event.Status = ChannelAvailabilityEventProcessing
		event.Owner = owner
		event.LeaseUntil = now + leaseSeconds
		claimed = &event
		return nil
	})
	return claimed, err
}

func GetChannelAvailabilityNotificationRetryAt(now int64) (int64, error) {
	var event ChannelAvailabilityNotificationEvent
	query := DB.
		Where("status IN ?", []string{ChannelAvailabilityEventPending, ChannelAvailabilityEventProcessing}).
		Order("notification_revision asc").
		Limit(1).
		Find(&event)
	if query.Error != nil {
		return 0, query.Error
	}
	if query.RowsAffected == 0 {
		return 0, nil
	}
	if event.Status == ChannelAvailabilityEventProcessing && event.LeaseUntil > now {
		return event.LeaseUntil, nil
	}
	return now, nil
}

func CancelPendingChannelAvailabilityNotificationEventsWithDB(db *gorm.DB) error {
	now := time.Now().Unix()
	return db.Model(&ChannelAvailabilityNotificationEvent{}).
		Where("status IN ?", []string{ChannelAvailabilityEventPending, ChannelAvailabilityEventProcessing}).
		Updates(map[string]any{
			"status":       ChannelAvailabilityEventCancelled,
			"owner":        "",
			"lease_until":  0,
			"result_json":  `{"cancelled":"notifications disabled or reconfigured"}`,
			"completed_at": now,
		}).Error
}

func CompleteChannelAvailabilityNotificationEvent(id int64, owner, resultJSON string) error {
	result := DB.Model(&ChannelAvailabilityNotificationEvent{}).
		Where("id = ? AND status = ? AND owner = ?", id, ChannelAvailabilityEventProcessing, owner).
		Updates(map[string]any{
			"status":       ChannelAvailabilityEventCompleted,
			"owner":        "",
			"lease_until":  0,
			"result_json":  resultJSON,
			"completed_at": time.Now().Unix(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("channel availability notification event lease lost")
	}
	return nil
}

// ClaimChannelAvailabilityTransition remains for focused model tests. Production
// callers use ReconcileChannelAvailabilityNotification so metadata and the edge
// are persisted atomically.
func ClaimChannelAvailabilityTransition() (*ChannelAvailabilityTransition, error) {
	event, err := ReconcileChannelAvailabilityNotification(ChannelAvailabilityNotificationInput{Notify: true})
	if err != nil || event == nil {
		return nil, err
	}
	return &ChannelAvailabilityTransition{
		FromAvailable: event.FromAvailable,
		ToAvailable:   event.ToAvailable,
		Snapshot: ChannelAvailabilitySnapshot{
			Available:    event.ToAvailable,
			EnabledCount: event.EnabledCount,
			TotalCount:   event.TotalCount,
		},
	}, nil
}
