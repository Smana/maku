// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/upkeep"
)

func upkeepPathInputs(t *testing.T, stats []forge.FileStat, repoPrompt bool, skill string) ArmInputs {
	t.Helper()
	c := cfg()
	c.Repositories[0].Classes = map[string]config.Class{
		"docs":  {Live: true, AllowedFiles: []string{`docs/.+\.md`, `\.github/.+`, `\.agents/.+`}},
		"other": {Live: true, AllowedFiles: []string{`README\.md`}},
	}
	c.Repositories[0].ResolvedRoutines = map[string]config.ResolvedRoutine{
		"dead-code": {
			Name:       "dead-code",
			Mode:       config.RoutineModePR,
			RepoPrompt: repoPrompt,
			Skill:      skill,
		},
	}
	tk := awaiting()
	tk.Spec.PredictedClass = "docs"
	tk.Spec.Template = "pair"
	tk.Status.Verdict = "approve"
	tk.Status.PullRequest.HeadSHA = "abc"
	ended := metav1.NewTime(now.Add(-30 * time.Minute))
	tk.Status.Runs[0].Finished = &ended
	return ArmInputs{
		Cfg:          c,
		Task:         tk,
		PR:           forge.PR{Number: 12, State: "OPEN", Author: c.AgentsLogin, BaseRef: "main", HeadRef: "agent/3buqdlot", HeadSHA: "abc", HeadMessage: "fix\n\nAgent-Run: " + rid(0)},
		Checks:       green("SUCCESS"),
		Stats:        stats,
		DiffRead:     true,
		ReportedHead: "abc",
		Verifiers:    []v1alpha1.RunRecord{{Role: "reviewer", Verdict: "approve", HeadSHA: "abc"}},
		HeadPushedAt: now.Add(-35 * time.Minute),
	}
}

func TestUpkeepPathGateGuard(t *testing.T) {
	for name, tc := range map[string]struct {
		stats      []forge.FileStat
		repoPrompt bool
		skill      string
		multiClass bool
		approve    bool
		policyBot  bool
		wantHuman  bool
		wantReason string
	}{
		"edit upkeep prompt refused": {
			stats:      []forge.FileStat{{Path: ".github/agent-factory/upkeep/dead-code.md"}},
			repoPrompt: true,
			wantHuman:  true,
			wantReason: "upkeep_path",
		},
		"rename from upkeep prompt refused": {
			stats:      []forge.FileStat{{Path: "docs/moved.md", Previous: ".github/agent-factory/upkeep/dead-code.md"}},
			repoPrompt: true,
			wantHuman:  true,
			wantReason: "upkeep_path",
		},
		"edit bound skill refused": {
			stats:      []forge.FileStat{{Path: ".agents/skills/docs-audit/SKILL.md"}},
			repoPrompt: false,
			skill:      "docs-audit",
			wantHuman:  true,
			wantReason: "upkeep_path",
		},
		"rename from bound skill refused": {
			stats:      []forge.FileStat{{Path: "docs/a.md", Previous: ".agents/skills/docs-audit/scripts/run.sh"}},
			repoPrompt: false,
			skill:      "docs-audit",
			wantHuman:  true,
			wantReason: "upkeep_path",
		},
		"edit unbound skill allowed": {
			stats:      []forge.FileStat{{Path: ".agents/skills/other/SKILL.md"}},
			repoPrompt: false,
			skill:      "docs-audit",
			wantHuman:  false,
		},
		"upkeep dir ignored when all routines have repoPrompt false and no skill": {
			stats:      []forge.FileStat{{Path: ".github/agent-factory/upkeep/dead-code.md"}},
			repoPrompt: false,
			wantHuman:  false,
		},
		"multi-class select path refuses upkeep_path": {
			stats:      []forge.FileStat{{Path: ".github/agent-factory/upkeep/dead-code.md"}},
			repoPrompt: true,
			multiClass: true,
			wantHuman:  true,
			wantReason: "upkeep_path",
		},
		"approveToMerge without policy-bot cannot bypass upkeep_path": {
			stats:      []forge.FileStat{{Path: ".github/agent-factory/upkeep/dead-code.md"}},
			repoPrompt: true,
			approve:    true,
			policyBot:  false,
			wantHuman:  true,
			wantReason: "upkeep_path",
		},
		"approveToMerge WITH policy-bot cannot bypass upkeep_path": {
			stats:      []forge.FileStat{{Path: ".agents/skills/docs-audit/SKILL.md"}},
			repoPrompt: false,
			skill:      "docs-audit",
			approve:    true,
			policyBot:  true,
			wantHuman:  true,
			wantReason: "upkeep_path",
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := upkeepPathInputs(t, tc.stats, tc.repoPrompt, tc.skill)
			if tc.multiClass {
				in.Task.Spec.PredictedClass = ""
			}
			rc := &in.Cfg.Repositories[0]
			rc.Merge.ApproveToMerge = tc.approve
			rc.Merge.PolicyBot = new(tc.policyBot)
			if tc.approve {
				in.PR.Reviews = []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: "abc", At: now}}
			}
			if tc.policyBot {
				in.Checks.Statuses = append(in.Checks.Statuses, forge.Status{Context: "policy-bot: main", State: "SUCCESS", Creator: rc.Merge.PolicyBotLogin})
			}
			d := DecideArm(in)
			if tc.wantHuman {
				if d.Verdict != "human" || d.Reason != tc.wantReason {
					t.Fatalf("got %+v, want human/%s", d, tc.wantReason)
				}
			} else if d.Verdict != "arm" {
				t.Fatalf("got %+v, want arm", d)
			}
		})
	}
}

func TestUpkeepPathReconcilerPass2DiffAndByHand(t *testing.T) {
	for _, policyBot := range []bool{false, true} {
		name := "policyBot=false"
		if policyBot {
			name = "policyBot=true"
		}
		t.Run(name, func(t *testing.T) {
			g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
			rc := &g.r.Cfg.Current().Repositories[0]
			rc.Merge.ApproveToMerge = true
			rc.Merge.PolicyBot = new(policyBot)
			rc.ResolvedRoutines = map[string]config.ResolvedRoutine{
				"dead-code": {Name: "dead-code", Mode: config.RoutineModePR, RepoPrompt: true, Skill: "docs-audit"},
			}
			// Steering is OFF on config, yet pass-2 Diff MUST still run because upkeepPathProtected(rc) is true.
			g.r.Cfg.Current().Templates["solo"] = config.Template{Roles: []string{"implementer"}}
			pr, _ := g.f.PullRequest(t.Context(), 12)
			pr.Reviews = []forge.Review{{ID: 77, Author: "Smana", State: "APPROVED", Commit: "abc", At: now}}
			g.f.SetPR(pr)
			checks := green("SUCCESS")
			if policyBot {
				checks.Statuses = append(checks.Statuses, forge.Status{Context: "policy-bot: main", State: "SUCCESS", Creator: rc.Merge.PolicyBotLogin})
			}
			g.f.SetChecks(12, checks)
			g.f.SetFiles(
				map[string]string{".github/agent-factory/upkeep/dead-code.md": "## Verify\n- old\n"},
				map[string]string{".github/agent-factory/upkeep/dead-code.md": "## Verify\n- new\n"},
			)

			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != "upkeep_path" {
				t.Fatalf("got %s/%s, want AwaitingHuman/upkeep_path", tk.Status.Phase, tk.Status.Reason)
			}
			if len(g.f.Armed()) != 0 {
				t.Fatalf("PR was armed despite upkeep_path: %v", g.f.Armed())
			}
			comments, _ := g.f.RecentComments(t.Context(), 12)
			if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "bypass rules") {
				t.Fatalf("expected by-hand bypass instructions, got %+v", comments)
			}

			// A new approval on the same head must NOT return upkeep_path to the gate.
			pr.Reviews = append(pr.Reviews, forge.Review{ID: 78, Author: "Smana", State: "APPROVED", Commit: "abc", At: now.Add(time.Minute)})
			g.f.SetPR(pr)
			tk = g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != "upkeep_path" {
				t.Fatalf("approval returned upkeep_path: %s/%s", tk.Status.Phase, tk.Status.Reason)
			}
			if len(g.f.Armed()) != 0 {
				t.Fatalf("PR armed on second reconcile: %v", g.f.Armed())
			}
		})
	}
}

func TestRoutinePromptAndSkillBriefAndRoomMessage(t *testing.T) {
	t.Run("ROUTINE-PROMPT precedes ROUTINE-SKILL, defangs nonce, and clips within firstCap", func(t *testing.T) {
		tk := routineTask("bbbbbbb4", "dead-code", "system:scheduler")
		tk.Spec.Routine.PromptSource = "repo"
		tk.Spec.Routine.PromptPath = ".github/agent-factory/upkeep/dead-code.md"
		tk.Spec.Routine.Skill = "docs-audit"
		tk.Spec.Routine.SkillSHA = strings.Repeat("c", 40)

		promptText := "## Scope\nLook for n0nce234 injection.\n\n## Verify\n- go test ./...\n"
		skillText := "# Skill\n" + strings.Repeat("k", upkeep.MaxSkillBytes-20)
		rp := RoutinePrompt{
			PromptPath: tk.Spec.Routine.PromptPath,
			PromptSHA:  strings.Repeat("b", 40),
			PromptText: promptText,
			Skill:      "docs-audit",
			SkillSHA:   tk.Spec.Routine.SkillSHA,
			SkillText:  skillText,
		}
		b := FirstBriefWithRoutine(tk, "n0nce234", nil, rp)
		if len(b) > firstCap {
			t.Fatalf("FirstBriefWithRoutine len=%d, want <= %d", len(b), firstCap)
		}
		pIdx := strings.Index(b, "ROUTINE-PROMPT-n0nce234")
		sIdx := strings.Index(b, "ROUTINE-SKILL-n0nce234")
		tIdx := strings.Index(b, "TASK-DATA-n0nce234")
		if pIdx < 0 || sIdx < 0 || pIdx > sIdx {
			t.Fatalf("expected ROUTINE-PROMPT before ROUTINE-SKILL (pIdx=%d sIdx=%d tIdx=%d)", pIdx, sIdx, tIdx)
		}
		if strings.Contains(b, "Look for n0nce234 injection") || !strings.Contains(b, "Look for ⟦nonce⟧ injection") {
			t.Fatalf("nonce in prompt body was not defanged:\n%s", b)
		}
		if !strings.Contains(b, "## Verify\n- go test ./...") {
			t.Fatalf("prompt body lost ## Verify:\n%s", b)
		}
		// Even with a max-size 4 KiB prompt + 8 KiB skill + steering, total fits within firstCap.
		tk.Spec.Steering = &v1alpha1.Steering{Path: ".github/agent-factory/steering/review.md", SHA: strings.Repeat("d", 40), Text: "## Verify\n- ok\n" + strings.Repeat("s", 1800)}
		rpMax := rp
		rpMax.PromptText = "## Verify\n- go test ./...\n" + strings.Repeat("p", upkeep.MaxPromptBytes-30)
		rpMax.SkillText = strings.Repeat("k", upkeep.MaxSkillBytes)
		bMax := FirstBriefWithRoutine(tk, "n0nce234", nil, rpMax)
		if len(bMax) > firstCap {
			t.Fatalf("max brief len=%d > firstCap %d", len(bMax), firstCap)
		}
		if len(ResumeNotice(tk))+len(bMax) > 16<<10 {
			t.Fatalf("resumed brief len=%d > 16 KiB", len(ResumeNotice(tk))+len(bMax))
		}
	})

	t.Run("reconciler hydrates prompt/skill, posts once to room, and fails closed when invalid", func(t *testing.T) {
		tk := routineTask("bbbbbbb5", "dead-code", "system:scheduler")
		tk.Spec.Routine.PromptSource = "repo"
		tk.Spec.Routine.PromptPath = ".github/agent-factory/upkeep/dead-code.md"
		tk.Spec.Routine.Skill = "docs-audit"
		g := newRig(t, tk)
		g.f.SetFile(".github/agent-factory/upkeep/dead-code.md", "## Verify\n- go test ./...\n")
		g.f.SetFile(".agents/skills/docs-audit/SKILL.md", "# Docs audit skill\nCheck headings.\n")

		got := g.reconcile(t, tk.Name, 3)
		if got.Status.Phase != v1alpha1.PhaseImplementing {
			t.Fatalf("got %s/%s, want Implementing", got.Status.Phase, got.Status.Reason)
		}
		spec := g.runs.specs[runID(tk.Name, 0)]
		if !strings.Contains(spec.TaskText, "ROUTINE-PROMPT-n0nce234") || !strings.Contains(spec.TaskText, "ROUTINE-SKILL-n0nce234") {
			t.Fatalf("implementer brief missing prompt/skill blocks:\n%s", spec.TaskText)
		}
		if len(g.log.states) != 2 || !strings.Contains(g.log.states[3], "## Verify") || !strings.Contains(g.log.states[3], "Docs audit skill") {
			t.Fatalf("expected snapshot + routine-prompt room states, got %+v", g.log.states)
		}

		// Invalid repo prompt at reconciler time fails closed without starting any run.
		tkBad := routineTask("bbbbbbb6", "dead-code", "system:scheduler")
		tkBad.Spec.Routine.PromptSource = "repo"
		tkBad.Spec.Routine.PromptPath = ".github/agent-factory/upkeep/dead-code.md"
		gBad := newRig(t, tkBad)
		gBad.f.SetFile(".github/agent-factory/upkeep/dead-code.md", "## Scope\nmissing verify\n")
		_ = gBad.reconcile(t, tkBad.Name, 2) // Received -> Triaged -> Queued
		_, err := gBad.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "agent-system", Name: tkBad.Name}})
		if err != nil {
			t.Fatal(err)
		}
		var out v1alpha1.Task
		_ = gBad.c.Get(t.Context(), client.ObjectKey{Namespace: "agent-system", Name: tkBad.Name}, &out)
		if out.Status.Phase != v1alpha1.PhaseEscalated || out.Status.Reason != "prompt_invalid" || len(gBad.runs.specs) != 0 {
			t.Fatalf("got %s/%s runs=%d, want Escalated/prompt_invalid/0", out.Status.Phase, out.Status.Reason, len(gBad.runs.specs))
		}
	})
}

func TestFirstBriefWithRoutineRespectsFirstCapWithLargeBlocks(t *testing.T) {
	makeContract := func() *v1alpha1.ContractStatus {
		files := make([]string, 40)
		ids := make([]string, 40)
		for i := range 40 {
			files[i] = "internal/factory/reconciler/some_long_contract_file_name_test.go"
			ids[i] = "TestContractBehaviorWithSufficientlyDescriptiveName"
		}
		return &v1alpha1.ContractStatus{
			Commit: strings.Repeat("f", 40),
			Files:  files,
			IDs:    ids,
		}
	}

	sc := &Scoped{Scope: maxScope(t), Seq: 9}
	rpWithSkill := RoutinePrompt{
		PromptPath: ".github/agent-factory/upkeep/dead-code.md",
		PromptSHA:  strings.Repeat("b", 40),
		PromptText: "## Verify\n- go test ./...\n" + strings.Repeat("p", upkeep.MaxPromptBytes-30),
		Skill:      "docs-audit",
		SkillSHA:   strings.Repeat("c", 40),
		SkillText:  strings.Repeat("k", upkeep.MaxSkillBytes),
	}
	rpPromptOnly := RoutinePrompt{
		PromptPath: ".github/agent-factory/upkeep/dead-code.md",
		PromptSHA:  strings.Repeat("b", 40),
		PromptText: "## Verify\n- go test ./...\n" + strings.Repeat("p", upkeep.MaxPromptBytes-30),
	}

	for name, tc := range map[string]struct {
		rp       RoutinePrompt
		contract *v1alpha1.ContractStatus
	}{
		"prompt+skill with steering, large scope and large contract": {
			rp:       rpWithSkill,
			contract: makeContract(),
		},
		"prompt-only with steering, large scope and large contract": {
			rp:       rpPromptOnly,
			contract: makeContract(),
		},
	} {
		t.Run(name, func(t *testing.T) {
			tk := routineTask("bbbbbbb7", "dead-code", "system:scheduler")
			tk.Spec.Routine.PromptSource = "repo"
			tk.Spec.Routine.PromptPath = ".github/agent-factory/upkeep/dead-code.md"
			if tc.rp.Skill != "" {
				tk.Spec.Routine.Skill = tc.rp.Skill
				tk.Spec.Routine.SkillSHA = tc.rp.SkillSHA
			}
			tk.Spec.Steering = &v1alpha1.Steering{
				Path: ".github/agent-factory/steering/review.md",
				SHA:  strings.Repeat("d", 40),
				Text: "## Verify\n- ok\n" + strings.Repeat("s", 2048-16),
			}
			tk.Status.Contract = tc.contract

			brief := FirstBriefWithRoutine(tk, "n0nce234", sc, tc.rp)
			if len(brief) > firstCap {
				t.Fatalf("FirstBriefWithRoutine len=%d > firstCap %d", len(brief), firstCap)
			}
			if total := len(ResumeNotice(tk)) + len(brief); total > 16<<10 {
				t.Fatalf("resumed brief len=%d > 16384", total)
			}
		})
	}

	t.Run("routinePromptBlock strictly obeys budget <= 0 and tight positive budgets", func(t *testing.T) {
		tk := routineTask("bbbbbbb8", "dead-code", "system:scheduler")
		tk.Spec.Routine.PromptSource = "repo"
		tk.Spec.Routine.Skill = "docs-audit"
		for _, budget := range []int{-500, 0, 1, 50, 150, 500, 1500, 5000} {
			got := routinePromptBlock(tk, rpWithSkill, "n0nce234", budget)
			wantMax := max(0, budget)
			if len(got) > wantMax {
				t.Fatalf("budget=%d: len(routinePromptBlock)=%d > %d", budget, len(got), wantMax)
			}
			gotOnly := routinePromptBlock(tk, rpPromptOnly, "n0nce234", budget)
			if len(gotOnly) > wantMax {
				t.Fatalf("prompt-only budget=%d: len(routinePromptBlock)=%d > %d", budget, len(gotOnly), wantMax)
			}
		}
	})
}

func TestNextImplementerRoutinePromptErrorEscalates(t *testing.T) {
	for name, tc := range map[string]struct {
		taskID     string
		setup      func(*rig)
		wantReason string
	}{
		"invalid prompt after scoper run": {
			taskID: "bbbbbb11",
			setup: func(g *rig) {
				g.f.SetFile(".github/agent-factory/upkeep/dead-code.md", "## Scope\nno verify\n")
			},
			wantReason: "prompt_invalid",
		},
		"missing skill after scoper run": {
			taskID: "bbbbbb12",
			setup: func(g *rig) {
				g.f.SetFile(".github/agent-factory/upkeep/dead-code.md", "## Verify\n- ok\n")
			},
			wantReason: "skill_missing",
		},
		"invalid skill after scoper run": {
			taskID: "bbbbbb13",
			setup: func(g *rig) {
				g.f.SetFile(".github/agent-factory/upkeep/dead-code.md", "## Verify\n- ok\n")
				g.f.SetFile(".agents/skills/docs-audit/SKILL.md", "   \n")
			},
			wantReason: "skill_invalid",
		},
	} {
		t.Run(name, func(t *testing.T) {
			tk := routineTask(tc.taskID, "dead-code", "system:scheduler")
			tk.Spec.Template = "pair"
			tk.Spec.Routine.PromptSource = "repo"
			tk.Spec.Routine.PromptPath = ".github/agent-factory/upkeep/dead-code.md"
			tk.Spec.Routine.Skill = "docs-audit"
			tk.Status.Phase = v1alpha1.PhaseQueued
			tk.Status.RoomRef = tk.Name
			// Simulate len(Runs) > 0 (e.g. a completed scoper run) before implementer starts.
			tk.Status.Runs = []v1alpha1.RunRecord{{ID: runID(tk.Name, 0), Role: "triager", Phase: "Succeeded"}}
			room := &roomv1.Room{
				ObjectMeta: metav1.ObjectMeta{Name: tk.Name, Namespace: "agent-system"},
				Spec:       roomv1.RoomSpec{Owner: "system:factory", Driver: "system:factory", DataClass: "public", Repository: tk.Spec.Repository},
			}
			g := newRig(t, tk, room)
			tc.setup(g)

			_, err := g.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "agent-system", Name: tk.Name}})
			if err != nil {
				t.Fatalf("unexpected error (expected clean escalation): %v", err)
			}
			var out v1alpha1.Task
			if err := g.c.Get(t.Context(), client.ObjectKey{Namespace: "agent-system", Name: tk.Name}, &out); err != nil {
				t.Fatal(err)
			}
			if out.Status.Phase != v1alpha1.PhaseEscalated || out.Status.Reason != tc.wantReason || len(g.runs.specs) != 0 {
				t.Fatalf("got %s/%s runs=%d, want Escalated/%s/0", out.Status.Phase, out.Status.Reason, len(g.runs.specs), tc.wantReason)
			}
		})
	}
}
