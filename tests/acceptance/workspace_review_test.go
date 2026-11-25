package acceptance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/celanwang/stead/internal/httpapi"
)

func TestWorkspaceReviewSharesOnlyAuthorizedSourceMetadata(t *testing.T) {
	b := newBriefCase(t, true, "status", "rates")
	const confidential = "Negotiated rates must stay private."
	b.fact("status", "Friday launch approved", true, 0)
	b.fact("rates", confidential, false, 0)
	b.advance()

	reviewerToken, otherToken := strings.Repeat("a", 48), strings.Repeat("b", 48)
	config, err := json.Marshal([]httpapi.Credential{
		{Principal: b.d.Briefing.Reviewer, Token: reviewerToken},
		{Principal: "independent-owner", Token: otherToken},
	})
	must(t, err)
	api, err := httpapi.New(b.l, nil, string(config))
	must(t, err)
	handler := api.Handler()
	get := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	w := get("/v1/reviews/"+b.d.ID, reviewerToken)
	check(t, w.Code == http.StatusOK && strings.Contains(w.Body.String(), "Friday launch approved") &&
		strings.Contains(w.Body.String(), "rates") && !strings.Contains(w.Body.String(), confidential),
		"review response exposed the wrong data: %s", w.Body)
	check(t, get("/v1/reviews/"+b.d.ID, otherToken).Code == http.StatusNotFound, "an unrelated principal could open the review")
	check(t, get("/v1/delegations/"+b.d.ID, reviewerToken).Code == http.StatusNotFound, "reviewer could read the owner's ledger")
}
