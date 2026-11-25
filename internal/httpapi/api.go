package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/intent"
	"github.com/celanwang/stead/internal/ledger"
)

type Credential struct {
	Principal string `json:"principal"`
	Token     string `json:"token"`
}

type credential struct {
	principal string
	hash      [sha256.Size]byte
}

type API struct {
	store       *ledger.Ledger
	model       intent.Interpreter
	credentials []credential
	modelLimit  chan struct{}
}

func New(store *ledger.Ledger, model intent.Interpreter, rawCredentials string) (*API, error) {
	var configured []Credential
	if err := jsonv2.Unmarshal([]byte(rawCredentials), &configured, jsonv2.RejectUnknownMembers(true)); err != nil ||
		len(configured) == 0 || len(configured) > 100 {
		return nil, errors.New("configure 1–100 API credentials in STEAD_API_TOKENS")
	}
	a := &API{store: store, model: model, modelLimit: make(chan struct{}, 4)}
	for _, c := range configured {
		if c.Principal == "" || len(c.Principal) > 191 || strings.IndexFunc(c.Principal, unicode.IsControl) >= 0 ||
			len(c.Token) < 32 || len(c.Token) > 512 || strings.IndexFunc(c.Token, unicode.IsSpace) >= 0 {
			return nil, errors.New("API credentials require a principal and a bearer token of at least 32 bytes")
		}
		h := sha256.Sum256([]byte(c.Token))
		for _, previous := range a.credentials {
			if subtle.ConstantTimeCompare(previous.hash[:], h[:]) == 1 {
				return nil, errors.New("configure a distinct bearer token for each API credential")
			}
		}
		a.credentials = append(a.credentials, credential{principal: c.Principal, hash: h})
	}
	return a, nil
}

type operation func(http.ResponseWriter, *http.Request, string)

func (a *API) authorized(run operation) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var principal string
		if values := r.Header.Values("Authorization"); len(values) == 1 {
			scheme, token, ok := strings.Cut(values[0], " ")
			if ok && strings.EqualFold(scheme, "Bearer") && len(token) >= 32 && len(token) <= 512 {
				h := sha256.Sum256([]byte(token))
				for _, c := range a.credentials {
					if subtle.ConstantTimeCompare(c.hash[:], h[:]) == 1 {
						principal = c.principal
					}
				}
			}
		}
		if principal == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, http.StatusUnauthorized, "authentication required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer cancel()
		run(w, r.WithContext(ctx), principal)
	}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	a.workspaceRoutes(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		write(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.store.CheckReady(ctx); err != nil {
			fail(w, http.StatusServiceUnavailable, "ledger unavailable or migrations pending")
			return
		}
		write(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /v1/drafts", a.authorized(a.draft))
	mux.HandleFunc("POST /v1/delegations", a.authorized(a.submit))
	mux.HandleFunc("GET /v1/delegations", a.authorized(a.list))
	mux.HandleFunc("GET /v1/delegations/{id}", a.authorized(a.status))
	mux.HandleFunc("POST /v1/delegations/{id}/events", a.authorized(a.receive))
	mux.HandleFunc("GET /v1/projects/{project}/feed", a.authorized(a.feed))
	mux.HandleFunc("POST /v1/projects/{project}/feed/{seq}/ack", a.authorized(a.ack))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (a *API) draft(w http.ResponseWriter, r *http.Request, _ string) {
	var request struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &request) {
		return
	}
	text := strings.TrimSpace(request.Text)
	if text == "" || len(text) > 32768 {
		fail(w, http.StatusUnprocessableEntity, "provide a request of 1–32768 bytes")
		return
	}
	if a.model == nil {
		fail(w, http.StatusServiceUnavailable, "draft model is not configured")
		return
	}
	select {
	case a.modelLimit <- struct{}{}:
		defer func() { <-a.modelLimit }()
	default:
		w.Header().Set("Retry-After", "1")
		fail(w, http.StatusTooManyRequests, "draft model is busy")
		return
	}
	d, err := a.model.Interpret(r.Context(), text)
	var clarification *intent.Clarification
	switch {
	case errors.As(err, &clarification):
		write(w, http.StatusUnprocessableEntity, map[string]string{"clarification": clarification.Question})
	case err != nil:
		fail(w, http.StatusBadGateway, "draft model request failed")
	default:
		write(w, http.StatusOK, d)
	}
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "expected application/json")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	var size *http.MaxBytesError
	switch {
	case errors.As(err, &size):
		fail(w, http.StatusRequestEntityTooLarge, "request exceeds 64 KiB")
	case err != nil || jsonv2.Unmarshal(body, into, jsonv2.RejectUnknownMembers(true)) != nil:
		fail(w, http.StatusBadRequest, "invalid JSON request")
	default:
		return true
	}
	return false
}

func internal(w http.ResponseWriter, err error) {
	if errors.Is(err, delegation.ErrNotFound) {
		fail(w, http.StatusNotFound, "record not found")
		return
	}
	fail(w, http.StatusInternalServerError, "ledger operation failed")
}

func fail(w http.ResponseWriter, status int, message string) {
	write(w, status, map[string]string{"error": message})
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
