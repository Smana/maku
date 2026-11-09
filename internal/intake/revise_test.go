// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/intake"
)

const (
	revisionBodyOne = "## What is wrong\nPréface 🙂\n[NEEDS CLARIFICATION: g1 which owner?]\n## What it should be\nThe owner is documented.\n## Acceptance check\n[NEEDS CLARIFICATION: g2 which check?]\n\n## Decisions\nD1 [g8]: keep the old decision"
	revisionBodyTwo = "[NEEDS CLARIFICATION: g3 what is in scope?]\n## What is wrong\nThe API is unclear.\n## What it should be\n[NEEDS CLARIFICATION: g4 which API?]\n## Acceptance check\nThe API contract is explicit.\n\n## Decisions\nD3: keep the scoped decision"
)

func revisionBase() intake.Plan {
	p := intake.Plan{
		APIVersion: intake.APIVersion,
		Round:      1,
		RoundsLeft: 9,
		Repository: "owner/repository",
		Budget:     intake.Budget{CeilingUSD: 4, Source: "class:docs-links.taskUSD"},
		Issues: []intake.Issue{
			{Index: 1, Title: "docs: owner is unclear", Body: revisionBodyOne, Class: "docs-links", Roles: []string{"scoper", "reviewer"}},
			{Index: 2, Title: "docs: API is unclear", Body: revisionBodyTwo, Class: "docs-links", Roles: []string{"scoper"}, DependsOn: []int{1}},
		},
		Gaps: []intake.Gap{
			{ID: "g1", Section: "what-is-wrong", Text: "which owner?", Recommended: intake.Answer{Answer: "docs team", Why: "they own it"}},
			{ID: "g2", Section: "acceptance-check", Text: "which check?", Recommended: intake.Answer{Answer: "task check", Why: "it is authoritative"}},
			{ID: "g3", Section: "scope", Text: "what is in scope?", Recommended: intake.Answer{Answer: "documentation only", Why: "smallest change"}},
			{ID: "g4", Section: "what-it-should-be", Text: "which API?", Recommended: intake.Answer{Answer: "existing API", Why: "compatibility"}},
		},
	}
	p = intake.WithGaps(p, p.Gaps)
	p.Hash = intake.Hash(p)
	return p
}

func revisionCandidate(base intake.Plan) intake.Plan {
	candidate := cloneRevisionPlan(base)
	candidate.Issues[0].Body = strings.Replace(candidate.Issues[0].Body,
		"[NEEDS CLARIFICATION: g2 which check?]", "Run `task check` ✅", 1)
	candidate.Issues[0].Body += "\nD4 [g2]: run task check, because it is authoritative"
	candidate.Issues[1].Body = strings.Replace(candidate.Issues[1].Body,
		"[NEEDS CLARIFICATION: g4 which API?]", "Keep the existing API.", 1)
	candidate.Issues[1].Body += "\nA5 [g4]: assumed existing API, because compatibility"
	candidate.Gaps = []intake.Gap{{ID: "forged", Text: "display input is not trusted"}}
	candidate.Question = nil
	return candidate
}

func cloneRevisionPlan(p intake.Plan) intake.Plan {
	p.Issues = slices.Clone(p.Issues)
	for i := range p.Issues {
		p.Issues[i].Roles = slices.Clone(p.Issues[i].Roles)
		p.Issues[i].DependsOn = slices.Clone(p.Issues[i].DependsOn)
	}
	p.Gaps = slices.Clone(p.Gaps)
	return p
}

func requireRevisionRefusal(t *testing.T, err error, reason string) {
	t.Helper()
	var refusal *intake.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != reason {
		t.Fatalf("error = %v, want refusal %q", err, reason)
	}
}

func TestRevise(t *testing.T) {
	base := revisionBase()
	base.Round = 0 // Legacy plans have no round and start at round one.
	candidate := revisionCandidate(base)

	got, err := intake.Revise(base, intake.Hash(base), candidate, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 2 || got.RoundsLeft != 8 {
		t.Fatalf("rounds = %d/%d, want 2/8", got.Round, got.RoundsLeft)
	}
	if got.Hash == intake.Hash(base) || got.Hash != intake.Hash(got) {
		t.Fatalf("candidate hash not renewed: %+v", got)
	}
	for i := range candidate.Issues {
		if got.Issues[i].Body != candidate.Issues[i].Body {
			t.Errorf("issue %d body changed during verification\n got: %q\nwant: %q", i+1, got.Issues[i].Body, candidate.Issues[i].Body)
		}
	}
	wantGaps := []intake.Gap{base.Gaps[0], base.Gaps[2]}
	if !slices.Equal(got.Gaps, wantGaps) {
		t.Fatalf("remaining gaps = %+v, want %+v", got.Gaps, wantGaps)
	}
	if got.Question == nil || got.Question.Text != "which owner?" {
		t.Fatalf("first remaining gap did not map to Question: %+v", got.Question)
	}

	recordsOne, err := sanitize.DecisionRecords(got.Issues[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	wantOne := []sanitize.DecisionRecord{
		{Kind: "D", Number: 1, Gap: "g8", Text: "keep the old decision"},
		{Kind: "D", Number: 4, Gap: "g2", Text: "run task check, because it is authoritative"},
	}
	if !slices.Equal(recordsOne, wantOne) {
		t.Fatalf("issue one records = %+v, want %+v", recordsOne, wantOne)
	}
	recordsTwo, err := sanitize.DecisionRecords(got.Issues[1].Body)
	if err != nil {
		t.Fatal(err)
	}
	wantTwo := []sanitize.DecisionRecord{
		{Kind: "D", Number: 3, Text: "keep the scoped decision"},
		{Kind: "A", Number: 5, Gap: "g4", Text: "assumed existing API, because compatibility"},
	}
	if !slices.Equal(recordsTwo, wantTwo) {
		t.Fatalf("issue two records = %+v, want %+v", recordsTwo, wantTwo)
	}

	t.Run("later rounds derive from the verified base", func(t *testing.T) {
		base := revisionBase()
		base.Round = 4
		got, err := intake.Revise(base, intake.Hash(base), revisionCandidate(base), 10)
		if err != nil {
			t.Fatal(err)
		}
		if got.Round != 5 || got.RoundsLeft != 5 {
			t.Fatalf("rounds = %d/%d, want 5/5", got.Round, got.RoundsLeft)
		}
	})
}

func TestReviseRejects(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*intake.Plan)
	}{
		{name: "api version", mutate: func(p *intake.Plan) { p.APIVersion = "plan/v2" }},
		{name: "repository", mutate: func(p *intake.Plan) { p.Repository = "owner/other" }},
		{name: "budget", mutate: func(p *intake.Plan) { p.Budget.CeilingUSD++ }},
		{name: "issue index", mutate: func(p *intake.Plan) { p.Issues[0].Index = 2 }},
		{name: "issue title", mutate: func(p *intake.Plan) { p.Issues[0].Title = "docs: rewritten" }},
		{name: "issue class", mutate: func(p *intake.Plan) { p.Issues[0].Class = "other" }},
		{name: "issue roles", mutate: func(p *intake.Plan) { p.Issues[0].Roles = []string{"admin"} }},
		{name: "dependencies", mutate: func(p *intake.Plan) { p.Issues[1].DependsOn = nil }},
		{name: "issue order", mutate: func(p *intake.Plan) { p.Issues[0], p.Issues[1] = p.Issues[1], p.Issues[0] }},
		{name: "non-marker body text", mutate: func(p *intake.Plan) { p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "Préface", "Rewrite", 1) }},
		{name: "empty replacement", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "Run `task check` ✅", " \t ", 1)
		}},
		{name: "remaining marker rewritten", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "which owner?", "rewritten?", 1)
		}},
		{name: "new marker", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "Run `task check` ✅", "[NEEDS CLARIFICATION: g9 new?]", 1)
		}},
		{name: "malformed marker", mutate: func(p *intake.Plan) { p.Issues[0].Body += "\n[NEEDS CLARIFICATION: g0 bad?]" }},
		{name: "old decision rewritten", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "keep the old decision", "rewrite history", 1)
		}},
		{name: "old decision removed", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "D1 [g8]: keep the old decision\n", "", 1)
		}},
		{name: "decision section removed", mutate: func(p *intake.Plan) { p.Issues[0].Body = strings.Split(p.Issues[0].Body, "\n\n## Decisions")[0] }},
		{name: "blank-only decision append", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.TrimSuffix(p.Issues[0].Body, "\nD4 [g2]: run task check, because it is authoritative") + "\n"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := revisionBase()
			candidate := revisionCandidate(base)
			test.mutate(&candidate)
			_, err := intake.Revise(base, intake.Hash(base), candidate, 10)
			requireRevisionRefusal(t, err, intake.ReasonPlannerInvalid)
		})
	}

	t.Run("base hash precedes candidate validation", func(t *testing.T) {
		base := revisionBase()
		candidate := revisionCandidate(base)
		candidate.Repository = "outside/repository"
		_, err := intake.Revise(base, "sha256:stale", candidate, 10)
		requireRevisionRefusal(t, err, intake.ReasonPlanChanged)
	})

	for _, test := range []struct {
		name      string
		baseRound int
		maxRounds int
	}{
		{name: "above cap", baseRound: 10, maxRounds: 10},
		{name: "legacy base cannot revise when cap is one", baseRound: 0, maxRounds: 1},
		{name: "negative base round", baseRound: -1, maxRounds: 10},
		{name: "invalid cap", baseRound: 1, maxRounds: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := revisionBase()
			base.Round = test.baseRound
			_, err := intake.Revise(base, intake.Hash(base), revisionCandidate(base), test.maxRounds)
			requireRevisionRefusal(t, err, intake.ReasonPlannerInvalid)
		})
	}
}

func TestReviseBodyBounds(t *testing.T) {
	t.Run("base body over 16 KiB", func(t *testing.T) {
		base := revisionBase()
		base.Issues[0].Body += strings.Repeat("\n", 16<<10)
		candidate := cloneRevisionPlan(base)
		_, err := intake.Revise(base, intake.Hash(base), candidate, 10)
		requireRevisionRefusal(t, err, intake.ReasonPlannerInvalid)
		if !strings.Contains(err.Error(), "base issue 1 body") || !strings.Contains(err.Error(), "16 KiB") {
			t.Fatalf("error = %v, want the base body bound", err)
		}
	})

	t.Run("long candidate newline run is rejected cheaply", func(t *testing.T) {
		base := intake.Plan{
			APIVersion: intake.APIVersion,
			Round:      1,
			Repository: "owner/repository",
			Budget:     intake.Budget{CeilingUSD: 2, Source: "class"},
			Issues: []intake.Issue{{
				Index: 1, Title: "docs: clarify behavior", Class: "docs-links", Roles: []string{"scoper"},
				Body: "## What is wrong\nProtected prefix.\n[NEEDS CLARIFICATION: g1 owner?]\n" +
					"## What it should be\nDocumented.\n## Acceptance check\nDone.",
			}},
			Gaps: []intake.Gap{{
				ID: "g1", Section: "what-is-wrong", Text: "owner?",
				Recommended: intake.Answer{Answer: "docs team", Why: "ownership"},
			}},
		}
		base = intake.WithGaps(base, base.Gaps)
		candidate := cloneRevisionPlan(base)
		candidate.Issues[0].Body = "## What is wrong\nChanged prefix.\nChosen answer.\n" +
			"## What it should be\nDocumented.\n## Acceptance check\nDone." +
			strings.Repeat("\n", 64<<10) + "## Decisions\nD1 [g1]: chosen answer, because ownership"

		start := time.Now()
		_, err := intake.Revise(base, intake.Hash(base), candidate, 10)
		elapsed := time.Since(start)
		requireRevisionRefusal(t, err, intake.ReasonPlannerInvalid)
		if elapsed > 250*time.Millisecond {
			t.Fatalf("oversize newline run took %s before rejection", elapsed)
		}
		if !strings.Contains(err.Error(), "candidate issue 1 body") || !strings.Contains(err.Error(), "16 KiB") {
			t.Fatalf("error = %v, want the candidate body bound", err)
		}
	})
}

func TestReviseCorrelation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*intake.Plan)
	}{
		{name: "missing record", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "\nD4 [g2]: run task check, because it is authoritative", "", 1)
		}},
		{name: "duplicate record for one marker", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "D4 [g2]:", "D4 [g2]: first\nD5 [g2]:", 1)
		}},
		{name: "record names another gap", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "D4 [g2]:", "D4 [g1]:", 1)
		}},
		{name: "record without gap", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "D4 [g2]:", "D4:", 1)
		}},
		{name: "record in another issue", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "\nD4 [g2]: run task check, because it is authoritative", "", 1)
			p.Issues[1].Body = strings.Replace(p.Issues[1].Body, "A5 [g4]:", "D4 [g2]: moved\nA5 [g4]:", 1)
		}},
		{name: "record without a resolved marker", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "Run `task check` ✅", "[NEEDS CLARIFICATION: g2 which check?]", 1)
		}},
		{name: "non-global record number", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "D4 [g2]:", "D9 [g2]:", 1)
		}},
		{name: "second issue must continue global sequence", mutate: func(p *intake.Plan) {
			p.Issues[1].Body = strings.Replace(p.Issues[1].Body, "A5 [g4]:", "A4 [g4]:", 1)
		}},
		{name: "unrelated extra record", mutate: func(p *intake.Plan) {
			p.Issues[0].Body += "\nD6 [g9]: unrelated"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := revisionBase()
			candidate := revisionCandidate(base)
			test.mutate(&candidate)
			_, err := intake.Revise(base, intake.Hash(base), candidate, 10)
			requireRevisionRefusal(t, err, intake.ReasonPlannerInvalid)
		})
	}

	t.Run("base gap and marker correspondence is required", func(t *testing.T) {
		base := revisionBase()
		base.Gaps = base.Gaps[1:]
		_, err := intake.Revise(base, intake.Hash(base), revisionCandidate(base), 10)
		requireRevisionRefusal(t, err, intake.ReasonPlannerInvalid)
	})
}

func TestReviseCapAcceptsRemaining(t *testing.T) {
	body := "## What is wrong\n[NEEDS CLARIFICATION: g1 owner?]\n## What it should be\n[NEEDS CLARIFICATION: g2 behavior?]\n## Acceptance check\n[NEEDS CLARIFICATION: g3 check?]"
	base := intake.Plan{
		APIVersion: intake.APIVersion,
		Round:      9,
		Repository: "owner/repository",
		Budget:     intake.Budget{CeilingUSD: 2, Source: "class"},
		Issues:     []intake.Issue{{Index: 1, Title: "docs: clarify behavior", Body: body, Class: "docs-links", Roles: []string{"scoper"}}},
		Gaps: []intake.Gap{
			{ID: "g1", Section: "what-is-wrong", Text: "owner?", Recommended: intake.Answer{Answer: "docs team", Why: "ownership"}},
			{ID: "g2", Section: "what-it-should-be", Text: "behavior?", Recommended: intake.Answer{Answer: "use β", Why: "compatibility"}},
			{ID: "g3", Section: "acceptance-check", Text: "check?", Recommended: intake.Answer{Answer: "run nightly", Why: "coverage"}},
		},
	}
	base = intake.WithGaps(base, base.Gaps)
	candidate := cloneRevisionPlan(base)
	candidate.Issues[0].Body = strings.Replace(body, "[NEEDS CLARIFICATION: g1 owner?]", "Chosen answer.", 1) +
		"\n\n## Decisions\nD1 [g1]: chosen answer, because the person chose it"
	candidateBefore := candidate.Issues[0].Body

	got, err := intake.Revise(base, intake.Hash(base), candidate, 10)
	if err != nil {
		t.Fatal(err)
	}
	wantBody := "## What is wrong\nChosen answer.\n## What it should be\nuse β\n## Acceptance check\nrun nightly\n\n## Decisions\n" +
		"D1 [g1]: chosen answer, because the person chose it\n" +
		"A2 [g2]: assumed use β, because compatibility\n" +
		"A3 [g3]: assumed run nightly, because coverage"
	if got.Issues[0].Body != wantBody {
		t.Fatalf("cap body\n got: %q\nwant: %q", got.Issues[0].Body, wantBody)
	}
	if candidate.Issues[0].Body != candidateBefore {
		t.Fatal("Revise mutated its candidate at the cap")
	}
	if got.Round != 10 || got.RoundsLeft != 0 || len(got.Gaps) != 0 || got.Question != nil {
		t.Fatalf("cap metadata = round %d, left %d, gaps %+v, question %+v", got.Round, got.RoundsLeft, got.Gaps, got.Question)
	}
	if got.Hash != intake.Hash(got) {
		t.Fatal("cap result was not rehashed")
	}

	t.Run("cap continues numbering across split bodies", func(t *testing.T) {
		base := revisionBase()
		base.Round = 9
		got, err := intake.Revise(base, intake.Hash(base), revisionCandidate(base), 10)
		if err != nil {
			t.Fatal(err)
		}
		first, err := sanitize.DecisionRecords(got.Issues[0].Body)
		if err != nil {
			t.Fatal(err)
		}
		second, err := sanitize.DecisionRecords(got.Issues[1].Body)
		if err != nil {
			t.Fatal(err)
		}
		if len(first) != 3 || first[2].Kind != "A" || first[2].Number != 6 || first[2].Gap != "g1" {
			t.Fatalf("first body's cap records = %+v", first)
		}
		if len(second) != 3 || second[2].Kind != "A" || second[2].Number != 7 || second[2].Gap != "g3" {
			t.Fatalf("second body's cap records = %+v", second)
		}
		if len(got.Gaps) != 0 || strings.Contains(got.Issues[0].Body+got.Issues[1].Body, "NEEDS CLARIFICATION") {
			t.Fatal("split-body cap result retains a clarification")
		}
	})

	t.Run("no candidate answers still accepts every recommendation", func(t *testing.T) {
		got, err := intake.Revise(base, intake.Hash(base), cloneRevisionPlan(base), 10)
		if err != nil {
			t.Fatal(err)
		}
		records, err := sanitize.DecisionRecords(got.Issues[0].Body)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 3 || records[0].Kind != "A" || records[0].Number != 1 || records[2].Number != 3 {
			t.Fatalf("automatic assumptions = %+v", records)
		}
		for _, marker := range []string{"g1", "g2", "g3"} {
			if strings.Contains(got.Issues[0].Body, "NEEDS CLARIFICATION: "+marker) {
				t.Errorf("%s remains at the cap", marker)
			}
		}
	})
}

func TestCheckCarryForward(t *testing.T) {
	base := revisionBase()
	candidate := revisionCandidate(base)
	next := cloneRevisionPlan(candidate)
	next.Issues[0].Body = strings.Replace(next.Issues[0].Body, "Préface 🙂", "The model may rewrite unsettled prose.", 1)
	next.Issues[0].Body = strings.Replace(next.Issues[0].Body, "which owner?", "a refined remaining question?", 1)
	if refusal := intake.CheckCarryForward(base, candidate, next); refusal != nil {
		t.Fatalf("allowed model re-plan refused: %v", refusal)
	}

	for _, test := range []struct {
		name   string
		mutate func(*intake.Plan)
	}{
		{name: "settled replacement dropped", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "Run `task check` ✅", "task check", 1)
		}},
		{name: "replacement hidden in a new decision", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "Run `task check` ✅", "rewritten away", 1)
			p.Issues[0].Body += "\nD6 [g9]: Run `task check` ✅"
		}},
		{name: "second issue replacement rewritten", mutate: func(p *intake.Plan) {
			p.Issues[1].Body = strings.Replace(p.Issues[1].Body, "Keep the existing API.", "Use a new API.", 1)
		}},
		{name: "base decision dropped", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "D1 [g8]: keep the old decision\n", "", 1)
		}},
		{name: "candidate decision rewritten", mutate: func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "because it is authoritative", "because the model says so", 1)
		}},
		{name: "candidate assumption dropped", mutate: func(p *intake.Plan) {
			p.Issues[1].Body = strings.Replace(p.Issues[1].Body, "\nA5 [g4]: assumed existing API, because compatibility", "", 1)
		}},
		{name: "record moved to another issue", mutate: func(p *intake.Plan) {
			p.Issues[1].Body = strings.Replace(p.Issues[1].Body, "\nA5 [g4]: assumed existing API, because compatibility", "", 1)
			p.Issues[0].Body += "\nA5 [g4]: assumed existing API, because compatibility"
		}},
		{name: "settled issue dropped", mutate: func(p *intake.Plan) { p.Issues = p.Issues[:1] }},
		{name: "next decisions malformed", mutate: func(p *intake.Plan) { p.Issues[0].Body += "\nnot a record" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := cloneRevisionPlan(candidate)
			test.mutate(&next)
			if refusal := intake.CheckCarryForward(base, candidate, next); refusal == nil || refusal.Reason != intake.ReasonPlannerInvalid {
				t.Fatalf("refusal = %v, want planner_invalid", refusal)
			}
		})
	}
}
