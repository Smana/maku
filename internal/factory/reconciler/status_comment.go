// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/narrate"
	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/summary"
)

func (r *Reconciler) statusFactsCtx(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) narrate.StatusFacts {
	f := r.factsForCtx(ctx, cfg, t)
	var sf narrate.StatusFacts
	if f.Profile != nil {
		card := roomview.CardOf(&f, "")
		sf.Workflow, sf.Reason = card.Workflow, card.Reason
		sf.CeilingMicroUSD = f.Profile.CeilingMicroUSD
	}
	fl := &summary.Flow{Plan: f.Plan, Nodes: make([]summary.FlowNode, 0, len(f.Runs)), Split: f.Split}
	for _, rf := range f.Runs {
		fl.Nodes = append(fl.Nodes, summary.FlowNode{
			Run: rf.ID, Persona: rf.Persona, Role: rf.Role,
			StartedAt: rf.StartedAt, EndedAt: rf.EndedAt, Outcome: rf.Outcome,
		})
	}
	if v1alpha1.TerminalPhase(f.Phase) || f.Phase == v1alpha1.PhaseEscalated || f.Phase == v1alpha1.PhaseVerifying || f.Split != nil {
		fl.Ghosts = []string{}
	}
	sf.Strip = summary.Strip(fl)
	sf.Split = f.Split
	if t.Status.Phase == v1alpha1.PhaseAwaitingHuman && t.Status.Reason == ReasonPlanReview &&
		t.Status.Plan != nil && t.Status.Plan.Parked {
		sf.PlanParkedHours = r.repo(cfg, t).PlanGateHoursOr()
	}
	return sf
}

// statusCommentDueCtx reports whether an issue task has narration to represent, or a recorded status
// to repair, without making a GitHub call on an already-current reconcile.
func (r *Reconciler) statusCommentDueCtx(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) bool {
	comment := t.Status.StatusComment
	if comment != nil && comment.Inactive {
		return false
	}
	if t.Spec.Issue == 0 {
		return false
	}
	if comment == nil && len(t.Status.Outbox) == 0 && len(t.Status.Narrated) == 0 {
		return false
	}
	return comment == nil || comment.ID == 0 || comment.Body != narrate.Status(t, cfg.RoomsURL, r.statusFactsCtx(ctx, cfg, t), r.repo(cfg, t).Merge.ApproveToMerge)
}

// syncStatusComment records the remote identity and body in t.Status; Reconcile's second status
// update persists them alongside the outbox and facts results.
func (r *Reconciler) syncStatusComment(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	if comment := t.Status.StatusComment; comment != nil && comment.Inactive {
		return nil
	}
	return r.narrator(ctx, cfg).SyncStatus(ctx, t, cfg.RoomsURL, r.statusFactsCtx(ctx, cfg, t), r.repo(cfg, t).Merge.ApproveToMerge)
}
