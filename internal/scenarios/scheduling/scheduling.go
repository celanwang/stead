package scheduling

import (
	"context"
	"fmt"

	"github.com/celanwang/stead/internal/delegation"
)

// Proposer handles a single scheduling request backed by explicit owner approval.
type Proposer struct{}

func (Proposer) Propose(_ context.Context, s delegation.Snapshot) (delegation.Proposal, error) {
	d := s.Delegation
	for _, a := range s.Actions {
		switch a.State {
		case delegation.Sending, delegation.Unknown, delegation.Delivered:
			return waitFor(d, delegation.Evidence, d.ID), nil
		case delegation.Pending:
			e := s.Event
			if e != nil && e.Kind == delegation.Input && e.Sender == d.Owner && e.MayAffectGrant {
				return approvalWait(d), nil
			}
			return delegation.Proposal{}, nil
		}
	}

	e := s.Event
	if d.Wait.Matches(e) && e.Kind == delegation.Approval && s.At.Before(e.Grant.ExpiresAt) {
		return delegation.Proposal{Action: &delegation.ActionRequest{GrantID: e.ID, Scope: d.Request}}, nil
	}
	if d.Wait != nil && s.At.Before(d.Wait.Until) && !d.Wait.Matches(e) {
		if e == nil || e.Kind != delegation.Input || e.Sender != d.Owner || !e.MayAffectGrant {
			return delegation.Proposal{}, nil
		}
	}
	return approvalWait(d), nil
}

func approvalWait(d delegation.Delegation) delegation.Proposal {
	return waitFor(d, delegation.Approval, fmt.Sprintf("%s/%d", d.ID, d.Version))
}

func waitFor(d delegation.Delegation, kind delegation.EventKind, correlation string) delegation.Proposal {
	return delegation.Proposal{Wait: &delegation.Wait{
		Kind: kind, Sender: d.Owner, Correlation: correlation, Until: d.Deadline,
	}}
}
