package rsi

import (
	"errors"
	"fmt"
)

// Example is a labeled decision from an execution trace or a test fixture.
// Examples in a Pair present similar signals that require different decisions.
type Example struct {
	ID       string      `json:"id"`
	Family   string      `json:"family"`
	Pair     string      `json:"pair,omitempty"`
	Signal   Signal      `json:"signal"`
	Expected Disposition `json:"expected"`
	Feedback bool        `json:"feedback,omitempty"`
}

// Dataset puts corrections and paired examples in Development and independent
// promotion checks in Validation. Compare reads Holdout only after all runs.
type Dataset struct {
	Development []Example `json:"development"`
	Validation  []Example `json:"validation"`
	Holdout     []Example `json:"holdout,omitempty"`
}

func (d Dataset) validate() error {
	if len(d.Development) == 0 || len(d.Validation) == 0 {
		return errors.New("development and validation examples are required")
	}
	ids := make(map[string]bool)
	families, pairs := make(map[string]int), make(map[string]int)
	for split, examples := range [][]Example{d.Development, d.Validation, d.Holdout} {
		for _, e := range examples {
			if e.ID == "" || e.Family == "" || ids[e.ID] {
				return fmt.Errorf("example requires a unique ID and family: %q", e.ID)
			}
			if e.Expected != Immediate && e.Expected != Digest {
				return fmt.Errorf("example %q: %w", e.ID, errInvalidDisposition)
			}
			if seen, ok := families[e.Family]; ok && seen != split {
				return fmt.Errorf("example family %q crosses dataset partitions", e.Family)
			}
			if e.Pair != "" {
				if seen, ok := pairs[e.Pair]; ok && seen != split {
					return fmt.Errorf("example pair %q crosses dataset partitions", e.Pair)
				}
				pairs[e.Pair] = split
			}
			families[e.Family], ids[e.ID] = split, true
		}
	}
	for _, e := range d.Development {
		if e.Feedback {
			return nil
		}
	}
	return errors.New("development requires at least one example marked as feedback")
}

func (d Dataset) target(round int) Example {
	var corrections []Example
	for _, e := range d.Development {
		if e.Feedback {
			corrections = append(corrections, e)
		}
	}
	return corrections[(round-1)%len(corrections)]
}

type Score struct {
	Examples       int      `json:"examples"`
	FalseAlarms    int      `json:"false_alarms"`
	MissedAlerts   int      `json:"missed_alerts"`
	IncorrectCases []string `json:"incorrect_cases,omitempty"`
}

func (s Score) errors() int { return s.FalseAlarms + s.MissedAlerts }

func Evaluate(p Policy, examples []Example) Score {
	score := Score{Examples: len(examples)}
	for _, e := range examples {
		if p.Decide(e.Signal) != e.Expected {
			score.IncorrectCases = append(score.IncorrectCases, e.ID)
			if e.Expected == Immediate {
				score.MissedAlerts++
			} else {
				score.FalseAlarms++
			}
		}
	}
	return score
}
