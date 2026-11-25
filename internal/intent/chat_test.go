package intent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const schedulingDraft = `{"kind":"scheduling","goal":"约下周的评审","scheduling":{"target":"团队日历","content":"周二评审"}}`

func TestChatDraftForReview(t *testing.T) {
	const input = "帮我约下周二的团队评审"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer local-test" {
			t.Errorf("unexpected HTTP request")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var req struct {
			Model          string            `json:"model"`
			Messages       []message         `json:"messages"`
			ResponseFormat map[string]string `json:"response_format"`
		}
		if err := json.Unmarshal(body, &req, json.RejectUnknownMembers(true)); err != nil {
			t.Error(err)
			return
		}
		if req.Model != "local-model" || len(req.Messages) != 2 || req.Messages[0].Role != "system" ||
			req.Messages[1] != (message{Role: "user", Content: input}) || req.ResponseFormat["type"] != "json_object" {
			t.Errorf("incorrect request: %+v", req)
		}
		fmt.Fprintf(w, `{"id":"trace","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":%q}}]}`, schedulingDraft)
	}))
	defer server.Close()

	model, err := NewChatCompletions(Config{Endpoint: server.URL + "/v1/chat/completions", Model: "local-model", APIKey: "local-test"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	d, err := model.Interpret(t.Context(), input)
	if err != nil || d.Kind != Scheduling || d.Scheduling.Target != "团队日历" || calls.Load() != 1 {
		t.Fatalf("draft = %+v, err = %v, requests = %d", d, err, calls.Load())
	}
	if _, err := model.Interpret(t.Context(), " "); err == nil || calls.Load() != 1 {
		t.Fatal("invalid input reached provider")
	}
}

func TestParseDraft(t *testing.T) {
	for kind, input := range map[Kind]string{
		Scheduling: schedulingDraft,
		Briefing:   `{"kind":"briefing","goal":"整理进度","briefing":{"sources":["项目周报"],"format":"一页摘要"}}`,
		Tracking:   `{"kind":"tracking","goal":"跟踪修订","tracking":{"source":"papers","object_id":"p1","watch":"结论变更"}}`,
	} {
		d, err := ParseDraft([]byte(input))
		if err != nil || d.Kind != kind {
			t.Errorf("kind %v, got %+v, err %v", kind, d, err)
		}
	}

	for _, input := range []string{
		schedulingDraft + "{}",
		strings.Replace(schedulingDraft, `"scheduling":`, `"approval":true,"scheduling":`, 1),
		strings.Replace(schedulingDraft, `"target":`, `"admin":true,"target":`, 1),
		strings.Replace(schedulingDraft, `"target":`, `"target":"different","target":`, 1),
		strings.Replace(schedulingDraft, `"target":`, `"TARGET":`, 1),
		strings.Replace(schedulingDraft, `"kind":"scheduling"`, `"kind":"tracking"`, 1),
		strings.Replace(schedulingDraft, `"团队日历"`, `null`, 1),
		`{"kind":"tracking","goal":"g","tracking":{"source":"x","watch":"changes"}}`,
		`{"kind":"briefing","goal":"g","briefing":{"sources":[],"format":"plain"}}`,
		`{"kind":"briefing","goal":"g","briefing":{"sources":[" "],"format":"plain"}}`,
		`{"clarification":"which?","owner":"admin"}`,
	} {
		if _, err := ParseDraft([]byte(input)); err == nil {
			t.Errorf("unexpectedly accepted %s", input)
		}
	}
	var q *Clarification
	if _, err := ParseDraft([]byte(`{"clarification":"Which calendar?"}`)); !errors.As(err, &q) || q.Question != "Which calendar?" {
		t.Errorf("missing clarification: %v", err)
	}
}

func TestRejectNonTextAndPartialCompletions(t *testing.T) {
	content, err := json.Marshal(schedulingDraft)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"choices":[]}`,
		fmt.Sprintf(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":%s}}]}`, content),
		fmt.Sprintf(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","tool_calls":[{}],"content":%s}}]}`, content),
		fmt.Sprintf(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","function_call":{},"content":%s}}]}`, content),
		fmt.Sprintf(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","refusal":"no","content":%s}}]}`, content),
	} {
		if _, err := interpretResponse([]byte(body)); err == nil {
			t.Errorf("accepted invalid envelope %s", body)
		}
	}
}

func TestHTTPFailureBoundaries(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
	}))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/redirect":
			http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
		case "/large":
			io.WriteString(w, strings.Repeat("x", 256<<10+1))
		case "/error":
			http.Error(w, "private request text", http.StatusTooManyRequests)
		}
	}))
	defer server.Close()
	for _, path := range []string{"/slow", "/redirect", "/large", "/error"} {
		cfg := Config{Endpoint: server.URL + path, Model: "local", Timeout: time.Second}
		if path == "/slow" {
			cfg.Timeout = 50 * time.Millisecond
		}
		model, err := NewChatCompletions(cfg, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		_, err = model.Interpret(t.Context(), "hello")
		if err == nil || strings.Contains(err.Error(), "private request text") {
			t.Errorf("%s returned %v", path, err)
		}
		if path == "/slow" && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("timeout error = %v", err)
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("private task forwarded to redirect destination")
	}
}

func TestConfigAndJSONRoundTrip(t *testing.T) {
	for _, cfg := range []Config{
		{Model: "local"},
		{Endpoint: "ftp://example.org", Model: "local"},
		{Endpoint: "https://example.org"},
		{Endpoint: "http://example.org", Model: "local", APIKey: "secret"},
	} {
		if _, err := NewChatCompletions(cfg, nil); err == nil {
			t.Fatal("accepted invalid model configuration")
		}
	}
	d, err := ParseDraft([]byte(schedulingDraft))
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(d)
	if err != nil || !jsontext.Value(out).IsValid() {
		t.Fatalf("encoded invalid draft: %s (%v)", out, err)
	}
	if _, err := ParseDraft(out); err != nil {
		t.Fatal(err)
	}
}
