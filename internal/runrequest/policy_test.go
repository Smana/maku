// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Stage 6: the evidence is read with the caller's own token, never without one, and a refusal
// keeps its bounded reason.
func TestPolicyReadsForwardTheCallersToken(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/policy/repositories":
			_, _ = w.Write([]byte(`{"repositories":["Smana/cloud-native-ref"]}`))
		case "/v1/policy/evidence":
			if r.URL.Query().Get("repository") != "Smana/cloud-native-ref" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"not_found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"repository":"Smana/cloud-native-ref","minimum":20,"classes":[{"class":"docs-links","mode":"shadow"}]}`))
		}
	}))
	defer srv.Close()
	f := Factory{URL: srv.URL, HC: srv.Client()}
	if _, err := f.PolicyEvidence(t.Context(), "", "Smana/cloud-native-ref"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("no token: %v", err)
	}
	r, err := f.PolicyEvidence(t.Context(), "tok", "Smana/cloud-native-ref")
	if err != nil || r.Minimum != 20 || r.Classes[0].Class != "docs-links" || seen[0] != "GET /v1/policy/evidence?repository=Smana%2Fcloud-native-ref Bearer tok" {
		t.Fatalf("%+v %v %q", r, err, seen)
	}
	var ref ErrRefused
	if _, err := f.PolicyEvidence(t.Context(), "tok", "Smana/other"); !errors.As(err, &ref) || ref.Status != http.StatusNotFound || ref.Reason != "not_found" {
		t.Fatalf("%v", err)
	}
	if rs, err := f.PolicyRepositories(t.Context(), "tok"); err != nil || len(rs) != 1 {
		t.Fatalf("%q %v", rs, err)
	}
}
