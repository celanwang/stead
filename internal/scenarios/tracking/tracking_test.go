package tracking

import (
	"slices"
	"testing"
)

func TestStableIdentityAndRevisionOrder(t *testing.T) {
	k := Key{Owner: "alice", Project: "ml-research", Source: "preprints", ObjectID: "stable-123"}
	first := Observation{Key: k, EventID: "created-123", Revision: 1, Title: "An early result"}
	state, decision, err := Apply(Item{}, first, nil)
	if err != nil || decision.Outcome != Applied || decision.Disposition != Immediate {
		t.Fatalf("new object: decision=%+v err=%v", decision, err)
	}

	rename := first
	rename.EventID = "renamed-123"
	rename.Title = "The same result with a new title"
	rename.Revision = 3
	state, decision, err = Apply(state, rename, nil)
	if err != nil || decision.Outcome != Applied || decision.Disposition != Digest {
		t.Fatalf("revision with changed title: decision=%+v err=%v", decision, err)
	}

	stale := first
	stale.EventID = "late-123"
	stale.Revision = 2
	stale.Material = true
	stale.ConclusionIDs = []string{"report/old-claim"}
	state, decision, err = Apply(state, stale, nil)
	if err != nil || decision.Outcome != Stale || decision.Disposition != Silent || state.Latest.Revision != 3 {
		t.Fatalf("late event: state=%+v decision=%+v err=%v", state, decision, err)
	}

	retries := []Observation{rename, rename}
	retries[0].Revision = 5 // Reuse of a stable event ID cannot claim another revision.
	retries[1].EventID = "duplicate-revision"
	for _, retry := range retries {
		state, decision, err = Apply(state, retry, nil)
		if err != nil || decision.Outcome != Duplicate || decision.Disposition != Silent || state.Latest.Revision != 3 {
			t.Fatalf("duplicate: state=%+v decision=%+v err=%v", state, decision, err)
		}
	}

	other := rename
	other.Key.ObjectID = "stable-456"
	other.EventID = "created-456"
	other.Revision = 1
	if _, _, err := Apply(state, other, nil); err == nil {
		t.Fatal("different source object replaced existing tracking state")
	}
	if _, d, err := Apply(Item{}, other, nil); err != nil || d.Disposition != Immediate {
		t.Fatalf("distinct object with identical title: decision=%+v err=%v", d, err)
	}
}

func TestMutePreservesProgressWhileTerminalModesDiscardResults(t *testing.T) {
	key := Key{Owner: "alice", Project: "ongoing", Source: "docs", ObjectID: "doc-8"}
	state, err := SetMode(Item{Key: key}, Muted)
	if err != nil {
		t.Fatal(err)
	}
	event := Observation{Key: key, EventID: "created", Revision: 1, Title: "Private draft"}
	neverCalled := func(Facts) Disposition {
		t.Fatal("classifier was called for a muted, terminal, or rejected event")
		return Immediate
	}

	state, muted, err := Apply(state, event, neverCalled)
	if err != nil || muted.Outcome != Applied || muted.Disposition != Silent || state.Latest.Revision != 1 {
		t.Fatalf("muted watch: state=%+v decision=%+v err=%v", state, muted, err)
	}
	state, err = SetMode(state, Active)
	if err != nil {
		t.Fatal(err)
	}
	_, repeated, err := Apply(state, event, neverCalled)
	if err != nil || repeated.Outcome != Duplicate || repeated.Disposition != Silent {
		t.Fatalf("resume replayed a muted revision: decision=%+v err=%v", repeated, err)
	}

	revision := event
	revision.EventID, revision.Revision, revision.Material = "correction", 3, true
	revision.ConclusionIDs = []string{"report/profit", "report/risk", "report/profit"}
	updated, decision, err := Apply(state, revision, func(f Facts) Disposition {
		if f.Initial || !f.Material || f.Withdrawn ||
			!slices.Equal(f.AffectedConclusions, []string{"report/profit", "report/risk"}) {
			t.Fatalf("classifier facts=%+v", f)
		}
		f.AffectedConclusions[0] = "mutated input"
		return Immediate
	})
	if err != nil || decision.Disposition != Immediate || !slices.Equal(decision.AffectedConclusions, []string{"report/profit", "report/risk"}) {
		t.Fatalf("resumed watch: decision=%+v err=%v", decision, err)
	}
	decision.AffectedConclusions[0] = "mutated output"
	if state.Latest.Revision != 1 || updated.Latest.ConclusionIDs[0] != "report/profit" {
		t.Fatalf("transition shared mutable state: old=%+v new=%+v", state.Latest, updated.Latest)
	}

	for _, terminal := range []Mode{Stopped, TakenOver} {
		t.Run(string(terminal), func(t *testing.T) {
			ended, err := SetMode(updated, terminal)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range []Observation{event, {Key: key, EventID: "late-new", Revision: 4}} {
				result, d, err := Apply(ended, event, neverCalled)
				if err != nil || d.Outcome != Inactive || d.Disposition != Silent || result.Latest.Revision != 3 {
					t.Fatalf("terminal watch changed: state=%+v decision=%+v err=%v", result, d, err)
				}
			}
			if _, err := SetMode(ended, Active); err == nil {
				t.Fatal("terminal watch reactivated")
			}
		})
	}
}
