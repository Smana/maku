// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

const fenceOurs = "factory: ogenki\n"

// checkRig is two public repositories fenced to this factory, each with its own App connection,
// and the central config's scripts class and standard tier.
// checkCfg is the config checkRig built: the checks take it per call.
var checkCfg *config.Config

func checkRig() (GitHubChecks, map[string]*forgetest.Fake) {
	cfg := testConfig(config.CoordinationExecute)
	checkCfg = cfg
	cfg.Factory = "ogenki"
	cfg.FactoryLogin = "ogenki-agent-factory[bot]"
	cfg.Defaults.Template = "pair"
	cfg.Templates = map[string]config.Template{"pair": {Roles: []string{"implementer", "reviewer"}}}
	cfg.Tiers = map[string]config.Tier{"standard": {Model: "agent-default", RunUSD: 0.51, TaskUSD: 5.10}}
	for i := range cfg.Repositories {
		cfg.Repositories[i].Classes = map[string]config.Class{"scripts": {}}
	}
	fakes := map[string]*forgetest.Fake{}
	for _, r := range []string{repoA, repoB} {
		f := forgetest.NewFake()
		f.SetRepositoryInfo(forge.RepositoryInfo{FullName: r, DefaultBranch: "main"})
		f.SetFile(".github/agent-factory.yaml", fenceOurs)
		fakes[r] = f
	}
	fakes[repoA].SetPermission("Smana", "admin")
	fakes[repoB].SetPermission("other-maintainer", "write")
	fakes[repoB].SetPermission("readonly", "read")
	fakes[repoB].SetPermission("Smana", "write")
	return GitHubChecks{Forges: func(r string) CheckForge {
		if f, ok := fakes[r]; ok {
			return f
		}
		return nil
	}}, fakes
}

func consumerChild() v1alpha1.CoordinationChild {
	return v1alpha1.CoordinationChild{ID: "consumer", Repository: repoB, Maintainer: "github:other-maintainer", Class: "scripts", Tier: "standard"}
}

// Every repository check is read fresh through that repository's own connection and refuses with
// its bounded reason.
func TestRepositoryChecks(t *testing.T) {
	maintainer := Actor{Principal: "zitadel:o", LinkedGitHubLogin: "other-maintainer"}
	for _, tc := range []struct {
		name  string
		actor Actor
		child func() v1alpha1.CoordinationChild
		setup func(*GitHubChecks, map[string]*forgetest.Fake)
		auth  func(GitHubChecks) *v1alpha1.ChildAuthorization
		want  error
	}{
		{name: "the child's maintainer with write", actor: maintainer},
		{name: "policy only, no actor"},
		{name: "an unlisted repository", child: func() v1alpha1.CoordinationChild { c := consumerChild(); c.Repository = "Smana/elsewhere"; return c }, want: ErrRepositoryNotAllowed},
		{name: "no App connection", setup: func(g *GitHubChecks, _ map[string]*forgetest.Fake) { g.Forges = func(string) CheckForge { return nil } }, want: ErrAppUnavailable},
		{name: "an App read that fails", setup: func(_ *GitHubChecks, f map[string]*forgetest.Fake) { f[repoB].FailRepositoryInfo(errors.New("404")) }, want: ErrAppUnavailable},
		{name: "a connection for another repository", setup: func(_ *GitHubChecks, f map[string]*forgetest.Fake) {
			f[repoB].SetRepositoryInfo(forge.RepositoryInfo{FullName: repoA})
		}, want: ErrAppUnavailable},
		{name: "a private repository", setup: func(_ *GitHubChecks, f map[string]*forgetest.Fake) {
			f[repoB].SetRepositoryInfo(forge.RepositoryInfo{FullName: repoB, Private: true})
		}, want: ErrPrivateRepository},
		{name: "no fence", setup: func(_ *GitHubChecks, f map[string]*forgetest.Fake) {
			f[repoB].SetFile(".github/agent-factory.yaml", "")
		}, want: ErrRepositoryNotOwned},
		{name: "a foreign fence", setup: func(_ *GitHubChecks, f map[string]*forgetest.Fake) {
			f[repoB].SetFile(".github/agent-factory.yaml", "factory: other\n")
		}, want: ErrRepositoryNotOwned},
		{name: "a fence read that fails", setup: func(_ *GitHubChecks, f map[string]*forgetest.Fake) {
			f[repoB].SetFile(".github/agent-factory.yaml", "!")
		}, want: ErrAccessUnverified},
		{name: "a class the repository does not allow", child: func() v1alpha1.CoordinationChild { c := consumerChild(); c.Class = "docs-links"; return c }, want: ErrClassNotAllowed},
		{name: "a class the fence no longer declares", setup: func(_ *GitHubChecks, f map[string]*forgetest.Fake) {
			f[repoB].SetFile(".github/agent-factory.yaml", fenceOurs+"classes:\n  revert:\n    mode: off\n    author: agents\n    harness: x\n")
		}, want: ErrClassNotAllowed},
		// Follow-up of #538: a fence in the binding shape is read too (B15).
		{name: "a fence that binds the class", setup: func(_ *GitHubChecks, f map[string]*forgetest.Fake) {
			checkCfg.Policies = map[string]config.ServedPolicy{"scripts": {Generation: 2}}
			f[repoB].SetFile(".github/agent-factory.yaml", fenceOurs+"mergePolicies:\n  - {name: scripts, mode: \"off\"}\n")
		}},
		{name: "a fence that binds other classes only", setup: func(_ *GitHubChecks, f map[string]*forgetest.Fake) {
			checkCfg.Policies = map[string]config.ServedPolicy{"scripts": {Generation: 2}, "revert": {}}
			f[repoB].SetFile(".github/agent-factory.yaml", fenceOurs+"mergePolicies:\n  - {name: revert, mode: \"off\"}\n")
		}, want: ErrClassNotAllowed},
		{name: "a fence binding a policy the factory does not serve", setup: func(_ *GitHubChecks, f map[string]*forgetest.Fake) {
			f[repoB].SetFile(".github/agent-factory.yaml", fenceOurs+"mergePolicies:\n  - {name: scripts, mode: \"off\"}\n")
		}, want: ErrClassNotAllowed},
		{name: "a fence changed since authorization", actor: maintainer, auth: func(GitHubChecks) *v1alpha1.ChildAuthorization {
			return &v1alpha1.ChildAuthorization{Repository: repoB, FenceSHA: "0000000000000000000000000000000000000000"}
		}, want: ErrPolicyChanged},
		{name: "the primary maintainer for B", actor: smana, setup: func(_ *GitHubChecks, _ map[string]*forgetest.Fake) {
			checkCfg.Repositories[1].Maintainers = []string{"other-maintainer"}
		}, want: ErrRepositoryPermission},
		{name: "a read-only maintainer", actor: Actor{Principal: "zitadel:r", LinkedGitHubLogin: "readonly"}, want: ErrRepositoryPermission},
		{name: "a permission read that fails", actor: maintainer, setup: func(_ *GitHubChecks, f map[string]*forgetest.Fake) { f[repoB].FailPermission(errors.New("502")) }, want: ErrAccessUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, fakes := checkRig()
			if tc.setup != nil {
				tc.setup(&g, fakes)
			}
			c := consumerChild()
			if tc.child != nil {
				c = tc.child()
			}
			var auth *v1alpha1.ChildAuthorization
			if tc.auth != nil {
				auth = tc.auth(g)
			}
			pol, err := g.Check(context.Background(), checkCfg, tc.actor, c, auth)
			if !errors.Is(err, tc.want) && (tc.want != nil || err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.want != nil {
				return
			}
			if pol.FenceSHA == "" || !pol.Public || pol.AppRepository != repoB || pol.RunMicroUSD != 510000 || pol.TaskMicroUSD != 5100000 || len(pol.Roles) != 2 {
				t.Fatalf("policy %+v", pol)
			}
			if (tc.actor.LinkedGitHubLogin != "") != (pol.Permission == "write") {
				t.Fatalf("permission %q", pol.Permission)
			}
		})
	}
}

// A success is never cached: losing write after a passing check refuses the next one.
func TestRepositoryChecksCacheNothing(t *testing.T) {
	g, fakes := checkRig()
	a := Actor{LinkedGitHubLogin: "other-maintainer"}
	if _, err := g.Check(context.Background(), checkCfg, a, consumerChild(), nil); err != nil {
		t.Fatal(err)
	}
	fakes[repoB].SetPermission("other-maintainer", "read")
	if _, err := g.Check(context.Background(), checkCfg, a, consumerChild(), nil); !errors.Is(err, ErrRepositoryPermission) {
		t.Fatalf("err = %v after write was lost", err)
	}
}
