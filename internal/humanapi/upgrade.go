// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"strings"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/roomview"
)

// replannedLine leads the answer to a plan/v1 confirmed after the upgrade (P14): nothing was filed,
// and the plan to confirm is the one below, under its new hash.
const replannedLine = "This plan was made before profiles, so nothing was filed. Here it is again as plan/v2, " +
	"with its new hash: check it and confirm this one."

// planReplanned is a plan/v1 shown again as plan/v2: never filed, and never a dead end.
type planReplanned struct {
	Status      string      `json:"status"` // replanned
	Plan        intake.Plan `json:"plan"`
	Hash        string      `json:"hash"`
	CanStart    bool        `json:"canStart"` // always false: the plan/v2 is confirmed first
	CanStartWhy string      `json:"canStartWhy"`
}

// replannedWhy is canStartWhy on a replanned answer: no client starts work from it.
const replannedWhy = "replanned: confirm the plan/v2 answered"

// replanned is run_create's answer to a plan/v1: an error to the filing path, so nothing below it
// runs, and the tool's result to the caller.
type replanned struct{ plan intake.Plan }

func (r replanned) Error() string { return "replanned: " + r.plan.Hash }

// result is the answer a client reads: the line, the plan as a person reads it, and the plan/v2
// with its hash to confirm.
func (r replanned) result(s *Server) (viewResult, error) {
	var b strings.Builder
	b.WriteString(replannedLine)
	b.WriteString("\n\n")
	if err := roomview.Plan(&b, r.plan, "", roomview.Options{Format: roomview.Markdown, Now: s.Intake.now()}); err != nil {
		return viewResult{}, err
	}
	return viewResult{text: b.String(), data: planReplanned{Status: "replanned", Plan: r.plan, Hash: r.plan.Hash, CanStartWhy: replannedWhy}}, nil
}

// planClasses is the class table a plan for repository is held to, as p (for onBehalfOf) reads it:
// the factory's catalogue for that repository, through the 60 s cache, else the config's table
// (until stage 5). A failed read is counted by the cache (`intake_catalogue_reads_total{outcome="error"}`).
func (s *Server) planClasses(ctx context.Context, p authn.Principal, onBehalfOf, repository string) map[string]intake.Class {
	return intake.ClassesFor(s.planOffers(ctx, p, onBehalfOf, []string{repository}), s.Intake.Classes, repository)
}

// planOffers is each candidate's catalogue classes, keyed by lowercase repository; a candidate whose
// catalogue the broker could not read is left out, so it plans from the config's table.
func (s *Server) planOffers(ctx context.Context, p authn.Principal, onBehalfOf string, candidates []string) map[string]map[string]intake.Class {
	if s.Catalogue == nil {
		return nil
	}
	var offers map[string]map[string]intake.Class
	for _, repository := range candidates {
		c, err := s.Catalogue.Get(ctx, p, repository, onBehalfOf)
		if err != nil {
			s.log().Info("intake: the catalogue is unavailable; planning from the config's classes", "err", err)
			continue
		}
		if offers == nil {
			offers = make(map[string]map[string]intake.Class, len(candidates))
		}
		offers[strings.ToLower(repository)] = intake.ClassesFromCatalogue(c)
	}
	return offers
}

// upgrade is a plan/v1 the caller may still file into, shown again as plan/v2 (P14, G2): the
// requester's guard first, so a caller who may not use the repository learns nothing of its classes,
// then intake.Upgrade over the plan repository's table. No model call, no daily plan.
func (s *Server) upgrade(ctx context.Context, p authn.Principal, onBehalfOf string, v1 intake.Plan) (intake.Plan, *intake.Refusal) {
	if _, _, ref := s.requester(ctx, p, intakeArgs{
		Repository: v1.Repository, Candidates: alternativeRepos(v1), OnBehalfOf: onBehalfOf}); ref != nil {
		return intake.Plan{}, ref
	}
	return intake.Upgrade(v1, s.planClasses(ctx, p, onBehalfOf, v1.Repository))
}
