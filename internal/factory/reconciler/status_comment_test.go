// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

const issueStatusMarker = "<!-- agent-factory issue-status -->"

func TestStatusCommentLifecycle(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	f := &reconcilerStatusForge{Fake: g.f}
	g.r.Forge = f

	tk := g.reconcile(t, "3buqdlot", 2) // Received → Triaged → Queued narrates nothing.
	if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.StatusComment != nil || len(statusCommentBodies(g.f, 7)) != 0 {
		t.Fatalf("status was created before narration: phase %s, ledger %+v", tk.Status.Phase, tk.Status.StatusComment)
	}

	tk = g.reconcile(t, "3buqdlot", 1) // Queued → Implementing writes and drains Started.
	if tk.Status.StatusComment == nil || tk.Status.StatusComment.ID == 0 {
		t.Fatalf("the first narration did not persist a status comment: %+v", tk.Status.StatusComment)
	}
	id := tk.Status.StatusComment.ID
	if got := statusCommentBodies(g.f, 7); len(got) != 1 || got[0] != tk.Status.StatusComment.Body ||
		!strings.Contains(got[0], "Factory status · implementing") {
		t.Fatalf("first status: ledger %+v, remote %q", tk.Status.StatusComment, got)
	}
	if f.count("create") != 1 {
		t.Fatalf("first narration made status calls %q", f.calls)
	}

	// Discovering the pull request is a material change: edit the same comment, never add another.
	g.runs.set(rid(0), "Running")
	g.f.SetBranch("agent/3buqdlot", 12)
	g.f.SetPR(forge.PR{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", State: "OPEN", NodeID: "PR_1", HeadSHA: "abc"})
	tk = g.reconcile(t, "3buqdlot", 1)
	if got := statusCommentBodies(g.f, 7); len(got) != 1 || tk.Status.StatusComment.ID != id ||
		!strings.Contains(got[0], "PR: [#12](https://github.com/Smana/cloud-native-ref/pull/12)") || f.count("edit") != 1 {
		t.Fatalf("material change did not edit %d: ledger %+v, remote %q, calls %q", id, tk.Status.StatusComment, got, f.calls)
	}

	f.calls = nil
	tk = g.reconcile(t, "3buqdlot", 1)
	if len(f.calls) != 0 {
		t.Fatalf("an identical reconcile made status calls: %q", f.calls)
	}
	if got := statusCommentBodies(g.f, 7); len(got) != 1 || got[0] != tk.Status.StatusComment.Body {
		t.Fatalf("an identical reconcile changed status: ledger %+v, remote %q", tk.Status.StatusComment, got)
	}

	// The terminal transition is synchronized even though the closing event waits for usage to settle.
	g.runs.set(rid(0), "Succeeded")
	g.log.end(rid(0), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseAwaitingCI {
		t.Fatalf("run end reached %s, want AwaitingCI", tk.Status.Phase)
	}
	g.f.SetPR(forge.PR{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", State: "MERGED", MergedBy: "Smana", MergeCommitSHA: "def"})
	tk = g.reconcile(t, "3buqdlot", 1)
	if got := statusCommentBodies(g.f, 7); tk.Status.Phase != v1alpha1.PhaseDone || tk.Status.StatusComment.ID != id ||
		len(got) != 1 || !strings.Contains(got[0], "Factory status · done") {
		t.Fatalf("terminal outcome was not rendered on %d: phase %s, ledger %+v, remote %q", id, tk.Status.Phase, tk.Status.StatusComment, got)
	}
}

func TestStatusCommentGenerationOwnership(t *testing.T) {
	f := &reconcilerStatusForge{Fake: forgetest.NewFake()}
	oldBody := "generation one's status\n" + issueStatusMarker + "\n<!-- agent-factory issue-status-owner task=aaaaaaaa generation=1 -->"
	id, err := f.Fake.CommentWithID(t.Context(), 7, oldBody)
	if err != nil {
		t.Fatal(err)
	}

	newer := issueTask("bbbbbbbb", 7, "x")
	newer.Spec.Source.Key = "Smana/cloud-native-ref#7:gen2"
	newer.Status.Phase = v1alpha1.PhaseDone
	newer.Status.PhaseSince = &metav1.Time{Time: now}
	newer.Status.Narrated = []string{"started-0"}
	g := newRig(t, newer)
	g.f, g.r.Forge = f.Fake, f
	tk := g.reconcile(t, "bbbbbbbb", 1)
	if tk.Status.StatusComment == nil || tk.Status.StatusComment.ID != id || f.count("create") != 0 || f.count("edit") != 1 {
		t.Fatalf("generation 2 did not adopt comment %d: ledger %+v, calls %q", id, tk.Status.StatusComment, f.calls)
	}
	newBody := strings.Join(statusCommentBodies(f.Fake, 7), "")
	if !strings.Contains(newBody, "task=bbbbbbbb generation=2") || !strings.Contains(newBody, "Factory status · done") {
		t.Fatalf("generation 2 did not replace the body: %s", newBody)
	}

	older := issueTask("aaaaaaaa", 7, "x")
	older.Spec.Source.Key = "Smana/cloud-native-ref#7:gen1"
	older.Status.Phase = v1alpha1.PhaseEscalated
	older.Status.PhaseSince = &metav1.Time{Time: now}
	older.Status.Narrated = []string{"started-0"}
	older.Status.StatusComment = &v1alpha1.StatusComment{ID: id, Body: oldBody}
	oldRig := newRig(t, older)
	oldRig.f, oldRig.r.Forge = f.Fake, f
	f.calls = nil
	tk = oldRig.reconcile(t, "aaaaaaaa", 1)
	if got := strings.Join(statusCommentBodies(f.Fake, 7), ""); got != newBody || f.count("create") != 0 || f.count("edit") != 0 {
		t.Fatalf("escalated generation 1 overwrote generation 2: body %q, calls %q", got, f.calls)
	}
	if tk.Status.StatusComment == nil || tk.Status.StatusComment.Body != oldBody {
		t.Fatalf("the refused generation changed its successful-body ledger: %+v", tk.Status.StatusComment)
	}
}

func TestStatusCommentFailuresDoNotBlockTransition(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, strings.Repeat("x", 14337)))
	f := &reconcilerStatusForge{Fake: g.f, createErr: errors.New("github unavailable")}
	g.r.Forge = f

	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err == nil || !strings.Contains(err.Error(), "github unavailable") {
		t.Fatalf("the status failure was not returned for retry: %v", err)
	}
	tk := g.reconcile(t, "3buqdlot", 0)
	if tk.Status.Phase != v1alpha1.PhaseRejected || tk.Status.Reason != "text_too_long" || len(tk.Status.Outbox) != 0 {
		t.Fatalf("the status failure rolled back the transition or event drain: %+v", tk.Status)
	}
	eventBodies := eventCommentBodies(g.f, 7)
	if len(eventBodies) != 1 || !strings.Contains(eventBodies[0], "longer than the factory accepts") || tk.Status.StatusComment != nil {
		t.Fatalf("unexpected remote/ledger state after the failure: events %q, ledger %+v", eventBodies, tk.Status.StatusComment)
	}

	f.createErr = nil
	f.calls = nil
	tk = g.reconcile(t, "3buqdlot", 1)
	if tk.Status.StatusComment == nil || tk.Status.StatusComment.ID == 0 || f.count("create") != 1 {
		t.Fatalf("the failed create did not retry: ledger %+v, calls %q", tk.Status.StatusComment, f.calls)
	}
	if got := eventCommentBodies(g.f, 7); len(got) != 1 || got[0] != eventBodies[0] {
		t.Fatalf("status retry changed the existing event comments: before %q, after %q", eventBodies, got)
	}
	if got := statusCommentBodies(g.f, 7); len(got) != 1 || !strings.Contains(got[0], "Factory status · rejected") {
		t.Fatalf("retry did not render the terminal outcome: %q", got)
	}
}

func TestStatusCommentUsesApproveToMergeConfig(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "x")
	tk.Status.Phase = v1alpha1.PhaseAwaitingHuman
	tk.Status.Reason = "class_unarmed"
	tk.Status.PhaseSince = &metav1.Time{Time: now}
	tk.Status.Narrated = []string{"handoff-class_unarmed"}
	tk.Status.Runs = []v1alpha1.RunRecord{{ID: rid(0), Role: "implementer", Phase: "Succeeded", Started: &metav1.Time{Time: now.Add(-time.Minute)}, Finished: &metav1.Time{Time: now}}}
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", HeadSHA: "abc"}
	g := newRig(t, tk)
	g.r.Cfg.Merge.ApproveToMerge = true
	g.f.SetPR(forge.PR{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", State: "OPEN", HeadSHA: "abc", HeadRef: "agent/3buqdlot"})

	g.reconcile(t, "3buqdlot", 1)
	body := strings.Join(statusCommentBodies(g.f, 7), "")
	for _, want := range []string{
		"the factory does not merge this class on its own: it merges once a maintainer approves the current commit",
		"next: a maintainer reviews, then approves the current commit or closes the pull request on GitHub; the merger App merges that commit",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("status does not follow merge.approveToMerge: missing %q in\n%s", want, body)
		}
	}
}

type reconcilerStatusForge struct {
	*forgetest.Fake
	calls     []string
	createErr error
}

func (f *reconcilerStatusForge) CommentWithID(ctx context.Context, n int, body string) (int64, error) {
	f.calls = append(f.calls, "create")
	if f.createErr != nil {
		return 0, f.createErr
	}
	return f.Fake.CommentWithID(ctx, n, body)
}

func (f *reconcilerStatusForge) EditComment(ctx context.Context, id int64, body string) error {
	f.calls = append(f.calls, "edit")
	return f.Fake.EditComment(ctx, id, body)
}

func (f *reconcilerStatusForge) AllComments(ctx context.Context, n int) ([]forge.Comment, error) {
	f.calls = append(f.calls, "all")
	return f.Fake.AllComments(ctx, n)
}

func (f *reconcilerStatusForge) count(call string) int {
	n := 0
	for _, got := range f.calls {
		if got == call {
			n++
		}
	}
	return n
}

func statusCommentBodies(f *forgetest.Fake, n int) []string {
	var out []string
	for _, body := range f.Comments(n) {
		if strings.Contains(body, issueStatusMarker) {
			out = append(out, body)
		}
	}
	return out
}

func eventCommentBodies(f *forgetest.Fake, n int) []string {
	var out []string
	for _, body := range f.Comments(n) {
		if !strings.Contains(body, issueStatusMarker) {
			out = append(out, body)
		}
	}
	return out
}

func eventComments(comments []forge.Comment) []forge.Comment {
	return slices.DeleteFunc(slices.Clone(comments), func(comment forge.Comment) bool {
		return strings.Contains(comment.Body, issueStatusMarker)
	})
}
