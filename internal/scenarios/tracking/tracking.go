// Package tracking follows versioned source objects and classifies their updates.
package tracking

import (
	"errors"
	"slices"
	"strings"
)

type Key struct {
	Owner    string
	Project  string
	Source   string
	ObjectID string
}

func (k Key) Validate() error {
	if !validID(k.Owner) || !validID(k.Project) || !validID(k.Source) || !validID(k.ObjectID) {
		return errors.New("tracking requires an owner, project, source, and stable object ID")
	}
	return nil
}

func validID(id string) bool {
	return id != "" && len(id) <= 191 && id == strings.TrimSpace(id)
}

type Mode string

const (
	Active    Mode = "active"
	Muted     Mode = "muted"
	Stopped   Mode = "stopped"
	TakenOver Mode = "taken_over"
)

type Observation struct {
	Key           Key
	EventID       string
	Revision      uint64
	Title         string
	Summary       string
	Material      bool
	Withdrawn     bool
	ConclusionIDs []string
}

type Item struct {
	Key    Key
	Mode   Mode
	Latest *Observation
}

type Disposition string

const (
	Immediate Disposition = "immediate"
	Digest    Disposition = "digest"
	Silent    Disposition = "silent"
)

type Facts struct {
	Initial             bool
	Material            bool
	Withdrawn           bool
	AffectedConclusions []string
}

type Classifier func(Facts) Disposition

func DefaultDisposition(f Facts) Disposition {
	if f.Initial || f.Withdrawn || len(f.AffectedConclusions) > 0 {
		return Immediate
	}
	return Digest
}

type Outcome string

const (
	Applied   Outcome = "applied"
	Duplicate Outcome = "duplicate"
	Stale     Outcome = "stale"
	Inactive  Outcome = "inactive"
)

type Decision struct {
	Key                 Key
	EventID             string
	Revision            uint64
	Outcome             Outcome
	Disposition         Disposition
	Mode                Mode
	AffectedConclusions []string
}

// Apply admits newer revisions of a stable object. For a source-wide event
// ledger, callers can also deduplicate (owner, project, source, event ID) in
// the same transaction as persisting the returned item.
func Apply(current Item, event Observation, classify Classifier) (Item, Decision, error) {
	if err := event.Key.Validate(); err != nil {
		return current, Decision{}, err
	}
	if !validID(event.EventID) || event.Revision == 0 {
		return current, Decision{}, errors.New("tracking observation requires a stable event ID and a positive revision")
	}
	if current == (Item{}) {
		current = Item{Key: event.Key, Mode: Active}
	}
	if current.Key != event.Key {
		return current, Decision{}, errors.New("tracking observation belongs to a different object")
	}
	if current.Mode == "" {
		current.Mode = Active
	}

	decision := Decision{
		Key: event.Key, EventID: event.EventID, Revision: event.Revision, Mode: current.Mode, Disposition: Silent,
	}
	switch current.Mode {
	case Active, Muted:
	case Stopped, TakenOver:
		decision.Outcome = Inactive
		return current, decision, nil
	default:
		return current, Decision{}, errors.New("invalid stored tracking mode")
	}

	if latest := current.Latest; latest != nil {
		switch {
		case event.EventID == latest.EventID || event.Revision == latest.Revision:
			decision.Outcome = Duplicate
			return current, decision, nil
		case event.Revision < latest.Revision:
			decision.Outcome = Stale
			return current, decision, nil
		}
	}

	event.ConclusionIDs = slices.Clone(event.ConclusionIDs)
	for _, id := range event.ConclusionIDs {
		if !validID(id) {
			return current, Decision{}, errors.New("invalid affected conclusion ID")
		}
	}
	slices.Sort(event.ConclusionIDs)
	event.ConclusionIDs = slices.Compact(event.ConclusionIDs)
	if current.Mode == Active {
		if classify == nil {
			classify = DefaultDisposition
		}
		decision.Disposition = classify(Facts{
			Initial: current.Latest == nil, Material: event.Material, Withdrawn: event.Withdrawn,
			AffectedConclusions: slices.Clone(event.ConclusionIDs),
		})
		switch decision.Disposition {
		case Immediate, Digest, Silent:
		default:
			return current, Decision{}, errors.New("invalid tracking disposition")
		}
	}
	decision.Outcome = Applied
	decision.AffectedConclusions = slices.Clone(event.ConclusionIDs)
	current.Latest = &event
	return current, decision, nil
}

// SetMode can silence and resume an existing watch. A stopped or handed-off
// watch stays terminal so a late result cannot reactivate it.
func SetMode(current Item, mode Mode) (Item, error) {
	if err := current.Key.Validate(); err != nil {
		return current, err
	}
	switch mode {
	case Active, Muted, Stopped, TakenOver:
	default:
		return current, errors.New("invalid tracking mode")
	}
	switch current.Mode {
	case "", Active, Muted:
	case Stopped, TakenOver:
		if mode != current.Mode {
			return current, errors.New("tracking has already ended for this object")
		}
	default:
		return current, errors.New("invalid stored tracking mode")
	}
	current.Mode = mode
	return current, nil
}
