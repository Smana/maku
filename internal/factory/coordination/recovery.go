// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// The recovery codes (MR11): status.coordination.recovery names exactly one.
const (
	RecoveryRetryChild       = "retry_child"
	RecoveryResumeChild      = "resume_child"
	RecoveryRevisePlan       = "revise_plan"
	RecoveryIncreaseBudget   = "increase_budget"
	RecoveryRetryProducer    = "retry_producer"
	RecoveryRetryDependent   = "retry_dependent"
	RecoveryRetryIntegration = "retry_integration"
	RecoveryRerunIntegration = "rerun_integration"
	RecoveryAuthorizeChild   = "authorize_child"
)

// The recovery refusals.
var (
	// ErrRecoveryNotProposed is a recovery call naming a code the parent does not propose now.
	ErrRecoveryNotProposed = errors.New("recovery_not_proposed")
	// ErrPlanRevisionRequired is revise_plan or increase_budget through the recovery route: both
	// are a new plan version and its confirmations, never an action the parent takes (MR11).
	ErrPlanRevisionRequired = errors.New("plan_revision_required")
)

// authorityReasons are the repository-check refusals a held child shows: each proposes
// authorize_child, the authority to restore before anything runs there again (MR6).
var authorityReasons = []string{ErrAccessUnverified.Error(), ErrRepositoryNotOwned.Error(), ErrRepositoryPermission.Error(),
	ErrPolicyChanged.Error(), ErrAppUnavailable.Error(), ErrClassNotAllowed.Error(), ErrRepositoryNotAllowed.Error()}

// The reasons a child's or an integration's failure carries when its Task has none of its own.
const (
	reasonPRClosed       = "pull_request_closed"
	reasonRejected       = "rejected"
	reasonReverted       = "reverted"
	reasonStopped        = "stopped"
	reasonEscalated      = "escalated"
	reasonHeadMoved      = "head_moved"
	reasonIntegrationRed = "integration_failed"
	reasonIntegrationInf = "integration_pending"
)

// budgetReasons are an escalation that only more allocation clears.
var budgetReasons = []string{"allocation_exhausted", "budget-task"}

// ProposeRecovery is parent's one proposed next action (MR11), selected from persisted control
// state by the design's fixed table, never by a model: the first failed child in mergeOrder, then
// the newest integration attempt of the current plan, then budget. children are the parent's
// current child Tasks, integrations its integration Tasks. A child with a pending generation is
// already being replaced, and a superseded Task is no failure. A terminal parent proposes nothing:
// a later revert there is an incident. It never bundles a second action.
func ProposeRecovery(parent *v1alpha1.Task, children []v1alpha1.Task, integrations []v1alpha1.Task, now time.Time) *v1alpha1.RecoveryProposal {
	cs := parent.Status.Coordination
	if cs == nil || cs.Plan == nil || v1alpha1.TerminalPhase(parent.Status.Phase) {
		return nil
	}
	byName := map[string]*v1alpha1.Task{}
	for i := range children {
		byName[children[i].Name] = &children[i]
	}
	for i := range integrations {
		byName[integrations[i].Name] = &integrations[i]
	}
	stamp := func(p *v1alpha1.RecoveryProposal) *v1alpha1.RecoveryProposal {
		p.PlanVersion, p.CreatedAt = cs.Plan.Version, metav1.NewTime(now)
		p.RequiresConfirmation = slices.Contains([]string{RecoveryRevisePlan, RecoveryIncreaseBudget, RecoveryAuthorizeChild}, p.Code)
		return p
	}
	var budget *v1alpha1.RecoveryProposal
	for _, id := range mergeOrdered(*cs.Plan) {
		p := childFailure(cs, id, byName)
		switch {
		case p == nil:
		case p.Code == RecoveryIncreaseBudget:
			if budget == nil {
				budget = p
			}
		default:
			return stamp(p)
		}
	}
	if p := integrationFailure(cs, byName); p != nil {
		return stamp(p)
	}
	if budget != nil {
		return stamp(budget)
	}
	for _, st := range cs.Children {
		if st.Reason == ErrIntegrationRunsExhausted.Error() {
			return stamp(&v1alpha1.RecoveryProposal{Code: RecoveryIncreaseBudget, ChildID: st.ID, Reason: st.Reason})
		}
	}
	return nil
}

// mergeOrdered is p's child ids in mergeOrder, then any it omits in plan order.
func mergeOrdered(p v1alpha1.CoordinationPlan) []string {
	out := slices.Clone(p.MergeOrder)
	for _, c := range p.Children {
		if !slices.Contains(out, c.ID) {
			out = append(out, c.ID)
		}
	}
	return out
}

// childFailure is child id's failure row, nil for none: its current Task's terminal or held
// state, then its entry's authority refusal, then an artifact a dependent could not verify at its
// landed merge.
func childFailure(cs *v1alpha1.CoordinationStatus, id string, byName map[string]*v1alpha1.Task) *v1alpha1.RecoveryProposal {
	i := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == id })
	if i < 0 || Pending(cs, id) != nil {
		return nil
	}
	st := cs.Children[i]
	unchanged := st.Fingerprint == ChildFingerprint(*cs.Plan, id)
	retry := func(code, reason string) *v1alpha1.RecoveryProposal {
		if !unchanged {
			code = RecoveryRevisePlan
		}
		return &v1alpha1.RecoveryProposal{Code: code, ChildID: id, Reason: reason}
	}
	if t := byName[st.Task]; t != nil && st.Task != "" {
		reason := t.Status.Reason
		switch t.Status.Phase {
		case v1alpha1.PhaseClosed:
			return retry(RecoveryRetryChild, or(reason, reasonPRClosed))
		case v1alpha1.PhaseRejected:
			return &v1alpha1.RecoveryProposal{Code: RecoveryRevisePlan, ChildID: id, Reason: or(reason, reasonRejected)}
		case v1alpha1.PhaseReverted:
			return &v1alpha1.RecoveryProposal{Code: RecoveryRevisePlan, ChildID: id, Reason: reasonReverted}
		case v1alpha1.PhaseStopped:
			if reason == "superseded" {
				return nil
			}
			return retry(RecoveryResumeChild, or(reason, reasonStopped))
		case v1alpha1.PhaseEscalated:
			if slices.Contains(budgetReasons, reason) {
				return &v1alpha1.RecoveryProposal{Code: RecoveryIncreaseBudget, ChildID: id, Reason: reason}
			}
			return retry(RecoveryResumeChild, or(reason, reasonEscalated))
		}
		if !v1alpha1.TerminalPhase(t.Status.Phase) && slices.Contains(authorityReasons, reason) {
			return &v1alpha1.RecoveryProposal{Code: RecoveryAuthorizeChild, ChildID: id, Reason: reason}
		}
	}
	if slices.Contains(authorityReasons, st.Reason) {
		return &v1alpha1.RecoveryProposal{Code: RecoveryAuthorizeChild, ChildID: id, Reason: st.Reason}
	}
	for _, d := range cs.Plan.Children {
		j := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == d.ID })
		if j < 0 || cs.Children[j].Reason != ErrArtifactUnverified.Error() {
			continue
		}
		if slices.ContainsFunc(d.Consumes, func(k v1alpha1.ArtifactConsumer) bool { return k.From == id && k.Required }) {
			return retry(RecoveryRetryProducer, ErrArtifactUnverified.Error())
		}
	}
	return nil
}

// integrationFailure is the integration row, nil for none: the newest attempt of the current plan
// red (retry_dependent before a merge while that head is still the dependent's, revise_plan for the
// final), its run lost to infrastructure (retry_integration), or a released head that moved with no
// attempt on the new head (rerun_integration). A failed manifest is never retried by itself.
func integrationFailure(cs *v1alpha1.CoordinationStatus, byName map[string]*v1alpha1.Task) *v1alpha1.RecoveryProposal {
	var newest *v1alpha1.IntegrationAttempt
	for i := range cs.IntegrationAttempts {
		if cs.IntegrationAttempts[i].PlanHash == cs.PlanHash {
			newest = &cs.IntegrationAttempts[i]
		}
	}
	if newest != nil {
		a := *newest
		it := byName[a.Task]
		switch {
		case a.State == ResultPending && (a.Reason == reasonIntegrationInf || (it != nil && it.Status.Phase == v1alpha1.PhaseEscalated)):
			return &v1alpha1.RecoveryProposal{Code: RecoveryRetryIntegration, IntegrationTask: a.Task, Reason: reasonIntegrationInf}
		case a.State == ResultFailed && a.Purpose == PurposeFinal:
			return &v1alpha1.RecoveryProposal{Code: RecoveryRevisePlan, IntegrationTask: a.Task, Reason: or(a.Reason, reasonIntegrationRed)}
		case a.State == ResultFailed:
			for _, s := range a.Subjects {
				if s.HeadSHA != "" && headOf(cs, s.ChildID, byName) == s.HeadSHA {
					return &v1alpha1.RecoveryProposal{Code: RecoveryRetryDependent, ChildID: s.ChildID, IntegrationTask: a.Task,
						Reason: or(a.Reason, reasonIntegrationRed)}
				}
			}
		}
	}
	for _, id := range mergeOrdered(*cs.Plan) {
		head := headOf(cs, id, byName)
		if head == "" || Pending(cs, id) != nil {
			continue
		}
		passed, onHead := false, false
		for _, a := range cs.IntegrationAttempts {
			if a.PlanHash != cs.PlanHash || a.Purpose != PurposeBeforeDependentMerge {
				continue
			}
			for _, s := range a.Subjects {
				if s.ChildID != id || s.HeadSHA == "" {
					continue
				}
				passed = passed || (a.State == ResultPassed && s.HeadSHA != head)
				onHead = onHead || s.HeadSHA == head
			}
		}
		if passed && !onHead {
			return &v1alpha1.RecoveryProposal{Code: RecoveryRerunIntegration, ChildID: id, Reason: reasonHeadMoved}
		}
	}
	return nil
}

// headOf is child id's current Task's open pull request head, "" once merged or with none.
func headOf(cs *v1alpha1.CoordinationStatus, id string, byName map[string]*v1alpha1.Task) string {
	i := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == id })
	if i < 0 || cs.Children[i].Task == "" {
		return ""
	}
	t := byName[cs.Children[i].Task]
	if t == nil || t.Status.PullRequest == nil || t.Status.PullRequest.MergedAt != nil {
		return ""
	}
	return t.Status.PullRequest.HeadSHA
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// SameRecovery is whether a and b propose the same action for the same reason under the same plan:
// a parent keeps its proposal's createdAt while it stands.
func SameRecovery(a, b *v1alpha1.RecoveryProposal) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Code == b.Code && a.ChildID == b.ChildID && a.IntegrationTask == b.IntegrationTask && a.Reason == b.Reason && a.PlanVersion == b.PlanVersion
}

// Recover performs parentTask's one proposed recovery, by actor, exactly as code names it under
// planVersion (MR11). The authority is the selected ordinary path's: a child action needs that
// child's own maintainer holding GitHub write on its repository now; an integration or plan action
// the primary's maintainer holding write there. It binds the current plan version (else
// stale_plan) and the current proposal (else recovery_not_proposed), then takes one action through
// the existing machinery: a child retry is that child's next generation through the generation
// barrier, authorize_child a fresh authorization at the repository's current fence, an
// integration retry one new attempt within maxRuns behind the execute gate. revise_plan and
// increase_budget are a new plan version, never taken here. Nothing here reverts, merges or
// reaches another repository's running child.
func (s *Service) Recover(ctx context.Context, actor Actor, parentTask, code string, planVersion int32) error {
	cfg := s.config()
	switch {
	case modeOf(cfg) == config.CoordinationOff:
		return ErrCoordinationOff
	case actor.LinkedGitHubLogin == "":
		return fmt.Errorf("%w: no linked GitHub login", ErrRepositoryPermission)
	case modeOf(cfg) != config.CoordinationExecute:
		return ErrNotExecuting
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		t, err := s.parent(ctx, parentTask)
		if err != nil {
			return err
		}
		if v1alpha1.TerminalPhase(t.Status.Phase) {
			return ErrParentTerminal
		}
		cs := t.Status.Coordination
		rec := cs.Recovery
		switch {
		case planVersion != cs.Plan.Version:
			return refuse(ErrStalePlan, "planVersion", "not the current plan version")
		case rec == nil || rec.Code != code || rec.PlanVersion != cs.Plan.Version:
			return ErrRecoveryNotProposed
		}
		if err := s.recoveryAuthority(ctx, cfg, actor, cs, rec); err != nil {
			return err
		}
		if err := s.recoverOnce(ctx, cfg, actor, t, rec); err != nil {
			return err
		}
		cs.Recovery = nil // resolving it may reveal the next one, never a bundled playbook
		return s.Client.Status().Update(ctx, t)
	})
}

// childCodes are the recoveries a child's own maintainer takes.
var childCodes = []string{RecoveryRetryChild, RecoveryResumeChild, RecoveryRetryProducer, RecoveryRetryDependent, RecoveryAuthorizeChild}

// recoveryAuthority is the selected path's authority, read now.
func (s *Service) recoveryAuthority(ctx context.Context, cfg *config.Config, actor Actor, cs *v1alpha1.CoordinationStatus, rec *v1alpha1.RecoveryProposal) error {
	p := *cs.Plan
	if slices.Contains(childCodes, rec.Code) {
		c, ok := find(p, rec.ChildID)
		switch {
		case !ok:
			return ErrRecoveryNotProposed
		case !strings.EqualFold(MaintainerLogin(c), actor.LinkedGitHubLogin) || !cfg.RepositoryFor(c.Repository).IsMaintainer(actor.LinkedGitHubLogin):
			return fmt.Errorf("%w: not the child's maintainer", ErrRepositoryPermission)
		}
		_, err := s.check(ctx, cfg, actor, c, nil)
		return err
	}
	pc, ok := find(p, primaryChildID(p))
	if !ok || !cfg.RepositoryFor(p.Primary.Repository).IsMaintainer(actor.LinkedGitHubLogin) {
		return fmt.Errorf("%w: not a maintainer of the primary", ErrRepositoryPermission)
	}
	_, err := s.check(ctx, cfg, actor, pc, nil)
	return err
}

// recoverOnce takes rec's one action on parent t's status, written by the caller.
func (s *Service) recoverOnce(ctx context.Context, cfg *config.Config, actor Actor, t *v1alpha1.Task, rec *v1alpha1.RecoveryProposal) error {
	cs := t.Status.Coordination
	switch rec.Code {
	case RecoveryRetryChild, RecoveryResumeChild, RecoveryRetryProducer, RecoveryRetryDependent:
		i := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == rec.ChildID })
		if i < 0 || cs.Children[i].Task == "" || Pending(cs, rec.ChildID) != nil {
			return ErrRecoveryNotProposed
		}
		st := &cs.Children[i]
		// The next generation, under the same fingerprint and lifetime allocation: the reconciler
		// stops the old Task if it still lives and starts the new one only once it drained.
		cs.PendingGenerations = v1alpha1.AppendNewest(cs.PendingGenerations, v1alpha1.PendingGeneration{ChildID: st.ID,
			Generation: st.Generation + 1, PlanVersion: cs.Plan.Version, StoppingTask: st.Task}, v1alpha1.MaxPendingGenerations)
		st.Reason, st.IntegrationRelease = ReasonGenerationPending, nil
		return nil
	case RecoveryAuthorizeChild:
		kept := slices.DeleteFunc(slices.Clone(cs.Authorizations), func(a v1alpha1.ChildAuthorization) bool { return a.ChildID == rec.ChildID })
		cs.Authorizations = kept
		_, err := s.authorizeChild(ctx, cfg, actor, cs, rec.ChildID, ChildFingerprint(*cs.Plan, rec.ChildID))
		return err
	case RecoveryRetryIntegration, RecoveryRerunIntegration:
		return s.retryIntegration(ctx, cfg, t, rec)
	}
	return ErrPlanRevisionRequired
}

// retryIntegration starts one new integration attempt: the same manifest after an infrastructure
// loss, or the dependent's current head after a release went stale. It is counted against maxRuns
// and behind the execute gate re-read for the primary, as every attempt is.
func (s *Service) retryIntegration(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, rec *v1alpha1.RecoveryProposal) error {
	cs := t.Status.Coordination
	var m IntegrationManifest
	switch rec.Code {
	case RecoveryRetryIntegration:
		i := slices.IndexFunc(cs.IntegrationAttempts, func(a v1alpha1.IntegrationAttempt) bool { return a.Task == rec.IntegrationTask })
		if i < 0 {
			return ErrRecoveryNotProposed
		}
		var it v1alpha1.Task
		if err := s.Client.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: rec.IntegrationTask}, &it); err != nil {
			if apierrors.IsNotFound(err) {
				return ErrRecoveryNotProposed
			}
			return err
		}
		var err error
		if m, err = ManifestOf(&it, cs); err != nil {
			return err
		}
		// The lost attempt is over: it consumed one run and never passes.
		cs.IntegrationAttempts[i].State, cs.IntegrationAttempts[i].Reason = ResultFailed, reasonIntegrationInf
	case RecoveryRerunIntegration:
		merges, head, err := s.familyHeads(ctx, cs, rec.ChildID)
		if err != nil {
			return err
		}
		if m, err = BeforeDependentManifest(t.Name, cs, rec.ChildID, head, merges); err != nil {
			return err
		}
	}
	if err := IntegrationAttemptAllowed(cs); err != nil {
		return err
	}
	if err := s.ExecuteReady(ctx, cfg, cs, []string{PrimaryChild(cs)}); err != nil {
		return err
	}
	it := IntegrationTask(s.Namespace, cfg, t, m, len(cs.IntegrationAttempts)+1)
	if err := s.Client.Create(ctx, it); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	cs.IntegrationAttempts = v1alpha1.AppendNewest(cs.IntegrationAttempts, v1alpha1.IntegrationAttempt{Task: it.Name, Purpose: m.Purpose,
		PlanHash: m.PlanHash, ManifestHash: ManifestHash(m), State: ResultPending, Subjects: m.Subjects,
		RunMicroUSD: it.Spec.Budget.RunMicroUSD}, v1alpha1.MaxCoordinationIntegrationAttempts)
	return nil
}

// familyHeads is every child's landed merge commit and dependent's open head, read from their
// current Tasks.
func (s *Service) familyHeads(ctx context.Context, cs *v1alpha1.CoordinationStatus, dependent string) (map[string]string, string, error) {
	merges, head := map[string]string{}, ""
	for _, st := range cs.Children {
		if st.Task == "" {
			continue
		}
		var c v1alpha1.Task
		switch err := s.Client.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: st.Task}, &c); {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return nil, "", err
		}
		pr := c.Status.PullRequest
		switch {
		case pr == nil || c.Spec.Repository != st.Repository:
		case pr.MergedAt != nil && c.Status.Phase != v1alpha1.PhaseReverted:
			merges[st.ID] = pr.MergeCommitSHA
		case st.ID == dependent && pr.MergedAt == nil:
			head = pr.HeadSHA
		}
	}
	return merges, head, nil
}

// RecoveryRepository is the repository whose authority parentTask's current proposal needs: the
// child's for a child action, else the primary. The caller resolves the actor's login there;
// Recover re-reads the authority itself.
func (s *Service) RecoveryRepository(ctx context.Context, parentTask string) (string, error) {
	cfg := s.config()
	if modeOf(cfg) == config.CoordinationOff {
		return "", ErrCoordinationOff
	}
	t, err := s.parent(ctx, parentTask)
	if err != nil {
		return "", err
	}
	cs := t.Status.Coordination
	rec := cs.Recovery
	if rec == nil {
		return "", ErrRecoveryNotProposed
	}
	if slices.Contains(childCodes, rec.Code) {
		if c, ok := find(*cs.Plan, rec.ChildID); ok {
			return c.Repository, nil
		}
		return "", ErrRecoveryNotProposed
	}
	return cs.Plan.Primary.Repository, nil
}
