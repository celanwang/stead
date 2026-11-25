package delegation

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Store owns the transaction boundaries. Calls into plugins happen between store calls.
type Store interface {
	Receive(context.Context, Event) (bool, error)
	Snapshot(context.Context, string) (Snapshot, bool, error)
	Advance(context.Context, string, uint64, time.Time, Proposal) (Decision, error)
	Claim(context.Context, string) (*Action, error)
	Settle(context.Context, Receipt) error
	Ready(context.Context) ([]string, error)
	Pending(context.Context) ([]string, error)
	Sending(context.Context) ([]string, error)
}

type Runtime struct {
	Store    Store
	Proposer Proposer
	Executor Executor
	Verify   func(context.Context) error
	Probe    func(context.Context) error
}

func (r Runtime) Receive(ctx context.Context, e Event) (bool, error) {
	return r.Store.Receive(ctx, e)
}

func (r Runtime) Advance(ctx context.Context, id string) (Decision, error) {
	s, ready, err := r.Store.Snapshot(ctx, id)
	if err != nil || !ready {
		return Decision{}, err
	}
	version := s.Delegation.Version
	var proposal Proposal
	if s.Delegation.Briefing != nil || Resolve(s, Proposal{}, s.At).Result == nil {
		proposal, err = r.Proposer.Propose(ctx, s)
		if err != nil && (s.Delegation.Briefing == nil || s.At.Before(s.Delegation.Deadline)) {
			return Decision{}, fmt.Errorf("propose for %s: %w", id, err)
		}
	}
	return r.Store.Advance(ctx, id, version, s.At, proposal)
}

func (r Runtime) Dispatch(ctx context.Context, id string) error {
	action, err := r.Store.Claim(ctx, id)
	if err != nil || action == nil {
		return err
	}
	receipt, err := r.Executor.Execute(ctx, *action)
	switch {
	case err != nil:
		receipt = Receipt{ActionID: id, State: Unknown, Detail: err.Error()}
	case receipt.ActionID != id || !receipt.Valid():
		receipt = Receipt{ActionID: id, State: Unknown, Detail: "executor returned an invalid receipt"}
	}
	return r.Store.Settle(ctx, receipt)
}

func (r Runtime) Recover(ctx context.Context) error {
	ids, err := r.Store.Sending(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		err = r.Store.Settle(ctx, Receipt{
			ActionID: id, State: Unknown, Detail: "worker interrupted before settlement",
		})
		if err != nil {
			return fmt.Errorf("recover %s: %w", id, err)
		}
	}
	return nil
}

func (r Runtime) Step(ctx context.Context) error {
	ready, err := r.Store.Ready(ctx)
	if err != nil {
		return err
	}
	var failures []error
	if r.Verify != nil {
		if err := r.Verify(ctx); err != nil {
			failures = append(failures, fmt.Errorf("verify previous deliveries: %w", err))
		}
	}
	for _, id := range ready {
		if _, err := r.Advance(ctx, id); err != nil && !errors.Is(err, ErrStale) {
			failures = append(failures, err)
		}
	}
	pending, err := r.Store.Pending(ctx)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	for _, id := range pending {
		if err := r.Dispatch(ctx, id); err != nil {
			failures = append(failures, fmt.Errorf("dispatch %s: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

func (r Runtime) Run(ctx context.Context, interval time.Duration, report func(error)) error {
	if interval <= 0 {
		return errors.New("poll interval must be positive")
	}
	if err := r.Recover(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if r.Probe != nil {
			if err := r.Probe(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("worker session lost: %w", err)
			}
		}
		if err := r.Step(ctx); err != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	return nil
}
