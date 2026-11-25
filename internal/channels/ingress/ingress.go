// Package ingress receives source observations from a local collector.
package ingress

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json/v2"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/celanwang/stead/internal/scenarios/tracking"
)

const maxBody = 64 << 10

type Binding struct {
	Owner   string
	Project string
	Source  string
}

type Receipt struct {
	Sequence    uint64               `json:"seq"`
	Duplicate   bool                 `json:"duplicate"`
	Outcome     tracking.Outcome     `json:"outcome"`
	Disposition tracking.Disposition `json:"disposition"`
}

type Observe func(context.Context, tracking.Observation) (Receipt, error)

var ErrConflict = errors.New("event ID is already bound to another observation")

func NewHandler(binding Binding, token string, observe Observe) (http.Handler, error) {
	key := tracking.Key{Owner: binding.Owner, Project: binding.Project, Source: binding.Source, ObjectID: "object"}
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if token == "" || strings.ContainsAny(token, " \r\n\t") || observe == nil {
		return nil, errors.New("ingress requires a bearer token and an observation handler")
	}
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/observations" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		scheme, provided, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		digest := sha256.Sum256([]byte(provided))
		if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(digest[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "bearer token required", http.StatusUnauthorized)
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			http.Error(w, "use application/json", http.StatusUnsupportedMediaType)
			return
		}
		var input struct {
			ObjectID      string   `json:"object_id"`
			EventID       string   `json:"event_id"`
			Revision      uint64   `json:"revision"`
			Title         string   `json:"title"`
			Summary       string   `json:"summary"`
			Material      bool     `json:"material"`
			Withdrawn     bool     `json:"withdrawn"`
			ConclusionIDs []string `json:"conclusion_ids"`
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				http.Error(w, "observation exceeds 64 KiB", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, "could not read observation", http.StatusBadRequest)
			}
			return
		}
		if err := json.Unmarshal(data, &input, json.RejectUnknownMembers(true)); err != nil {
			http.Error(w, "invalid observation JSON", http.StatusBadRequest)
			return
		}
		event := tracking.Observation{
			Key:     tracking.Key{Owner: binding.Owner, Project: binding.Project, Source: binding.Source, ObjectID: input.ObjectID},
			EventID: input.EventID, Revision: input.Revision, Title: input.Title, Summary: input.Summary,
			Material: input.Material, Withdrawn: input.Withdrawn, ConclusionIDs: input.ConclusionIDs,
		}
		// Validate the complete observation even when a prior delivery exists in the ledger.
		if _, _, err := tracking.Apply(tracking.Item{}, event, nil); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		receipt, err := observe(r.Context(), event)
		if errors.Is(err, ErrConflict) {
			http.Error(w, "event ID conflicts with an earlier observation", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "could not persist observation", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, receipt)
	}), nil
}
