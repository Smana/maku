// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/promote"
)

func (s *Server) policyMaintainer(w http.ResponseWriter, r *http.Request, p authn.Principal, repository string) string {
	cfg := s.Cfg.Current()
	repo := s.readable(cfg, w, r, p, repository)
	if repo == "" {
		return ""
	}
	switch can, err := s.Access.CanWrite(r.Context(), p, repo); {
	case err != nil:
		refuse(w, http.StatusServiceUnavailable, reasonLinkUnverified)
		return ""
	case !can:
		refuse(w, http.StatusForbidden, "not_a_maintainer")
		return ""
	}
	return repo
}

func decodePromotion(w http.ResponseWriter, r *http.Request, in any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(in); err != nil || dec.More() {
		refuse(w, http.StatusBadRequest, "bad_json")
		return false
	}
	return true
}

// policyPromote opens a one-line promotion pull request for a maintainer.
func (s *Server) policyPromote(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	var in struct {
		Repository string `json:"repository"`
		Class      string `json:"class"`
		Minimum    int    `json:"minimum"`
	}
	if !decodePromotion(w, r, &in) {
		return
	}
	repo := s.policyMaintainer(w, r, p, in.Repository)
	if repo == "" {
		return
	}
	out, err := s.Editor.Promote(r.Context(), repo, in.Class, in.Minimum)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "promotion_unavailable")
		return
	}
	reply(w, http.StatusOK, out)
}

// policyDrill opens a labelled rollback-drill revert for a maintainer.
func (s *Server) policyDrill(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	var in struct {
		Repository  string `json:"repository"`
		PullRequest int    `json:"pullRequest"`
	}
	if !decodePromotion(w, r, &in) {
		return
	}
	if in.PullRequest < 1 {
		refuse(w, http.StatusBadRequest, "pr_required")
		return
	}
	repo := s.policyMaintainer(w, r, p, in.Repository)
	if repo == "" {
		return
	}
	out, err := s.Editor.Drill(r.Context(), repo, in.PullRequest)
	if err != nil {
		code, reason := drillRefusal(err)
		if s.Log != nil {
			s.Log.ErrorContext(r.Context(), "policy drill: "+reason, "repository", repo, "pullRequest", in.PullRequest, "error", err)
		}
		refuse(w, code, reason)
		return
	}
	reply(w, http.StatusOK, out)
}

// drillRefusal names why a drill opened nothing, so `maku policy drill` says which: a pull request
// that is not merged, a repository the factory cannot reach, or GitHub refusing the revert.
func drillRefusal(err error) (int, string) {
	switch {
	case errors.Is(err, promote.ErrNotMerged):
		return http.StatusConflict, "drill_not_merged"
	case errors.Is(err, promote.ErrUnknownRepository):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, promote.ErrNoConnection):
		return http.StatusServiceUnavailable, "drill_no_connection"
	case errors.Is(err, promote.ErrNoMerger):
		return http.StatusServiceUnavailable, "drill_no_merger"
	}
	return http.StatusBadGateway, "drill_revert_failed"
}

// policyPromotionStatus reads the PR and served-mode facts that close a promotion record.
func (s *Server) policyPromotionStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	number, err := strconv.Atoi(r.URL.Query().Get("pr"))
	if err != nil || number < 1 {
		refuse(w, http.StatusBadRequest, "pr_required")
		return
	}
	repo := s.readable(s.Cfg.Current(), w, r, p, r.URL.Query().Get("repository"))
	if repo == "" {
		return
	}
	out, err := s.Editor.PromotionStatus(r.Context(), repo, r.URL.Query().Get("class"), number)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "promotion_unavailable")
		return
	}
	reply(w, http.StatusOK, out)
}
