// SPDX-License-Identifier: Apache-2.0

package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"

	"github.com/Smana/maku/internal/factory/config"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/hardening"
)

// EvidenceSource is each repository's promotion evidence (merge policy stage 6); *evidence.Service
// implements it.
type EvidenceSource interface {
	Report(ctx context.Context, repository string) (evidence.Report, error)
}

// ReadChecker answers whether a human may read a repository on GitHub, as WriteChecker answers
// write; GitHubAccess implements both.
type ReadChecker interface {
	CanRead(ctx context.Context, p authn.Principal, repository string) (bool, error)
}

// promotionOn is whether policyManagement.promotion serves the evidence routes: off is a 404.
func (s *Server) promotionOn(cfg *config.Config) bool {
	return cfg.PolicyManagement.Promotion && s.Evidence != nil
}

// policyReader is a human caller; sponsored and system principals read no evidence.
func (s *Server) policyReader(w http.ResponseWriter, r *http.Request) (authn.Principal, bool) {
	p, err := s.Auth.Authenticate(r)
	switch {
	case errors.Is(err, authn.ErrForbidden):
		refuse(w, http.StatusForbidden, "not_permitted")
		return p, false
	case err != nil:
		refuse(w, http.StatusUnauthorized, "unauthenticated")
		return p, false
	case p.Sponsored || p.Sub == "":
		refuse(w, http.StatusForbidden, "human_only")
		return p, false
	}
	return p, true
}

// reads is whether p may read repository on GitHub; an error is no answer.
func (s *Server) reads(ctx context.Context, p authn.Principal, repository string) (bool, error) {
	rc, ok := s.Access.(ReadChecker)
	if !ok {
		return false, nil
	}
	return rc.CanRead(ctx, p, repository)
}

// configured is repository as the config spells it, "" when it lists none.
func (s *Server) configured(cfg *config.Config, repository string) string {
	for _, r := range cfg.Repos() {
		if strings.EqualFold(r.Name, repository) {
			return r.Name
		}
	}
	return ""
}

// policyEvidence is GET /v1/policy/evidence?repository=owner/name: the repository's promotion
// evidence, for a reader of it on GitHub (MP17). A repository the caller cannot read, or the
// config does not list, is a 404 alike, so the answer tells no repository's existence.
func (s *Server) policyEvidence(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	asked := r.URL.Query().Get("repository")
	if asked == "" {
		refuse(w, http.StatusBadRequest, "repository_required")
		return
	}
	repo := s.readable(cfg, w, r, p, asked)
	if repo == "" {
		return
	}
	rep, err := s.Evidence.Report(r.Context(), repo)
	if err != nil {
		if s.Log != nil {
			s.Log.WarnContext(r.Context(), "policy evidence unread", "repository", repo, "reason", evidence.Reason(err), "error", err)
		}
		refuse(w, http.StatusServiceUnavailable, "evidence_unavailable")
		return
	}
	if s.Readiness != nil && cfg.Hardening.ModeFor(repo).AtLeast(hardening.ModeAudit) { // H5: the readiness row
		h := readerView(s.readinessOf(r.Context(), repo))
		rep.Hardening = &h
	}
	reply(w, http.StatusOK, rep)
}

// policyRepositories is GET /v1/policy/repositories: the configured repositories the caller reads.
// One that cannot be checked is left out, never shown.
func (s *Server) policyRepositories(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	out := []string{}
	for _, rc := range cfg.Repos() {
		if can, err := s.reads(r.Context(), p, rc.Name); err == nil && can {
			out = append(out, rc.Name)
		}
	}
	reply(w, http.StatusOK, map[string][]string{"repositories": out})
}

// PolicyEditor is the settings editor's check and the decision timeline (stage 6), each answered
// as the JSON document it is; app wires *policyview.Service through it. The documents are any so
// this package does not import the reconciler: intake's tests import this package, and the
// reconciler imports intake.
type PolicyEditor interface {
	Preview(ctx context.Context, repository, file string) (any, error)
	// PreviewBindings and PreviewPolicy are the binding and policy edits (B16): one repository's
	// mergePolicies, and a MergePolicy with its consumers the caller reads (readable).
	PreviewBindings(ctx context.Context, repository string, bindings []config.Binding) (any, error)
	PreviewPolicy(ctx context.Context, name string, spec config.PolicySpec, readable func(repository string) bool) (any, error)
	Timeline(ctx context.Context, repository string, pr int) (any, error)
}

// PolicySource is the class-shape MergePolicies the factory serves (B16); *evidence.Service
// implements it.
type PolicySource interface {
	Policies(ctx context.Context) []evidence.PolicyReport
	Policy(ctx context.Context, name string) (evidence.PolicyReport, error)
}

// bindersOf are the repositories binding policy name, as the config spells them, by the name of
// each Repository object (the status's consumers name those), else the repository's own: those
// whose Repository binds it, and those cut over to a file that binds it (stage 4, B15).
func (s *Server) bindersOf(cfg *config.Config, name string) map[string]string {
	out := map[string]string{}
	binds := func(bs []config.Binding) bool {
		return slices.ContainsFunc(bs, func(b config.Binding) bool { return b.Name == name })
	}
	for _, rc := range cfg.Repos() {
		bound := binds(rc.Bindings)
		if !bound && cfg.RepositoryAuthoritative(&rc) && s.PolicySnapshot != nil {
			if snap, ok := s.PolicySnapshot(rc.Name); ok && snap.File != nil {
				bound = binds(snap.File.MergePolicies)
			}
		}
		if bound {
			out[cmp.Or(cfg.Generations.Repos[strings.ToLower(rc.Name)].RepositoryName, rc.Name)] = rc.Name
		}
	}
	return out
}

// visible is p cut to what the caller reads (B16): the consumers they read, and the counts over them.
// False when they read none: a policy is shown to a reader of one of its consumers, never more,
// so its existence is not told to anyone else. A lookup that fails reads nothing.
func (s *Server) visible(ctx context.Context, cfg *config.Config, pr authn.Principal, p evidence.PolicyReport) (evidence.PolicyReport, bool) {
	reads := map[string]bool{}
	for object, repo := range s.bindersOf(cfg, p.Name) {
		if can, err := s.reads(ctx, pr, repo); err == nil && can {
			reads[object] = true
		}
	}
	if len(reads) == 0 {
		return evidence.PolicyReport{}, false
	}
	// The consumers the caller reads, and the counts over them alone: no count tells of a
	// repository they cannot see.
	p.Status.Consumers = slices.DeleteFunc(slices.Clone(p.Status.Consumers), func(c factoryv1.ConsumerStatus) bool { return !reads[c.Repository] })
	p.Status.ConsumerCount, p.Status.CurrentCount, p.Status.LiveCount = len(p.Status.Consumers), 0, 0
	for _, c := range p.Status.Consumers {
		if c.Accepted && c.ServedGeneration == p.Status.ObservedGeneration {
			p.Status.CurrentCount++
		}
		if c.Mode == config.ModeLive && c.ServedGeneration > 0 {
			p.Status.LiveCount++
		}
	}
	return p, true
}

// policyList is GET /v1/policy/policies: the MergePolicies the caller reads a consumer of (B16).
func (s *Server) policyList(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	out := []evidence.PolicyReport{}
	for _, pol := range s.Policies.Policies(r.Context()) {
		if v, ok := s.visible(r.Context(), cfg, p, pol); ok {
			out = append(out, v)
		}
	}
	reply(w, http.StatusOK, map[string][]evidence.PolicyReport{"policies": out})
}

// policyGet is GET /v1/policy/policies/{name}: one MergePolicy, its definition, manifest and the
// consumers the caller reads (B16). An unknown policy and one the caller reads no consumer of are a
// 404 alike.
func (s *Server) policyGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	pol, err := s.Policies.Policy(r.Context(), r.PathValue("name"))
	if err != nil {
		refuse(w, http.StatusNotFound, "not_found")
		return
	}
	v, ok := s.visible(r.Context(), cfg, p, pol)
	if !ok {
		refuse(w, http.StatusNotFound, "not_found")
		return
	}
	reply(w, http.StatusOK, v)
}

// maxPreviewBody bounds a proposed policy: the file's own bound and its envelope.
const maxPreviewBody = mergepolicy.MaxFileBytes + 4<<10

// readable answers the caller's read of repository, refusing for them when not: the repository as
// the config spells it, or "".
func (s *Server) readable(cfg *config.Config, w http.ResponseWriter, r *http.Request, p authn.Principal, asked string) string {
	repo := s.configured(cfg, asked)
	if repo == "" {
		refuse(w, http.StatusNotFound, "not_found")
		return ""
	}
	switch can, err := s.reads(r.Context(), p, repo); {
	case err != nil:
		refuse(w, http.StatusServiceUnavailable, reasonLinkUnverified)
		return ""
	case !can:
		refuse(w, http.StatusNotFound, "not_found")
		return ""
	}
	return repo
}

// policyPreview is POST /v1/policy/preview {repository, file}: a maintainer's proposed policy file,
// checked strictly, compared structurally and simulated (MP8, MP16). It writes nothing: the answer
// is the file to submit and the GitHub page to submit it from.
func (s *Server) policyPreview(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	var in struct {
		// Kind is file (the default), binding or policy (B16).
		Kind       string             `json:"kind"`
		Repository string             `json:"repository"`
		File       string             `json:"file"`
		Bindings   []config.Binding   `json:"bindings"`
		Policy     string             `json:"policy"`
		Spec       *config.PolicySpec `json:"spec"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPreviewBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	switch in.Kind {
	case "", "file", "binding":
	case "policy":
		// A policy is central: its preview writes nothing and shows only the consumers the caller
		// reads, so any reader of the settings may run it.
		if in.Policy == "" || in.Spec == nil {
			refuse(w, http.StatusBadRequest, "policy_required")
			return
		}
		readable := func(repo string) bool { can, err := s.reads(r.Context(), p, repo); return err == nil && can }
		pv, err := s.Editor.PreviewPolicy(r.Context(), in.Policy, *in.Spec, readable)
		if err != nil {
			refuse(w, http.StatusServiceUnavailable, "evidence_unavailable")
			return
		}
		reply(w, http.StatusOK, pv)
		return
	default:
		refuse(w, http.StatusBadRequest, "kind_unknown")
		return
	}
	repo := s.readable(cfg, w, r, p, in.Repository)
	if repo == "" {
		return
	}
	switch can, err := s.Access.CanWrite(r.Context(), p, repo); {
	case err != nil:
		refuse(w, http.StatusServiceUnavailable, reasonLinkUnverified)
		return
	case !can:
		refuse(w, http.StatusForbidden, "not_a_maintainer")
		return
	}
	var pv any
	var err error
	if in.Kind == "binding" {
		pv, err = s.Editor.PreviewBindings(r.Context(), repo, in.Bindings)
	} else {
		pv, err = s.Editor.Preview(r.Context(), repo, in.File)
	}
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "evidence_unavailable")
		return
	}
	reply(w, http.StatusOK, pv)
}

// policyDecisions is GET /v1/policy/decisions?repository=owner/name&pr=N: the pull request's
// decision timeline, for a reader of the repository.
func (s *Server) policyDecisions(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, ok := s.policyReader(w, r)
	if !ok {
		return
	}
	pr, err := strconv.Atoi(r.URL.Query().Get("pr"))
	if err != nil || pr < 1 {
		refuse(w, http.StatusBadRequest, "pr_required")
		return
	}
	repo := s.readable(cfg, w, r, p, r.URL.Query().Get("repository"))
	if repo == "" {
		return
	}
	tl, err := s.Editor.Timeline(r.Context(), repo, pr)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "decisions_unavailable")
		return
	}
	reply(w, http.StatusOK, tl)
}
