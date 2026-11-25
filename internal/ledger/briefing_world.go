package ledger

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"gorm.io/gorm"
)

type factRow struct {
	ID        string
	Revision  uint64
	Shareable bool
	Value     string
}

func (r factRow) fact() delegation.Fact {
	f := delegation.Fact{
		ID: r.ID, Revision: r.Revision, Shareable: r.Shareable,
		Fingerprint: delegation.Fingerprint(strconv.FormatBool(r.Shareable), r.Value),
	}
	if r.Shareable {
		f.Value = r.Value
	}
	return f
}

type briefingDeliveryRow struct {
	ID               string
	Target           string
	AudienceRevision uint64
	AudienceClass    string
	ArtifactID       string
	Content          string
	Manual           bool
	DeliveredAt      time.Time
}

func (r briefingDeliveryRow) proof() delegation.DeliveryProof {
	return delegation.DeliveryProof{ID: r.ID, Target: r.Target, ArtifactID: r.ArtifactID, At: r.DeliveredAt,
		Audience: delegation.Audience{Target: r.Target, Revision: r.AudienceRevision, Class: r.AudienceClass}}
}

func validateWorldKey(fields ...string) error {
	for _, field := range fields {
		if field == "" || len(field) > 191 {
			return errors.New("world keys must contain 1–191 bytes")
		}
	}
	return nil
}

// SetBriefingFact is the reference provider's input port. It advances a stable ID from an expected revision.
func (l *Ledger) SetBriefingFact(ctx context.Context, owner, project, id, value string, shareable bool, expected uint64) (delegation.Fact, error) {
	if err := validateWorldKey(owner, project, id); err != nil {
		return delegation.Fact{}, err
	}
	if value == "" || len(value) > 1024 {
		return delegation.Fact{}, errors.New("fact value must contain 1–1024 bytes")
	}
	db := l.db.WithContext(ctx)
	var result *gorm.DB
	if expected == 0 {
		result = db.Exec("INSERT INTO briefing_facts (owner, project, id, revision, shareable, value) VALUES (?, ?, ?, 1, ?, ?)",
			owner, project, id, shareable, value)
	} else {
		result = db.Exec(`UPDATE briefing_facts SET revision = revision + 1, shareable = ?, value = ?
			WHERE owner = ? AND project = ? AND id = ? AND revision = ?`, shareable, value, owner, project, id, expected)
	}
	if result.Error != nil {
		return delegation.Fact{}, result.Error
	}
	if result.RowsAffected != 1 {
		return delegation.Fact{}, errors.New("fact revision changed")
	}
	return (factRow{ID: id, Revision: expected + 1, Shareable: shareable, Value: value}).fact(), nil
}

func (l *Ledger) SetBriefingAudience(ctx context.Context, owner, project, target, class string, expected uint64) (delegation.Audience, error) {
	if err := validateWorldKey(owner, project, target, class); err != nil {
		return delegation.Audience{}, err
	}
	db := l.db.WithContext(ctx)
	var result *gorm.DB
	if expected == 0 {
		result = db.Exec("INSERT INTO briefing_audiences (owner, project, target, revision, class) VALUES (?, ?, ?, 1, ?)",
			owner, project, target, class)
	} else {
		result = db.Exec(`UPDATE briefing_audiences SET revision = revision + 1, class = ?
			WHERE owner = ? AND project = ? AND target = ? AND revision = ?`, class, owner, project, target, expected)
	}
	if result.Error != nil {
		return delegation.Audience{}, result.Error
	}
	if result.RowsAffected != 1 {
		return delegation.Audience{}, errors.New("audience revision changed")
	}
	return delegation.Audience{Target: target, Revision: expected + 1, Class: class}, nil
}

func (l *Ledger) ObserveBriefing(ctx context.Context, d delegation.Delegation, target string) (delegation.BriefingObservation, error) {
	db := l.db.WithContext(ctx)
	var out delegation.BriefingObservation
	var ids []string
	for _, i := range d.Briefing.Items {
		ids = append(ids, i.ID)
	}
	var facts []factRow
	if err := db.Raw(`SELECT id, revision, shareable, value FROM briefing_facts
		WHERE owner = ? AND project = ? AND id IN ?`, d.Owner, d.Briefing.Project, ids).Scan(&facts).Error; err != nil {
		return out, err
	}
	for _, fact := range facts {
		out.Facts = append(out.Facts, fact.fact())
	}
	if err := db.Raw(`SELECT target, revision, class FROM briefing_audiences
		WHERE owner = ? AND project = ? AND target = ?`, d.Owner, d.Briefing.Project, target).Scan(&out.Audience).Error; err != nil {
		return out, err
	}
	rows, err := l.briefingDeliveries(ctx, d.ID)
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		out.Deliveries = append(out.Deliveries, row.proof())
	}
	return out, nil
}

func (l *Ledger) briefingDeliveries(ctx context.Context, id string) ([]briefingDeliveryRow, error) {
	var rows []briefingDeliveryRow
	err := l.db.WithContext(ctx).Raw(`SELECT id, target, audience_revision, audience_class, artifact_id, content, manual, delivered_at
		FROM briefing_deliveries WHERE delegation_id = ? ORDER BY delivered_at, id`, id).Scan(&rows).Error
	return rows, err
}

type BriefingDelivery struct {
	Proof   delegation.DeliveryProof
	Content string
	Manual  bool
}

func (l *Ledger) BriefingDeliveries(ctx context.Context, id string) ([]BriefingDelivery, error) {
	rows, err := l.briefingDeliveries(ctx, id)
	if err != nil {
		return nil, err
	}
	var deliveries []BriefingDelivery
	for _, row := range rows {
		deliveries = append(deliveries, BriefingDelivery{Proof: row.proof(), Content: row.Content, Manual: row.Manual})
	}
	return deliveries, nil
}

func briefingEffectKey(g *delegation.DeliveryGuard) string {
	return delegation.Fingerprint(g.DelegationID, g.Audience.Target, g.ArtifactID, strconv.FormatUint(g.Audience.Revision, 10))
}

func (l *Ledger) VerifyBriefingReceipts(ctx context.Context) error {
	var ids []string
	if err := l.db.WithContext(ctx).Raw(`SELECT a.id FROM actions a JOIN briefing_deliveries e ON e.id = a.id
		WHERE a.state = ? AND e.manual = FALSE ORDER BY a.seq`, delegation.Unknown).Scan(&ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err := l.Settle(ctx, delegation.Receipt{ActionID: id, State: delegation.Delivered, Detail: id}); err != nil {
			return err
		}
	}
	return nil
}

// WriteBriefingDelivery represents a separate provider transaction. The conditional write checks
// exact fact and audience versions under locks before producing a result visible outside the harness.
func (l *Ledger) WriteBriefingDelivery(ctx context.Context, id string, scope delegation.Scope, manual bool) (delegation.Receipt, error) {
	guard := scope.Guard
	if guard == nil || guard.ArtifactID != delegation.Fingerprint(scope.Content) || scope.Target != guard.Audience.Target ||
		scope.Content == "" || len(scope.Content) > 60_000 || len(guard.Facts) == 0 {
		return delegation.Receipt{}, errors.New("delivery requires a prepared artifact and matching guard")
	}
	r := delegation.Receipt{ActionID: id}
	err := l.transaction(ctx, func(tx *gorm.DB) error {
		var audience delegation.Audience
		result := tx.Raw(`SELECT target, revision, class FROM briefing_audiences
			WHERE owner = ? AND project = ? AND target = ? FOR UPDATE`, guard.Owner, guard.Project, scope.Target).Scan(&audience)
		if result.Error != nil {
			return result.Error
		}
		effect := briefingEffectKey(guard)
		var existing struct{ ID string }
		if err := tx.Raw("SELECT id FROM briefing_deliveries WHERE effect_key = ?", effect).Scan(&existing).Error; err != nil {
			return err
		}
		if existing.ID != "" {
			r.State, r.Detail = delegation.Delivered, existing.ID
			return nil
		}
		if audience != guard.Audience {
			r.State, r.Detail = delegation.Failed, "audience changed before provider write"
			return nil
		}
		facts := slices.Clone(guard.Facts)
		slices.SortFunc(facts, func(a, b delegation.Fact) int { return strings.Compare(a.ID, b.ID) })
		for _, expected := range facts {
			var current factRow
			err := tx.Raw(`SELECT id, revision, shareable, value FROM briefing_facts
				WHERE owner = ? AND project = ? AND id = ? FOR UPDATE`, guard.Owner, guard.Project, expected.ID).Scan(&current).Error
			if err != nil {
				return err
			}
			checked := current.fact()
			if checked.Revision != expected.Revision || checked.Fingerprint != expected.Fingerprint || checked.Shareable != expected.Shareable {
				r.State, r.Detail = delegation.Failed, fmt.Sprintf("source %s changed before provider write", expected.ID)
				return nil
			}
		}
		at := l.now()
		if !manual && !at.Before(guard.Deadline) {
			r.State, r.Detail = delegation.Failed, "delegation deadline elapsed before provider write"
			return nil
		}
		err := tx.Exec(`INSERT INTO briefing_deliveries
			(id, delegation_id, effect_key, target, audience_revision, audience_class, artifact_id, content, manual, delivered_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, guard.DelegationID, effect, scope.Target, audience.Revision,
			audience.Class, guard.ArtifactID, scope.Content, manual, at).Error
		if err == nil {
			r.State, r.Detail = delegation.Delivered, id
		}
		return err
	})
	return r, err
}
