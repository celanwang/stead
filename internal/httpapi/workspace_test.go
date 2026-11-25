package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceAssetsAndSessionIsolation(t *testing.T) {
	const token = "owner-workspace-verification-token"
	api, err := New(nil, nil, `[{"principal":"alice","token":"`+token+`"}]`)
	if err != nil {
		t.Fatal(err)
	}
	handler := api.Handler()
	for url, marker := range map[string]string{"/": "Connect to Stead", "/workspace.css": ".task-row", "/workspace.js": "/v1/session"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), marker) ||
			strings.Contains(w.Body.String(), token) || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Errorf("unexpected workspace response for %s: HTTP %d", url, w.Code)
		}
	}
	request := httptest.NewRequest("GET", "/v1/session", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated session returned %d", w.Code)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"principal":"alice"}` {
		t.Fatalf("unexpected authenticated session: %s", w.Body.String())
	}
}
