// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"testing"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/runwatch"
)

// enforcing puts the rig's repository in audit with types enforced, and an allowlist.
func (g *rig) enforcing(types ...hardening.OutputType) {
	g.auditing(hardening.ModeAudit)
	h := &g.r.Cfg.Current().Hardening
	h.EnforceOutputs, h.LabelAllowlist = types, []string{"documentation", "needs-human"}
}

// HR14: an implementer claim on an enforced repository names its safe outputs and allowlist, so
// the broker serves the tools to it alone; a claim elsewhere carries neither.
func TestStartRunSafeOutputs(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "# Fix the link"))
	g.f.SetIssue(forge.Issue{Number: 7, Author: "smana", Association: "OWNER"})
	g.enforcing(hardening.PR, hardening.Comment)
	tk := g.reconcile(t, "3buqdlot", 3)
	if tk.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatal(tk.Status.Phase)
	}
	s := g.runs.specs[rid(0)]
	if !slices.Equal(s.SafeOutputs, []string{"comment", "pr"}) || !slices.Equal(s.LabelAllowlist, []string{"documentation", "needs-human"}) {
		t.Fatalf("safe outputs %v, allowlist %v", s.SafeOutputs, s.LabelAllowlist)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	u := runs.Build(runwatch.DefaultKind, s)
	if ann := u.GetAnnotations(); ann[runs.AnnSafeOutputs] != "comment,pr" || ann[runs.AnnLabelAllowlist] != `["documentation","needs-human"]` {
		t.Fatalf("annotations %v", ann)
	}

	for name, tc := range map[string]struct {
		role  string
		types []hardening.OutputType
	}{
		"a reviewer, enforced":     {"reviewer", []hardening.OutputType{hardening.PR}},
		"a triager, enforced":      {"triager", []hardening.OutputType{hardening.PR}},
		"an implementer, not":      {"implementer", nil},
		"a tester, enforced, kept": {"tester", []hardening.OutputType{hardening.Labels}},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t, issueTask("3buqdlot", 7, "# Fix the link"))
			g.enforcing(tc.types...)
			s := runs.Spec{Role: tc.role}
			g.r.markSafeOutputs(g.r.Cfg.Current(), issueTask("3buqdlot", 7, "# Fix the link"), &s)
			want := tc.role == "tester"
			if (len(s.SafeOutputs) > 0) != want || (s.LabelAllowlist != nil) != want {
				t.Fatalf("%s: safe outputs %v, allowlist %v", tc.role, s.SafeOutputs, s.LabelAllowlist)
			}
			if u := runs.Build(runwatch.DefaultKind, s); !want {
				if _, ok := u.GetAnnotations()[runs.AnnSafeOutputs]; ok {
					t.Fatal("annotated")
				}
			}
		})
	}
}
