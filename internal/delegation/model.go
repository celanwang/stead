package delegation

import (
	"context"
	"errors"
	"slices"
	"time"
)

var (
	ErrStale    = errors.New("delegation version changed")
	ErrNotFound = errors.New("ledger record not found")
)

type Clock interface {
	Now() time.Time
}

type WallClock struct{}

func (WallClock) Now() time.Time { return time.Now().UTC() }

type Delegation struct {
	ID       string
	Owner    string
	Sources  []string
	Goal     string
	Request  Scope
	Version  uint64
	Deadline time.Time
	Wait     *Wait
	Result   *Result
	Briefing *Briefing
}

type Wait struct {
	Kind        EventKind
	Sender      string
	Correlation string
	Until       time.Time
	MatchUntil  time.Time `json:",omitzero"`
}

func (w *Wait) Matches(e *Event) bool {
	if w == nil || e == nil || w.Kind != e.Kind || w.Sender != e.Sender || w.Correlation != e.Correlation {
		return false
	}
	until := w.Until
	if !w.MatchUntil.IsZero() {
		until = w.MatchUntil
	}
	return e.ReceivedAt.Before(until)
}

type Result struct {
	Complete   bool
	Reason     string
	Evidence   string
	Draft      string `json:",omitempty"`
	ArtifactID string `json:",omitempty"`
	At         time.Time
}

type EventKind string

const (
	Input    EventKind = "input"
	Approval EventKind = "approval"
	Revoke   EventKind = "revoke"
	Evidence EventKind = "evidence"
)

type Event struct {
	ID             string
	Source         string
	ExternalID     string
	DelegationID   string
	Sender         string
	Correlation    string
	Kind           EventKind
	Raw            string
	ReceivedAt     time.Time
	Grant          *GrantTerms
	RevokeGrantID  string
	MayAffectGrant bool
	Review         *ReviewNote `json:",omitempty"`
	Amendment      *Amendment  `json:",omitempty"`
}

func (e Event) BlocksClaim(d Delegation) bool {
	return e.Kind == Evidence || e.Kind == Approval || e.Kind == Amend || e.Kind == Review ||
		(e.Kind == Input && e.Sender == d.Owner && e.MayAffectGrant)
}

type GrantTerms struct {
	Scope     Scope
	ExpiresAt time.Time
}

type Grant struct {
	ID            string
	DelegationID  string
	Authorizer    string
	SourceEventID string
	Original      string
	Terms         GrantTerms
	RevokedAt     *time.Time
	RevokedBy     string
	InvalidatedBy string
}

type Scope struct {
	Target    string
	Content   string
	Resources []Resource
	Guard     *DeliveryGuard `json:",omitempty"`
}

type Resource struct {
	Key   string
	Start time.Time
	End   time.Time
}

func (s Scope) valid() bool {
	if s.Target == "" || s.Content == "" {
		return false
	}
	for _, r := range s.Resources {
		if r.Key == "" || len(r.Key) > 191 || !r.Start.Equal(r.Start.Truncate(time.Microsecond)) ||
			!r.End.Equal(r.End.Truncate(time.Microsecond)) || !r.Start.Before(r.End) {
			return false
		}
	}
	return true
}

func (s Scope) equal(other Scope) bool {
	return s.sameEffect(other) && s.Guard.equal(other.Guard)
}

func (s Scope) sameEffect(other Scope) bool {
	return s.Target == other.Target && s.Content == other.Content &&
		slices.EqualFunc(s.Resources, other.Resources, func(a, b Resource) bool {
			return a.Key == b.Key && a.Start.Equal(b.Start) && a.End.Equal(b.End)
		})
}

type ActionState string

const (
	Pending   ActionState = "pending"
	Sending   ActionState = "sending"
	Unknown   ActionState = "unknown"
	Delivered ActionState = "delivered"
	Failed    ActionState = "failed"
	Canceled  ActionState = "canceled"
)

type Action struct {
	ID           string
	DelegationID string
	GrantID      string
	Scope        Scope
	State        ActionState
	Receipt      *Receipt
}

type Receipt struct {
	ActionID string
	State    ActionState
	Detail   string
	At       time.Time
}

func (r Receipt) Valid() bool {
	return r.ActionID != "" && r.Detail != "" && (r.State == Delivered || r.State == Failed || r.State == Unknown)
}

func (g Grant) Allows(d Delegation, a Action, at time.Time) bool {
	return g.ID == a.GrantID && g.Authorizer == d.Owner && g.DelegationID == d.ID &&
		a.DelegationID == d.ID && g.RevokedAt == nil && g.InvalidatedBy == "" &&
		at.Before(g.Terms.ExpiresAt) && g.Terms.Scope.equal(a.Scope)
}

type Snapshot struct {
	Delegation Delegation
	Event      *Event
	Grants     []Grant
	Actions    []Action
	At         time.Time
}

type Proposal struct {
	Wait     *Wait
	Action   *ActionRequest
	Return   string
	Briefing *BriefingObservation `json:",omitempty"`
}

type ActionRequest struct {
	GrantID string
	Scope   Scope
}

// Proposer receives a detached view and suggests one next step.
type Proposer interface {
	Propose(context.Context, Snapshot) (Proposal, error)
}

// Executor runs only actions that have committed a claim.
type Executor interface {
	Execute(context.Context, Action) (Receipt, error)
}

type Decision struct {
	Wait             *Wait
	Grant            *Grant
	Action           *Action
	Result           *Result
	InvalidateGrants bool
	Rejection        string
	Briefing         *Briefing `json:",omitempty"`
	Prepared         *Scope    `json:",omitempty"`
}

type DecisionRecord struct {
	Snapshot Snapshot
	Proposal Proposal
	At       time.Time
	Outcome  Decision
}

type ReceiptRecord struct {
	Before  Action
	Receipt Receipt
	After   Action
}

type Record struct {
	Decision *DecisionRecord
	Receipt  *ReceiptRecord
}
