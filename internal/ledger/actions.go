package ledger

import (
	"context"
	"errors"
	"slices"

	"github.com/celanwang/stead/internal/delegation"
	"gorm.io/gorm"
)

func (l *Ledger) actionIDs(ctx context.Context, state delegation.ActionState) ([]string, error) {
	var ids []string
	err := l.db.WithContext(ctx).Model(&actionRow{}).Where("state = ?", state).Order("seq").Pluck("id", &ids).Error
	return ids, err
}

func (l *Ledger) Pending(ctx context.Context) ([]string, error) {
	return l.actionIDs(ctx, delegation.Pending)
}

func (l *Ledger) Sending(ctx context.Context) ([]string, error) {
	return l.actionIDs(ctx, delegation.Sending)
}

// All writes to a delegation's actions take the delegation lock first.
func lockAction(tx *gorm.DB, id string) (delegationRow, actionRow, error) {
	var a actionRow
	if err := tx.Select("delegation_id").Take(&a, "id = ?", id).Error; err != nil {
		return delegationRow{}, a, err
	}
	d, err := lockedDelegation(tx, a.DelegationID)
	if err != nil {
		return d, a, err
	}
	err = tx.Clauses(forUpdate).Take(&a, "id = ?", id).Error
	return d, a, err
}

func (l *Ledger) Claim(ctx context.Context, id string) (*delegation.Action, error) {
	var claimed *delegation.Action
	err := l.transaction(ctx, func(tx *gorm.DB) error {
		d, row, err := lockAction(tx, id)
		if err != nil || row.State != delegation.Pending || d.Done {
			return err
		}
		a := row.Data
		at := l.now()
		if !at.Before(d.Data.Deadline) {
			return nil
		}
		var queued bool
		err = tx.Raw("SELECT EXISTS(SELECT 1 FROM events WHERE delegation_id = ? AND processed = FALSE AND blocks_claim = TRUE)", d.ID).
			Scan(&queued).Error
		if err != nil || queued {
			return err
		}
		var g grantRow
		if err := tx.Take(&g, "id = ?", a.GrantID).Error; err != nil {
			return err
		}
		if !g.Data.Allows(d.Data, a, at) {
			return cancelClaim(tx, d, row)
		}

		conflict, err := lockResources(tx, d.Data.Owner, a.Scope.Resources)
		if err != nil || conflict {
			return err
		}
		// Waiting for a resource lock may have crossed either deadline.
		at = l.now()
		if !at.Before(d.Data.Deadline) {
			return nil
		}
		if !g.Data.Allows(d.Data, a, at) {
			return cancelClaim(tx, d, row)
		}
		for _, r := range a.Scope.Resources {
			if err := tx.Exec(`INSERT INTO occupations
				(owner, resource_key, action_id, starts_at, ends_at) VALUES (?, ?, ?, ?, ?)`,
				d.Data.Owner, r.Key, a.ID, r.Start.UTC(), r.End.UTC()).Error; err != nil {
				return err
			}
		}
		a.State = delegation.Sending
		if err := saveAction(tx, row, a); err != nil {
			return err
		}
		claimed = &a
		return saveDelegation(tx, d)
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func cancelClaim(tx *gorm.DB, d delegationRow, row actionRow) error {
	a := row.Data
	a.State = delegation.Canceled
	if err := saveAction(tx, row, a); err != nil {
		return err
	}
	d.Ready = true
	return saveDelegation(tx, d)
}

func lockResources(tx *gorm.DB, owner string, resources []delegation.Resource) (bool, error) {
	keys := make([]string, 0, len(resources))
	for _, r := range resources {
		keys = append(keys, r.Key)
	}
	slices.Sort(keys)
	for _, key := range slices.Compact(keys) {
		// The upsert takes an exclusive lock even when there are no occupations yet.
		err := tx.Exec(`INSERT INTO resource_keys (owner, resource_key) VALUES (?, ?)
			ON DUPLICATE KEY UPDATE resource_key = resource_key`, owner, key).Error
		if err != nil {
			return false, err
		}
	}
	for _, r := range resources {
		var matches []uint64
		// A locking read observes current occupations after acquiring the stable key.
		err := tx.Raw(`SELECT id FROM occupations
			WHERE owner = ? AND resource_key = ? AND released_at IS NULL
			  AND starts_at < ? AND ends_at > ?
			LIMIT 1 FOR UPDATE`, owner, r.Key, r.End.UTC(), r.Start.UTC()).Scan(&matches).Error
		if err != nil || len(matches) != 0 {
			return len(matches) != 0, err
		}
	}
	return false, nil
}

func (l *Ledger) Settle(ctx context.Context, r delegation.Receipt) error {
	return l.settle(ctx, r, false)
}

func (l *Ledger) Reconcile(ctx context.Context, r delegation.Receipt) error {
	return l.settle(ctx, r, true)
}

func (l *Ledger) settle(ctx context.Context, r delegation.Receipt, verification bool) error {
	return l.transaction(ctx, func(tx *gorm.DB) error {
		d, row, err := lockAction(tx, r.ActionID)
		if err != nil {
			return err
		}
		if verification && (r.State == delegation.Unknown || (row.State != delegation.Unknown && row.State != r.State)) {
			return errors.New("verification requires an action with an unknown outcome")
		}
		r.At = l.now()
		a, changed, err := delegation.ApplyReceipt(row.Data, r)
		if err != nil || !changed {
			return err
		}
		if err := saveAction(tx, row, a); err != nil {
			return err
		}
		switch a.State {
		case delegation.Delivered:
			if err := tx.Exec("UPDATE occupations SET confirmed_at = ? WHERE action_id = ? AND released_at IS NULL",
				r.At, a.ID).Error; err != nil {
				return err
			}
		case delegation.Failed:
			if err := tx.Exec("UPDATE occupations SET released_at = ? WHERE action_id = ? AND released_at IS NULL",
				r.At, a.ID).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&auditRow{DelegationID: d.ID, Data: delegation.Record{
			Receipt: &delegation.ReceiptRecord{Before: row.Data, Receipt: r, After: a},
		}}).Error; err != nil {
			return err
		}
		d.Ready = !d.Done
		return saveDelegation(tx, d)
	})
}
