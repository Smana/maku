// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/Smana/maku/internal/factory/grant"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// Grants is the store's saved approval grants, as the factory reaches them (merge policy stage 5);
// *store.Store implements it.
type Grants interface {
	LookupGrants(ctx context.Context, repository, class, policyHash string, maxLifetime time.Duration, actor string) ([]store.Grant, []string, error)
	MoveGrant(ctx context.Context, id string, m store.GrantMove) (store.Grant, error)
}

// maxGrantBody bounds a grant route's body: a few ids and a reason.
const maxGrantBody = 4 << 10

var (
	grantRepositoryRE = regexp.MustCompile(`^[a-z0-9_.-]{1,100}/[a-z0-9_.-]{1,100}$`)
	grantClassRE      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`)
	policyHashRE      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	headRE            = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// grantCaller admits GrantClient alone (the factory, system:factory) to the grant routes, under
// its limits. release is called once the request is served.
func (s *Server) grantCaller(w http.ResponseWriter, r *http.Request) (release func(), ok bool) {
	p, ok := s.systemAuth(w, r)
	if !ok {
		return nil, false
	}
	if s.Grants == nil || s.GrantClient == "" || p.ID != s.GrantClient {
		fail(w, http.StatusForbidden, wire.ReasonNotPermitted)
		return nil, false
	}
	return s.admit(w, p.ID)
}

// wireGrant is a stored grant as the factory reads it.
func wireGrant(g store.Grant) grant.Grant {
	return grant.Grant{ID: g.ID, Version: g.Version, Repository: g.Repository, Class: g.Class, Files: g.Files,
		DiffShape: g.DiffShape, PolicyHash: g.PolicyHash, Status: g.Status, ExpiresAt: g.ExpiresAt}
}

// grantLookup: POST /v1/grants/lookup {repository, class, policyHash, maxLifetimeSeconds}, the
// factory's read immediately before a decision. It expires and suspends under the row locks
// (store.LookupGrants), then answers the active grants and the events it recorded.
func (s *Server) grantLookup(w http.ResponseWriter, r *http.Request) {
	release, ok := s.grantCaller(w, r)
	if !ok {
		return
	}
	defer release()
	var in struct {
		Repository         string `json:"repository"`
		Class              string `json:"class"`
		PolicyHash         string `json:"policyHash"`
		MaxLifetimeSeconds int64  `json:"maxLifetimeSeconds"`
	}
	if err := decodeStrict(r.Body, &in); err != nil || !grantRepositoryRE.MatchString(in.Repository) ||
		!grantClassRE.MatchString(in.Class) || !policyHashRE.MatchString(in.PolicyHash) ||
		in.MaxLifetimeSeconds <= 0 || in.MaxLifetimeSeconds > int64((366*24*time.Hour).Seconds()) {
		fail(w, http.StatusBadRequest, wire.ReasonBadGrant)
		return
	}
	active, events, err := s.Grants.LookupGrants(r.Context(), in.Repository, in.Class, in.PolicyHash,
		time.Duration(in.MaxLifetimeSeconds)*time.Second, s.GrantClient)
	if err != nil {
		s.grantFailure(w, err, "look up approval grants")
		return
	}
	out := struct {
		Grants []grant.Grant `json:"grants"`
		Events []string      `json:"events"`
	}{Grants: []grant.Grant{}, Events: []string{}}
	for _, g := range active {
		out.Grants = append(out.Grants, wireGrant(g))
	}
	out.Events = append(out.Events, events...)
	reply(w, http.StatusOK, out)
}

// grantUse: POST /v1/grants/{id}/use {version, pr, head}, once the factory merged on the grant.
func (s *Server) grantUse(w http.ResponseWriter, r *http.Request) {
	s.grantMove(w, r, grant.EventUsed)
}

// grantDisable: POST /v1/grants/{id}/disable {pr, reason}, once the factory reverted a merge the
// grant authorized: disabled for good (merge policy, saved grants).
func (s *Server) grantDisable(w http.ResponseWriter, r *http.Request) {
	s.grantMove(w, r, grant.EventDisabled)
}

func (s *Server) grantMove(w http.ResponseWriter, r *http.Request, event string) {
	release, ok := s.grantCaller(w, r)
	if !ok {
		return
	}
	defer release()
	var in struct {
		Version int    `json:"version"`
		PR      int    `json:"pr"`
		Head    string `json:"head"`
		Reason  string `json:"reason"`
	}
	id := r.PathValue("id")
	if err := decodeStrict(r.Body, &in); err != nil || !grant.IDRE.MatchString(id) || in.Version < 0 || in.PR < 1 ||
		(in.Head != "" && !headRE.MatchString(in.Head)) || len(in.Reason) > grant.MaxReasonBytes ||
		(event == grant.EventUsed && (in.Version < 1 || in.Head == "")) {
		fail(w, http.StatusBadRequest, wire.ReasonBadGrant)
		return
	}
	g, err := s.Grants.MoveGrant(r.Context(), id, store.GrantMove{Event: event, Actor: s.GrantClient, Version: in.Version,
		PR: in.PR, Head: in.Head, Reason: in.Reason})
	if err != nil {
		s.grantFailure(w, err, "move an approval grant", "grant", id, "event", event)
		return
	}
	reply(w, http.StatusOK, wireGrant(g))
}

// grantFailure maps the grant sentinels to their status in one place.
func (s *Server) grantFailure(w http.ResponseWriter, err error, msg string, attrs ...any) {
	switch {
	case errors.Is(err, store.ErrNoGrant):
		fail(w, http.StatusNotFound, wire.ReasonNoGrant)
	case errors.Is(err, store.ErrGrantState), errors.Is(err, store.ErrGrantVersion):
		fail(w, http.StatusConflict, wire.ReasonGrantState)
	default:
		s.logFailure(w, err, msg, attrs...)
	}
}
