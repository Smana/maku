// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
)

type fakePolicies struct{}

func (fakePolicies) Policies(ctx context.Context) []evidence.PolicyReport {
	p, _ := fakePolicies{}.Policy(ctx, "dead-links")
	return []evidence.PolicyReport{p}
}

func (fakePolicies) Policy(_ context.Context, name string) (evidence.PolicyReport, error) {
	if name != "dead-links" {
		return evidence.PolicyReport{}, evidence.ErrUnknownPolicy
	}
	return evidence.PolicyReport{Name: name, Generation: 4, Status: v1alpha1.MergePolicyStatus{ConsumerCount: 2,
		Consumers: []v1alpha1.ConsumerStatus{{Repository: "primary"}, {Repository: "other"}}}}, nil
}

// policiesServer binds dead-links in both repositories; "human" reads the primary one only.
func policiesServer(on bool) *Server {
	s, _ := evidenceServer(on)
	cfg := s.Cfg.Current()
	cfg.Repositories[0].Bindings = []config.Binding{{Name: "dead-links", Mode: "shadow"}}
	cfg.Repositories[1].Bindings = []config.Binding{{Name: "dead-links", Mode: "off"}}
	cfg.Generations.Repos = map[string]config.RepoGenerations{
		strings.ToLower(coordPrimary): {RepositoryName: "primary"}, strings.ToLower(coordOther): {RepositoryName: "other"}}
	s.Policies = fakePolicies{}
	return s
}

// B16: the policies a caller reads a consumer of, each with the consumers they read; another
// policy, or one none of whose consumers they read, is a 404 alike; off, no route.
func TestPoliciesForReadersOfAConsumer(t *testing.T) {
	s := policiesServer(true)
	w := getAs(s.Handler(), "human", "/v1/policy/policies/dead-links")
	var p evidence.PolicyReport
	if err := json.Unmarshal(w.Body.Bytes(), &p); w.Code != http.StatusOK || err != nil || p.Generation != 4 {
		t.Fatalf("%d %s %v", w.Code, w.Body, err)
	}
	if len(p.Status.Consumers) != 1 || p.Status.Consumers[0].Repository != "primary" || p.Status.ConsumerCount != 2 {
		t.Fatalf("the consumers the caller reads, the count over all: %+v", p.Status)
	}
	w = getAs(s.Handler(), "human", "/v1/policy/policies")
	var list struct {
		Policies []evidence.PolicyReport `json:"policies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Policies) != 1 || len(list.Policies[0].Status.Consumers) != 1 {
		t.Fatalf("%s %v", w.Body, err)
	}
	for name, tc := range map[string]struct {
		on          bool
		token, path string
		code        int
	}{
		"an unknown policy":     {true, "human", "/v1/policy/policies/nope", http.StatusNotFound},
		"no consumer it reads":  {true, "reader", "/v1/policy/policies/dead-links", http.StatusNotFound},
		"a sponsored principal": {true, "slack", "/v1/policy/policies/dead-links", http.StatusForbidden},
		"unauthenticated":       {true, "", "/v1/policy/policies", http.StatusUnauthorized},
		"off: no route":         {false, "human", "/v1/policy/policies", http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			if w := getAs(policiesServer(tc.on).Handler(), tc.token, tc.path); w.Code != tc.code {
				t.Fatalf("%d %s, want %d", w.Code, w.Body, tc.code)
			}
		})
	}
	if w := getAs(policiesServer(true).Handler(), "reader", "/v1/policy/policies"); w.Body.String() != "{\"policies\":[]}\n" {
		t.Fatalf("%s", w.Body)
	}
}

// B16: the preview takes a binding or a policy edit beside a file; a binding edit is a
// maintainer's, as a file edit is.
func TestPreviewKinds(t *testing.T) {
	post := func(token, body string) *httptest.ResponseRecorder {
		s := policiesServer(true)
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/policy/preview", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	for name, tc := range map[string]struct {
		token, body, want string
		code              int
	}{
		"a binding edit":             {"human", `{"kind":"binding","repository":"` + coordPrimary + `","bindings":[{"name":"docs-links","mode":"live"}]}`, "binding", 200},
		"a binding edit by a reader": {"onlyreader", `{"kind":"binding","repository":"` + coordPrimary + `","bindings":[]}`, "", 403},
		"a policy edit":              {"human", `{"kind":"policy","policy":"dead-links","spec":{"maxPerDay":2}}`, "policy", 200},
		"a policy edit, no name":     {"human", `{"kind":"policy","spec":{}}`, "", 400},
		"an unknown kind":            {"human", `{"kind":"other","repository":"` + coordPrimary + `"}`, "", 400},
		"a file, as before":          {"human", `{"repository":"` + coordPrimary + `","file":"factory: aws-0\n"}`, "file", 200},
	} {
		t.Run(name, func(t *testing.T) {
			w := post(tc.token, tc.body)
			if w.Code != tc.code || (tc.want != "" && !strings.Contains(w.Body.String(), `"kind":"`+tc.want+`"`)) {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		})
	}
}
