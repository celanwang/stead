package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
	"github.com/celanwang/stead/internal/scenarios/briefing"
	"github.com/celanwang/stead/internal/simulation"
)

type briefCase struct {
	fixture
	l        *ledger.Ledger
	provider *ledger.Ledger
	d        delegation.Delegation
	rt       delegation.Runtime
	sends    int
}

type observedSend func(context.Context, delegation.Action) (delegation.Receipt, error)

func (send observedSend) Execute(ctx context.Context, a delegation.Action) (delegation.Receipt, error) {
	return send(ctx, a)
}

func newBriefCase(t *testing.T, fallback bool, ids ...string) *briefCase {
	t.Helper()
	b := &briefCase{fixture: newFixture(t)}
	b.l, b.provider = b.connect(), b.connect()
	spec, err := delegation.NewBriefing("release", "editor", "team", "bullets", ids, fallback, 10*time.Minute)
	must(t, err)
	b.d = delegation.Delegation{
		ID: b.prefix + "-brief", Owner: b.prefix + "-owner", Goal: "Send the reviewed release brief to the team",
		Sources: []string{"acceptance"}, Deadline: b.clock.Now().Add(time.Hour),
		Request: delegation.Scope{Target: "team-chat"}, Briefing: spec,
	}
	must(t, b.l.Create(b.ctx, b.d))
	_, err = b.provider.SetBriefingAudience(b.ctx, b.d.Owner, spec.Project, "team-chat", "team", 0)
	must(t, err)
	executor := briefing.Executor{World: b.provider}
	b.rt = delegation.Runtime{
		Store: b.l, Proposer: briefing.Proposer{World: b.provider, Poll: time.Minute},
		Executor: observedSend(func(ctx context.Context, a delegation.Action) (delegation.Receipt, error) {
			b.sends++
			return executor.Execute(ctx, a)
		}),
	}
	return b
}

func (b *briefCase) fact(id, text string, shareable bool, expected uint64) delegation.Fact {
	b.Helper()
	f, err := b.provider.SetBriefingFact(b.ctx, b.d.Owner, b.d.Briefing.Project, id, text, shareable, expected)
	must(b.T, err)
	return f
}

func (b *briefCase) advance() delegation.Decision {
	b.Helper()
	out, err := b.rt.Advance(b.ctx, b.d.ID)
	must(b.T, err)
	return out
}

func (b *briefCase) reviewEvent(fact delegation.Fact, label string) delegation.Event {
	e := event(b.d, delegation.Review, label)
	e.Sender = b.d.Briefing.Reviewer
	e.Review = &delegation.ReviewNote{Source: fact.ID, Fingerprint: fact.Fingerprint}
	return e
}

func (b *briefCase) review(fact delegation.Fact) delegation.Decision {
	b.Helper()
	b.receive(b.l, b.reviewEvent(fact, fmt.Sprintf("review-%s-%d", fact.ID, fact.Revision)))
	return b.advance()
}

func (b *briefCase) stageBrief() delegation.Action {
	b.Helper()
	s := b.status(b.l, b.d.ID).Delegation
	check(b.T, s.Wait != nil && s.Wait.Kind == delegation.Approval && s.Request.Guard != nil, "owner approval wait missing: %+v", s)
	e := event(b.d, delegation.Approval, fmt.Sprintf("approve-%d", s.Version))
	e.Correlation = s.Wait.Correlation
	e.Grant = &delegation.GrantTerms{Scope: s.Request, ExpiresAt: b.clock.Now().Add(30 * time.Minute)}
	b.receive(b.l, e)
	out := b.advance()
	check(b.T, out.Action != nil && out.Action.State == delegation.Pending, "approval did not produce a pending action: %+v", out)
	return *out.Action
}

func (b *briefCase) deliveries(want int) []ledger.BriefingDelivery {
	b.Helper()
	rows, err := b.provider.BriefingDeliveries(b.ctx, b.d.ID)
	must(b.T, err)
	check(b.T, len(rows) == want, "got %d provider deliveries, want %d: %+v", len(rows), want, rows)
	return rows
}

func (b *briefCase) verifyReplay() {
	b.Helper()
	history, err := b.l.History(b.ctx, b.d.ID)
	must(b.T, err)
	before := b.sends
	replay, err := simulation.Replay(history)
	must(b.T, err)
	current := b.status(b.l, b.d.ID)
	check(b.T, replay.ExternalCalls == 0 && b.sends == before && reflect.DeepEqual(replay.Result, current.Delegation.Result),
		"replay differs from persisted result: %+v / %+v", replay.Result, current.Delegation.Result)
}

func TestBriefingExactArtifactAndPrivateSources(t *testing.T) {
	b := newBriefCase(t, true, "schedule", "budget", "launch")
	const secret = "private vendor rate 745"
	schedule := b.fact("schedule", "Monday", true, 0)
	budget := b.fact("budget", secret, false, 0)
	launch := b.fact("launch", "Friday", true, 0)
	b.advance()
	for _, fact := range []delegation.Fact{schedule, budget, launch} {
		b.review(fact)
	}
	initial := b.status(b.l, b.d.ID).Delegation
	amend := event(b.d, delegation.Amend, "owner-handles-schedule")
	amend.Amendment = &delegation.Amendment{TakeOver: schedule.ID}
	b.receive(b.l, amend)
	b.advance()
	s := b.status(b.l, b.d.ID).Delegation
	check(t, s.Briefing.Items[0].TakenOver && s.Briefing.Items[1] == initial.Briefing.Items[1] && s.Briefing.Items[2] == initial.Briefing.Items[2],
		"unaffected facts were rendered or reviewed again: %+v", s.Briefing.Items)
	check(t, initial.Wait.Correlation != s.Wait.Correlation, "amendment reused the old approval")
	action := b.stageBrief()
	want := "- launch: Friday"
	check(t, action.Scope.Content == want && len(action.Scope.Guard.Facts) == 2, "unexpected approved artifact: %+v", action.Scope)
	must(t, b.rt.Dispatch(b.ctx, action.ID))
	result := b.advance().Result
	delivered := b.deliveries(1)[0]
	check(t, b.sends == 1 && !delivered.Manual && delivered.Content == want && result != nil && result.Complete &&
		result.ArtifactID == delegation.Fingerprint(want) && result.Evidence == delivered.Proof.ID && delivered.Proof.ArtifactID == result.ArtifactID &&
		delivered.Proof.Audience == action.Scope.Guard.Audience, "result and provider delivery differ: %+v %+v", result, delivered)
	history, err := b.l.History(b.ctx, b.d.ID)
	must(t, err)
	encoded, err := json.Marshal([]any{b.status(b.l, b.d.ID), history, delivered})
	must(t, err)
	check(t, !strings.Contains(string(encoded), secret), "private source plaintext leaked into runtime, audit or delivery")
	b.verifyReplay()
	t.Log("takeover removed one item, two observations reused, private value absent, exact approved artifact delivered once")
}

func TestBriefingProviderCASAfterClaim(t *testing.T) {
	for _, mutation := range []string{"source", "audience"} {
		t.Run(mutation, func(t *testing.T) {
			b := newBriefCase(t, false, "launch")
			current := b.fact("launch", "Friday", true, 0)
			b.advance()
			b.review(current)
			old := b.stageBrief()
			claimed, err := b.l.Claim(b.ctx, old.ID)
			must(t, err)
			check(t, claimed != nil, "first action was not claimed")
			switch mutation {
			case "source":
				current = b.fact("launch", "Monday", true, current.Revision)
			case "audience":
				_, err = b.provider.SetBriefingAudience(b.ctx, b.d.Owner, b.d.Briefing.Project, old.Scope.Target, "team", 1)
				must(t, err)
			}
			r, err := b.rt.Executor.Execute(b.ctx, *claimed)
			must(t, err)
			check(t, r.State == delegation.Failed, "provider accepted stale %s: %+v", mutation, r)
			must(t, b.l.Settle(b.ctx, r))
			b.deliveries(0)
			out := b.advance()
			if mutation == "source" {
				check(t, out.Wait != nil && out.Wait.Kind == delegation.Review && out.Prepared.Guard == nil, "changed source retained review: %+v", out)
				b.review(current)
			}
			b.deliveries(0)
			fresh := b.stageBrief()
			check(t, !reflect.DeepEqual(old.Scope.Guard, fresh.Scope.Guard) && fresh.ID != old.ID && fresh.GrantID != old.GrantID,
				"stale approval reused: old=%+v fresh=%+v", old, fresh)
			must(t, b.rt.Dispatch(b.ctx, fresh.ID))
			result := b.advance().Result
			row := b.deliveries(1)[0]
			check(t, result != nil && result.Complete && row.Proof.ID == fresh.ID && row.Content == "- launch: "+current.Value,
				"wrong final artifact after provider conflict: %+v %+v", result, row)
			t.Logf("%s changed after claim, old sends produced 0 effects; fresh approval produced 1", mutation)
		})
	}
}

func TestBriefingManualCompletion(t *testing.T) {
	b := newBriefCase(t, false, "launch")
	fact := b.fact("launch", "Friday", true, 0)
	b.advance()
	b.review(fact)
	s := b.status(b.l, b.d.ID)
	proof, err := b.provider.WriteBriefingDelivery(b.ctx, b.prefix+"-manual", s.Delegation.Request, true)
	must(t, err)
	check(t, proof.State == delegation.Delivered, "manual delivery failed: %+v", proof)
	b.clock.Advance(time.Minute)
	result := b.advance().Result
	row := b.deliveries(1)[0]
	check(t, result != nil && result.Complete && result.Evidence == proof.Detail && row.Manual && b.sends == 0 &&
		len(b.status(b.l, b.d.ID).Actions) == 0, "manual completion was not recognized: %+v %+v", result, row)
	b.verifyReplay()
	t.Log("1 manual provider record, 0 agent sends and 0 registered actions")
}

func TestBriefingDeadlineWithoutSendApproval(t *testing.T) {
	for _, fallback := range []bool{true, false} {
		t.Run(fmt.Sprintf("return-draft-%t", fallback), func(t *testing.T) {
			b := newBriefCase(t, fallback, "launch")
			b.fact("launch", "tentative Friday", true, 0)
			b.advance()
			must(t, b.l.Close())
			b.clock.Advance(time.Hour)
			b.l = b.connect()
			b.rt.Store = b.l
			must(t, b.rt.Recover(b.ctx))
			out := b.advance()
			check(t, out.Result != nil && !out.Result.Complete && out.Result.At.Equal(b.d.Deadline), "incorrect deadline result: %+v", out)
			if fallback {
				check(t, out.Result.Draft == "- launch: tentative Friday" && out.Result.ArtifactID == delegation.Fingerprint(out.Result.Draft),
					"authorized fallback missing draft: %+v", out.Result)
			} else {
				check(t, out.Result.Draft == "" && out.Result.ArtifactID == "", "unsolicited fallback: %+v", out.Result)
			}
			check(t, b.sends == 0 && len(b.status(b.l, b.d.ID).Actions) == 0, "unapproved action created")
			b.deliveries(0)
			b.verifyReplay()
		})
	}
}

func TestBriefingReviewerAndOutOfOrderFacts(t *testing.T) {
	b := newBriefCase(t, false, "launch")
	first := b.fact("launch", "Friday", true, 0)
	b.advance()
	second := b.fact("launch", "Monday", true, first.Revision)
	_, err := b.provider.SetBriefingFact(b.ctx, b.d.Owner, b.d.Briefing.Project, first.ID, "stale overwrite", true, first.Revision)
	check(t, err != nil, "out-of-order source update succeeded")
	wrong := b.reviewEvent(second, "wrong-reviewer")
	wrong.Sender = b.d.Owner
	inserted, err := b.l.Receive(b.ctx, wrong)
	check(t, !inserted && err != nil, "the owner substituted for the assigned reviewer")
	out := b.review(first)
	check(t, out.Wait != nil && out.Wait.Kind == delegation.Review && out.Prepared.Guard == nil &&
		out.Briefing.Draft == "- launch: Monday", "stale review or out-of-order source accepted: %+v", out)
	out = b.review(second)
	check(t, out.Wait != nil && out.Wait.Kind == delegation.Approval && out.Prepared.Guard != nil,
		"current reviewer evidence did not permit asking owner: %+v", out)
	b.deliveries(0)
}
