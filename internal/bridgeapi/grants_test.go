// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/grant"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// fakeGrants is the store's grants as the routes see them: one active grant, and the moves made.
type fakeGrants struct {
	moves  []store.GrantMove
	lookup struct {
		repository, class, hash string
		maxLifetime             time.Duration
	}
}

const (
	liveGrant = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	gone      = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	moved     = "cccccccccccccccccccccccccc"
	brokenID  = "dddddddddddddddddddddddddd"
	fullHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	headSHA   = "0123456789abcdef0123456789abcdef01234567"
)

func (f *fakeGrants) LookupGrants(_ context.Context, repository, class, hash string, maxLifetime time.Duration, _ string) ([]store.Grant, []string, error) {
	f.lookup.repository, f.lookup.class, f.lookup.hash, f.lookup.maxLifetime = repository, class, hash, maxLifetime
	if class == "down" {
		return nil, nil, errors.New("store down")
	}
	return []store.Grant{{ID: liveGrant, Version: 2, Repository: repository, Class: class, Files: []string{"a"},
		PolicyHash: hash[:12], Status: "active", ExpiresAt: time.Unix(2e9, 0).UTC()}}, []string{"suspended"}, nil
}

func (f *fakeGrants) MoveGrant(_ context.Context, id string, m store.GrantMove) (store.Grant, error) {
	switch id {
	case gone:
		return store.Grant{}, store.ErrNoGrant
	case moved:
		return store.Grant{}, store.ErrGrantVersion
	case brokenID:
		return store.Grant{}, errors.New("store down")
	}
	f.moves = append(f.moves, m)
	return store.Grant{ID: id, Version: 2, Status: "disabled"}, nil
}

// Merge policy stage 5: the factory alone looks grants up and moves them. Every other caller is
// refused, a malformed request is a 400 bad_grant, a version since left a 409 grant_state.
func TestTheGrantRoutesAreTheFactorys(t *testing.T) {
	s, _, _ := newServer(t)
	g := &fakeGrants{}
	s.Grants, s.GrantClient = g, factory
	h := s.Routes()
	rec := call(t, h, http.MethodPost, "/v1/grants/lookup", "sys:"+factory,
		map[string]any{"repository": "smana/docs", "class": "docs-broad", "policyHash": fullHash, "maxLifetimeSeconds": 3600})
	var out struct {
		Grants []grant.Grant `json:"grants"`
		Events []string      `json:"events"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Grants) != 1 ||
		out.Grants[0].ID != liveGrant || out.Grants[0].Version != 2 || strings.Join(out.Events, ",") != "suspended" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if g.lookup.hash != fullHash || g.lookup.maxLifetime != time.Hour {
		t.Fatalf("%+v", g.lookup)
	}
	rec = call(t, h, http.MethodPost, "/v1/grants/"+liveGrant+"/disable", "sys:"+factory, map[string]any{"pr": 12, "reason": "main_red"})
	if rec.Code != http.StatusOK || len(g.moves) != 1 || g.moves[0].Event != "disabled" || g.moves[0].Actor != factory || g.moves[0].PR != 12 {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body, g.moves)
	}
	rec = call(t, h, http.MethodPost, "/v1/grants/"+liveGrant+"/use", "sys:"+factory, map[string]any{"version": 2, "pr": 12, "head": headSHA})
	if rec.Code != http.StatusOK || len(g.moves) != 2 || g.moves[1].Event != "used" || g.moves[1].Version != 2 || g.moves[1].Head != headSHA {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body, g.moves)
	}
	lookup := func(class string) map[string]any {
		return map[string]any{"repository": "smana/docs", "class": class, "policyHash": fullHash, "maxLifetimeSeconds": 3600}
	}
	for name, c := range map[string]struct {
		path, token string
		body        any
		code        int
		reason      string
	}{
		"another system principal": {"/v1/grants/lookup", "sys:system:runlore", lookup("docs"), http.StatusForbidden, wire.ReasonNotPermitted},
		"no token":                 {"/v1/grants/lookup", "", lookup("docs"), http.StatusUnauthorized, wire.ReasonUnauthenticated},
		"a short hash": {"/v1/grants/lookup", "sys:" + factory,
			map[string]any{"repository": "smana/docs", "class": "docs", "policyHash": fullHash[:12], "maxLifetimeSeconds": 3600}, http.StatusBadRequest, wire.ReasonBadGrant},
		"an uppercase repository": {"/v1/grants/lookup", "sys:" + factory,
			map[string]any{"repository": "Smana/docs", "class": "docs", "policyHash": fullHash, "maxLifetimeSeconds": 3600}, http.StatusBadRequest, wire.ReasonBadGrant},
		"no lifetime": {"/v1/grants/lookup", "sys:" + factory,
			map[string]any{"repository": "smana/docs", "class": "docs", "policyHash": fullHash}, http.StatusBadRequest, wire.ReasonBadGrant},
		"an unknown field": {"/v1/grants/" + liveGrant + "/disable", "sys:" + factory, map[string]any{"pr": 1, "status": "active"}, http.StatusBadRequest, wire.ReasonBadGrant},
		"a bad id":         {"/v1/grants/x/disable", "sys:" + factory, map[string]any{"pr": 1}, http.StatusBadRequest, wire.ReasonBadGrant},
		"a use unversioned": {"/v1/grants/" + liveGrant + "/use", "sys:" + factory, map[string]any{"pr": 1, "head": headSHA},
			http.StatusBadRequest, wire.ReasonBadGrant},
		"no such grant":   {"/v1/grants/" + gone + "/disable", "sys:" + factory, map[string]any{"pr": 1}, http.StatusNotFound, wire.ReasonNoGrant},
		"a version left":  {"/v1/grants/" + moved + "/use", "sys:" + factory, map[string]any{"version": 1, "pr": 1, "head": headSHA}, http.StatusConflict, wire.ReasonGrantState},
		"the store down":  {"/v1/grants/" + brokenID + "/disable", "sys:" + factory, map[string]any{"pr": 1}, http.StatusServiceUnavailable, wire.ReasonLogUnavailable},
		"a lookup failed": {"/v1/grants/lookup", "sys:" + factory, lookup("down"), http.StatusServiceUnavailable, wire.ReasonLogUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(t, h, http.MethodPost, c.path, c.token, c.body)
			if rec.Code != c.code || reason(t, rec) != c.reason {
				t.Fatalf("%d %s, want %d %s", rec.Code, reason(t, rec), c.code, c.reason)
			}
		})
	}
	s.GrantClient = ""
	if rec := call(t, s.Routes(), http.MethodPost, "/v1/grants/lookup", "sys:"+factory, lookup("docs")); rec.Code != http.StatusForbidden {
		t.Fatalf("off: %d", rec.Code)
	}
}
