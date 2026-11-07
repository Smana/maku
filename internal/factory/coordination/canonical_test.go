// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// exampleHash pins the design example's plan hash: the canonical bytes are a wire contract every
// consumer (the planner tool, the confirmation API, a parent issue repost) must reproduce.
const exampleHash = "5454f05eceb9e5eb5f7ecb3bd05edc1cfdf221358d4d6e2f2bd00b5c8bc28fbb"

// exampleYAML is the design's example as a person writes it: comments, flow lists, another key
// and child/artifact order, and presentation whitespace.
const exampleYAML = `# the design's example, reordered
kind: CoordinationPlan
apiVersion: maku.ogenki.io/v1alpha1
version: 3
id: p4zt6wce          # stable draft/parent Task id
title: Add and consume the v1 consumer schema
budget: {totalMicroUSD: 12750000, planningMicroUSD: 510000}
mergeOrder: [contract, consumer]
integration:
  maxRuns: 4
  runMicroUSD: 510000
  beforeDependents: true
  role: triager
  acceptance:
    - proves: The exact proposed heads work together, then the exact merged heads still do
      check: tests/integration/consumer "$MAKU_COORDINATION_MANIFEST"
children:
  - id: consumer
    repository: Smana/public-consumer
    maintainer: github:other-maintainer
    rationale: Read the published schema instead of duplicating it.
    class: scripts
    tier: standard
    roles: [implementer, reviewer]
    budgetMicroUSD: 5100000
    consumes:
      - {required: true, artifact: consumer-v1-schema, from: contract}
    dependsOn: [contract]
    scope:
      acceptance:
        - {proves: The consumer accepts the v1 schema, check: go test ./internal/consumer}
      assumptions: []
      plan: [Fetch the recorded schema commit, add the compatibility check]
      leave: [.github]
      touches: [internal/consumer]
      task: Validate the consumer against the landed v1 schema.
      state: ready
  - id: contract
    repository: Smana/public-contracts
    maintainer: github:Smana
    rationale: Publish the schema the consumer will read.
    class: scripts
    tier: standard
    roles: [implementer, reviewer]
    budgetMicroUSD: 5100000
    produces:
      - maxBytes: 65536
        mediaType: application/json
        path: schemas/consumer-v1.json
        kind: file
        name: consumer-v1-schema
    scope:
      state: ready
      task: Add the consumer v1 JSON schema and nothing else.
      touches: [schemas/consumer-v1.json]
      leave: [.github, docs/superpowers]
      plan: [Define the schema, validate its fixtures]
      assumptions: []
      acceptance:
        - {check: task test, proves: The schema and fixtures validate}
primary:
  rationale: The integration fixture and the consumer contract live here.
  repository: Smana/public-contracts
`

func TestCanonical(t *testing.T) {
	p := validPlan(t)
	c, err := Canonical(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := PlanHash(p); got != exampleHash {
		t.Errorf("PlanHash(example) = %s, want %s\ncanonical: %s", got, exampleHash, c)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(PlanHash(p)) {
		t.Errorf("PlanHash is not 64 lowercase hex: %s", PlanHash(p))
	}
	if bytes.ContainsAny(c, "\n\t") || bytes.Contains(c, []byte(`&`)) || bytes.Contains(c, []byte("null")) {
		t.Errorf("canonical bytes are not compact, unescaped and null-free: %s", c)
	}
	if i, j := bytes.Index(c, []byte(`"apiVersion"`)), bytes.Index(c, []byte(`"budget"`)); i < 0 || j < 0 || i > j {
		t.Errorf("object keys are not sorted: %s", c)
	}

	// The same plan as a person writes it, in another order, with comments: the same bytes.
	y, err := DecodePlan(strings.NewReader(exampleYAML))
	if err != nil {
		t.Fatalf("decode the YAML example: %v", err)
	}
	cy, err := Canonical(y)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c, cy) {
		t.Errorf("canonical differs across key order, child/artifact order and comments:\njson %s\nyaml %s", c, cy)
	}

	// Absent, null and empty lists are the same promise; nil beforeDependents is true.
	q := p.DeepCopy()
	q.Integration.BeforeDependents = nil
	for i := range q.Children {
		q.Children[i].Scope.Assumptions = nil
	}
	if PlanHash(*q) != PlanHash(p) {
		t.Error("nil beforeDependents or a nil empty list changed the hash")
	}

	// Edge and artifact order is not semantic; Canonical never mutates its argument.
	r := p.DeepCopy()
	slices.Reverse(r.Children)
	before := r.DeepCopy()
	if PlanHash(*r) != PlanHash(p) {
		t.Error("child order changed the hash")
	}
	if r.Children[0].ID != before.Children[0].ID {
		t.Error("Canonical sorted its argument in place")
	}
}

// The ordered lists keep their order: a reordered scope.plan, role list or mergeOrder is a
// different plan. Every typed section moves the hash.
func TestCanonicalPlanHashCovers(t *testing.T) {
	base := PlanHash(validPlan(t))
	for name, edit := range map[string]func(t *testing.T, p *v1alpha1.CoordinationPlan){
		"scope.plan order": func(t *testing.T, p *v1alpha1.CoordinationPlan) { slices.Reverse(child(t, p, "contract").Scope.Plan) },
		"role order":       func(t *testing.T, p *v1alpha1.CoordinationPlan) { slices.Reverse(child(t, p, "contract").Roles) },
		"mergeOrder":       func(_ *testing.T, p *v1alpha1.CoordinationPlan) { slices.Reverse(p.MergeOrder) },
		"id":               func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.ID = "q4zt6wce" },
		"version":          func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Version++ },
		"title":            func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Title += "!" },
		"primary":          func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Primary.Repository = "Smana/public-consumer" },
		"child":            func(t *testing.T, p *v1alpha1.CoordinationPlan) { child(t, p, "consumer").Tier = "light" },
		"dependency":       func(t *testing.T, p *v1alpha1.CoordinationPlan) { child(t, p, "consumer").DependsOn = nil },
		"consumed edge":    func(t *testing.T, p *v1alpha1.CoordinationPlan) { child(t, p, "consumer").Consumes[0].Required = false },
		"integration":      func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Integration.MaxRuns = 3 },
		"budget":           func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Budget.TotalMicroUSD++ },
	} {
		t.Run(name, func(t *testing.T) {
			p := validPlan(t)
			edit(t, &p)
			if PlanHash(p) == base {
				t.Fatalf("%s did not change the plan hash", name)
			}
		})
	}
}

func TestChildFingerprint(t *testing.T) {
	p := validPlan(t)
	contract, consumer := ChildFingerprint(p, "contract"), ChildFingerprint(p, "consumer")
	if contract == consumer || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(contract) {
		t.Fatalf("fingerprints contract %q consumer %q", contract, consumer)
	}
	if ChildFingerprint(p, "ghost") != "" {
		t.Error("an absent child has a fingerprint")
	}

	// What a child maintainer authorizes moves that child's fingerprint.
	for name, edit := range map[string]func(c *v1alpha1.CoordinationChild){
		"repository":   func(c *v1alpha1.CoordinationChild) { c.Repository = "Smana/public-other" },
		"maintainer":   func(c *v1alpha1.CoordinationChild) { c.Maintainer = "github:someone" },
		"scope":        func(c *v1alpha1.CoordinationChild) { c.Scope.Task += "!" },
		"acceptance":   func(c *v1alpha1.CoordinationChild) { c.Scope.Acceptance[0].Check += " -v" },
		"class":        func(c *v1alpha1.CoordinationChild) { c.Class = "docs" },
		"tier":         func(c *v1alpha1.CoordinationChild) { c.Tier = "light" },
		"roles":        func(c *v1alpha1.CoordinationChild) { c.Roles = c.Roles[:1] },
		"allocation":   func(c *v1alpha1.CoordinationChild) { c.BudgetMicroUSD-- },
		"consumes":     func(c *v1alpha1.CoordinationChild) { c.Consumes[0].Required = false },
		"dependencies": func(c *v1alpha1.CoordinationChild) { c.DependsOn = nil },
	} {
		q := validPlan(t)
		edit(child(t, &q, "consumer"))
		if ChildFingerprint(q, "consumer") == consumer {
			t.Errorf("%s did not change the child's fingerprint", name)
		}
		if ChildFingerprint(q, "contract") != contract {
			t.Errorf("%s of consumer changed contract's fingerprint", name)
		}
	}

	// A produced artifact moves the producer and the consumer that reads it.
	q := validPlan(t)
	child(t, &q, "contract").Produces[0].Path = "schemas/v1.json"
	if ChildFingerprint(q, "contract") == contract || ChildFingerprint(q, "consumer") == consumer {
		t.Error("a produced artifact did not move both producer and consumer fingerprints")
	}

	// The merge position is part of it.
	q = validPlan(t)
	slices.Reverse(q.MergeOrder)
	if ChildFingerprint(q, "contract") == contract || ChildFingerprint(q, "consumer") == consumer {
		t.Error("a merge position change did not move the fingerprints")
	}

	// Presentation and family-level edits do not.
	q = validPlan(t)
	q.Title += "!"
	q.Version++
	q.Primary.Rationale += "!"
	child(t, &q, "consumer").Rationale += "!"
	q.Budget.TotalMicroUSD++
	q.Integration.MaxRuns = 3
	slices.Reverse(q.Children)
	if ChildFingerprint(q, "contract") != contract || ChildFingerprint(q, "consumer") != consumer {
		t.Error("title, version, rationale, total, integration or child order moved a fingerprint")
	}
}
