package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/rsi"
	"github.com/celanwang/stead/internal/scenarios/tracking"
	"gorm.io/gorm"
)

type RSIProject struct {
	Owner             string        `json:"owner"`
	Project           string        `json:"project"`
	Revision          uint64        `json:"revision"`
	Research          rsi.State     `json:"research"`
	LivePolicyVersion string        `json:"live_policy_version"`
	SelectionFamilies []string      `json:"selection_families,omitempty"`
	Feedback          []RSIFeedback `json:"feedback,omitempty"`
	Releases          []RSIRelease  `json:"releases,omitempty"`
}

type RSIFeedback struct {
	ObservationSeq uint64          `json:"observation_seq"`
	PolicyVersion  string          `json:"policy_version"`
	Observed       rsi.Disposition `json:"observed"`
	Example        rsi.Example     `json:"example"`
	At             time.Time       `json:"at"`
}

type RSIRelease struct {
	Operation string                `json:"operation"`
	From      string                `json:"from"`
	To        string                `json:"to"`
	At        time.Time             `json:"at"`
	Report    *rsi.ActivationReport `json:"report,omitempty"`
}

type rsiProjectRow struct {
	Owner    string `gorm:"primaryKey"`
	Project  string `gorm:"primaryKey"`
	Revision uint64
	Data     RSIProject `gorm:"serializer:json"`
}

func (rsiProjectRow) TableName() string { return "rsi_projects" }

func validateRSIScope(owner, project string) error {
	if err := validateTrackingScope(owner, project); err != nil {
		return errors.New("rsi requires a valid owner and project")
	}
	return nil
}

func (l *Ledger) CreateRSIProject(ctx context.Context, owner, project string, policy rsi.Policy) (RSIProject, error) {
	if err := validateRSIScope(owner, project); err != nil {
		return RSIProject{}, err
	}
	state, err := rsi.NewState(policy)
	if err != nil {
		return RSIProject{}, err
	}
	record := RSIProject{
		Owner: owner, Project: project, Revision: 1, Research: state, LivePolicyVersion: rsi.BaselineVersion,
	}
	row := rsiProjectRow{Owner: owner, Project: project, Revision: record.Revision, Data: record}
	if err := l.db.WithContext(ctx).Create(&row).Error; err != nil {
		return RSIProject{}, err
	}
	return record, nil
}

func (l *Ledger) RSIProject(ctx context.Context, owner, project string) (RSIProject, error) {
	if err := validateRSIScope(owner, project); err != nil {
		return RSIProject{}, err
	}
	var row rsiProjectRow
	err := l.db.WithContext(ctx).Take(&row, "owner = ? AND project = ?", owner, project).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RSIProject{}, delegation.ErrNotFound
	}
	if err != nil {
		return RSIProject{}, err
	}
	if row.Data.Owner != owner || row.Data.Project != project || row.Data.Revision != row.Revision {
		return RSIProject{}, errors.New("stored rsi project does not match its key or revision")
	}
	if err := row.Data.Research.Validate(); err != nil {
		return RSIProject{}, err
	}
	return row.Data, nil
}

func (p RSIProject) NotificationPolicy() (rsi.Policy, string, error) {
	if p.LivePolicyVersion == rsi.BaselineVersion {
		return rsi.DefaultPolicy(), rsi.BaselineVersion, nil
	}
	policy, exists := p.Research.VersionPolicy(p.LivePolicyVersion)
	if !exists || !p.approved(p.LivePolicyVersion) {
		return rsi.Policy{}, "", errors.New("live rsi policy has no approved release")
	}
	return policy, p.LivePolicyVersion, nil
}

func (p RSIProject) approved(version string) bool {
	return slices.ContainsFunc(p.Releases, func(release RSIRelease) bool {
		return release.To == version && release.Report != nil && release.Report.Approved
	})
}

func (p RSIProject) tested(family string) bool {
	return slices.ContainsFunc(p.Releases, func(release RSIRelease) bool {
		return release.Report != nil && slices.Contains(release.Report.Families, family)
	})
}

// saveRSIProject uses the read revision so concurrent training, feedback or
// deployment cannot overwrite each other's history.
func (l *Ledger) saveRSIProject(ctx context.Context, p RSIProject) (RSIProject, error) {
	prior := p.Revision
	p.Revision++
	row := rsiProjectRow{Owner: p.Owner, Project: p.Project, Revision: p.Revision, Data: p}
	result := l.db.WithContext(ctx).Model(&rsiProjectRow{}).
		Where("owner = ? AND project = ? AND revision = ?", p.Owner, p.Project, prior).
		Select("revision", "data").Updates(&row)
	if result.Error != nil {
		return RSIProject{}, result.Error
	}
	if result.RowsAffected != 1 {
		return RSIProject{}, fmt.Errorf("rsi project changed while this command was running: %w", delegation.ErrStale)
	}
	return p, nil
}

func (l *Ledger) ImproveRSI(ctx context.Context, owner, project string, dataset rsi.Dataset,
	options rsi.Options, includeFeedback bool) (rsi.Round, error) {
	p, err := l.RSIProject(ctx, owner, project)
	if err != nil {
		return rsi.Round{}, err
	}
	if includeFeedback {
		dataset.Development = slices.Clone(dataset.Development)
		for _, feedback := range p.Feedback {
			dataset.Development = append(dataset.Development, feedback.Example)
		}
	}
	families := slices.Clone(p.SelectionFamilies)
	for _, split := range [][]rsi.Example{dataset.Development, dataset.Validation} {
		for _, e := range split {
			if p.tested(e.Family) {
				return rsi.Round{}, fmt.Errorf("family %q was already used for live validation", e.Family)
			}
			families = append(families, e.Family)
		}
	}
	round, err := p.Research.Improve(dataset, options)
	if err != nil {
		return rsi.Round{}, err
	}
	slices.Sort(families)
	p.SelectionFamilies = slices.Compact(families)
	_, err = l.saveRSIProject(ctx, p)
	return round, err
}

func (l *Ledger) ActivateRSI(ctx context.Context, owner, project, version string, holdout []rsi.Example) (RSIRelease, error) {
	p, err := l.RSIProject(ctx, owner, project)
	if err != nil {
		return RSIRelease{}, err
	}
	candidate, ok := p.Research.VersionPolicy(version)
	if !ok {
		return RSIRelease{}, fmt.Errorf("unknown policy version %q", version)
	}
	baseline, _, err := p.NotificationPolicy()
	if err != nil {
		return RSIRelease{}, err
	}
	for _, e := range holdout {
		if slices.Contains(p.SelectionFamilies, e.Family) || p.tested(e.Family) {
			return RSIRelease{}, fmt.Errorf("holdout family %q was used for selection or a previous live check", e.Family)
		}
	}
	report, err := rsi.CheckActivation(candidate, baseline, holdout)
	if err != nil {
		return RSIRelease{}, err
	}
	release := RSIRelease{Operation: "activate", From: p.LivePolicyVersion, To: version, At: l.now(), Report: &report}
	p.Releases = append(p.Releases, release)
	if report.Approved {
		p.LivePolicyVersion = version
	}
	_, err = l.saveRSIProject(ctx, p)
	return release, err
}

func (l *Ledger) RollbackRSI(ctx context.Context, owner, project, policy, improver string, live bool) (RSIProject, error) {
	p, err := l.RSIProject(ctx, owner, project)
	if err != nil {
		return RSIProject{}, err
	}
	if live {
		if improver != "" || policy == "" {
			return RSIProject{}, errors.New("live rollback requires a policy version and takes no improver")
		}
		if policy != rsi.BaselineVersion && !p.approved(policy) {
			return RSIProject{}, fmt.Errorf("policy %q has no approved release", policy)
		}
		if p.LivePolicyVersion == policy {
			return p, nil
		}
		p.Releases = append(p.Releases, RSIRelease{
			Operation: "rollback", From: p.LivePolicyVersion, To: policy, At: l.now(),
		})
		p.LivePolicyVersion = policy
	} else {
		if policy == "" && improver == "" {
			return RSIProject{}, errors.New("research rollback requires a policy or improver version")
		}
		active := p.Research.Active
		if err := p.Research.Rollback(policy, improver); err != nil {
			return RSIProject{}, err
		}
		if p.Research.Active == active {
			return p, nil
		}
	}
	return l.saveRSIProject(ctx, p)
}

var validRSIPair = regexp.MustCompile(`^[a-zA-Z0-9][-a-zA-Z0-9._:/]{0,95}$`)

func (l *Ledger) RecordRSIFeedback(ctx context.Context, owner, project string, seq uint64,
	expected rsi.Disposition, pair string, anchor bool) (RSIFeedback, error) {
	if seq == 0 || (expected != rsi.Immediate && expected != rsi.Digest) {
		return RSIFeedback{}, errors.New("feedback requires an observation sequence and immediate or digest label")
	}
	if pair != "" && !validRSIPair.MatchString(pair) {
		return RSIFeedback{}, errors.New("feedback pair requires a short identifier")
	}
	p, err := l.RSIProject(ctx, owner, project)
	if err != nil {
		return RSIFeedback{}, err
	}
	for _, f := range p.Feedback {
		if f.ObservationSeq == seq {
			if f.Example.Expected != expected || f.Example.Pair != pair || f.Example.Feedback == anchor {
				return RSIFeedback{}, errors.New("observation already has a different reviewed label")
			}
			return f, nil
		}
	}

	var row trackingObservationRow
	err = l.db.WithContext(ctx).Take(&row, "owner = ? AND project = ? AND seq = ?", owner, project, seq).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RSIFeedback{}, delegation.ErrNotFound
	}
	if err != nil {
		return RSIFeedback{}, err
	}
	record := row.record()
	observed := rsi.Disposition(record.Decision.Disposition)
	if !record.PolicyUsed || record.Decision.Outcome != tracking.Applied ||
		(observed != rsi.Immediate && observed != rsi.Digest) {
		return RSIFeedback{}, errors.New("observation does not contain a notification policy decision")
	}
	group, err := json.Marshal(record.Observation.Key)
	if err != nil {
		return RSIFeedback{}, err
	}
	hash := sha256.Sum256(group)
	feedback := RSIFeedback{
		ObservationSeq: seq, PolicyVersion: record.PolicyVersion, Observed: observed, At: l.now(),
		Example: rsi.Example{
			ID: fmt.Sprintf("observation-%d", seq), Family: fmt.Sprintf("tracking-%x", hash[:12]),
			Pair: pair, Signal: TrackingSignal(record.Facts), Expected: expected, Feedback: !anchor,
		},
	}
	p.Feedback = append(p.Feedback, feedback)
	_, err = l.saveRSIProject(ctx, p)
	return feedback, err
}
