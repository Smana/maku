// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"context"
	"errors"
)

// Public reports whether the repository is public, read live from GET repos/{owner}/{repo}. A
// failed read, or a reply without the private flag, is an error and never "public": demo mode
// serves public repositories only and fails closed.
func (g *GitHub) Public(ctx context.Context) (bool, error) {
	r, _, err := g.rest.Repositories.Get(ctx, g.owner, g.name)
	g.mark(err)
	if err != nil {
		return false, wrap("repository", err)
	}
	if r.Private == nil {
		return false, errors.New("forge: repository: the reply has no private flag")
	}
	return !*r.Private, nil
}

// RepositoryInfo is the repository as its App connection reads it, live: a successful read is
// the installation check (v4 MR6), and its full name and private flag are GitHub's, not config's.
type RepositoryInfo struct {
	FullName      string
	DefaultBranch string
	Private       bool
}

// RepositoryInfo reads GET repos/{owner}/{repo} through this connection. A failed read, or a reply
// without its full name or private flag, is an error and never an answer.
func (g *GitHub) RepositoryInfo(ctx context.Context) (RepositoryInfo, error) {
	r, _, err := g.rest.Repositories.Get(ctx, g.owner, g.name)
	g.mark(err)
	if err != nil {
		return RepositoryInfo{}, wrap("repository", err)
	}
	if r.Private == nil || r.GetFullName() == "" {
		return RepositoryInfo{}, errors.New("forge: repository: the reply has no full name or private flag")
	}
	return RepositoryInfo{FullName: r.GetFullName(), DefaultBranch: r.GetDefaultBranch(), Private: *r.Private}, nil
}
