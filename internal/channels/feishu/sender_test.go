package feishu

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/celanwang/stead/internal/delegation"
)

func configuredSender(t *testing.T, handler http.HandlerFunc) *Sender {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	s, err := NewSender(SenderConfig{AppID: "test-app", AppSecret: "test-secret", BaseURL: server.URL,
		AllowedChatIDs: []string{"oc_team"}}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func approved(id string) delegation.Action {
	return delegation.Action{ID: id, DelegationID: "task", GrantID: "owner-approval", State: delegation.Sending,
		Scope: delegation.Scope{Target: TargetPrefix + "oc_team", Content: "评审在周五\nRoom \"A\""}}
}

func TestApprovedTextAndCachedTenantToken(t *testing.T) {
	var tokens, sends atomic.Int32
	uuids := make(chan string, 3)
	s := configuredSender(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s", r.Method)
		}
		switch r.URL.RequestURI() {
		case tokenPath:
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["app_id"] != "test-app" ||
				body["app_secret"] != "test-secret" || r.Header.Get("Authorization") != "" {
				t.Error("invalid tenant token request")
			}
			fmt.Fprintf(w, `{"code":0,"tenant_access_token":"t-%d","expire":100}`, tokens.Add(1))
		case sendPath:
			var body map[string]string
			var content map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || json.Unmarshal([]byte(body["content"]), &content) != nil ||
				content["text"] != approved("a").Scope.Content || body["receive_id"] != "oc_team" || body["msg_type"] != "text" ||
				r.Header.Get("Authorization") != fmt.Sprintf("Bearer t-%d", tokens.Load()) {
				t.Error("invalid IM message or credentials")
			}
			uuids <- body["uuid"]
			fmt.Fprintf(w, `{"code":0,"data":{"message_id":"om_%d","chat_id":"oc_team"}}`, sends.Add(1))
		default:
			t.Errorf("unexpected API path %s", r.URL.RequestURI())
		}
	})
	now := time.Now()
	s.now = func() time.Time { return now }
	for index, id := range []string{"action-1", "action-2", "action-1"} {
		if index == 2 {
			now = now.Add(2 * time.Minute)
		}
		r, err := s.Execute(t.Context(), approved(id))
		if err != nil || r.State != delegation.Delivered || r.ActionID != id || r.Detail != fmt.Sprintf("om_%d", index+1) {
			t.Fatalf("receipt=%+v, err=%v", r, err)
		}
	}
	first, second, repeated := <-uuids, <-uuids, <-uuids
	if len(first) != 36 || first == second || repeated != first || tokens.Load() != 2 || sends.Load() != 3 {
		t.Fatalf("unstable action UUID or token cache; tokens=%d sends=%d", tokens.Load(), sends.Load())
	}
}

func TestAmbiguousMessageIsNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"server error", 502, ""},
		{"truncated", 200, `{"code":0`},
		{"missing code", 200, `{"data":{"message_id":"m","chat_id":"oc_team"}}`},
		{"wrong chat", 200, `{"code":0,"data":{"message_id":"m","chat_id":"oc_other"}}`},
		{"no message ID", 200, `{"code":0,"data":{"chat_id":"oc_team"}}`},
		{"unknown error code", 200, `{"code":99999,"msg":"sensitive provider details"}`},
		{"oversized", 200, strings.Repeat(" ", 256<<10+1)},
		{"connection lost", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sends atomic.Int32
			s := configuredSender(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tokenPath {
					io.WriteString(w, `{"code":0,"tenant_access_token":"t","expire":7200}`)
					return
				}
				sends.Add(1)
				if tc.status == 0 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						conn.Close()
					}
					return
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			r, err := s.Execute(t.Context(), approved("a"))
			if err != nil || !r.Valid() || r.State != delegation.Unknown || sends.Load() != 1 || strings.Contains(r.Detail, "sensitive") {
				t.Fatalf("receipt=%+v, err=%v, send attempts=%d", r, err, sends.Load())
			}
		})
	}
}

func TestDefiniteFailureBeforeSend(t *testing.T) {
	var tokenRequests atomic.Int32
	s := configuredSender(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != tokenPath {
			t.Error("unexpected message send")
		}
		tokenRequests.Add(1)
		http.Error(w, "private error details", http.StatusUnauthorized)
	})
	for _, change := range []func(*delegation.Action){
		func(a *delegation.Action) { a.State = delegation.Pending },
		func(a *delegation.Action) { a.Scope.Target = "feishu:oc_other" },
		func(a *delegation.Action) { a.Scope.Target = "oc_team" },
		func(a *delegation.Action) { a.Scope.Guard = &delegation.DeliveryGuard{} },
		func(a *delegation.Action) { a.GrantID = "" },
		func(a *delegation.Action) { a.Scope.Content = strings.Repeat("x", 150<<10+1) },
	} {
		a := approved("a")
		change(&a)
		r, err := s.Execute(t.Context(), a)
		if err != nil || !r.Valid() || r.State != delegation.Failed || tokenRequests.Load() != 0 {
			t.Fatalf("invalid preflight result: %+v, err=%v", r, err)
		}
	}
	r, err := s.Execute(t.Context(), approved("a"))
	if err != nil || r.State != delegation.Failed || tokenRequests.Load() != 1 || strings.Contains(r.Detail, "private error") {
		t.Fatalf("auth failure: %+v, err=%v", r, err)
	}
}
