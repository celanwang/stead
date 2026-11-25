package ledger

import (
	"context"
	"errors"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"gorm.io/gorm"
)

func (l *Ledger) Delegation(ctx context.Context, id string) (delegation.Delegation, error) {
	var row delegationRow
	err := l.db.WithContext(ctx).Take(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = delegation.ErrNotFound
	}
	return row.Data, err
}

func (l *Ledger) ListPage(ctx context.Context, owner, after string, limit int) ([]delegation.Delegation, error) {
	var rows []delegationRow
	err := l.db.WithContext(ctx).Model(&delegationRow{}).
		Where("CAST(JSON_UNQUOTE(JSON_EXTRACT(data, '$.Owner')) AS BINARY) = CAST(? AS BINARY) AND id > ?", owner, after).
		Order("id").Limit(limit).Find(&rows).Error
	items := make([]delegation.Delegation, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.Data)
	}
	return items, err
}

func (l *Ledger) List(ctx context.Context, owner string) ([]delegation.Delegation, error) {
	if owner == "" {
		return nil, errors.New("list requires an owner")
	}
	var rows []delegationRow
	err := l.db.WithContext(ctx).Model(&delegationRow{}).
		Where("CAST(JSON_UNQUOTE(JSON_EXTRACT(data, '$.Owner')) AS BINARY) = CAST(? AS BINARY)", owner).
		Order("done, wake_at, id").Find(&rows).Error
	items := make([]delegation.Delegation, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.Data)
	}
	return items, err
}

type Status struct {
	Delegation  delegation.Delegation
	Events      []EventStatus
	Grants      []delegation.Grant
	Actions     []delegation.Action
	Occupations []Occupation
}

type EventStatus struct {
	Event     delegation.Event
	Processed bool
}

type Occupation struct {
	Owner       string
	ResourceKey string
	ActionID    string
	StartsAt    time.Time
	EndsAt      time.Time
	ConfirmedAt *time.Time
	ReleasedAt  *time.Time
}

func (l *Ledger) Status(ctx context.Context, id string) (status Status, err error) {
	err = l.transaction(ctx, func(tx *gorm.DB) error {
		row, err := lockedDelegation(tx, id)
		if err != nil {
			return err
		}
		status.Delegation = row.Data

		var events []eventRow
		if err := tx.Where("delegation_id = ?", id).Order("seq").Find(&events).Error; err != nil {
			return err
		}
		for _, e := range events {
			status.Events = append(status.Events, EventStatus{Event: e.Data, Processed: e.Processed})
		}

		grants, err := grantRows(tx, id)
		if err != nil {
			return err
		}
		for _, g := range grants {
			status.Grants = append(status.Grants, g.Data)
		}

		actions, err := actionRows(tx, id)
		if err != nil {
			return err
		}
		for _, a := range actions {
			status.Actions = append(status.Actions, a.Data)
		}
		return tx.Raw(`SELECT o.owner, o.resource_key, o.action_id, o.starts_at, o.ends_at,
				o.confirmed_at, o.released_at
			FROM occupations o JOIN actions a ON a.id = o.action_id
			WHERE a.delegation_id = ? ORDER BY o.id`, id).Scan(&status.Occupations).Error
	})
	return
}

func (l *Ledger) History(ctx context.Context, id string) ([]delegation.Record, error) {
	var records []delegation.Record
	err := l.transaction(ctx, func(tx *gorm.DB) error {
		if _, err := lockedDelegation(tx, id); err != nil {
			return err
		}
		var rows []auditRow
		if err := tx.Where("delegation_id = ?", id).Order("seq").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			records = append(records, row.Data)
		}
		return nil
	})
	return records, err
}
