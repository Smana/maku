// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/issuebody"
	"github.com/Smana/maku/internal/factory/narrate"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/intake"
)

// acceptanceText is the acceptance the issue states, read as the judge reads it (issuebody.Acceptance).
func acceptanceText(text string) string { return issuebody.Acceptance(text) }

// statesAcceptance: the issue text carries a non-empty acceptance section or an inline Acceptance:
// paragraph (HR3).
func statesAcceptance(text string) bool { return issuebody.Acceptance(text) != "" }

const (
	intakeAcceptanceQuestion   = "The issue states no acceptance check. Which command or observation proves it is done?"
	intakeAcceptanceAssumption = "Proceed without a stated acceptance check (rely on repository checks and review)."
)

// askStageOn reports whether the task's workflow enables the deterministic acceptance guard (WR6).
func askStageOn(cfg *config.Config, rc *config.Repository, workflow, class string) bool {
	if workflow != "" {
		return cfg.StagesFor(rc, workflow, class).Ask
	}
	if row, ok := cfg.Loops["default"]; ok && row.Ask != nil {
		return *row.Ask
	}
	return cfg.ScopeFor("default") == config.ScopeScoper
}

// askAtIntake is the deterministic acceptance guard at triage (WR6, E3): when the task's stages
// hold ask, an issue without a stated acceptance check bounces immediately to Escalated
// needs_author with factory/needs-author and no AgentRun started. Schedules (no issue) proceed as
// assumed; tasks filed from a confirmed intake plan that settled acceptance proceed as answered.
func (r *Reconciler) askAtIntake(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, workflow string) (bool, error) {
	if cfg.Investigates(t.Spec.Template) || cfg.ReviewsOnly(t.Spec.Template) ||
		t.Spec.Outcome == config.OutcomeReport || t.Spec.Outcome == config.OutcomeReview {
		return false, nil
	}
	if !askStageOn(cfg, r.repo(cfg, t), workflow, t.Spec.PredictedClass) || t.Spec.Parent != nil {
		return false, nil
	}
	if t.Spec.Issue == 0 {
		r.noteStage(t, "ask", "asked_assumed")
		return false, nil
	}
	if t.Spec.Intake != nil && (statesAcceptance(t.Spec.Text) || intake.DoneWhen(t.Spec.Text) != "") {
		r.noteStage(t, "ask", "asked_answered")
		return false, nil
	}
	if statesAcceptance(t.Spec.Text) {
		r.noteStage(t, "ask", "not_needed")
		return false, nil
	}
	// The question is the room's to answer too (maku answer, maku cancel, the web inbox): the room
	// is made now, as triaged would make it. One the factory cannot make leaves the GitHub path,
	// as before: the retry's Triaged pass escalates a foreign room.
	if _, err := r.ensureRoom(ctx, t); err == nil {
		t.Status.RoomRef = t.Name
	} else if !errors.Is(err, rooms.ErrForeignRoom) && !apierrors.IsInvalid(err) {
		return false, err
	}
	n, name := t.Spec.Issue, t.Name
	record(ctx, func(ctx context.Context) {
		if err := r.gh(ctx).AddLabels(ctx, n, LabelNeedsAuthor); err != nil {
			r.log().Warn("label not added", "task.id", name, "label", LabelNeedsAuthor, "err", err)
		}
	})
	r.to(t, v1alpha1.PhaseEscalated, ReasonNeedsAuthor)
	narrateLater(t, narrate.NeedsAuthor(t, intakeAcceptanceQuestion, intakeAcceptanceAssumption, r.repo(cfg, t).Maintainers, cfg.RoomsURL))
	r.outcome(ctx, cfg, t)
	return true, nil
}
