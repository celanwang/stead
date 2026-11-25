package ledger

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/preferences"
	"github.com/celanwang/stead/internal/rsi"
	"github.com/celanwang/stead/internal/scenarios/tracking"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const TrackingNotificationPreference = "tracking.notification"

type TrackingOptions struct {
	// Classify must be pure. It evaluates detached facts inside the intake transaction.
	Classify      tracking.Classifier
	PolicyVersion string
	TaskID        string
}

func TrackingSignal(f tracking.Facts) rsi.Signal {
	impactsConclusion := len(f.AffectedConclusions) != 0
	return rsi.Signal{
		FirstSeen: f.Initial, Material: f.Material, Withdrawn: f.Withdrawn,
		Cosmetic:          !f.Initial && !f.Material && !f.Withdrawn,
		ImpactsConclusion: impactsConclusion,
	}
}

type TrackingRecord struct {
	Seq           uint64
	Observation   tracking.Observation
	Decision      tracking.Decision
	Facts         tracking.Facts
	ReceivedAt    time.Time
	PolicyVersion string
	PolicyUsed    bool
	PreferenceID  string
	TaskID        string
	ReadAt        *time.Time
	SuppressedAt  *time.Time
}

type TrackingIntake struct {
	Record    TrackingRecord
	Duplicate bool
}

type trackingItemRow struct {
	Owner    string        `gorm:"primaryKey"`
	Project  string        `gorm:"primaryKey"`
	Source   string        `gorm:"primaryKey"`
	ObjectID string        `gorm:"primaryKey"`
	Data     tracking.Item `gorm:"serializer:json"`
}

func (trackingItemRow) TableName() string { return "tracked_items" }

type trackingObservationRow struct {
	Seq          uint64 `gorm:"primaryKey"`
	Owner        string
	Project      string
	Source       string
	ObjectID     string
	EventID      string
	Disposition  tracking.Disposition
	ReadAt       *time.Time
	SuppressedAt *time.Time
	Data         TrackingRecord `gorm:"serializer:json"`
}

func (trackingObservationRow) TableName() string { return "tracked_observations" }

func (r trackingObservationRow) record() TrackingRecord {
	out := r.Data
	out.Seq, out.ReadAt, out.SuppressedAt = r.Seq, r.ReadAt, r.SuppressedAt
	return out
}

type preferenceRow struct {
	Owner string `gorm:"primaryKey"`
	ID    string `gorm:"primaryKey"`
	Name  string
	Data  preferences.Record `gorm:"serializer:json"`
}

func (preferenceRow) TableName() string { return "agent_preferences" }

func trackingKey(tx *gorm.DB, key tracking.Key) *gorm.DB {
	return tx.Where("owner = ? AND project = ? AND source = ? AND object_id = ?", key.Owner, key.Project, key.Source, key.ObjectID)
}

func trackingEvent(tx *gorm.DB, event tracking.Observation) *gorm.DB {
	k := event.Key
	return tx.Where("owner = ? AND project = ? AND source = ? AND event_id = ?", k.Owner, k.Project, k.Source, event.EventID)
}

func lockedTrackingItem(tx *gorm.DB, key tracking.Key) (trackingItemRow, error) {
	row := trackingItemRow{
		Owner: key.Owner, Project: key.Project, Source: key.Source, ObjectID: key.ObjectID,
		Data: tracking.Item{Key: key, Mode: tracking.Active},
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return row, err
	}
	var stored trackingItemRow
	err := trackingKey(tx, key).Clauses(forUpdate).Take(&stored).Error
	return stored, err
}

func saveTrackingItem(tx *gorm.DB, row trackingItemRow, item tracking.Item) error {
	row.Data = item
	return trackingKey(tx.Model(&trackingItemRow{}), item.Key).Select("data").Updates(&row).Error
}

var errTrackingEventExists = errors.New("tracking event already exists")

func (l *Ledger) ObserveTracking(ctx context.Context, event tracking.Observation, options TrackingOptions) (TrackingIntake, error) {
	if err := event.Key.Validate(); err != nil {
		return TrackingIntake{}, err
	}
	var written trackingObservationRow
	err := l.transaction(ctx, func(tx *gorm.DB) error {
		item, err := lockedTrackingItem(tx, event.Key)
		if err != nil {
			return err
		}
		var prior trackingObservationRow
		err = trackingEvent(tx, event).Take(&prior).Error
		switch {
		case err == nil:
			return errTrackingEventExists
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}

		at := l.now()
		classifier := options.Classify
		if classifier == nil {
			classifier = tracking.DefaultDisposition
		}
		preference, found, err := resolvePreference(tx, preferences.Context{
			Owner: event.Key.Owner, Project: event.Key.Project, Task: options.TaskID,
		}, TrackingNotificationPreference, at)
		if err != nil {
			return err
		}
		var (
			preferenceID  string
			policyUsed    bool
			recordedFacts tracking.Facts
		)
		next, decision, err := tracking.Apply(item.Data, event, func(facts tracking.Facts) tracking.Disposition {
			recordedFacts = facts
			recordedFacts.AffectedConclusions = slices.Clone(facts.AffectedConclusions)
			if found {
				preferenceID = preference.ID
				return tracking.Disposition(preference.Value)
			}
			policyUsed = true
			return classifier(facts)
		})
		if err != nil {
			return err
		}
		policyVersion := options.PolicyVersion
		if policyVersion == "" {
			policyVersion = "baseline"
		}
		written = trackingObservationRow{
			Owner: item.Owner, Project: item.Project, Source: item.Source, ObjectID: item.ObjectID,
			EventID: event.EventID, Disposition: decision.Disposition,
			Data: TrackingRecord{
				Observation: event, Decision: decision, Facts: recordedFacts, ReceivedAt: at,
				PolicyVersion: policyVersion, PolicyUsed: policyUsed,
				PreferenceID: preferenceID, TaskID: options.TaskID,
			},
		}
		if err := tx.Create(&written).Error; err != nil {
			var duplicate *mysqldriver.MySQLError
			if errors.As(err, &duplicate) && duplicate.Number == 1062 {
				return errTrackingEventExists
			}
			return err
		}
		if decision.Outcome == tracking.Applied {
			return saveTrackingItem(tx, item, next)
		}
		return nil
	})
	if errors.Is(err, errTrackingEventExists) {
		// The losing transaction rolls back its item upsert as well as the intake.
		err = trackingEvent(l.db.WithContext(ctx), event).Take(&written).Error
		if err != nil {
			return TrackingIntake{}, err
		}
		return TrackingIntake{Record: written.record(), Duplicate: true}, nil
	}
	if err != nil {
		return TrackingIntake{}, err
	}
	return TrackingIntake{Record: written.record()}, nil
}

func (l *Ledger) SetTrackingMode(ctx context.Context, key tracking.Key, mode tracking.Mode) (tracking.Item, error) {
	if err := key.Validate(); err != nil {
		return tracking.Item{}, err
	}
	var item tracking.Item
	err := l.transaction(ctx, func(tx *gorm.DB) error {
		row, err := lockedTrackingItem(tx, key)
		if err != nil {
			return err
		}
		item, err = tracking.SetMode(row.Data, mode)
		if err != nil || row.Data.Mode == item.Mode {
			return err
		}
		if err := saveTrackingItem(tx, row, item); err != nil {
			return err
		}
		if mode == tracking.Active {
			return nil
		}
		return pendingTracking(trackingKey(tx.Model(&trackingObservationRow{}), key)).
			Update("suppressed_at", l.now()).Error
	})
	if err != nil {
		return tracking.Item{}, err
	}
	return item, nil
}

func (l *Ledger) TrackingStatus(ctx context.Context, key tracking.Key) (tracking.Item, error) {
	if err := key.Validate(); err != nil {
		return tracking.Item{}, err
	}
	var row trackingItemRow
	err := trackingKey(l.db.WithContext(ctx), key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = delegation.ErrNotFound
	}
	return row.Data, err
}

func (l *Ledger) TrackingHistory(ctx context.Context, key tracking.Key) ([]TrackingRecord, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	return trackingRecords(trackingKey(l.db.WithContext(ctx), key))
}

func (l *Ledger) TrackingFeed(ctx context.Context, owner, project string, kind tracking.Disposition) ([]TrackingRecord, error) {
	if err := validateTrackingScope(owner, project); err != nil {
		return nil, err
	}
	query := pendingTracking(l.db.WithContext(ctx).Where("owner = ? AND project = ?", owner, project))
	switch kind {
	case "":
	case tracking.Immediate, tracking.Digest:
		query = query.Where("disposition = ?", kind)
	default:
		return nil, errors.New("feed kind must be immediate or digest")
	}
	return trackingRecords(query)
}

func (l *Ledger) AcknowledgeTracking(ctx context.Context, owner, project string, seq uint64) (TrackingRecord, error) {
	if err := validateTrackingScope(owner, project); err != nil {
		return TrackingRecord{}, err
	}
	var row trackingObservationRow
	err := l.transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Clauses(forUpdate).
			Take(&row, "owner = ? AND project = ? AND seq = ? AND suppressed_at IS NULL AND disposition IN ?",
				owner, project, seq, []tracking.Disposition{tracking.Immediate, tracking.Digest}).Error; err != nil {
			return err
		}
		if row.ReadAt != nil {
			return nil
		}
		at := l.now()
		row.ReadAt = &at
		return tx.Model(&row).Update("read_at", at).Error
	})
	if err != nil {
		return TrackingRecord{}, err
	}
	return row.record(), nil
}

func validateTrackingScope(owner, project string) error {
	if owner == "" || project == "" || len(owner) > 191 || len(project) > 191 ||
		owner != strings.TrimSpace(owner) || project != strings.TrimSpace(project) {
		return errors.New("tracking requires a valid owner and project")
	}
	return nil
}

func pendingTracking(query *gorm.DB) *gorm.DB {
	return query.Where("read_at IS NULL AND suppressed_at IS NULL AND disposition IN ?",
		[]tracking.Disposition{tracking.Immediate, tracking.Digest})
}

func trackingRecords(query *gorm.DB) ([]TrackingRecord, error) {
	var rows []trackingObservationRow
	if err := query.Order("seq").Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]TrackingRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, row.record())
	}
	return records, nil
}

func (l *Ledger) SetPreference(ctx context.Context, r preferences.Record) (preferences.Record, error) {
	if r.EffectiveAt.IsZero() {
		r.EffectiveAt = l.now()
	}
	if err := r.Validate(); err != nil {
		return preferences.Record{}, err
	}
	if r.RevokedAt != nil || len(r.ID) > 191 || len(r.Name) > 191 || len(r.Owner) > 191 {
		return preferences.Record{}, errors.New("new preference has invalid identity or revocation fields")
	}
	if r.Name == TrackingNotificationPreference {
		switch tracking.Disposition(r.Value) {
		case tracking.Immediate, tracking.Digest, tracking.Silent:
		default:
			return preferences.Record{}, fmt.Errorf("%s must be immediate, digest, or silent", r.Name)
		}
	}
	row := preferenceRow{Owner: r.Owner, ID: r.ID, Name: r.Name, Data: r}
	if err := l.db.WithContext(ctx).Create(&row).Error; err != nil {
		return preferences.Record{}, err
	}
	return r, nil
}

func (l *Ledger) ResolvePreference(ctx context.Context, scope preferences.Context, name string, at time.Time) (preferences.Record, bool, error) {
	if at.IsZero() {
		at = l.now()
	}
	return resolvePreference(l.db.WithContext(ctx), scope, name, at)
}

func resolvePreference(db *gorm.DB, scope preferences.Context, name string, at time.Time) (preferences.Record, bool, error) {
	if scope.Owner == "" || name == "" {
		return preferences.Record{}, false, errors.New("resolving a preference requires an owner and name")
	}
	var rows []preferenceRow
	if err := db.Where("owner = ? AND name = ?", scope.Owner, name).Find(&rows).Error; err != nil {
		return preferences.Record{}, false, err
	}
	records := make([]preferences.Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, row.Data)
	}
	resolved, found := preferences.Resolve(records, scope, name, at)
	return resolved, found, nil
}

func (l *Ledger) RevokePreference(ctx context.Context, owner, id string) (preferences.Record, error) {
	var row preferenceRow
	err := l.transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Clauses(forUpdate).Take(&row, "owner = ? AND id = ?", owner, id).Error; err != nil {
			return err
		}
		if row.Data.RevokedAt != nil {
			return nil
		}
		record, err := preferences.Revoke(row.Data, l.now())
		if err != nil {
			return err
		}
		row.Data = record
		return tx.Model(&row).Select("data").Updates(&row).Error
	})
	if err != nil {
		return preferences.Record{}, err
	}
	return row.Data, nil
}
