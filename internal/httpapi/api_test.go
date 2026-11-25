package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/celanwang/stead/internal/intent"
)

type modelFunc func(context.Context, string) (intent.Draft, error)

func (f modelFunc) Interpret(ctx context.Context, text string) (intent.Draft, error) {
	return f(ctx, text)
}

func TestDraftAuthenticationAndStrictDecoding(t *testing.T) {
	token := strings.Repeat("x", 40)
	calls := 0
	model := modelFunc(func(_ context.Context, text string) (intent.Draft, error) {
		calls++
		if text == "clarify" {
			return intent.Draft{}, &intent.Clarification{Question: "Who is attending?"}
		}
		return intent.Draft{Kind: intent.Scheduling, Goal: text,
			Scheduling: &intent.Schedule{Target: "calendar", Content: "Confirm the appointment"}}, nil
	})
	api, err := New(nil, model, `[{"principal":"alice","token":"`+token+`"}]`)
	if err != nil {
		t.Fatal(err)
	}
	h := api.Handler()
	for _, tc := range []struct {
		token string
		body  string
		want  int
	}{
		{"wrong", `{"text":"unsafe"}`, http.StatusUnauthorized},
		{token, `{"text":"x","owner":"bob"}`, http.StatusBadRequest},
		{token, `{"text":"x","text":"duplicate"}`, http.StatusBadRequest},
		{token, `{"text":"` + strings.Repeat("x", 64<<10) + `"}`, http.StatusRequestEntityTooLarge},
		{token, `{"text":"clarify"}`, http.StatusUnprocessableEntity},
		{token, `{"text":"Set a meeting"}`, http.StatusOK},
	} {
		r := httptest.NewRequest("POST", "/v1/drafts", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("response status=%d want=%d: %s", w.Code, tc.want, w.Body.String())
		}
	}
	if calls != 2 {
		t.Errorf("model called %d times, want 2", calls)
	}
}

func TestCredentialRejection(t *testing.T) {
	token := strings.Repeat("a", 40)
	for _, config := range []string{`[]`, `[{"principal":"alice","token":"short"}]`,
		`[{"principal":"alice","token":"` + token + `"},{"principal":"bob","token":"` + token + `"}]`} {
		if _, err := New(nil, nil, config); err == nil {
			t.Errorf("accepted invalid credential configuration")
		}
	}
}
