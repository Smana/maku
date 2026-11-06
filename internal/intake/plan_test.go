// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/intake"
)

// planFixture is a golden plan, decoded: the same bytes every task in this package reads, and this
// file is the only one that knows where they live.
func planFixture(t *testing.T, name string) intake.Plan {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "plan", name+".json"))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var p intake.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return p
}

// classes is the one-row class table the broker's config would carry for a docs repository: the
// roles and the ceiling a plan takes from config, never from a model.
func classes() map[string]intake.Class {
	return map[string]intake.Class{
		"docs-links": {Roles: []string{"scoper", "implementer", "reviewer"}, CeilingUSD: 2, Source: "class:docs-links.taskUSD"},
	}
}

// CD27: the hash covers every material field and nothing else. A display-only change keeps it.
func TestPlanHashCoversTheMaterialSetOnly(t *testing.T) {
	base := planFixture(t, "single")
	m := intake.Hash(base)
	for name, change := range map[string]func(*intake.Plan){
		"apiVersion": func(p *intake.Plan) { p.APIVersion = "plan/v2" },
		"repository": func(p *intake.Plan) { p.Repository = "owner/other" },
		"ceiling":    func(p *intake.Plan) { p.Budget.CeilingUSD = 2.5 },
		"budget src": func(p *intake.Plan) { p.Budget.Source = "channel:C1.maxCostUSD" },
		"index":      func(p *intake.Plan) { p.Issues[0].Index = 2 },
		"title":      func(p *intake.Plan) { p.Issues[0].Title = "docs: another" },
		"body":       func(p *intake.Plan) { p.Issues[0].Body += "\n" },
		"acceptance": func(p *intake.Plan) {
			p.Issues[0].Body = strings.Replace(p.Issues[0].Body, "## Acceptance check", "## Acceptance check\n- one", 1)
		},
		"class":     func(p *intake.Plan) { p.Issues[0].Class = "docs" },
		"roles":     func(p *intake.Plan) { p.Issues[0].Roles = []string{"implementer", "reviewer"} },
		"dependsOn": func(p *intake.Plan) { p.Issues[0].DependsOn = []int{1} },
	} {
		p := base
		p.Issues = slices.Clone(base.Issues)
		change(&p)
		if intake.Hash(p) == m {
			t.Errorf("%s: the hash did not move", name)
		}
	}
	// Display, and only display: a promise about what will be filed is unaffected.
	for name, change := range map[string]func(*intake.Plan){
		"evidence": func(p *intake.Plan) {
			p.RepositoryEvidence = append(p.RepositoryEvidence, intake.Evidence{Kind: "path", Text: "a.md"})
		},
		"alternative": func(p *intake.Plan) {
			p.Alternatives = []intake.Alternative{{Repository: "o/d", Why: "also serves"}}
		},
		"rev": func(p *intake.Plan) { p.Rev = 3 },
		"question": func(p *intake.Plan) {
			p.Question = &intake.Question{Text: "which?", Recommended: intake.Answer{Answer: "a", Why: "b"}}
		},
		"planner at": func(p *intake.Plan) { p.Planner.At = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC) },
		"hash field": func(p *intake.Plan) { p.Hash = "sha256:00" },
	} {
		p := base
		p.Issues = slices.Clone(base.Issues)
		change(&p)
		if intake.Hash(p) != m {
			t.Errorf("%s: the hash moved on a field the design does not hash", name)
		}
	}
}

// The canonical bytes are stable, and the ceiling is an integer: 2000000 micro-dollars for $2.00,
// never `2.0000000000000004` or `2` — the deliberate deviation from the spec's `ceilingUSD`.
func TestPlanHashIsStable(t *testing.T) {
	p := planFixture(t, "three")
	for i := 0; i < 5; i++ {
		if intake.Hash(p) != intake.Hash(planFixture(t, "three")) {
			t.Fatal("unstable")
		}
	}
	m, err := intake.Material(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"apiVersion":"plan/v1","repository":"Smana/ai-native-ref","budget":{"ceilingMicroUSD":2000000,"source":"class:docs-links.taskUSD"},"issues":[{"index":1,`
	if !strings.HasPrefix(string(m), want) {
		t.Fatalf("%s", m)
	}
	if !strings.Contains(string(m), `"roles":[`) || strings.Index(string(m), `"dependsOn"`) < strings.Index(string(m), `"roles"`) {
		t.Fatalf("field order: %s", m)
	}
}

// Review Focus 1: a repository outside the candidate set is refused whole, and the outsider is
// never named in the refusal.
func TestPlanRefusesARepositoryOutsideTheCandidates(t *testing.T) {
	p := planFixture(t, "single")
	p.Repository = "owner/secret-repo"
	if r := intake.Check(p, []string{"Smana/ai-native-ref"}, classes()); r == nil || r.Reason != intake.ReasonRepositoryNotInCandidates {
		t.Fatalf("%+v", r)
	} else if strings.Contains(r.Sentence, "secret-repo") {
		t.Fatalf("the refusal names the outsider: %s", r.Sentence)
	}
}

// Every rule Check applies, one row each, in the order Check applies them: a rule that stops firing,
// or that fires on a plan that holds, is a plan filed under no rule at all.
func TestCheckRefusesEachRule(t *testing.T) {
	const accepted = "Smana/ai-native-ref"
	for _, c := range []struct {
		name string
		// candidates overrides the accepted set; the zero value asks for the one repository.
		candidates []string
		change     func(*intake.Plan)
		// want is the refusal's reason; empty means the plan holds.
		want string
		// detail is a fragment of the diagnostic a reader must see: the field and the bound.
		detail string
		// forbidden must appear in no sentence: Check never echoes what it refused to plan for.
		forbidden string
	}{
		{name: "the fixture holds"},
		{name: "a candidate in another case is the same repository",
			candidates: []string{"smana/AI-NATIVE-REF"}},
		{name: "apiVersion",
			change: func(p *intake.Plan) { p.APIVersion = "plan/v0" },
			want:   intake.ReasonPlannerInvalid, detail: `apiVersion "plan/v0", the contract's is plan/v1`},
		{name: "no issues",
			change: func(p *intake.Plan) { p.Issues = nil },
			want:   intake.ReasonPlannerInvalid, detail: "issues: 0, the contract asks for 1 to 3"},
		{name: "four issues",
			change: func(p *intake.Plan) {
				p.Issues = append(slices.Clone(p.Issues), p.Issues[0], p.Issues[0], p.Issues[0])
			},
			want: intake.ReasonPlannerInvalid, detail: "issues: 4, the contract asks for 1 to 3"},
		{name: "a draft CheckDraft refuses, verbatim",
			change: func(p *intake.Plan) { p.Issues[0].Title = "no area prefix" },
			want:   intake.ReasonInvalidDraft, detail: "invalid_draft: the title is one line"},
		{name: "a class the config does not define",
			change: func(p *intake.Plan) { p.Issues[0].Class = "admin" },
			want:   intake.ReasonInvalidClass, detail: `issue 1: class "admin" is not in the class table`},
		{name: "six evidence items",
			change: func(p *intake.Plan) {
				p.RepositoryEvidence = slices.Repeat([]intake.Evidence{{Kind: "path", Text: "README.md"}}, 6)
			},
			want: intake.ReasonPlanTooLarge, detail: "repositoryEvidence: 6 items, the bound is 5"},
		{name: "an evidence item over 280 runes",
			change: func(p *intake.Plan) {
				// 281 runes and 562 bytes: the bound counts what a reader sees, not what it costs.
				p.RepositoryEvidence = []intake.Evidence{{Kind: "thread", Text: strings.Repeat("é", 281)}}
			},
			want: intake.ReasonPlanTooLarge, detail: "repositoryEvidence[0].text: 281 characters, the bound is 280"},
		{name: "a question with nothing to answer",
			change: func(p *intake.Plan) { p.Question = &intake.Question{Text: "which repository?"} },
			want:   intake.ReasonPlannerInvalid, detail: "question: it carries text and a recommended answer"},
		{name: "a repository outside the candidates",
			change: func(p *intake.Plan) { p.Repository = "owner/secret-repo" },
			want:   intake.ReasonRepositoryNotInCandidates, detail: "outside the ones this request may use",
			forbidden: "secret-repo"},
		{name: "an alternative outside the candidates",
			change: func(p *intake.Plan) {
				p.Alternatives = []intake.Alternative{{Repository: "owner/secret-repo", Why: "also fits"}}
			},
			want: intake.ReasonRepositoryNotInCandidates, detail: "outside the ones this request may use",
			forbidden: "secret-repo"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := planFixture(t, "single")
			p.Issues = slices.Clone(p.Issues)
			if c.change != nil {
				c.change(&p)
			}
			candidates := c.candidates
			if candidates == nil {
				candidates = []string{accepted}
			}
			r := intake.Check(p, candidates, classes())
			if c.want == "" {
				if r != nil {
					t.Fatalf("a plan that holds was refused: %+v", r)
				}
				return
			}
			if r == nil || r.Reason != c.want {
				t.Fatalf("%s: %+v", c.want, r)
			}
			if !strings.Contains(r.Sentence, c.detail) {
				t.Fatalf("the diagnostic does not name %q: %s", c.detail, r.Sentence)
			}
			if strings.Contains(r.Sentence, "%") {
				t.Fatalf("an unfilled or broken format reached the reader: %s", r.Sentence)
			}
			if c.forbidden != "" && strings.Contains(r.Error(), c.forbidden) {
				t.Fatalf("the refusal echoes %q: %s", c.forbidden, r.Error())
			}
			if c.want == intake.ReasonInvalidDraft {
				// The one message that is not this package's: CheckDraft's own, shown once.
				if _, err := sanitize.CheckDraft(p.Issues[0].Title, p.Issues[0].Body); err == nil || r.Sentence != err.Error() || r.Error() != r.Sentence {
					t.Fatalf("not the validator's message, verbatim: %q", r.Error())
				}
				return
			}
			if !strings.HasPrefix(r.Error(), c.want+": ") {
				t.Fatalf("the error does not open with its reason: %q", r.Error())
			}
		})
	}
}

// The closed list is closed: one sentence per reason, and no sentence a reader sees carries a
// placeholder. A reason whose sentence has a diagnostic slot is never emitted without one — "(%s)"
// in a person's line is the same defect as a reason nobody can label.
func TestNoRefusalSentenceEmitsAPlaceholder(t *testing.T) {
	sentences := intake.Sentences()
	declared := []string{
		intake.ReasonPrivateRepository, intake.ReasonRepositoryNotAllowed, intake.ReasonNoWrite,
		intake.ReasonInvalidDraft, intake.ReasonPlannerInvalid, intake.ReasonPlannerUnavailable,
		intake.ReasonPlannerTimeout, intake.ReasonPlanChanged, intake.ReasonRepositoryUnverified,
		intake.ReasonRepositoryNotInCandidates, intake.ReasonInvalidClass, intake.ReasonNotLinked,
		intake.ReasonDailyPlansReached, intake.ReasonPlanTooLarge,
	}
	for _, reason := range declared {
		s, ok := sentences[reason]
		if !ok {
			t.Fatalf("%s: declared with no sentence", reason)
		}
		if !strings.Contains(s, "%s") {
			got := intake.RefusalFor(reason, "").Sentence
			if strings.Contains(got, "%") || got != s {
				t.Fatalf("%s: %q", reason, got)
			}
			continue
		}
		// A diagnostic slot: filled, it leaves no placeholder; unfilled, the emitter refuses to emit.
		if got := intake.RefusalFor(reason, "field: 1, the bound is 2").Sentence; strings.Contains(got, "%") {
			t.Fatalf("%s: %q", reason, got)
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s: a sentence with a diagnostic slot was emitted unfilled", reason)
				}
			}()
			_ = intake.RefusalFor(reason, "")
		}()
	}
	if len(sentences) != len(declared) {
		t.Fatalf("%d sentences for %d reasons: one is not in the closed list", len(sentences), len(declared))
	}
}
