// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
)

type fakeEvidence struct {
	asked []string
	err   error
}

func (f *fakeEvidence) Report(_ context.Context, repository string) (evidence.Report, error) {
	f.asked = append(f.asked, repository)
	return evidence.Report{Repository: repository, Minimum: 20}, f.err
}

// readAccess reads by principal: "human:291" reads coordPrimary only.
type readAccess struct{ coordAccess }

func (readAccess) CanRead(_ context.Context, p authn.Principal, repository string) (bool, error) {
	if p.ID == "human:9" {
		return false, errors.New("github down")
	}
	return (p.ID == "human:291" || p.ID == "human:5") && repository == coordPrimary, nil
}

type fakeEditor struct{ previews, timelines int }

func (f *fakeEditor) Preview(context.Context, string, string) (any, error) {
	f.previews++
	return map[string]bool{"valid": true}, nil
}

func (f *fakeEditor) Timeline(_ context.Context, repo string, pr int) (any, error) {
	f.timelines++
	return map[string]any{"repository": repo, "pullRequest": pr}, nil
}

func evidenceServer(on bool) (*Server, *fakeEvidence) {
	f := &fakeEvidence{}
	return &Server{
		Auth: byToken{"human": coordHuman, "reader": coordReader, "slack": coordSlack,
			"flaky":      authn.Principal{Kind: coordHuman.Kind, ID: "human:9", Sub: "9"},
			"onlyreader": authn.Principal{Kind: coordHuman.Kind, ID: "human:5", Sub: "5"}},
		Cfg: &config.Config{PolicyManagement: config.PolicyManagement{Promotion: on},
			Repositories: []config.Repository{{Name: coordPrimary}, {Name: coordOther}}},
		Access: readAccess{coordAccess{principals: map[string]bool{"human:291|" + coordPrimary: true}}}, Evidence: f, Editor: &fakeEditor{},
	}, f
}

func getAs(h http.Handler, token, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// Stage 6: a reader of the repository on GitHub reads its evidence; anyone else, a machine, or an
// unconfigured repository reads nothing, and off the route does not exist.
func TestPolicyEvidenceIsForReadersOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		on           bool
		token, query string
		code         int
	}{
		"a reader":                    {true, "human", "?repository=" + coordPrimary, http.StatusOK},
		"off: no route":               {false, "human", "?repository=" + coordPrimary, http.StatusNotFound},
		"not a reader":                {true, "reader", "?repository=" + coordPrimary, http.StatusNotFound},
		"another repository":          {true, "human", "?repository=" + coordOther, http.StatusNotFound},
		"not configured":              {true, "human", "?repository=Smana/elsewhere", http.StatusNotFound},
		"no repository":               {true, "human", "", http.StatusBadRequest},
		"a sponsored principal":       {true, "slack", "?repository=" + coordPrimary, http.StatusForbidden},
		"unauthenticated":             {true, "", "?repository=" + coordPrimary, http.StatusUnauthorized},
		"GitHub cannot say who reads": {true, "flaky", "?repository=" + coordPrimary, http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			s, f := evidenceServer(tc.on)
			w := getAs(s.Handler(), tc.token, "/v1/policy/evidence"+tc.query)
			if w.Code != tc.code {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if (tc.code == http.StatusOK) != (len(f.asked) == 1) {
				t.Fatalf("read the evidence: %q", f.asked)
			}
		})
	}
}

func TestPolicyEvidenceAnswersTheReport(t *testing.T) {
	s, f := evidenceServer(true)
	w := getAs(s.Handler(), "human", "/v1/policy/evidence?repository="+coordPrimary)
	var r evidence.Report
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || r.Repository != coordPrimary || r.Minimum != 20 {
		t.Fatalf("%s %v", w.Body, err)
	}
	f.err = errors.New("github down")
	s.Evidence = f
	if w := getAs(s.Handler(), "human", "/v1/policy/evidence?repository="+coordPrimary); w.Code != http.StatusServiceUnavailable ||
		w.Body.String() != "{\"error\":\"evidence_unavailable\"}\n" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// The repositories a caller may read, for the settings index.
func TestPolicyRepositoriesListsWhatTheCallerReads(t *testing.T) {
	s, _ := evidenceServer(true)
	w := getAs(s.Handler(), "human", "/v1/policy/repositories")
	if w.Code != http.StatusOK || w.Body.String() != "{\"repositories\":[\""+coordPrimary+"\"]}\n" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := getAs(s.Handler(), "reader", "/v1/policy/repositories"); w.Body.String() != "{\"repositories\":[]}\n" {
		t.Fatalf("%s", w.Body)
	}
}

// Stage 6, one row per role per handler: readers read evidence, repositories and timelines;
// only a maintainer previews a policy; nobody else reaches the editor; off, no route exists.
func TestPolicyHandlersByRole(t *testing.T) {
	preview := `{"repository":"` + coordPrimary + `","file":"factory: aws-0\n"}`
	routes := map[string]struct{ method, path, body string }{
		"evidence":  {http.MethodGet, "/v1/policy/evidence?repository=" + coordPrimary, ""},
		"repos":     {http.MethodGet, "/v1/policy/repositories", ""},
		"decisions": {http.MethodGet, "/v1/policy/decisions?repository=" + coordPrimary + "&pr=12", ""},
		"preview":   {http.MethodPost, "/v1/policy/preview", preview},
	}
	want := map[string]map[string]int{
		"human":      {"evidence": 200, "repos": 200, "decisions": 200, "preview": 200},
		"onlyreader": {"evidence": 200, "repos": 200, "decisions": 200, "preview": 403},
		"reader":     {"evidence": 404, "repos": 200, "decisions": 404, "preview": 404},
		"slack":      {"evidence": 403, "repos": 403, "decisions": 403, "preview": 403},
		"":           {"evidence": 401, "repos": 401, "decisions": 401, "preview": 401},
	}
	for role, codes := range want {
		for name, rt := range routes {
			t.Run(role+" "+name, func(t *testing.T) {
				for _, on := range []bool{true, false} {
					s, _ := evidenceServer(on)
					req := httptest.NewRequestWithContext(context.Background(), rt.method, rt.path, strings.NewReader(rt.body))
					if role != "" {
						req.Header.Set("Authorization", "Bearer "+role)
					}
					w := httptest.NewRecorder()
					s.Handler().ServeHTTP(w, req)
					code := codes[name]
					if !on {
						code = http.StatusNotFound
					}
					if w.Code != code {
						t.Fatalf("on=%v: %d %s, want %d", on, w.Code, w.Body, code)
					}
				}
			})
		}
	}
}
