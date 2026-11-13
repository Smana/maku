// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

// HR41: class authority is the live config's: a repository cut over with repository authority on
// is authoritative; one that is not, or an unknown one, is not.
func TestConfigAuthority(t *testing.T) {
	cfg := &config.Config{Repositories: []config.Repository{
		{Name: "o/own", ClassAuthority: config.ClassAuthorityRepository}, {Name: "o/central"}}}
	a := ConfigAuthority{Cfg: cfg}
	for repo, want := range map[string]bool{"o/own": false, "o/central": false, "o/unknown": false} {
		if got, err := a.Authoritative(t.Context(), repo); err != nil || got != want {
			t.Errorf("%s with the switch off: %v %v", repo, got, err)
		}
	}
	cfg.PolicyManagement.RepositoryAuthority = true
	for repo, want := range map[string]bool{"o/own": true, "o/central": false, "o/unknown": false} {
		if got, err := a.Authoritative(t.Context(), repo); err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", repo, got, err, want)
		}
	}
}
