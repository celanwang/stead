package rsi

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
)

const (
	defaultBudget = 10
	probeWidth    = 2
	metaEdits     = 2
)

type Mode string

const (
	Frozen    Mode = "frozen_improver"
	Simple    Mode = "simple_search"
	Recursive Mode = "recursive_improver"
)

type Options struct {
	Rounds          int    `json:"rounds"`
	CandidateBudget int    `json:"candidate_budget"`
	Seed            uint64 `json:"seed"`
	TargetID        string `json:"target_id,omitempty"`
}

func (o Options) withDefaults() (Options, error) {
	if o.Rounds == 0 {
		o.Rounds = 3
	}
	if o.CandidateBudget == 0 {
		o.CandidateBudget = defaultBudget
	}
	if o.Rounds < 1 || o.Rounds > 64 {
		return o, errors.New("rounds must be between 1 and 64")
	}
	if o.CandidateBudget < 8 || o.CandidateBudget > 24 {
		return o, errors.New("candidate budget must be between 8 and 24")
	}
	return o, nil
}

type MethodTrial struct {
	Operation  string      `json:"operation"`
	Method     Improver    `json:"method"`
	Candidates []Candidate `json:"candidates"`
	Selected   Candidate   `json:"selected"`
}

type Counterfactual struct {
	ImproverVersion  string    `json:"improver_version"`
	Selected         Candidate `json:"selected"`
	ChangedSelection bool      `json:"changed_selection"`
	Evaluations      int       `json:"audit_candidate_evaluations"`
}

type Round struct {
	Number             int             `json:"number"`
	Mode               Mode            `json:"mode"`
	Loaded             Versions        `json:"loaded"`
	ActiveAfter        Versions        `json:"active_after"`
	DatasetFingerprint string          `json:"selection_dataset_fingerprint"`
	Target             Example         `json:"target"`
	Baseline           Candidate       `json:"baseline"`
	Candidates         []Candidate     `json:"candidates"`
	Selected           Candidate       `json:"selected"`
	ProbeTrials        []MethodTrial   `json:"probe_trials,omitempty"`
	SelectedMethodEdit string          `json:"selected_method_edit,omitempty"`
	ParentComparison   *Counterfactual `json:"parent_comparison,omitempty"`
	CandidateBudget    int             `json:"candidate_budget"`
	Evaluations        int             `json:"candidate_evaluations"`
	CaseEvaluations    int             `json:"candidate_case_evaluations"`
}

// Improve performs one recursive round. Score holdout examples after choosing
// versions, with Evaluate or Compare.
func (s *State) Improve(d Dataset, o Options) (Round, error) {
	if err := s.Validate(); err != nil {
		return Round{}, err
	}
	if err := d.validate(); err != nil {
		return Round{}, err
	}
	options, err := o.withDefaults()
	if err != nil {
		return Round{}, err
	}
	if options.TargetID != "" && !slices.ContainsFunc(d.Development, func(e Example) bool {
		return e.ID == options.TargetID && e.Feedback
	}) {
		return Round{}, fmt.Errorf("no development correction has ID %q", options.TargetID)
	}
	d.Holdout = nil
	return s.step(d, options, Recursive), nil
}

func (s *State) step(d Dataset, o Options, mode Mode) Round {
	n := len(s.Rounds) + 1
	current := s.improverVersion(s.Active.Improver)
	round := Round{
		Number: n, Mode: mode, Loaded: s.Active, DatasetFingerprint: fingerprint(d),
		Target: d.target(n), CandidateBudget: o.CandidateBudget,
	}
	if o.TargetID != "" {
		for _, e := range d.Development {
			if e.ID == o.TargetID {
				round.Target = e
				break
			}
		}
	}
	p := s.Policy()
	base := candidate{policy: p, summary: assess(p, Candidate{ID: fingerprint(p)}, nil, d, round.Target)}
	round.Baseline = base.summary

	budget := o.CandidateBudget
	if mode == Recursive {
		budget -= (metaEdits + 1) * probeWidth
	}
	var random *rand.Rand
	if mode == Simple {
		random = rand.New(rand.NewPCG(o.Seed, uint64(n)))
	}
	task := runTrial(base, current.Improver, d, round.Target, budget, random)
	round.Candidates, round.Selected = task.candidates, task.winner.summary
	round.Evaluations = len(task.candidates)

	if mode == Recursive {
		if current.Parent != "" {
			prior := s.improverVersion(current.Parent)
			control := runTrial(base, prior.Improver, d, round.Target, budget, nil)
			round.ParentComparison = &Counterfactual{
				ImproverVersion: prior.ID, Selected: control.winner.summary,
				ChangedSelection: task.winner.summary.ID != control.winner.summary.ID,
				Evaluations:      len(control.candidates),
			}
		}
		s.experimentWithImprover(&round, base, current.Improver, d)
	}
	if task.winner.summary.ID != base.summary.ID {
		s.adoptPolicy(task.winner.policy)
	}
	round.ActiveAfter = s.Active
	round.CaseEvaluations = round.Evaluations * (len(d.Development) + len(d.Validation))
	s.Rounds = append(s.Rounds, round)
	return round
}

func (s *State) experimentWithImprover(round *Round, base candidate, current Improver, d Dataset) {
	tests := append([]methodEdit{{operation: "keep_current_improver", method: current}}, current.editItself()[:metaEdits]...)
	best := 0
	for i, test := range tests {
		t := runTrial(base, test.method, d, round.Target, probeWidth, nil)
		round.ProbeTrials = append(round.ProbeTrials, MethodTrial{
			Operation: test.operation, Method: test.method, Candidates: t.candidates, Selected: t.winner.summary,
		})
		round.Evaluations += len(t.candidates)
		// The promotion metric is fixed. A proposed method controls which
		// candidate it selects, while the experiment checks paired outcomes.
		if better(t.winner.summary, round.ProbeTrials[best].Selected, true) {
			best = i
		}
	}
	if best != 0 {
		selected := round.ProbeTrials[best]
		s.adoptImprover(selected.Method)
		round.SelectedMethodEdit = selected.Operation
	}
}

type Run struct {
	Mode            Mode  `json:"mode"`
	Evaluations     int   `json:"candidate_evaluations"`
	CaseEvaluations int   `json:"candidate_case_evaluations"`
	Holdout         Score `json:"holdout"`
	State           State `json:"state"`
}

type Comparison struct {
	Options Options `json:"options"`
	Before  Score   `json:"holdout_before"`
	Runs    []Run   `json:"runs"`
}

// Compare starts each mode at the same policy with equal candidate budgets.
// Parent replays are reported separately and never feed a promotion.
func Compare(d Dataset, initial Policy, o Options) (Comparison, error) {
	if err := d.validate(); err != nil {
		return Comparison{}, err
	}
	if len(d.Holdout) == 0 {
		return Comparison{}, errors.New("comparison requires a family-isolated holdout")
	}
	if o.TargetID != "" {
		return Comparison{}, errors.New("compare rotates through all development corrections")
	}
	if err := initial.Validate(); err != nil {
		return Comparison{}, err
	}
	options, err := o.withDefaults()
	if err != nil {
		return Comparison{}, err
	}

	holdout := d.Holdout
	d.Holdout = nil
	out := Comparison{Options: options}
	for _, mode := range []Mode{Frozen, Simple, Recursive} {
		s, err := NewState(initial)
		if err != nil {
			return Comparison{}, err
		}
		run := Run{Mode: mode}
		for range options.Rounds {
			round := s.step(d, options, mode)
			run.Evaluations += round.Evaluations
			run.CaseEvaluations += round.CaseEvaluations
		}
		run.State = s
		out.Runs = append(out.Runs, run)
	}
	out.Before = Evaluate(initial, holdout)
	for i := range out.Runs {
		out.Runs[i].Holdout = Evaluate(out.Runs[i].State.Policy(), holdout)
	}
	return out, nil
}
