// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/narrate"
)

// The label intake's coordination refusals (v4 Issues and confirmations).
const (
	// reasonCoordinationParent is the trigger label on a parent issue: no work Task, ever.
	reasonCoordinationParent = "coordination_parent"
	// reasonCoordinationScopeChanged is the trigger on a child issue whose snapshot is not the
	// current confirmed plan's: a change comes through a plan revision and its reauthorization.
	reasonCoordinationScopeChanged = "coordination_scope_changed"
)

// coordinationIssue guards the trigger label on a coordination issue. Protection needs both a
// factory-authored first-line marker and the coordination Task it names on this repository and
// issue: a marker copied onto an ordinary issue grants nothing, and that issue takes the ordinary
// path. A parent issue is refused; a child issue retries only its unchanged factory snapshot under
// the current confirmed plan, as the same deterministic child Task, never one built from the issue.
func (p *IssuePoller) coordinationIssue(ctx context.Context, n int, iss forge.Issue, last forge.LabelEvent) (bool, error) {
	if !forge.SameLogin(iss.Author, p.Cfg.FactoryLogin) {
		return false, nil
	}
	if task, ok := narrate.CoordinationParentOf(iss.Body); ok {
		parent, err := p.coordinationParent(ctx, task)
		if err != nil || parent == nil || parent.Spec.Issue != n || !strings.EqualFold(parent.Spec.Repository, p.repo().Name) {
			return false, err
		}
		return true, p.refuse(ctx, n, last, reasonCoordinationParent)
	}
	task, id, ok := narrate.CoordinationChildOf(iss.Body)
	if !ok {
		return false, nil
	}
	parent, err := p.coordinationParent(ctx, task)
	if err != nil || parent == nil {
		return false, err
	}
	cs := parent.Status.Coordination
	c, ok := coordination.PlanChild(cs, id)
	i := slices.IndexFunc(cs.Children, func(s v1alpha1.CoordinationChildStatus) bool { return s.ID == id })
	if !ok || i < 0 || cs.Children[i].Issue != n || c.Repository != p.repo().Name {
		return false, nil // not this family's child issue here: the ordinary path, with no lineage
	}
	if _, want := narrate.CoordinationChildIssue(task, *cs.Plan, c); iss.Body != want || !iss.LastEditedAt.IsZero() || !iss.TitleEditedAt.IsZero() {
		return true, p.refuse(ctx, n, last, reasonCoordinationScopeChanged)
	}
	t := coordination.ChildTask(p.Namespace, p.Cfg, parent, c, n, max(cs.Children[i].Generation, 1))
	if err := p.Client.Create(ctx, t); err != nil && !apierrors.IsAlreadyExists(err) {
		return true, fmt.Errorf("create coordination child %s: %w", t.Name, err)
	}
	return true, p.Forge.RemoveLabel(ctx, n, p.repo().TriggerLabel)
}

// coordinationParent is the coordination parent named task, nil when there is none.
func (p *IssuePoller) coordinationParent(ctx context.Context, task string) (*v1alpha1.Task, error) {
	var t v1alpha1.Task
	switch err := p.Client.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: task}, &t); {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	case t.Spec.Kind != v1alpha1.TaskKindCoordination || t.Status.Coordination == nil || t.Status.Coordination.Plan == nil:
		return nil, nil
	}
	return &t, nil
}
