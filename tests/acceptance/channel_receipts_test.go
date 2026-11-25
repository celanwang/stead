package acceptance

import (
	"testing"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
)

func acknowledgmentAction(f fixture, l *ledger.Ledger, suffix, target string) delegation.Action {
	f.Helper()
	d := delegation.Delegation{
		ID: f.prefix + "-" + suffix, Owner: f.prefix, Goal: "Coordinate the review", Sources: []string{"acceptance"},
		Deadline: f.clock.Now().Add(time.Hour), Request: delegation.Scope{Target: target, Content: "Review at 10:00"},
	}
	must(f.T, l.Create(f.ctx, d))
	return f.stage(l, d)
}

func TestRecoveryFromFeishuAcknowledgment(t *testing.T) {
	f := newFixture(t)
	l := f.connect()
	acknowledged := acknowledgmentAction(f, l, "acknowledged", "feishu:oc_team")
	uncertain := acknowledgmentAction(f, l, "uncertain", "feishu:oc_team")
	proof := delegation.Receipt{ActionID: acknowledged.ID, State: delegation.Delivered, Detail: "om_" + f.prefix}

	check(t, l.RecordDeliveryAcknowledgment(f.ctx, proof) != nil, "pending action accepted a provider receipt")
	for _, a := range []delegation.Action{acknowledged, uncertain} {
		claimed, err := l.Claim(f.ctx, a.ID)
		must(t, err)
		check(t, claimed != nil, "could not claim %s", a.ID)
	}
	// Response has reached the worker, but settlement has not committed.
	must(t, l.RecordDeliveryAcknowledgment(f.ctx, proof))
	must(t, l.RecordDeliveryAcknowledgment(f.ctx, proof))
	must(t, l.VerifyDeliveryAcknowledgments(f.ctx))
	check(t, f.status(l, acknowledged.DelegationID).Actions[0].State == delegation.Sending, "verifier settled an in-flight send")

	conflict := proof
	conflict.Detail += "-another"
	check(t, l.RecordDeliveryAcknowledgment(f.ctx, conflict) != nil, "replaced the recorded provider message ID")
	conflict = proof
	conflict.ActionID = uncertain.ID
	check(t, l.RecordDeliveryAcknowledgment(f.ctx, conflict) != nil, "same provider message ID acknowledged another action")

	must(t, l.Close())
	l = f.connect()
	rt := f.runtime(l)
	must(t, rt.Recover(f.ctx))
	check(t, f.status(l, acknowledged.DelegationID).Actions[0].State == delegation.Unknown, "recovery did not preserve uncertainty")
	check(t, l.Reconcile(f.ctx, delegation.Receipt{
		ActionID: acknowledged.ID, State: delegation.Failed, Detail: "manual report conflicts with provider",
	}) != nil, "manual verification overwrote a recorded provider acknowledgment")
	must(t, l.VerifyDeliveryAcknowledgments(f.ctx))
	s := f.status(l, acknowledged.DelegationID)
	check(t, s.Actions[0].State == delegation.Delivered && s.Actions[0].Receipt.Detail == proof.Detail, "missing provider proof: %+v", s.Actions)
	must(t, l.RecordDeliveryAcknowledgment(f.ctx, proof))
	must(t, l.VerifyDeliveryAcknowledgments(f.ctx))
	check(t, f.status(l, acknowledged.DelegationID).Delegation.Version == s.Delegation.Version, "duplicate acknowledgment changed state")
	check(t, f.status(l, uncertain.DelegationID).Actions[0].State == delegation.Unknown, "matched another action by identical chat or content")
	must(t, rt.Dispatch(f.ctx, acknowledged.ID))
	must(t, rt.Dispatch(f.ctx, uncertain.ID))
	f.calls(0)
	t.Log("two identical sends interrupted; one stored provider message ID settled only its action; 0 recovery sends")
}

func TestAcknowledgmentsExcludeManualEvidenceAndOtherTargets(t *testing.T) {
	f := newFixture(t)
	l := f.connect()
	for _, target := range []string{"sim:chat", "feishu:", "feishu:oc_manual"} {
		a := acknowledgmentAction(f, l, target, target)
		claimed, err := l.Claim(f.ctx, a.ID)
		must(t, err)
		check(t, claimed != nil, "claim failed")
		proof := delegation.Receipt{ActionID: a.ID, State: delegation.Delivered, Detail: "om_" + f.prefix}
		if target == "feishu:oc_manual" {
			for _, state := range []delegation.ActionState{delegation.Failed, delegation.Unknown} {
				check(t, l.RecordDeliveryAcknowledgment(f.ctx, delegation.Receipt{ActionID: a.ID, State: state, Detail: "operator note"}) != nil,
					"accepted nonpositive provider receipt")
			}
			must(t, l.Settle(f.ctx, delegation.Receipt{ActionID: a.ID, State: delegation.Unknown, Detail: "interrupted"}))
			must(t, l.Reconcile(f.ctx, proof))
		}
		check(t, l.RecordDeliveryAcknowledgment(f.ctx, proof) != nil, "new provider evidence accepted for %s", target)
	}
}

func TestConcurrentAcknowledgmentsCannotShareProviderID(t *testing.T) {
	f := newFixture(t)
	left, right := f.connect(), f.connect()
	first := acknowledgmentAction(f, left, "first", "feishu:oc_team")
	second := acknowledgmentAction(f, right, "second", "feishu:oc_team")
	type result struct {
		id  string
		err error
	}
	ready, results := make(chan struct{}), make(chan result, 2)
	for l, a := range map[*ledger.Ledger]delegation.Action{left: first, right: second} {
		claimed, err := l.Claim(f.ctx, a.ID)
		must(t, err)
		check(t, claimed != nil, "action not claimed: %s", a.ID)
		go func() {
			<-ready
			err := l.RecordDeliveryAcknowledgment(f.ctx, delegation.Receipt{
				ActionID: a.ID, State: delegation.Delivered, Detail: "om_" + f.prefix,
			})
			results <- result{a.ID, err}
		}()
	}
	close(ready)
	a, b := <-results, <-results
	check(t, (a.err == nil) != (b.err == nil), "expected exactly one acknowledgment: %+v, %+v", a, b)
	winner := a.id
	if a.err != nil {
		winner = b.id
	}
	must(t, f.runtime(left).Recover(f.ctx))
	must(t, right.VerifyDeliveryAcknowledgments(f.ctx))
	for _, action := range []delegation.Action{first, second} {
		want := delegation.Unknown
		if action.ID == winner {
			want = delegation.Delivered
		}
		check(t, f.status(left, action.DelegationID).Actions[0].State == want, "conflicting acknowledgment for %s", action.ID)
	}
}
