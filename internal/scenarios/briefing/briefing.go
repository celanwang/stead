package briefing

import (
	"context"
	"errors"
	"time"

	"github.com/celanwang/stead/internal/delegation"
)

type World interface {
	ObserveBriefing(context.Context, delegation.Delegation, string) (delegation.BriefingObservation, error)
	WriteBriefingDelivery(context.Context, string, delegation.Scope, bool) (delegation.Receipt, error)
}

type Proposer struct {
	World World
	Poll  time.Duration
	Other delegation.Proposer
}

func (p Proposer) Propose(ctx context.Context, s delegation.Snapshot) (delegation.Proposal, error) {
	if s.Delegation.Briefing == nil {
		return p.Other.Propose(ctx, s)
	}
	target := s.Delegation.Request.Target
	if e := s.Event; e != nil && e.Kind == delegation.Amend && e.Amendment.Target != "" {
		target = e.Amendment.Target
	}
	observation, err := p.World.ObserveBriefing(ctx, s.Delegation, target)
	if err != nil {
		return delegation.Proposal{}, err
	}
	observation.Poll = p.Poll
	return delegation.Proposal{Briefing: &observation}, nil
}

type Executor struct {
	World World
	Other delegation.Executor
}

func (e Executor) Execute(ctx context.Context, action delegation.Action) (delegation.Receipt, error) {
	if action.State != delegation.Sending {
		return delegation.Receipt{}, errors.New("delivery requires a claimed action")
	}
	if action.Scope.Guard == nil {
		return e.Other.Execute(ctx, action)
	}
	return e.World.WriteBriefingDelivery(ctx, action.ID, action.Scope, false)
}
