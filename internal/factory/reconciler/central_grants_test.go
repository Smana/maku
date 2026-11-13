// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/grant"
)

// centralApprovalRig is the merge rig with docs-links declared centrally, every file in its
// requiresApproval subset, a retargeted link in the diff and CI green.
func centralApprovalRig(t *testing.T, docsLinks config.Class) *rig {
	t.Helper()
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	docsLinks.RequiresApproval = []config.ApprovalLeaf{{ChangedFiles: []string{"."}}}
	g.r.Cfg.Current().Classes = map[string]config.Class{"docs-links": docsLinks, "revert": {Shadow: true}}
	files := changed("[x](https://example.com/old)", "[x](https://example.com/new)")
	g.f.SetFiles(files.Base, files.Head)
	g.f.SetChecks(12, green("SUCCESS"))
	return g
}

// centralHash is the resolved hash of the rig's docs-links (B13).
func centralHash(g *rig) string {
	cl, _ := g.r.Cfg.Current().RepositoryFor(awaiting().Spec.Repository).Class("docs-links")
	return config.ResolvedHash(cl)
}

func boundGrant(hash string) grant.Grant {
	gr := rigGrant()
	gr.PolicyHash = hash[:12]
	return gr
}

// B13, PR12: a central class's requiresApproval asks a human in a live class, and is recorded as
// would-require-approval in a shadow one.
func TestCentralClassAsksApproval(t *testing.T) {
	if tk := centralApprovalRig(t, config.Class{Live: true}).reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != reasonApprovalRequired {
		t.Fatalf("live: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
	g := centralApprovalRig(t, config.Class{Shadow: true})
	g.reconcile(t, "3buqdlot", 1)
	if c := strings.Join(g.f.Comments(12), "\n"); !strings.Contains(c, "decision=shadow:"+reasonShadowApproval) {
		t.Fatalf("shadow: %s", c)
	}
}

// B13, Q9: with approval memory on, a grant bound to the central class's resolved hash arms, and the
// record names it; a grant bound to another hash (the file's, before B13) does not.
func TestCentralGrantArms(t *testing.T) {
	for name, bound := range map[string]bool{"bound to the resolved class": true, "bound to the file's hash": false} {
		t.Run(name, func(t *testing.T) {
			g := centralApprovalRig(t, config.Class{Live: true})
			g.r.Cfg.Current().PolicyManagement.ApprovalMemory = true
			hash := centralHash(g)
			f := &fakeGrants{list: []grant.Grant{boundGrant(rigHash + "0000")}}
			if bound {
				f.list = []grant.Grant{boundGrant(hash)}
			}
			g.r.Grants = f
			tk := g.reconcile(t, "3buqdlot", 1)
			want := "lookup smana/cloud-native-ref docs-links " + hash + " " + config.DefaultGrantLifetime.String()
			if len(f.calls) == 0 || f.calls[0] != want {
				t.Fatalf("%q, want %q", f.calls, want)
			}
			c := strings.Join(g.f.Comments(12), "\n")
			switch {
			case bound && (tk.Status.Phase != v1alpha1.PhaseAutoMerging || !strings.Contains(c, " grant=aaaaaaaaaaaaaaaaaaaaaaaaaa:1 ")):
				t.Fatalf("%s %s: %s", tk.Status.Phase, tk.Status.Reason, c)
			case !bound && (tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != reasonApprovalRequired || strings.Contains(c, "grant=")):
				t.Fatalf("%s %s: %s", tk.Status.Phase, tk.Status.Reason, c)
			}
		})
	}
}

// B13: a grant binds to its own class's definition. Another class's edit leaves the hash, and the
// grant, intact; any edit to this class suspends it.
func TestUnrelatedClassEditKeepsGrants(t *testing.T) {
	g := centralApprovalRig(t, config.Class{Live: true})
	g.r.Cfg.Current().PolicyManagement.ApprovalMemory = true
	g.r.Grants = &fakeGrants{list: []grant.Grant{boundGrant(centralHash(g))}}
	classes := g.r.Cfg.Current().Classes
	classes["revert"] = config.Class{Shadow: true, HoldoutChecks: []string{"another check"}}
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging {
		t.Fatalf("another class's edit suspended the grant: %s %s", tk.Status.Phase, tk.Status.Reason)
	}

	g = centralApprovalRig(t, config.Class{Live: true})
	g.r.Cfg.Current().PolicyManagement.ApprovalMemory = true
	g.r.Grants = &fakeGrants{list: []grant.Grant{boundGrant(centralHash(g))}}
	dl := g.r.Cfg.Current().Classes["docs-links"]
	dl.ProtectedFiles = []string{"^nothing-here/"} // a tightening: still a new definition
	g.r.Cfg.Current().Classes["docs-links"] = dl
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != reasonApprovalRequired {
		t.Fatalf("this class's edit kept the grant: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
}
