// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"fmt"
	"maps"
	"slices"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/taskid"
	"github.com/Smana/maku/internal/factory/triage"
)

// Complete is whether cs's current plan has its parent's confirmation and every child's
// authorization at its current fingerprint.
func Complete(cs *v1alpha1.CoordinationStatus) bool { return complete(cs) }

// ChildTask is generation gen of child c of parent, filed as issue: an ordinary issue Task in c's
// own repository with the confirmed plan's frozen scope, class, template and allocation, requested
// by c's authorizer and hashed to the confirmed plan (v4 MR2, MR5). It carries
// LabelCoordinationParent and spec.coordinationParent, never the splitter's lineage, so it runs no
// C7, scoper or split. Its name is the key's, so a replay creates nothing twice.
func ChildTask(ns string, cfg *config.Config, parent *v1alpha1.Task, c v1alpha1.CoordinationChild, issue int, gen int32) *v1alpha1.Task {
	cs := parent.Status.Coordination
	key := taskid.CoordinationChildKey(parent.Name, c.ID, int(cs.Plan.Version), int(gen))
	b := triage.BudgetFor(cfg, c.Tier)
	b.TaskMicroUSD = c.BudgetMicroUSD // the child's confirmed lifetime allocation (MR8)
	// An unpriced run holds a task to TaskTokens: bound them by the allocation at the reference
	// rate, as a split part's are, so the fallback cannot spend past it.
	if lt := int64(float64(c.BudgetMicroUSD) / config.RefUSDPerMToken); b.TaskTokens <= 0 || lt < b.TaskTokens {
		b.TaskTokens = lt
	}
	scope := *c.Scope.DeepCopy()
	return &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: taskid.Name(key), Namespace: ns, Labels: map[string]string{
			v1alpha1.LabelIssue: strconv.Itoa(issue), v1alpha1.LabelCoordinationParent: parent.Name}},
		Spec: v1alpha1.TaskSpec{
			Source: v1alpha1.Source{Kind: "issue", Ref: fmt.Sprintf("%s#%d", c.Repository, issue), Key: key,
				RequestedBy: v1alpha1.Person(MaintainerLogin(c)), Trust: "untrusted", ContentSHA256: cs.PlanHash},
			Repository: c.Repository, Issue: issue, IssueAuthor: cfg.FactoryLogin, Text: c.Scope.Task, DataClass: "public",
			PredictedClass: c.Class, Template: templateFor(cfg, c.Roles), Budget: b, Scope: &scope,
			CoordinationParent: &v1alpha1.CoordinationParent{Task: parent.Name, PlanVersion: cs.Plan.Version, ChildID: c.ID},
		},
	}
}

// templateFor is the config template whose roles are exactly roles, by name order; the default
// template when none is.
func templateFor(cfg *config.Config, roles []string) string {
	for _, name := range slices.Sorted(maps.Keys(cfg.Templates)) {
		if slices.Equal(cfg.Templates[name].Roles, roles) {
			return name
		}
	}
	return cfg.Defaults.Template
}

// PlanChild is child id of cs's current plan.
func PlanChild(cs *v1alpha1.CoordinationStatus, id string) (v1alpha1.CoordinationChild, bool) {
	if cs == nil || cs.Plan == nil {
		return v1alpha1.CoordinationChild{}, false
	}
	return find(*cs.Plan, id)
}
