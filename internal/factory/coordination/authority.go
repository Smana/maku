// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"

	"github.com/Smana/maku/internal/factory/config"
)

// ConfigAuthority is ClassAuthority over the live config (H5, HR41): a repository's classes are
// authoritative when it is cut over to its own policy file and repository authority is on
// (config.RepositoryAuthoritative). An unknown repository is not.
type ConfigAuthority struct{ Cfg config.Source }

// Authoritative reads the live config afresh.
func (a ConfigAuthority) Authoritative(_ context.Context, repository string) (bool, error) {
	cfg := a.Cfg.Current()
	return cfg.RepositoryAuthoritative(cfg.RepositoryFor(repository)), nil
}
