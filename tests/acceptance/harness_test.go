// Package acceptance exercises the harness against MySQL InnoDB.
package acceptance

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
	"github.com/celanwang/stead/internal/scenarios/scheduling"
	"github.com/celanwang/stead/internal/simulation"
)

const testDSN = "STEAD_TEST_MYSQL_DSN"

var start = time.Date(2030, 1, 2, 10, 0, 0, 0, time.UTC)

type fixture struct {
	*testing.T
	ctx      context.Context
	dsn      string
	prefix   string
	dir      string
	clock    *simulation.Clock
	executor *simulation.Executor
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dsn := os.Getenv(testDSN)
	if dsn == "" {
		t.Skipf("set %s to a dedicated MySQL database", testDSN)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	executor, err := simulation.NewExecutor(dir)
	must(t, err)
	return fixture{
		T: t, ctx: ctx, dsn: dsn, prefix: rand.Text(), dir: dir,
		clock: simulation.NewClock(start), executor: executor,
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func check(t *testing.T, ok bool, message string, details ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(message, details...)
	}
}

func (f fixture) connect() *ledger.Ledger {
	f.Helper()
	l, err := ledger.Open(f.ctx, f.dsn, f.clock)
	must(f.T, err)
	f.Cleanup(func() { l.Close() })
	must(f.T, l.Migrate(f.ctx))
	return l
}

func (f fixture) runtime(l *ledger.Ledger) delegation.Runtime {
	return delegation.Runtime{Store: l, Proposer: scheduling.Proposer{}, Executor: f.executor}
}

func (f fixture) create(l *ledger.Ledger, suffix string) delegation.Delegation {
	f.Helper()
	d := delegation.Delegation{
		ID: f.prefix + "-" + suffix, Owner: f.prefix + "-owner", Sources: []string{"acceptance"},
		Goal: "Schedule a review", Deadline: f.clock.Now().Add(time.Hour),
		Request: delegation.Scope{
			Target: "calendar", Content: "Confirm the review",
			Resources: []delegation.Resource{{Key: "review", Start: start.Add(2 * time.Hour), End: start.Add(3 * time.Hour)}},
		},
	}
	must(f.T, l.Create(f.ctx, d))
	return d
}

func event(d delegation.Delegation, kind delegation.EventKind, suffix string) delegation.Event {
	return delegation.Event{
		Source: "acceptance", ExternalID: d.ID + "-" + suffix, DelegationID: d.ID,
		Sender: d.Owner, Correlation: d.ID, Kind: kind, Raw: "original " + suffix,
	}
}

func (f fixture) status(l *ledger.Ledger, id string) ledger.Status {
	f.Helper()
	s, err := l.Status(f.ctx, id)
	must(f.T, err)
	return s
}

func (f fixture) approval(l *ledger.Ledger, d delegation.Delegation) delegation.Event {
	f.Helper()
	decision, err := f.runtime(l).Advance(f.ctx, d.ID)
	must(f.T, err)
	check(f.T, decision.Wait != nil && decision.Wait.Kind == delegation.Approval, "approval wait missing: %+v", decision)
	e := event(d, delegation.Approval, "approval")
	e.Correlation = decision.Wait.Correlation
	e.Grant = &delegation.GrantTerms{Scope: d.Request, ExpiresAt: f.clock.Now().Add(30 * time.Minute)}
	return e
}

func (f fixture) receive(l *ledger.Ledger, e delegation.Event) {
	f.Helper()
	inserted, err := f.runtime(l).Receive(f.ctx, e)
	must(f.T, err)
	check(f.T, inserted, "event %s was not inserted", e.ExternalID)
}

func (f fixture) stage(l *ledger.Ledger, d delegation.Delegation) delegation.Action {
	f.Helper()
	f.receive(l, f.approval(l, d))
	out, err := f.runtime(l).Advance(f.ctx, d.ID)
	must(f.T, err)
	check(f.T, out.Action != nil && out.Action.State == delegation.Pending, "missing pending action: %+v", out)
	return *out.Action
}

func (f fixture) calls(want int) []delegation.Action {
	f.Helper()
	deliveries, err := f.executor.Deliveries()
	must(f.T, err)
	check(f.T, len(deliveries) == want, "got %d deliveries, want %d", len(deliveries), want)
	return deliveries
}

func TestRestartAndDuplicateEvent(t *testing.T) {
	f := newFixture(t)
	l := f.connect()
	d := f.create(l, "restarted")
	approval := f.approval(l, d)
	before := f.status(l, d.ID)
	f.receive(l, approval)
	// Stop the worker/client after the intake commit and before any decision.
	must(t, l.Close())
	l = f.connect()
	rt := f.runtime(l)
	must(t, rt.Recover(f.ctx))
	inserted, err := rt.Receive(f.ctx, approval)
	must(t, err)
	check(t, !inserted, "resend inserted a duplicate")
	resumed := f.status(l, d.ID)
	check(t, reflect.DeepEqual(resumed.Delegation.Wait, before.Delegation.Wait), "original wait changed after reconnect")
	check(t, len(resumed.Events) == 1 && !resumed.Events[0].Processed, "unprocessed event not restored: %+v", resumed.Events)
	out, err := rt.Advance(f.ctx, d.ID)
	must(t, err)
	check(t, out.Action != nil, "no action approved after restart: %+v", out)
	must(t, rt.Dispatch(f.ctx, out.Action.ID))
	inserted, err = rt.Receive(f.ctx, approval)
	must(t, err)
	check(t, !inserted, "resend inserted a duplicate after settlement")
	must(t, rt.Dispatch(f.ctx, out.Action.ID))
	s := f.status(l, d.ID)
	check(t, len(s.Events) == 1 && s.Events[0].Processed && len(s.Actions) == 1, "ledger items: %+v", s)
	check(t, len(s.Grants) == 1 && s.Grants[0].Original == approval.Raw &&
		s.Grants[0].Terms.ExpiresAt.Equal(approval.Grant.ExpiresAt) &&
		s.Grants[0].SourceEventID == s.Events[0].Event.ID, "grant evidence: %+v", s.Grants)
	check(t, s.Delegation.Deadline.Equal(d.Deadline), "deadline changed: %s", s.Delegation.Deadline)
	check(t, f.calls(1)[0].ID == s.Actions[0].ID, "simulator delivery differs from ledger")
	t.Logf("events=%d actions=%d deliveries=1 deadline=%s", len(s.Events), len(s.Actions), s.Delegation.Deadline)
}

type proposerFunc func(context.Context, delegation.Snapshot) (delegation.Proposal, error)

func (p proposerFunc) Propose(ctx context.Context, s delegation.Snapshot) (delegation.Proposal, error) {
	return p(ctx, s)
}

func TestSuspendedJudgmentAndRevocation(t *testing.T) {
	f := newFixture(t)
	l, other := f.connect(), f.connect()
	d := f.create(l, "revoked")
	action := f.stage(l, d)

	wake := event(d, delegation.Input, "wake")
	wake.Sender = "observer"
	f.receive(l, wake)
	entered, release := make(chan uint64, 1), make(chan struct{})
	rt := f.runtime(l)
	rt.Proposer = proposerFunc(func(ctx context.Context, s delegation.Snapshot) (delegation.Proposal, error) {
		entered <- s.Delegation.Version
		select {
		case <-release:
			return delegation.Proposal{Action: &delegation.ActionRequest{GrantID: action.GrantID, Scope: action.Scope}}, nil
		case <-ctx.Done():
			return delegation.Proposal{}, ctx.Err()
		}
	})
	result := make(chan error, 1)
	go func() {
		_, err := rt.Advance(f.ctx, d.ID)
		result <- err
	}()
	var oldVersion uint64
	select {
	case oldVersion = <-entered:
	case err := <-result:
		t.Fatalf("judgment did not suspend: %v", err)
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	input := event(d, delegation.Input, "hold")
	input.MayAffectGrant = true
	f.receive(other, input)
	held := f.status(other, d.ID)
	check(t, !held.Events[len(held.Events)-1].Processed && held.Actions[0].State == delegation.Pending,
		"expected a pending action and unresolved owner input")
	claimed, err := other.Claim(f.ctx, action.ID)
	must(t, err)
	check(t, claimed == nil, "claim passed pending owner input")
	f.calls(0)

	revoke := event(d, delegation.Revoke, "revocation")
	revoke.RevokeGrantID = action.GrantID
	f.receive(other, revoke)
	s := f.status(other, d.ID)
	check(t, s.Actions[0].State == delegation.Canceled && s.Grants[0].RevokedAt != nil &&
		s.Grants[0].RevokedBy == s.Events[len(s.Events)-1].Event.ID, "revoke did not commit atomically: %+v", s)
	close(release)
	select {
	case err := <-result:
		check(t, errors.Is(err, delegation.ErrStale), "old proposal returned %v", err)
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}

	must(t, l.Close())
	l = f.connect()
	rt = f.runtime(l)
	must(t, rt.Recover(f.ctx))
	for range 3 {
		_, err := rt.Advance(f.ctx, d.ID)
		must(t, err)
	}
	must(t, rt.Dispatch(f.ctx, action.ID))
	s = f.status(l, d.ID)
	check(t, len(s.Actions) == 1 && s.Actions[0].State == delegation.Canceled, "old proposal changed the action ledger")
	f.calls(0)
	t.Logf("pending input blocked claim, version %d became stale, state=%s deliveries=0", oldVersion, s.Actions[0].State)
}

func TestConcurrentOverlappingClaims(t *testing.T) {
	f := newFixture(t)
	first, second := f.connect(), f.connect()
	a := f.create(first, "first")
	b := f.create(second, "second")
	firstAction, secondAction := f.stage(first, a), f.stage(second, b)
	type claim struct {
		action *delegation.Action
		err    error
	}
	barrier, results := make(chan struct{}), make(chan claim, 2)
	go func() {
		<-barrier
		act, err := first.Claim(f.ctx, firstAction.ID)
		results <- claim{act, err}
	}()
	go func() {
		<-barrier
		act, err := second.Claim(f.ctx, secondAction.ID)
		results <- claim{act, err}
	}()
	close(barrier)
	var winner *delegation.Action
	var granted int
	for range 2 {
		result := <-results
		must(t, result.err)
		if result.action != nil {
			winner = result.action
			granted++
		}
	}
	check(t, granted == 1, "got %d successful claims", granted)
	s := f.status(first, winner.DelegationID)
	check(t, len(s.Occupations) == 1 && s.Occupations[0].ReleasedAt == nil && s.Occupations[0].ConfirmedAt == nil &&
		s.Occupations[0].ActionID == winner.ID, "no winner occupation: %+v", s.Occupations)
	receipt, err := f.executor.Execute(f.ctx, *winner)
	must(t, err)
	must(t, first.Settle(f.ctx, receipt))
	check(t, f.status(first, winner.DelegationID).Occupations[0].ConfirmedAt != nil, "delivery did not confirm occupation")
	loser := firstAction
	if winner.ID == firstAction.ID {
		loser = secondAction
	}
	must(t, f.runtime(second).Dispatch(f.ctx, loser.ID))
	check(t, f.calls(1)[0].ID == winner.ID, "delivery does not match winning claim")
	check(t, len(f.status(second, loser.DelegationID).Occupations) == 0, "loser acquired an occupation")
	t.Logf("independent connections=2 claims=%d active occupations=%d deliveries=1", granted, len(s.Occupations))
}

func TestProcessExitBetweenDeliveryAndSettlement(t *testing.T) {
	f := newFixture(t)
	l := f.connect()
	d := f.create(l, "crash")
	action := f.stage(l, d)

	child := exec.CommandContext(f.ctx, os.Args[0], "-test.run=^TestDeliveryExitHelper$")
	child.Env = append(os.Environ(),
		"STEAD_TEST_CRASH_ACTION="+action.ID,
		"STEAD_TEST_CRASH_DIR="+f.dir,
	)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	check(t, errors.As(err, &exit) && exit.ExitCode() == 23, "worker exit=%v output=%s", err, output)
	check(t, f.calls(1)[0].ID == action.ID, "independent delivery record missing")
	check(t, f.status(l, d.ID).Actions[0].State == delegation.Sending, "expected unsettled sending action")

	must(t, l.Close())
	l = f.connect()
	rt := f.runtime(l)
	must(t, l.WithWorker(f.ctx, func() error { return rt.Recover(f.ctx) }))
	s := f.status(l, d.ID)
	check(t, s.Actions[0].State == delegation.Unknown && len(s.Occupations) == 1 &&
		s.Occupations[0].ReleasedAt == nil && s.Occupations[0].ConfirmedAt == nil,
		"recovery lost unknown action or occupation: %+v", s)
	must(t, rt.Dispatch(f.ctx, action.ID))
	other := f.create(l, "blocked")
	competing := f.stage(l, other)
	must(t, rt.Dispatch(f.ctx, competing.ID))
	f.calls(1)

	verified := delegation.Receipt{ActionID: action.ID, State: delegation.Delivered, Detail: "verified against simulator spool"}
	must(t, l.Reconcile(f.ctx, verified))
	s = f.status(l, d.ID)
	must(t, l.Reconcile(f.ctx, verified))
	afterDuplicate := f.status(l, d.ID)
	check(t, s.Delegation.Version == afterDuplicate.Delegation.Version &&
		s.Actions[0].State == delegation.Delivered && s.Occupations[0].ConfirmedAt != nil && s.Occupations[0].ReleasedAt == nil,
		"duplicate settlement advanced or lost occupation")
	records, err := l.History(f.ctx, d.ID)
	must(t, err)
	replay, err := simulation.Replay(records)
	must(t, err)
	check(t, len(replay.Receipts) == 2 && replay.ExternalCalls == 0, "replay receipt transitions: %+v", replay)
	f.calls(1)
	t.Logf("exit=23 recovered=%s occupation=retained sends_before=1 sends_during_verification=0", delegation.Unknown)
}

func TestDeliveryExitHelper(t *testing.T) {
	id := os.Getenv("STEAD_TEST_CRASH_ACTION")
	if id == "" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	clock := simulation.NewClock(start)
	l, err := ledger.Open(ctx, os.Getenv(testDSN), clock)
	must(t, err)
	executor, err := simulation.NewExecutor(os.Getenv("STEAD_TEST_CRASH_DIR"))
	must(t, err)
	executor.AfterDelivery = func(receipt delegation.Receipt) {
		if receipt.State != delegation.Delivered {
			t.Fatalf("unexpected receipt: %+v", receipt)
		}
		os.Exit(23)
	}
	rt := delegation.Runtime{Store: l, Executor: executor}
	must(t, l.WithWorker(ctx, func() error { return rt.Dispatch(ctx, id) }))
	t.Fatal("delivery did not reach crash point")
}

func TestCompletionDeadlineAndReplay(t *testing.T) {
	f := newFixture(t)
	l := f.connect()
	d := f.create(l, "already-complete")
	f.receive(l, event(d, delegation.Evidence, "calendar proof"))
	out, err := f.runtime(l).Advance(f.ctx, d.ID)
	must(t, err)
	check(t, out.Result != nil && out.Result.Complete && out.Result.Evidence == "original calendar proof",
		"missing evidence result: %+v", out)
	check(t, len(f.status(l, d.ID).Actions) == 0, "action created despite completion evidence")

	pending := f.create(l, "cancel-on-evidence")
	action := f.stage(l, pending)
	f.receive(l, event(pending, delegation.Evidence, "earlier confirmation"))
	_, err = f.runtime(l).Advance(f.ctx, pending.ID)
	must(t, err)
	must(t, f.runtime(l).Dispatch(f.ctx, action.ID))
	check(t, f.status(l, pending.ID).Actions[0].State == delegation.Canceled, "completion did not cancel pending action")

	expiring := f.create(l, "expires")
	_ = f.approval(l, expiring)
	must(t, l.Close())
	f.clock.Advance(time.Hour)
	l = f.connect()
	rt := f.runtime(l)
	must(t, rt.Recover(f.ctx))
	out, err = rt.Advance(f.ctx, expiring.ID)
	must(t, err)
	check(t, out.Result != nil && !out.Result.Complete && out.Result.Reason == "delegation deadline reached",
		"missing deadline result: %+v", out)
	for _, id := range []string{d.ID, pending.ID, expiring.ID} {
		history, err := l.History(f.ctx, id)
		must(t, err)
		replay, err := simulation.Replay(history)
		must(t, err)
		persisted := f.status(l, id).Delegation.Result
		check(t, reflect.DeepEqual(persisted, replay.Result) && replay.ExternalCalls == 0,
			"replay=%+v ledger=%+v", replay, persisted)
	}
	f.calls(0)
	t.Log("completion evidence and deadline reproduced from audit records, deliveries=0, replay external calls=0")
}

func TestGrantExpiryAndVerifiedFailureRelease(t *testing.T) {
	f := newFixture(t)
	l := f.connect()
	expiring := f.create(l, "grant-expires")
	expired := f.stage(l, expiring)
	f.clock.Advance(30 * time.Minute)
	rt := f.runtime(l)
	must(t, rt.Dispatch(f.ctx, expired.ID))
	s := f.status(l, expiring.ID)
	check(t, s.Actions[0].State == delegation.Canceled && s.Delegation.Result == nil &&
		f.clock.Now().Before(s.Delegation.Deadline), "grant expiry and delegation deadline were conflated: %+v", s)
	f.calls(0)

	d := f.create(l, "failed-send")
	action := f.stage(l, d)
	claimed, err := l.Claim(f.ctx, action.ID)
	must(t, err)
	check(t, claimed != nil, "action did not claim its resource")
	must(t, l.Settle(f.ctx, delegation.Receipt{ActionID: action.ID, State: delegation.Unknown, Detail: "transport interrupted"}))

	other := f.create(l, "after-release")
	competing := f.stage(l, other)
	must(t, rt.Dispatch(f.ctx, competing.ID))
	f.calls(0)
	must(t, l.Reconcile(f.ctx, delegation.Receipt{ActionID: action.ID, State: delegation.Failed, Detail: "provider verified no effect"}))
	s = f.status(l, d.ID)
	check(t, s.Occupations[0].ConfirmedAt == nil && s.Occupations[0].ReleasedAt != nil, "failed action kept its occupation")
	must(t, rt.Dispatch(f.ctx, competing.ID))
	check(t, f.calls(1)[0].ID == competing.ID, "released resource was not available to competing action")
	t.Log("expired grant blocked dispatch while delegation stayed open; verified failure released the resource")
}
