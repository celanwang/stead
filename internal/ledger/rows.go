package ledger

import (
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type delegationRow struct {
	ID      string
	Version uint64
	Ready   bool
	Done    bool
	WakeAt  time.Time
	Data    delegation.Delegation `gorm:"serializer:json"`
}

func (delegationRow) TableName() string { return "delegations" }

type eventRow struct {
	Seq          uint64 `gorm:"primaryKey"`
	ID           string
	DelegationID string
	Source       string
	ExternalID   string
	Kind         delegation.EventKind
	Processed    bool
	BlocksClaim  bool
	Data         delegation.Event `gorm:"serializer:json"`
}

func (eventRow) TableName() string { return "events" }

type grantRow struct {
	ID           string
	DelegationID string
	Data         delegation.Grant `gorm:"serializer:json"`
}

func (grantRow) TableName() string { return "grants" }

type actionRow struct {
	Seq          uint64 `gorm:"primaryKey"`
	ID           string
	DelegationID string
	GrantID      string
	State        delegation.ActionState
	Data         delegation.Action `gorm:"serializer:json"`
}

func (actionRow) TableName() string { return "actions" }

type auditRow struct {
	Seq          uint64 `gorm:"primaryKey"`
	DelegationID string
	Data         delegation.Record `gorm:"serializer:json"`
}

func (auditRow) TableName() string { return "audit_records" }

var forUpdate = clause.Locking{Strength: "UPDATE"}

func lockedDelegation(tx *gorm.DB, id string) (delegationRow, error) {
	var row delegationRow
	err := tx.Clauses(forUpdate).Take(&row, "id = ?", id).Error
	return row, err
}

func saveDelegation(tx *gorm.DB, row delegationRow) error {
	row.Data.Version++
	row.Version, row.Done = row.Data.Version, row.Data.Result != nil
	row.WakeAt = row.Data.Deadline
	if row.Data.Wait != nil && row.Data.Wait.Until.Before(row.WakeAt) {
		row.WakeAt = row.Data.Wait.Until
	}
	return tx.Model(&row).Select("version", "ready", "done", "wake_at", "data").Updates(&row).Error
}

func saveAction(tx *gorm.DB, row actionRow, a delegation.Action) error {
	row.Data, row.State = a, a.State
	return tx.Model(&row).Select("state", "data").Updates(&row).Error
}

func actionRows(tx *gorm.DB, id string) ([]actionRow, error) {
	var rows []actionRow
	err := tx.Where("delegation_id = ?", id).Order("seq").Find(&rows).Error
	return rows, err
}

func grantRows(tx *gorm.DB, id string) ([]grantRow, error) {
	var rows []grantRow
	err := tx.Where("delegation_id = ?", id).Order("id").Find(&rows).Error
	return rows, err
}
