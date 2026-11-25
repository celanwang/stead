// Package preferences resolves explicit user choices at their recorded scope.
package preferences

import (
	"errors"
	"strings"
	"time"
)

type Scope string

const (
	OwnerScope   Scope = "owner"
	ProjectScope Scope = "project"
	TaskScope    Scope = "task"
)

type Record struct {
	ID          string
	Owner       string
	Project     string
	Task        string
	Name        string
	Value       string
	Original    string
	Source      string
	Scope       Scope
	EffectiveAt time.Time
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
}

type Context struct {
	Owner   string
	Project string
	Task    string
}

func (r Record) Validate() error {
	if r.ID == "" || r.Owner == "" || r.Name == "" || r.Value == "" || strings.TrimSpace(r.Original) == "" ||
		r.Source == "" || r.EffectiveAt.IsZero() {
		return errors.New("preference requires an ID, owner, name, value, original wording, source, and start time")
	}
	if r.ExpiresAt != nil && !r.EffectiveAt.Before(*r.ExpiresAt) {
		return errors.New("preference expiry must be after its start")
	}
	switch r.Scope {
	case OwnerScope:
		if r.Project == "" && r.Task == "" {
			return nil
		}
	case ProjectScope:
		if r.Project != "" && r.Task == "" {
			return nil
		}
	case TaskScope:
		if r.Task != "" {
			return nil
		}
	}
	return errors.New("preference scope does not match its project and task")
}

func Revoke(record Record, at time.Time) (Record, error) {
	if err := record.Validate(); err != nil {
		return record, err
	}
	if at.IsZero() {
		return record, errors.New("preference revocation requires a time")
	}
	if record.RevokedAt == nil {
		at = at.UTC()
		record.RevokedAt = &at
	}
	return record, nil
}

// Resolve returns the most specific active record and its provenance. At the
// same scope temporary overrides precede standing preferences. Within each
// group, a more recent start takes precedence and ID breaks exact time ties.
func Resolve(records []Record, ctx Context, name string, at time.Time) (Record, bool) {
	var selected Record
	bestScope := 0
	for _, r := range records {
		if r.Owner != ctx.Owner || r.Name != name || r.Validate() != nil || at.Before(r.EffectiveAt) ||
			(r.ExpiresAt != nil && !at.Before(*r.ExpiresAt)) ||
			(r.RevokedAt != nil && !at.Before(*r.RevokedAt)) {
			continue
		}

		scope := 1
		switch r.Scope {
		case TaskScope:
			if r.Task != ctx.Task || r.Project != ctx.Project {
				continue
			}
			scope = 3
		case ProjectScope:
			if r.Project != ctx.Project {
				continue
			}
			scope = 2
		}
		if scope > bestScope || (scope == bestScope && precedes(r, selected)) {
			selected, bestScope = r, scope
		}
	}
	return selected, bestScope > 0
}

func precedes(a, b Record) bool {
	if (a.ExpiresAt == nil) != (b.ExpiresAt == nil) {
		return a.ExpiresAt != nil
	}
	return a.EffectiveAt.After(b.EffectiveAt) ||
		(a.EffectiveAt.Equal(b.EffectiveAt) && a.ID > b.ID)
}
