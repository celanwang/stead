package rsi

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExplicitHandoffAndEqualBudgets(t *testing.T) {
	d, initial := ExampleDataset()
	result, err := Compare(d, initial, Options{Seed: 27})
	if err != nil {
		t.Fatal(err)
	}
	if result.Before.FalseAlarms != 2 || result.Before.MissedAlerts != 0 {
		t.Fatalf("unexpected starting benchmark: %+v", result.Before)
	}
	for _, run := range result.Runs {
		if run.Evaluations != result.Options.Rounds*result.Options.CandidateBudget ||
			run.CaseEvaluations != run.Evaluations*(len(d.Development)+len(d.Validation)) {
			t.Fatalf("unexpected budget for %s: %d candidates, %d examples", run.Mode, run.Evaluations, run.CaseEvaluations)
		}
		t.Logf("%s candidates=%d evaluations=%d holdout false alarms/missed alerts=%d/%d",
			run.Mode, run.Evaluations, run.CaseEvaluations, run.Holdout.FalseAlarms, run.Holdout.MissedAlerts)
	}
	recursive := result.Runs[2]
	first, next := recursive.State.Rounds[0], recursive.State.Rounds[1]
	if first.Loaded.Improver == first.ActiveAfter.Improver || first.SelectedMethodEdit != "retain_paired_outcomes" {
		t.Fatalf("first round did not promote its change to paired selection: %+v", first)
	}
	if next.Loaded.Improver != first.ActiveAfter.Improver || next.ParentComparison == nil ||
		next.ParentComparison.ImproverVersion != first.Loaded.Improver || !next.ParentComparison.ChangedSelection {
		t.Fatalf("new version did not replace its parent in round two: %+v", next)
	}
	if next.Selected.PairedCorrect != next.Selected.PairedTotal || next.ParentComparison.Selected.PairedCorrect >= next.Selected.PairedCorrect {
		t.Fatalf("expected new policy to fix the correction and keep paired alerts: %+v", next)
	}
	if next.ProbeTrials[0].Method.RequirePairs != true || next.ProbeTrials[1].Operation == "retain_paired_outcomes" {
		t.Fatalf("the new method did not generate its own next-round revisions: %+v", next.ProbeTrials)
	}
	if recursive.Holdout.FalseAlarms != 0 || recursive.Holdout.MissedAlerts != 0 {
		t.Fatalf("recursive holdout errors: %+v", recursive.Holdout)
	}
	t.Logf("handoff %s → %s, %s changed its successor from %s to %s; paired %d/%d to %d/%d",
		first.Loaded.Improver, next.Loaded.Improver, next.Target.ID,
		next.ParentComparison.Selected.Operator, next.Selected.Operator,
		next.ParentComparison.Selected.PairedCorrect, next.ParentComparison.Selected.PairedTotal,
		next.Selected.PairedCorrect, next.Selected.PairedTotal)
}

func TestVersionedStateNoOpAndRollback(t *testing.T) {
	d, initial := ExampleDataset()
	s, err := NewState(initial)
	if err != nil {
		t.Fatal(err)
	}
	original := s.Active
	first, err := s.Improve(d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if first.ActiveAfter == original {
		t.Fatal("expected the first experiment to promote versions")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded State
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Rollback(original.Policy, original.Improver); err != nil {
		t.Fatal(err)
	}
	if reloaded.Active != original || !reflect.DeepEqual(reloaded.Policy(), initial) ||
		len(reloaded.Rounds) != 1 || len(reloaded.Rollbacks) != 1 {
		t.Fatalf("rollback did not preserve lineage: %+v", reloaded)
	}
	reloaded.Policies[0].Policy.Rules[0].Decide = Digest
	if err := reloaded.Validate(); err == nil {
		t.Fatal("accepted a modified version with its old identity")
	}

	stable, err := NewState(DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	before := stable.Active
	noChange, err := stable.Improve(d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if noChange.Loaded != before || noChange.ActiveAfter != before || len(stable.Rounds) != 1 {
		t.Fatalf("a passing policy did not retain its version: %+v", noChange)
	}
}

func TestHoldoutSeparationAndFixedPromotionChecks(t *testing.T) {
	d, initial := ExampleDataset()
	a, err := Compare(d, initial, Options{})
	if err != nil {
		t.Fatal(err)
	}
	d.Holdout[0].Expected = Immediate
	b, err := Compare(d, initial, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range a.Runs {
		if !reflect.DeepEqual(a.Runs[i].State, b.Runs[i].State) {
			t.Fatalf("holdout labels changed selection for %s", a.Runs[i].Mode)
		}
	}
	d.Holdout[0].Family = d.Development[0].Family
	if _, err := Compare(d, initial, Options{}); err == nil {
		t.Fatal("accepted a holdout family also used during development")
	}
	d, initial = ExampleDataset()
	// An independent must-alert case specifically rejects the broad cosmetic edit.
	d.Validation = append(d.Validation, Example{ID: "checked-historical-claim", Family: "archive-val",
		Signal: Signal{Cosmetic: true, ImpactsConclusion: true}, Expected: Immediate})
	m, err := NewState(initial)
	if err != nil {
		t.Fatal(err)
	}
	trial, err := m.Improve(d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(trial.Candidates[0].RejectedByChecks) != 1 ||
		trial.Candidates[0].RejectedByChecks[0] != "checked-historical-claim" ||
		trial.Selected.ID == trial.Candidates[0].ID {
		t.Fatalf("regressing a known validation example was not blocked: %+v", trial)
	}
}
