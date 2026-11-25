package rsi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

type PolicyVersion struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
	Policy Policy `json:"policy"`
}

type ImproverVersion struct {
	ID       string   `json:"id"`
	Parent   string   `json:"parent,omitempty"`
	Improver Improver `json:"improver"`
}

type Versions struct {
	Policy   string `json:"policy"`
	Improver string `json:"improver"`
}

type Rollback struct {
	AfterRound int      `json:"after_round"`
	From       Versions `json:"from"`
	To         Versions `json:"to"`
}

// State is the portable, versioned state of an improver and its promoted policies.
type State struct {
	Active    Versions          `json:"active"`
	Policies  []PolicyVersion   `json:"policies"`
	Improvers []ImproverVersion `json:"improvers"`
	Rounds    []Round           `json:"rounds,omitempty"`
	Rollbacks []Rollback        `json:"rollbacks,omitempty"`
}

func NewState(policy Policy) (State, error) {
	if err := policy.Validate(); err != nil {
		return State{}, err
	}
	var state State
	state.adoptPolicy(policy)
	state.adoptImprover(defaultImprover())
	return state, nil
}

func (s State) Policy() Policy {
	return s.policyVersion(s.Active.Policy).Policy.clone()
}

func (s State) VersionPolicy(id string) (Policy, bool) {
	v := s.policyVersion(id)
	return v.Policy.clone(), v.ID != ""
}

func (s State) Validate() error {
	policies, improvers := make(map[string]bool), make(map[string]bool)
	for _, v := range s.Policies {
		if err := v.Policy.Validate(); err != nil {
			return err
		}
		if !orderedVersion(policies, v.ID, v.Parent, "policy-"+fingerprint(v.withoutID())) {
			return fmt.Errorf("invalid policy lineage at %q", v.ID)
		}
		policies[v.ID] = true
	}
	for _, v := range s.Improvers {
		if err := v.Improver.validate(); err != nil {
			return err
		}
		if !orderedVersion(improvers, v.ID, v.Parent, "improver-"+fingerprint(v.withoutID())) {
			return fmt.Errorf("invalid improver lineage at %q", v.ID)
		}
		improvers[v.ID] = true
	}
	if !policies[s.Active.Policy] || !improvers[s.Active.Improver] {
		return errors.New("active policy and improver must refer to known versions")
	}
	return nil
}

func orderedVersion(known map[string]bool, id, parent, expected string) bool {
	return id == expected && !known[id] && ((parent == "" && len(known) == 0) || known[parent])
}

func (v PolicyVersion) withoutID() PolicyVersion {
	v.ID = ""
	return v
}

func (v ImproverVersion) withoutID() ImproverVersion {
	v.ID = ""
	return v
}

func (s *State) Rollback(policyVersion, improverVersion string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	from, to := s.Active, s.Active
	if policyVersion != "" {
		if s.policyVersion(policyVersion).ID == "" {
			return fmt.Errorf("unknown policy version %q", policyVersion)
		}
		to.Policy = policyVersion
	}
	if improverVersion != "" {
		if s.improverVersion(improverVersion).ID == "" {
			return fmt.Errorf("unknown improver version %q", improverVersion)
		}
		to.Improver = improverVersion
	}
	if to != from {
		s.Rollbacks = append(s.Rollbacks, Rollback{AfterRound: len(s.Rounds), From: from, To: to})
		s.Active = to
	}
	return nil
}

func (s *State) adoptPolicy(p Policy) {
	v := PolicyVersion{Parent: s.Active.Policy, Policy: p.clone()}
	v.ID = "policy-" + fingerprint(v)
	if s.policyVersion(v.ID).ID == "" {
		s.Policies = append(s.Policies, v)
	}
	s.Active.Policy = v.ID
}

func (s *State) adoptImprover(m Improver) {
	m.Operators = slices.Clone(m.Operators)
	v := ImproverVersion{Parent: s.Active.Improver, Improver: m}
	v.ID = "improver-" + fingerprint(v)
	if s.improverVersion(v.ID).ID == "" {
		s.Improvers = append(s.Improvers, v)
	}
	s.Active.Improver = v.ID
}

func (s State) policyVersion(id string) PolicyVersion {
	for _, v := range s.Policies {
		if v.ID == id {
			return v
		}
	}
	return PolicyVersion{}
}

func (s State) improverVersion(id string) ImproverVersion {
	for _, v := range s.Improvers {
		if v.ID == id {
			return v
		}
	}
	return ImproverVersion{}
}

func fingerprint(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:12])
}
