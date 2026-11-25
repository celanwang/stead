package rsi

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
)

type Operator string

const (
	RetuneRule       Operator = "change_matched_rule"
	GuardedException Operator = "derive_paired_exception"
	ExactException   Operator = "add_exact_exception"
	FeatureOverride  Operator = "search_feature_overrides"
)

// Improver controls task candidate order and whether paired outcomes guide selection.
type Improver struct {
	Operators    []Operator `json:"operators"`
	RequirePairs bool       `json:"require_pairs"`
}

func defaultImprover() Improver {
	return Improver{Operators: []Operator{RetuneRule, GuardedException, ExactException, FeatureOverride}}
}

func (m Improver) validate() error {
	expected := defaultImprover().Operators
	if len(m.Operators) != len(expected) {
		return errors.New("improver must order each available operator")
	}
	seen := make(map[Operator]bool)
	for _, op := range m.Operators {
		if !slices.Contains(expected, op) || seen[op] {
			return fmt.Errorf("invalid or repeated improver operator %q", op)
		}
		seen[op] = true
	}
	return nil
}

type candidate struct {
	policy  Policy
	summary Candidate
}

type Candidate struct {
	ID               string   `json:"id"`
	Operator         Operator `json:"operator"`
	Development      Score    `json:"development"`
	Validation       Score    `json:"validation"`
	TargetCorrect    bool     `json:"target_correct"`
	PairedCorrect    int      `json:"paired_correct"`
	PairedTotal      int      `json:"paired_total"`
	RejectedByChecks []string `json:"rejected_by_checks,omitempty"`
}

func generateCandidates(p Policy, target Example, examples []Example, m Improver) []candidate {
	var out []candidate
	seen := map[string]bool{fingerprint(p): true}
	add := func(next Policy, op Operator) {
		id := fingerprint(next)
		if !seen[id] {
			out = append(out, candidate{policy: next, summary: Candidate{ID: id, Operator: op}})
			seen[id] = true
		}
	}
	for _, op := range m.Operators {
		switch op {
		case RetuneRule:
			if i := p.match(target.Signal); i < len(p.Rules) {
				next := p.clone()
				next.Rules[i].Decide = target.Expected
				add(next, op)
			}
		case GuardedException:
			if rule, ok := pairedRule(target, examples); ok {
				add(p.prepend(rule), op)
			}
			if at := p.match(target.Signal); at < len(p.Rules) && target.Pair != "" {
				next := p.clone()
				next.Rules[at].Decide = target.Expected
				for _, counter := range examples {
					if counter.Pair == target.Pair && counter.Expected != target.Expected {
						if guard, ok := pairedRule(counter, examples); ok {
							next = next.prepend(guard)
						}
					}
				}
				add(next, op)
			}
		case ExactException:
			add(p.prepend(target.Signal.exactRule(target.Expected)), op)
		case FeatureOverride:
			for mask := 1; mask < 1<<len(features); mask++ {
				rule := Rule{Decide: target.Expected}
				for bit, feature := range features {
					if mask&(1<<bit) == 0 {
						continue
					}
					if target.Signal.has(feature) {
						rule.Require = append(rule.Require, feature)
					} else {
						rule.Exclude = append(rule.Exclude, feature)
					}
				}
				add(p.prepend(rule), op)
			}
		}
	}
	return out
}

func pairedRule(target Example, examples []Example) (Rule, bool) {
	if target.Pair == "" {
		return Rule{}, false
	}
	rule := Rule{Decide: target.Expected}
	hasDifference := false
	for _, feature := range features {
		differs := slices.ContainsFunc(examples, func(other Example) bool {
			return other.Pair == target.Pair && other.Expected != target.Expected &&
				other.Signal.has(feature) != target.Signal.has(feature)
		})
		hasDifference = hasDifference || differs
		if target.Signal.has(feature) {
			rule.Require = append(rule.Require, feature)
		} else if differs {
			rule.Exclude = append(rule.Exclude, feature)
		}
	}
	return rule, hasDifference
}

func assess(p Policy, result Candidate, incumbent *Score, dataset Dataset, target Example) Candidate {
	result.Development = Evaluate(p, dataset.Development)
	result.Validation = Evaluate(p, dataset.Validation)
	result.TargetCorrect = !slices.Contains(result.Development.IncorrectCases, target.ID)
	for _, e := range dataset.Development {
		if target.Pair != "" && e.ID != target.ID && e.Pair == target.Pair {
			result.PairedTotal++
			if !slices.Contains(result.Development.IncorrectCases, e.ID) {
				result.PairedCorrect++
			}
		}
	}
	// A candidate may fix validation errors. Passing outcomes cannot regress.
	for _, id := range result.Validation.IncorrectCases {
		if incumbent != nil && !slices.Contains(incumbent.IncorrectCases, id) {
			result.RejectedByChecks = append(result.RejectedByChecks, id)
		}
	}
	return result
}

func better(next, current Candidate, withPairs bool) bool {
	if len(next.RejectedByChecks) != 0 {
		return false
	}
	if next.TargetCorrect != current.TargetCorrect {
		return next.TargetCorrect
	}
	if withPairs && next.PairedCorrect != current.PairedCorrect {
		return next.PairedCorrect > current.PairedCorrect
	}
	return next.Validation.errors() < current.Validation.errors()
}

func betterByScore(next, current Candidate) bool {
	if len(next.RejectedByChecks) != 0 {
		return false
	}
	if next.Development.errors() != current.Development.errors() {
		return next.Development.errors() < current.Development.errors()
	}
	if next.Development.MissedAlerts != current.Development.MissedAlerts {
		return next.Development.MissedAlerts < current.Development.MissedAlerts
	}
	return next.Validation.errors() < current.Validation.errors()
}

type trial struct {
	winner     candidate
	candidates []Candidate
}

func runTrial(incumbent candidate, m Improver, dataset Dataset, target Example, budget int, simple *rand.Rand) trial {
	choices := generateCandidates(incumbent.policy, target, dataset.Development, m)
	if simple != nil {
		simple.Shuffle(len(choices), func(i, j int) { choices[i], choices[j] = choices[j], choices[i] })
	}

	out := trial{winner: incumbent}
	for _, choice := range choices[:min(len(choices), budget)] {
		choice.summary = assess(choice.policy, choice.summary, &incumbent.summary.Validation, dataset, target)
		out.candidates = append(out.candidates, choice.summary)
		wins := better(choice.summary, out.winner.summary, m.RequirePairs)
		if simple != nil {
			wins = betterByScore(choice.summary, out.winner.summary)
		}
		if wins {
			out.winner = choice
		}
	}
	return out
}

type methodEdit struct {
	operation string
	method    Improver
}

func (m Improver) editItself() []methodEdit {
	// A paired method next spends its trials on changes to proposal order.
	var edits []methodEdit
	if !m.RequirePairs {
		mutant := m
		mutant.RequirePairs = true
		edits = append(edits, methodEdit{operation: "retain_paired_outcomes", method: mutant})
	}
	for _, op := range []Operator{GuardedException, ExactException, FeatureOverride} {
		if m.Operators[0] != op {
			order := []Operator{op}
			for _, entry := range m.Operators {
				if entry != op {
					order = append(order, entry)
				}
			}
			edits = append(edits, methodEdit{
				operation: "try_" + string(op) + "_first",
				method:    Improver{Operators: order, RequirePairs: m.RequirePairs},
			})
		}
	}
	return edits
}
