package httpapi

import (
	"embed"
	"net/http"
	"time"

	"github.com/celanwang/stead/internal/delegation"
)

//go:embed workspace/*
var workspace embed.FS

func (a *API) workspaceRoutes(mux *http.ServeMux) {
	for path, asset := range map[string]string{
		"/{$}": "index.html", "/workspace.css": "workspace.css", "/workspace.js": "workspace.js",
	} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
			w.Header().Set("Referrer-Policy", "no-referrer")
			http.ServeFileFS(w, r, workspace, "workspace/"+asset)
		})
	}
	mux.HandleFunc("GET /v1/session", a.authorized(func(w http.ResponseWriter, _ *http.Request, principal string) {
		write(w, http.StatusOK, map[string]string{"principal": principal})
	}))
	mux.HandleFunc("GET /v1/reviews/{id}", a.authorized(a.review))
}

type reviewItem struct {
	Source      string `json:"source"`
	Fingerprint string `json:"fingerprint"`
	Rendered    string `json:"rendered"`
	Reviewed    bool   `json:"reviewed"`
}

func (a *API) review(w http.ResponseWriter, r *http.Request, principal string) {
	d, err := a.store.Delegation(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	if d.Briefing == nil || d.Briefing.Reviewer != principal {
		internal(w, delegation.ErrNotFound)
		return
	}
	items := make([]reviewItem, 0, len(d.Briefing.Items))
	for _, item := range d.Briefing.Items {
		if !item.TakenOver && item.Fingerprint != "" {
			items = append(items, reviewItem{item.ID, item.Fingerprint, item.Rendered, item.Reviewed == item.Fingerprint})
		}
	}
	write(w, http.StatusOK, struct {
		ID       string       `json:"id"`
		Goal     string       `json:"goal"`
		Deadline time.Time    `json:"deadline"`
		Closed   bool         `json:"closed"`
		Items    []reviewItem `json:"items"`
	}{d.ID, d.Goal, d.Deadline, d.Result != nil, items})
}
