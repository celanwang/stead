// Package rsi improves bounded notification policies with reproducible evidence.
package rsi

import (
	"errors"
	"fmt"
	"slices"
)

type Signal struct {
	FirstSeen         bool `json:"first_seen"`
	Material          bool `json:"material"`
	Cosmetic          bool `json:"cosmetic"`
	ImpactsConclusion bool `json:"impacts_conclusion"`
	Withdrawn         bool `json:"withdrawn"`
}

type Disposition string

const (
	Immediate Disposition = "immediate"
	Digest    Disposition = "digest"
)

type Feature string

const (
	FirstSeen         Feature = "first_seen"
	Material          Feature = "material"
	Cosmetic          Feature = "cosmetic"
	ImpactsConclusion Feature = "impacts_conclusion"
	Withdrawn         Feature = "withdrawn"
)

var features = []Feature{FirstSeen, Material, Cosmetic, ImpactsConclusion, Withdrawn}

type Rule struct {
	Require []Feature   `json:"require"`
	Exclude []Feature   `json:"exclude,omitempty"`
	Decide  Disposition `json:"decide"`
}

// Policy evaluates the first matching rule and otherwise places the update in the digest.
type Policy struct {
	Rules []Rule `json:"rules"`
}

func DefaultPolicy() Policy {
	return Policy{Rules: []Rule{
		{Require: []Feature{Withdrawn}, Decide: Immediate},
		{Require: []Feature{ImpactsConclusion}, Decide: Immediate},
		{Require: []Feature{FirstSeen}, Decide: Immediate},
	}}
}

func (p Policy) Decide(s Signal) Disposition {
	i := p.match(s)
	if i < len(p.Rules) {
		return p.Rules[i].Decide
	}
	return Digest
}

func (p Policy) match(s Signal) int {
	for i, rule := range p.Rules {
		if rule.matches(s) {
			return i
		}
	}
	return len(p.Rules)
}

func (p Policy) Validate() error {
	for i, rule := range p.Rules {
		if len(rule.Require)+len(rule.Exclude) == 0 {
			return fmt.Errorf("rule %d requires a feature constraint", i+1)
		}
		if rule.Decide != Immediate && rule.Decide != Digest {
			return fmt.Errorf("rule %d requires a valid disposition", i+1)
		}
		seen := make(map[Feature]bool)
		for _, list := range [][]Feature{rule.Require, rule.Exclude} {
			for _, feature := range list {
				if !slices.Contains(features, feature) || seen[feature] {
					return fmt.Errorf("rule %d has an invalid or repeated feature %q", i+1, feature)
				}
				seen[feature] = true
			}
		}
	}
	return nil
}

func (r Rule) matches(s Signal) bool {
	for _, feature := range r.Require {
		if !s.has(feature) {
			return false
		}
	}
	for _, feature := range r.Exclude {
		if s.has(feature) {
			return false
		}
	}
	return true
}

func (s Signal) has(feature Feature) bool {
	switch feature {
	case FirstSeen:
		return s.FirstSeen
	case Material:
		return s.Material
	case Cosmetic:
		return s.Cosmetic
	case ImpactsConclusion:
		return s.ImpactsConclusion
	case Withdrawn:
		return s.Withdrawn
	default:
		return false
	}
}

func (s Signal) exactRule(decide Disposition) Rule {
	rule := Rule{Decide: decide}
	for _, feature := range features {
		if s.has(feature) {
			rule.Require = append(rule.Require, feature)
		} else {
			rule.Exclude = append(rule.Exclude, feature)
		}
	}
	return rule
}

func (p Policy) clone() Policy {
	q := Policy{Rules: slices.Clone(p.Rules)}
	for i := range q.Rules {
		q.Rules[i].Require = slices.Clone(q.Rules[i].Require)
		q.Rules[i].Exclude = slices.Clone(q.Rules[i].Exclude)
	}
	return q
}

func (p Policy) prepend(r Rule) Policy {
	q := Policy{Rules: []Rule{r}}
	for _, rule := range p.Rules {
		if !slices.Equal(rule.Require, r.Require) || !slices.Equal(rule.Exclude, r.Exclude) {
			q.Rules = append(q.Rules, rule)
		}
	}
	return q
}

var errInvalidDisposition = errors.New("expected disposition must be immediate or digest")
