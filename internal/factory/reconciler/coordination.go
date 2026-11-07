// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/narrate"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/factory/tracing"
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
	if t.Status.Trace == nil { // the parent's root span, exported at its end with its family's links
		tr, sp := tracing.Mint()
		t.Status.Trace = &v1alpha1.TraceRef{TraceID: tr, SpanID: sp}
	}
	if t.Status.Phase != v1alpha1.PhaseCoordinating {
		r.to(t, v1alpha1.PhaseCoordinating, coordination.ReasonAwaitingConfirmation)
		return nil
	}
	if t.Status.RoomRef == "" {
		switch err := rooms.Ensure(ctx, r.Client, r.Namespace, t.Name, t.Spec.DataClass, t.Spec.Repository, coordinationWatchers(r.Cfg)...); {
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
	// MR12: an affected old generation stops through the ordinary stop machinery at once, whatever
	// else holds the family; stopping widens nothing.
	if err := r.supersede(ctx, cs); err != nil {
		return err
	}
	// MR11: a parent factory/stop pauses the family's future scheduling and integration releases
	// (integrationGate reads it too); it borrows no repository's authority to revoke a running
	// child, whose own stop and the global kill switch keep theirs.
	if parentStopped(t) {
		return nil
	}
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
		r.coordinationRefused(ctx, t.Spec.Repository, &t.Status.Reason, reason)
		return nil
	}
	if err := r.replaceGenerations(ctx, t); err != nil {
		return err
	}
	if err := r.admitCoordinationChildren(ctx, t); err != nil {
		return err
	}
	if err := r.coordinateIntegration(ctx, t); err != nil {
		return err
	}
	return r.proposeRecovery(ctx, t)
}

// coordinationSpan is a parent's span data (v4 observability): its plan version and hash, which
// tracing cuts to a prefix, and links to each child's and integration Task's root span; nil for any
// other Task. A family Task not found is left out: its span is no evidence of anything.
func (r *Reconciler) coordinationSpan(ctx context.Context, t *v1alpha1.Task) *tracing.Coordination {
	cs := t.Status.Coordination
	if t.Spec.Kind != v1alpha1.TaskKindCoordination || cs == nil {
		return nil
	}
	c := &tracing.Coordination{PlanVersion: cs.PlanVersion, PlanHash: cs.PlanHash}
	var names []string
	for _, st := range cs.Children {
		names = append(names, st.Task)
	}
	for _, a := range cs.IntegrationAttempts {
		names = append(names, a.Task)
	}
	for _, n := range names {
		var k v1alpha1.Task
		if n == "" || r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: n}, &k) != nil || k.Status.Trace == nil {
			continue
		}
		c.Links = append(c.Links, tracing.Link{TraceID: k.Status.Trace.TraceID, SpanID: k.Status.Trace.SpanID})
	}
	return c
}

// parentStopped is a coordination parent a maintainer's factory/stop, or a hand-set stop, paused.
func parentStopped(t *v1alpha1.Task) bool { return t.Annotations[v1alpha1.AnnotationStop] != "" }

// coordinationRejected ends coordination Task t Rejected for a lineage refusal, counted once on its
// repository with the end.
func (r *Reconciler) coordinationRejected(ctx context.Context, t *v1alpha1.Task, reason string) error {
	repo := t.Spec.Repository
	record(ctx, func(ctx context.Context) { r.Metrics.CoordinationAdmissionRefused(ctx, repo, reason) })
	return r.end(ctx, t, v1alpha1.PhaseRejected, reason)
}

// coordinationRefused sets *reason to code, counting the refusal on repository once: when the
// reason changes, not on every pass that finds it standing.
func (r *Reconciler) coordinationRefused(ctx context.Context, repository string, reason *string, code string) {
	if *reason != code {
		record(ctx, func(ctx context.Context) { r.Metrics.CoordinationAdmissionRefused(ctx, repository, code) })
	}
	*reason = code
}

// proposeRecovery persists the parent's one proposed recovery (MR11), selected by
// coordination.ProposeRecovery from the current child and integration Tasks; a standing proposal
// keeps its createdAt.
func (r *Reconciler) proposeRecovery(ctx context.Context, t *v1alpha1.Task) error {
	cs := t.Status.Coordination
	var kids, its []v1alpha1.Task
	get := func(name string, into *[]v1alpha1.Task) error {
		if name == "" {
			return nil
		}
		var x v1alpha1.Task
		switch err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: name}, &x); {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return err
		}
		*into = append(*into, x)
		return nil
	}
	for _, st := range cs.Children {
		if err := get(st.Task, &kids); err != nil {
			return err
		}
	}
	for _, a := range cs.IntegrationAttempts {
		if a.PlanHash != cs.PlanHash {
			continue
		}
		if err := get(a.Task, &its); err != nil {
			return err
		}
	}
	p := coordination.ProposeRecovery(t, kids, its, r.Now())
	if !coordination.SameRecovery(p, cs.Recovery) {
		if old := cs.Recovery; old != nil { // the proposal stood until now: that is the recovery wait
			record(ctx, func(ctx context.Context) {
				r.Metrics.CoordinationWait(ctx, t.Spec.Repository, "recovery", r.Now().Sub(old.CreatedAt.Time))
			})
		}
		cs.Recovery = p
	}
	return nil
}

// tellCoordinationParent records child t's revert on its coordination parent once that parent is
// Done (MR11): one bounded post-completion incident and its narration on the parent issue, by a
// conflict-retried status write that never changes the terminal phase or reverts a sibling. Before
// Done the parent reads the Reverted child itself: it pauses downstream work and proposes
// revise_plan.
func (r *Reconciler) tellCoordinationParent(ctx context.Context, t *v1alpha1.Task, revert int) {
	cp := t.Spec.CoordinationParent
	if cp == nil || cp.ChildID == "" || t.Spec.Kind != "" {
		return
	}
	repo, class := t.Spec.Repository, t.Spec.PredictedClass
	record(ctx, func(ctx context.Context) { r.Metrics.CoordinationChild(ctx, repo, class, "reverted") })
	sha := ""
	if pr := t.Status.PullRequest; pr != nil && sha40.MatchString(pr.MergeCommitSHA) {
		sha = pr.MergeCommitSHA
	}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var p v1alpha1.Task // fresh on every attempt: the conflict is a newer write
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: cp.Task}, &p); err != nil {
			return client.IgnoreNotFound(err)
		}
		cs := p.Status.Coordination
		if p.Spec.Kind != v1alpha1.TaskKindCoordination || p.Status.Phase != v1alpha1.PhaseDone || cs == nil ||
			slices.ContainsFunc(cs.Incidents, func(i v1alpha1.CoordinationIncident) bool { return i.ChildID == cp.ChildID && i.MergeCommitSHA == sha }) {
			return nil
		}
		cs.Incidents = v1alpha1.AppendNewest(cs.Incidents, v1alpha1.CoordinationIncident{ChildID: cp.ChildID, Reason: "reverted",
			MergeCommitSHA: sha, At: metav1.NewTime(r.Now())}, v1alpha1.MaxCoordinationIncidents)
		narrateOn(&p, p.Spec.Issue, narrate.CoordinationIncident(p.Name, cp.ChildID, revert)) // through the primary's own App
		return r.Client.Status().Update(ctx, &p)
	})
	if err != nil {
		r.log().Warn("the coordination parent was not told of a reverted child", "task.id", t.Name, "parent", cp.Task, "err", err)
	}
}

// supersede marks each pending generation's old Task superseded, once: the stop the reconciler
// already honors revokes its live run and ends it Stopped/superseded. An unaffected sibling is
// never named, so its authority is untouched.
func (r *Reconciler) supersede(ctx context.Context, cs *v1alpha1.CoordinationStatus) error {
	if cs == nil {
		return nil
	}
	for _, pg := range cs.PendingGenerations {
		if pg.StoppingTask == "" {
			continue
		}
		var old v1alpha1.Task
		switch err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: pg.StoppingTask}, &old); {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return err
		}
		if v1alpha1.TerminalPhase(old.Status.Phase) || old.Annotations[v1alpha1.AnnotationStop] != "" {
			continue
		}
		patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{v1alpha1.AnnotationStop: "superseded"}}})
		if err := r.Client.Patch(ctx, &old, client.RawPatch(types.MergePatchType, patch)); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

// replaceGenerations releases each pending generation whose old generation has drained (MR8): the
// child's entry is reset to the new generation under the current fingerprint, and the ordinary
// admission below creates CoordinationChildKey(parent, id, version, generation) with the execute
// gate re-read and only what the child's lifetime allocation still holds. It is reached only once
// the current version's confirmations exist.
func (r *Reconciler) replaceGenerations(ctx context.Context, t *v1alpha1.Task) error {
	cs := t.Status.Coordination
	var kept []v1alpha1.PendingGeneration
	for _, pg := range cs.PendingGenerations {
		drained, err := r.generationDrained(ctx, t, pg.ChildID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == pg.ChildID })
		if _, inPlan := coordination.PlanChild(cs, pg.ChildID); i < 0 || !inPlan {
			continue // a child the plan dropped: nothing replaces it
		}
		if !drained {
			cs.Children[i].Reason = coordination.ReasonGenerationPending
			kept = append(kept, pg)
			continue
		}
		st := &cs.Children[i]
		st.Task, st.Generation, st.Reason = "", pg.Generation, ""
		st.Fingerprint = coordination.ChildFingerprint(*cs.Plan, st.ID)
		st.IntegrationRelease = nil
	}
	cs.PendingGenerations = kept
	return nil
}

// generationDrained is whether child id's pending generation may start (MR8): its old Task is
// terminal, no AgentRun of it is live, the day ledger holds no reservation for any of its runs or
// its deterministic next one, and its usage has settled. Only then are its spend and allocation
// final, so the replacement's share never overlaps it.
func (r *Reconciler) generationDrained(ctx context.Context, parent *v1alpha1.Task, id string) (bool, error) {
	pg := coordination.Pending(parent.Status.Coordination, id)
	if pg == nil {
		return true, nil
	}
	var old v1alpha1.Task
	switch err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: pg.StoppingTask}, &old); {
	case apierrors.IsNotFound(err):
		return false, nil // never assumed gone: its runs and reservation are unaccounted
	case err != nil:
		return false, err
	}
	if !v1alpha1.TerminalPhase(old.Status.Phase) || (len(old.Status.Runs) > 0 && !old.Status.UsageSettled) {
		return false, nil
	}
	all, err := r.Runs.List(ctx)
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(all, func(run runs.Run) bool { return run.TaskID == old.Name && !runs.Terminal(run.Phase) }) {
		return false, nil
	}
	held, err := r.reservationHeld(ctx, &old)
	return !held, err
}

// reservationHeld is whether any day ledger still holds a reservation for one of t's runs or its
// deterministic next run, read live through the ledger client.
func (r *Reconciler) reservationHeld(ctx context.Context, t *v1alpha1.Task) (bool, error) {
	if r.Ledger == nil {
		return true, nil // unread is held
	}
	var l corev1.ConfigMapList
	if err := r.Ledger.List(ctx, &l, client.InNamespace(r.Namespace)); err != nil {
		return true, err
	}
	for _, cm := range l.Items {
		if _, ok := ledger.Day(cm.Name, ledger.Prefix); !ok {
			continue
		}
		for n := 0; n <= len(t.Status.Runs); n++ {
			if _, ok := cm.Data[ledger.Reserved+runID(t.Name, n)]; ok {
				return true, nil
			}
		}
	}
	return false, nil
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
				r.coordinationRefused(ctx, c.Repository, &st.Reason, reason)
				continue
			}
			refs, _ = coordination.ConsumedArtifacts(c, cs.Artifacts)
		}
		gen := max(st.Generation, 1)
		child := coordination.ChildTask(r.Namespace, r.Cfg, t, c, st.Issue, gen)
		child.Spec.CoordinationArtifacts = refs
		if gen > 1 {
			// A replacement: the execute gate is re-read for it, and it holds only what every
			// earlier generation left of the lifetime allocation (MR8).
			if len(c.DependsOn) == 0 {
				if err := r.Coordination.ExecuteReady(ctx, cs, []string{c.ID}); err != nil {
					reason := coordination.Reason(err)
					if reason == "" {
						return err
					}
					r.coordinationRefused(ctx, c.Repository, &st.Reason, reason)
					continue
				}
			}
			tasks, err := coordination.ChildTasks(ctx, r.Client, r.Namespace, t.Name)
			if err != nil {
				return err
			}
			coordination.LimitAllocation(child, c.BudgetMicroUSD-coordination.LifetimeSpent(tasks, c.ID))
		}
		if err := r.Client.Create(ctx, child); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		if err := r.recordAdmitted(ctx, t, c); err != nil {
			return err
		}
		st.Task, st.Generation, st.Reason = child.Name, gen, ""
		st.Fingerprint, st.AllocationMicroUSD = coordination.ChildFingerprint(*cs.Plan, c.ID), child.Spec.Budget.TaskMicroUSD
	}
	return nil
}

// recordAdmitted counts child c admitted once its parent's write records it and, for a dependent,
// the artifact wait it ends: from its earliest producer's admission (that Task's creation) to now.
func (r *Reconciler) recordAdmitted(ctx context.Context, parent *v1alpha1.Task, c v1alpha1.CoordinationChild) error {
	record(ctx, func(ctx context.Context) { r.Metrics.CoordinationChild(ctx, c.Repository, c.Class, "admitted") })
	if len(c.DependsOn) == 0 {
		return nil
	}
	tasks, err := coordination.ChildTasks(ctx, r.Client, r.Namespace, parent.Name)
	if err != nil {
		return err
	}
	var since time.Time
	for _, k := range tasks {
		if cp := k.Spec.CoordinationParent; cp != nil && slices.Contains(c.DependsOn, cp.ChildID) &&
			(since.IsZero() || k.CreationTimestamp.Before(&metav1.Time{Time: since})) {
			since = k.CreationTimestamp.Time
		}
	}
	if !since.IsZero() {
		record(ctx, func(ctx context.Context) {
			r.Metrics.CoordinationWait(ctx, parent.Spec.Repository, "artifact", r.Now().Sub(since))
		})
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
	if pr == nil || pr.MergedAt == nil || p.Spec.Repository != cs.Children[i].Repository || p.Status.Phase == v1alpha1.PhaseReverted {
		return "", nil // MR11: a reverted producer no longer stands for anything downstream
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
