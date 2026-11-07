// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
)

// MaxBudgetMicroUSD is the CRD's bound on any one amount, and so on the family total that every
// allocation sums under.
const MaxBudgetMicroUSD = 1_000_000_000

// The CRD's patterns, checked here too so a plan refused at admission is refused while still a
// draft (api/factory/v1alpha1/coordination_types.go).
var (
	reC2ID       = regexp.MustCompile(`^[a-z2-7]{8}$`)
	reChildID    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	reRepository = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	reMaintainer = regexp.MustCompile(`^github:[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	reArtifact   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

// The plan's fixed values in this release.
const (
	integrationRole    = "triager"
	artifactKindFile   = "file"
	maxArtifactBytes   = 65536
	maxIntegrationRuns = 16
	childrenExecute    = 2
)

var (
	tiers = []string{"light", "standard", "frontier"}
	roles = []string{"implementer", "reviewer", "tester", "triager"}
)

// Validate checks p for mode and returns its fully resolved snapshot: each child's class, tier and
// roles as ResolvePolicy answers for that child's own repository, and beforeDependents defaulted
// true (MR4, MR12). It is deterministic, calls no model, and never modifies p. An allocation above
// a child's task ceiling is refused; ValidateWithIncrease is the one path that admits it.
func Validate(ctx context.Context, p v1alpha1.CoordinationPlan, mode Mode, resolve ResolvePolicy) (v1alpha1.CoordinationPlan, error) {
	return ValidateWithIncrease(ctx, p, mode, resolve, nil)
}

// ValidateWithIncrease is Validate admitting an allocation above the task ceiling for each child
// confirmed[childID] marks. It only records the proposal: execution still needs the parent's and
// that child maintainer's confirmations of the version carrying it (MR8).
func ValidateWithIncrease(ctx context.Context, p v1alpha1.CoordinationPlan, mode Mode, resolve ResolvePolicy, confirmed map[string]bool) (v1alpha1.CoordinationPlan, error) {
	if mode != ModePlan && mode != ModeExecute {
		return p, refuse(ErrInvalidPlan, "mode", "%q is neither plan nor execute", mode)
	}
	if resolve == nil {
		return p, refuse(ErrPolicyUnresolved, "", "no policy resolver")
	}
	n := *p.DeepCopy()
	if err := shape(n, mode); err != nil {
		return p, err
	}
	if err := dependencies(n); err != nil {
		return p, err
	}
	for i := range n.Children {
		if err := resolveChild(ctx, &n.Children[i], resolve, confirmed[n.Children[i].ID]); err != nil {
			return p, err
		}
	}
	if err := budget(n); err != nil {
		return p, err
	}
	if n.Integration.Role == "" {
		n.Integration.Role = integrationRole
	}
	if n.Integration.BeforeDependents == nil {
		t := true
		n.Integration.BeforeDependents = &t
	}
	return n, nil
}

// text is a bounded single value of person-facing text: no control or format character, valid
// UTF-8, at most limit bytes, and non-blank when required.
func text(v string, required bool, limit int) bool {
	if strings.TrimSpace(v) == "" {
		return !required && v == ""
	}
	return len(v) <= limit && utf8.ValidString(v) && !strings.ContainsFunc(v, envelope.Control)
}

// shape checks every bound and identity the CRD holds, plus the mode's child count (MR1).
func shape(p v1alpha1.CoordinationPlan, mode Mode) error {
	switch {
	case p.APIVersion != v1alpha1.CoordinationPlanAPIVersion:
		return refuse(ErrInvalidPlan, "apiVersion", "must be %s", v1alpha1.CoordinationPlanAPIVersion)
	case p.Kind != v1alpha1.CoordinationPlanKind:
		return refuse(ErrInvalidPlan, "kind", "must be %s", v1alpha1.CoordinationPlanKind)
	case !reC2ID.MatchString(p.ID):
		return refuse(ErrInvalidPlan, "id", "not an eight-character C2 id")
	case !text(p.Title, true, 200):
		return refuse(ErrInvalidPlan, "title", "1-200 bytes of text")
	case p.Version < 1:
		return refuse(ErrInvalidPlan, "version", "at least 1")
	case !reRepository.MatchString(p.Primary.Repository) || len(p.Primary.Repository) > 140:
		return refuse(ErrInvalidPlan, "primary.repository", "not owner/name")
	case !text(p.Primary.Rationale, false, 1024):
		return refuse(ErrInvalidPlan, "primary.rationale", "at most 1024 bytes of text")
	case len(p.Children) == 0:
		return refuse(ErrInvalidPlan, "children", "a plan has a child")
	case len(p.Children) > childrenExecute:
		return refuse(ErrInvalidPlan, "children", "at most %d children (MR1)", childrenExecute)
	case mode == ModeExecute && len(p.Children) != childrenExecute:
		return refuse(ErrIncompletePlan, "children", "execute needs exactly %d children (MR1)", childrenExecute)
	}
	ids, repos := map[string]bool{}, map[string]bool{}
	for _, c := range p.Children {
		if err := childShape(c); err != nil {
			return err
		}
		if ids[c.ID] {
			return refuse(ErrInvalidPlan, "children."+c.ID, "duplicate child id")
		}
		if repos[strings.ToLower(c.Repository)] {
			return refuse(ErrInvalidPlan, "children."+c.ID+".repository", "a repository holds one child (MR1)")
		}
		ids[c.ID], repos[strings.ToLower(c.Repository)] = true, true
	}
	if !repos[strings.ToLower(p.Primary.Repository)] {
		return refuse(ErrInvalidPlan, "primary.repository", "is no child's repository (MR3)")
	}
	if len(p.MergeOrder) != len(p.Children) {
		return refuse(ErrInvalidPlan, "mergeOrder", "must name every child once")
	}
	for i, id := range p.MergeOrder {
		if !ids[id] || slices.Index(p.MergeOrder, id) != i {
			return refuse(ErrInvalidPlan, "mergeOrder", "must name every child once")
		}
	}
	return integrationShape(p.Integration)
}

func childShape(c v1alpha1.CoordinationChild) error {
	f := "children." + c.ID
	switch {
	case !reChildID.MatchString(c.ID):
		return refuse(ErrInvalidPlan, "children", "a child id is a lowercase label of at most 32")
	case !reRepository.MatchString(c.Repository) || len(c.Repository) > 140:
		return refuse(ErrInvalidPlan, f+".repository", "not owner/name")
	case !reMaintainer.MatchString(c.Maintainer):
		return refuse(ErrInvalidPlan, f+".maintainer", "not github:<login>")
	case !text(c.Rationale, false, 1024):
		return refuse(ErrInvalidPlan, f+".rationale", "at most 1024 bytes of text")
	case !text(c.Class, true, 64):
		return refuse(ErrInvalidPlan, f+".class", "1-64 bytes")
	case !slices.Contains(tiers, c.Tier):
		return refuse(ErrInvalidPlan, f+".tier", "none of %v", tiers)
	case len(c.Roles) > 4:
		return refuse(ErrInvalidPlan, f+".roles", "at most 4")
	case c.BudgetMicroUSD < 0:
		return refuse(ErrInvalidPlan, f+".budgetMicroUSD", "negative")
	case c.Scope.State != envelope.ScopeReady:
		return refuse(ErrInvalidPlan, f+".scope.state", "a child's scope is frozen ready")
	case len(c.Produces) > 8 || len(c.Consumes) > 8:
		return refuse(ErrInvalidPlan, f, "at most 8 produced and 8 consumed artifacts")
	}
	for _, r := range c.Roles {
		if !slices.Contains(roles, r) {
			return refuse(ErrInvalidPlan, f+".roles", "unknown role")
		}
	}
	if err := envelopeScope(c.Scope).Validate(); err != nil {
		return refuse(ErrInvalidPlan, f+".scope", "%v", err)
	}
	names := map[string]bool{}
	for _, a := range c.Produces {
		af := f + ".produces." + a.Name
		switch {
		case !reArtifact.MatchString(a.Name):
			return refuse(ErrInvalidPlan, f+".produces", "an artifact name is a lowercase label of at most 64")
		case names[a.Name]:
			return refuse(ErrInvalidPlan, af, "duplicate artifact")
		case a.Kind != artifactKindFile:
			return refuse(ErrInvalidPlan, af+".kind", "file is the only artifact kind")
		case !text(a.Path, true, 512):
			return refuse(ErrInvalidPlan, af+".path", "1-512 bytes")
		case !text(a.MediaType, false, 128):
			return refuse(ErrInvalidPlan, af+".mediaType", "at most 128 bytes")
		case a.MaxBytes < 1 || a.MaxBytes > maxArtifactBytes:
			return refuse(ErrInvalidPlan, af+".maxBytes", "1-%d", maxArtifactBytes)
		}
		names[a.Name] = true
	}
	return nil
}

func integrationShape(in v1alpha1.IntegrationPlan) error {
	switch {
	case in.Role != "" && in.Role != integrationRole:
		return refuse(ErrInvalidPlan, "integration.role", "integration runs %s only (MR17)", integrationRole)
	case !in.EffectiveBeforeDependents() && strings.TrimSpace(in.BeforeDependentsRationale) == "":
		return refuse(ErrInvalidPlan, "integration.beforeDependentsRationale", "beforeDependents: false needs a rationale")
	case !text(in.BeforeDependentsRationale, false, 1024):
		return refuse(ErrInvalidPlan, "integration.beforeDependentsRationale", "at most 1024 bytes of text")
	case in.MaxRuns < 1 || in.MaxRuns > maxIntegrationRuns:
		return refuse(ErrInvalidPlan, "integration.maxRuns", "1-%d", maxIntegrationRuns)
	case in.RunMicroUSD < 0:
		return refuse(ErrInvalidPlan, "integration.runMicroUSD", "negative")
	case len(in.Acceptance) < 1 || len(in.Acceptance) > 5:
		return refuse(ErrInvalidPlan, "integration.acceptance", "1-5 checks")
	}
	for _, a := range in.Acceptance {
		if !text(a.Check, true, 300) || !text(a.Proves, true, 200) {
			return refuse(ErrInvalidPlan, "integration.acceptance", "check 1-300 and proves 1-200 bytes")
		}
	}
	return nil
}

// dependencies holds MR7 and validation invariant 2: at most one edge in the family, pointing
// backward in mergeOrder, carried by at least one consumed artifact the producer declares, and no
// consumption without its dependency.
func dependencies(p v1alpha1.CoordinationPlan) error {
	edges := 0
	for _, c := range p.Children {
		edges += len(c.DependsOn)
	}
	if edges > 1 {
		return refuse(ErrInvalidDependency, "children", "at most one dependency in the family (MR1)")
	}
	for _, c := range p.Children {
		f := "children." + c.ID
		for _, d := range c.DependsOn {
			switch {
			case d == c.ID:
				return refuse(ErrInvalidDependency, f+".dependsOn", "a child cannot depend on itself")
			case !slices.ContainsFunc(p.Children, func(x v1alpha1.CoordinationChild) bool { return x.ID == d }):
				return refuse(ErrInvalidDependency, f+".dependsOn", "names no child")
			case slices.Index(p.MergeOrder, d) > slices.Index(p.MergeOrder, c.ID):
				return refuse(ErrInvalidDependency, f+".dependsOn", "points forward in mergeOrder")
			case !slices.ContainsFunc(c.Consumes, func(e v1alpha1.ArtifactConsumer) bool { return e.From == d }):
				return refuse(ErrInvalidDependency, f+".dependsOn", "consumes no artifact of its dependency (MR7)")
			}
		}
		seen := map[string]bool{}
		for _, e := range c.Consumes {
			key := e.From + "/" + e.Artifact
			switch {
			case seen[key]:
				return refuse(ErrInvalidDependency, f+".consumes."+key, "duplicate consumed artifact")
			case !slices.Contains(c.DependsOn, e.From):
				return refuse(ErrInvalidDependency, f+".consumes."+key, "consumes from a child it does not depend on")
			case producedBy(p, e.From, e.Artifact) == nil:
				return refuse(ErrInvalidDependency, f+".consumes."+key, "the producer declares no such artifact")
			}
			seen[key] = true
		}
	}
	return nil
}

// resolveChild resolves c's class and tier at c's own repository and writes the harness's roles
// when c names none; any other role list is refused. The allocation covers one run and stays under
// the task ceiling unless increase is set (validation invariants 3 and 4).
func resolveChild(ctx context.Context, c *v1alpha1.CoordinationChild, resolve ResolvePolicy, increase bool) error {
	f := "children." + c.ID
	pol, err := resolve(ctx, c.Repository, c.Class, c.Tier)
	if err != nil {
		e := refuse(ErrPolicyUnresolved, f+".class", "")
		e.Detail, e.cause = "", err
		return e
	}
	if pol.Repository != c.Repository || pol.Class != c.Class || pol.Tier != c.Tier {
		return refuse(ErrPolicyMismatch, f, "resolved another repository, class or tier")
	}
	if len(pol.Roles) == 0 {
		return refuse(ErrPolicyMismatch, f+".roles", "the class resolves no role")
	}
	switch {
	case len(c.Roles) == 0:
		c.Roles = slices.Clone(pol.Roles)
	case !slices.Equal(c.Roles, pol.Roles):
		return refuse(ErrRolesMismatch, f+".roles", "the class harness is %v", pol.Roles)
	}
	switch {
	case c.BudgetMicroUSD < pol.RunMicroUSD:
		return refuse(ErrAllocationBelowRun, f+".budgetMicroUSD", "one %s run is %d", c.Tier, pol.RunMicroUSD)
	case c.BudgetMicroUSD > pol.TaskMicroUSD && !increase:
		return refuse(ErrAllocationAboveCeiling, f+".budgetMicroUSD", "the task ceiling is %d", pol.TaskMicroUSD)
	}
	return nil
}

// budget holds MR8 with checked arithmetic: planning + every child + maxRuns × runMicroUSD fits
// the total, and the total is within the CRD's bound, so every part is.
func budget(p v1alpha1.CoordinationPlan) error {
	if p.Budget.PlanningMicroUSD < 0 || p.Budget.TotalMicroUSD < 0 {
		return refuse(ErrInvalidPlan, "budget", "negative")
	}
	runs, ok := mul(int64(p.Integration.MaxRuns), p.Integration.RunMicroUSD)
	sum := p.Budget.PlanningMicroUSD
	ok = ok && add(&sum, runs)
	for _, c := range p.Children {
		ok = ok && add(&sum, c.BudgetMicroUSD)
	}
	switch {
	case !ok:
		return refuse(ErrBudgetOverflow, "budget.totalMicroUSD", "the allocations overflow")
	case sum > p.Budget.TotalMicroUSD:
		return refuse(ErrBudgetExceeded, "budget.totalMicroUSD", "allocations %d exceed the total %d", sum, p.Budget.TotalMicroUSD)
	case p.Budget.TotalMicroUSD > MaxBudgetMicroUSD:
		return refuse(ErrInvalidPlan, "budget.totalMicroUSD", "at most %d", MaxBudgetMicroUSD)
	}
	return nil
}

// add adds non-negative v to *s, reporting false on overflow.
func add(s *int64, v int64) bool {
	if v > math.MaxInt64-*s {
		return false
	}
	*s += v
	return true
}

// mul multiplies non-negative a and b, reporting false on overflow.
func mul(a, b int64) (int64, bool) {
	if a != 0 && b > math.MaxInt64/a {
		return 0, false
	}
	return a * b, true
}

// envelopeScope is a child's TaskScope in the scoper's shape, whose Validate holds SC6's bounds.
func envelopeScope(s v1alpha1.TaskScope) envelope.Scope {
	out := envelope.Scope{State: s.State, Task: s.Task, Touches: s.Touches, Leave: s.Leave, Plan: s.Plan, Assumptions: s.Assumptions}
	for _, a := range s.Acceptance {
		out.Acceptance = append(out.Acceptance, envelope.Acceptance{Check: a.Check, Proves: a.Proves})
	}
	return out
}

// String names the mode, for a diagnostic.
func (m Mode) String() string { return fmt.Sprintf("%q", string(m)) }
