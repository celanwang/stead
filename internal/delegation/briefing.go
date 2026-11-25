package delegation

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	Review EventKind = "review"
	Amend  EventKind = "amend"
)

type Briefing struct {
	Project    string
	Reviewer   string
	Audience   string
	Format     string
	Fallback   bool
	AskLead    time.Duration
	Items      []BriefingItem
	Draft      string
	ArtifactID string
	AskReason  string
}

type BriefingItem struct {
	ID          string
	TakenOver   bool
	Rendered    string
	Fingerprint string
	Reviewed    string
	Renders     int
}

type Fact struct {
	ID          string
	Revision    uint64
	Fingerprint string
	Shareable   bool
	Value       string
}

type Audience struct {
	Target   string
	Revision uint64
	Class    string
}

type DeliveryGuard struct {
	DelegationID string
	Owner        string
	Project      string
	ArtifactID   string
	Audience     Audience
	Facts        []Fact
	Deadline     time.Time
}

type DeliveryProof struct {
	ID         string
	Target     string
	ArtifactID string
	Audience   Audience
	At         time.Time
}

type BriefingObservation struct {
	Facts      []Fact
	Audience   Audience
	Deliveries []DeliveryProof
	Poll       time.Duration
}

type ReviewNote struct {
	Source      string
	Fingerprint string
}

type Amendment struct {
	Target   string
	Format   string
	TakeOver string
}

func NewBriefing(project, reviewer, audience, format string, sources []string, fallback bool, askLead time.Duration) (*Briefing, error) {
	b := &Briefing{
		Project: project, Reviewer: reviewer, Audience: audience, Format: format, Fallback: fallback, AskLead: askLead,
	}
	for _, source := range sources {
		b.Items = append(b.Items, BriefingItem{ID: source})
	}
	if err := b.validate(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b Briefing) validate() error {
	if b.Project == "" || len(b.Project) > 191 || b.Reviewer == "" || b.Audience == "" ||
		!validFormat(b.Format) || b.AskLead <= 0 || len(b.Items) == 0 || len(b.Items) > 32 {
		return errors.New("briefing requires a project, reviewer, audience, format, ask lead and 1–32 sources")
	}
	seen := make(map[string]bool, len(b.Items))
	for _, item := range b.Items {
		if item.ID == "" || len(item.ID) > 191 || seen[item.ID] {
			return errors.New("briefing sources must be distinct, non-empty identifiers")
		}
		seen[item.ID] = true
	}
	return nil
}

func validFormat(format string) bool { return format == "bullets" || format == "plain" }

func (b Briefing) validAmendment(a Amendment) bool {
	return (a.Target != "" || a.Format != "" || a.TakeOver != "") &&
		(a.Format == "" || validFormat(a.Format)) &&
		(a.TakeOver == "" || slices.ContainsFunc(b.Items, func(i BriefingItem) bool { return i.ID == a.TakeOver }))
}

func (b Briefing) hasSource(id string) bool {
	return slices.ContainsFunc(b.Items, func(i BriefingItem) bool { return i.ID == id && !i.TakenOver })
}

// Fingerprint binds approval to an observed value. Length prefixes keep tuples unambiguous.
func Fingerprint(values ...string) string {
	h := sha256.New()
	for _, value := range values {
		fmt.Fprintf(h, "%d:", len(value))
		h.Write([]byte(value))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (g *DeliveryGuard) equal(other *DeliveryGuard) bool {
	if g == nil || other == nil {
		return g == other
	}
	return g.DelegationID == other.DelegationID && g.Owner == other.Owner && g.Project == other.Project &&
		g.ArtifactID == other.ArtifactID && g.Audience == other.Audience && slices.Equal(g.Facts, other.Facts) &&
		g.Deadline.Equal(other.Deadline)
}

func resolveBriefing(s Snapshot, p Proposal, at time.Time) Decision {
	d, e := s.Delegation, s.Event
	b := *d.Briefing
	b.Items = slices.Clone(b.Items)
	target := d.Request.Target
	out := Decision{Briefing: &b}

	if e != nil && e.Kind == Amend {
		a := e.Amendment
		if a.Target != "" {
			target = a.Target
		}
		if a.Format != "" {
			b.Format = a.Format
		}
		for i := range b.Items {
			if b.Items[i].ID == a.TakeOver {
				b.Items[i].TakenOver = true
			}
		}
	}

	world := p.Briefing
	if world == nil {
		if !at.Before(d.Deadline) {
			out.Result = briefingDeadline(b, at)
		} else {
			out.Wait = d.Wait
		}
		return out
	}
	var lines []string
	var guardFacts []Fact
	active, allReviewed, hasOutput := 0, true, false
	for i := range b.Items {
		item := &b.Items[i]
		if item.TakenOver {
			continue
		}
		active++
		index := slices.IndexFunc(world.Facts, func(f Fact) bool { return f.ID == item.ID })
		if index < 0 {
			item.Rendered, item.Fingerprint, item.Reviewed = "", "", ""
			allReviewed = false
			continue
		}
		fact := world.Facts[index]
		if item.Fingerprint != fact.Fingerprint {
			item.Fingerprint, item.Reviewed = fact.Fingerprint, ""
			item.Rendered = ""
			item.Renders++
			if fact.Shareable {
				item.Rendered = fmt.Sprintf("%s: %s", item.ID, fact.Value)
			}
		}
		if e != nil && e.Kind == Review && e.Review != nil &&
			e.Review.Source == item.ID && e.Review.Fingerprint == fact.Fingerprint {
			item.Reviewed = fact.Fingerprint
		}
		allReviewed = allReviewed && item.Reviewed == fact.Fingerprint
		if item.Rendered != "" {
			line := item.Rendered
			if b.Format == "bullets" {
				line = "- " + line
			}
			lines = append(lines, line)
			hasOutput = true
		}
		fact.Value = ""
		guardFacts = append(guardFacts, fact)
	}

	b.Draft, b.ArtifactID, b.AskReason = strings.Join(lines, "\n"), "", ""
	if b.Draft != "" {
		b.ArtifactID = Fingerprint(b.Draft)
	}
	scope := Scope{Target: target}
	if active == 0 {
		out.Result = &Result{Reason: "owner took over all briefing items", At: at}
		return out
	}

	usableAudience := world.Audience.Target == target && world.Audience.Class == b.Audience && world.Audience.Revision != 0
	if allReviewed && hasOutput && usableAudience {
		scope.Content = b.Draft
		scope.Guard = &DeliveryGuard{
			DelegationID: d.ID, Owner: d.Owner, Project: b.Project, ArtifactID: b.ArtifactID,
			Audience: world.Audience, Facts: guardFacts, Deadline: d.Deadline,
		}
		if proof := matchingDelivery(*scope.Guard, world.Deliveries, d.Deadline); proof != nil {
			out.Result = &Result{Complete: true, Reason: "verified briefing delivery",
				Evidence: proof.ID, Draft: b.Draft, ArtifactID: b.ArtifactID, At: at}
			return out
		}
	}
	if !at.Before(d.Deadline) {
		out.Result = briefingDeadline(b, at)
		return out
	}
	out.Prepared = &scope
	unchanged := d.Request.equal(scope)
	out.InvalidateGrants = !unchanged || (e != nil && e.BlocksClaim(d) && e.Kind == Input)

	if scope.Guard != nil {
		if unchanged && !out.InvalidateGrants && d.Wait.Matches(e) &&
			e.Kind == Approval && e.Grant != nil && e.Grant.Scope.equal(scope) && at.Before(e.Grant.ExpiresAt) {
			out.Grant = &Grant{ID: e.ID, DelegationID: d.ID, Authorizer: e.Sender, SourceEventID: e.ID, Original: e.Raw, Terms: *e.Grant}
			a := Action{ID: fmt.Sprintf("%s-%d", d.ID, d.Version), DelegationID: d.ID, GrantID: e.ID, Scope: scope, State: Pending}
			if !duplicateEffect(s.Actions, a) {
				out.Action = &a
			}
		}
		if out.Action == nil && !hasOpenEffect(s.Actions, scope) {
			b.AskReason = "Approve this document for the currently verified audience"
		}
	} else if !b.Fallback && !at.Before(d.Deadline.Add(-b.AskLead)) {
		b.AskReason = "Review or eligible audience is missing before the deadline"
	}
	until := at.Add(world.Poll)
	if world.Poll <= 0 || until.After(d.Deadline) {
		until = d.Deadline
	}
	askAt := d.Deadline.Add(-b.AskLead)
	if !b.Fallback && at.Before(askAt) && askAt.Before(until) {
		until = askAt
	}
	if b.AskReason != "" && scope.Guard != nil {
		correlation := fmt.Sprintf("%s/%d", d.ID, d.Version)
		if unchanged && d.Wait != nil && d.Wait.Kind == Approval && !out.InvalidateGrants &&
			(e == nil || e.Kind != Revoke) {
			correlation = d.Wait.Correlation
		}
		out.Wait = &Wait{Kind: Approval, Sender: d.Owner, Correlation: correlation, Until: until, MatchUntil: d.Deadline}
	} else {
		out.Wait = &Wait{Kind: Review, Sender: b.Reviewer, Correlation: d.ID, Until: until}
	}
	return out
}

func briefingDeadline(b Briefing, at time.Time) *Result {
	result := &Result{Reason: "delegation deadline reached", At: at}
	if b.Fallback && b.Draft != "" {
		result.Reason = "prepared draft returned at deadline"
		result.Draft, result.ArtifactID = b.Draft, b.ArtifactID
	}
	return result
}

func matchingDelivery(g DeliveryGuard, proofs []DeliveryProof, deadline time.Time) *DeliveryProof {
	for i := range proofs {
		p := &proofs[i]
		if p.Target == g.Audience.Target && p.Audience == g.Audience && p.ArtifactID == g.ArtifactID && p.At.Before(deadline) {
			return p
		}
	}
	return nil
}
