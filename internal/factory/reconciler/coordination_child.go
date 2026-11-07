// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/tracing"
)

// The bounded reasons a coordination child is refused or held (MR2, MR6).
var (
	// errCrossRepositoryLineage is a child whose Task, Room, issue and plan repositories disagree.
	errCrossRepositoryLineage = errors.New("cross_repository_lineage")
	// errCoordinationLineage is a child carrying the splitter's lineage, or a parent label that is
	// not its spec's: the two lineages never mix.
	errCoordinationLineage = errors.New("coordination_lineage")
	// errCoordinationParent is a child whose parent is gone, not a coordination parent, or not
	// filed.
	errCoordinationParent = errors.New("coordination_parent_missing")
	// errCoordinationScopeChanged is a child whose frozen snapshot is not the confirmed plan's.
	errCoordinationScopeChanged = errors.New("coordination_scope_changed")
	// errCoordinationArtifacts is a child whose landed references are not exactly its required
	// consumes as its parent recorded them (MR7).
	errCoordinationArtifacts = coordination.ErrArtifactUnverified
)

// reasonAllocationExhausted is a coordination child whose own allocation no longer holds a
// whole run (MR8): distinct from waiting_daily_budget and from every authority refusal.
const reasonAllocationExhausted = "allocation_exhausted"

// lineageReason is err's bounded reason when it is a lineage refusal.
func lineageReason(err error) string {
	for _, s := range []error{errCrossRepositoryLineage, errCoordinationLineage, errCoordinationParent, errCoordinationScopeChanged, errCoordinationArtifacts} {
		if errors.Is(err, s) {
			return s.Error()
		}
	}
	return ""
}

// receivedCoordinationChild admits a coordination child (MR2, MR5, MR6): its lineage agrees in
// every repository, its frozen snapshot is the confirmed plan's, and its authorizer's authority
// and the capability gate are re-read now. It never runs C7, the scoper or the splitter: class,
// template, scope and budget are the plan's. A refused authority holds this child alone.
func (r *Reconciler) receivedCoordinationChild(ctx context.Context, t *v1alpha1.Task) error {
	p, err := r.checkCoordinationLineage(ctx, t)
	if reason := lineageReason(err); reason != "" {
		return r.end(ctx, t, v1alpha1.PhaseRejected, reason)
	}
	if err != nil {
		return err
	}
	if held, err := r.coordinationAuthority(ctx, t, p); held || err != nil {
		return err
	}
	if r.Trace != nil && t.Status.Trace == nil { // R46: the task's root span, minted once at acceptance
		tr, sp := tracing.Mint()
		t.Status.Trace = &v1alpha1.TraceRef{TraceID: tr, SpanID: sp}
	}
	cp := t.Spec.CoordinationParent
	c, _ := coordination.PlanChild(p.Status.Coordination, cp.ChildID)
	t.Status.Classification = &v1alpha1.Classification{Tier: c.Tier, Classifier: "coordination", Fallback: "static"}
	t.Status.Coordination = &v1alpha1.CoordinationStatus{PlanVersion: cp.PlanVersion, ChildID: cp.ChildID, AllocationMicroUSD: c.BudgetMicroUSD}
	t.Status.ConfigHash = r.Cfg.Hash
	r.to(t, v1alpha1.PhaseTriaged, "")
	return nil
}

// coordinationAuthority re-reads child t's authority under its parent p: held is true when it
// does not pass, with the refusal's bounded reason on t. Nothing is cached, so a lost App, fence,
// maintainer, permission, class or capability holds the next admission or run.
func (r *Reconciler) coordinationAuthority(ctx context.Context, t *v1alpha1.Task, p *v1alpha1.Task) (bool, error) {
	if r.Coordination == nil {
		t.Status.Reason = coordination.ReasonPrerequisites
		return true, nil
	}
	err := r.Coordination.ExecuteReady(ctx, p.Status.Coordination, []string{t.Spec.CoordinationParent.ChildID})
	if err == nil {
		return false, nil
	}
	reason := coordination.Reason(err)
	if reason == "" {
		return true, err
	}
	t.Status.Reason = reason
	return true, nil
}

// coordinationHold is queued's re-check before every new run or resume of a coordination child:
// its lineage, then its authority. held is true when it does not start.
func (r *Reconciler) coordinationHold(ctx context.Context, t *v1alpha1.Task) (bool, error) {
	p, err := r.checkCoordinationLineage(ctx, t)
	if reason := lineageReason(err); reason != "" {
		return true, r.end(ctx, t, v1alpha1.PhaseRejected, reason)
	}
	if err != nil {
		return true, err
	}
	return r.coordinationAuthority(ctx, t, p)
}

// checkCoordinationLineage proves child t is one lineage in one repository (MR2): no splitter
// parent, split or label; its label names its spec's parent; its parent is a filed coordination
// parent whose plan holds this child at this repository, with this issue and the confirmed hash;
// its frozen scope is the plan's; and the Room of its name, when one exists, is on the same
// repository. It answers the parent.
func (r *Reconciler) checkCoordinationLineage(ctx context.Context, t *v1alpha1.Task) (*v1alpha1.Task, error) {
	cp := t.Spec.CoordinationParent
	switch {
	case cp == nil || cp.ChildID == "":
		return nil, fmt.Errorf("%w: no childID", errCoordinationLineage)
	case t.Spec.Parent != nil || t.Spec.Split != nil || t.Status.Split != nil || t.Labels[v1alpha1.LabelParent] != "":
		return nil, fmt.Errorf("%w: splitter lineage", errCoordinationLineage)
	case t.Labels[v1alpha1.LabelCoordinationParent] != cp.Task:
		return nil, fmt.Errorf("%w: the label is not the spec's parent", errCoordinationLineage)
	}
	var p v1alpha1.Task
	switch err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: cp.Task}, &p); {
	case apierrors.IsNotFound(err):
		return nil, errCoordinationParent
	case err != nil:
		return nil, err
	case p.Spec.Kind != v1alpha1.TaskKindCoordination || p.Status.Reason != coordination.ReasonFiled:
		return nil, errCoordinationParent
	}
	cs := p.Status.Coordination
	c, ok := coordination.PlanChild(cs, cp.ChildID)
	if !ok {
		return nil, errCoordinationParent
	}
	issue := 0
	for _, s := range cs.Children {
		if s.ID == c.ID {
			issue = s.Issue
			if s.Repository != c.Repository {
				return nil, errCrossRepositoryLineage
			}
		}
	}
	if c.Repository != t.Spec.Repository || t.Spec.Source.Ref != fmt.Sprintf("%s#%d", c.Repository, t.Spec.Issue) || issue != t.Spec.Issue {
		return nil, errCrossRepositoryLineage
	}
	if t.Spec.Source.ContentSHA256 != cs.PlanHash || cp.PlanVersion != cs.Plan.Version || t.Spec.Scope == nil ||
		!equality.Semantic.DeepEqual(*t.Spec.Scope, c.Scope) {
		return nil, errCoordinationScopeChanged
	}
	if want, ok := coordination.ConsumedArtifacts(c, cs.Artifacts); !ok || !slices.Equal(want, t.Spec.CoordinationArtifacts) {
		return nil, errCoordinationArtifacts
	}
	var room roomv1.Room
	switch err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: t.Name}, &room); {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, err
	case !strings.EqualFold(room.Spec.Repository, t.Spec.Repository) || room.Spec.Repository == "":
		return nil, errCrossRepositoryLineage // a Room for another repository is never adopted
	}
	return &p, nil
}
