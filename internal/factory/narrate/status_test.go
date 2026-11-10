// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

const statusRoomsURL = "https://rooms.example/"

func statusTask() *v1alpha1.Task {
	tk := task()
	tk.Name = "aaaaaaaa"
	tk.Spec.Source.Key = "Smana/repo#7:gen1"
	tk.Spec.Source.ContentSHA256 = strings.Repeat("a", 64)
	tk.Spec.Text = "## Acceptance check\n\n- the behavior works\n\n## Decisions\n\nD1 [g1]: use one comment, because it stays current\nA2 [g2]: assume UTC, because the factory uses it\nD3: keep the accepted scope\n"
	tk.Status.Phase = v1alpha1.PhaseImplementing
	tk.Status.PhaseSince = &metav1.Time{Time: time.Date(2026, 11, 9, 14, 2, 0, 0, time.UTC)}
	return tk
}

func TestStatus(t *testing.T) {
	tk := statusTask()
	tk.Status.Phase = v1alpha1.PhaseAwaitingHuman
	tk.Status.Reason = "class_unarmed"
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 1234, URL: "https://github.com/Smana/repo/pull/1234"}
	tk.Status.Usage.Tokens = 900_000
	tk.Status.Usage.CostMicroUSD = 42_000_000

	got := Status(tk, statusRoomsURL)
	for _, want := range []string{
		"Factory status · awaitinghuman · updated 2026-11-09 14:02 UTC",
		"Room: [3buqdlot](https://rooms.example/r/3buqdlot)",
		"waiting on: the factory does not merge this class: a maintainer reviews and merges it; next: a maintainer looks at the task's issue",
		"Spec: sha256:" + strings.Repeat("a", 64) + " · 2 decisions · 1 assumption",
		"PR: [#1234](https://github.com/Smana/repo/pull/1234)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status lacks %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"checks", "spend", "tokens", "$42", "900 k"} {
		if strings.Contains(strings.ToLower(got), unwanted) {
			t.Errorf("status unexpectedly contains %q:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "<!-- agent-factory issue-status -->") ||
		!strings.Contains(got, "<!-- agent-factory issue-status-owner task=aaaaaaaa generation=1 -->") {
		t.Fatalf("status lacks its issue and owner markers:\n%s", got)
	}
}

func TestSyncStatusNoOp(t *testing.T) {
	f := &statusForge{Fake: forgetest.NewFake()}
	n := Narrator{Forge: f, Login: bot}
	tk := statusTask()
	if err := n.SyncStatus(t.Context(), tk, statusRoomsURL); err != nil {
		t.Fatal(err)
	}
	if tk.Status.StatusComment == nil || tk.Status.StatusComment.ID == 0 || tk.Status.StatusComment.Body != Status(tk, statusRoomsURL) {
		t.Fatalf("creation was not recorded: %+v", tk.Status.StatusComment)
	}
	f.calls = nil
	if err := n.SyncStatus(t.Context(), tk, statusRoomsURL); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("an unchanged status made forge calls: %q", f.calls)
	}
}

func TestSyncStatusRecoversCreatedComment(t *testing.T) {
	f := &statusForge{Fake: forgetest.NewFake()}
	tk := statusTask()
	id, err := f.Fake.CommentWithID(t.Context(), tk.Spec.Issue, Status(tk, statusRoomsURL))
	if err != nil {
		t.Fatal(err)
	}
	// The Task status write was lost after GitHub created the comment.
	tk.Status.StatusComment = nil
	if err := (Narrator{Forge: f, Login: bot}).SyncStatus(t.Context(), tk, statusRoomsURL); err != nil {
		t.Fatal(err)
	}
	if got := len(f.Comments(tk.Spec.Issue)); got != 1 {
		t.Fatalf("recovery created a duplicate: %d comments", got)
	}
	if tk.Status.StatusComment == nil || tk.Status.StatusComment.ID != id || tk.Status.StatusComment.Body != Status(tk, statusRoomsURL) {
		t.Fatalf("the created comment was not adopted: %+v", tk.Status.StatusComment)
	}
	if f.count("create") != 0 || f.count("edit") != 0 || f.count("all") != 1 {
		t.Fatalf("unexpected recovery calls: %q", f.calls)
	}
}

func TestSyncStatusRecoversBeyondRecentWindow(t *testing.T) {
	f := &statusForge{Fake: forgetest.NewFake()}
	tk := statusTask()
	id, err := f.Fake.CommentWithID(t.Context(), tk.Spec.Issue, Status(tk, statusRoomsURL))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 65 {
		if err := f.Fake.Comment(t.Context(), tk.Spec.Issue, fmt.Sprintf("event %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	recent, err := f.Fake.RecentComments(t.Context(), tk.Spec.Issue)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range recent {
		if c.ID == id {
			t.Fatal("the status comment is still inside the recent window")
		}
	}
	tk.Status.StatusComment = nil
	if err := (Narrator{Forge: f, Login: bot}).SyncStatus(t.Context(), tk, statusRoomsURL); err != nil {
		t.Fatal(err)
	}
	if got := len(f.Comments(tk.Spec.Issue)); got != 66 {
		t.Fatalf("full-history recovery created a duplicate: %d comments", got)
	}
	if tk.Status.StatusComment == nil || tk.Status.StatusComment.ID != id {
		t.Fatalf("the old status comment was not recovered: %+v", tk.Status.StatusComment)
	}
	if f.count("recent") != 0 || f.count("all") != 1 || f.count("create") != 0 {
		t.Fatalf("recovery used the wrong comment window: %q", f.calls)
	}
}

func TestSyncStatusRetriesFailedCreate(t *testing.T) {
	f := &statusForge{Fake: forgetest.NewFake(), createErr: errors.New("502")}
	n := Narrator{Forge: f, Login: bot}
	tk := statusTask()
	if err := n.SyncStatus(t.Context(), tk, statusRoomsURL); err == nil {
		t.Fatal("the failed create was hidden")
	}
	if tk.Status.StatusComment != nil || len(f.Comments(tk.Spec.Issue)) != 0 {
		t.Fatalf("a failed create was recorded: %+v", tk.Status.StatusComment)
	}
	f.createErr = nil
	if err := n.SyncStatus(t.Context(), tk, statusRoomsURL); err != nil {
		t.Fatal(err)
	}
	if tk.Status.StatusComment == nil || len(f.Comments(tk.Spec.Issue)) != 1 || f.count("create") != 2 {
		t.Fatalf("create did not retry cleanly: ledger %+v, calls %q", tk.Status.StatusComment, f.calls)
	}
}

func TestSyncStatusRetriesFailedEdit(t *testing.T) {
	f := &statusForge{Fake: forgetest.NewFake()}
	n := Narrator{Forge: f, Login: bot}
	tk := statusTask()
	if err := n.SyncStatus(t.Context(), tk, statusRoomsURL); err != nil {
		t.Fatal(err)
	}
	previous := tk.Status.StatusComment.Body
	tk.Status.Phase = v1alpha1.PhaseReviewing
	f.editErr = errors.New("502")
	if err := n.SyncStatus(t.Context(), tk, statusRoomsURL); err == nil {
		t.Fatal("the failed edit was hidden")
	}
	if tk.Status.StatusComment.Body != previous || f.Comments(tk.Spec.Issue)[0] != previous {
		t.Fatal("a failed edit replaced the last successful body")
	}
	f.editErr = nil
	if err := n.SyncStatus(t.Context(), tk, statusRoomsURL); err != nil {
		t.Fatal(err)
	}
	want := Status(tk, statusRoomsURL)
	if tk.Status.StatusComment.Body != want || f.Comments(tk.Spec.Issue)[0] != want || f.count("edit") != 2 {
		t.Fatalf("edit did not retry cleanly: ledger %+v, calls %q", tk.Status.StatusComment, f.calls)
	}
}

func TestSyncStatusGenerationOwnership(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		phase                string
		currentName          string
		currentGen, ownerGen int
		wantWrite            bool
	}{
		{name: "newer terminal generation adopts", phase: v1alpha1.PhaseDone, currentName: "bbbbbbbb", currentGen: 2, ownerGen: 1, wantWrite: true},
		{name: "newer active generation adopts", phase: v1alpha1.PhaseImplementing, currentName: "bbbbbbbb", currentGen: 2, ownerGen: 1, wantWrite: true},
		{name: "equal generation must own", phase: v1alpha1.PhaseDone, currentName: "aaaaaaaa", currentGen: 2, ownerGen: 2, wantWrite: true},
		{name: "equal generation other task cannot write", phase: v1alpha1.PhaseDone, currentName: "bbbbbbbb", currentGen: 2, ownerGen: 2},
		{name: "older terminal generation cannot write", phase: v1alpha1.PhaseDone, currentName: "aaaaaaaa", currentGen: 1, ownerGen: 2},
		{name: "older escalated generation cannot write", phase: v1alpha1.PhaseEscalated, currentName: "aaaaaaaa", currentGen: 1, ownerGen: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &statusForge{Fake: forgetest.NewFake()}
			owner := statusTask()
			owner.Name = "aaaaaaaa"
			owner.Spec.Source.Key = fmt.Sprintf("Smana/repo#7:gen%d", tc.ownerGen)
			ownerBody := Status(owner, statusRoomsURL)
			id, err := f.Fake.CommentWithID(t.Context(), owner.Spec.Issue, ownerBody)
			if err != nil {
				t.Fatal(err)
			}

			current := statusTask()
			current.Name = tc.currentName
			current.Spec.Source.Key = fmt.Sprintf("Smana/repo#7:gen%d", tc.currentGen)
			current.Status.Phase = tc.phase
			if err := (Narrator{Forge: f, Login: bot}).SyncStatus(t.Context(), current, statusRoomsURL); err != nil {
				t.Fatal(err)
			}
			gotBody := f.Comments(current.Spec.Issue)[0]
			if tc.wantWrite {
				want := Status(current, statusRoomsURL)
				if gotBody != want || current.Status.StatusComment == nil || current.Status.StatusComment.ID != id || current.Status.StatusComment.Body != want {
					t.Fatalf("current generation did not adopt and edit: ledger %+v\nbody %s", current.Status.StatusComment, gotBody)
				}
			} else if gotBody != ownerBody || current.Status.StatusComment != nil || f.count("edit") != 0 || f.count("create") != 0 {
				t.Fatalf("a generation without ownership wrote status: ledger %+v, calls %q", current.Status.StatusComment, f.calls)
			}
		})
	}
}

type statusForge struct {
	*forgetest.Fake
	calls              []string
	createErr, editErr error
}

func (f *statusForge) Comment(ctx context.Context, n int, body string) error {
	f.calls = append(f.calls, "comment")
	return f.Fake.Comment(ctx, n, body)
}

func (f *statusForge) CommentWithID(ctx context.Context, n int, body string) (int64, error) {
	f.calls = append(f.calls, "create")
	if f.createErr != nil {
		return 0, f.createErr
	}
	return f.Fake.CommentWithID(ctx, n, body)
}

func (f *statusForge) EditComment(ctx context.Context, id int64, body string) error {
	f.calls = append(f.calls, "edit")
	if f.editErr != nil {
		return f.editErr
	}
	return f.Fake.EditComment(ctx, id, body)
}

func (f *statusForge) RecentComments(ctx context.Context, n int) ([]forge.Comment, error) {
	f.calls = append(f.calls, "recent")
	return f.Fake.RecentComments(ctx, n)
}

func (f *statusForge) AllComments(ctx context.Context, n int) ([]forge.Comment, error) {
	f.calls = append(f.calls, "all")
	return f.Fake.AllComments(ctx, n)
}

func (f *statusForge) count(call string) int {
	n := 0
	for _, got := range f.calls {
		if got == call {
			n++
		}
	}
	return n
}
