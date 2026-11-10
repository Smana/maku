// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

var (
	recoveryAt = time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)
	headB2     = strings.Repeat("c", 40)
)

// recoveryFamily is p's filed parent with contract's Task contractt and consumer's consumert,
// contract merged at mergeA and consumer open at headB.
func recoveryFamily(p v1alpha1.CoordinationPlan) (*v1alpha1.Task, []v1alpha1.Task) {
	cs := &v1alpha1.CoordinationStatus{PlanVersion: p.Version, Plan: p.DeepCopy(), PlanHash: PlanHash(p)}
	var kids []v1alpha1.Task
	merged := metav1.NewTime(recoveryAt.Add(-time.Hour))
	for i, c := range p.Children {
		name := c.ID[:8]
		if len(name) < 8 {
			name += strings.Repeat("t", 8-len(name))
		}
		cs.Children = append(cs.Children, v1alpha1.CoordinationChildStatus{ID: c.ID, Repository: c.Repository, Issue: 21 + i, Task: name,
			Generation: 1, Fingerprint: ChildFingerprint(p, c.ID)})
		k := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.TaskSpec{Repository: c.Repository,
			CoordinationParent: &v1alpha1.CoordinationParent{Task: p.ID, PlanVersion: p.Version, ChildID: c.ID}},
			Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseAwaitingCI, PullRequest: &v1alpha1.PullRequestRef{Number: 12 + i, HeadSHA: headB}}}
		if i == 0 {
			k.Status.Phase = v1alpha1.PhaseDone
			k.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, HeadSHA: mergeA, MergeCommitSHA: mergeA, MergedAt: &merged}
		}
		kids = append(kids, k)
	}
	parent := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: p.ID}, Spec: v1alpha1.TaskSpec{Kind: v1alpha1.TaskKindCoordination},
		Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseCoordinating, Reason: ReasonFiled, Coordination: cs}}
	return parent, kids
}

// attempt is an integration attempt of the current plan over subjects, in state.
func attempt(cs *v1alpha1.CoordinationStatus, task, purpose, state, reason string, subjects ...v1alpha1.IntegrationSubject) {
	cs.IntegrationAttempts = append(cs.IntegrationAttempts, v1alpha1.IntegrationAttempt{Task: task, Purpose: purpose, PlanHash: cs.PlanHash,
		State: state, Reason: reason, Subjects: subjects})
}

func preMerge(head string) []v1alpha1.IntegrationSubject {
	return []v1alpha1.IntegrationSubject{{ChildID: "consumer", Repository: repoB, HeadSHA: head}, {ChildID: "contract", Repository: repoA, MergeCommitSHA: mergeA}}
}

// MR11: every failure row of the design's table proposes exactly its one action, selected from
// control state alone; none bundles a second action, reverts a merged sibling or tops up a budget.
func TestEveryFailureRowHasOneRecovery(t *testing.T) {
	p := draftPlan(t)
	for _, tc := range []struct {
		name   string
		edit   func(cs *v1alpha1.CoordinationStatus, kids []v1alpha1.Task, its *[]v1alpha1.Task)
		code   string
		child  string
		integ  string
		reason string
	}{
		{name: "a child PR closed unmerged, scope unchanged", edit: func(_ *v1alpha1.CoordinationStatus, k []v1alpha1.Task, _ *[]v1alpha1.Task) {
			k[1].Status.Phase, k[1].Status.Reason = v1alpha1.PhaseClosed, "pr_closed"
		}, code: RecoveryRetryChild, child: "consumer", reason: "pr_closed"},
		{name: "a child PR closed under a changed scope", edit: func(cs *v1alpha1.CoordinationStatus, k []v1alpha1.Task, _ *[]v1alpha1.Task) {
			k[1].Status.Phase = v1alpha1.PhaseClosed
			cs.Children[1].Fingerprint = strings.Repeat("0", 64)
		}, code: RecoveryRevisePlan, child: "consumer", reason: reasonPRClosed},
		{name: "a rejected child", edit: func(_ *v1alpha1.CoordinationStatus, k []v1alpha1.Task, _ *[]v1alpha1.Task) {
			k[1].Status.Phase, k[1].Status.Reason = v1alpha1.PhaseRejected, "coordination_scope_changed"
		}, code: RecoveryRevisePlan, child: "consumer", reason: "coordination_scope_changed"},
		{name: "a stopped child", edit: func(_ *v1alpha1.CoordinationStatus, k []v1alpha1.Task, _ *[]v1alpha1.Task) {
			k[1].Status.Phase, k[1].Status.Reason = v1alpha1.PhaseStopped, "stopped_by_label"
		}, code: RecoveryResumeChild, child: "consumer", reason: "stopped_by_label"},
		{name: "an escalated child", edit: func(_ *v1alpha1.CoordinationStatus, k []v1alpha1.Task, _ *[]v1alpha1.Task) {
			k[1].Status.Phase, k[1].Status.Reason = v1alpha1.PhaseEscalated, "review_rounds"
		}, code: RecoveryResumeChild, child: "consumer", reason: "review_rounds"},
		{name: "a child's allocation exhausted", edit: func(_ *v1alpha1.CoordinationStatus, k []v1alpha1.Task, _ *[]v1alpha1.Task) {
			k[1].Status.Phase, k[1].Status.Reason = v1alpha1.PhaseEscalated, "allocation_exhausted"
		}, code: RecoveryIncreaseBudget, child: "consumer", reason: "allocation_exhausted"},
		{name: "a pre-merge integration red on the current head", edit: func(cs *v1alpha1.CoordinationStatus, _ []v1alpha1.Task, _ *[]v1alpha1.Task) {
			attempt(cs, "integr01", PurposeBeforeDependentMerge, ResultFailed, "integration_failed", preMerge(headB)...)
		}, code: RecoveryRetryDependent, child: "consumer", integ: "integr01", reason: "integration_failed"},
		{name: "a pre-merge integration lost to infrastructure", edit: func(cs *v1alpha1.CoordinationStatus, _ []v1alpha1.Task, its *[]v1alpha1.Task) {
			attempt(cs, "integr01", PurposeBeforeDependentMerge, ResultPending, "", preMerge(headB)...)
			*its = append(*its, v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "integr01"}, Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseEscalated}})
		}, code: RecoveryRetryIntegration, integ: "integr01", reason: reasonIntegrationInf},
		{name: "the head moved after a pass", edit: func(cs *v1alpha1.CoordinationStatus, k []v1alpha1.Task, _ *[]v1alpha1.Task) {
			attempt(cs, "integr01", PurposeBeforeDependentMerge, ResultPassed, "", preMerge(headB)...)
			k[1].Status.PullRequest.HeadSHA = headB2
		}, code: RecoveryRerunIntegration, child: "consumer", reason: reasonHeadMoved},
		{name: "the producer reverted before final acceptance", edit: func(_ *v1alpha1.CoordinationStatus, k []v1alpha1.Task, _ *[]v1alpha1.Task) {
			k[0].Status.Phase, k[0].Status.Reason = v1alpha1.PhaseReverted, "revert_requested"
		}, code: RecoveryRevisePlan, child: "contract", reason: reasonReverted},
		{name: "the final integration red", edit: func(cs *v1alpha1.CoordinationStatus, _ []v1alpha1.Task, _ *[]v1alpha1.Task) {
			attempt(cs, "integr02", PurposeFinal, ResultFailed, "integration_failed",
				v1alpha1.IntegrationSubject{ChildID: "contract", MergeCommitSHA: mergeA}, v1alpha1.IntegrationSubject{ChildID: "consumer", MergeCommitSHA: headB})
		}, code: RecoveryRevisePlan, integ: "integr02", reason: "integration_failed"},
		{name: "the final integration lost to infrastructure", edit: func(cs *v1alpha1.CoordinationStatus, _ []v1alpha1.Task, _ *[]v1alpha1.Task) {
			attempt(cs, "integr02", PurposeFinal, ResultPending, reasonIntegrationInf,
				v1alpha1.IntegrationSubject{ChildID: "contract", MergeCommitSHA: mergeA}, v1alpha1.IntegrationSubject{ChildID: "consumer", MergeCommitSHA: headB})
		}, code: RecoveryRetryIntegration, integ: "integr02", reason: reasonIntegrationInf},
		{name: "a lost fence on a held child", edit: func(_ *v1alpha1.CoordinationStatus, k []v1alpha1.Task, _ *[]v1alpha1.Task) {
			k[1].Status.Phase, k[1].Status.Reason = v1alpha1.PhaseQueued, ErrPolicyChanged.Error()
		}, code: RecoveryAuthorizeChild, child: "consumer", reason: "policy_changed"},
		{name: "a lost write before admission", edit: func(cs *v1alpha1.CoordinationStatus, _ []v1alpha1.Task, _ *[]v1alpha1.Task) {
			cs.Children[1].Task, cs.Children[1].Reason = "", ErrRepositoryPermission.Error()
		}, code: RecoveryAuthorizeChild, child: "consumer", reason: "repository_permission"},
		{name: "integration runs exhausted", edit: func(cs *v1alpha1.CoordinationStatus, _ []v1alpha1.Task, _ *[]v1alpha1.Task) {
			cs.Children[1].Reason = ErrIntegrationRunsExhausted.Error()
		}, code: RecoveryIncreaseBudget, child: "consumer", reason: "integration_runs_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, kids := recoveryFamily(p)
			var its []v1alpha1.Task
			tc.edit(parent.Status.Coordination, kids, &its)
			got := ProposeRecovery(parent, kids, its, recoveryAt)
			want := v1alpha1.RecoveryProposal{Code: tc.code, ChildID: tc.child, IntegrationTask: tc.integ, Reason: tc.reason, PlanVersion: p.Version,
				RequiresConfirmation: tc.code == RecoveryRevisePlan || tc.code == RecoveryIncreaseBudget || tc.code == RecoveryAuthorizeChild,
				CreatedAt:            metav1.NewTime(recoveryAt)}
			if got == nil || *got != want {
				t.Fatalf("proposal %+v, want %+v", got, want)
			}
		})
	}
}

// Multiple failures: the first failed child in mergeOrder, then integration, then budget, one
// visible proposal at a time; nothing for a healthy family, a child being replaced, a superseded
// Task, or a terminal parent.
func TestRecoveryOrderAndSilence(t *testing.T) {
	p := draftPlan(t)
	for _, tc := range []struct {
		name  string
		edit  func(parent *v1alpha1.Task, k []v1alpha1.Task)
		code  string
		child string
	}{
		{name: "two failed children: the first in mergeOrder", edit: func(_ *v1alpha1.Task, k []v1alpha1.Task) {
			k[1].Status.Phase = v1alpha1.PhaseClosed
			k[0].Status.Phase, k[0].Status.Reason = v1alpha1.PhaseReverted, "revert_requested"
		}, code: RecoveryRevisePlan, child: "contract"},
		{name: "a child before integration", edit: func(parent *v1alpha1.Task, k []v1alpha1.Task) {
			attempt(parent.Status.Coordination, "integr01", PurposeBeforeDependentMerge, ResultFailed, "integration_failed", preMerge(headB)...)
			k[1].Status.Phase, k[1].Status.Reason = v1alpha1.PhaseQueued, ErrAccessUnverified.Error()
		}, code: RecoveryAuthorizeChild, child: "consumer"},
		{name: "integration before budget", edit: func(parent *v1alpha1.Task, _ []v1alpha1.Task) {
			attempt(parent.Status.Coordination, "integr01", PurposeBeforeDependentMerge, ResultFailed, "integration_failed", preMerge(headB)...)
			parent.Status.Coordination.Children[0].Reason = ErrIntegrationRunsExhausted.Error()
		}, code: RecoveryRetryDependent, child: "consumer"},
		{name: "budget before nothing", edit: func(_ *v1alpha1.Task, k []v1alpha1.Task) {
			k[1].Status.Phase, k[1].Status.Reason = v1alpha1.PhaseEscalated, "allocation_exhausted"
		}, code: RecoveryIncreaseBudget, child: "consumer"},
		{name: "a healthy family", edit: func(*v1alpha1.Task, []v1alpha1.Task) {}},
		{name: "a child already being replaced", edit: func(parent *v1alpha1.Task, k []v1alpha1.Task) {
			k[1].Status.Phase = v1alpha1.PhaseClosed
			parent.Status.Coordination.PendingGenerations = []v1alpha1.PendingGeneration{{ChildID: "consumer", Generation: 2, PlanVersion: 1, StoppingTask: k[1].Name}}
		}},
		{name: "a superseded Task", edit: func(_ *v1alpha1.Task, k []v1alpha1.Task) {
			k[1].Status.Phase, k[1].Status.Reason = v1alpha1.PhaseStopped, "superseded"
		}},
		{name: "a red attempt on a head the dependent left", edit: func(parent *v1alpha1.Task, _ []v1alpha1.Task) {
			attempt(parent.Status.Coordination, "integr01", PurposeBeforeDependentMerge, ResultFailed, "integration_failed", preMerge(headB2)...)
		}},
		{name: "an attempt of an older plan", edit: func(parent *v1alpha1.Task, _ []v1alpha1.Task) {
			attempt(parent.Status.Coordination, "integr01", PurposeFinal, ResultFailed, "integration_failed")
			parent.Status.Coordination.IntegrationAttempts[0].PlanHash = strings.Repeat("9", 64)
		}},
		{name: "a Done parent", edit: func(parent *v1alpha1.Task, k []v1alpha1.Task) {
			parent.Status.Phase = v1alpha1.PhaseDone
			k[0].Status.Phase = v1alpha1.PhaseReverted
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, kids := recoveryFamily(p)
			tc.edit(parent, kids)
			got := ProposeRecovery(parent, kids, nil, recoveryAt)
			switch {
			case tc.code == "" && got != nil:
				t.Fatalf("proposal %+v, want none", got)
			case tc.code != "" && (got == nil || got.Code != tc.code || got.ChildID != tc.child):
				t.Fatalf("proposal %+v, want %s for %s", got, tc.code, tc.child)
			}
		})
	}
}

// Review Focus 3: a producer whose artifact did not verify at its landed merge proposes exactly
// retry_producer for the unchanged plan, and revise_plan once the producer's fingerprint changed;
// the dependent is never named, and the merged producer is never reverted.
func TestArtifactFailureHasOneRecovery(t *testing.T) {
	p := draftPlan(t)
	parent, kids := recoveryFamily(p)
	cs := parent.Status.Coordination
	cs.Children[1].Task, cs.Children[1].Reason = "", ErrArtifactUnverified.Error() // no dependent Task, no landed reference
	kids = kids[:1]
	got := ProposeRecovery(parent, kids, nil, recoveryAt)
	if got == nil || got.Code != RecoveryRetryProducer || got.ChildID != "contract" || got.Reason != "artifact_unverified" || got.RequiresConfirmation {
		t.Fatalf("proposal %+v", got)
	}
	if again := ProposeRecovery(parent, kids, nil, recoveryAt.Add(time.Hour)); !SameRecovery(got, again) {
		t.Fatalf("a second pass proposed %+v", again)
	}
	cs.Children[0].Fingerprint = strings.Repeat("0", 64) // the producer's plan changed
	if got = ProposeRecovery(parent, kids, nil, recoveryAt); got == nil || got.Code != RecoveryRevisePlan || got.ChildID != "contract" {
		t.Fatalf("under a changed plan: %+v", got)
	}
}

// proposing sets parent p's proposal to code for child (or integration Task integ).
func proposing(t *testing.T, r *filingRig, p v1alpha1.CoordinationPlan, code, child, integ string) {
	t.Helper()
	if err := r.s.setStatus(context.Background(), p.ID, func(task *v1alpha1.Task) {
		task.Status.Coordination.Recovery = &v1alpha1.RecoveryProposal{Code: code, ChildID: child, IntegrationTask: integ, Reason: "x",
			PlanVersion: task.Status.Coordination.Plan.Version, CreatedAt: metav1.NewTime(recoveryAt)}
	}); err != nil {
		t.Fatal(err)
	}
}

// Recover takes exactly the proposed action, bound to the current plan version and proposal, by
// the authority of the ordinary path it selects: a child's retry by that child's maintainer with
// write, as its next generation through the barrier (no Task, run or revert here); a plan action
// never, since it is a new version.
func TestRecoverTakesOnlyTheProposedAction(t *testing.T) {
	ctx := context.Background()
	r, p := filedRig(t)
	proposing(t, r, p, RecoveryRetryChild, "consumer", "")
	for _, tc := range []struct {
		name    string
		actor   Actor
		code    string
		version int32
		want    error
	}{
		{name: "another plan version", actor: other, code: RecoveryRetryChild, version: 2, want: ErrStalePlan},
		{name: "another action", actor: other, code: RecoveryResumeChild, version: 1, want: ErrRecoveryNotProposed},
		{name: "the primary's maintainer for a child action", actor: smana, code: RecoveryRetryChild, version: 1, want: ErrRepositoryPermission},
		{name: "an unlinked caller", actor: Actor{Principal: "zitadel:x"}, code: RecoveryRetryChild, version: 1, want: ErrRepositoryPermission},
	} {
		if err := r.s.Recover(ctx, tc.actor, p.ID, tc.code, tc.version); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	r.s.Checks.(*fakeChecks).perms[repoB]["other-maintainer"] = "read"
	if err := r.s.Recover(ctx, other, p.ID, RecoveryRetryChild, 1); !errors.Is(err, ErrRepositoryPermission) {
		t.Fatalf("without write now: %v", err)
	}
	r.s.Checks.(*fakeChecks).perms[repoB]["other-maintainer"] = "write"
	if err := r.s.Recover(ctx, other, p.ID, RecoveryRetryChild, 1); err != nil {
		t.Fatal(err)
	}
	cs := parentOf(t, r.s, p.ID).Status.Coordination
	pg := Pending(cs, "consumer")
	if cs.Recovery != nil || pg == nil || pg.Generation != 2 || pg.StoppingTask != cs.Children[1].Task || Pending(cs, "contract") != nil {
		t.Fatalf("recovery %+v pending %+v", cs.Recovery, cs.PendingGenerations)
	}
	var l v1alpha1.TaskList
	if err := r.s.Client.List(ctx, &l); err != nil || len(l.Items) != 1 {
		t.Fatalf("Recover created %d Tasks", len(l.Items)-1)
	}
	if n := len(r.forges[repoA].Reverts()) + len(r.forges[repoB].Reverts()) + len(r.forges[repoA].Armed()) + len(r.forges[repoB].Armed()); n != 0 {
		t.Fatalf("%d reverts or merges", n)
	}
	if err := r.s.Recover(ctx, other, p.ID, RecoveryRetryChild, 1); !errors.Is(err, ErrRecoveryNotProposed) {
		t.Fatalf("a second call bundled a second action: %v", err)
	}

	for _, code := range []string{RecoveryRevisePlan, RecoveryIncreaseBudget} {
		proposing(t, r, p, code, "", "")
		if err := r.s.Recover(ctx, other, p.ID, code, 1); !errors.Is(err, ErrRepositoryPermission) {
			t.Fatalf("%s by a non-primary maintainer: %v", code, err)
		}
		if err := r.s.Recover(ctx, smana, p.ID, code, 1); !errors.Is(err, ErrPlanRevisionRequired) {
			t.Fatalf("%s: %v", code, err)
		}
	}
}

// authorize_child is a fresh authorization at the repository's current fence by that child's
// maintainer: a moved fence re-read, never the cached one.
func TestRecoverReauthorizesAtTheCurrentFence(t *testing.T) {
	ctx := context.Background()
	r, p := filedRig(t)
	r.s.Checks.(*fakeChecks).fence[repoB] = strings.Repeat("d", 40)
	cs := parentOf(t, r.s, p.ID).Status.Coordination
	if err := r.s.ExecuteReady(ctx, r.s.Cfg.Current(), cs, []string{"consumer"}); !errors.Is(err, ErrPolicyChanged) {
		t.Fatalf("before: %v", err)
	}
	proposing(t, r, p, RecoveryAuthorizeChild, "consumer", "")
	if err := r.s.Recover(ctx, other, p.ID, RecoveryAuthorizeChild, 1); err != nil {
		t.Fatal(err)
	}
	cs = parentOf(t, r.s, p.ID).Status.Coordination
	if a := currentAuthorization(cs, "consumer"); a == nil || a.FenceSHA != strings.Repeat("d", 40) || a.Actor != other.Principal {
		t.Fatalf("authorization %+v", a)
	}
	if err := r.s.ExecuteReady(ctx, r.s.Cfg.Current(), cs, []string{"consumer"}); err != nil {
		t.Fatalf("after: %v", err)
	}
}

// retry_integration is one new attempt of the lost attempt's exact manifest within maxRuns,
// behind the execute gate, by the primary's maintainer; past maxRuns it is refused.
func TestRecoverRetriesIntegrationOnce(t *testing.T) {
	ctx := context.Background()
	r, p := filedRig(t)
	merged := metav1.NewTime(recoveryAt)
	var lost string
	if err := r.s.setStatus(ctx, p.ID, func(task *v1alpha1.Task) {
		cs := task.Status.Coordination
		cs.Artifacts = []v1alpha1.LandedArtifact{{ChildID: "contract", Name: "consumer-v1-schema", Repository: repoA, Path: "schemas/consumer-v1.json",
			MergeCommitSHA: mergeA, BlobSHA: strings.Repeat("1", 40), ContentSHA256: strings.Repeat("2", 64), SizeBytes: 17}}
		m, err := BeforeDependentManifest(task.Name, cs, "consumer", headB, map[string]string{"contract": mergeA})
		if err != nil {
			t.Fatal(err)
		}
		it := IntegrationTask(ns, r.s.Cfg.Current(), task, m, 1)
		it.Status.Phase = v1alpha1.PhaseEscalated
		if err := r.s.Client.Create(ctx, it); err != nil {
			t.Fatal(err)
		}
		lost = it.Name
		cs.IntegrationAttempts = []v1alpha1.IntegrationAttempt{{Task: it.Name, Purpose: m.Purpose, PlanHash: m.PlanHash, ManifestHash: ManifestHash(m),
			State: ResultPending, Reason: reasonIntegrationInf, Subjects: m.Subjects}}
		cs.Recovery = &v1alpha1.RecoveryProposal{Code: RecoveryRetryIntegration, IntegrationTask: it.Name, Reason: reasonIntegrationInf,
			PlanVersion: cs.Plan.Version, CreatedAt: merged}
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Recover(ctx, other, p.ID, RecoveryRetryIntegration, 1); !errors.Is(err, ErrRepositoryPermission) {
		t.Fatalf("by the child's maintainer: %v", err)
	}
	if err := r.s.Recover(ctx, smana, p.ID, RecoveryRetryIntegration, 1); err != nil {
		t.Fatal(err)
	}
	cs := parentOf(t, r.s, p.ID).Status.Coordination
	if len(cs.IntegrationAttempts) != 2 || cs.IntegrationAttempts[0].State != ResultFailed || cs.IntegrationAttempts[1].State != ResultPending ||
		cs.IntegrationAttempts[1].ManifestHash != cs.IntegrationAttempts[0].ManifestHash || cs.IntegrationAttempts[1].Task == lost || cs.Recovery != nil {
		t.Fatalf("attempts %+v recovery %+v", cs.IntegrationAttempts, cs.Recovery)
	}
	var it v1alpha1.Task
	if err := r.s.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: cs.IntegrationAttempts[1].Task}, &it); err != nil || it.Spec.Kind != v1alpha1.TaskKindIntegration {
		t.Fatalf("no new attempt Task: %v", err)
	}
	if err := r.s.setStatus(ctx, p.ID, func(task *v1alpha1.Task) {
		c := task.Status.Coordination
		c.Plan.Integration.MaxRuns = 2
		c.Recovery = &v1alpha1.RecoveryProposal{Code: RecoveryRetryIntegration, IntegrationTask: it.Name, PlanVersion: c.Plan.Version, Reason: "x", CreatedAt: merged}
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Recover(ctx, smana, p.ID, RecoveryRetryIntegration, 1); !errors.Is(err, ErrIntegrationRunsExhausted) {
		t.Fatalf("a retry past maxRuns: %v", err)
	}
}
