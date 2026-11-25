package acceptance

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/httpapi"
)

func TestHTTPSubmissionScopeAndApproval(t *testing.T) {
	f := newFixture(t)
	l := f.connect()
	alice, bob := f.prefix+"-alice", f.prefix+"-bob"
	tokenA, tokenB := strings.Repeat("a", 48), strings.Repeat("b", 48)
	credentials, err := json.Marshal([]httpapi.Credential{{Principal: alice, Token: tokenA}, {Principal: bob, Token: tokenB}})
	must(t, err)
	api, err := httpapi.New(l, nil, string(credentials))
	must(t, err)
	h := api.Handler()
	respond := func(method, path, token, key string, payload any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	assertStatus := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		check(t, w.Code == status, "HTTP status=%d want=%d: %s", w.Code, status, w.Body)
	}
	const collection = "/v1/delegations"
	assertStatus(respond("GET", "/healthz", "", "", nil), http.StatusOK)
	assertStatus(respond("GET", "/readyz", "", "", nil), http.StatusOK)
	assertStatus(respond("GET", collection, "", "", nil), http.StatusUnauthorized)

	body := map[string]any{
		"goal": "Schedule a review", "deadline": f.clock.Now().Add(time.Hour),
		"scheduling": map[string]string{"target": "calendar", "content": "Confirm review"},
	}
	const key = "schedule-review"
	results := make(chan *httptest.ResponseRecorder, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			results <- respond("POST", collection, tokenA, key, body)
		})
	}
	workers.Wait()
	close(results)
	var created int
	var id string
	for w := range results {
		check(t, w.Code == http.StatusCreated || w.Code == http.StatusOK, "submit failed: %s", w.Body)
		if w.Code == http.StatusCreated {
			created++
		}
		var reply struct{ Delegation delegation.Delegation }
		must(t, json.Unmarshal(w.Body.Bytes(), &reply))
		if id == "" {
			id = reply.Delegation.ID
		}
		check(t, id == reply.Delegation.ID && reply.Delegation.Owner == alice, "idempotent response differs: %+v", reply)
	}
	check(t, created == 1 && id != "", "expected one durable creation, got %d", created)
	uri := collection + "/" + id
	assertStatus(respond("GET", uri, tokenB, "", nil), http.StatusNotFound)
	assertStatus(respond("POST", uri+"/events", tokenB, "", map[string]string{
		"id": "unauthorized", "kind": "input", "text": "Change someone else's task",
	}), http.StatusNotFound)
	body["goal"] = "Change the existing request"
	assertStatus(respond("POST", collection, tokenA, key, body), http.StatusConflict)
	assertStatus(respond("POST", collection, tokenB, key, body), http.StatusCreated)

	rt := f.runtime(l)
	decision, err := rt.Advance(f.ctx, id)
	must(t, err)
	check(t, decision.Wait != nil, "approval wait was not written")
	current := respond("GET", uri, tokenA, "", nil)
	assertStatus(current, http.StatusOK)
	var parsed struct {
		ScopeFingerprint string `json:"scope_fingerprint"`
	}
	must(t, json.Unmarshal(current.Body.Bytes(), &parsed))
	approval := map[string]any{
		"id": "approve-1", "kind": "approval", "text": "I approve this scope",
		"correlation": decision.Wait.Correlation, "scope_fingerprint": "stale",
		"expires_at": f.clock.Now().Add(30 * time.Minute),
	}
	assertStatus(respond("POST", uri+"/events", tokenA, "", approval), http.StatusConflict)
	approval["scope_fingerprint"] = parsed.ScopeFingerprint
	assertStatus(respond("POST", uri+"/events", tokenA, "", approval), http.StatusAccepted)
	decision, err = rt.Advance(f.ctx, id)
	must(t, err)
	check(t, decision.Action != nil, "no action was approved")
	must(t, rt.Dispatch(f.ctx, decision.Action.ID))
	f.calls(1)
	assertStatus(respond("POST", uri+"/events", tokenA, "", approval), http.StatusOK)
	approval["text"] = "Replay ID with different content"
	assertStatus(respond("POST", uri+"/events", tokenA, "", approval), http.StatusConflict)
	status, err := l.Status(f.ctx, id)
	must(t, err)
	check(t, len(status.Events) == 1 && len(status.Actions) == 1, "unexpected events or actions: %+v", status)
	t.Log("two simultaneous submissions produced one delegation, owner isolation held, one approval dispatched once")
}
