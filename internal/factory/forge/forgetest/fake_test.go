// SPDX-License-Identifier: Apache-2.0

package forgetest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/forge"
)

// api is the method set the factory's consumers declare (narrate, intake, reconciler): the
// GitHub adapter and the fake must never drift apart.
type api interface {
	Labeled(ctx context.Context, label string) ([]forge.Item, error)
	LabelEvents(ctx context.Context, number int, label string) ([]forge.LabelEvent, error)
	Issue(ctx context.Context, number int) (forge.Issue, error)
	RenderMarkdown(ctx context.Context, text string) (string, error)
	Comment(ctx context.Context, number int, body string) error
	CommentWithID(ctx context.Context, number int, body string) (int64, error)
	EditComment(ctx context.Context, id int64, body string) error
	RecentComments(ctx context.Context, number int) ([]forge.Comment, error)
	AllComments(ctx context.Context, number int) ([]forge.Comment, error)
	AddLabels(ctx context.Context, number int, labels ...string) error
	RemoveLabel(ctx context.Context, number int, label string) error
	PullRequestForBranch(ctx context.Context, branch string) (int, error)
	PullRequest(ctx context.Context, number int) (forge.PR, error)
}

var _ api = (*Fake)(nil)

func TestFakeRecordsWhatTheFactoryDid(t *testing.T) {
	ctx := t.Context()
	at := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	f := NewFake()
	f.Now = func() time.Time { return at }
	f.SetLabeled("factory/ready", forge.Item{Number: 7}, forge.Item{Number: 9, PullRequest: true})
	f.SetIssue(forge.Issue{Number: 7, Title: "Fix the link"})
	f.SetPR(forge.PR{Number: 12, HeadRef: "agent/3buqdlot"})
	f.SetBranch("agent/3buqdlot", 12)
	f.SetEvents(7, forge.LabelEvent{Actor: "Smana", Label: "factory/ready", At: at})
	if err := f.Comment(ctx, 7, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := f.AddLabels(ctx, 7, "factory/class:docs-links"); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveLabel(ctx, 7, "factory/ready"); err != nil {
		t.Fatal(err)
	}
	items, _ := f.Labeled(ctx, "factory/ready")
	if len(items) != 1 || items[0].Number != 9 {
		t.Errorf("a removed label leaves the listing: %v", items)
	}
	if got := f.Comments(7); len(got) != 1 || got[0] != "hello" {
		t.Errorf("comments %v", got)
	}
	cs, _ := f.RecentComments(ctx, 7)
	if len(cs) != 1 || cs[0].Author != "ogenki-agent-factory[bot]" || !cs[0].At.Equal(at) || cs[0].ID != 1 {
		t.Errorf("recent %+v", cs)
	}
	if a, r := f.Added(7), f.Removed(7); len(a) != 1 || len(r) != 1 {
		t.Errorf("added %v removed %v", a, r)
	}
	if n, _ := f.PullRequestForBranch(ctx, "agent/3buqdlot"); n != 12 {
		t.Errorf("branch → %d", n)
	}
	if evs, _ := f.LabelEvents(ctx, 7, "factory/ready"); len(evs) != 1 {
		t.Errorf("events %v", evs)
	}
	if evs, _ := f.LabelEvents(ctx, 7, "factory/stop"); len(evs) != 0 {
		t.Errorf("another label's events: %v", evs)
	}
	f.SetEventsTruncated(7)
	if evs, err := f.LabelEvents(ctx, 7, "factory/ready"); len(evs) != 1 || !errors.Is(err, forge.ErrEventsTruncated) {
		t.Errorf("truncated: %v %v", evs, err)
	}
	if _, err := f.Issue(ctx, 8); err == nil {
		t.Error("an unknown issue is an error")
	}
	if _, err := f.PullRequest(ctx, 13); err == nil {
		t.Error("an unknown PR is an error")
	}
	if p, err := f.PullRequest(ctx, 12); err != nil || p.HeadRef != "agent/3buqdlot" {
		t.Errorf("%+v %v", p, err)
	}
	f.SetFiles(map[string]string{"docs/a.md": "old"}, map[string]string{"docs/a.md": "new"})
	if d, err := f.Diff(ctx, "base", "head"); err != nil || d.Base["docs/a.md"] != "old" || d.Head["docs/a.md"] != "new" ||
		len(d.Files) != 1 || d.Files[0] != (forge.FileStat{Path: "docs/a.md", Status: "modified", Additions: 1, Deletions: 1}) {
		t.Errorf("diff %+v %v", d, err)
	}
}

func TestCommentIdentityAndEdit(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	first, err := f.CommentWithID(ctx, 7, "first")
	if err != nil || first == 0 {
		t.Fatalf("first id %d, %v", first, err)
	}
	second, err := f.CommentWithID(ctx, 7, "second")
	if err != nil || second == 0 || second <= first {
		t.Fatalf("second id %d after %d, %v", second, first, err)
	}
	if err := f.EditComment(ctx, first, "first edited"); err != nil {
		t.Fatal(err)
	}
	cs, err := f.AllComments(ctx, 7)
	if err != nil || len(cs) != 2 {
		t.Fatalf("%+v, %v", cs, err)
	}
	if cs[0].ID != second || cs[0].Body != "second" || cs[0].Edited {
		t.Fatalf("the other comment changed: %+v", cs[0])
	}
	if cs[1].ID != first || cs[1].Body != "first edited" || !cs[1].Edited {
		t.Fatalf("the edited comment: %+v", cs[1])
	}
	if err := f.EditComment(ctx, 999, "absent"); err == nil {
		t.Fatal("editing an absent comment is an error")
	}
}

func TestAllCommentsPaginates(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	for i := 1; i <= 120; i++ {
		if _, err := f.CommentWithID(ctx, 7, fmt.Sprintf("c%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	all, err := f.AllComments(ctx, 7)
	if err != nil || len(all) != 120 {
		t.Fatalf("%d comments, %v", len(all), err)
	}
	for i, c := range all {
		want := int64(120 - i)
		if c.ID != want || c.Body != fmt.Sprintf("c%d", want) {
			t.Fatalf("comment %d: %+v, want id and body %d", i, c, want)
		}
	}
	recent, err := f.RecentComments(ctx, 7)
	if err != nil || len(recent) != 50 {
		t.Fatalf("recent: %d comments, %v", len(recent), err)
	}
	for i := range recent {
		if recent[i] != all[i] {
			t.Fatalf("recent comment %d: %+v, want %+v", i, recent[i], all[i])
		}
	}
}

// RenderMarkdown answers an inert page by default, escaped as GitHub escapes text, and what a test
// sets otherwise; it records what it was asked.
func TestFakeRenderMarkdown(t *testing.T) {
	f := NewFake()
	if got, err := f.RenderMarkdown(t.Context(), "a <b>"); err != nil || got != "<p>a &lt;b&gt;</p>" {
		t.Fatalf("%q %v", got, err)
	}
	f.SetRender(func(string) (string, error) { return "", errors.New("rate limit") })
	if _, err := f.RenderMarkdown(t.Context(), "c"); err == nil {
		t.Fatal("the set renderer was not used")
	}
	if got := f.Rendered(); len(got) != 2 || got[0] != "a <b>" || got[1] != "c" {
		t.Fatalf("%q", got)
	}
}

// The amendment (external review R02, ruling R52): a decision merges with expectedHeadOid, and
// GitHub checks the head at merge time. The fake must refuse a moved head the same way — and
// nothing may merge on it.
func TestFMergeRefusesAMovedHead(t *testing.T) {
	ctx := t.Context()
	f := NewFake()
	f.SetPR(forge.PR{Number: 12, NodeID: "PR_12", State: "OPEN", HeadSHA: "abc"})
	if err := f.Merge(ctx, "PR_12", "def"); !errors.Is(err, forge.ErrHeadMoved) {
		t.Fatalf("a moved head: %v", err)
	}
	if a := f.Armed(); len(a) != 0 {
		t.Fatalf("a refused merge is not a merge: %v", a)
	}
	if p, err := f.PullRequest(ctx, 12); err != nil || p.State != "OPEN" {
		t.Fatalf("nothing merged: %+v %v", p, err)
	}
	if err := f.Merge(ctx, "PR_12", "abc"); err != nil {
		t.Fatal(err)
	}
	if a := f.Armed(); len(a) != 1 || a[0] != "PR_12 abc" {
		t.Fatalf("the merge records the decided head: %v", a)
	}
}

// A created issue is numbered above every seeded one, and creates and closes can be made to fail.
func TestFakeIssuesNeverCollideAndCanFail(t *testing.T) {
	f := NewFake()
	f.SetIssue(forge.Issue{Number: 501, Title: "seeded"})
	iss, err := f.CreateIssue(t.Context(), "t", "b", nil)
	if err != nil || iss.Number <= 501 {
		t.Fatalf("%+v %v", iss, err)
	}
	boom := errors.New("boom")
	f.FailCreates(boom)
	f.FailCloses(boom)
	if _, err := f.CreateIssue(t.Context(), "t", "b", nil); !errors.Is(err, boom) {
		t.Errorf("create: %v", err)
	}
	if err := f.CloseIssue(t.Context(), iss.Number); !errors.Is(err, boom) {
		t.Errorf("close: %v", err)
	}
}

// The follow-up reads return what was set, MergedSince keeps what merged at or after since (to the
// second, as the search does), and BlameAtParent counts its calls.
func TestFakeFollowUpReads(t *testing.T) {
	ctx := t.Context()
	at := time.Date(2026, 10, 16, 10, 0, 0, 500, time.UTC)
	f := NewFake()
	f.SetMerged(forge.MergedPull{Number: 4, MergedAt: at.Add(-time.Minute)}, forge.MergedPull{Number: 5, MergeCommitSHA: "m5", MergedAt: at})
	f.SetPullFiles(5, forge.FilePatch{Path: "a.yaml", Patch: "@@ -1 +1 @@"})
	f.SetBlame("m5", "a.yaml", forge.BlameRange{Start: 1, End: 3, Commit: "c"})
	if ps, err := f.MergedSince(ctx, at); err != nil || len(ps) != 1 || ps[0].Number != 5 {
		t.Errorf("merged %+v %v", ps, err)
	}
	if ps, _ := f.MergedSince(ctx, at.Add(-time.Hour)); len(ps) != 2 {
		t.Errorf("an earlier since keeps both: %+v", ps)
	}
	if fs, err := f.PullFiles(ctx, 5); err != nil || len(fs) != 1 || fs[0].Path != "a.yaml" {
		t.Errorf("files %+v %v", fs, err)
	}
	if _, err := f.PullFiles(ctx, 6); err == nil {
		t.Error("a pull request never set is an error, as PullRequest's is")
	}
	if rs, err := f.BlameAtParent(ctx, "m5", "a.yaml"); err != nil || len(rs) != 1 || rs[0].Commit != "c" {
		t.Errorf("blame %+v %v", rs, err)
	}
	if _, err := f.BlameAtParent(ctx, "m5", "other.yaml"); !errors.Is(err, forge.ErrNoBlame) {
		t.Errorf("an unset file has nothing to blame: %v", err)
	}
	if f.BlameCalls() != 2 {
		t.Errorf("calls %d", f.BlameCalls())
	}
}

func TestFakeFollowUpReadFailures(t *testing.T) {
	ctx := t.Context()
	f := NewFake()
	f.SetPullFiles(5)
	f.SetBlame("m5", "a.yaml")
	f.FailMergedSince(forge.ErrMergedTruncated)
	f.FailPullFiles(5, forge.ErrNoPatch)
	f.FailBlame("m5", "a.yaml", forge.ErrNoBlame)
	if _, err := f.MergedSince(ctx, time.Time{}); !errors.Is(err, forge.ErrMergedTruncated) {
		t.Error(err)
	}
	if _, err := f.PullFiles(ctx, 5); !errors.Is(err, forge.ErrNoPatch) {
		t.Error(err)
	}
	if _, err := f.BlameAtParent(ctx, "m5", "a.yaml"); !errors.Is(err, forge.ErrNoBlame) || f.BlameCalls() != 1 {
		t.Error(err)
	}
	f.FailMergedSince(nil)
	f.FailPullFiles(5, nil)
	f.FailBlame("m5", "a.yaml", nil)
	if _, err := f.MergedSince(ctx, time.Time{}); err != nil {
		t.Error(err)
	}
	if _, err := f.PullFiles(ctx, 5); err != nil {
		t.Error(err)
	}
	if _, err := f.BlameAtParent(ctx, "m5", "a.yaml"); err != nil {
		t.Error(err)
	}
}

func TestFakeIsAncestor(t *testing.T) {
	f := NewFake()
	f.SetAncestors("c3", "c1", "c2")
	for _, tc := range []struct {
		anc, sha string
		want     bool
	}{{"c1", "c3", true}, {"c3", "c3", true}, {"c3", "c1", false}, {"x", "c3", false}} {
		if got, err := f.IsAncestor(t.Context(), tc.anc, tc.sha); err != nil || got != tc.want {
			t.Errorf("%s,%s: %v %v", tc.anc, tc.sha, got, err)
		}
	}
}

// The shadow pass's reads (HR1) answer as GitHub's: one actor's label additions and one login's
// issues inside a window, oldest first.
func TestFakeReadsAnAgentsWrites(t *testing.T) {
	ctx := t.Context()
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	f := NewFake()
	f.SetEvents(7,
		forge.LabelEvent{Actor: "agents[bot]", Label: "late", At: at.Add(2 * time.Minute)},
		forge.LabelEvent{Actor: "agents[bot]", Label: "before", At: at.Add(-time.Minute)},
		forge.LabelEvent{Actor: "Smana", Label: "human", At: at.Add(time.Minute)},
		forge.LabelEvent{Actor: "agents[bot]", Label: "first", At: at})
	evs, err := f.LabelEventsBy(ctx, 7, "agents[bot]", at)
	if err != nil || len(evs) != 2 || evs[0].Label != "first" || evs[1].Label != "late" {
		t.Fatalf("%+v %v", evs, err)
	}
	f.SetEventsTruncated(7)
	if _, err := f.LabelEventsBy(ctx, 7, "agents[bot]", at); !errors.Is(err, forge.ErrEventsTruncated) {
		t.Fatalf("truncation: %v", err)
	}
	f.SetIssue(forge.Issue{Number: 9, Author: "agents[bot]", CreatedAt: at.Add(time.Minute)})
	f.SetIssue(forge.Issue{Number: 8, Author: "agents[bot]", CreatedAt: at})
	f.SetIssue(forge.Issue{Number: 7, Author: "agents[bot]", CreatedAt: at.Add(-time.Minute)})
	f.SetIssue(forge.Issue{Number: 6, Author: "Smana", CreatedAt: at})
	iss, err := f.IssuesByAuthorSince(ctx, "agents[bot]", at)
	if err != nil || len(iss) != 2 || iss[0].Number != 8 || iss[1].Number != 9 {
		t.Fatalf("%+v %v", iss, err)
	}
	f.FailLists(errors.New("down"))
	if _, err := f.IssuesByAuthorSince(ctx, "agents[bot]", at); err == nil {
		t.Fatal("FailLists ignored")
	}
}
