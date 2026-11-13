// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/grant"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/rooms"
)

// fakeGrants is the broker's grant routes: what a lookup answers, and every call made.
type fakeGrants struct {
	list     []grant.Grant
	events   []string
	err      error
	disabled error
	calls    []string
}

func (f *fakeGrants) LookupGrants(_ context.Context, repository, class, hash string, maxLifetime time.Duration) ([]grant.Grant, []string, error) {
	f.calls = append(f.calls, strings.Join([]string{"lookup", repository, class, hash, maxLifetime.String()}, " "))
	return f.list, f.events, f.err
}

func (f *fakeGrants) UseGrant(_ context.Context, id string, version, pr int, head string) error {
	f.calls = append(f.calls, strings.Join([]string{"use", id, itoa(version), itoa(pr), head}, " "))
	return nil
}

func (f *fakeGrants) DisableGrant(_ context.Context, id string, pr int, reason string) error {
	f.calls = append(f.calls, strings.Join([]string{"disable", id, itoa(pr), reason}, " "))
	return f.disabled
}

func itoa(n int) string { return strconv.Itoa(n) }

func rigDecisionRows(ref string) decision.Rows { return decision.Rows{Grant: ref} }

// refusingGrants fails the test on any call: the source a switched-off gate must never touch.
type refusingGrants struct{ t *testing.T }

func (r refusingGrants) LookupGrants(context.Context, string, string, string, time.Duration) ([]grant.Grant, []string, error) {
	r.t.Fatal("a grant was looked up with approval memory off")
	return nil, nil, nil
}

func (r refusingGrants) UseGrant(context.Context, string, int, int, string) error {
	r.t.Fatal("a grant was used with approval memory off")
	return nil
}

func (r refusingGrants) DisableGrant(context.Context, string, int, string) error {
	r.t.Fatal("a grant was disabled with approval memory off")
	return nil
}

const rigHash = "0123456789abcdef"

// approvalSnapshot is the repository's policy for the merge rig: docs-links live, as the central
// class is, with every file in its requiresApproval subset.
func approvalSnapshot() (mergepolicy.Snapshot, bool) {
	live := withShape(config.Class{Live: true}, "docs-links")
	return mergepolicy.Snapshot{Hash: rigHash, HasClasses: true,
		Classes: map[string]mergepolicy.Resolved{"docs-links": {Class: live, Approval: []string{"."}}}}, true
}

// rigBound is the resolved hash of approvalSnapshot's docs-links: what its grants bind to (B13).
var rigBound = func() string { s, _ := approvalSnapshot(); return snapshotHash(s.Classes["docs-links"]) }()

// rigGrant covers every file of the rig's diff, in its repository and class, bound to rigBound.
func rigGrant() grant.Grant {
	return grant.Grant{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaa", Version: 1, Repository: "smana/cloud-native-ref", Class: "docs-links",
		Files: []string{".+"}, PolicyHash: rigBound[:12], Status: grant.StatusActive, ExpiresAt: now.Add(time.Hour)}
}

// Stage 5 off: the gate reads no grant, whatever the repository's policy asks, and its record is
// what dual evaluation alone writes.
func TestApprovalMemoryOffReadsNoGrant(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().PolicyManagement = config.PolicyManagement{Read: true, DualEvaluation: true}
	g.r.Policy = func(string) (mergepolicy.Snapshot, bool) { return approvalSnapshot() }
	g.r.Grants = refusingGrants{t}
	g.f.SetChecks(12, green("SUCCESS"))
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging {
		t.Fatalf("the central decision stands: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
	if c := strings.Join(g.f.Comments(12), "\n"); strings.Contains(c, "grant=") {
		t.Fatalf("%s", c)
	}
}

// Stage 5 on, beside dual evaluation: the class's grants are read once, immediately before the
// repository's decision, with the operator's lifetime cap; a covering grant makes the repository's
// decision the central one's (agree), and the lookup's count and events are recorded.
func TestDualEvaluationConsultsAGrantReadImmediatelyBefore(t *testing.T) {
	for name, tc := range map[string]struct {
		grants *fakeGrants
		want   string
	}{
		"a covering grant": {&fakeGrants{list: []grant.Grant{rigGrant()}, events: []string{"suspended"}}, "agree"},
		"no grant":         {&fakeGrants{}, "unexplained"},
		"a failed lookup":  {&fakeGrants{list: []grant.Grant{rigGrant()}, err: errors.New("broker down")}, "unexplained"},
	} {
		t.Run(name, func(t *testing.T) {
			g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
			g.r.Cfg.Current().PolicyManagement = config.PolicyManagement{Read: true, DualEvaluation: true, ApprovalMemory: true}
			g.r.Cfg.Current().MergeGuardrails.MaxGrantLifetime = config.Duration{Duration: 30 * 24 * time.Hour}
			g.r.Policy = func(string) (mergepolicy.Snapshot, bool) { return approvalSnapshot() }
			g.r.Grants = tc.grants
			files := changed("[x](https://example.com/old)", "[x](https://example.com/new)")
			g.f.SetFiles(files.Base, files.Head) // one retargeted link: in the subset, which reads every file
			g.f.SetChecks(12, green("SUCCESS"))
			if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging {
				t.Fatalf("the central decision stands: %s %s", tk.Status.Phase, tk.Status.Reason)
			}
			if want := "lookup smana/cloud-native-ref docs-links " + rigBound + " 720h0m0s"; !slices.Equal(tc.grants.calls, []string{want}) {
				t.Fatalf("%q", tc.grants.calls)
			}
			if !slices.Contains(g.metrics.recorded, "policy_dual docs-links "+tc.want) {
				t.Fatalf("%q", g.metrics.recorded)
			}
			if tc.grants.err == nil && !slices.Contains(g.metrics.recorded, "grants_active docs-links "+itoa(len(tc.grants.list))) {
				t.Fatalf("%q", g.metrics.recorded)
			}
			if c := strings.Join(g.f.Comments(12), "\n"); strings.Contains(c, "grant=") {
				t.Fatalf("the central decision used no grant: %s", c)
			}
		})
	}
}

// A grant is consulted only where it could matter: no lookup for a class without an approval
// subset.
func TestNoLookupWithoutAnApprovalSubset(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().PolicyManagement = config.PolicyManagement{Read: true, DualEvaluation: true, ApprovalMemory: true}
	g.r.Policy = func(string) (mergepolicy.Snapshot, bool) {
		s, ok := approvalSnapshot()
		r := s.Classes["docs-links"]
		r.Approval = nil
		s.Classes["docs-links"] = r
		return s, ok
	}
	g.r.Grants = refusingGrants{t}
	g.f.SetChecks(12, green("SUCCESS"))
	g.reconcile(t, "3buqdlot", 1)
}

// The grant a decision armed on is read again immediately before the merge call: the same id at
// the same version, active under the policy in force. Anything else hands the pull request to a
// human, as the approval subset would have (MP12, MP13).
func TestAGrantIsReadAgainBeforeTheMergeCall(t *testing.T) {
	tk := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Repository: "Smana/cloud-native-ref", PredictedClass: "docs-links"}}
	armed := ArmDecision{Verdict: "arm", Rows: rigDecisionRows("aaaaaaaaaaaaaaaaaaaaaaaaaa:1")}
	// Central authority (B13): the grant binds to the central class's resolved hash.
	central := config.Class{Live: true, RequiresApproval: []config.ApprovalLeaf{{ChangedFiles: []string{"."}}}}
	centralGrant := func() grant.Grant { g := rigGrant(); g.PolicyHash = config.ResolvedHash(central)[:12]; return g }
	moved := centralGrant()
	moved.Version = 2
	for name, tc := range map[string]struct {
		d      ArmDecision
		on     bool
		grants *fakeGrants
		want   bool
	}{
		"no grant used":        {ArmDecision{Verdict: "arm"}, false, &fakeGrants{}, true},
		"still active":         {armed, true, &fakeGrants{list: []grant.Grant{centralGrant()}}, true},
		"moved on a version":   {armed, true, &fakeGrants{list: []grant.Grant{moved}}, false},
		"gone since":           {armed, true, &fakeGrants{}, false},
		"the lookup failed":    {armed, true, &fakeGrants{list: []grant.Grant{centralGrant()}, err: errors.New("down")}, false},
		"approval memory off":  {armed, false, &fakeGrants{list: []grant.Grant{centralGrant()}}, false},
		"a malformed grant id": {ArmDecision{Verdict: "arm", Rows: rigDecisionRows("x:1")}, true, &fakeGrants{list: []grant.Grant{centralGrant()}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := &Reconciler{Cfg: &config.Config{PolicyManagement: config.PolicyManagement{Read: true, ApprovalMemory: tc.on},
				Repositories: []config.Repository{{Name: "Smana/cloud-native-ref", Classes: map[string]config.Class{"docs-links": central}}}},
				Grants: tc.grants, Metrics: &fakeMetrics{}, Now: func() time.Time { return now },
				Policy: func(string) (mergepolicy.Snapshot, bool) { return approvalSnapshot() }}
			if got := r.grantHolds(t.Context(), r.Cfg.Current(), tk, tc.d); got != tc.want {
				t.Fatalf("got %v, calls %q", got, tc.grants.calls)
			}
		})
	}
}

// A merge on a grant records its use, and the task keeps the grant for a revert to find.
func TestAMergeOnAGrantRecordsItsUse(t *testing.T) {
	f := &fakeGrants{}
	r := &Reconciler{Cfg: &config.Config{PolicyManagement: config.PolicyManagement{Read: true, ApprovalMemory: true}},
		Grants: f, Metrics: &fakeMetrics{}}
	tk := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Repository: "Smana/cloud-native-ref", PredictedClass: "docs-links"}}
	ref := &v1alpha1.PullRequestRef{Number: 12}
	r.grantUsed(t.Context(), r.Cfg.Current(), tk, ref, ArmDecision{Verdict: "arm", Rows: rigDecisionRows("aaaaaaaaaaaaaaaaaaaaaaaaaa:3")}, "abc")
	if ref.Grant != "aaaaaaaaaaaaaaaaaaaaaaaaaa:3" || !slices.Equal(f.calls, []string{"use aaaaaaaaaaaaaaaaaaaaaaaaaa 3 12 abc"}) {
		t.Fatalf("%q %q", ref.Grant, f.calls)
	}
	r.grantUsed(t.Context(), r.Cfg.Current(), tk, &v1alpha1.PullRequestRef{Number: 13}, ArmDecision{Verdict: "arm"}, "abc")
	if len(f.calls) != 1 {
		t.Fatalf("a merge without a grant used one: %q", f.calls)
	}
}

// Saved grants: "disabled immediately when an auto-merge they authorize is reverted". main_red
// reverts the merge and disables its grant; a grant already out of active is settled; a drill
// leaves it be.
func TestARevertDisablesTheGrantItsMergeUsed(t *testing.T) {
	for name, tc := range map[string]struct {
		drill    bool
		disabled error
		want     []string
	}{
		"main red":         {false, nil, []string{"disable aaaaaaaaaaaaaaaaaaaaaaaaaa 12 main_red"}},
		"already revoked":  {false, &rooms.APIError{Status: 409, Reason: "grant_state"}, []string{"disable aaaaaaaaaaaaaaaaaaaaaaaaaa 12 main_red"}},
		"a drill keeps it": {true, nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			g := mergeRig(t, v1alpha1.PhaseVerifying)
			f := &fakeGrants{disabled: tc.disabled}
			g.r.Grants = f
			g.r.Cfg.Current().PolicyManagement = config.PolicyManagement{Read: true, ApprovalMemory: true}
			var tk v1alpha1.Task
			_ = g.c.Get(t.Context(), client.ObjectKey{Namespace: "agent-system", Name: "3buqdlot"}, &tk)
			if tc.drill { // a maintainer's drill: factory/revert with the drill annotation (AM6)
				tk.Annotations = map[string]string{v1alpha1.AnnotationRevert: "Smana", v1alpha1.AnnotationDrill: "true"}
				_ = g.c.Update(t.Context(), &tk)
			}
			merged := metav1.NewTime(now)
			tk.Status.PullRequest.MergeCommitSHA, tk.Status.PullRequest.AutoMerged, tk.Status.PullRequest.MergedAt = "m1", true, &merged
			tk.Status.PullRequest.Grant = "aaaaaaaaaaaaaaaaaaaaaaaaaa:1"
			tk.Status.ConfigHash = g.r.Cfg.Current().Hash
			_ = g.c.Status().Update(t.Context(), &tk)
			red := green("SUCCESS")
			red.Runs[0].State = "FAILURE"
			if tc.drill {
				red = green("SUCCESS")
			}
			g.f.SetCommitChecks("m1", red.Runs...)
			if got := g.reconcile(t, "3buqdlot", 1); got.Status.Phase != v1alpha1.PhaseReverted {
				t.Fatalf("%s %s", got.Status.Phase, got.Status.Reason)
			}
			if !slices.Equal(f.calls, tc.want) {
				t.Fatalf("%q", f.calls)
			}
		})
	}
}
