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
