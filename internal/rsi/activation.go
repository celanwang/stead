package rsi

import (
	"errors"
	"fmt"
	"slices"
)

const BaselineVersion = "baseline"

type ActivationReport struct {
	Approved       bool     `json:"approved"`
	HoldoutHash    string   `json:"holdout_hash"`
	Families       []string `json:"families"`
	Baseline       Score    `json:"baseline"`
	Candidate      Score    `json:"candidate"`
	NewErrors      []string `json:"new_errors,omitempty"`
	MissedCritical []Signal `json:"missed_critical,omitempty"`
}

// CheckActivation checks a candidate against the currently deployed policy on
// independently labeled cases, plus an invariant for conclusion impacts and
// withdrawals. Owner preferences and tracking modes are applied separately.
func CheckActivation(candidate, deployed Policy, holdout []Example) (ActivationReport, error) {
	if err := candidate.Validate(); err != nil {
		return ActivationReport{}, err
	}
	if err := deployed.Validate(); err != nil {
		return ActivationReport{}, err
	}
	ids, families := make(map[string]bool), make(map[string]bool)
	var hasAlert, hasDigest bool
	for _, e := range holdout {
		if e.ID == "" || e.Family == "" || ids[e.ID] || e.Feedback {
			return ActivationReport{}, fmt.Errorf("activation example %q needs a unique ID and family with no feedback flag", e.ID)
		}
		if e.Expected != Immediate && e.Expected != Digest {
			return ActivationReport{}, fmt.Errorf("activation example %q: %w", e.ID, errInvalidDisposition)
		}
		hasAlert = hasAlert || e.Expected == Immediate
		hasDigest = hasDigest || e.Expected == Digest
		ids[e.ID], families[e.Family] = true, true
	}
	if !hasAlert || !hasDigest || len(families) < 2 {
		return ActivationReport{}, errors.New("activation requires both expected dispositions and at least two independent holdout families")
	}

	report := ActivationReport{
		HoldoutHash: fingerprint(holdout),
		Baseline:    Evaluate(deployed, holdout),
		Candidate:   Evaluate(candidate, holdout),
	}
	for family := range families {
		report.Families = append(report.Families, family)
	}
	slices.Sort(report.Families)
	for _, id := range report.Candidate.IncorrectCases {
		if !slices.Contains(report.Baseline.IncorrectCases, id) {
			report.NewErrors = append(report.NewErrors, id)
		}
	}
	for mask := 0; mask < 32; mask++ {
		signal := Signal{
			FirstSeen: mask&1 != 0, Material: mask&2 != 0, Cosmetic: mask&4 != 0,
			ImpactsConclusion: mask&8 != 0, Withdrawn: mask&16 != 0,
		}
		if (signal.ImpactsConclusion || signal.Withdrawn) && candidate.Decide(signal) != Immediate {
			report.MissedCritical = append(report.MissedCritical, signal)
		}
	}
	report.Approved = len(report.NewErrors) == 0 && len(report.MissedCritical) == 0
	return report, nil
}
