package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
)

type submission struct {
	Goal       string           `json:"goal"`
	Deadline   time.Time        `json:"deadline"`
	Scheduling *scheduleRequest `json:"scheduling"`
	Briefing   *briefingRequest `json:"briefing"`
}

type scheduleRequest struct {
	Target    string            `json:"target"`
	Content   string            `json:"content"`
	Resources []resourceRequest `json:"resources"`
}

type resourceRequest struct {
	Key   string    `json:"key"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type briefingRequest struct {
	Project  string   `json:"project"`
	Reviewer string   `json:"reviewer"`
	Target   string   `json:"target"`
	Audience string   `json:"audience"`
	Sources  []string `json:"sources"`
	Format   string   `json:"format"`
	Fallback *bool    `json:"fallback"`
}

func (a *API) submit(w http.ResponseWriter, r *http.Request, owner string) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 {
		fail(w, http.StatusBadRequest, "Idempotency-Key must contain 1–128 bytes")
		return
	}
	var body submission
	if !decode(w, r, &body) {
		return
	}
	if (body.Scheduling == nil) == (body.Briefing == nil) || strings.TrimSpace(body.Goal) == "" ||
		body.Deadline.IsZero() || body.Deadline.Year() < 1000 || body.Deadline.Year() > 9999 {
		fail(w, http.StatusUnprocessableEntity, "provide a goal, a deadline, and one delegation type")
		return
	}
	raw, err := json.Marshal(body)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "delegation must be encodable")
		return
	}
	d := delegation.Delegation{
		ID: "api-" + delegation.Fingerprint(owner, key)[:32], Owner: owner, Goal: body.Goal,
		Sources: []string{"local"}, Deadline: body.Deadline, SubmissionHash: delegation.Fingerprint(string(raw)),
	}
	if s := body.Scheduling; s != nil {
		d.Request = delegation.Scope{Target: s.Target, Content: s.Content}
		for _, resource := range s.Resources {
			d.Request.Resources = append(d.Request.Resources, delegation.Resource{Key: resource.Key, Start: resource.Start, End: resource.End})
		}
	}
	if b := body.Briefing; b != nil {
		format := b.Format
		if format == "" {
			format = "bullets"
		}
		fallback := b.Fallback == nil || *b.Fallback
		d.Briefing, err = delegation.NewBriefing(b.Project, b.Reviewer, b.Audience, format, b.Sources, fallback, 15*time.Minute)
		if err != nil {
			fail(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		d.Request.Target = b.Target
	}
	if err := delegation.ValidateNew(d, time.Time{}); err != nil {
		fail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	d, created, err := a.store.Submit(r.Context(), d)
	switch {
	case errors.Is(err, ledger.ErrSubmissionConflict):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, ledger.ErrSubmissionDeadline):
		fail(w, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		internal(w, err)
	case created:
		w.Header().Set("Location", "/v1/delegations/"+d.ID)
		write(w, http.StatusCreated, view(d))
	default:
		write(w, http.StatusOK, view(d))
	}
}

type delegationView struct {
	Delegation       delegation.Delegation `json:"delegation"`
	ScopeFingerprint string                `json:"scope_fingerprint"`
}

func view(d delegation.Delegation) delegationView {
	raw, _ := json.Marshal(d.Request)
	return delegationView{Delegation: d, ScopeFingerprint: delegation.Fingerprint(string(raw))}
}

func (a *API) list(w http.ResponseWriter, r *http.Request, owner string) {
	after := r.URL.Query().Get("after")
	if len(after) > 128 {
		fail(w, http.StatusBadRequest, "invalid cursor")
		return
	}
	const limit = 50
	items, err := a.store.ListPage(r.Context(), owner, after, limit+1)
	if err != nil {
		internal(w, err)
		return
	}
	var next string
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	out := make([]delegationView, 0, len(items))
	for _, d := range items {
		out = append(out, view(d))
	}
	write(w, http.StatusOK, struct {
		Items []delegationView `json:"items"`
		Next  string           `json:"next,omitempty"`
	}{out, next})
}

func (a *API) status(w http.ResponseWriter, r *http.Request, owner string) {
	id := r.PathValue("id")
	d, err := a.store.Delegation(r.Context(), id)
	if err != nil {
		internal(w, err)
		return
	}
	if d.Owner != owner {
		internal(w, delegation.ErrNotFound)
		return
	}
	state, err := a.store.Status(r.Context(), id)
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, struct {
		State            ledger.Status `json:"state"`
		ScopeFingerprint string        `json:"scope_fingerprint"`
	}{state, view(state.Delegation).ScopeFingerprint})
}

type eventRequest struct {
	ID               string               `json:"id"`
	Kind             delegation.EventKind `json:"kind"`
	Text             string               `json:"text"`
	Correlation      string               `json:"correlation"`
	ScopeFingerprint string               `json:"scope_fingerprint"`
	ExpiresAt        time.Time            `json:"expires_at"`
	RevokeGrantID    string               `json:"grant_id"`
	Review           *reviewRequest       `json:"review"`
	Amendment        *amendmentRequest    `json:"amendment"`
}

type reviewRequest struct {
	Source      string `json:"source"`
	Fingerprint string `json:"fingerprint"`
}

type amendmentRequest struct {
	Target   string `json:"target"`
	Format   string `json:"format"`
	TakeOver string `json:"take_over"`
}

func (a *API) receive(w http.ResponseWriter, r *http.Request, principal string) {
	d, err := a.store.Delegation(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	reviewer := d.Briefing != nil && d.Briefing.Reviewer == principal
	if principal != d.Owner && !reviewer {
		internal(w, delegation.ErrNotFound)
		return
	}
	var body eventRequest
	if !decode(w, r, &body) {
		return
	}
	if principal != d.Owner && body.Kind != delegation.Review {
		internal(w, delegation.ErrNotFound)
		return
	}
	if body.ID == "" || len(body.ID) > 128 || strings.TrimSpace(body.Text) == "" {
		fail(w, http.StatusUnprocessableEntity, "event requires a stable ID and original text")
		return
	}
	e := delegation.Event{
		Source: "local", ExternalID: "api-" + delegation.Fingerprint(d.ID, principal, body.ID),
		DelegationID: d.ID, Sender: principal, Raw: body.Text, Kind: body.Kind, Correlation: d.ID,
		MayAffectGrant: body.Kind == delegation.Input, RevokeGrantID: body.RevokeGrantID,
	}
	if body.Review != nil {
		e.Review = &delegation.ReviewNote{Source: body.Review.Source, Fingerprint: body.Review.Fingerprint}
	}
	if body.Amendment != nil {
		e.Amendment = &delegation.Amendment{Target: body.Amendment.Target, Format: body.Amendment.Format, TakeOver: body.Amendment.TakeOver}
	}
	// An exact retry survives a changed wait. A different payload using the same ID is rejected.
	fingerprint, _ := json.Marshal(body)
	e.IntakeFingerprint = delegation.Fingerprint(string(fingerprint))
	duplicate, err := a.store.CheckEventRetry(r.Context(), e)
	if err != nil {
		writeIntakeError(w, err)
		return
	}
	if duplicate {
		write(w, http.StatusOK, map[string]bool{"inserted": false})
		return
	}
	if body.Kind == delegation.Approval {
		if d.Wait == nil || d.Wait.Kind != delegation.Approval || d.Wait.Correlation != body.Correlation ||
			body.ScopeFingerprint != view(d).ScopeFingerprint || !time.Now().Before(body.ExpiresAt) {
			fail(w, http.StatusConflict, "approval no longer matches the current wait and scope")
			return
		}
		e.Correlation = body.Correlation
		e.Grant = &delegation.GrantTerms{Scope: d.Request, ExpiresAt: body.ExpiresAt}
	}
	if err := delegation.ValidateEvent(d, e); err != nil {
		fail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	inserted, err := a.store.Receive(r.Context(), e)
	if err != nil {
		writeIntakeError(w, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusAccepted
	}
	write(w, status, map[string]bool{"inserted": inserted})
}

func writeIntakeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ledger.ErrSourceEventConflict) || errors.Is(err, ledger.ErrApprovalChanged) {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	internal(w, err)
}

func (a *API) feed(w http.ResponseWriter, r *http.Request, owner string) {
	items, err := a.store.TrackingInbox(r.Context(), owner, r.PathValue("project"), 100)
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, items)
}

func (a *API) ack(w http.ResponseWriter, r *http.Request, owner string) {
	seq, err := strconv.ParseUint(r.PathValue("seq"), 10, 64)
	if err != nil || seq == 0 {
		fail(w, http.StatusBadRequest, "invalid feed sequence")
		return
	}
	item, err := a.store.AcknowledgeTracking(r.Context(), owner, r.PathValue("project"), seq)
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, item)
}
