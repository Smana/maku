// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
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
