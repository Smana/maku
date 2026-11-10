// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
)

const (
	headBefore = "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headAfter  = "a431a2f6bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// Review-driven narration goes where the maintainer reviewed: the pull request once there is one,
// the issue otherwise (cloud-native-ref#2737).
func TestReviewTarget(t *testing.T) {
	for name, c := range map[string]struct {
		issue, pr, want int
	}{
		"the pull request when there is one": {issue: 7, pr: 12, want: 12},
		"the issue before a pull request":    {issue: 7, want: 7},
		"a task without an issue":            {pr: 12, want: 12},
		"nowhere":                            {},
	} {
		t.Run(name, func(t *testing.T) {
			tk := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Issue: c.issue}}
			if c.pr > 0 {
				tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: c.pr}
			}
			if got := reviewTarget(tk); got != c.want {
				t.Fatalf("reviewTarget = %d, want %d", got, c.want)
			}
		})
	}
}

// The task's own paths: its `## Files` lines and the paths its acceptance check names in code spans.
func TestTaskPaths(t *testing.T) {
	text := "# x\n\n## Acceptance check\n\n`opentofu/AGENTS.md` names the error text; `task check` passes.\n\n" +
		"## Files\n\n- `docs/a.md`\n- `opentofu/AGENTS.md`\n"
	got := taskPaths(text)
	if len(got) != 2 || got[0] != "docs/a.md" || got[1] != "opentofu/AGENTS.md" {
		t.Fatalf("%q", got)
	}
}

// After a maintainer's Request changes, the revision run's end is narrated once on the pull request:
// the new head, the files it changed, and, when it took a path the task names out of the pull request,
// that the scope changed, which the task's reason carries to the room's summary.
func TestRevisionEndIsNarratedOnThePR(t *testing.T) {
	const snapshot = "# opentofu: name the error\n\n## Acceptance check\n\n`opentofu/AGENTS.md` names the error text.\n\n" +
		"## Files\n\n- `opentofu/AGENTS.md`\n"
	for name, c := range map[string]struct {
		head       string
		changed    []string
		prFiles    []string
		want       []string
		unwanted   []string
		wantReason string
	}{
		"a kept path": {
			head: headAfter, changed: []string{"opentofu/AGENTS.md"}, prFiles: []string{"opentofu/AGENTS.md"},
			want:     []string{"[`a431a2f`](https://github.com/Smana/cloud-native-ref/commit/" + headAfter + ")", "`opentofu/AGENTS.md`", "re-review"},
			unwanted: []string{"Scope changed"},
		},
		"a dropped path": {
			head: headAfter, changed: []string{"opentofu/AGENTS.md", "website/content/docs/reference/commands.md"},
			prFiles:    []string{"website/content/docs/reference/commands.md"},
			want:       []string{"**Scope changed:**", "took `opentofu/AGENTS.md` out of the pull request", "confirm the scope change"},
			wantReason: "scope_changed",
		},
		"no new commit": {
			head:     headBefore,
			want:     []string{"the revision ended without a new commit; read the room for why"},
			unwanted: []string{"Scope changed", "re-review"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			tk := awaiting()
			tk.Spec.Text = snapshot
			g := newRig(t, tk, roomOf("3buqdlot"))
			pr := pr12(changes(901, "Smana", "AGENTS.md is a gate path", 5*time.Minute))
			pr.HeadSHA = headBefore
			g.f.SetPR(pr)
			got := g.reconcile(t, "3buqdlot", 2) // Request changes → Queued → the revision runs
			if got.Status.Phase != v1alpha1.PhaseImplementing || got.Status.Runs[1].HeadSHA != headBefore {
				t.Fatalf("%s %+v", got.Status.Phase, got.Status.Runs)
			}
			if cs := g.f.Comments(12); len(cs) != 1 || !strings.Contains(cs[0], "revising after @Smana's review") {
				t.Fatalf("Revising goes on the pull request: PR %q, issue %q", cs, eventCommentBodies(g.f, 7))
			}
			if is := strings.Join(eventCommentBodies(g.f, 7), "\n"); strings.Contains(is, "revis") {
				t.Fatalf("nothing review-driven on the issue (a run's start stays there): %q", is)
			}
			pr.HeadSHA = c.head
			g.f.SetPR(pr)
			g.f.SetChangedFiles(headBefore, headAfter, c.changed, nil)
			var fs []forge.FilePatch
			for _, p := range c.prFiles {
				fs = append(fs, forge.FilePatch{Path: p, Status: "modified"})
			}
			g.f.SetPullFiles(12, fs...)
			g.runs.set(rid(1), "Succeeded")
			g.log.end(rid(1), "Succeeded", "agent_finished")
			got = g.reconcile(t, "3buqdlot", 3) // replays included: once per revision
			if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || got.Status.Reason != c.wantReason {
				t.Fatalf("%s %q, want reason %q", got.Status.Phase, got.Status.Reason, c.wantReason)
			}
			var revised []string
			for _, b := range g.f.Comments(12) {
				if strings.Contains(b, "event=revised-") {
					revised = append(revised, b)
				}
			}
			if len(revised) != 1 {
				t.Fatalf("one Revised comment on the pull request, got %d: %q", len(revised), g.f.Comments(12))
			}
			for _, w := range c.want {
				if !strings.Contains(revised[0], w) {
					t.Errorf("missing %q in\n%s", w, revised[0])
				}
			}
			for _, w := range c.unwanted {
				if strings.Contains(revised[0], w) {
					t.Errorf("unexpected %q in\n%s", w, revised[0])
				}
			}
			if strings.Contains(revised[0], "AGENTS.md is a gate path") {
				t.Errorf("the review's text is never quoted:\n%s", revised[0])
			}
			if is := strings.Join(eventCommentBodies(g.f, 7), "\n"); strings.Contains(is, "revis") {
				t.Fatalf("nothing review-driven on the issue (a run's start stays there): %q", is)
			}
		})
	}
}
