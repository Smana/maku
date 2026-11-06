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
