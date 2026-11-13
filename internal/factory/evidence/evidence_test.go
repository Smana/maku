// SPDX-License-Identifier: Apache-2.0

package evidence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The fixtures are scripts/shadow-merge-tally_test.sh's: the Go count must agree with the script
// on every one of them (stage 6), so the evidence page and the operator's tally never disagree.
func fixture[T any](t *testing.T, name string) T {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "testdata", "tally", name))
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var logins = Logins{Factory: "ogenki-agent-factory", Merger: "ogenki-agent-merger"}

const repo = "Smana/cloud-native-ref"

// shadow-merge-tally_test.sh's "decision record" block: --class docs-links on prs-decisions.json
// with reverts-drill.json.
func TestShadowAgreesWithTheTallyOnItsDecisionFixture(t *testing.T) {
	got := Shadow(fixture[[]PR](t, "prs-decisions.json"), fixture[[]Revert](t, "reverts-drill.json"), "docs-links", repo, logins)
	// --strictness: refused=3 refused_merged_unchanged=2, as the script's test says.
	want := ShadowTally{MergedUnchanged: 2, Changed: 1, Reverted: 1, Drill: 1, Refused: 3, RefusedMergedUnchanged: 2,
		Reviewers:    []Reviewer{{Model: "glm-5.3", MergedUnchanged: 2}, {Model: "kimi-k3", Changed: 1}},
		ByGeneration: []GenerationCount{{Generation: 4, MergedUnchanged: 1}, {Before: true, MergedUnchanged: 1, Changed: 1}},
		Judged:       []int{21, 22, 23, 29}, Drilled: []int{21}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// The adopted block: Renovate's pull requests, judged by WouldArmAdopted comments.
func TestShadowAgreesWithTheTallyOnTheAdoptedFixture(t *testing.T) {
	got := Shadow(fixture[[]PR](t, "prs-adopted.json"), nil, "deps-patch", repo, logins)
	if got.MergedUnchanged != 1 || got.Changed != 1 || got.Reverted != 0 || got.AutoMerged != 0 || got.Drill != 0 {
		t.Fatalf("%+v", got)
	}
}

// The script's first block, inline there: only the factory's head-pinned would-arm comments count,
// the newest decides, a merge at another head is changed, a merger's merge is auto_merged.
func TestShadowCountsOnlyTheFactorysHeadPinnedComments(t *testing.T) {
	wa := func(author, at, class, head string) Comment {
		ts, _ := time.Parse(time.RFC3339, at)
		return Comment{Author: Actor{Login: author}, CreatedAt: ts,
			Body: "would auto-merge: `" + class + "` <!-- agent-factory scope=x event=would-arm-1 head=" + head + " -->"}
	}
	pr := func(n int, head, by string, cs ...Comment) PR {
		return PR{Number: n, HeadRefOid: head, MergedBy: &Actor{Login: by}, Comments: cs}
	}
	prs := []PR{
		pr(1, "aaa", "Smana", wa("ogenki-agent-factory", "2026-10-01T00:00:00Z", "docs-links", "aaa")),
		pr(2, "bbb", "Smana", wa("app/ogenki-agent-factory", "2026-10-01T00:00:00Z", "docs-links", "aaa")),
		pr(3, "ccc", "Smana", wa("ogenki-agent-factory", "2026-10-01T00:00:00Z", "docs-links", "bbb"), wa("ogenki-agent-factory", "2026-10-02T00:00:00Z", "docs-links", "ccc")),
		pr(4, "ddd", "Smana", wa("someone", "2026-10-01T00:00:00Z", "docs-links", "ddd")),
		pr(5, "eee", "Smana", wa("ogenki-agent-factory", "2026-10-01T00:00:00Z", "review", "eee")),
		pr(6, "fff", "Smana", wa("ogenki-agent-factory", "2026-10-01T00:00:00Z", "docs-links", "fff")),
		pr(7, "abc", "ogenki-agent-merger", wa("ogenki-agent-factory", "2026-10-01T00:00:00Z", "docs-links", "abc")),
	}
	reverts := []Revert{{Body: "Reverts Smana/cloud-native-ref#6", State: "MERGED"},
		{Labels: []Label{{Name: "factory/revert-of:#1"}}, State: "CLOSED"}}
	got := Shadow(prs, reverts, "docs-links", repo, logins)
	if got.MergedUnchanged != 2 || got.Changed != 1 || got.Reverted != 1 || got.AutoMerged != 1 || !slices.Equal(got.Judged, []int{1, 2, 3, 6, 7}) {
		t.Fatalf("%+v", got)
	}
}

func outcomes(t *testing.T, fx string) OutcomeTally {
	t.Helper()
	name, rev := "prs.json", []Revert(nil)
	if fx != "" {
		name = "prs-" + fx + ".json"
	}
	if fx == "drill" {
		rev = fixture[[]Revert](t, "reverts-drill.json")
	}
	followed := map[int]bool{}
	if fx == "followup" {
		followed[100] = true // gh-stub.sh: only #100 has a follow-up
	}
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	return Outcomes(fixture[[]PR](t, name), rev, followed, repo, logins, now, 168*time.Hour)
}

// Offline proof 7 of the script's test: the same counts, fixture by fixture.
func TestOutcomesAgreeWithTheTally(t *testing.T) {
	for fx, want := range map[string]OutcomeTally{
		"":           {Merges: 31, Mature: 30, Conclusive: 29, Inconclusive: 1},
		"regression": {Merges: 31, Mature: 30, Conclusive: 29, Inconclusive: 1, Bad: 1, Regression: 1},
		"followup":   {Merges: 31, Mature: 30, Conclusive: 29, Inconclusive: 1, Bad: 1, FollowUp: 1},
		"regcomment": {Merges: 31, Mature: 30, Conclusive: 29, Inconclusive: 1, Bad: 1, Regression: 1},
		"drill":      {Merges: 3, Mature: 3, Conclusive: 2, Inconclusive: 1, Bad: 1, Reverted: 1, Drill: 1},
	} {
		t.Run(fx, func(t *testing.T) {
			got := outcomes(t, fx)
			got.Numbers, got.Drilled = nil, nil
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

// clopper-pearson.py's answers, to its six decimals, and the ladder the script's test proves.
func TestUpperBoundAgreesWithTheScript(t *testing.T) {
	for _, tc := range []struct {
		bad, n int
		want   string
	}{{0, 20, "0.139108"}, {0, 29, "0.098145"}, {0, 28, "0.101466"}, {1, 46, "0.099024"}, {2, 61, "0.099635"}, {3, 10, "0.606624"}} {
		if got := fmt.Sprintf("%.6f", UpperBound(tc.bad, tc.n)); got != tc.want {
			t.Errorf("UpperBound(%d, %d) = %s, want %s", tc.bad, tc.n, got, tc.want)
		}
	}
	if UpperBound(0, 0) != 1 {
		t.Fatal("nothing observed bounds nothing")
	}
}

// MP7: twenty merged unchanged at their heads, no bad mature outcome, a passed drill.
func TestEligibility(t *testing.T) {
	clean := func(n int) ShadowTally { return ShadowTally{MergedUnchanged: n, Judged: []int{21}} }
	drilled := []Revert{{Body: "Reverts " + repo + "#21", Labels: []Label{{Name: "factory/drill"}}, State: "MERGED"}}
	open := []Revert{{Body: "Reverts " + repo + "#21", Labels: []Label{{Name: "factory/drill"}}, State: "OPEN"}}
	for name, tc := range map[string]struct {
		sh       ShadowTally
		out      OutcomeTally
		reverts  []Revert
		min      int
		eligible bool
		blockers []string
		drill    string
	}{
		"twenty, clean, drilled":   {clean(20), OutcomeTally{}, drilled, 20, true, nil, DrillPassed},
		"nineteen":                 {clean(19), OutcomeTally{}, drilled, 20, false, []string{BlockBelowMinimum}, DrillPassed},
		"a raised minimum":         {clean(20), OutcomeTally{}, drilled, 29, false, []string{BlockBelowMinimum}, DrillPassed},
		"a bad mature outcome":     {clean(20), OutcomeTally{Bad: 1}, drilled, 20, false, []string{BlockBadOutcome}, DrillPassed},
		"no drill":                 {clean(20), OutcomeTally{}, nil, 20, false, []string{BlockNoDrill}, DrillNone},
		"a drill not yet merged":   {clean(20), OutcomeTally{}, open, 20, false, []string{BlockNoDrill}, DrillOpen},
		"everything short at once": {clean(3), OutcomeTally{Bad: 2}, nil, 20, false, []string{BlockBelowMinimum, BlockBadOutcome, BlockNoDrill}, DrillNone},
	} {
		t.Run(name, func(t *testing.T) {
			e := Evaluate(tc.sh, tc.out, tc.reverts, repo, tc.min)
			if e.Eligible != tc.eligible || !slices.Equal(e.Blockers, tc.blockers) || e.Drill != tc.drill || e.Minimum != tc.min {
				t.Fatalf("%+v", e)
			}
		})
	}
}

// The widening ladder (MP7: 29/46/61) is reported, never acted on: each step tolerates one more
// bad outcome among the mature, conclusive merges.
func TestTheWideningLadder(t *testing.T) {
	e := Evaluate(ShadowTally{}, OutcomeTally{Merges: 50, Mature: 47, Conclusive: 46, Inconclusive: 1, Bad: 1}, nil, repo, 20)
	want := []Step{{Merges: 29, MaxBad: 0}, {Merges: 46, MaxBad: 1, Reached: true}, {Merges: 61, MaxBad: 2}}
	if !slices.Equal(e.Ladder, want) || !e.Widen || e.Immature != 3 || e.Pending != 1 {
		t.Fatalf("%+v", e)
	}
	if e := Evaluate(ShadowTally{}, OutcomeTally{Conclusive: 28}, nil, repo, 20); e.Widen || e.Ladder[0].Reached {
		t.Fatalf("0 in 28 is not under 10%%: %+v", e)
	}
}

// Q6, PR13: the card splits shadow decisions by the policy generation they were made under; a
// record without repogen= was made before the binding, and is bucketed "before". Promotion still
// counts every one.
func TestShadowByGeneration(t *testing.T) {
	var prs []PR
	add := func(n int, gens string) {
		head := fmt.Sprintf("a%03x", n)
		prs = append(prs, PR{Number: n, HeadRefOid: head, MergedBy: &Actor{Login: "Smana"}, Comments: []Comment{{
			Author: Actor{Login: "ogenki-agent-factory"}, CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			Body: "would auto-merge: `dead-links` <!-- agent-factory scope=x event=would-arm-1 head=" + head + " -->\n\n" +
				"<!-- agent-factory-decision head=" + head + " class=dead-links decision=shadow:shadow_would_arm files=1/3 lines=2/20 shape=ok holdout=n/a oracle=n/a age=n/a evidence=ok paused=false capped=false" +
				gens + " reviewer=glm-5.3 -->"}}})
	}
	n := 0
	for range 3 {
		n++
		add(n, " policygen=4 repogen=7 resolved=0123456789ab")
	}
	for range 17 {
		n++
		add(n, " policygen=3 repogen=6 resolved=0123456789ab")
	}
	for range 12 {
		n++
		add(n, " policygen=2")
	}
	sh := Shadow(prs, nil, "dead-links", repo, logins)
	want := []GenerationCount{{Generation: 4, MergedUnchanged: 3}, {Generation: 3, MergedUnchanged: 17}, {Before: true, MergedUnchanged: 12}}
	if !slices.Equal(sh.ByGeneration, want) {
		t.Fatalf("by generation %+v, want %+v", sh.ByGeneration, want)
	}
	if e := Evaluate(sh, OutcomeTally{}, nil, repo, 30); e.Clean != 32 {
		t.Fatalf("clean %d, want 32: promotion counts every shadow decision (Q6)", e.Clean)
	}
	// The script's fixture: #22's record is a bound class's (policygen=4 repogen=7), the rest before.
	fx := Shadow(fixture[[]PR](t, "prs-decisions.json"), fixture[[]Revert](t, "reverts-drill.json"), "docs-links", repo, logins)
	if want := []GenerationCount{{Generation: 4, MergedUnchanged: 1}, {Before: true, MergedUnchanged: 1, Changed: 1}}; !slices.Equal(fx.ByGeneration, want) {
		t.Fatalf("fixture by generation %+v, want %+v", fx.ByGeneration, want)
	}
}
