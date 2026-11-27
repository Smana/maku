// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/upkeep"
)

func TestFirstBriefWithRoutineHistoryPackingAndPriority(t *testing.T) {
	tk := routineTask("hist0001", "dead-code", "system:scheduler")
	tk.Spec.Routine.PromptSource = "repo"
	tk.Spec.Routine.PromptPath = ".github/agent-factory/upkeep/dead-code.md"
	tk.Spec.Routine.Skill = "docs-audit"
	tk.Spec.Routine.SkillSHA = strings.Repeat("c", 40)
	tk.Spec.Steering = &v1alpha1.Steering{
		Path: ".github/agent-factory/steering/review.md",
		SHA:  strings.Repeat("d", 40),
		Text: "## Verify\n- ok\n" + strings.Repeat("s", 1400),
	}
	sc := &Scoped{Scope: maxScope(t), Seq: 9}

	rp := RoutinePrompt{
		PromptPath: tk.Spec.Routine.PromptPath,
		PromptSHA:  strings.Repeat("b", 40),
		PromptText: "## Verify\n- go test ./...\n" + strings.Repeat("p", upkeep.MaxPromptBytes-30),
		Skill:      "docs-audit",
		SkillSHA:   tk.Spec.Routine.SkillSHA,
		SkillText:  strings.Repeat("k", upkeep.MaxSkillBytes),
	}
	var history []upkeep.HistoryItem
	for i := range 10 {
		history = append(history, upkeep.HistoryItem{
			PRNumber: 100 + i,
			Date:     now.Add(-time.Duration(i+1) * time.Hour),
			Outcome:  upkeep.HistoryOutcomeClosedUnmerged,
			Title:    "reject candidate " + strings.Repeat("t", 80),
			Paths:    []string{"internal/a.go", "internal/b.go"},
			Comment:  "Do not touch this module: " + strings.Repeat("c", 180),
		})
	}

	b := FirstBriefWithRoutineHistory(tk, "n0nce234", sc, rp, history)
	if len(b) > firstCap {
		t.Fatalf("FirstBriefWithRoutineHistory len=%d > firstCap %d", len(b), firstCap)
	}
	if total := len(ResumeNotice(tk)) + len(b); total > 16<<10 {
		t.Fatalf("resumed brief len=%d > 16384", total)
	}
	if !strings.Contains(b, "ROUTINE-PROMPT-n0nce234") {
		t.Fatalf("ROUTINE-PROMPT lost during packing:\n%s", b)
	}
	if !strings.Contains(b, "UPKEEP-HISTORY-n0nce234") {
		t.Fatalf("UPKEEP-HISTORY lost during packing:\n%s", b)
	}
	pIdx := strings.Index(b, "ROUTINE-PROMPT-n0nce234")
	sIdx := strings.Index(b, "ROUTINE-SKILL-n0nce234")
	hIdx := strings.Index(b, "UPKEEP-HISTORY-n0nce234")
	if pIdx < 0 || sIdx < 0 || hIdx < 0 || pIdx > sIdx || sIdx > hIdx {
		t.Fatalf("expected ROUTINE-PROMPT -> ROUTINE-SKILL -> UPKEEP-HISTORY (pIdx=%d sIdx=%d hIdx=%d)", pIdx, sIdx, hIdx)
	}
	if !strings.Contains(b, "⟦skill clipped") {
		t.Fatalf("expected ROUTINE-SKILL to yield budget to UPKEEP-HISTORY:\n%s", b)
	}
}

func TestRoutineTriagerBriefWithHistoryRespectsFirstCap(t *testing.T) {
	tk := proposeRoutineTask("hist0002", "doc-drift", "public")
	tk.Spec.Routine.PromptSource = "repo"
	tk.Spec.Routine.PromptPath = ".github/agent-factory/upkeep/doc-drift.md"
	tk.Spec.Routine.Skill = "docs-audit"
	tk.Spec.Routine.SkillSHA = strings.Repeat("c", 40)
	tk.Spec.Text = strings.Repeat("x", 8000)

	rp := RoutinePrompt{
		PromptPath: tk.Spec.Routine.PromptPath,
		PromptSHA:  strings.Repeat("b", 40),
		PromptText: "## Verify\n- make test\n" + strings.Repeat("p", upkeep.MaxPromptBytes-30),
		Skill:      "docs-audit",
		SkillSHA:   tk.Spec.Routine.SkillSHA,
		SkillText:  strings.Repeat("k", upkeep.MaxSkillBytes),
	}
	history := []upkeep.HistoryItem{
		{
			IssueNumber: 44,
			Date:        now.Add(-2 * time.Hour),
			Outcome:     upkeep.HistoryOutcomeClosedUnmerged,
			Title:       "old doc proposal",
			Paths:       []string{"docs/cli.md"},
			Comment:     "Keep existing CLI wording.",
		},
	}

	b := TriagerBriefWithRoutineHistory(tk, "n0nce234", rp, history)
	if len(b) > firstCap {
		t.Fatalf("TriagerBriefWithRoutineHistory len=%d > firstCap %d", len(b), firstCap)
	}
	if !strings.Contains(b, "ROUTINE-PROMPT-n0nce234") || !strings.Contains(b, "UPKEEP-HISTORY-n0nce234") {
		t.Fatalf("triager brief missing prompt or history:\n%s", b)
	}
}

func TestReconcilerHydratesRoutineHistoryImplementerAndPropose(t *testing.T) {
	t.Run("implementer hydrates task history + maintainer comment and cold-start PR fallback", func(t *testing.T) {
		ended := metav1.NewTime(now.Add(-24 * time.Hour))
		past := routineTask("past234a", "dead-code", "system:scheduler")
		past.Status.Phase = v1alpha1.PhaseClosed
		past.Status.Reason = "pr_closed"
		past.Status.EndedAt = &ended
		past.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 55}

		cur := routineTask("cur2345a", "dead-code", "system:scheduler")
		cur.Spec.Routine.PromptSource = "repo"
		cur.Spec.Routine.PromptPath = ".github/agent-factory/upkeep/dead-code.md"

		g := newRig(t, cur, past)
		g.f.SetFile(".github/agent-factory/upkeep/dead-code.md", "## Verify\n- go test ./...\n")
		g.f.SetPR(forge.PR{
			Number:   55,
			State:    "CLOSED",
			ClosedAt: ended.Time,
			Title:    "drop unused helper Foo",
			Labels:   []string{"factory/routine:dead-code"},
			Comments: []forge.Comment{
				{ID: 1, Author: "Smana", Body: "Foo is used via reflection.", At: ended.Add(-time.Minute)},
			},
		})
		g.f.SetPullFiles(55, forge.FilePatch{Path: "pkg/foo.go"})
		// Cold-start PR #56 (no Task CR).
		g.f.SetPR(forge.PR{
			Number:   56,
			State:    "CLOSED",
			ClosedAt: ended.Add(-2 * time.Hour),
			Title:    "cold-start stale PR",
			Labels:   []string{"factory/routine:dead-code", "factory/stale"},
		})
		g.f.SetPullFiles(56, forge.FilePatch{Path: "pkg/bar.go"})

		got := g.reconcile(t, cur.Name, 3)
		if got.Status.Phase != v1alpha1.PhaseImplementing {
			t.Fatalf("got %s/%s, want Implementing", got.Status.Phase, got.Status.Reason)
		}
		brief := g.runs.specs[runID(cur.Name, 0)].TaskText
		if !strings.Contains(brief, "UPKEEP-HISTORY-n0nce234") ||
			!strings.Contains(brief, "drop unused helper Foo") ||
			!strings.Contains(brief, "Foo is used via reflection.") ||
			!strings.Contains(brief, "pkg/foo.go") ||
			!strings.Contains(brief, "cold-start stale PR") {
			t.Fatalf("implementer brief missing expected history:\n%s", brief)
		}
	})

	t.Run("propose triager hydrates history and degrades gracefully with ErrNoMerger or list errors", func(t *testing.T) {
		ended := metav1.NewTime(now.Add(-12 * time.Hour))
		past := proposeRoutineTask("past234b", "doc-drift", "public")
		past.Spec.Issue = 88
		past.Status.Phase = v1alpha1.PhaseDone
		past.Status.Reason = "proposal_filed"
		past.Status.EndedAt = &ended

		cur := proposeRoutineTask("cur2345b", "doc-drift", "public")
		g := proposeRig(t, cur, past)
		g.r.Merger = forge.Absent{} // merger off -> ErrNoMerger must degrade cleanly
		g.f.SetIssue(forge.Issue{
			Number:       88,
			State:        "CLOSED",
			Title:        "doc-drift: old closed proposal",
			Body:         "## Files\n- `docs/old.md`\n",
			LastEditedAt: ended.Time,
		})
		g.f.SetComments(88, forge.Comment{ID: 2, Author: "Smana", Body: "Already tracked elsewhere.", At: ended.Add(-time.Minute)})

		got := g.reconcile(t, cur.Name, 3)
		if got.Status.Phase != v1alpha1.PhaseImplementing {
			t.Fatalf("got %s/%s, want Implementing", got.Status.Phase, got.Status.Reason)
		}
		brief := g.runs.specs[runID(cur.Name, 0)].TaskText
		if !strings.Contains(brief, "UPKEEP-HISTORY-n0nce234") ||
			!strings.Contains(brief, "old closed proposal") ||
			!strings.Contains(brief, "Already tracked elsewhere.") ||
			!strings.Contains(brief, "docs/old.md") {
			t.Fatalf("propose triager brief missing expected history:\n%s", brief)
		}

		// TaskList error in hydrateRoutineHistory must not block a run.
		cur2 := routineTask("cur2345c", "dead-code", "system:scheduler")
		g2 := newRig(t, cur2)
		g2.r.Merger = nil
		baseClient := g2.c.(client.WithWatch)
		var failedHydrate bool
		g2.r.Client = interceptor.NewClient(baseClient, interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*v1alpha1.TaskList); ok && strings.Contains(string(debug.Stack()), "hydrateRoutineHistory") {
					failedHydrate = true
					return errors.New("simulated list failure")
				}
				return cl.List(ctx, list, opts...)
			},
		})
		got2 := g2.reconcile(t, cur2.Name, 3)
		if got2.Status.Phase != v1alpha1.PhaseImplementing || !failedHydrate {
			t.Fatalf("TaskList failure blocked run: phase=%s/%s failedHydrate=%t", got2.Status.Phase, got2.Status.Reason, failedHydrate)
		}
	})
}
