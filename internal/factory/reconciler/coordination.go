// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

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

// reasonWaitingDependency is a dependent child's entry while a producer has not landed.
const reasonWaitingDependency = "waiting_dependency"

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
	if err := r.admitCoordinationChildren(ctx, t); err != nil {
		return err
	}
	return r.coordinateIntegration(ctx, t)
}

// admitCoordinationChildren creates each filed child's Task once and records it on the parent.
// An independent child is created at once; a dependent child only once every required artifact
// is landed and the execute gate passes again, and it is created with those references (MR7).
func (r *Reconciler) admitCoordinationChildren(ctx context.Context, t *v1alpha1.Task) error {
	cs := t.Status.Coordination
	for _, c := range cs.Plan.Children {
		i := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == c.ID })
		if i < 0 || cs.Children[i].Issue == 0 || cs.Children[i].Task != "" {
			continue
		}
		st := &cs.Children[i]
		var refs []v1alpha1.LandedArtifact
		if len(c.DependsOn) > 0 {
			landed, err := r.landDependencies(ctx, cs, c, st)
			if err != nil {
				return err
			}
			if !landed {
				continue
			}
			if err := r.Coordination.ExecuteReady(ctx, cs, []string{c.ID}); err != nil {
				reason := coordination.Reason(err)
				if reason == "" {
					return err
				}
				st.Reason = reason
				continue
			}
			refs, _ = coordination.ConsumedArtifacts(c, cs.Artifacts)
		}
		gen := max(st.Generation, 1)
		child := coordination.ChildTask(r.Namespace, r.Cfg, t, c, st.Issue, gen)
		child.Spec.CoordinationArtifacts = refs
		if err := r.Client.Create(ctx, child); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		st.Task, st.Generation, st.Reason = child.Name, gen, ""
	}
	return nil
}

// landDependencies records each of c's required artifacts on cs once its producer's pull request
// has landed (MR7): read through the producer repository's own client, at the merge commit the
// producer Task recorded, never its head or the default branch. landed is false while a producer
// has not merged (waiting_dependency) or an artifact is not the exact declared file
// (artifact_unverified, with no reference recorded); st carries the reason.
func (r *Reconciler) landDependencies(ctx context.Context, cs *v1alpha1.CoordinationStatus, c v1alpha1.CoordinationChild, st *v1alpha1.CoordinationChildStatus) (bool, error) {
	merges := map[string]string{}
	for _, from := range c.DependsOn {
		sha, err := r.producerMerge(ctx, cs, from)
		if err != nil || sha == "" {
			st.Reason = reasonWaitingDependency
			return false, err
		}
		merges[from] = sha
	}
	for _, k := range c.Consumes {
		if _, ok := coordination.LandedRef(cs.Artifacts, k.From, k.Artifact); ok || !k.Required {
			continue
		}
		pc, okc := coordination.PlanChild(cs, k.From)
		spec, oks := coordination.ProducedSpec(pc, k.Artifact)
		sha := merges[k.From]
		if !okc || !oks || sha == "" { // validation forbids it: a consume of no landed producer never unlocks
			st.Reason = coordination.ErrArtifactUnverified.Error()
			return false, nil
		}
		fx, ok := r.forgeFor(pc.Repository)
		if !ok {
			st.Reason = coordination.ErrAccessUnverified.Error()
			return false, nil
		}
		ref, err := coordination.VerifyArtifact(ctx, fx, pc, spec, sha)
		switch {
		case errors.Is(err, coordination.ErrArtifactUnverified):
			st.Reason = coordination.ErrArtifactUnverified.Error()
			return false, nil
		case err != nil:
			return false, err
		}
		cs.Artifacts = append(cs.Artifacts, ref)
	}
	return true, nil
}

// producerMerge is child from's landed merge commit as its own Task recorded it from GitHub; ""
// until its pull request has merged.
func (r *Reconciler) producerMerge(ctx context.Context, cs *v1alpha1.CoordinationStatus, from string) (string, error) {
	i := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == from })
	if i < 0 || cs.Children[i].Task == "" {
		return "", nil
	}
	var p v1alpha1.Task
	switch err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: cs.Children[i].Task}, &p); {
	case apierrors.IsNotFound(err):
		return "", nil
	case err != nil:
		return "", err
	}
	pr := p.Status.PullRequest
	if pr == nil || pr.MergedAt == nil || p.Spec.Repository != cs.Children[i].Repository {
		return "", nil
	}
	return pr.MergeCommitSHA, nil
}

// forgeFor is repository's own factory App connection: the one a producer's artifact is read
// through. One repository's factory (no Clients) has only Forge.
func (r *Reconciler) forgeFor(repository string) (TaskForge, bool) {
	if r.Clients == nil {
		return r.Forge, r.Forge != nil
	}
	f, _, ok := r.Clients(repository)
	return f, ok
}
