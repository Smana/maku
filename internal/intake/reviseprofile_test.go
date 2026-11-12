// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/intake"
)

// finalBase is a plan with every gap answered: the only plan a profile is pinned on.
func finalBase() intake.Plan {
	p := intake.Plan{
		APIVersion: intake.APIVersion,
		Rev:        3,
		Round:      2,
		RoundsLeft: 8,
		Repository: "owner/repository",
		Budget:     intake.Budget{CeilingUSD: 8, Source: "catalogue:review.ceilingUSD"},
		Issues: []intake.Issue{
			{Index: 1, Title: "docs: owner is unclear", Class: "review", Roles: []string{},
				Body:       "## What is wrong\nThe owner is unclear.\n## What it should be\nThe owner is documented.\n## Acceptance check\nThe owner is named.\n\n## Decisions\nD2 [g1]: docs team",
				Suggestion: &intake.Suggestion{Profile: "review-frontier", Why: "several files"}},
			{Index: 2, Title: "docs: API is unclear", Class: "review", Roles: []string{}, DependsOn: []int{1},
				Body: "## What is wrong\nThe API is unclear.\n## What it should be\nThe API is explicit.\n## Acceptance check\nThe API contract is explicit."},
		},
	}
	p.Hash = intake.Hash(p)
	return p
}

// Review Focus 2: a profile revision changes nothing but issues[].profile and the one record it
// appends; rounds, gaps and every other material field stay as the base had them.
func TestReviseProfileOnly(t *testing.T) {
	base := finalBase()
	candidate := cloneRevisionPlan(base)
	candidate.Issues[1].Profile = "review-frontier"
	candidate.Round, candidate.Gaps = 99, []intake.Gap{{ID: "forged"}} // display input is not trusted

	got, err := intake.Revise(base, base.Hash, candidate, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Issues[1].Profile != "review-frontier" || got.Issues[0].Profile != "" {
		t.Fatalf("profiles = %q, %q", got.Issues[0].Profile, got.Issues[1].Profile)
	}
	if got.Round != base.Round || got.RoundsLeft != base.RoundsLeft || got.Rev != base.Rev || len(got.Gaps) != 0 {
		t.Fatalf("display changed: %+v", got)
	}
	if got.Issues[0].Body != base.Issues[0].Body {
		t.Fatalf("unpinned issue body changed: %q", got.Issues[0].Body)
	}
	wantBody := base.Issues[1].Body + "\n\n## Decisions\nD3: profile: review-frontier, pinned by the developer"
	if got.Issues[1].Body != wantBody {
		t.Fatalf("body = %q\nwant %q", got.Issues[1].Body, wantBody)
	}
	for i := range got.Issues {
		b, g := base.Issues[i], got.Issues[i]
		if g.Title != b.Title || g.Class != b.Class || !slices.Equal(g.Roles, b.Roles) || !slices.Equal(g.DependsOn, b.DependsOn) {
			t.Fatalf("issue %d material changed: %+v", i+1, g)
		}
	}
	if got.Hash == base.Hash || got.Hash != intake.Hash(got) {
		t.Fatalf("hash not renewed: %s", got.Hash)
	}
	if r := intake.CheckFinal(got, []string{"owner/repository"}, map[string]intake.Class{"review": {}}); r != nil {
		t.Fatalf("pinned plan does not hold CheckFinal: %v", r)
	}

	t.Run("back to the factory's choice is a decision too", func(t *testing.T) {
		again := cloneRevisionPlan(got)
		again.Issues[1].Profile = ""
		next, err := intake.Revise(got, got.Hash, again, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(next.Issues[1].Body, "\nD4: profile: factory's choice, chosen by the developer") {
			t.Fatalf("body = %q", next.Issues[1].Body)
		}
	})
}

// A profile revision is a profile revision only: any other edit beside it is refused whole, and a
// profile is pinned only once every gap is answered.
func TestReviseProfileRejectsOtherEdits(t *testing.T) {
	for name, mutate := range map[string]func(*intake.Plan){
		"title":    func(p *intake.Plan) { p.Issues[0].Title = "docs: rewritten" },
		"body":     func(p *intake.Plan) { p.Issues[0].Body += "\nD3: sneaked in" },
		"class":    func(p *intake.Plan) { p.Issues[0].Class = "docs-links" },
		"roles":    func(p *intake.Plan) { p.Issues[0].Roles = []string{"implementer"} },
		"bad name": func(p *intake.Plan) { p.Issues[0].Profile = "Review Frontier\n## Decisions" },
	} {
		t.Run(name, func(t *testing.T) {
			base := finalBase()
			candidate := cloneRevisionPlan(base)
			candidate.Issues[0].Profile = "review-frontier"
			mutate(&candidate)
			_, err := intake.Revise(base, base.Hash, candidate, 10)
			requireRevisionRefusal(t, err, intake.ReasonPlannerInvalid)
		})
	}
	t.Run("gaps left", func(t *testing.T) {
		base := revisionBase()
		candidate := cloneRevisionPlan(base)
		candidate.Issues[0].Profile = "review-frontier"
		_, err := intake.Revise(base, base.Hash, candidate, 10)
		requireRevisionRefusal(t, err, intake.ReasonPlannerInvalid)
	})
}

// A kept factory's choice with a named suggestion is written down as an Assumption, once, in the
// closed record grammar; a pinned issue and a plan without a suggestion gain nothing.
func TestProfileRecord(t *testing.T) {
	base := finalBase()
	got := intake.RecordProfiles(base)
	want := base.Issues[0].Body + "\nA3: profile: factory's choice; suggested review-frontier"
	if got.Issues[0].Body != want {
		t.Fatalf("body = %q\nwant %q", got.Issues[0].Body, want)
	}
	if got.Issues[1].Body != base.Issues[1].Body {
		t.Fatalf("an issue without a suggestion gained a record: %q", got.Issues[1].Body)
	}
	if got.Hash != intake.Hash(got) || got.Hash == base.Hash {
		t.Fatalf("hash = %s", got.Hash)
	}
	if again := intake.RecordProfiles(got); again.Hash != got.Hash {
		t.Fatal("RecordProfiles is not idempotent")
	}
	for _, is := range got.Issues {
		if _, err := sanitize.CheckFinalDraft(is.Title, is.Body); err != nil {
			t.Fatalf("record grammar: %v", err)
		}
	}

	pinned := cloneRevisionPlan(base)
	pinned.Issues[0].Profile = "review-frontier"
	pinned.Hash = intake.Hash(pinned)
	if got := intake.RecordProfiles(pinned); got.Hash != pinned.Hash {
		t.Fatal("a pinned issue gained an Assumption")
	}
	if !intake.IsProfileRevision(base, pinned) || intake.IsProfileRevision(base, base) {
		t.Fatal("IsProfileRevision")
	}
}
