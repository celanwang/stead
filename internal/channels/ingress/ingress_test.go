package ingress

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/celanwang/stead/internal/scenarios/tracking"
)

var binding = Binding{Owner: "alice", Project: "readings", Source: "preprints"}

const observation = `{"object_id":"paper-1","event_id":"chg-2","revision":2,"title":"Renamed","material":true,"conclusion_ids":["claim-1"]}`

func TestBoundObservationAndReceipt(t *testing.T) {
	var calls int
	h, err := NewHandler(binding, "local-token", func(ctx context.Context, event tracking.Observation) (Receipt, error) {
		calls++
		if event.Key != (tracking.Key{Owner: "alice", Project: "readings", Source: "preprints", ObjectID: "paper-1"}) ||
			event.EventID != "chg-2" || event.Revision != 2 || event.Title != "Renamed" || !event.Material ||
			len(event.ConclusionIDs) != 1 || event.ConclusionIDs[0] != "claim-1" {
			t.Errorf("incorrect normalized observation: %+v", event)
		}
		return Receipt{Sequence: 17, Duplicate: calls == 2, Outcome: tracking.Applied, Disposition: tracking.Immediate}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, duplicate := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(observation))
		req.Header.Set("Authorization", "Bearer local-token")
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var result Receipt
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Sequence != 17 ||
			result.Duplicate != duplicate || result.Disposition != tracking.Immediate {
			t.Fatalf("response = %d %s, err = %v", w.Code, w.Body.String(), err)
		}
	}
}

func TestInvalidRequestsNeverReachStore(t *testing.T) {
	h, err := NewHandler(binding, "local-token", func(context.Context, tracking.Observation) (Receipt, error) {
		t.Fatal("invalid request reached store")
		return Receipt{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, auth, media, data string
		want                                  int
	}{
		{"unknown route", "POST", "/admin", "Bearer local-token", "application/json", observation, 404},
		{"method", "GET", "/observations", "Bearer local-token", "application/json", observation, 405},
		{"wrong bearer", "POST", "/observations", "Bearer wrong-token", "application/json", observation, 401},
		{"no bearer", "POST", "/observations", "", "application/json", observation, 401},
		{"content type", "POST", "/observations", "Bearer local-token", "text/plain", observation, 415},
		{"authority field", "POST", "/observations", "Bearer local-token", "application/json", strings.Replace(observation, "{", `{"owner":"other",`, 1), 400},
		{"duplicate field", "POST", "/observations", "Bearer local-token", "application/json", strings.Replace(observation, "{", `{"object_id":"other",`, 1), 400},
		{"trailing value", "POST", "/observations", "Bearer local-token", "application/json", observation + " {}", 400},
		{"bad key", "POST", "/observations", "Bearer local-token", "application/json", strings.Replace(observation, "chg-2", " ", 1), 400},
		{"oversized", "POST", "/observations", "Bearer local-token", "application/json", strings.Repeat(" ", maxBody+1), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.data))
			req.Header.Set("Authorization", tc.auth)
			req.Header.Set("Content-Type", tc.media)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d %q, want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}

func TestAdapterPassesRequestCancellationAndHidesErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{ErrConflict, 409}, {errors.New("private DB details"), 503}} {
		h, err := NewHandler(binding, "token", func(ctx context.Context, event tracking.Observation) (Receipt, error) {
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Error("request cancellation was lost")
			}
			return Receipt{}, tc.err
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		req := httptest.NewRequestWithContext(ctx, "POST", "/observations", strings.NewReader(observation))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private DB details") {
			t.Fatalf("unexpected error %d %q", w.Code, w.Body.String())
		}
	}
}

func TestLoopbackListenerLifecycle(t *testing.T) {
	for _, nonlocal := range []string{"0.0.0.0:8787", "[::]:8787", "localhost:8787", ":8787", "192.0.2.1:8787", "127.0.0.1:http"} {
		if ln, err := Listen(nonlocal); err == nil {
			ln.Close()
			t.Fatalf("permitted %q", nonlocal)
		}
	}
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
		}))
	}()
	requestDone := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), "GET", "http://"+ln.Addr().String(), nil)
		if err == nil {
			var response *http.Response
			response, err = http.DefaultClient.Do(req)
			if response != nil {
				response.Body.Close()
			}
		}
		requestDone <- err
	}()
	select {
	case <-entered:
	case err := <-requestDone:
		t.Fatalf("request did not reach the listener: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach the listener")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not drain after cancellation")
	}
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
}
