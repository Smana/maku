// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/factory/tracing"
)

// The integration path's bounded reasons (MR9, MR10).
const (
	// reasonIntegrationPending is a dependent head held at its gate until an integration passes
	// for it, and an integration Task whose run its infrastructure lost.
	reasonIntegrationPending = "integration_pending"
	// reasonIntegrationPassed is a passing integration Task, and a parent Done after its final one.
	reasonIntegrationPassed = "integration_passed"
	// reasonIntegrationFailed is an integration Task whose checks failed or that reported no result.
	reasonIntegrationFailed = "integration_failed"
	// reasonIntegrationNoResult is the result reason of a run that reported no typed result of its
	// own manifest: prose, another run's result or another hash is none.
	reasonIntegrationNoResult = "integration_result_missing"
)

// integrating is an integration Task's whole path (MR10, MR17): its lineage and manifest checked
// at admission, the ordinary slot, allocation and daily reservation in queued, then one triager
// run of integrationSpec, never implementerSpec, a verifier, a PR or a merger call. Its result is
// the run's typed integration_result alone. An Escalated attempt waits for the recovery path.
func (r *Reconciler) integrating(ctx context.Context, t *v1alpha1.Task) error {
	switch t.Status.Phase {
	case "":
		return r.receivedIntegration(ctx, t)
	case v1alpha1.PhaseQueued:
		return r.queued(ctx, t)
	case v1alpha1.PhaseImplementing:
		return r.integrationRunning(ctx, t)
	}
	return nil
}

// receivedIntegration admits an integration Task: one lineage in the primary repository, its
// manifest the one its parent holds now, and its own Room on the primary.
func (r *Reconciler) receivedIntegration(ctx context.Context, t *v1alpha1.Task) error {
	_, m, err := r.integrationLineage(ctx, t)
	if reason := integrationRefusal(err); reason != "" {
		return r.end(ctx, t, v1alpha1.PhaseRejected, reason)
	}
	if err != nil {
		return err
	}
	switch err := rooms.Ensure(ctx, r.Client, r.Namespace, t.Name, t.Spec.DataClass, t.Spec.Repository); {
	case errors.Is(err, rooms.ErrForeignRoom):
		t.Status.Reason = "foreign_room"
		return nil
	case err != nil:
		return err
	}
	if r.Trace != nil && t.Status.Trace == nil {
		tr, sp := tracing.Mint()
		t.Status.Trace = &v1alpha1.TraceRef{TraceID: tr, SpanID: sp}
	}
	t.Status.RoomRef = t.Name
	t.Status.Result = &v1alpha1.IntegrationResult{State: coordination.ResultPending, ManifestHash: coordination.ManifestHash(m)}
	t.Status.ConfigHash = r.Cfg.Hash
	r.to(t, v1alpha1.PhaseQueued, "")
	return nil
}

// integrationRefusal is err's bounded reason when it refuses an integration Task's lineage.
func integrationRefusal(err error) string {
	if errors.Is(err, coordination.ErrIntegrationStale) {
		return coordination.ErrIntegrationStale.Error()
	}
	return lineageReason(err)
}

// integrationLineage proves integration Task t is its parent's, in the primary repository, with
// the manifest its parent holds now: no childID or splitter lineage, a filed coordination parent,
// the primary as its repository and Room's, and ManifestOf. It answers the parent and manifest.
func (r *Reconciler) integrationLineage(ctx context.Context, t *v1alpha1.Task) (*v1alpha1.Task, coordination.IntegrationManifest, error) {
	var none coordination.IntegrationManifest
	cp := t.Spec.CoordinationParent
	switch {
	case cp == nil || cp.ChildID != "" || t.Spec.Integration == nil:
		return nil, none, fmt.Errorf("%w: not an integration lineage", errCoordinationLineage)
	case t.Spec.Parent != nil || t.Spec.Split != nil || t.Labels[v1alpha1.LabelParent] != "" || t.Labels[v1alpha1.LabelCoordinationParent] != cp.Task:
		return nil, none, fmt.Errorf("%w: splitter or another parent's lineage", errCoordinationLineage)
	}
	var p v1alpha1.Task
	switch err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: cp.Task}, &p); {
	case apierrors.IsNotFound(err):
		return nil, none, errCoordinationParent
	case err != nil:
		return nil, none, err
	case p.Spec.Kind != v1alpha1.TaskKindCoordination || p.Status.Reason != coordination.ReasonFiled || p.Status.Coordination == nil:
		return nil, none, errCoordinationParent
	}
	cs := p.Status.Coordination
	if cs.Plan == nil || t.Spec.Repository != cs.Plan.Primary.Repository {
		return nil, none, errCrossRepositoryLineage
	}
	var room roomv1.Room
	switch err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: t.Name}, &room); {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, none, err
	case !strings.EqualFold(room.Spec.Repository, t.Spec.Repository):
		return nil, none, errCrossRepositoryLineage
	}
	m, err := coordination.ManifestOf(t, cs)
	if err != nil {
		return nil, none, err
	}
	return &p, m, nil
}

// integrationHold is queued's re-check before an integration run: the stop object, the lineage
// and manifest, and the execute gate over the parent's plan with the primary's authority re-read.
// held is true when it does not start.
func (r *Reconciler) integrationHold(ctx context.Context, t *v1alpha1.Task) (bool, error) {
	if on, err := killswitch.Engaged(ctx, r.Client, r.Namespace); on || err != nil {
		t.Status.Reason = "kill_switch"
		return true, err
	}
	p, _, err := r.integrationLineage(ctx, t)
	if reason := integrationRefusal(err); reason != "" {
		return true, r.end(ctx, t, v1alpha1.PhaseRejected, reason)
	}
	if err != nil {
		return true, err
	}
	if r.Coordination == nil {
		t.Status.Reason = coordination.ReasonPrerequisites
		return true, nil
	}
	if err := r.Coordination.ExecuteReady(ctx, p.Status.Coordination, []string{coordination.PrimaryChild(p.Status.Coordination)}); err != nil {
		reason := coordination.Reason(err)
		if reason == "" {
			return true, err
		}
		t.Status.Reason = reason
		return true, nil
	}
	return false, nil
}

// startIntegration starts t's one triager run on its current manifest.
func (r *Reconciler) startIntegration(ctx context.Context, t *v1alpha1.Task) error {
	_, m, err := r.integrationLineage(ctx, t)
	if reason := integrationRefusal(err); reason != "" {
		return r.end(ctx, t, v1alpha1.PhaseRejected, reason)
	}
	if err != nil {
		return err
	}
	return r.startRun(ctx, t, r.integrationSpec(t, m), "initial")
}

// integrationSpec is an integration run (MR10, MR17): triager on the primary, in the integration
// Task's own Room, with no branch, no PR, no landed references and the canonical manifest, whose
// exact subject SHAs are the only inputs. The v4 item 4 runtime that writes it to a read-only file,
// exports MAKU_COORDINATION_MANIFEST and checks out the exact SHAs is not shipped: execute stays
// gated until it is.
func (r *Reconciler) integrationSpec(t *v1alpha1.Task, m coordination.IntegrationManifest) runs.Spec {
	b, _ := coordination.CanonicalManifest(m) // strings and integers: never fails
	return runs.Spec{TaskID: t.Name, Role: "triager", Repository: t.Spec.Repository, BaseRef: "main",
		TaskText: IntegrationBrief(t, m), Principal: runs.PrincipalFactory, DataClass: t.Spec.DataClass,
		Model: t.Spec.Budget.Model, RoomRef: t.Status.RoomRef, Queue: runs.QueueFactory,
		MaxTokens: t.Spec.Budget.RunTokens, MaxMinutes: t.Spec.Budget.RunMinutes, Traceparent: traceparent(t), Tier: t.Spec.Budget.Tier,
		CoordinationManifest: string(b), CoordinationManifestHash: coordination.ManifestHash(m)}
}

// IntegrationBrief is an integration run's brief: factory text only. The subjects and the
// confirmed acceptance ride the claim's manifest, never the brief.
func IntegrationBrief(t *v1alpha1.Task, m coordination.IntegrationManifest) string {
	return fmt.Sprintf("Read-only integration verification %s of coordination %s (%s).\n"+
		"Run only the confirmed acceptance in the manifest at $MAKU_COORDINATION_MANIFEST, at its exact subject SHAs. "+
		"Push nothing and open no pull request. Report one %s message with state passed or failed and manifestHash %s.\n",
		t.Name, m.ParentTask, m.Purpose, envelope.KindIntegrationResult, coordination.ManifestHash(m))
}

// integrationRunning follows the one run: a typed pass of a run that succeeded is passed, a typed
// failure or no result is failed, and a run its infrastructure lost escalates integration_pending
// with no acceptance.
func (r *Reconciler) integrationRunning(ctx context.Context, t *v1alpha1.Task) error {
	run, found, err := r.observe(ctx, t)
	if err != nil {
		return err
	}
	if !found {
		return r.end(ctx, t, v1alpha1.PhaseEscalated, reasonIntegrationPending)
	}
	if err := r.boundPending(ctx, t, run); err != nil {
		return err
	}
	if !runs.Terminal(run.Phase) {
		if s, err := r.stuck(ctx, t, run); err != nil || !s {
			return err
		}
		return r.end(ctx, t, v1alpha1.PhaseEscalated, reasonIntegrationPending)
	}
	reason := r.finished(ctx, t, run)
	if reason == "" {
		return nil
	}
	current(t).Reason = reason
	if infraLost(run) {
		return r.end(ctx, t, v1alpha1.PhaseEscalated, reasonIntegrationPending)
	}
	want := ""
	if t.Status.Result != nil {
		want = t.Status.Result.ManifestHash
	}
	res, ok, err := r.integrationResult(ctx, t, want)
	if err != nil {
		return err
	}
	switch {
	case ok && res.State == coordination.ResultPassed && run.Phase == "Succeeded":
		t.Status.Result = &res
		return r.end(ctx, t, v1alpha1.PhaseDone, reasonIntegrationPassed)
	case ok && res.State == coordination.ResultFailed:
		t.Status.Result = &res
	default:
		t.Status.Result = &v1alpha1.IntegrationResult{State: coordination.ResultFailed, Reason: reasonIntegrationNoResult, ManifestHash: want}
	}
	return r.end(ctx, t, v1alpha1.PhaseDone, reasonIntegrationFailed)
}

// integrationResult is the newest integration_result of t's current run naming manifest hash
// want, as the broker stamped it: the run's own agent actor by the client origin. Nothing else in
// the room counts.
func (r *Reconciler) integrationResult(ctx context.Context, t *v1alpha1.Task, want string) (v1alpha1.IntegrationResult, bool, error) {
	cur := current(t)
	evs, _, err := r.roomTail(ctx, t.Status.RoomRef, cur.StartSeq, func(e envelope.Event) bool {
		return e.Type == envelope.Message && e.RunID == cur.ID && e.Origin == envelope.OriginClient &&
			e.Actor.Kind == envelope.ActorAgent && e.Actor.ID == "agent:"+cur.ID
	})
	if err != nil {
		return v1alpha1.IntegrationResult{}, false, err
	}
	for i := len(evs) - 1; i >= 0; i-- {
		var p envelope.MessagePayload
		if json.Unmarshal(evs[i].Payload, &p) != nil || p.Kind != envelope.KindIntegrationResult || p.Integration == nil {
			continue
		}
		if p.Integration.Validate() == nil && want != "" && p.Integration.ManifestHash == want {
			return integrationResultOf(*p.Integration), true, nil
		}
	}
	return v1alpha1.IntegrationResult{}, false, nil
}

// integrationResultOf is the envelope's mirror as the CRD's result.
func integrationResultOf(e envelope.IntegrationResult) v1alpha1.IntegrationResult {
	return v1alpha1.IntegrationResult{State: e.State, Reason: e.Reason, ManifestHash: e.ManifestHash}
}

// childFact is what the parent reads of one child's Task: its merge, once landed, and the head a
// held dependent waits on.
type childFact struct {
	task           *v1alpha1.Task
	mergeCommitSHA string
}

// childFacts reads each child's current Task once.
func (r *Reconciler) childFacts(ctx context.Context, cs *v1alpha1.CoordinationStatus) (map[string]childFact, error) {
	out := map[string]childFact{}
	for _, st := range cs.Children {
		if st.Task == "" {
			continue
		}
		var c v1alpha1.Task
		switch err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: st.Task}, &c); {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return nil, err
		}
		f := childFact{task: &c}
		if pr := c.Status.PullRequest; pr != nil && pr.MergedAt != nil && pr.MergeCommitSHA != "" && c.Spec.Repository == st.Repository {
			f.mergeCommitSHA = pr.MergeCommitSHA
		}
		out[st.ID] = f
	}
	return out, nil
}

// coordinateIntegration is the parent's integration pass (MR9, MR10): it records the landing
// order, absorbs each attempt's typed result from its Task, drops a release whose head moved,
// then schedules at most one attempt: a held dependent's pre-merge check once its dependencies
// landed, or the final check once every child landed. The parent stays filed throughout: its
// children's lineage reads it.
func (r *Reconciler) coordinateIntegration(ctx context.Context, t *v1alpha1.Task) error {
	cs := t.Status.Coordination
	facts, err := r.childFacts(ctx, cs)
	if err != nil {
		return err
	}
	merges := map[string]string{}
	for _, c := range cs.Plan.Children {
		if sha := facts[c.ID].mergeCommitSHA; sha != "" {
			merges[c.ID] = sha
			if !slices.Contains(cs.MergeOrder, c.ID) {
				cs.MergeOrder = append(cs.MergeOrder, c.ID)
			}
		}
	}
	if err := r.absorbAttempts(ctx, cs); err != nil {
		return err
	}
	for i := range cs.Children {
		st := &cs.Children[i]
		f, ok := facts[st.ID]
		if rel := st.IntegrationRelease; rel != nil && ok && f.mergeCommitSHA == "" &&
			(f.task.Status.PullRequest == nil || f.task.Status.PullRequest.HeadSHA != rel.HeadSHA) {
			st.IntegrationRelease = nil // MR9: the head moved, so the release is stale
		}
	}
	if slices.ContainsFunc(cs.IntegrationAttempts, func(a v1alpha1.IntegrationAttempt) bool { return a.State == coordination.ResultPending }) {
		return nil
	}
	if len(merges) == len(cs.Plan.Children) {
		if cs.Acceptance != nil {
			return nil
		}
		m, err := coordination.FinalManifest(t.Name, cs, merges)
		if err != nil {
			return nil // a merge this pass cannot bind exactly: nothing runs on it
		}
		return r.startAttempt(ctx, t, m, coordination.PrimaryChild(cs))
	}
	if !cs.Plan.Integration.EffectiveBeforeDependents() {
		return nil
	}
	for _, c := range cs.Plan.Children {
		f, ok := facts[c.ID]
		if len(c.DependsOn) == 0 || !ok || f.mergeCommitSHA != "" || f.task.Status.Phase != v1alpha1.PhaseAwaitingCI ||
			f.task.Status.Reason != reasonIntegrationPending || f.task.Status.PullRequest == nil {
			continue
		}
		m, err := coordination.BeforeDependentManifest(t.Name, cs, c.ID, f.task.Status.PullRequest.HeadSHA, merges)
		if err != nil {
			continue // a dependency not landed, or a head not exact: it stays held
		}
		return r.startAttempt(ctx, t, m, c.ID)
	}
	return nil
}

// startAttempt creates one integration Task for m once: never for a manifest already attempted,
// past maxRuns, or behind a refused execute gate, whose reason lands on child's entry.
func (r *Reconciler) startAttempt(ctx context.Context, t *v1alpha1.Task, m coordination.IntegrationManifest, child string) error {
	cs := t.Status.Coordination
	hash := coordination.ManifestHash(m)
	if slices.ContainsFunc(cs.IntegrationAttempts, func(a v1alpha1.IntegrationAttempt) bool { return a.ManifestHash == hash }) {
		return nil // one attempt per manifest: a retry is the recovery path's
	}
	hold := func(reason string) {
		if i := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == child }); i >= 0 {
			cs.Children[i].Reason = reason
		}
	}
	if err := coordination.IntegrationAttemptAllowed(cs); err != nil {
		hold(coordination.ReasonOf(err))
		return nil
	}
	if err := r.Coordination.ExecuteReady(ctx, cs, []string{coordination.PrimaryChild(cs)}); err != nil {
		reason := coordination.Reason(err)
		if reason == "" {
			return err
		}
		hold(reason)
		return nil
	}
	it := coordination.IntegrationTask(r.Namespace, r.Cfg, t, m, len(cs.IntegrationAttempts)+1)
	if err := r.Client.Create(ctx, it); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	cs.IntegrationAttempts = v1alpha1.AppendNewest(cs.IntegrationAttempts, v1alpha1.IntegrationAttempt{Task: it.Name, Purpose: m.Purpose,
		PlanHash: m.PlanHash, ManifestHash: hash, State: coordination.ResultPending, Subjects: m.Subjects,
		RunMicroUSD: it.Spec.Budget.RunMicroUSD}, v1alpha1.MaxCoordinationIntegrationAttempts)
	return nil
}

// absorbAttempts records each pending attempt's outcome from its Task's typed result, never a
// comment, room message or green child: a pass for the current plan releases a pre-merge
// dependent's exact head (MR9).
func (r *Reconciler) absorbAttempts(ctx context.Context, cs *v1alpha1.CoordinationStatus) error {
	for i := range cs.IntegrationAttempts {
		a := &cs.IntegrationAttempts[i]
		if a.State != coordination.ResultPending {
			continue
		}
		var it v1alpha1.Task
		switch err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: a.Task}, &it); {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return err
		}
		a.CostMicroUSD = it.Status.Usage.CostMicroUSD
		res := it.Status.Result
		if res == nil || res.ManifestHash != a.ManifestHash || it.Spec.Integration == nil || it.Spec.Integration.PlanHash != a.PlanHash {
			continue
		}
		if res.State == coordination.ResultPending {
			if it.Status.Phase == v1alpha1.PhaseEscalated {
				a.Reason = reasonIntegrationPending
			}
			continue
		}
		a.State, a.Reason = res.State, res.Reason
		if a.State != coordination.ResultPassed || a.PlanHash != cs.PlanHash || a.Purpose != coordination.PurposeBeforeDependentMerge {
			continue
		}
		for _, s := range a.Subjects {
			j := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == s.ChildID })
			if s.HeadSHA != "" && j >= 0 {
				cs.Children[j].IntegrationRelease = &v1alpha1.IntegrationRelease{ChildID: s.ChildID, HeadSHA: s.HeadSHA,
					IntegrationTask: a.Task, ManifestHash: a.ManifestHash}
			}
		}
	}
	return nil
}
