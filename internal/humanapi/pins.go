// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/metrics"
)

// checkPins is intake's early check of every pinned profile in plan (P6), against the plan
// repository's catalogue as the caller reads it: its maintainer flag is the caller's (P5). A plan
// that pins nothing reads no catalogue; one that pins while the catalogue cannot be read is refused
// `catalogue_unavailable`, since nothing could say the pin holds. The plan's repository is public by
// the planner's own route, so a tests-first pin is checked as on public data; triage checks it again
// against the task's own data class. A refusal is counted under its code.
func (s *Server) checkPins(ctx context.Context, p authn.Principal, onBehalfOf string, plan intake.Plan, surface intake.Surface) *intake.Refusal {
	var refused *intake.ProfileRefusal
	for _, is := range plan.Issues {
		if is.Profile == "" {
			continue
		}
		c, err := s.Catalogue.Get(ctx, p, plan.Repository, onBehalfOf)
		if err != nil {
			refused = &intake.ProfileRefusal{Code: intake.CatalogueUnavailable}
			break
		}
		if refused = intake.CheckPin(c, is.Class, is.Profile, true); refused != nil {
			break
		}
	}
	if refused == nil {
		return nil
	}
	if s.Metrics != nil {
		s.Metrics.IntakeProfileRefusal(ctx, string(surface), refused.Code)
	}
	return refused.Refusal()
}

// finishPinned answers intake_plan's profile revision: the final plan's checks, then the pin's
// against the catalogue, then the suggestion the broker derives, with no model call.
func (s *Server) finishPinned(ctx context.Context, run planRun, plan intake.Plan, candidates []string, onBehalfOf string) (any, error) {
	classes := s.planClasses(ctx, run.principal, onBehalfOf, plan.Repository)
	if ref := intake.CheckFinal(plan, candidates, classes); ref != nil {
		return s.finishPlan(ctx, run, nil, false, ref)
	}
	if ref := s.checkPins(ctx, run.principal, onBehalfOf, plan, run.surface); ref != nil {
		return s.finishPlan(ctx, run, nil, false, ref)
	}
	if err := s.checkPlanDrafts(ctx, plan); err != nil {
		ref, ok := asRefusal(err)
		if !ok {
			return nil, err
		}
		return s.finishPlan(ctx, run, nil, false, ref)
	}
	plan = intake.Resuggest(plan, classes)
	return s.finishPlan(ctx, run, &plan, false, nil)
}

// countChoices counts each issue of a confirmed plan by what its profile is (P4): the factory's
// choice, the suggested profile pinned, or another pinned. plan's suggestions are the broker's
// (Resuggest), so the comparison never reads a client's copy.
func (s *Server) countChoices(ctx context.Context, plan intake.Plan, surface intake.Surface) {
	if s.Metrics == nil {
		return
	}
	for _, is := range plan.Issues {
		s.Metrics.IntakeProfileChoice(ctx, string(surface), is.Class, choiceOf(is))
	}
}

func choiceOf(is intake.Issue) string {
	switch {
	case is.Profile == "":
		return metrics.ChoiceFactory
	case is.Suggestion != nil && is.Suggestion.Profile == is.Profile:
		return metrics.ChoiceSuggested
	}
	return metrics.ChoiceOther
}
