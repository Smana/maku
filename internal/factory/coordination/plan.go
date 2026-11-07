// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// Mode is the coordination mode a plan is validated for (MR16). off validates nothing: it serves
// no coordination surface at all.
type Mode string

// The modes a plan validates under: plan keeps an incomplete one-child draft editable; execute
// needs the whole two-child family.
const (
	ModePlan    Mode = "plan"
	ModeExecute Mode = "execute"
)

// MaxPlanBytes bounds a plan document before it is parsed: two bounded children, their scopes and
// the integration acceptance fit well inside it.
const MaxPlanBytes = 64 << 10

// The bounded reasons a plan, an edit or a confirmation is refused. Each is a sentinel callers
// branch on with errors.Is; a *PlanError carries it with the offending field.
var (
	// ErrInvalidPlan is a shape or bound the typed plan breaks (MR12): an unknown field, a second
	// document, a duplicate id or repository, a third child, a non-file or oversize artifact.
	ErrInvalidPlan = errors.New("invalid_plan")
	// ErrInvalidDependency is a dependency that is not one backward edge carried by a produced and
	// consumed artifact (MR7): forward, cyclic, multiple or without its artifact.
	ErrInvalidDependency = errors.New("invalid_dependency")
	// ErrIncompletePlan is an execute-mode plan without exactly two children (MR1).
	ErrIncompletePlan = errors.New("incomplete_plan")
	// ErrPolicyUnresolved is a ResolvePolicy failure; the resolver's own error stays in the chain.
	ErrPolicyUnresolved = errors.New("policy_unresolved")
	// ErrUnknownClass is what a ResolvePolicy returns for a class the repository does not allow.
	ErrUnknownClass = errors.New("unknown_class")
	// ErrPolicyMismatch is a resolved policy for another repository, class or tier than asked.
	ErrPolicyMismatch = errors.New("policy_mismatch")
	// ErrRolesMismatch is a role list other than the resolved class harness, in its order.
	ErrRolesMismatch = errors.New("roles_mismatch")
	// ErrAllocationBelowRun is a child allocation that cannot pay for one run at its tier.
	ErrAllocationBelowRun = errors.New("allocation_below_run")
	// ErrAllocationAboveCeiling is a child allocation above its task ceiling without an explicit
	// increase (ValidateWithIncrease).
	ErrAllocationAboveCeiling = errors.New("allocation_above_ceiling")
	// ErrBudgetOverflow is a budget sum that overflows int64.
	ErrBudgetOverflow = errors.New("budget_overflow")
	// ErrBudgetExceeded is planning + children + integration above budget.totalMicroUSD (MR8).
	ErrBudgetExceeded = errors.New("budget_exceeded")
	// ErrStalePlan is a confirmation of a plan hash or child fingerprint that is no longer current.
	ErrStalePlan = errors.New("stale_plan")
	// ErrPlanVersion is an edit that is not the same plan at exactly the next version.
	ErrPlanVersion = errors.New("plan_version")
	// ErrPrimaryRequiresNewParent is an edit of the primary repository: a new parent, not an edit.
	ErrPrimaryRequiresNewParent = errors.New("primary_requires_new_parent")
	// ErrInvalidConfirmation is a confirmation naming neither, or both, a parent and a child.
	ErrInvalidConfirmation = errors.New("invalid_confirmation")
	// ErrPlanningCostUnverified is a planning attempt carrying a reservation or settlement
	// identifier without its amount: a cost nobody can reconcile against planningMicroUSD.
	ErrPlanningCostUnverified = errors.New("planning_cost_unverified")
)

// PlanError is one refusal: its bounded reason code (one of the sentinels above), the field it
// concerns and, for a resolver failure, the cause. Field and Detail name plan structure, never plan
// text, so a log or a diagnostic may carry them.
type PlanError struct {
	Code   string
	Field  string
	Detail string
	sent   error
	cause  error
}

func (e *PlanError) Error() string {
	s := e.Code
	if e.Field != "" {
		s += ": " + e.Field
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	if e.cause != nil {
		s += ": " + e.cause.Error()
	}
	return s
}

// Unwrap is the reason's sentinel and the cause, so errors.Is matches either.
func (e *PlanError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.sent}
	}
	return []error{e.sent, e.cause}
}

// refuse is a *PlanError for sentinel at field.
func refuse(sentinel error, field, format string, args ...any) *PlanError {
	return &PlanError{Code: sentinel.Error(), Field: field, Detail: fmt.Sprintf(format, args...), sent: sentinel}
}

// ReasonOf is err's bounded reason code, or "" when err carries none.
func ReasonOf(err error) string {
	if pe, ok := errors.AsType[*PlanError](err); ok {
		return pe.Code
	}
	return ""
}

// RepositoryPolicy is one repository's resolved facts for one child: its class, tier and the
// class harness's roles in order, the fence blob they were read at, the run and task ceilings of
// that tier in micro-USD (MR6, MR8), and what a RepositoryChecks read alongside.
type RepositoryPolicy struct {
	Repository, Class, Tier, FenceSHA string
	Roles                             []string
	RunMicroUSD, TaskMicroUSD         int64
	// Public is the repository's live visibility, AppRepository the full name its App connection
	// answered for, and Permission the actor's GitHub permission when an actor was checked.
	Public        bool
	AppRepository string
	Permission    string
}

// ResolvePolicy resolves a class and tier at repository's confirmed policy and the central
// guardrails, returning ErrUnknownClass for a class the repository does not allow. It is the only
// source of roles and ceilings: a plan never declares them (MR4).
type ResolvePolicy func(ctx context.Context, repository, class, tier string) (RepositoryPolicy, error)

// DecodePlan reads one plan document, YAML or JSON, bounded by MaxPlanBytes. It refuses a second
// document, a duplicate key and an unknown field anywhere: a plan must never read differently from
// what its author sees. It checks no invariant; that is Validate's.
func DecodePlan(r io.Reader) (v1alpha1.CoordinationPlan, error) {
	var p v1alpha1.CoordinationPlan
	raw, err := io.ReadAll(io.LimitReader(r, MaxPlanBytes+1))
	if err != nil {
		return p, fmt.Errorf("read plan: %w", err)
	}
	if len(raw) > MaxPlanBytes {
		return p, refuse(ErrInvalidPlan, "", "over %d bytes", MaxPlanBytes)
	}
	// yaml.v3 refuses duplicate keys; it also tells one document from several.
	dec := yamlv3.NewDecoder(bytes.NewReader(raw))
	var doc yamlv3.Node
	if err := dec.Decode(&doc); err != nil {
		return p, refuse(ErrInvalidPlan, "", "not one YAML or JSON document: %v", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yamlv3.MappingNode {
		return p, refuse(ErrInvalidPlan, "", "not a mapping")
	}
	if err := dec.Decode(new(yamlv3.Node)); !errors.Is(err, io.EOF) {
		return p, refuse(ErrInvalidPlan, "", "more than one document")
	}
	if err := uniqueKeys(&doc); err != nil {
		return p, err
	}
	j, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return p, refuse(ErrInvalidPlan, "", "%v", err)
	}
	jd := json.NewDecoder(bytes.NewReader(j))
	jd.DisallowUnknownFields()
	if err := jd.Decode(&p); err != nil {
		return v1alpha1.CoordinationPlan{}, refuse(ErrInvalidPlan, "", "%v", err)
	}
	return p, nil
}

// uniqueKeys refuses a mapping that writes a key twice, at any depth: decoding into a Node keeps
// both, and YAMLToJSON would keep the last silently.
func uniqueKeys(n *yamlv3.Node) error {
	if n.Kind == yamlv3.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if seen[k] {
				return refuse(ErrInvalidPlan, k, "written twice")
			}
			seen[k] = true
		}
	}
	for _, c := range n.Content {
		if err := uniqueKeys(c); err != nil {
			return err
		}
	}
	return nil
}
