package acceptance

import (
	"errors"
	"testing"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
	"github.com/celanwang/stead/internal/preferences"
	"github.com/celanwang/stead/internal/scenarios/tracking"
)

func TestTrackingEventDedupAcrossConnections(t *testing.T) {
	f := newFixture(t)
	first, second := f.connect(), f.connect()
	key := tracking.Key{Owner: f.prefix, Project: "research", Source: "preprints", ObjectID: "first"}
	e := tracking.Observation{Key: key, EventID: "source-event", Revision: 1, Title: "Paper"}
	competing := e
	competing.Key.ObjectID = "second"
	type result struct {
		record ledger.TrackingIntake
		err    error
	}
	barrier, results := make(chan struct{}), make(chan result, 2)
	for i, l := range []*ledger.Ledger{first, second} {
		event := []tracking.Observation{e, competing}[i]
		go func() {
			<-barrier
			record, err := l.ObserveTracking(f.ctx, event, ledger.TrackingOptions{})
			results <- result{record, err}
		}()
	}
	close(barrier)

	a, b := <-results, <-results
	must(t, a.err)
	must(t, b.err)
	check(t, a.record.Duplicate != b.record.Duplicate && a.record.Record.Seq == b.record.Record.Seq,
		"concurrent intake did not deduplicate atomically: %+v %+v", a, b)
	winning := a.record.Record.Observation.Key
	losing := e.Key
	if winning == losing {
		losing = competing.Key
	}
	if _, err := second.TrackingStatus(f.ctx, losing); !errors.Is(err, delegation.ErrNotFound) {
		t.Fatalf("losing object registration was not rolled back: %v", err)
	}
	status, err := second.TrackingStatus(f.ctx, winning)
	must(t, err)
	check(t, status.Latest != nil && status.Latest.Revision == 1, "committed state missing: %+v", status)
	feed, err := second.TrackingFeed(f.ctx, winning.Owner, winning.Project, tracking.Immediate)
	must(t, err)
	check(t, len(feed) == 1 && feed[0].Seq == a.record.Record.Seq, "duplicate or missing feed item: %+v", feed)
	t.Log("two source deliveries, one durable observation, one tracked object, and one feed entry")
}

func TestTrackingPreferencesMuteTakeoverAndFeed(t *testing.T) {
	f := newFixture(t)
	l := f.connect()
	key := tracking.Key{Owner: f.prefix, Project: "research", Source: "docs", ObjectID: "paper"}
	base := preferences.Record{
		ID: "base", Owner: key.Owner, Name: ledger.TrackingNotificationPreference,
		Scope: preferences.OwnerScope, Value: "digest", Original: "Use digests", Source: "local",
	}
	_, err := l.SetPreference(f.ctx, base)
	must(t, err)
	expiry := f.clock.Now().Add(time.Hour)
	temporary := preferences.Record{
		ID: "temporary", Owner: key.Owner, Project: key.Project, Name: ledger.TrackingNotificationPreference,
		Scope: preferences.ProjectScope, Value: "silent", Original: "Hold alerts for one hour",
		Source: "local", ExpiresAt: &expiry,
	}
	_, err = l.SetPreference(f.ctx, temporary)
	must(t, err)
	observe := func(id string, revision uint64) ledger.TrackingRecord {
		t.Helper()
		out, err := l.ObserveTracking(f.ctx, tracking.Observation{
			Key: key, EventID: id, Revision: revision, ConclusionIDs: []string{"board-report/revenue"},
		}, ledger.TrackingOptions{PolicyVersion: "test-policy", Classify: func(tracking.Facts) tracking.Disposition {
			return tracking.Immediate
		}})
		must(t, err)
		check(t, !out.Duplicate, "unexpected intake duplicate: %+v", out)
		return out.Record
	}
	feed := func(want int) {
		t.Helper()
		rows, err := l.TrackingFeed(f.ctx, key.Owner, key.Project, "")
		must(t, err)
		check(t, len(rows) == want, "expected %d feed items, got %+v", want, rows)
	}
	first := observe("first", 1)
	check(t, !first.PolicyUsed && first.PreferenceID == temporary.ID && first.Decision.Disposition == tracking.Silent,
		"temporary preference did not override notification policy: %+v", first)
	feed(0)

	must(t, l.Close())
	l = f.connect()
	f.clock.Advance(time.Hour)
	latest := observe("after-expiry", 2)
	check(t, latest.PreferenceID == base.ID && latest.Decision.Disposition == tracking.Digest,
		"did not fall back to the owner preference after expiry: %+v", latest)
	feed(1)
	read, err := l.AcknowledgeTracking(f.ctx, key.Owner, key.Project, latest.Seq)
	must(t, err)
	f.clock.Advance(time.Second)
	readAgain, err := l.AcknowledgeTracking(f.ctx, key.Owner, key.Project, latest.Seq)
	must(t, err)
	check(t, read.ReadAt != nil && readAgain.ReadAt != nil && read.ReadAt.Equal(*readAgain.ReadAt),
		"acknowledgment was not idempotent: %+v %+v", read, readAgain)
	feed(0)

	revoked, err := l.RevokePreference(f.ctx, key.Owner, base.ID)
	must(t, err)
	check(t, revoked.RevokedAt != nil, "preference revoke was not persisted")
	deliverable := observe("pending", 3)
	check(t, deliverable.PolicyUsed && deliverable.PolicyVersion == "test-policy" &&
		deliverable.PreferenceID == "" && deliverable.Decision.Disposition == tracking.Immediate,
		"policy was not applied after revocation: %+v", deliverable)
	signal := ledger.TrackingSignal(deliverable.Facts)
	check(t, !signal.FirstSeen && signal.Cosmetic && signal.ImpactsConclusion,
		"policy did not retain the exact observed signal: %+v", signal)
	feed(1)
	_, err = l.SetTrackingMode(f.ctx, key, tracking.Muted)
	must(t, err)
	feed(0)
	muted := observe("updates-while-muted", 4)
	check(t, muted.Decision.Disposition == tracking.Silent && muted.Decision.Outcome == tracking.Applied && !muted.PolicyUsed,
		"muting did not preserve silent progress: %+v", muted)
	_, err = l.SetTrackingMode(f.ctx, key, tracking.Active)
	must(t, err)
	feed(0)

	_, err = l.SetTrackingMode(f.ctx, key, tracking.TakenOver)
	must(t, err)
	late := observe("completed-after-takeover", 5)
	status, err := l.TrackingStatus(f.ctx, key)
	must(t, err)
	check(t, late.Decision.Outcome == tracking.Inactive && status.Latest != nil && status.Latest.Revision == 4,
		"takeover did not freeze tracked state: %+v %+v", late, status)
	history, err := l.TrackingHistory(f.ctx, key)
	must(t, err)
	check(t, len(history) == 5 && history[2].SuppressedAt != nil && history[2].Decision.Disposition == tracking.Immediate &&
		history[0].PreferenceID == temporary.ID && len(history[0].Decision.AffectedConclusions) == 1,
		"prior decisions or notification suppression are not auditable: %+v", history)
	feed(0)
	t.Log("temporary preference expired across reconnect, revocation restored policy, mute hid backlog, takeover ignored result")
}
