// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/forge"
)

// put creates o and writes its status.
func (g *rig) put(t *testing.T, o *v1alpha1.Task) {
	t.Helper()
	st := o.Status
	if err := g.c.Create(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	o.Status = st
	if err := g.c.Status().Update(t.Context(), o); err != nil {
		t.Fatal(err)
	}
}

// holdRig is mergeRig's task as the consumer child of a filed parent whose contract child landed
// at producerMerg: a dependent at its gate on head abc (independent: no dependsOn).
func holdRig(t *testing.T, independent bool) (*rig, *v1alpha1.Task) {
	t.Helper()
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	p := coordinationPlan(t, independent)
	parent := filedParent(p, coordination.ReasonFiled)
	parent.Status.Coordination.Artifacts = []v1alpha1.LandedArtifact{schemaRef()}
	contract := coordination.ChildTask("agent-system", cfg(), parent, p.Children[0], 21, 1)
	at := metav1.NewTime(now.Add(-time.Hour))
	contract.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, Reason: "merged",
		PullRequest: &v1alpha1.PullRequestRef{Number: 5, MergeCommitSHA: producerMerg, MergedAt: &at}}
	parent.Status.Coordination.Children[0].Task, parent.Status.Coordination.Children[1].Task = contract.Name, "3buqdlot"
	g.put(t, parent)
	g.put(t, contract)
	g.patch(t, func(tk *v1alpha1.Task) {
		tk.Labels = map[string]string{v1alpha1.LabelCoordinationParent: p.ID}
		tk.Spec.CoordinationParent = &v1alpha1.CoordinationParent{Task: p.ID, PlanVersion: p.Version, ChildID: "consumer"}
		tk.Status.Coordination = &v1alpha1.CoordinationStatus{PlanVersion: p.Version, ChildID: "consumer"}
	})
	g.f.SetPR(gatePR(headH1, rid(0))) // an integration binds 40-hex heads only
	g.log.handoff(rid(0), headH1)
	g.patch(t, func(tk *v1alpha1.Task) { tk.Status.PullRequest.HeadSHA = headH1 })
	g.f.SetChecks(12, green("SUCCESS"))
	listRepos(g.r.Cfg.Current(), coordA, coordB) // the parent's and the children's repositories are served
	return g, parent
}

// The consumer's two heads: H1, then the push that moves it to H2.
const (
	headH1 = "1111111111111111111111111111111111111111"
	headH2 = "2222222222222222222222222222222222222222"
)

// pass records a passing pre-merge integration of head as its run would: the integration Task
// with its typed result, which the parent's next pass absorbs into the release of that exact head
// and the child's accepted head.
func (g *rig) pass(t *testing.T, parentName, head string) {
	t.Helper()
	parent, _ := g.coordTask(t, parentName)
	cs := parent.Status.Coordination
	m, err := coordination.BeforeDependentManifest(parentName, cs, "consumer", head, map[string]string{"contract": producerMerg})
	if err != nil {
		t.Fatal(err)
	}
	it := coordination.IntegrationTask("agent-system", cfg(), parent, m, len(cs.IntegrationAttempts)+1)
	hash := coordination.ManifestHash(m)
	it.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, Reason: reasonIntegrationPassed,
		Result: &v1alpha1.IntegrationResult{State: coordination.ResultPassed, ManifestHash: hash}}
	g.put(t, it)
	cs.IntegrationAttempts = append(cs.IntegrationAttempts, v1alpha1.IntegrationAttempt{Task: it.Name, Purpose: m.Purpose,
		PlanHash: m.PlanHash, ManifestHash: hash, State: coordination.ResultPending, Subjects: m.Subjects})
	if err := g.c.Status().Update(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	mode, port := g.r.Cfg.Current().Coordination.Mode, g.r.Coordination
	g.r.Cfg.Current().Coordination.Mode, g.r.Coordination = config.CoordinationExecute, &fakeCoordination{ready: map[string]error{}}
	defer func() { g.r.Cfg.Current().Coordination.Mode, g.r.Coordination = mode, port }()
	p := g.reconcile(t, parentName, 2)
	want := v1alpha1.IntegrationRelease{ChildID: "consumer", HeadSHA: head, IntegrationTask: it.Name, ManifestHash: hash}
	if rel := p.Status.Coordination.Children[1].IntegrationRelease; rel == nil || *rel != want {
		t.Fatalf("release %+v", rel)
	}
	if c, _ := g.coordTask(t, "3buqdlot"); c.Status.Coordination.IntegrationAcceptedHead != head {
		t.Fatalf("the child's accepted head %q", c.Status.Coordination.IntegrationAcceptedHead)
	}
}

// countingMerger counts merge calls.
type countingMerger struct {
	forge.Merger
	merges *int
}

func (m countingMerger) Merge(ctx context.Context, nodeID, head string) error {
	*m.merges++
	return m.Merger.Merge(ctx, nodeID, head)
}

// racingClient drops the parent's release right after the pass's first read of the parent: the
// release goes stale between the arm decision and the pre-merge re-read.
type racingClient struct {
	client.Client
	parent string
	reads  int
	race   func()
}

func (c *racingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := c.Client.Get(ctx, key, obj, opts...)
	if key.Name == c.parent {
		if c.reads++; c.reads == 1 {
			c.race()
		}
	}
	return err
}

// held asserts the task waits at its gate for integration with no merger call.
func held(t *testing.T, g *rig, merges int, tk *v1alpha1.Task) {
	t.Helper()
	if tk.Status.Phase != v1alpha1.PhaseAwaitingCI || tk.Status.Reason != reasonIntegrationPending || merges != 0 || len(g.f.Armed()) != 0 {
		t.Fatalf("%s/%s, %d merge calls %v", tk.Status.Phase, tk.Status.Reason, merges, g.f.Armed())
	}
	if tk.Status.Coordination.IntegrationAcceptedHead != "" {
		t.Fatalf("accepted head %q while held", tk.Status.Coordination.IntegrationAcceptedHead)
	}
}

// Review Focus 4, MR9: the integration hold is the final word on every arm DecideArm returns, the
// live class's automatic arm and the maintainer-approved arm alike, through both decisions and the
// approval's return. Only the exact accepted head arms; a moved head closes it again; a release
// gone stale between the decision and the pre-merge re-read merges nothing. Shadow records
// would-arm and never releases. An independent child has no hold.
func TestIntegrationHoldCoversEveryArmAndStaleHead(t *testing.T) {
	t.Run("live class automatic arm", func(t *testing.T) {
		g, parent := holdRig(t, false)
		merges := 0
		g.r.Merger = countingMerger{Merger: g.f, merges: &merges}
		tk := g.reconcile(t, "3buqdlot", 1)
		held(t, g, merges, tk)
		if tk.Status.PullRequest.HeadSHA != headH1 {
			t.Fatalf("the held head is not recorded for the parent: %q", tk.Status.PullRequest.HeadSHA)
		}
		g.pass(t, parent.Name, headH1)
		tk = g.reconcile(t, "3buqdlot", 1)
		if tk.Status.Phase != v1alpha1.PhaseAutoMerging || merges != 1 || g.f.Armed()[0] != "PR_12 "+headH1 ||
			tk.Status.Coordination.IntegrationAcceptedHead != headH1 {
			t.Fatalf("the accepted head: %s/%s, %d merges, accepted %q", tk.Status.Phase, tk.Status.Reason, merges, tk.Status.Coordination.IntegrationAcceptedHead)
		}
	})

	t.Run("a second decision on the read diff", func(t *testing.T) {
		g, parent := holdRig(t, false)
		g.r.Cfg.Current().Repositories[0].Classes["docs-links"] = config.Class{Live: true, MaxPatchFiles: 5} // a diff condition: DecideArm decides again on the read diff
		merges := 0
		g.r.Merger = countingMerger{Merger: g.f, merges: &merges}
		held(t, g, merges, g.reconcile(t, "3buqdlot", 1))
		g.pass(t, parent.Name, headH1)
		if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging || merges != 1 {
			t.Fatalf("%s/%s, %d merges", tk.Status.Phase, tk.Status.Reason, merges)
		}
	})

	t.Run("a moved head closes the release", func(t *testing.T) {
		g, parent := holdRig(t, false)
		merges := 0
		g.r.Merger = countingMerger{Merger: g.f, merges: &merges}
		g.pass(t, parent.Name, headH1)
		g.patch(t, func(tk *v1alpha1.Task) { tk.Status.Coordination.IntegrationAcceptedHead = headH1 })
		g.f.SetPR(gatePR(headH2, rid(0)))
		g.log.handoff(rid(0), headH2)
		g.f.SetChecks(12, green("SUCCESS"))
		held(t, g, merges, g.reconcile(t, "3buqdlot", 1))
		// The parent sees the moved head and drops the stale release.
		g.r.Cfg.Current().Coordination.Mode = config.CoordinationExecute
		g.r.Coordination = &fakeCoordination{ready: map[string]error{}}
		g.reconcile(t, parent.Name, 2)
		if p, _ := g.coordTask(t, parent.Name); p.Status.Coordination.Children[1].IntegrationRelease != nil {
			t.Fatalf("a stale release survived: %+v", p.Status.Coordination.Children[1].IntegrationRelease)
		}
	})

	t.Run("maintainer approved, through the approval's return", func(t *testing.T) {
		g, parent := holdRig(t, false)
		g.r.Cfg.Current().Repositories[0].Merge.ApproveToMerge = true
		g.r.Cfg.Current().Repositories[0].Classes["docs-links"] = config.Class{} // unarmed: only the approval can merge it
		merges := 0
		g.r.Merger = countingMerger{Merger: g.f, merges: &merges}
		if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != "class_unarmed" {
			t.Fatalf("%s/%s", tk.Status.Phase, tk.Status.Reason)
		}
		g.r.Now = func() time.Time { return now.Add(10 * time.Minute) }
		pr, _ := g.f.PullRequest(t.Context(), 12)
		pr.Reviews = []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: headH1, At: now.Add(10 * time.Minute)}}
		g.f.SetPR(pr)
		if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingCI || tk.Status.Reason != "reentered_approved" {
			t.Fatalf("return: %s/%s", tk.Status.Phase, tk.Status.Reason)
		}
		held(t, g, merges, g.reconcile(t, "3buqdlot", 1))
		g.pass(t, parent.Name, headH1)
		tk := g.reconcile(t, "3buqdlot", 1)
		if tk.Status.Phase != v1alpha1.PhaseAutoMerging || tk.Status.PullRequest.MergedHow != "approved" || merges != 1 {
			t.Fatalf("%s/%s %+v, %d merges", tk.Status.Phase, tk.Status.Reason, tk.Status.PullRequest, merges)
		}
	})

	t.Run("a maintainer's approval hand-off", func(t *testing.T) {
		g, _ := holdRig(t, false)
		pr, _ := g.f.PullRequest(t.Context(), 12)
		pr.Reviews = []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: headH1, At: now}}
		g.f.SetPR(pr)
		merges := 0
		g.r.Merger = countingMerger{Merger: g.f, merges: &merges}
		held(t, g, merges, g.reconcile(t, "3buqdlot", 1)) // no hand-off a human could merge past the hold
	})

	t.Run("a release gone stale before the pre-merge read", func(t *testing.T) {
		g, parent := holdRig(t, false)
		g.pass(t, parent.Name, headH1)
		merges := 0
		g.r.Merger = countingMerger{Merger: g.f, merges: &merges}
		g.r.Client = &racingClient{Client: g.c, parent: parent.Name, race: func() {
			p, _ := g.coordTask(t, parent.Name)
			p.Status.Coordination.Children[1].IntegrationRelease = nil
			if err := g.c.Status().Update(t.Context(), p); err != nil {
				t.Fatal(err)
			}
		}}
		held(t, g, merges, g.reconcile(t, "3buqdlot", 1))
	})

	t.Run("shadow would arm and never releases", func(t *testing.T) {
		g, parent := holdRig(t, false)
		g.r.Cfg.Current().Repositories[0].Classes = map[string]config.Class{"docs-links": {Shadow: true}}
		merges := 0
		g.r.Merger = countingMerger{Merger: g.f, merges: &merges}
		tk := g.reconcile(t, "3buqdlot", 1)
		if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != "shadow_would_arm" || merges != 0 {
			t.Fatalf("%s/%s, %d merges", tk.Status.Phase, tk.Status.Reason, merges)
		}
		if p, _ := g.coordTask(t, parent.Name); p.Status.Coordination.Children[1].IntegrationRelease != nil ||
			len(p.Status.Coordination.IntegrationAttempts) != 0 {
			t.Fatalf("shadow supplied a release: %+v", p.Status.Coordination)
		}
	})

	t.Run("an independent child has no hold", func(t *testing.T) {
		g, _ := holdRig(t, true)
		merges := 0
		g.r.Merger = countingMerger{Merger: g.f, merges: &merges}
		if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging || merges != 1 {
			t.Fatalf("%s/%s, %d merges", tk.Status.Phase, tk.Status.Reason, merges)
		}
	})
}

// withIntegrationHold rewrites every arm of a required, unaccepted head and a maintainer's
// approval hand-off, and nothing else: shadow, a wait and every refusal stand.
func TestWithIntegrationHold(t *testing.T) {
	need := IntegrationGate{Required: true, Reason: reasonIntegrationPending}
	for _, tc := range []struct {
		g    IntegrationGate
		in   ArmDecision
		want string
	}{
		{need, ArmDecision{Verdict: "arm"}, "wait/integration_pending"},
		{need, ArmDecision{Verdict: "arm", Reason: "maintainer_approved"}, "wait/integration_pending"},
		{need, ArmDecision{Verdict: "human", Reason: "maintainer_approved"}, "wait/integration_pending"},
		{need, ArmDecision{Verdict: "shadow", Reason: "shadow_would_arm"}, "shadow/shadow_would_arm"},
		{need, ArmDecision{Verdict: "human", Reason: "changes_requested"}, "human/changes_requested"},
		{need, ArmDecision{Verdict: "ci_red", Reason: "ci_red"}, "ci_red/ci_red"},
		{IntegrationGate{Required: true, Accepted: true, AcceptedHead: headH1}, ArmDecision{Verdict: "arm"}, "arm/"},
		{IntegrationGate{}, ArmDecision{Verdict: "arm"}, "arm/"},
	} {
		d := withIntegrationHold(tc.g, tc.in)
		if got := d.Verdict + "/" + d.Reason; got != tc.want {
			t.Errorf("%+v %+v: %s, want %s", tc.g, tc.in, got, tc.want)
		}
	}
}
