// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The catalogue is read with the caller's own token, never without one; the repository is escaped
// into the path and onBehalfOf into the query; a refusal keeps its bounded reason.
func TestCatalogueClient(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery+" "+r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/catalogue/Smana/cloud-native-ref":
			_, _ = w.Write([]byte(`{"repository":"Smana/cloud-native-ref","maintainer":true,` +
				`"classes":[{"name":"docs-links","mode":"live","floor":"links-solo","allowed":[],"ceilingUSD":2,"description":"","escalation":null}],` +
				`"profiles":{"links-solo":{"template":"solo","roles":["implementer"],"offered":true}}}`))
		case "/v1/catalogue/Smana/forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"human_only"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		}
	}))
	defer srv.Close()
	f := Factory{URL: srv.URL, HC: srv.Client()}

	if _, err := f.Catalogue(t.Context(), "", "Smana/cloud-native-ref", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("no token: %v", err)
	}
	c, err := f.Catalogue(t.Context(), "tok", "Smana/cloud-native-ref", "")
	if err != nil || !c.Maintainer || len(c.Classes) != 1 || c.Classes[0].CeilingUSD != 2 || !c.Profiles["links-solo"].Offered ||
		seen[0] != "GET /v1/catalogue/Smana/cloud-native-ref? Bearer tok" {
		t.Fatalf("%+v %v %q", c, err, seen)
	}
	if _, err := f.Catalogue(t.Context(), "tok", "Smana/cloud-native-ref", "slack:T01/U02"); err != nil ||
		seen[1] != "GET /v1/catalogue/Smana/cloud-native-ref?onBehalfOf=slack%3AT01%2FU02 Bearer tok" {
		t.Fatalf("%v %q", err, seen)
	}
	var ref ErrRefused
	if _, err := f.Catalogue(t.Context(), "tok", "Smana/elsewhere", ""); !errors.As(err, &ref) || ref.Status != http.StatusNotFound || ref.Reason != "not_found" {
		t.Fatalf("404: %v", err)
	}
	if _, err := f.Catalogue(t.Context(), "tok", "Smana/forbidden", ""); !errors.As(err, &ref) || ref.Status != http.StatusForbidden || ref.Reason != "human_only" {
		t.Fatalf("403: %v", err)
	}
	if _, err := f.Catalogue(t.Context(), "tok", "Smana/we ird?#", ""); !errors.As(err, &ref) {
		t.Fatalf("an odd name must stay one escaped segment: %v", err)
	}
	if got := seen[len(seen)-1]; got != "GET /v1/catalogue/Smana/we%20ird%3F%23? Bearer tok" {
		t.Fatalf("path = %q", got)
	}
}
