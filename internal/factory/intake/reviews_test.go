// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

func TestTheReviewLabelIsAMaintainersOrTheAuthors(t *testing.T) {
	cases := []struct {
		name      string
		prAuthor  string
		labeller  string
		wantTask  bool
		wantError string
	}{
		{name: "maintainer creates review task", prAuthor: "alice", labeller: "Smana", wantTask: true},
		{name: "PR author creates review task", prAuthor: "alice", labeller: "alice", wantTask: true},
		{name: "stranger is refused", prAuthor: "alice", labeller: "stranger", wantTask: false, wantError: "unauthorised"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := forgetest.NewFake()
			f.SetLabeled(LabelReview, forge.Item{Number: 42, PullRequest: true})
			f.SetEvents(42, forge.LabelEvent{Actor: tc.labeller, Label: LabelReview, At: t0.Add(time.Minute)})
			f.SetPR(forge.PR{
				Number:  42,
				URL:     "https://github.com/Smana/cloud-native-ref/pull/42",
				Title:   "Add cache",
				Body:    "Adds in-memory cache.",
				State:   "OPEN",
				Author:  tc.prAuthor,
				BaseRef: "main",
				HeadRef: "feat/cache",
				HeadSHA: strings.Repeat("a", 40),
			})
			p, c := poller(t, f)
			if err := p.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := tasks(t, c)
			if tc.wantTask {
				if len(got) != 1 {
					t.Fatalf("expected 1 task, got %d (comments: %v)", len(got), f.Comments(42))
				}
				tk := got[0]
				if tk.Spec.Outcome != config.OutcomeReview || tk.Spec.Source.Kind != "review" || tk.Spec.Source.Ref != "Smana/cloud-native-ref#42" {
					t.Fatalf("unexpected review task spec: %+v", tk.Spec)
				}
				if gotRem := f.Removed(42); len(gotRem) != 1 || gotRem[0] != LabelReview {
					t.Fatalf("expected %q removed, got %v", LabelReview, gotRem)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("expected 0 tasks for %s, got %d", tc.labeller, len(got))
			}
			if gotRem := f.Removed(42); len(gotRem) != 1 || gotRem[0] != LabelReview {
				t.Fatalf("expected unauthorized %q removed, got %v", LabelReview, gotRem)
			}
			if cs := f.Comments(42); len(cs) != 1 {
				t.Fatalf("expected 1 refusal comment, got %v", cs)
			}
		})
	}
}
