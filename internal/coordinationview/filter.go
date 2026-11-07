// SPDX-License-Identifier: Apache-2.0

// Package coordinationview is the broker's per-viewer reading of a coordination parent (v4 design
// MR13): the core's unfiltered CoordinationView goes in, and only what this viewer may read of
// each child comes out. It is the one place a human projection of a family is sanitized; every
// renderer downstream (summary, roomview, the web rail, Slack) consumes its View and never the
// core's.
package coordinationview

import (
	"context"
	"slices"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
)

// View is a filtered coordination view: every core field, minus what a restricted child would
// reveal, plus how many children were withheld. The embedding keeps the core JSON unchanged.
type View struct {
	coordination.CoordinationView
	RestrictedChildren int `json:"restrictedChildren"`
}

// RepositoryReader is the viewer's GitHub standing: their linked login for a repository, and
// whether that login may read it (D7). ghidentity.Resolver and repoaccess.Checker satisfy it.
type RepositoryReader interface {
	Login(ctx context.Context, sub, repository string) (string, error)
	CanRead(ctx context.Context, repository, login string) (bool, error)
}

// Viewer is who reads. A sponsored principal has no GitHub login: its configured repositories
// are its whole reach. There is deliberately no admin flag: agents-admin confers no child access.
type Viewer struct {
	Sub          string
	Sponsored    bool
	Repositories map[string]bool
}

// Filter is raw as viewer may read it. Each child is checked on its own repository; a missing
// login, a false answer or any error restricts it (fail closed). A restricted child contributes
// only to RestrictedChildren: its record, every reference to it, and whatever family-wide field
// could reveal it by prose or subtraction are removed. raw is never modified.
func Filter(ctx context.Context, raw coordination.CoordinationView, viewer Viewer, access RepositoryReader) View {
	restricted := map[string]bool{}
	for id, repo := range childRepositories(raw) {
		if !mayRead(ctx, viewer, access, repo) {
			restricted[id] = true
		}
	}
	v := deepCopy(raw)
	if len(restricted) == 0 {
		return View{CoordinationView: v}
	}
	return View{CoordinationView: sanitize(v, restricted), RestrictedChildren: len(restricted)}
}

// childRepositories is every child id the view names, plan or live, with its repository; "" when
// none is known, which restricts it.
func childRepositories(v coordination.CoordinationView) map[string]string {
	repos := map[string]string{}
	for _, c := range v.Plan.Children {
		repos[c.ID] = c.Repository
	}
	for _, c := range v.Children {
		prev, ok := repos[c.ID]
		switch {
		case !ok:
			repos[c.ID] = c.Repository
		case prev != c.Repository:
			// A plan and a live child disagreeing on the repository: neither is trusted alone.
			repos[c.ID] = ""
		}
	}
	return repos
}

func mayRead(ctx context.Context, viewer Viewer, access RepositoryReader, repo string) bool {
	if repo == "" {
		return false
	}
	if viewer.Sponsored {
		return viewer.Repositories[repo]
	}
	login, err := access.Login(ctx, viewer.Sub, repo)
	if err != nil || login == "" {
		return false
	}
	ok, err := access.CanRead(ctx, repo, login)
	return err == nil && ok
}

func sanitize(v coordination.CoordinationView, r map[string]bool) coordination.CoordinationView {
	keep := func(ids []string) []string {
		return slices.DeleteFunc(ids, func(id string) bool { return r[id] })
	}

	p := &v.Plan
	p.Children = slices.DeleteFunc(p.Children, func(c v1alpha1.CoordinationChild) bool { return r[c.ID] })
	for i := range p.Children {
		c := &p.Children[i]
		c.DependsOn = keep(c.DependsOn)
		c.Consumes = slices.DeleteFunc(c.Consumes, func(a v1alpha1.ArtifactConsumer) bool { return r[a.From] })
	}
	p.MergeOrder = keep(p.MergeOrder)
	p.Budget = v1alpha1.CoordinationPlanBudget{}
	p.Primary.Rationale = ""
	p.Integration.Acceptance = nil
	p.Integration.BeforeDependentsRationale = ""

	v.ChildAuthorizations = slices.DeleteFunc(v.ChildAuthorizations, func(a coordination.ChildAuthorization) bool { return r[a.ChildID] })
	v.Children = slices.DeleteFunc(v.Children, func(c coordination.CoordinationChildView) bool { return r[c.ID] })
	for i := range v.Children {
		c := &v.Children[i]
		c.DependsOn = keep(c.DependsOn)
		c.Consumes = slices.DeleteFunc(c.Consumes, func(ref string) bool {
			from, _, _ := strings.Cut(ref, "/")
			return r[from]
		})
		// An acceptance or a release is a family-wide fact: it may rest on the restricted child.
		c.IntegrationAcceptedHead = ""
		c.IntegrationRelease = nil
	}
	v.Artifacts = slices.DeleteFunc(v.Artifacts, func(a v1alpha1.LandedArtifact) bool { return r[a.ChildID] })

	kept := map[string]bool{}
	v.IntegrationAttempts = slices.DeleteFunc(v.IntegrationAttempts, func(a coordination.IntegrationAttemptView) bool {
		touches := slices.ContainsFunc(a.Subjects, func(s v1alpha1.IntegrationSubject) bool { return r[s.ChildID] })
		if !touches {
			kept[a.Task] = true
		}
		return touches
	})
	v.MergeOrder = keep(v.MergeOrder)
	v.Budget = coordination.CoordinationBudgetView{}
	if rec := v.Recovery; rec != nil && (r[rec.ChildID] || (rec.IntegrationTask != "" && !kept[rec.IntegrationTask])) {
		v.Recovery = nil
	}
	v.PostCompletionIncidents = slices.DeleteFunc(v.PostCompletionIncidents, func(i coordination.CoordinationIncident) bool { return r[i.ChildID] })
	if len(v.PostCompletionIncidents) == 0 {
		v.PostCompletionIncidents = nil
	}
	return v
}

// deepCopy copies every slice and pointer of v, so sanitizing never reaches the caller's value.
func deepCopy(v coordination.CoordinationView) coordination.CoordinationView {
	out := v
	out.Plan = *v.Plan.DeepCopy()
	if v.ParentConfirmation != nil {
		out.ParentConfirmation = v.ParentConfirmation.DeepCopy()
	}
	out.ChildAuthorizations = cloneEach(v.ChildAuthorizations, func(a coordination.ChildAuthorization) coordination.ChildAuthorization { return *a.DeepCopy() })
	out.Children = cloneEach(v.Children, func(c coordination.CoordinationChildView) coordination.CoordinationChildView {
		c.DependsOn = slices.Clone(c.DependsOn)
		c.Consumes = slices.Clone(c.Consumes)
		if c.IntegrationRelease != nil {
			c.IntegrationRelease = c.IntegrationRelease.DeepCopy()
		}
		return c
	})
	out.Artifacts = slices.Clone(v.Artifacts)
	out.IntegrationAttempts = cloneEach(v.IntegrationAttempts, func(a coordination.IntegrationAttemptView) coordination.IntegrationAttemptView {
		a.Subjects = slices.Clone(a.Subjects)
		return a
	})
	out.MergeOrder = slices.Clone(v.MergeOrder)
	if v.Recovery != nil {
		out.Recovery = v.Recovery.DeepCopy()
	}
	out.PostCompletionIncidents = cloneEach(v.PostCompletionIncidents, func(i coordination.CoordinationIncident) coordination.CoordinationIncident { return *i.DeepCopy() })
	return out
}

func cloneEach[T any](s []T, cp func(T) T) []T {
	if s == nil {
		return nil
	}
	out := make([]T, len(s))
	for i, v := range s {
		out[i] = cp(v)
	}
	return out
}
