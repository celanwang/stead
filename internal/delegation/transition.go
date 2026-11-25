package delegation

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

func ValidateNew(d Delegation, at time.Time) error {
	if d.ID == "" || len(d.ID) > 128 || d.Owner == "" || len(d.Owner) > 191 ||
		d.Goal == "" || len(d.Sources) == 0 || !at.Before(d.Deadline) || d.Request.Target == "" ||
		d.Version != 0 || d.Wait != nil || d.Result != nil {
		return errors.New("new delegation requires an ID, owner, sources, goal, request, and future deadline")
	}
	if d.Briefing != nil {
		return d.Briefing.validate()
	}
	if !d.Request.valid() || d.Request.Guard != nil {
		return errors.New("invalid scheduling request")
	}
	for _, source := range d.Sources {
		if source == "" || len(source) > 191 {
			return errors.New("invalid delegation source")
		}
	}
	return nil
}

// ValidateEvent expects source and sender identities authenticated by the ingress adapter.
func ValidateEvent(d Delegation, e Event) error {
	if !slices.Contains(d.Sources, e.Source) || e.ExternalID == "" || len(e.ExternalID) > 191 ||
		e.Sender == "" || e.Raw == "" || e.DelegationID != d.ID {
		return errors.New("invalid event source, identity, or payload")
	}
	switch e.Kind {
	case Input:
		return nil
	case Amend:
		if d.Briefing == nil || e.Sender != d.Owner || e.Correlation != d.ID || e.Amendment == nil ||
			!d.Briefing.validAmendment(*e.Amendment) {
			return errors.New("invalid briefing amendment")
		}
		return nil
	case Review:
		if d.Briefing == nil || e.Sender != d.Briefing.Reviewer || e.Correlation != d.ID || e.Review == nil ||
			e.Review.Fingerprint == "" || !d.Briefing.hasSource(e.Review.Source) {
			return errors.New("review requires the specified reviewer and an active source fingerprint")
		}
		return nil
	case Approval, Revoke, Evidence:
		if e.Sender != d.Owner || e.Correlation == "" || (e.Kind != Approval && e.Correlation != d.ID) {
			return errors.New("event requires the delegation owner and matching correlation")
		}
	default:
		return errors.New("unknown event kind")
	}

	switch e.Kind {
	case Approval:
		if e.Grant == nil || e.Grant.ExpiresAt.IsZero() || !e.Grant.Scope.valid() {
			return errors.New("approval requires a valid scope and expiry")
		}
	case Revoke:
		if e.RevokeGrantID == "" {
			return errors.New("revocation requires a grant ID")
		}
	case Evidence:
		if d.Briefing != nil {
			return errors.New("briefing delivery requires a verified provider record")
		}
	}
	return nil
}

// Resolve first applies kernel rules, then accepts a valid plugin proposal.
func Resolve(s Snapshot, p Proposal, at time.Time) Decision {
	d, e := s.Delegation, s.Event
	if d.Briefing != nil {
		return resolveBriefing(s, p, at)
	}
	if e != nil && e.Kind == Evidence && e.ReceivedAt.Before(d.Deadline) {
		return Decision{Result: &Result{Complete: true, Reason: "completion evidence received", Evidence: e.Raw, At: at}}
	}
	if !at.Before(d.Deadline) {
		return Decision{Result: &Result{Reason: "delegation deadline reached", At: at}}
	}

	outcome := Decision{Wait: d.Wait}
	matched := d.Wait.Matches(e)
	if d.Wait != nil && (matched || !at.Before(d.Wait.Until)) {
		outcome.Wait = nil
	}
	if e != nil && e.Kind == Input && e.Sender == d.Owner && e.MayAffectGrant {
		outcome.InvalidateGrants = true
	}
	// The grant is derived from the owner's event, never from plugin output.
	if matched && e.Kind == Approval && e.Grant != nil && at.Before(e.Grant.ExpiresAt) {
		outcome.Grant = &Grant{
			ID: e.ID, DelegationID: d.ID, Authorizer: e.Sender,
			SourceEventID: e.ID, Original: e.Raw, Terms: *e.Grant,
		}
	}

	if p.Wait != nil && (p.Action != nil || p.Return != "") || p.Action != nil && p.Return != "" {
		outcome.Rejection = "proposal must choose a single next step"
		return outcome
	}
	switch {
	case p.Wait != nil:
		w := p.Wait
		if w.Sender == "" || w.Correlation == "" || !at.Before(w.Until) || w.Until.After(d.Deadline) ||
			(w.Kind != Approval && w.Kind != Evidence && w.Kind != Input) {
			outcome.Rejection = "invalid wait"
			break
		}
		outcome.Wait = w
	case p.Action != nil:
		action := Action{
			ID: fmt.Sprintf("%s-%d", d.ID, d.Version), GrantID: p.Action.GrantID,
			DelegationID: d.ID, Scope: p.Action.Scope, State: Pending,
		}
		if outcome.InvalidateGrants || !authorized(s.Grants, outcome.Grant, d, action, at) {
			outcome.Rejection = "action lacks a current grant for its approved scope"
			break
		}
		if duplicateEffect(s.Actions, action) {
			outcome.Rejection = "a matching action is already pending, unresolved, or delivered"
			break
		}
		outcome.Action = &action
	case p.Return != "":
		outcome.Wait = nil
		outcome.Result = &Result{Reason: p.Return, At: at}
	}
	return outcome
}

func duplicateEffect(actions []Action, candidate Action) bool {
	return hasOpenEffect(actions, candidate.Scope)
}

func hasOpenEffect(actions []Action, scope Scope) bool {
	return slices.ContainsFunc(actions, func(a Action) bool {
		return a.State != Failed && a.State != Canceled && a.Scope.sameEffect(scope)
	})
}

func authorized(grants []Grant, added *Grant, d Delegation, a Action, at time.Time) bool {
	if added != nil && added.Allows(d, a, at) {
		return true
	}
	return slices.ContainsFunc(grants, func(g Grant) bool { return g.Allows(d, a, at) })
}

func ApplyReceipt(a Action, r Receipt) (Action, bool, error) {
	if !r.Valid() || r.ActionID != a.ID {
		return a, false, errors.New("invalid action receipt")
	}
	if a.State == r.State {
		return a, false, nil
	}
	if r.State == Unknown && (a.State == Delivered || a.State == Failed) {
		return a, false, nil
	}
	if a.State != Sending && a.State != Unknown {
		return a, false, fmt.Errorf("action in %s state cannot accept a %s receipt", a.State, r.State)
	}
	a.State, a.Receipt = r.State, &r
	return a, true, nil
}
