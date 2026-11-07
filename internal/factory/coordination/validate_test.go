// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// validPlan is the design's example plan, decoded from testdata/plan/valid.json.
func validPlan(t *testing.T) v1alpha1.CoordinationPlan {
	t.Helper()
	f, err := os.Open("testdata/plan/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p, err := DecodePlan(f)
	if err != nil {
		t.Fatalf("decode the example plan: %v", err)
	}
	return p
}

// child returns a pointer to p's child id, failing the test when it is absent.
func child(t *testing.T, p *v1alpha1.CoordinationPlan, id string) *v1alpha1.CoordinationChild {
	t.Helper()
	for i := range p.Children {
		if p.Children[i].ID == id {
			return &p.Children[i]
		}
	}
	t.Fatalf("no child %q", id)
	return nil
}

// fakePolicies resolves the scripts class in the example's two repositories at the standard tier:
// one run is 510000 micro-USD and the task ceiling is 5100000.
type fakePolicies struct {
	calls []string
	err   error
	// other answers with another repository's policy, as a confused resolver might.
	other bool
}

func (f *fakePolicies) resolve(_ context.Context, repository, class, tier string) (RepositoryPolicy, error) {
	f.calls = append(f.calls, repository+"|"+class+"|"+tier)
	if f.err != nil {
		return RepositoryPolicy{}, f.err
	}
	known := repository == "Smana/public-contracts" || repository == "Smana/public-consumer"
	if !known || class != "scripts" || tier != "standard" {
		return RepositoryPolicy{}, ErrUnknownClass
	}
	if f.other {
		repository = "Smana/elsewhere"
	}
	return RepositoryPolicy{
		Repository: repository, Class: class, Tier: tier, FenceSHA: strings.Repeat("a", 40),
		Roles: []string{"implementer", "reviewer"}, RunMicroUSD: 510000, TaskMicroUSD: 5100000,
	}, nil
}

// The example plan validates in both modes, its arithmetic is exact, and the snapshot carries the
// resolved roles and the integration default.
func TestValidateCoordinationPlanExample(t *testing.T) {
	p := validPlan(t)
	sum := p.Budget.PlanningMicroUSD + int64(p.Integration.MaxRuns)*p.Integration.RunMicroUSD
	for _, c := range p.Children {
		sum += c.BudgetMicroUSD
	}
	if sum != 12750000 || p.Budget.TotalMicroUSD != 12750000 {
		t.Fatalf("example arithmetic = %d of %d, want 12750000 of 12750000", sum, p.Budget.TotalMicroUSD)
	}
	for _, mode := range []Mode{ModePlan, ModeExecute} {
		f := &fakePolicies{}
		got, err := Validate(context.Background(), p, mode, f.resolve)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		for _, c := range got.Children {
			if !slices.Equal(c.Roles, []string{"implementer", "reviewer"}) {
				t.Errorf("%s: %s roles = %v", mode, c.ID, c.Roles)
			}
		}
		if got.Integration.BeforeDependents == nil || !*got.Integration.BeforeDependents {
			t.Errorf("%s: beforeDependents not defaulted true", mode)
		}
		if len(f.calls) != 2 {
			t.Errorf("%s: resolved %v, want each child once", mode, f.calls)
		}
	}
}

// Validate fills absent roles from the resolved harness only, and defaults beforeDependents; it
// never mutates its argument.
func TestValidateCoordinationPlanResolves(t *testing.T) {
	p := validPlan(t)
	for i := range p.Children {
		p.Children[i].Roles = nil
	}
	p.Integration.BeforeDependents = nil
	got, err := Validate(context.Background(), p, ModeExecute, (&fakePolicies{}).resolve)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Children[0].Roles, []string{"implementer", "reviewer"}) || got.Integration.BeforeDependents == nil {
		t.Fatalf("snapshot not resolved: roles %v, beforeDependents %v", got.Children[0].Roles, got.Integration.BeforeDependents)
	}
	if p.Children[0].Roles != nil || p.Integration.BeforeDependents != nil {
		t.Fatal("Validate mutated its argument")
	}
}

func TestValidateCoordinationPlanRefusals(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
		edit func(t *testing.T, p *v1alpha1.CoordinationPlan)
		want error
	}{
		{"wrong apiVersion", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.APIVersion = "v1" }, ErrInvalidPlan},
		{"wrong kind", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Kind = "Plan" }, ErrInvalidPlan},
		{"id not C2", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.ID = "P4ZT6WCE" }, ErrInvalidPlan},
		{"version zero", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Version = 0 }, ErrInvalidPlan},
		{"control character in title", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Title = "a\x00b" }, ErrInvalidPlan},
		{"unknown mode", Mode("off"), func(*testing.T, *v1alpha1.CoordinationPlan) {}, ErrInvalidPlan},
		{"duplicate child id", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "consumer").ID = "contract"
		}, ErrInvalidPlan},
		{"duplicate repository", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "consumer").Repository = "Smana/public-contracts"
		}, ErrInvalidPlan},
		{"duplicate artifact", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			c := child(t, p, "contract")
			c.Produces = append(c.Produces, c.Produces[0])
		}, ErrInvalidPlan},
		{"third child", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			c := *child(t, p, "consumer")
			c.ID, c.Repository, c.DependsOn, c.Consumes = "third", "Smana/public-third", nil, nil
			p.Children = append(p.Children, c)
			p.MergeOrder = append(p.MergeOrder, "third")
		}, ErrInvalidPlan},
		{"primary outside the family", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) {
			p.Primary.Repository = "Smana/public-third"
		}, ErrInvalidPlan},
		{"mergeOrder misses a child", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.MergeOrder = []string{"contract"} }, ErrInvalidPlan},
		{"forward dependency", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) {
			p.MergeOrder = []string{"consumer", "contract"}
		}, ErrInvalidDependency},
		{"cyclic dependency", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "consumer").DependsOn = []string{"consumer"}
		}, ErrInvalidDependency},
		{"multiple dependencies", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			c := child(t, p, "contract")
			c.DependsOn = []string{"consumer"}
			c.Consumes = []v1alpha1.ArtifactConsumer{{From: "consumer", Artifact: "consumer-v1-schema", Required: true}}
		}, ErrInvalidDependency},
		{"dependency on an unknown child", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "consumer").DependsOn = []string{"ghost"}
		}, ErrInvalidDependency},
		{"dependency without a consumed artifact", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "consumer").Consumes = nil
		}, ErrInvalidDependency},
		{"consumed artifact nobody produces", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "consumer").Consumes[0].Artifact = "ghost"
		}, ErrInvalidDependency},
		{"consumption without a dependency", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "consumer").DependsOn = nil
		}, ErrInvalidDependency},
		{"non-file artifact", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "contract").Produces[0].Kind = "image"
		}, ErrInvalidPlan},
		{"artifact over 64 KiB", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "contract").Produces[0].MaxBytes = 65537
		}, ErrInvalidPlan},
		{"scope not ready", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "contract").Scope.State = "question"
		}, ErrInvalidPlan},
		{"scope without acceptance", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "contract").Scope.Acceptance = nil
		}, ErrInvalidPlan},
		{"unknown class", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "contract").Class = "anything"
		}, ErrUnknownClass},
		{"roles beyond the harness", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "contract").Roles = []string{"implementer", "reviewer", "tester"}
		}, ErrRolesMismatch},
		{"roles reordered from the harness", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "contract").Roles = []string{"reviewer", "implementer"}
		}, ErrRolesMismatch},
		{"allocation below one run", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "contract").BudgetMicroUSD = 509999
			p.Budget.TotalMicroUSD -= 5100000 - 509999
		}, ErrAllocationBelowRun},
		{"allocation above the task ceiling", ModePlan, func(t *testing.T, p *v1alpha1.CoordinationPlan) {
			child(t, p, "contract").BudgetMicroUSD++
			p.Budget.TotalMicroUSD++
		}, ErrAllocationAboveCeiling},
		{"maxRuns zero", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Integration.MaxRuns = 0 }, ErrInvalidPlan},
		{"maxRuns seventeen", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Integration.MaxRuns = 17 }, ErrInvalidPlan},
		{"integration role other than triager", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) {
			p.Integration.Role = "implementer"
		}, ErrInvalidPlan},
		{"beforeDependents false without rationale", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) {
			f := false
			p.Integration.BeforeDependents = &f
		}, ErrInvalidPlan},
		{"negative amount", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Budget.PlanningMicroUSD = -1 }, ErrInvalidPlan},
		{"integer overflow", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) {
			p.Integration.RunMicroUSD = math.MaxInt64 / 2
		}, ErrBudgetOverflow},
		{"allocations over the total", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) { p.Budget.TotalMicroUSD-- }, ErrBudgetExceeded},
		{"total over the bound", ModePlan, func(_ *testing.T, p *v1alpha1.CoordinationPlan) {
			p.Budget.TotalMicroUSD = MaxBudgetMicroUSD + 1
		}, ErrInvalidPlan},
		{"execute with one child", ModeExecute, func(t *testing.T, p *v1alpha1.CoordinationPlan) { oneChild(t, p) }, ErrIncompletePlan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validPlan(t)
			tc.edit(t, &p)
			_, err := Validate(context.Background(), p, tc.mode, (&fakePolicies{}).resolve)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate = %v, want %v", err, tc.want)
			}
			var pe *PlanError
			if !errors.As(err, &pe) || pe.Code == "" || ReasonOf(err) != pe.Code {
				t.Fatalf("Validate = %v, want a *PlanError with a reason", err)
			}
		})
	}
}

// oneChild reduces p to an editable one-child draft: the contract child alone.
func oneChild(t *testing.T, p *v1alpha1.CoordinationPlan) {
	t.Helper()
	p.Children = []v1alpha1.CoordinationChild{*child(t, p, "contract")}
	p.MergeOrder = []string{"contract"}
	p.Budget.TotalMicroUSD -= 5100000
}

// Plan mode keeps a one-child draft editable, yet still resolves its class: an unknown class is
// never accepted for later.
func TestValidateCoordinationPlanDraft(t *testing.T) {
	p := validPlan(t)
	oneChild(t, &p)
	if _, err := Validate(context.Background(), p, ModePlan, (&fakePolicies{}).resolve); err != nil {
		t.Fatalf("plan-mode one-child draft: %v", err)
	}
	p.Children[0].Class = "anything"
	if _, err := Validate(context.Background(), p, ModePlan, (&fakePolicies{}).resolve); !errors.Is(err, ErrUnknownClass) {
		t.Fatalf("plan-mode unknown class = %v, want %v", err, ErrUnknownClass)
	}
}

// A resolver failure is policy_unresolved, keeping its cause; another repository's policy is
// refused, never adopted.
func TestValidateCoordinationPlanPolicy(t *testing.T) {
	boom := errors.New("boom")
	_, err := Validate(context.Background(), validPlan(t), ModePlan, (&fakePolicies{err: boom}).resolve)
	if !errors.Is(err, ErrPolicyUnresolved) || !errors.Is(err, boom) {
		t.Fatalf("resolver failure = %v, want policy_unresolved wrapping its cause", err)
	}
	_, err = Validate(context.Background(), validPlan(t), ModePlan, (&fakePolicies{other: true}).resolve)
	if !errors.Is(err, ErrPolicyMismatch) {
		t.Fatalf("another repository's policy = %v, want %v", err, ErrPolicyMismatch)
	}
	if _, err := Validate(context.Background(), validPlan(t), ModePlan, nil); !errors.Is(err, ErrPolicyUnresolved) {
		t.Fatalf("nil resolver = %v, want %v", err, ErrPolicyUnresolved)
	}
}

// Only an explicit per-child increase marker admits an allocation above the task ceiling, and only
// for that child.
func TestValidateWithIncrease(t *testing.T) {
	p := validPlan(t)
	child(t, &p, "contract").BudgetMicroUSD++
	p.Budget.TotalMicroUSD++
	ctx := context.Background()
	if _, err := ValidateWithIncrease(ctx, p, ModeExecute, (&fakePolicies{}).resolve, map[string]bool{"contract": true}); err != nil {
		t.Fatalf("confirmed increase: %v", err)
	}
	if _, err := ValidateWithIncrease(ctx, p, ModeExecute, (&fakePolicies{}).resolve, map[string]bool{"consumer": true}); !errors.Is(err, ErrAllocationAboveCeiling) {
		t.Fatalf("another child's marker = %v, want %v", err, ErrAllocationAboveCeiling)
	}
	if _, err := Validate(ctx, p, ModeExecute, (&fakePolicies{}).resolve); !errors.Is(err, ErrAllocationAboveCeiling) {
		t.Fatalf("Validate = %v, want %v", err, ErrAllocationAboveCeiling)
	}
}

func TestDecodePlan(t *testing.T) {
	raw, err := os.ReadFile("testdata/plan/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   string
	}{
		{"unknown top-level field", strings.Replace(string(raw), `"title"`, `"presentation": 1, "title"`, 1)},
		{"unknown nested field", strings.Replace(string(raw), `"budgetMicroUSD": 5100000,`, `"budgetMicroUSD": 5100000, "note": "x",`, 1)},
		{"duplicate key", strings.Replace(string(raw), `"title"`, `"version": 4, "title"`, 1)},
		{"second document", "apiVersion: maku.ogenki.io/v1alpha1\n---\nkind: CoordinationPlan\n"},
		{"trailing document", string(raw) + "\n---\n{}\n"},
		{"empty", ""},
		{"not a mapping", "[1, 2]"},
		{"oversize", "title: " + strings.Repeat("a", MaxPlanBytes) + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodePlan(strings.NewReader(tc.in)); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("DecodePlan = %v, want %v", err, ErrInvalidPlan)
			}
		})
	}
}
