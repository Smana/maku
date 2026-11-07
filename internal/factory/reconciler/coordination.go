// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/rooms"
)

// coordinationPort is the coordination service the reconciler drives (v4 MR2):
// *coordination.Service implements it.
type coordinationPort interface {
	File(ctx context.Context, parentTask string) error
	ExecuteReady(ctx context.Context, cs *v1alpha1.CoordinationStatus, ids []string) error
}

// The parent's and the held integration Task's own reasons.
const (
	reasonWaitingDependency = "waiting_dependency"
	reasonIntegrationHeld   = "integration_held"
)

// coordinating is a coordination parent's whole path (MR2): it is Coordinating with a bounded
// reason, gets one human planning and progress Room on the primary, and never runs, triages,
// scopes, splits, arms, merges or reverts anything. Under execute, once every confirmation is
// present, it files through the service, whose gate re-reads every authority first; once filed,
// it creates each independent child Task in its own repository. It holds no active slot.
func (r *Reconciler) coordinating(ctx context.Context, t *v1alpha1.Task) error {
	if t.Status.Phase != v1alpha1.PhaseCoordinating {
		r.to(t, v1alpha1.PhaseCoordinating, coordination.ReasonAwaitingConfirmation)
		return nil
	}
	if t.Status.RoomRef == "" {
		switch err := rooms.Ensure(ctx, r.Client, r.Namespace, t.Name, t.Spec.DataClass, t.Spec.Repository); {
		case errors.Is(err, rooms.ErrForeignRoom):
			t.Status.Reason = "foreign_room" // never adopted: an operator removes it
			return nil
		case err != nil:
			return err
		}
		t.Status.RoomRef = t.Name
		return nil
	}
	cs := t.Status.Coordination
	if r.Coordination == nil || r.Cfg.Coordination.Mode != config.CoordinationExecute || !coordination.Complete(cs) {
		return nil
	}
	// §6.1: the stop object pauses intake, and filing or admitting a child is intake. On doubt too.
	if on, err := killswitch.Engaged(ctx, r.Client, r.Namespace); on || err != nil {
		return err
	}
	if t.Status.Reason != coordination.ReasonFiled {
		err := r.Coordination.File(ctx, t.Name)
		reason := coordination.Reason(err)
		switch {
		case err == nil:
			return nil // the service wrote the parent; the next pass admits its children
		case reason == "" || t.Status.Reason == coordination.ReasonFilingIncomplete:
			return err // visible issues keep the parent filing_incomplete until a replay completes it
		}
		t.Status.Reason = reason
		return nil
	}
	return r.admitCoordinationChildren(ctx, t)
}

// admitCoordinationChildren creates each filed, independent child's Task once and records it on
// the parent. A dependent child waits for its producer's landed artifact.
func (r *Reconciler) admitCoordinationChildren(ctx context.Context, t *v1alpha1.Task) error {
	cs := t.Status.Coordination
	for _, c := range cs.Plan.Children {
		i := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == c.ID })
		if i < 0 || cs.Children[i].Issue == 0 || cs.Children[i].Task != "" {
			continue
		}
		st := &cs.Children[i]
		if len(c.DependsOn) > 0 {
			st.Reason = reasonWaitingDependency
			continue
		}
		gen := max(st.Generation, 1)
		child := coordination.ChildTask(r.Namespace, r.Cfg, t, c, st.Issue, gen)
		if err := r.Client.Create(ctx, child); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		st.Task, st.Generation, st.Reason = child.Name, gen, ""
	}
	return nil
}

// integrationHeld holds an integration Task (MR10): its admission is the integration PR's, so
// until then it runs nothing.
func (r *Reconciler) integrationHeld(_ context.Context, t *v1alpha1.Task) error {
	t.Status.Reason = reasonIntegrationHeld
	return nil
}
