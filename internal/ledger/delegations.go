package ledger

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func (l *Ledger) Create(ctx context.Context, d delegation.Delegation) error {
	d.Deadline = d.Deadline.UTC().Truncate(time.Microsecond)
	if err := delegation.ValidateNew(d, l.now()); err != nil {
		return err
	}
	d.Version = 1
	row := delegationRow{ID: d.ID, Version: d.Version, Ready: true, WakeAt: d.Deadline, Data: d}
	return l.db.WithContext(ctx).Create(&row).Error
}

func (l *Ledger) Receive(ctx context.Context, e delegation.Event) (bool, error) {
	var inserted bool
	err := l.transaction(ctx, func(tx *gorm.DB) error {
		d, err := lockedDelegation(tx, e.DelegationID)
		if err != nil {
			return err
		}
		if err := delegation.ValidateEvent(d.Data, e); err != nil {
			return err
		}
		e.ID, e.ReceivedAt = rand.Text(), l.now()
		if e.Grant != nil {
			terms := *e.Grant
			terms.ExpiresAt = terms.ExpiresAt.UTC().Truncate(time.Microsecond)
			e.Grant = &terms
		}
		row := eventRow{
			ID: e.ID, DelegationID: d.ID, Source: e.Source, ExternalID: e.ExternalID,
			Kind: e.Kind, Processed: d.Done, BlocksClaim: e.BlocksClaim(d.Data), Data: e,
		}
		if err := tx.Create(&row).Error; err != nil {
			var duplicate *mysqldriver.MySQLError
			if errors.As(err, &duplicate) && duplicate.Number == 1062 {
				var existing eventRow
				// Only duplicates of the source event are successful retries.
				if err := tx.Take(&existing, "source = ? AND external_id = ?", e.Source, e.ExternalID).Error; err != nil {
					return err
				}
				if existing.DelegationID != d.ID {
					return errors.New("source event already belongs to another delegation")
				}
				return nil
			}
			return err
		}

		if e.Kind == delegation.Revoke {
			if err := revoke(tx, d.ID, e); err != nil {
				return err
			}
		}
		inserted = true
		return saveDelegation(tx, d)
	})
	return inserted && err == nil, err
}

func revoke(tx *gorm.DB, id string, e delegation.Event) error {
	var g grantRow
	if err := tx.Take(&g, "id = ? AND delegation_id = ?", e.RevokeGrantID, id).Error; err != nil {
		return err
	}
	if g.Data.RevokedAt == nil {
		g.Data.RevokedAt, g.Data.RevokedBy = &e.ReceivedAt, e.ID
		if err := tx.Model(&g).Select("data").Updates(&g).Error; err != nil {
			return err
		}
	}
	return cancelPending(tx, id, g.ID)
}

func cancelPending(tx *gorm.DB, id, grant string) error {
	query := tx.Where("delegation_id = ? AND state = ?", id, delegation.Pending)
	if grant != "" {
		query = query.Where("grant_id = ?", grant)
	}
	var rows []actionRow
	if err := query.Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		a := row.Data
		a.State = delegation.Canceled
		if err := saveAction(tx, row, a); err != nil {
			return err
		}
	}
	return nil
}

func snapshot(tx *gorm.DB, row delegationRow, at time.Time) (delegation.Snapshot, bool, error) {
	s := delegation.Snapshot{Delegation: row.Data, At: at}
	if row.Done {
		return s, false, nil
	}
	var e eventRow
	err := tx.Where("delegation_id = ? AND processed = FALSE", row.ID).
		Order("CASE WHEN kind = 'evidence' THEN 0 ELSE 1 END").Order("seq").Take(&e).Error
	switch {
	case err == nil:
		s.Event = &e.Data
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return s, false, err
	case !row.Ready && at.Before(row.WakeAt):
		return s, false, nil
	}
	grants, err := grantRows(tx, row.ID)
	if err != nil {
		return s, false, err
	}
	for _, g := range grants {
		s.Grants = append(s.Grants, g.Data)
	}
	actions, err := actionRows(tx, row.ID)
	if err != nil {
		return s, false, err
	}
	for _, a := range actions {
		s.Actions = append(s.Actions, a.Data)
	}
	return s, true, nil
}

func (l *Ledger) Snapshot(ctx context.Context, id string) (s delegation.Snapshot, ready bool, err error) {
	err = l.transaction(ctx, func(tx *gorm.DB) error {
		row, err := lockedDelegation(tx, id)
		if err != nil {
			return err
		}
		s, ready, err = snapshot(tx, row, l.now())
		return err
	})
	return
}

func (l *Ledger) Advance(ctx context.Context, id string, version uint64, observedAt time.Time, p delegation.Proposal) (out delegation.Decision, err error) {
	err = l.transaction(ctx, func(tx *gorm.DB) error {
		d, err := lockedDelegation(tx, id)
		if err != nil {
			return err
		}
		if d.Version != version {
			return delegation.ErrStale
		}
		at := l.now()
		s, ready, err := snapshot(tx, d, at)
		if err != nil || !ready {
			return err
		}
		s.At = observedAt
		out = delegation.Resolve(s, p, at)
		if err := applyDecision(tx, d, s.Event, out); err != nil {
			return err
		}
		return tx.Create(&auditRow{DelegationID: id, Data: delegation.Record{
			Decision: &delegation.DecisionRecord{Snapshot: s, Proposal: p, At: at, Outcome: out},
		}}).Error
	})
	if err != nil {
		out = delegation.Decision{}
	}
	return
}

func applyDecision(tx *gorm.DB, d delegationRow, e *delegation.Event, out delegation.Decision) error {
	if e != nil {
		if err := tx.Model(&eventRow{}).Where("id = ?", e.ID).Update("processed", true).Error; err != nil {
			return err
		}
	}
	if out.InvalidateGrants {
		cause := fmt.Sprintf("decision:%s/%d", d.ID, d.Version)
		if e != nil {
			cause = e.ID
		}
		grants, err := grantRows(tx, d.ID)
		if err != nil {
			return err
		}
		for _, g := range grants {
			if g.Data.InvalidatedBy == "" {
				g.Data.InvalidatedBy = cause
				if err := tx.Model(&g).Select("data").Updates(&g).Error; err != nil {
					return err
				}
			}
		}
	}
	if out.Result != nil || out.InvalidateGrants {
		if err := cancelPending(tx, d.ID, ""); err != nil {
			return err
		}
	}
	if out.Result != nil {
		if err := tx.Model(&eventRow{}).Where("delegation_id = ? AND processed = FALSE", d.ID).
			Update("processed", true).Error; err != nil {
			return err
		}
	}
	if out.Grant != nil {
		if err := tx.Create(&grantRow{ID: out.Grant.ID, DelegationID: d.ID, Data: *out.Grant}).Error; err != nil {
			return err
		}
	}
	if a := out.Action; a != nil {
		if err := tx.Create(&actionRow{
			ID: a.ID, DelegationID: d.ID, GrantID: a.GrantID, State: a.State, Data: *a,
		}).Error; err != nil {
			return err
		}
	}
	if out.Briefing != nil {
		d.Data.Briefing = out.Briefing
	}
	if out.Prepared != nil {
		d.Data.Request = *out.Prepared
	}
	d.Data.Wait, d.Data.Result = out.Wait, out.Result
	d.Ready = e != nil && out.Rejection != "" && out.Wait == nil
	return saveDelegation(tx, d)
}

func (l *Ledger) Ready(ctx context.Context) ([]string, error) {
	var ids []string
	err := l.db.WithContext(ctx).Raw(`
		SELECT d.id FROM delegations d
		WHERE d.done = FALSE AND (
			d.ready = TRUE OR d.wake_at <= ? OR EXISTS (
				SELECT 1 FROM events e WHERE e.delegation_id = d.id AND e.processed = FALSE
			)
		) ORDER BY d.wake_at, d.id`, l.now()).Scan(&ids).Error
	return ids, err
}
