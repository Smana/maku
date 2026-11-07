// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// ReasonGenerationPending is an affected child's entry after a material edit (MR8, MR12): its old
// generation is stopping and no replacement starts until that generation has drained and the
// new version's confirmations exist.
const ReasonGenerationPending = "generation_pending"

// applyPlanVersion is a full next version's acceptance on a parent that already filed children
// (MR8, MR12): AcceptVersion on its status, before the write that records the version.
func (s *Service) applyPlanVersion(_ context.Context, parent *v1alpha1.Task, next v1alpha1.CoordinationPlan, m Materiality) error {
	AcceptVersion(parent.Status.Coordination, next, m)
	return nil
}

// AcceptVersion applies an accepted version next to cs, a parent's status already holding next as
// its plan: every release an affected child or a changed integration rested on is dropped at
// once; each child whose fingerprint changed and has a Task gets one PendingGeneration naming
// that Task as stopping, and its entry generation_pending; a child with no Task yet takes the new
// fingerprint, so its first admission is under next. An unaffected child keeps its Task, entry and
// fingerprint. Nothing is replaced in place: the reconciler stops the old Task and creates
// CoordinationChildKey(..., version, generation+1) only once the old one drained.
func AcceptVersion(cs *v1alpha1.CoordinationStatus, next v1alpha1.CoordinationPlan, m Materiality) {
	integration := slices.ContainsFunc(m.ChangedSections, func(s string) bool { return strings.HasPrefix(s, "integration.") })
	for i := range cs.Children {
		st := &cs.Children[i]
		fp := ChildFingerprint(next, st.ID)
		affected := fp != st.Fingerprint
		if affected || integration {
			st.IntegrationRelease = nil // MR9: its manifest bound the old plan
		}
		if !affected {
			continue
		}
		if st.Task == "" {
			st.Fingerprint = fp
			continue
		}
		pg := v1alpha1.PendingGeneration{ChildID: st.ID, Generation: st.Generation + 1, PlanVersion: next.Version, StoppingTask: st.Task}
		if j := slices.IndexFunc(cs.PendingGenerations, func(p v1alpha1.PendingGeneration) bool { return p.ChildID == st.ID }); j >= 0 {
			cs.PendingGenerations[j].PlanVersion = next.Version // still the same stopping generation
		} else {
			cs.PendingGenerations = v1alpha1.AppendNewest(cs.PendingGenerations, pg, v1alpha1.MaxPendingGenerations)
		}
		st.Reason = ReasonGenerationPending
	}
}

// Pending is child id's pending generation on cs, nil for none.
func Pending(cs *v1alpha1.CoordinationStatus, id string) *v1alpha1.PendingGeneration {
	if cs == nil {
		return nil
	}
	for i := range cs.PendingGenerations {
		if cs.PendingGenerations[i].ChildID == id {
			return &cs.PendingGenerations[i]
		}
	}
	return nil
}

// LifetimeSpent is what every generation of child id holds of its lifetime allocation (MR8): each
// generation's AllocationSpent, over tasks, a parent's children by LabelCoordinationParent.
func LifetimeSpent(tasks []v1alpha1.Task, id string) int64 {
	var spent int64
	for i := range tasks {
		if cp := tasks[i].Spec.CoordinationParent; cp != nil && cp.ChildID == id {
			spent += AllocationSpent(&tasks[i])
		}
	}
	return spent
}

// ChildTasks is every generation's Task of parent: its implementation children, never an
// integration Task.
func ChildTasks(ctx context.Context, c client.Reader, ns, parent string) ([]v1alpha1.Task, error) {
	var l v1alpha1.TaskList
	if err := c.List(ctx, &l, client.InNamespace(ns), client.MatchingLabels{v1alpha1.LabelCoordinationParent: parent}); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(l.Items, func(t v1alpha1.Task) bool {
		return t.Spec.CoordinationParent == nil || t.Spec.CoordinationParent.ChildID == ""
	}), nil
}

// coveringConfirmation is the parent confirmation child id may still run under while a newer
// version waits for its confirmations, nil for none: its authorization at its current fingerprint
// stands, and the parent confirmed a version at or after the one that authorization names, in
// which the child was therefore exactly as now. A complete plan answers its current confirmation.
func coveringConfirmation(cs *v1alpha1.CoordinationStatus, id string) *ParentConfirmation {
	a := currentAuthorization(cs, id)
	if a == nil {
		return nil
	}
	if pc := parentConfirmation(cs); pc != nil {
		return pc
	}
	for i := len(cs.Confirmations) - 1; i >= 0; i-- {
		c := &cs.Confirmations[i]
		if slices.ContainsFunc(cs.History, func(v v1alpha1.CoordinationPlanVersion) bool {
			return v.PlanHash == c.PlanHash && v.Version >= a.PlanVersion
		}) {
			return c
		}
	}
	return nil
}
