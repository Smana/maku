// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"errors"
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

func snapshotOf(classes map[string]mergepolicy.Resolved, divergences ...string) mergepolicy.Snapshot {
	return mergepolicy.Snapshot{Hash: "0123456789abcdef", HasClasses: true, Classes: classes, Divergences: divergences}
}

// Stage 3: the decision is made again under the repository's policy and the two compared. The
// agreement is closed (fmetrics.PolicyAgreements) and explains each difference it can.
func TestDualAgreement(t *testing.T) {
	same := map[string]mergepolicy.Resolved{"dead-links": {Class: declaredClass()}}
	loose := declaredClass()
	loose.MaxModifiedLines = 0
	strict := declaredClass()
	strict.MaxModifiedLines = 1
	holdout := declaredClass()
	holdout.HoldoutChecks = nil
	for name, tc := range map[string]struct {
		snap   mergepolicy.Snapshot
		ok     bool
		mutate func(*ArmInputs)
		want   string
	}{
		"no snapshot":       {mergepolicy.Snapshot{}, false, func(*ArmInputs) {}, "no_policy"},
		"an invalid file":   {mergepolicy.Snapshot{Hash: "ab", Err: errors.New("x")}, true, func(*ArmInputs) {}, "no_policy"},
		"a fence-only file": {mergepolicy.Snapshot{Hash: "ab"}, true, func(*ArmInputs) {}, "no_policy"},
		"the same class":    {snapshotOf(same), true, func(*ArmInputs) {}, "agree"},
		"the same refusal":  {snapshotOf(same), true, func(in *ArmInputs) { in.Stats[0].Additions = 20 }, "agree"},
		"a known difference": {snapshotOf(map[string]mergepolicy.Resolved{"dead-links": {Class: strict}}, "dead-links: central {...}, repository {...}"),
			true, func(*ArmInputs) {}, "explained"},
		"an unknown difference": {snapshotOf(map[string]mergepolicy.Resolved{"dead-links": {Class: strict}}, "revert: only in the repository"),
			true, func(*ArmInputs) {}, "unexplained"},
		"the class absent": {snapshotOf(map[string]mergepolicy.Resolved{}, "dead-links: only in the central config"), true, func(*ArmInputs) {}, "explained"},
		"the diff it needs, unread": {snapshotOf(map[string]mergepolicy.Resolved{"dead-links": {Class: holdout}}), true,
			func(in *ArmInputs) { in.DiffRead, in.Stats = false, nil; in.Checks.Runs[2].State = "FAILURE" }, "unread"},
		"a looser class, the diff read": {snapshotOf(map[string]mergepolicy.Resolved{"dead-links": {Class: loose}}), true,
			func(in *ArmInputs) { in.Stats[0].Additions = 20 }, "unexplained"},
	} {
		t.Run(name, func(t *testing.T) {
			in := gateInputs()
			tc.mutate(&in)
			central := DecideArm(in)
			_, got := dualOf(central, in, tc.snap, tc.ok)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The repository's decision is the evaluator's, under the snapshot's class.
func TestDualDecidesUnderTheSnapshot(t *testing.T) {
	strict := declaredClass()
	strict.MaxModifiedLines = 1
	in := gateInputs()
	repo, _ := dualOf(DecideArm(in), in, snapshotOf(map[string]mergepolicy.Resolved{"dead-links": {Class: strict}}), true)
	if repo.Verdict != "human" || repo.Reason != "patch_too_large" {
		t.Fatalf("%+v", repo)
	}
}

// Stage 3 wired: with dualEvaluation on, a decision the gate acts on is decided again, counted,
// and its record names the policy and the agreement. The central decision is the one acted on.
func TestTheGateEvaluatesTheRepositoryPolicyBesideTheCentral(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().Classes = map[string]config.Class{"docs-links": {Shadow: true}, "revert": {Shadow: true}}
	g.r.Cfg.Current().PolicyManagement = config.PolicyManagement{Read: true, DualEvaluation: true}
	g.r.Policy = func(string) (mergepolicy.Snapshot, bool) {
		live := withShape(config.Class{Live: true}, "docs-links") // the repository would merge it
		return snapshotOf(map[string]mergepolicy.Resolved{"docs-links": {Class: live}}, "docs-links: central {...}, repository {...}"), true
	}
	g.f.SetChecks(12, green("SUCCESS"))
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || len(g.f.Armed()) != 0 {
		t.Fatalf("the central shadow decision stands: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
	if !slices.Contains(g.metrics.recorded, "policy_dual docs-links explained") {
		t.Fatalf("%q", g.metrics.recorded)
	}
	if c := strings.Join(g.f.Comments(12), "\n"); !strings.Contains(c, " policy=0123456789ab dual=explained reviewer=") {
		t.Fatalf("%s", c)
	}
}

// Off, nothing is read, counted or written beyond what the gate did before.
func TestDualEvaluationOffReadsNothing(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().Classes = map[string]config.Class{"docs-links": {Shadow: true}, "revert": {Shadow: true}}
	g.r.Cfg.Current().PolicyManagement = config.PolicyManagement{Read: true}
	g.r.Policy = func(string) (mergepolicy.Snapshot, bool) {
		t.Fatal("read with the switch off")
		return mergepolicy.Snapshot{}, false
	}
	g.f.SetChecks(12, green("SUCCESS"))
	g.reconcile(t, "3buqdlot", 1)
	if c := strings.Join(g.f.Comments(12), "\n"); strings.Contains(c, "dual=") {
		t.Fatalf("%s", c)
	}
}
