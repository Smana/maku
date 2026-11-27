// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Smana/maku/internal/fanout"
)

// Developer review gap 9: the New task picker lists every repository the planner serves, not only
// the repositories of rooms the developer already sees (none on day one).
func TestHandoffRepositoriesListsTheServedOnes(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.Catalogue = NewCatalogueCache(factoryServing("acme/b", "acme/a"), nil, nil)
		s.Intake.Allow = nil // no human.intake.plan.repositories: the factory's whole list
	})
	get := func(h http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/handoffs/repositories", nil)
		if h != nil {
			req.Header = h.Clone()
		}
		rec := httptest.NewRecorder()
		e.srv.Routes().ServeHTTP(rec, req)
		return rec
	}
	rec := get(header("dev"))
	if rec.Code != http.StatusOK {
		t.Fatalf("member = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !slices.Equal(got.Repositories, []string{"acme/a", "acme/b"}) {
		t.Fatalf("got %v (%v): %s", got.Repositories, err, rec.Body.String())
	}
	// human.intake.plan.repositories narrows the list, as it narrows the planner.
	e.srv.Intake.Allow = map[string]bool{"acme/b": true}
	e.srv.Catalogue = NewCatalogueCache(factoryServing("acme/b", "acme/a"), nil, nil)
	if rec := get(header("dev")); rec.Body.String() != "{\"repositories\":[\"acme/b\"]}\n" {
		t.Fatalf("narrowed: %s", rec.Body.String())
	}
	if rec := get(nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d", rec.Code)
	}
}

// The factory down leaves the picker as it was: an empty list, never an error page.
func TestHandoffRepositoriesWithTheFactoryDown(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.Catalogue = NewCatalogueCache(factoryDown, nil, nil)
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/handoffs/repositories", nil)
	req.Header = header("dev")
	rec := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"repositories\":[]}\n" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
}
