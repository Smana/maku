// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

func TestCanonicalGoal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "empty", text: " \t\n ", want: ""},
		{name: "NFKC fullwidth forms", text: "Ｆｉｘ　ＣＡＳＨ， ｎｏｗ！", want: "fix cash now"},
		{name: "NFKC compatibility characters", text: "ﬁx café", want: "fix café"},
		{name: "case and whitespace", text: "  Fix\tCACHE\ninvalidation  ", want: "fix cache invalidation"},
		{name: "ASCII punctuation", text: "Fix cache-invalidation, now!", want: "fix cache invalidation now"},
		{name: "Unicode punctuation", text: "Fix cache—invalidation…now", want: "fix cache invalidation now"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CanonicalGoal(tt.text); got != tt.want {
				t.Fatalf("CanonicalGoal(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestGoalFingerprint(t *testing.T) {
	t.Parallel()

	const want = "53a1ddbeb73030f098650a2499bb407853354b34ed560f7026fd654f06bc7d37"
	got := GoalFingerprint("Smana/Maku", "Ｆｉｘ　ＣＡＣＨＥ， ｎｏｗ！")
	if got != want {
		t.Fatalf("GoalFingerprint() = %q, want %q", got, want)
	}
	if again := GoalFingerprint("smana/maku", "fix cache now"); again != got {
		t.Fatalf("repository case or canonical goal changed fingerprint: %q != %q", again, got)
	}
	if other := GoalFingerprint("smana/other", "fix cache now"); other == got {
		t.Fatal("the same goal in another repository has the same fingerprint")
	}
}

func TestGoalTerms(t *testing.T) {
	t.Parallel()

	t.Run("sorted and deduplicated", func(t *testing.T) {
		t.Parallel()
		want := []string{"cache", "fix", "invalidation", "now"}
		if got := GoalTerms("Now, fix cache! Fix invalidation cache"); !reflect.DeepEqual(got, want) {
			t.Fatalf("GoalTerms() = %q, want %q", got, want)
		}
	})

	t.Run("cap retains the first 32 sorted distinct terms", func(t *testing.T) {
		t.Parallel()
		var words []string
		for i := 39; i >= 0; i-- {
			words = append(words, fmt.Sprintf("term%02d", i), fmt.Sprintf("term%02d", i))
		}
		want := []string{
			"term00", "term01", "term02", "term03", "term04", "term05", "term06", "term07",
			"term08", "term09", "term10", "term11", "term12", "term13", "term14", "term15",
			"term16", "term17", "term18", "term19", "term20", "term21", "term22", "term23",
			"term24", "term25", "term26", "term27", "term28", "term29", "term30", "term31",
		}
		if got := GoalTerms(strings.Join(words, " ")); !reflect.DeepEqual(got, want) {
			t.Fatalf("GoalTerms() = %q, want %q", got, want)
		}
	})
}

func TestRankOverlapCandidatesOrdering(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	candidates := make([]OverlapCandidate, 0, 26)
	for i := 19; i >= 0; i-- {
		candidates = append(candidates, OverlapCandidate{
			Match:  v1alpha1.OverlapMatch{Task: fmt.Sprintf("f%03d", i)},
			Text:   "fix",
			Recent: now,
		})
	}
	candidates = append(candidates,
		OverlapCandidate{Match: v1alpha1.OverlapMatch{Task: "inactive-full"}, Text: "fix cache invalidation preserve hit ratio", Recent: now},
		OverlapCandidate{Match: v1alpha1.OverlapMatch{Task: "active-two"}, Text: "fix cache", Active: true, Recent: now},
		OverlapCandidate{Match: v1alpha1.OverlapMatch{Task: "active-four-old"}, Text: "fix cache preserve hit", Active: true, Recent: now.Add(-time.Hour)},
		OverlapCandidate{Match: v1alpha1.OverlapMatch{Task: "active-four-b"}, Text: "fix cache preserve hit", Active: true, Recent: now},
		OverlapCandidate{Match: v1alpha1.OverlapMatch{Task: "active-four-a"}, Text: "fix cache preserve hit", Active: true, Recent: now},
		OverlapCandidate{Match: v1alpha1.OverlapMatch{Task: "active-full"}, Text: "fix cache invalidation preserve hit ratio", Active: true, Recent: now.Add(-24 * time.Hour)},
		OverlapCandidate{Match: v1alpha1.OverlapMatch{Task: "unrelated"}, Text: "write documentation", Active: true, Recent: now},
	)
	original := append([]OverlapCandidate(nil), candidates...)

	got := RankOverlapCandidates("fix cache invalidation", "preserve hit ratio", candidates)
	if len(got) != MaxOverlapCandidates {
		t.Fatalf("RankOverlapCandidates() returned %d candidates, want %d", len(got), MaxOverlapCandidates)
	}
	wantPrefix := []string{"active-full", "active-four-a", "active-four-b", "active-four-old", "active-two", "inactive-full"}
	for i, want := range wantPrefix {
		if got[i].Match.Task != want {
			t.Errorf("candidate %d = %q, want %q; full order: %s", i, got[i].Match.Task, want, overlapTaskNames(got))
		}
	}
	if got[MaxOverlapCandidates-1].Match.Task != "f013" {
		t.Errorf("last retained candidate = %q, want f013; global cap was not applied after sorting", got[MaxOverlapCandidates-1].Match.Task)
	}
	for _, c := range got {
		if c.Match.Task == "unrelated" {
			t.Error("candidate without a shared term was retained")
		}
		if c.Match.Relation != "lexical" {
			t.Errorf("candidate %q relation = %q, want lexical", c.Match.Task, c.Match.Relation)
		}
	}
	if again := RankOverlapCandidates("fix cache invalidation", "preserve hit ratio", candidates); !reflect.DeepEqual(again, got) {
		t.Errorf("repeat ranking changed order:\nfirst:  %s\nsecond: %s", overlapTaskNames(got), overlapTaskNames(again))
	}
	if !reflect.DeepEqual(candidates, original) {
		t.Error("RankOverlapCandidates mutated its input")
	}
}

func TestRankOverlapCandidatesExactActiveSurvivesLexicalCap(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	candidates := make([]OverlapCandidate, 0, 25)
	for i := 0; i < 24; i++ {
		candidates = append(candidates, OverlapCandidate{
			Match:    v1alpha1.OverlapMatch{Task: fmt.Sprintf("lexical-%02d", i), Phase: v1alpha1.PhaseImplementing},
			Text:     "fix cache invalidation preserve hit ratio",
			Active:   true,
			Recent:   now,
			Evidence: []string{"task"},
		})
	}
	candidates = append(candidates, OverlapCandidate{
		Match:  v1alpha1.OverlapMatch{Task: "exact-active", Phase: v1alpha1.PhaseMerged},
		Text:   "Fix, CACHE-invalidation!",
		Recent: now.Add(-168 * time.Hour),
	})

	got := RankOverlapCandidates("fix cache invalidation", "preserve hit ratio", candidates)
	if len(got) != MaxOverlapCandidates {
		t.Fatalf("RankOverlapCandidates() returned %d candidates, want %d", len(got), MaxOverlapCandidates)
	}
	var exact *OverlapCandidate
	for i := range got {
		if got[i].Match.Task == "exact-active" {
			exact = &got[i]
			break
		}
	}
	if exact == nil {
		t.Fatalf("exact active candidate was truncated; order: %s", overlapTaskNames(got))
	}
	if exact.Match.Relation != "exact" {
		t.Errorf("exact active relation = %q, want exact", exact.Match.Relation)
	}
	if !exact.Active {
		t.Error("Merged phase was not treated as active before truncation")
	}
}

func TestRankOverlapCandidatesPhaseActivity(t *testing.T) {
	t.Parallel()

	candidates := []OverlapCandidate{
		{Match: v1alpha1.OverlapMatch{Task: "split", Phase: v1alpha1.PhaseSplit}, Text: "same goal"},
		{Match: v1alpha1.OverlapMatch{Task: "merged", Phase: v1alpha1.PhaseMerged}, Text: "same goal"},
		{Match: v1alpha1.OverlapMatch{Task: "escalated", Phase: v1alpha1.PhaseEscalated}, Text: "same goal"},
		{Match: v1alpha1.OverlapMatch{Task: "empty", Phase: ""}, Text: "same goal"},
		{Match: v1alpha1.OverlapMatch{Task: "run", Phase: v1alpha1.PhaseDone}, Text: "same goal", Active: true},
	}
	got := RankOverlapCandidates("same goal", "", candidates)
	wantActive := map[string]bool{"split": false, "merged": true, "escalated": false, "empty": false, "run": true}
	if len(got) != len(wantActive) {
		t.Fatalf("RankOverlapCandidates() returned %d candidates, want %d", len(got), len(wantActive))
	}
	for _, c := range got {
		want, ok := wantActive[c.Match.Task]
		if !ok {
			t.Errorf("unexpected candidate %q", c.Match.Task)
			continue
		}
		if c.Active != want {
			t.Errorf("candidate %q active = %t, want %t", c.Match.Task, c.Active, want)
		}
	}
}

func TestRankOverlapCandidatesEmptyText(t *testing.T) {
	t.Parallel()

	candidates := []OverlapCandidate{
		{Match: v1alpha1.OverlapMatch{Task: "empty"}, Text: "", Active: true},
		{Match: v1alpha1.OverlapMatch{Task: "nonempty"}, Text: "fix cache", Active: true},
	}
	if got := RankOverlapCandidates("", "", candidates); len(got) != 0 {
		t.Fatalf("empty goal and acceptance returned %s, want no candidates", overlapTaskNames(got))
	}

	got := RankOverlapCandidates("", "fix cache", candidates)
	if len(got) != 1 || got[0].Match.Task != "nonempty" || got[0].Match.Relation != "lexical" {
		t.Fatalf("acceptance-only ranking = %#v, want one lexical nonempty candidate", got)
	}

	got = RankOverlapCandidates("fix cache", "", candidates)
	if len(got) != 1 || got[0].Match.Task != "nonempty" || got[0].Match.Relation != "exact" {
		t.Fatalf("empty-acceptance ranking = %#v, want one exact nonempty candidate", got)
	}
}

func TestRankOverlapCandidatesUsesSanitizedStoredText(t *testing.T) {
	t.Parallel()

	stored := v1alpha1.Task{Spec: v1alpha1.TaskSpec{
		Text:   "Fix, CACHE!",
		Source: v1alpha1.Source{ContentSHA256: strings.Repeat("a", 64)},
	}}
	candidates := []OverlapCandidate{{Match: v1alpha1.OverlapMatch{Task: "stored"}, Text: stored.Spec.Text}}
	got := RankOverlapCandidates("fix cache", "", candidates)
	if len(got) != 1 || got[0].Match.Relation != "exact" {
		t.Fatalf("stored sanitized goal did not exact-match: %#v", got)
	}

	stored.Spec.Source.ContentSHA256 = strings.Repeat("b", 64)
	if again := RankOverlapCandidates("fix cache", "", candidates); !reflect.DeepEqual(again, got) {
		t.Fatal("raw source content hash changed ranking derived from sanitized stored text")
	}
}

func overlapTaskNames(candidates []OverlapCandidate) string {
	names := make([]string, len(candidates))
	for i, c := range candidates {
		names[i] = c.Match.Task
	}
	return strings.Join(names, ", ")
}
