// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/factory/config"
)

type forbiddenAuth struct{}

func (forbiddenAuth) Authenticate(*http.Request) (authn.Principal, error) {
	return authn.Principal{}, authn.ErrForbidden
}

// flakyLogins fails the way ZITADEL does when it cannot answer.
type flakyLogins struct{}

func (flakyLogins) Login(context.Context, string, string) (string, error) {
	return "", errors.New("zitadel down")
}

func catalogueServer(promotion bool) *Server {
	s, _ := evidenceServer(promotion)
	s.Auth = byToken{
		"human":    coordHuman,
		"nonmaint": authn.Principal{Kind: envelope.ActorHuman, ID: "human:5", Sub: "5"},
		"reader":   coordReader,
		"slack":    coordSlack,
		"noapp":    authn.Principal{Kind: envelope.ActorSystem, ID: "system:other", Sponsored: true},
		"internal": authn.Principal{Kind: envelope.ActorSystem, ID: "system:slack-app", Sponsored: true, Internal: true},
		"machine":  authn.Principal{Kind: envelope.ActorSystem, ID: "system:x"},
	}
	s.Logins = coordLogins{"291": "Smana", "5": "someone"}
	s.Cfg = &config.Config{
		PolicyManagement: config.PolicyManagement{Promotion: promotion},
		Defaults:         config.Defaults{Tier: "standard"},
		Tiers:            map[string]config.Tier{"standard": {TaskUSD: 4}},
		Templates:        map[string]config.Template{"pair": {Roles: []string{"implementer", "reviewer"}}},
		HarnessProfiles:  map[string]config.HarnessProfile{"floor": {Template: "pair"}},
		Repositories: []config.Repository{
			{Name: coordPrimary, Maintainers: []string{"smana"}, Classes: map[string]config.Class{
				"review": {Harness: "floor", Description: "Anything else"}}},
			{Name: coordOther},
		},
		API: config.API{Sponsored: []config.Sponsored{{Name: "slack-app", Sponsor: "Smana", Repositories: []string{coordPrimary},
			OnBehalfOf: config.OnBehalfOf{Slack: map[string]string{"T01ABC/U02DEF": "Smana", "T01ABC/U03GHI": "someone"}}}}},
	}
	return s
}

func TestCatalogueRoute(t *testing.T) {
	path := "/v1/catalogue/" + coordPrimary
	for name, tc := range map[string]struct {
		promotion    bool
		token, path  string
		code         int
		maintainer   bool
		reason       string
		wantSpelling bool
	}{
		"a reader who maintains":              {true, "human", path, http.StatusOK, true, "", true},
		"a reader who does not maintain":      {true, "nonmaint", path, http.StatusOK, false, "", true},
		"mixed case path, configured spell":   {true, "human", "/v1/catalogue/smana/PUBLIC-contracts", http.StatusOK, true, "", true},
		"promotion off, still served":         {false, "human", path, http.StatusOK, true, "", true},
		"unserved repository names nothing":   {true, "human", "/v1/catalogue/Smana/elsewhere", http.StatusNotFound, false, "not_found", false},
		"served but not readable by caller":   {true, "reader", path, http.StatusNotFound, false, "not_found", false},
		"unauthenticated":                     {true, "", path, http.StatusUnauthorized, false, "unauthenticated", false},
		"sponsored without onBehalfOf":        {true, "slack", path, http.StatusForbidden, false, "human_only", false},
		"sponsored for a linked maintainer":   {true, "slack", path + "?onBehalfOf=slack:T01ABC/U02DEF", http.StatusOK, true, "", true},
		"sponsored for a non-maintainer":      {true, "slack", path + "?onBehalfOf=slack:T01ABC/U03GHI", http.StatusOK, false, "", true},
		"sponsored for an unlinked user":      {true, "slack", path + "?onBehalfOf=slack:T01ABC/UNONE", http.StatusForbidden, false, "not_linked", false},
		"sponsored, not a slack link":         {true, "slack", path + "?onBehalfOf=github:smana", http.StatusForbidden, false, "not_linked", false},
		"sponsored outside its repositories":  {true, "slack", "/v1/catalogue/" + coordOther + "?onBehalfOf=slack:T01ABC/U02DEF", http.StatusNotFound, false, "not_found", false},
		"sponsored case-insensitive repo":     {true, "slack", "/v1/catalogue/smana/PUBLIC-contracts?onBehalfOf=slack:T01ABC/U02DEF", http.StatusOK, true, "", true},
		"an internal principal":               {true, "internal", path + "?onBehalfOf=slack:T01ABC/U02DEF", http.StatusForbidden, false, "human_only", false},
		"a sponsored principal not in config": {true, "noapp", path + "?onBehalfOf=slack:T01ABC/U02DEF", http.StatusForbidden, false, "human_only", false},
		"a machine without a sub":             {true, "machine", path, http.StatusForbidden, false, "human_only", false},
	} {
		t.Run(name, func(t *testing.T) {
			w := getAs(catalogueServer(tc.promotion).Handler(), tc.token, tc.path)
			if w.Code != tc.code {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if tc.code != http.StatusOK {
				if tc.reason != "" && !strings.Contains(w.Body.String(), `"`+tc.reason+`"`) {
					t.Fatalf("body %s: want reason %s", w.Body, tc.reason)
				}
				return
			}
			var c catalogue.Catalogue
			if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
				t.Fatal(err)
			}
			if c.Repository != coordPrimary || c.Maintainer != tc.maintainer {
				t.Fatalf("repository %q maintainer %t: %s", c.Repository, c.Maintainer, w.Body)
			}
			if _, ok := c.Class("review"); !ok || c.Profiles["floor"].Roles == nil {
				t.Fatalf("the Task 1.1 body is missing: %s", w.Body)
			}
			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q", got)
			}
		})
	}
}

func TestCatalogueRouteAuthFailures(t *testing.T) {
	s := catalogueServer(true)
	s.Auth = forbiddenAuth{}
	if w := getAs(s.Handler(), "x", "/v1/catalogue/"+coordPrimary); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), "not_permitted") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s = catalogueServer(true)
	s.Logins = flakyLogins{}
	if w := getAs(s.Handler(), "human", "/v1/catalogue/"+coordPrimary); w.Code != http.StatusServiceUnavailable ||
		!strings.Contains(w.Body.String(), reasonLinkUnverified) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s = catalogueServer(true)
	s.Logins = nil // no resolver: nobody is a maintainer, the catalogue still reads
	if w := getAs(s.Handler(), "human", "/v1/catalogue/"+coordPrimary); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"maintainer":true`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
