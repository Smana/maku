// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/fence"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// The bounded reasons a repository check refuses (MR5, MR6, MR16). Each is checked fresh, at its
// own repository through its own App connection, at confirmation, filing, admission and resume: a
// cached success never admits anything.
var (
	// ErrRepositoryNotAllowed is a repository the factory config does not list.
	ErrRepositoryNotAllowed = errors.New("repository_not_allowed")
	// ErrPrivateRepository is a private repository: a family is public only in this release.
	ErrPrivateRepository = errors.New("private_repository_unsupported")
	// ErrRepositoryPermission is an actor who is not that repository's configured maintainer with
	// current GitHub write, or who has no linked GitHub login.
	ErrRepositoryPermission = errors.New("repository_permission")
	// ErrAccessUnverified is a permission or fence read that failed: no answer, never a guess.
	ErrAccessUnverified = errors.New("access_unverified")
	// ErrRepositoryNotOwned is a fence that names another factory, or none.
	ErrRepositoryNotOwned = errors.New("repository_not_owned")
	// ErrAppUnavailable is a repository the factory App has no connection or installation on.
	ErrAppUnavailable = errors.New("app_unavailable")
	// ErrClassNotAllowed is a class the repository does not allow under the central guardrails.
	ErrClassNotAllowed = errors.New("class_not_allowed")
	// ErrPolicyChanged is a fence that differs from the one the child's maintainer authorized at.
	ErrPolicyChanged = errors.New("policy_changed")
)

// checkReasons are the repository-check sentinels, in the order a reason is looked for.
var checkReasons = []error{ErrRepositoryNotAllowed, ErrPrivateRepository, ErrAppUnavailable, ErrRepositoryNotOwned,
	ErrAccessUnverified, ErrRepositoryPermission, ErrClassNotAllowed, ErrPolicyChanged}

// RepositoryChecks is one child repository's authority, read fresh: the repository is listed,
// public, installed and fenced to this factory, the child's class resolves there, and, for an actor
// with a linked login, that login is a configured maintainer holding GitHub write. auth, when
// given, binds the answer to the fence its maintainer authorized at (ErrPolicyChanged). An actor
// with no login asks for the repository's policy alone, as plan validation does.
type RepositoryChecks interface {
	Check(ctx context.Context, actor Actor, child v1alpha1.CoordinationChild, auth *v1alpha1.ChildAuthorization) (RepositoryPolicy, error)
}

// Actor is who confirms or drafts: the authenticated principal and the GitHub login its token's
// ZITADEL link resolves to. It is resolved by the factory API from the token, never read from a
// request body.
type Actor struct {
	Principal         string
	LinkedGitHubLogin string
}

// CheckForge is the reads a repository check makes through that repository's own factory App
// connection, never the primary's: forge.GitHub implements it.
type CheckForge interface {
	RepositoryInfo(ctx context.Context) (forge.RepositoryInfo, error)
	DefaultBranchFile(ctx context.Context, path string) (text, sha string, found bool, err error)
	Permission(ctx context.Context, login string) (string, error)
}

// GitHubChecks is RepositoryChecks against GitHub, every read live: the App's repository read
// (the installation check), the fence on the default branch, the class under the central
// guardrails, and the actor's collaborator permission. It caches nothing, so a lost App, fence,
// maintainer, permission or class refuses the very next check.
type GitHubChecks struct {
	Cfg *config.Config
	// Forges is repository's own App connection, nil when it has none.
	Forges func(repository string) CheckForge
}

// Check implements RepositoryChecks.
func (g GitHubChecks) Check(ctx context.Context, actor Actor, c v1alpha1.CoordinationChild, auth *v1alpha1.ChildAuthorization) (RepositoryPolicy, error) {
	repo := c.Repository
	if !slices.ContainsFunc(g.Cfg.Repos(), func(r config.Repository) bool { return strings.EqualFold(r.Name, repo) }) {
		return RepositoryPolicy{}, ErrRepositoryNotAllowed
	}
	var f CheckForge
	if g.Forges != nil {
		f = g.Forges(repo)
	}
	if f == nil {
		return RepositoryPolicy{}, fmt.Errorf("%w: no App connection", ErrAppUnavailable)
	}
	info, err := f.RepositoryInfo(ctx)
	switch {
	case err != nil:
		return RepositoryPolicy{}, fmt.Errorf("%w: %w", ErrAppUnavailable, err)
	case !strings.EqualFold(info.FullName, repo):
		return RepositoryPolicy{}, fmt.Errorf("%w: the connection answers for another repository", ErrAppUnavailable)
	case info.Private:
		return RepositoryPolicy{}, ErrPrivateRepository
	}
	text, sha, found, err := f.DefaultBranchFile(ctx, fence.Path)
	switch {
	case err != nil:
		return RepositoryPolicy{}, fmt.Errorf("%w: fence: %w", ErrAccessUnverified, err)
	case !found:
		return RepositoryPolicy{}, ErrRepositoryNotOwned
	}
	if name, err := fence.Parse([]byte(text)); err != nil || name != g.Cfg.Factory {
		return RepositoryPolicy{}, ErrRepositoryNotOwned
	}
	if auth != nil && (auth.FenceSHA != sha || !strings.EqualFold(auth.Repository, repo)) {
		return RepositoryPolicy{}, ErrPolicyChanged
	}
	pol, err := g.class(text, repo, c.Class, c.Tier)
	if err != nil {
		return RepositoryPolicy{}, err
	}
	pol.FenceSHA, pol.Public, pol.AppRepository = sha, true, info.FullName
	if actor.LinkedGitHubLogin == "" {
		return pol, nil
	}
	if !g.Cfg.RepositoryFor(repo).IsMaintainer(actor.LinkedGitHubLogin) {
		return RepositoryPolicy{}, fmt.Errorf("%w: not a configured maintainer", ErrRepositoryPermission)
	}
	perm, err := f.Permission(ctx, actor.LinkedGitHubLogin)
	switch {
	case err != nil:
		return RepositoryPolicy{}, fmt.Errorf("%w: permission: %w", ErrAccessUnverified, err)
	case !slices.Contains([]string{"write", "maintain", "admin"}, perm):
		return RepositoryPolicy{}, fmt.Errorf("%w: %s", ErrRepositoryPermission, perm)
	}
	pol.Permission = perm
	return pol, nil
}

// class resolves class at repo: the fence's own class when it declares classes, under the central
// guardrails (mergepolicy.Resolve), else the central config's; an adopted class authors nothing,
// so no child runs under one. Roles are the class harness's template, the tier's ceilings its run
// and task caps.
func (g GitHubChecks) class(fenceText, repo, class, tier string) (RepositoryPolicy, error) {
	template := g.Cfg.Defaults.Template
	if f, err := mergepolicy.Parse([]byte(fenceText)); err == nil && f.Classes != nil {
		cp, ok := f.Classes[class]
		if !ok {
			return RepositoryPolicy{}, fmt.Errorf("%w: %s is not declared", ErrClassNotAllowed, class)
		}
		r, err := mergepolicy.Resolve(class, cp, g.Cfg)
		if err != nil || r.Class.Author != nil {
			return RepositoryPolicy{}, fmt.Errorf("%w: %s", ErrClassNotAllowed, class)
		}
		if r.Profile.Template != "" {
			template = r.Profile.Template
		}
	} else if cl, ok := g.Cfg.RepositoryFor(repo).Classes[class]; !ok || cl.Author != nil {
		return RepositoryPolicy{}, fmt.Errorf("%w: %s", ErrClassNotAllowed, class)
	}
	t, ok := g.Cfg.Tiers[tier]
	roles := slices.DeleteFunc(slices.Clone(g.Cfg.Templates[template].Roles), func(r string) bool { return r == "scoper" })
	if !ok || len(roles) == 0 {
		return RepositoryPolicy{}, fmt.Errorf("%w: tier %s or template %s", ErrClassNotAllowed, tier, template)
	}
	run, task := config.MicroUSD(t.RunUSD), config.MicroUSD(t.TaskUSD)
	if run == 0 {
		run = config.RefMicroUSD(t.RunTokens)
	}
	if task == 0 {
		task = config.RefMicroUSD(t.TaskTokens)
	}
	return RepositoryPolicy{Repository: repo, Class: class, Tier: tier, Roles: roles, RunMicroUSD: run, TaskMicroUSD: task}, nil
}
