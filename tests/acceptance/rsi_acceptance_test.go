package acceptance

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
	"github.com/celanwang/stead/internal/rsi"
	"github.com/celanwang/stead/internal/scenarios/tracking"
)

func TestTrackedCorrectionProducesApprovedLivePolicy(t *testing.T) {
	f := newFixture(t)
	l := f.connect()
	owner, project := f.prefix+"-owner", f.prefix+"-project"
	state, err := l.CreateRSIProject(f.ctx, owner, project, rsi.DefaultPolicy())
	must(t, err)
	_, err = l.CreateRSIProject(f.ctx, owner, project+"-other", rsi.DefaultPolicy())
	must(t, err)
	key := tracking.Key{Owner: owner, Project: project, Source: "catalog", ObjectID: "component"}
	observe := func(id string, revision uint64) ledger.TrackingRecord {
		state, err := l.RSIProject(f.ctx, owner, project)
		must(t, err)
		policy, version, err := state.NotificationPolicy()
		must(t, err)
		result, err := l.ObserveTracking(f.ctx, tracking.Observation{
			Key: key, EventID: id, Revision: revision, Title: "private raw title",
			Summary: "private raw summary", Material: true,
		}, ledger.TrackingOptions{
			PolicyVersion: version,
			Classify: func(facts tracking.Facts) tracking.Disposition {
				return tracking.Disposition(policy.Decide(ledger.TrackingSignal(facts)))
			},
		})
		must(t, err)
		return result.Record
	}

	observe("initial", 1)
	ordinary := observe("before-release", 2)
	check(t, ordinary.Decision.Disposition == tracking.Digest && ordinary.PolicyVersion == rsi.BaselineVersion,
		"unexpected baseline outcome: %+v", ordinary)
	_, err = l.RecordRSIFeedback(f.ctx, owner, project+"-other", ordinary.Seq, rsi.Immediate, "", false)
	check(t, errors.Is(err, delegation.ErrNotFound), "cross-project feedback returned %v", err)
	label, err := l.RecordRSIFeedback(f.ctx, owner, project, ordinary.Seq, rsi.Immediate, "", false)
	must(t, err)
	duplicate, err := l.RecordRSIFeedback(f.ctx, owner, project, ordinary.Seq, rsi.Immediate, "", false)
	must(t, err)
	check(t, duplicate == label && label.Example.Signal == (rsi.Signal{Material: true}),
		"unexpected sanitized feedback: %+v", label)
	raw, err := json.Marshal(label)
	must(t, err)
	check(t, !strings.Contains(string(raw), "private raw"), "raw tracking content leaked to feedback")

	validation := rsi.Example{ID: "validated-withdrawal", Family: "selection-withdrawals",
		Signal: rsi.Signal{Withdrawn: true}, Expected: rsi.Immediate}
	round, err := l.ImproveRSI(f.ctx, owner, project, rsi.Dataset{Validation: []rsi.Example{validation}},
		rsi.Options{TargetID: label.Example.ID}, true)
	must(t, err)
	check(t, round.Selected.TargetCorrect && round.Target.ID == label.Example.ID &&
		round.ActiveAfter.Policy != state.Research.Active.Policy, "correction failed to produce a distinct policy")

	must(t, l.Close())
	l = f.connect()
	staged := observe("still-baseline", 3)
	check(t, staged.PolicyVersion == rsi.BaselineVersion && staged.Decision.Disposition == tracking.Digest,
		"research contaminated live routing")
	holdout := []rsi.Example{
		{ID: "new-source-release", Family: "releases-held", Signal: rsi.Signal{Material: true}, Expected: rsi.Immediate},
		{ID: "ordinary-rewording", Family: "formatting-held", Signal: rsi.Signal{Cosmetic: true}, Expected: rsi.Digest},
	}
	reused := append(append([]rsi.Example(nil), holdout...), validation)
	_, err = l.ActivateRSI(f.ctx, owner, project, round.ActiveAfter.Policy, reused)
	check(t, err != nil && strings.Contains(err.Error(), "used for selection"), "training family was accepted as holdout: %v", err)
	release, err := l.ActivateRSI(f.ctx, owner, project, round.ActiveAfter.Policy, holdout)
	must(t, err)
	check(t, release.Report.Approved && release.Report.Baseline.MissedAlerts == 1 && release.Report.Candidate.MissedAlerts == 0,
		"live release did not fix the independent positive case: %+v", release.Report)
	labeledHoldout := holdout[0]
	labeledHoldout.Feedback = true
	_, err = l.ImproveRSI(f.ctx, owner, project, rsi.Dataset{Development: []rsi.Example{labeledHoldout}, Validation: []rsi.Example{validation}},
		rsi.Options{}, false)
	check(t, err != nil && strings.Contains(err.Error(), "already used for live validation"), "release holdout leaked to training: %v", err)
	after := observe("using-live-release", 4)
	check(t, after.PolicyVersion == release.To && after.Decision.Disposition == tracking.Immediate,
		"tracking did not apply released policy: %+v", after)
	_, err = l.RollbackRSI(f.ctx, owner, project, rsi.BaselineVersion, "", true)
	must(t, err)
	restored := observe("after-rollback", 5)
	check(t, restored.PolicyVersion == rsi.BaselineVersion && restored.Decision.Disposition == tracking.Digest,
		"tracking did not return to baseline: %+v", restored)
	t.Logf("observation %d → learned %s → live %s → rollback %s", ordinary.Seq, round.ActiveAfter.Policy, after.Decision.Disposition, restored.Decision.Disposition)
}

func TestLivePolicyReleaseRejectsUnseenCriticalSuppression(t *testing.T) {
	f := newFixture(t)
	l := f.connect()
	policy := rsi.DefaultPolicy()
	policy.Rules = append([]rsi.Rule{{Require: []rsi.Feature{rsi.Material}, Decide: rsi.Digest}}, policy.Rules...)
	owner, project := f.prefix+"-owner", f.prefix+"-project"
	state, err := l.CreateRSIProject(f.ctx, owner, project, policy)
	must(t, err)
	holdout := []rsi.Example{
		{ID: "ordinary-new", Family: "new-held", Signal: rsi.Signal{FirstSeen: true}, Expected: rsi.Immediate},
		{ID: "ordinary-edit", Family: "edit-held", Signal: rsi.Signal{Material: true}, Expected: rsi.Digest},
	}
	release, err := l.ActivateRSI(f.ctx, owner, project, state.Research.Active.Policy, holdout)
	must(t, err)
	check(t, !release.Report.Approved && len(release.Report.NewErrors) == 0 && len(release.Report.MissedCritical) > 0,
		"unexpected approval of critical suppression: %+v", release)
	reconnect := f.connect()
	p, err := reconnect.RSIProject(f.ctx, owner, project)
	must(t, err)
	check(t, p.LivePolicyVersion == rsi.BaselineVersion && len(p.Releases) == 1 && !p.Releases[0].Report.Approved,
		"rejected release was not persisted without activation: %+v", p)
}
