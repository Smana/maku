// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// cutOverRig is mergeRig with its repository cut over to its own policy file (stage 4): no
// central class, the switch on, and the gate's policy and every reader's classes from the
// returned snapshot and error, as mergepolicy.Watch would serve them.
func cutOverRig(t *testing.T, snap mergepolicy.Snapshot, err error) *rig {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	rc := g.r.Cfg.Current().Repos()[0]
	rc.Classes, rc.ClassAuthority = nil, config.ClassAuthorityRepository
	g.r.Cfg.Current().Repositories, g.r.Cfg.Current().Repositories[0].Classes = []config.Repository{rc}, nil
	g.r.Cfg.Current().PolicyManagement = config.PolicyManagement{Read: true, RepositoryAuthority: true}
	g.r.Cfg.Current().SetClassSource(func(string) (map[string]config.Class, bool) {
		out := map[string]config.Class{}
		for n, r := range snap.Classes {
			out[n] = r.Class
		}
		return out, snap.Classes != nil
	})
	g.r.Authority = func(string) (mergepolicy.Snapshot, error) { return snap, err }
	g.f.SetChecks(12, green("SUCCESS"))
	return g
}

func resolvedLive(class string) mergepolicy.Resolved {
	return mergepolicy.Resolved{Class: withShape(config.Class{Live: true}, class)}
}

// Stage 4: a cut-over repository's live class merges on its own policy, and the record binds the
// decision to the policy's hash (MP12).
func TestACutOverRepositoryMergesOnItsOwnPolicy(t *testing.T) {
	g := cutOverRig(t, snapshotOf(map[string]mergepolicy.Resolved{"docs-links": resolvedLive("docs-links")}), nil)
	g.reconcile(t, "3buqdlot", 1)
	if len(g.f.Armed()) != 1 {
		t.Fatalf("armed %v", g.f.Armed())
	}
	if c := strings.Join(g.f.Comments(12), "\n"); c != "" && !recordHas(c, " policy=0123456789ab") {
		t.Fatalf("%s", c)
	}
}

// A shadow class records would-arm and merges nothing; a class the file does not declare is a
// maintainer's.
func TestACutOverRepositoryShadowsOrHandsOff(t *testing.T) {
	for name, tc := range map[string]struct {
		classes map[string]mergepolicy.Resolved
		reason  string
	}{
		"shadow":     {map[string]mergepolicy.Resolved{"docs-links": {Class: withShape(config.Class{Shadow: true}, "docs-links")}}, "shadow_would_arm"},
		"undeclared": {map[string]mergepolicy.Resolved{"revert": resolvedLive("revert")}, "class_unarmed"},
	} {
		t.Run(name, func(t *testing.T) {
			g := cutOverRig(t, snapshotOf(tc.classes), nil)
			tk := g.reconcile(t, "3buqdlot", 1)
			if len(g.f.Armed()) != 0 || tk.Status.Reason != tc.reason {
				t.Fatalf("%s %s %v", tk.Status.Phase, tk.Status.Reason, g.f.Armed())
			}
			if c := strings.Join(g.f.Comments(12), "\n"); tc.reason == "shadow_would_arm" && !strings.Contains(c, " policy=0123456789ab ") {
				t.Fatalf("%s", c)
			}
		})
	}
}

// MP13, the stage 4 exit: a policy that gives no authority pauses auto-merge. Unreadable or stale
// waits for the next read, counted; invalid is a maintainer's to fix; nothing arms either way.
func TestNoAuthorityPausesAutoMerge(t *testing.T) {
	for name, tc := range map[string]struct {
		err           error
		phase, reason string
		counted       string
	}{
		"unreadable": {mergepolicy.ErrUnreadable, v1alpha1.PhaseAwaitingCI, reasonPolicyUnavailable, "policy_load unreadable"},
		"stale":      {fmt.Errorf("x: %w", mergepolicy.ErrStale), v1alpha1.PhaseAwaitingCI, reasonPolicyUnavailable, "policy_load stale"},
		"invalid":    {mergepolicy.ErrInvalid, v1alpha1.PhaseAwaitingHuman, reasonPolicyInvalid, ""},
	} {
		t.Run(name, func(t *testing.T) {
			// The last valid file armed the class: an unusable read must not keep it armed.
			g := cutOverRig(t, snapshotOf(map[string]mergepolicy.Resolved{"docs-links": resolvedLive("docs-links")}), tc.err)
			tk := g.reconcile(t, "3buqdlot", 1)
			if len(g.f.Armed()) != 0 || tk.Status.Phase != tc.phase || tk.Status.Reason != tc.reason {
				t.Fatalf("%s %s %v", tk.Status.Phase, tk.Status.Reason, g.f.Armed())
			}
			if tc.counted != "" && !slices.Contains(g.metrics.recorded, tc.counted) {
				t.Fatalf("%q", g.metrics.recorded)
			}
		})
	}
}

// The fixed base is decided first, whatever the policy: a red CI is still the fix run's, never a
// policy pause (MP3).
func TestNoAuthorityKeepsTheFixedBase(t *testing.T) {
	in := gateInputs()
	in.Checks.Runs[0].State = "FAILURE"
	if d := decideUnder(in, gatePolicy{on: true, err: mergepolicy.ErrInvalid}); d.Verdict != "ci_red" {
		t.Fatalf("%+v", d)
	}
	in = gateInputs()
	if d := decideUnder(in, gatePolicy{on: true, err: mergepolicy.ErrUnreadable}); d.Verdict != "wait" || d.Reason != reasonPolicyUnavailable {
		t.Fatalf("%+v", d)
	}
}

// A live class's requiresApproval subset asks a human under repository authority (MP9): stage 5's
// grants are the only way it arms.
func TestTheApprovalSubsetAsksAHuman(t *testing.T) {
	in := gateInputs()
	r := mergepolicy.Resolved{Class: declaredClass(), Approval: []string{`^docs/`}}
	in.Stats[0].Path = "docs/a.md"
	if d := decideUnder(in, gatePolicy{on: true, snap: snapshotOf(map[string]mergepolicy.Resolved{"dead-links": r})}); d.Reason != reasonApprovalRequired {
		t.Fatalf("%+v", d)
	}
}

// Off, the gate is today's: nothing is read from a policy source, and the decision and its record
// are byte for byte the central ones.
func TestRepositoryAuthorityOffIsToday(t *testing.T) {
	run := func(wire bool) (string, []string) {
		g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
		g.f.SetChecks(12, green("SUCCESS"))
		if wire {
			g.r.Cfg.Current().PolicyManagement = config.PolicyManagement{Read: true}
			g.r.Authority = func(string) (mergepolicy.Snapshot, error) {
				t.Fatal("read with the switch off")
				return mergepolicy.Snapshot{}, nil
			}
		}
		tk := g.reconcile(t, "3buqdlot", 1)
		return tk.Status.Phase + " " + tk.Status.Reason + " " + strings.Join(g.f.Comments(12), "\n"), g.f.Armed()
	}
	before, armedBefore := run(false)
	after, armedAfter := run(true)
	if before != after || !slices.Equal(armedBefore, armedAfter) {
		t.Fatalf("%q\n%q", before, after)
	}
}

// Under repository authority the class is the repository's: dual evaluation has no central
// decision to compare it with, so it is not run.
func TestDualEvaluationSkipsACutOverRepository(t *testing.T) {
	g := cutOverRig(t, snapshotOf(map[string]mergepolicy.Resolved{"docs-links": resolvedLive("docs-links")}), nil)
	g.r.Cfg.Current().PolicyManagement.DualEvaluation = true
	g.r.Policy = func(string) (mergepolicy.Snapshot, bool) {
		t.Fatal("dual evaluation of a cut-over repository")
		return mergepolicy.Snapshot{}, false
	}
	g.reconcile(t, "3buqdlot", 1)
}

// B12 (review of #533): under repository authority the record's generations are the deciding
// class's, as the file resolved it (its policy's generation, a last resolved one's included), never
// the central config's for the class.
func TestACutOverRecordNamesTheDecidingClassGenerations(t *testing.T) {
	for name, tc := range map[string]struct {
		policyGen int64
		want      string
		absent    []string
	}{
		"a binding served from its last resolved class": {3, " policygen=3 repogen=9 ", nil},
		"a class-shape file":                            {0, " policy=0123456789ab ", []string{"policygen=", "repogen="}},
	} {
		t.Run(name, func(t *testing.T) {
			res := resolvedLive("docs-links")
			res.PolicyGeneration = tc.policyGen
			g := cutOverRig(t, snapshotOf(map[string]mergepolicy.Resolved{"docs-links": res}), nil)
			g.r.Cfg.Current().Generations = config.Generations{Repos: map[string]config.RepoGenerations{"smana/cloud-native-ref": {
				Repository: 9, Classes: map[string]config.ClassGeneration{"docs-links": {Policy: 5, PolicyName: "docs-links"}}}}}
			g.reconcile(t, "3buqdlot", 1)
			c := strings.Join(append(g.f.Comments(12), g.f.Comments(7)...), "\n")
			if !strings.Contains(c, tc.want) || strings.Contains(c, "policygen=5") {
				t.Fatalf("want %q, never the central 5: %s", tc.want, c)
			}
			for _, a := range tc.absent {
				if strings.Contains(c, a) {
					t.Fatalf("%s printed: %s", a, c)
				}
			}
		})
	}
}
