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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/factory/taskid"
)

// child is part k's first Task name.
func child(k int) string { return taskid.Name(taskid.SplitKey("3buqdlot", k)) }

// childTask reads part k's first Task.
func childTask(t *testing.T, g *rig, k int) *v1alpha1.Task {
	t.Helper()
	var tk v1alpha1.Task
	if err := g.c.Get(t.Context(), taskKey(child(k)), &tk); err != nil {
		t.Fatalf("part %d: %v", k, err)
	}
	return &tk
}

// approved is a proposed split a maintainer approved, after the parent's pass that opens part 1.
func approved(t *testing.T) *rig {
	t.Helper()
	g := proposed(t)
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	g.f.SetComments(7, splitBy(81, "Smana"))
	g.reconcile(t, "3buqdlot", 2) // Escalated → Split, then part 1
	return g
}

// SL7, SL8, offline proof 4: approval opens part 1 only: its issue with the title, the marker and
// both labels, its sub-issue link, its Task with the parent, the scope, and the approver as payer.
func TestApprovalOpensPartOneOnly(t *testing.T) {
	g := approved(t)
	created := g.f.Created()
	if len(created) != 1 || !strings.HasPrefix(created[0], "Part 1 of 2 of #7\n") || !strings.Contains(created[0], "split=3buqdlot/1") ||
		!strings.HasSuffix(created[0], "labels: factory/child-of:#7") || !strings.Contains(created[0], "on page 1") {
		t.Fatalf("issues %q", created)
	}
	if !slices.Contains(g.f.Calls(), "sub-issue 7 9501") {
		t.Fatalf("calls %v", g.f.Calls())
	}
	p := g.reconcile(t, "3buqdlot", 0)
	if p.Status.Phase != v1alpha1.PhaseSplit || len(p.Status.Split.Children) != 1 ||
		p.Status.Split.Children[0] != (v1alpha1.SplitChild{Index: 1, Issue: 501, Task: child(1)}) {
		t.Fatalf("%s %+v", p.Status.Phase, p.Status.Split)
	}
	c := childTask(t, g, 1)
	if *c.Spec.Parent != (v1alpha1.Parent{Task: "3buqdlot", Issue: 7, Index: 1, Of: 2}) || c.Spec.Issue != 501 ||
		c.Spec.Source.RequestedBy != "github:smana" || c.Labels[v1alpha1.LabelParent] != "3buqdlot" || c.Labels[v1alpha1.LabelIssue] != "501" ||
		c.Spec.Scope == nil || !strings.Contains(c.Spec.Scope.Task, "on page 1") {
		t.Fatalf("part 1 %+v %v", c.Spec, c.Labels)
	}
	var l v1alpha1.TaskList
	_ = g.c.List(t.Context(), &l)
	if len(l.Items) != 2 {
		t.Fatalf("one part at a time (SL7): %d tasks", len(l.Items))
	}
}

// SL5, SL9, SL10, SL11: a child's triage gives it its parent's text, class and steering, its scope,
// pair at standard, the family's cap, and no classifier call; its runs are the approver's.
func TestAChildIsTriagedFromItsParent(t *testing.T) {
	g := approved(t)
	g.reconcile(t, child(1), 3) // Received → Triaged → Queued → the implementer
	c := childTask(t, g, 1)
	p := g.reconcile(t, "3buqdlot", 0)
	if c.Spec.Text != parentText || c.Spec.Source.ContentSHA256 != p.Spec.Source.ContentSHA256 || c.Spec.PredictedClass != "review" ||
		c.Spec.ScopeMode != "scoper" || c.Spec.Template != "pair" || c.Spec.Budget.Tier != "standard" ||
		c.Spec.Budget.TaskMicroUSD != 5_100_000 || c.Status.Classification.Classifier != "split" || c.Status.Classification.Fit != "" {
		t.Fatalf("part 1 %+v %+v", c.Spec, c.Status.Classification)
	}
	s := g.runs.specs[runID(child(1), 0)]
	if s.Role != "implementer" || s.Branch != "agent/"+child(1) {
		t.Fatalf("no scoper for a part (SL5), its own branch: %+v", s)
	}
}

// R39: a part's triage marks it evaluated when its parent has a profile, and not otherwise; the
// merge reads the mark, never the parent.
func TestAPartIsMarkedEvaluatedFromItsParent(t *testing.T) {
	t.Run("a profiled parent", func(t *testing.T) {
		g, _ := harnessedSplit(t, 2)
		g.r.Now = func() time.Time { return now.Add(time.Hour) }
		g.f.SetComments(7, splitBy(80, "Smana"))
		g.reconcile(t, "3buqdlot", 2)
		if p := g.get(t); p.Status.Profile == nil {
			t.Fatalf("the parent carries the profile: %+v", p.Status)
		}
		g.reconcile(t, child(1), 3)
		if c := childTask(t, g, 1); !c.Status.Evaluated || c.Status.Profile != nil {
			t.Fatalf("marked, no profile of its own: %+v", c.Status)
		}
	})
	t.Run("a parent with none", func(t *testing.T) {
		g := approved(t)
		if p := g.reconcile(t, "3buqdlot", 0); p.Status.Profile != nil {
			t.Fatalf("no profile on this parent: %+v", p.Status.Profile)
		}
		g.reconcile(t, child(1), 3)
		if c := childTask(t, g, 1); c.Status.Evaluated {
			t.Fatalf("not marked: %+v", c.Status)
		}
	})
}

// SR10, offline proof 4: a replay after a lost write following CreateIssue adopts the issue by its
// marker and opens none; the Task's AlreadyExists is the dedup.
func TestAReplayOpensNoSecondIssue(t *testing.T) {
	g := approved(t)
	p := g.reconcile(t, "3buqdlot", 0)
	p.Status.Split.Children = nil // the write that recorded part 1 was lost
	if err := g.c.Status().Update(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	p = g.reconcile(t, "3buqdlot", 1)
	if len(g.f.Created()) != 1 || len(p.Status.Split.Children) != 1 || p.Status.Split.Children[0].Issue != 501 {
		t.Fatalf("issues %q children %+v", g.f.Created(), p.Status.Split.Children)
	}
}

// SL8: a failed sub-issue link blocks nothing.
func TestAFailedSubIssueLinkBlocksNothing(t *testing.T) {
	g := proposed(t)
	g.f.FailSubIssues(errors.New("422 sub-issues disabled"))
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	g.f.SetComments(7, splitBy(81, "Smana"))
	if p := g.reconcile(t, "3buqdlot", 2); len(p.Status.Split.Children) != 1 {
		t.Fatalf("children %+v", p.Status.Split.Children)
	}
}

// SL11: the approver pays, not the parent's labeller: a part's requestedBy is the approver's, and
// with per-person caps on its runs are charged to them.
func TestTheApproverPaysForTheParts(t *testing.T) {
	g := splitRigWith(t, "frontier", false, func(tk *v1alpha1.Task) { tk.Spec.Source.RequestedBy = "github:alice" })
	g.r.Cfg.Current().Budgets = personBudgets(true, false)
	g.reconcile(t, "3buqdlot", 3)
	splitHandoffOf(g, 0, readyScope(), parts(2))
	endScoper(t, g, "Succeeded", "agent_finished")
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	g.f.SetComments(7, splitBy(81, "Smana"))
	g.reconcile(t, "3buqdlot", 2)
	c := childTask(t, g, 1)
	if c.Spec.Source.RequestedBy != "github:smana" || c.Payer() != "github:smana" || runs.TaskPayer(c, true) != "github:smana" {
		t.Fatalf("requestedBy %q payer %q", c.Spec.Source.RequestedBy, c.Payer())
	}
}

// setPhase writes a task's phase and reason as its own reconcile would have.
func setPhase(t *testing.T, g *rig, name, phase, reason string) {
	t.Helper()
	var tk v1alpha1.Task
	if err := g.c.Get(t.Context(), taskKey(name), &tk); err != nil {
		t.Fatal(err)
	}
	tk.Status.Phase, tk.Status.Reason = phase, reason
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 600 + len(name), URL: "https://github.com/Smana/cloud-native-ref/pull/" + strconv.Itoa(600+len(name))}
	if err := g.c.Status().Update(t.Context(), &tk); err != nil {
		t.Fatal(err)
	}
}

// SL7, SL12, offline proof 5: part 1 Done opens part 2 with min(5.10, left); part 2 Done closes the
// parent issue and ends the parent Done, children_merged, counting each part once.
func TestThePartsLandOneByOneAndCloseTheParent(t *testing.T) {
	g := approved(t)
	setPhase(t, g, child(1), v1alpha1.PhaseDone, "merged")
	p := g.reconcile(t, "3buqdlot", 1)
	if len(p.Status.Split.Children) != 2 || p.Status.Split.Children[0].Outcome != "landed" || len(g.f.Created()) != 2 ||
		!strings.HasPrefix(g.f.Created()[1], "Part 2 of 2 of #7\n") {
		t.Fatalf("%+v %q", p.Status.Split, g.f.Created())
	}
	g.reconcile(t, child(2), 1)
	if c := childTask(t, g, 2); c.Spec.Budget.TaskMicroUSD != 5_100_000 {
		t.Fatalf("part 2's cap %d", c.Spec.Budget.TaskMicroUSD)
	}
	setPhase(t, g, child(2), v1alpha1.PhaseDone, "merged")
	p = g.reconcile(t, "3buqdlot", 1)
	if p.Status.Phase != v1alpha1.PhaseDone || p.Status.Reason != "children_merged" || !slices.Contains(g.f.Calls(), "close-issue 7") {
		t.Fatalf("%s %s %v", p.Status.Phase, p.Status.Reason, g.f.Calls())
	}
	if n := strings.Count(strings.Join(g.metrics.recorded, "|"), "split_child review landed"); n != 2 || p.Status.Classification.Fit != "" {
		t.Fatalf("counted %d, fit %q: %v", n, p.Status.Classification.Fit, g.metrics.recorded)
	}
	g.r.Now = func() time.Time { return now.Add(time.Hour + g.r.settleWindow(g.r.Cfg.Current())) }
	g.reconcile(t, "3buqdlot", 1)
	if last := g.f.Comments(7)[len(g.f.Comments(7))-1]; !strings.Contains(last, "every part of the split merged") || !strings.Contains(last, "#501") {
		t.Fatalf("the parent's end lists its parts: %q", last)
	}
}

// SL12: a part's issue closed with no landed task, or a part reverted, ends the parent Closed,
// child_dropped, and opens no further part.
func TestADroppedOrRevertedPartEndsTheFamily(t *testing.T) {
	g := approved(t)
	setPhase(t, g, child(1), v1alpha1.PhaseStopped, "stopped_by_label")
	if p := g.reconcile(t, "3buqdlot", 1); p.Status.Phase != v1alpha1.PhaseSplit {
		t.Fatalf("a stopped part with its issue open keeps the parent waiting (SL12): %s", p.Status.Phase)
	}
	_ = g.f.CloseIssue(t.Context(), 501)
	p := g.reconcile(t, "3buqdlot", 1)
	if p.Status.Phase != v1alpha1.PhaseClosed || p.Status.Reason != "child_dropped" || p.Status.Split.Children[0].Outcome != "dropped" ||
		len(g.f.Created()) != 1 || slices.Contains(g.f.Calls(), "close-issue 7") || !slices.Contains(g.metrics.recorded, "split_child review dropped") {
		t.Fatalf("%s %s %+v %v", p.Status.Phase, p.Status.Reason, p.Status.Split, g.metrics.recorded)
	}
	g = approved(t)
	setPhase(t, g, child(1), v1alpha1.PhaseDone, "merged")
	g.reconcile(t, "3buqdlot", 1) // part 2 opens
	setPhase(t, g, child(1), v1alpha1.PhaseReverted, "main_red")
	p = g.reconcile(t, "3buqdlot", 1)
	if p.Status.Phase != v1alpha1.PhaseClosed || p.Status.Split.Children[0].Outcome != "reverted" {
		t.Fatalf("%s %+v", p.Status.Phase, p.Status.Split)
	}
	// The running part 2 must not outlive the family: stopped, its outcome recorded and counted.
	if len(p.Status.Split.Children) != 2 || p.Status.Split.Children[1].Outcome != "dropped" ||
		childTask(t, g, 2).Annotations[v1alpha1.AnnotationStop] != StopFamilyDropped || !slices.Contains(g.metrics.recorded, "split_child review dropped") {
		t.Fatalf("%+v %v", p.Status.Split, childTask(t, g, 2).Annotations)
	}
	if c := g.reconcile(t, child(2), 1); c.Status.Phase != v1alpha1.PhaseStopped || c.Status.Reason != "family_dropped" {
		t.Fatalf("part 2: %s %s", c.Status.Phase, c.Status.Reason)
	}
}

// A task labelled as a child that names another parent is no part of this family: its spend is not
// charged to the family's cap (the label is anyone's, the spec is CEL's).
func TestAStrayLabelledTaskIsNotInTheFamily(t *testing.T) {
	g := approved(t)
	before, err := g.r.familyLeft(t.Context(), g.reconcile(t, "3buqdlot", 0))
	if err != nil {
		t.Fatal(err)
	}
	stray := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "zzstray", Namespace: "agent-system", Labels: map[string]string{v1alpha1.LabelParent: "3buqdlot"}},
		Spec: v1alpha1.TaskSpec{Parent: &v1alpha1.Parent{Task: "other", Issue: 9, Index: 1, Of: 2}}}
	if err := g.c.Create(t.Context(), stray); err != nil {
		t.Fatal(err)
	}
	stray.Status.Runs = []v1alpha1.RunRecord{{ID: "x", Role: "implementer", CostMicroUSD: 5_000_000, Tokens: 1}}
	if err := g.c.Status().Update(t.Context(), stray); err != nil {
		t.Fatal(err)
	}
	if after, err := g.r.familyLeft(t.Context(), g.reconcile(t, "3buqdlot", 0)); err != nil || after != before {
		t.Fatalf("left %d then %d (%v)", before, after, err)
	}
}

// A part whose run falls back to token caps (an unpriced run) is bounded in tokens by what the
// family has left at the reference rate, the rate spend() charges such a run: the USD cap alone
// would let it spend past the family's.
func TestAPartsTokenCapFollowsWhatTheFamilyHasLeft(t *testing.T) {
	g := approved(t)
	var c v1alpha1.Task
	_ = g.c.Get(t.Context(), taskKey(child(1)), &c)
	c.Status.Phase, c.Status.Reason = v1alpha1.PhaseDone, "merged"
	c.Status.Runs = []v1alpha1.RunRecord{{ID: runID(child(1), 0), Role: "implementer", Trigger: "initial", CostMicroUSD: 17_000_000, Tokens: 1}}
	_ = g.c.Status().Update(t.Context(), &c)
	p := g.reconcile(t, "3buqdlot", 1)
	left, err := g.r.familyLeft(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	g.reconcile(t, child(2), 1)
	got := childTask(t, g, 2).Spec.Budget
	if want := int64(float64(left) / 1.70); got.TaskTokens != want || got.TaskTokens >= 3_000_000 || got.TaskMicroUSD != left {
		t.Fatalf("tokens %d want %d (left %d), usd %d", got.TaskTokens, want, left, got.TaskMicroUSD)
	}
}

// Offline proof 4, SR10: Create of the part's Task fails once after its issue was opened; the
// replay adopts the issue by its marker and ends with one issue and one Task.
func TestAFailedCreateReplaysToOneIssueAndOneTask(t *testing.T) {
	g := proposed(t)
	fail := true
	c := interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		if tk, ok := o.(*v1alpha1.Task); ok && tk.Spec.Parent != nil && fail {
			fail = false
			return errors.New("apiserver blip")
		}
		return cl.Create(ctx, o, opts...)
	}})
	g.r.Client = c
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	g.f.SetComments(7, splitBy(81, "Smana"))
	req := ctrl.Request{NamespacedName: taskKey("3buqdlot")}
	_, _ = g.r.Reconcile(t.Context(), req) // Escalated → Split
	if _, err := g.r.Reconcile(t.Context(), req); err == nil {
		t.Fatal("the Task create fails once")
	}
	if _, err := g.r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	var l v1alpha1.TaskList
	_ = g.c.List(t.Context(), &l)
	if len(g.f.Created()) != 1 || len(l.Items) != 2 {
		t.Fatalf("issues %q tasks %d", g.f.Created(), len(l.Items))
	}
}

// openRevertRig is an approved split whose part 1 auto-merged as PR 12, ready to be reverted.
func openRevertRig(t *testing.T) (*rig, *v1alpha1.Task) {
	t.Helper()
	g := approved(t)
	g.r.Merger = g.f
	g.r.Cfg.Current().Repositories[0].Classes = map[string]config.Class{"revert": {Live: true}}
	g.f.SetPR(forge.PR{Number: 12, NodeID: "PR_12", State: "MERGED", Title: "docs: part 1"})
	c := childTask(t, g, 1)
	merged := metav1.NewTime(now)
	c.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, NodeID: "PR_12", AutoMerged: true, MergedAt: &merged}
	return g, c
}

func toParent(c *v1alpha1.Task) bool {
	return slices.ContainsFunc(c.Status.Outbox, func(o v1alpha1.Narration) bool { return o.Number == 7 && strings.Contains(o.Body, "#901") })
}

// SP6, through revert(): a part reverted after its parent closed comments on the parent issue;
// before the close, nothing extra.
func TestARevertedPartCommentsOnAClosedParent(t *testing.T) {
	g, c := openRevertRig(t)
	if err := g.r.revert(t.Context(), g.r.Cfg.Current(), c, "main_red"); err != nil || toParent(c) {
		t.Fatalf("before the close nothing extra: %+v %v", c.Status.Outbox, err)
	}
	g, c = openRevertRig(t)
	setPhase(t, g, "3buqdlot", v1alpha1.PhaseDone, "children_merged")
	if err := g.r.revert(t.Context(), g.r.Cfg.Current(), c, "main_red"); err != nil || !toParent(c) {
		t.Fatalf("%+v %v", c.Status.Outbox, err)
	}
}

// A part reverted while its parent waits Escalated on budget-family is told to the parent's issue,
// and an expiring parent does not count that part as landed.
func TestARevertedPartIsNotCountedLandedByAnExpiringParent(t *testing.T) {
	g, c := openRevertRig(t)
	var p v1alpha1.Task
	_ = g.c.Get(t.Context(), taskKey("3buqdlot"), &p)
	p.Status.Phase, p.Status.Reason = v1alpha1.PhaseEscalated, "budget-family"
	p.Status.Split.Children[0].Outcome = "landed"
	if err := g.c.Status().Update(t.Context(), &p); err != nil {
		t.Fatal(err)
	}
	if err := g.r.revert(t.Context(), g.r.Cfg.Current(), c, "main_red"); err != nil || !toParent(c) {
		t.Fatalf("%+v %v", c.Status.Outbox, err)
	}
	_ = g.c.Get(t.Context(), taskKey("3buqdlot"), &p)
	if got := p.Status.Split.Children[0].Outcome; got != "reverted" {
		t.Fatalf("outcome %q", got)
	}
	g.r.countFamily(t.Context(), &p)
	if slices.Contains(g.metrics.recorded, "split_child review landed") || !slices.Contains(g.metrics.recorded, "split_child review reverted") {
		t.Fatalf("%v", g.metrics.recorded)
	}
}

// A part's issue names its real branch, not a placeholder.
func TestAPartsIssueNamesItsBranch(t *testing.T) {
	g := approved(t)
	if body := g.f.Created()[0]; !strings.Contains(body, "`agent/"+child(1)+"`") || strings.Contains(body, "<task>") {
		t.Fatalf("%q", body)
	}
}

// A tie in creation time breaks on the name, the same way both ways round.
func TestNewerBreaksTiesByName(t *testing.T) {
	ts := metav1.NewTime(now)
	a := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "aaa", CreationTimestamp: ts}}
	b := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "bbb", CreationTimestamp: ts}}
	if newer(a, b) == newer(b, a) {
		t.Fatal("a tie must have one winner")
	}
}

// SL12: an escalated part keeps the parent waiting while its issue is open.
func TestAnEscalatedPartKeepsTheParentWaiting(t *testing.T) {
	g := approved(t)
	setPhase(t, g, child(1), v1alpha1.PhaseEscalated, "ci_red")
	if p := g.reconcile(t, "3buqdlot", 1); p.Status.Phase != v1alpha1.PhaseSplit || len(g.f.Created()) != 1 {
		t.Fatalf("%s %q", p.Status.Phase, g.f.Created())
	}
}

// SL10, offline proofs 5 and 8: a family that cannot hold one more run ends Escalated, budget-family,
// and a retry never runs the parent whole; the family's selector sums its spend.
func TestTheFamilysCapHolds(t *testing.T) {
	g := approved(t)
	var c v1alpha1.Task
	_ = g.c.Get(t.Context(), taskKey(child(1)), &c)
	c.Status.Phase, c.Status.Reason = v1alpha1.PhaseDone, "merged"
	c.Status.Runs = []v1alpha1.RunRecord{{ID: runID(child(1), 0), Role: "implementer", Trigger: "initial", CostMicroUSD: 18_500_000, Tokens: 1}}
	_ = g.c.Status().Update(t.Context(), &c)
	p := g.reconcile(t, "3buqdlot", 1)
	if p.Status.Phase != v1alpha1.PhaseEscalated || p.Status.Reason != "budget-family" || len(g.f.Created()) != 1 {
		t.Fatalf("%s %s %q", p.Status.Phase, p.Status.Reason, g.f.Created())
	}
	left, err := g.r.familyLeft(t.Context(), p)
	if err != nil || left != p.Spec.Split.FamilyMicroUSD-spend(p)-18_500_000 {
		t.Fatalf("left %d (%v)", left, err)
	}
	g.r.Now = func() time.Time { return now.Add(2 * time.Hour) }
	g.f.SetComments(7, retryBy(90, "Smana", -90*time.Minute))
	if p = g.reconcile(t, "3buqdlot", 1); p.Status.Phase != v1alpha1.PhaseEscalated {
		t.Fatalf("a retry ran an approved parent (SR9): %s", p.Status.Phase)
	}
}

// A priced run is USD-capped: a part whose tier has a USD price is not refused on tokens, however
// few the family's remaining dollars leave at the reference rate; an unpriced tier keeps the token
// bound (and no USD one).
func TestAPricedPartIsNotRefusedOnTokens(t *testing.T) {
	g := approved(t)
	p := g.reconcile(t, "3buqdlot", 0)
	tr := g.r.Cfg.Current().Tiers["standard"]
	tr.RunTokens, tr.TaskTokens = 6_000_000, 12_000_000 // more tokens than the dollars left buy at the reference rate
	g.r.Cfg.Current().Tiers["standard"] = tr
	if !g.r.partFits(g.r.Cfg.Current(), p, 2_600_000) {
		t.Error("a priced part with a run's dollars left is refused on tokens")
	}
	if g.r.partFits(g.r.Cfg.Current(), p, 2_000_000) {
		t.Error("a priced part without a run's dollars left fits")
	}
	tr.RunUSD, tr.TaskUSD = 0, 0 // no USD price: the token bound decides
	g.r.Cfg.Current().Tiers["standard"] = tr
	if g.r.partFits(g.r.Cfg.Current(), p, 2_600_000) {
		t.Error("an unpriced part fits past its token bound")
	}
	tr.RunTokens, tr.TaskTokens = 1_500_000, 3_000_000
	g.r.Cfg.Current().Tiers["standard"] = tr
	if !g.r.partFits(g.r.Cfg.Current(), p, 5_000_000) {
		t.Error("an unpriced part within its token bound is refused")
	}
}

// parallelPart builds a ready split part with declared files, checks and optional dependencies.
func parallelPart(k int, files, checks []string, deps ...int) envelope.Scope {
	sc := part(k)
	sc.Files = files
	sc.Checks = checks
	sc.DependsOn = deps
	return sc
}

// approvedParallel builds an approved split rig with split.parallel = 2 and the given split parts.
func approvedParallel(t *testing.T, sp envelope.Split) *rig {
	t.Helper()
	g := splitRig(t, "frontier", false)
	g.r.Cfg.Current().Loops = map[string]config.Loop{
		"default": {
			Scope: config.ScopeScoper,
			Split: &config.SplitPolicy{MaxChildren: 4, Parallel: config.MaxParallelParts},
		},
	}
	g.reconcile(t, "3buqdlot", 3) // Received -> Triaged -> Queued -> scoper run
	splitHandoffOf(g, 0, readyScope(), sp)
	endScoper(t, g, "Succeeded", "agent_finished")
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	g.f.SetComments(7, splitBy(81, "Smana"))
	g.reconcile(t, "3buqdlot", 2) // Escalated -> Split -> open eligible parts
	return g
}

// Phase H3: with split.parallel = 2, two parts declaring disjoint files, valid repository checks,
// and no dependency between them open and run simultaneously, each with its own branch, PR and room.
func TestDisjointPartsRunTwoAtATime(t *testing.T) {
	sp := envelope.Split{
		Why: "Independent documentation sections.",
		Children: []envelope.Scope{
			parallelPart(1, []string{"docs/alpha/*.md"}, []string{"Pre-commit checks"}),
			parallelPart(2, []string{"docs/beta/*.md"}, []string{"Pre-commit checks"}),
		},
	}
	g := approvedParallel(t, sp)
	p := g.reconcile(t, "3buqdlot", 0)
	if p.Status.Phase != v1alpha1.PhaseSplit || len(p.Status.Split.Children) != 2 {
		t.Fatalf("expected 2 open parts, got phase=%s split=%+v", p.Status.Phase, p.Status.Split)
	}
	// Both parts triage and start their own implementer runs on distinct branches and rooms.
	g.reconcile(t, child(1), 3)
	g.reconcile(t, child(2), 3)
	s1 := g.runs.specs[runID(child(1), 0)]
	s2 := g.runs.specs[runID(child(2), 0)]
	if s1.Branch == "" || s2.Branch == "" || s1.Branch == s2.Branch || s1.RoomRef == s2.RoomRef {
		t.Fatalf("each part must keep its own branch and room: s1=%+v s2=%+v", s1, s2)
	}
	// Landing both parts closes the parent.
	setPhase(t, g, child(1), v1alpha1.PhaseDone, "merged")
	setPhase(t, g, child(2), v1alpha1.PhaseDone, "merged")
	p = g.reconcile(t, "3buqdlot", 1)
	if p.Status.Phase != v1alpha1.PhaseDone || p.Status.Reason != ReasonChildrenMerged {
		t.Fatalf("parent after both land: phase=%s reason=%s", p.Status.Phase, p.Status.Reason)
	}
}

// Phase H3: parts whose declared files overlap never run at the same time.
func TestPartsWithASharedFileNeverRunTogether(t *testing.T) {
	sp := envelope.Split{
		Why: "Shared glob overlaps.",
		Children: []envelope.Scope{
			parallelPart(1, []string{"docs/*.md"}, []string{"Pre-commit checks"}),
			parallelPart(2, []string{"docs/page-2.md"}, []string{"Pre-commit checks"}),
		},
	}
	g := approvedParallel(t, sp)
	p := g.reconcile(t, "3buqdlot", 0)
	if len(p.Status.Split.Children) != 1 || p.Status.Split.Children[0].Index != 1 {
		t.Fatalf("overlapping parts must run one at a time: %+v", p.Status.Split.Children)
	}
	// Once part 1 lands, part 2 opens.
	setPhase(t, g, child(1), v1alpha1.PhaseDone, "merged")
	p = g.reconcile(t, "3buqdlot", 1)
	if len(p.Status.Split.Children) != 2 || p.Status.Split.Children[1].Index != 2 {
		t.Fatalf("part 2 should open once part 1 lands: %+v", p.Status.Split.Children)
	}
}

// Phase H3: a part that omits its own checks (or names an undeclared check) runs alone.
func TestAPartWithoutItsOwnCheckRunsAlone(t *testing.T) {
	for name, sp := range map[string]envelope.Split{
		"second part missing check": {
			Why: "Part 2 declares no check.",
			Children: []envelope.Scope{
				parallelPart(1, []string{"docs/a.md"}, []string{"Pre-commit checks"}),
				parallelPart(2, []string{"docs/b.md"}, nil),
			},
		},
		"first part missing check": {
			Why: "Part 1 declares no check.",
			Children: []envelope.Scope{
				parallelPart(1, []string{"docs/a.md"}, nil),
				parallelPart(2, []string{"docs/b.md"}, []string{"Pre-commit checks"}),
			},
		},
		"undeclared check": {
			Why: "Part 2 names a check not declared on the repository.",
			Children: []envelope.Scope{
				parallelPart(1, []string{"docs/a.md"}, []string{"Pre-commit checks"}),
				parallelPart(2, []string{"docs/b.md"}, []string{"unknown-check"}),
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := approvedParallel(t, sp)
			p := g.reconcile(t, "3buqdlot", 0)
			if len(p.Status.Split.Children) != 1 {
				t.Fatalf("part without valid check must run alone: %+v", p.Status.Split.Children)
			}
		})
	}
}

// Phase H3: a part that touches a file outside its declared files ends Escalated
// outside_declared_files and serialises the family back to one part at a time.
func TestAPartOutsideItsFilesSerialisesTheFamily(t *testing.T) {
	sp := envelope.Split{
		Why: "Four disjoint parts.",
		Children: []envelope.Scope{
			parallelPart(1, []string{"docs/a.md"}, []string{"Pre-commit checks"}),
			parallelPart(2, []string{"docs/b.md"}, []string{"Pre-commit checks"}),
			parallelPart(3, []string{"docs/c.md"}, []string{"Pre-commit checks"}),
			parallelPart(4, []string{"docs/d.md"}, []string{"Pre-commit checks"}),
		},
	}
	g := approvedParallel(t, sp)
	p := g.reconcile(t, "3buqdlot", 0)
	if len(p.Status.Split.Children) != 2 {
		t.Fatalf("expected parts 1 and 2 open initially: %+v", p.Status.Split.Children)
	}

	// Run part 1's implementer and have its PR modify a file outside docs/a.md.
	g.reconcile(t, child(1), 3) // Received -> Triaged -> Queued -> Implementing
	pr1 := forge.PR{Number: 61, NodeID: "PR_61", State: "OPEN", HeadRef: "agent/" + child(1), HeadSHA: strings.Repeat("1", 40), URL: "https://github.com/Smana/cloud-native-ref/pull/61"}
	g.f.SetPR(pr1)
	g.f.SetBranch("agent/"+child(1), 61)
	g.f.SetPullFiles(61, forge.FilePatch{Path: "docs/a.md"}, forge.FilePatch{Path: "docs/secret.md"})
	r1 := runID(child(1), 0)
	g.runs.set(r1, "Succeeded")
	g.reconcile(t, child(1), 1)
	g.r.Now = func() time.Time { return now.Add(time.Hour + 2*time.Minute) }
	c1 := g.reconcile(t, child(1), 1)
	if c1.Status.Phase != v1alpha1.PhaseEscalated || c1.Status.Reason != ReasonOutsideDeclaredFiles {
		t.Fatalf("part 1 phase=%q reason=%q, want Escalated / %s", c1.Status.Phase, c1.Status.Reason, ReasonOutsideDeclaredFiles)
	}

	// Parent sees part 1 escalated outside_declared_files and serialises the family.
	g.f.SetIssue(forge.Issue{Number: p.Status.Split.Children[0].Issue, State: "OPEN"})
	p = g.reconcile(t, "3buqdlot", 1)
	if !p.Status.Split.Serialised {
		t.Fatal("parent must record split.serialised = true after outside_declared_files")
	}

	// Even after parts 1 and 2 both land, parts 3 and 4 now run ONE at a time.
	setPhase(t, g, child(1), v1alpha1.PhaseDone, "merged")
	setPhase(t, g, child(2), v1alpha1.PhaseDone, "merged")
	p = g.reconcile(t, "3buqdlot", 1)
	if len(p.Status.Split.Children) != 3 || p.Status.Split.Children[2].Index != 3 {
		t.Fatalf("serialised family must open only part 3 (not 3 and 4): %+v", p.Status.Split.Children)
	}
}

// Phase H3: never more than MaxParallelParts (2) parts open at once, even with 4 disjoint parts.
func TestNeverMoreThanTwoPartsOpen(t *testing.T) {
	sp := envelope.Split{
		Why: "Four disjoint parts.",
		Children: []envelope.Scope{
			parallelPart(1, []string{"docs/1.md"}, []string{"Pre-commit checks"}),
			parallelPart(2, []string{"docs/2.md"}, []string{"Pre-commit checks"}),
			parallelPart(3, []string{"docs/3.md"}, []string{"Pre-commit checks"}),
			parallelPart(4, []string{"docs/4.md"}, []string{"Pre-commit checks"}),
		},
	}
	g := approvedParallel(t, sp)
	p := g.reconcile(t, "3buqdlot", 3) // extra reconciles must never open a 3rd
	if len(p.Status.Split.Children) != config.MaxParallelParts {
		t.Fatalf("opened %d parts, want at most %d (%+v)", len(p.Status.Split.Children), config.MaxParallelParts, p.Status.Split.Children)
	}
	// Landing part 1 opens part 3 (still at most 2 open: part 2 and part 3).
	setPhase(t, g, child(1), v1alpha1.PhaseDone, "merged")
	p = g.reconcile(t, "3buqdlot", 2)
	if len(p.Status.Split.Children) != 3 {
		t.Fatalf("after part 1 lands, opened %d parts, want 3 (2 open: #2 and #3)", len(p.Status.Split.Children))
	}
}

// Phase H3: the family USD cap still ends a parallel family Escalated budget-family.
func TestTheFamilyCapStillEndsTheFamily(t *testing.T) {
	sp := envelope.Split{
		Why: "Three disjoint parts.",
		Children: []envelope.Scope{
			parallelPart(1, []string{"docs/1.md"}, []string{"Pre-commit checks"}),
			parallelPart(2, []string{"docs/2.md"}, []string{"Pre-commit checks"}),
			parallelPart(3, []string{"docs/3.md"}, []string{"Pre-commit checks"}),
		},
	}
	g := approvedParallel(t, sp)
	// Parts 1 and 2 together spend past the family cap ($20.40 -> leaves < $2.55 run cap).
	var c1, c2 v1alpha1.Task
	_ = g.c.Get(t.Context(), taskKey(child(1)), &c1)
	c1.Status.Phase, c1.Status.Reason = v1alpha1.PhaseDone, "merged"
	c1.Status.Runs = []v1alpha1.RunRecord{{ID: runID(child(1), 0), Role: "implementer", Trigger: "initial", CostMicroUSD: 10_000_000, Tokens: 1}}
	_ = g.c.Status().Update(t.Context(), &c1)

	_ = g.c.Get(t.Context(), taskKey(child(2)), &c2)
	c2.Status.Phase, c2.Status.Reason = v1alpha1.PhaseDone, "merged"
	c2.Status.Runs = []v1alpha1.RunRecord{{ID: runID(child(2), 0), Role: "implementer", Trigger: "initial", CostMicroUSD: 9_000_000, Tokens: 1}}
	_ = g.c.Status().Update(t.Context(), &c2)

	p := g.reconcile(t, "3buqdlot", 1)
	if p.Status.Phase != v1alpha1.PhaseEscalated || p.Status.Reason != ReasonBudgetFamily {
		t.Fatalf("parent phase=%s reason=%s, want Escalated / %s", p.Status.Phase, p.Status.Reason, ReasonBudgetFamily)
	}
	if len(p.Status.Split.Children) != 2 {
		t.Fatalf("part 3 must not open past family cap: %+v", p.Status.Split.Children)
	}
}
