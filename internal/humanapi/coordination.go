// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/authn"
	fconfig "github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/taskid"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/mcpwire"
)

// coordination_plan is the multi-repository planner (v4 MR4): a sentence becomes a typed, validated,
// hashed CoordinationPlan through intake_plan's one governed route, its requester resolution, its
// public guard and its daily counter. It files nothing, confirms nothing and calls no factory API:
// filing is the separately confirmed coordination flow, and execute is never reached from here.
//
// The order is intake_plan's, and is the security property: arguments, then who is asking and the
// primary they must write, then every other candidate served and public, then the daily slot, then
// the one call, then composition and validation.

// CoordinationPlanner is coordination_plan as the broker serves it: the switch's mode, the policy
// its children resolve through, and the draft id source. Everything else — the model, the guard, the
// requester resolution, the daily counter and the clock — is Server.Intake's, so the two planners
// share one route and one allowance. Nil, or mode off, lists no tool.
type CoordinationPlanner struct {
	// Mode is the coordination switch (MR16): off lists nothing; plan and execute both plan, since
	// this tool never files.
	Mode string
	// Resolve is the broker-side policy: the class table's roles and ceiling (ClassPolicy). The
	// factory re-resolves each child at its repository's confirmed policy before anything is filed.
	Resolve coordination.ResolvePolicy
	// NewID is a fresh draft id when the request names none; nil means taskid.Random.
	NewID func() string
}

// on is whether the switch serves the tool at all.
func (c *CoordinationPlanner) on() bool {
	return c != nil && c.Mode != "" && c.Mode != fconfig.CoordinationOff
}

func (c *CoordinationPlanner) newID() string {
	if c.NewID != nil {
		return c.NewID()
	}
	return taskid.Random()
}

// The broker-side composition's fixed values. A child's tier is the standard one, the broker's
// class table carries no per-run price, so one run is a tenth of the class's task ceiling (the
// design's worked example: 510000 of 5100000); the factory replaces both with the repository's
// resolved policy when it validates the plan for confirmation.
const (
	coordinationTier        = "standard"
	coordinationRunsPerTask = 10
	coordinationVersion     = 1
	// scoperRole is dropped from a child's roles: a child's scope is frozen in the plan, so it has no
	// post-filing scoper run (MR2).
	scoperRole = "scoper"
)

// ClassPolicy is the broker's ResolvePolicy over the intake class table: a class's roles (without
// the scoper) and its ceiling as the task ceiling, one run priced at a tenth of it. A class the table
// does not define is coordination.ErrUnknownClass. It reads no repository: every repository resolves
// the same table, and the factory holds each child to its own repository's policy later.
func ClassPolicy(classes map[string]intake.Class) coordination.ResolvePolicy {
	return func(_ context.Context, repository, class, tier string) (coordination.RepositoryPolicy, error) {
		c, ok := classes[class]
		if !ok {
			return coordination.RepositoryPolicy{}, coordination.ErrUnknownClass
		}
		task := int64(math.Round(c.CeilingUSD * 1e6))
		return coordination.RepositoryPolicy{Repository: repository, Class: class, Tier: tier,
			Roles:       slices.DeleteFunc(slices.Clone(c.Roles), func(r string) bool { return r == scoperRole }),
			RunMicroUSD: task / coordinationRunsPerTask, TaskMicroUSD: task}, nil
	}
}

// The request's bounds: 2 to 20 candidates (a family is two repositories, intake's guard bounds the
// list at 20), and the primary line the context carries counted in intake's 4 KiB.
const (
	minCoordinationCandidates = 2
	primaryLine               = "\nThe primary repository, where the integration check runs: "
)

// coordinationArgs is coordination_plan's closed argument set. There is deliberately no file,
// confirm, merge, role or budget argument: those are config's, a maintainer's, or another flow's.
type coordinationArgs struct {
	Text       string   `json:"text"`
	Context    string   `json:"context"`
	Candidates []string `json:"candidates"`
	Primary    string   `json:"primary"`
	ID         string   `json:"id"`
	Version    int32    `json:"version"`
	OnBehalfOf string   `json:"on_behalf_of"`
}

func (a coordinationArgs) check(p authn.Principal) error {
	switch {
	case strings.TrimSpace(a.Text) == "":
		return toolError("invalid_arguments: text is one sentence about the change")
	case len(a.Text) > maxPlanText:
		return toolError("invalid_arguments: text is at most " + strconv.Itoa(maxPlanText>>10) + " KiB")
	case len(a.Candidates) < minCoordinationCandidates || len(a.Candidates) > maxPlanCandidates:
		return toolError("invalid_arguments: candidates holds 2 to " + strconv.Itoa(maxPlanCandidates) + " repositories")
	case !repositoryRE.MatchString(a.Primary):
		return toolError("invalid_arguments: primary is owner/name")
	case len(a.Context)+len(primaryLine)+len(a.Primary) > maxPlanContext:
		return toolError("invalid_arguments: context is at most " + strconv.Itoa(maxPlanContext>>10) + " KiB with the primary repository")
	case a.ID != "" && !c2RE.MatchString(a.ID):
		return toolError("invalid_arguments: id is an eight-character draft id")
	case a.Version < 0:
		return toolError("invalid_arguments: version is at least 1")
	}
	for _, c := range a.Candidates {
		if !repositoryRE.MatchString(c) {
			return toolError("invalid_arguments: every candidate is owner/name")
		}
	}
	if !slices.ContainsFunc(a.Candidates, func(c string) bool { return strings.EqualFold(c, a.Primary) }) {
		return toolError("invalid_arguments: primary is one of the candidates")
	}
	return claims(p, a.OnBehalfOf, 0)
}

// c2RE is a draft id's shape: the parent Task's C2 id.
var c2RE = regexp.MustCompile(`^[a-z2-7]{8}$`)

// coordinationPlanTool is coordination_plan, in intake_plan's order. Every refusal is a reason of
// intake's closed list, or a coordination plan reason with the field it concerns; none names a
// repository the request did not.
func (s *Server) coordinationPlanTool(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
	var a coordinationArgs
	if mcpwire.DecodeArgs(args, &a) != nil {
		return nil, toolError("invalid_arguments: text, candidates and primary are required, and no field outside the schema is taken")
	}
	if err := a.check(p); err != nil {
		return nil, err
	}
	run := planRun{principal: p, surface: intake.SurfaceSession, start: time.Now()}
	if p.Sponsored {
		run.surface = intake.SurfaceSlack
	}

	// Who is asking, and the primary they must write: intake's own resolution and guard, so the two
	// planners cannot disagree about either.
	req, primary, ref := s.requester(ctx, p, intakeArgs{Repository: a.Primary, OnBehalfOf: a.OnBehalfOf})
	run.requester = req
	if ref != nil {
		return s.finishCoordination(ctx, run, nil, ref)
	}
	candidates, ref := s.coordinationCandidates(ctx, a.Candidates)
	if ref != nil {
		return s.finishCoordination(ctx, run, nil, ref)
	}
	if !s.Intake.take(req.Key) {
		return s.finishCoordination(ctx, run, nil, intake.ReasonRefusal(intake.ReasonDailyPlansReached))
	}

	var draft coordination.CoordinationDraft
	usage, err := s.Intake.Model.Generate(ctx, intake.GovernedRequest{Text: a.Text,
		Context: strings.TrimSpace(a.Context + primaryLine + primary[0]), Candidates: candidates,
		Classes: classNames(s.Intake.Classes), SystemPrompt: coordination.SystemPrompt}, &draft)
	run.usage = usage
	if err != nil {
		ref, ok := intake.RefusalOf(err)
		if !ok {
			return nil, err
		}
		run.cause = causeOf(err)
		return s.finishCoordination(ctx, run, nil, ref)
	}

	attempt := coordination.PlanningReconciliation{AttemptID: usage.AttemptID, EffectiveRequester: req.Key,
		ReservationID: usage.ReservationID, SettlementID: usage.SettlementID,
		ReservedMicroUSD: usage.ReservedMicroUSD, SettledMicroUSD: usage.SettledMicroUSD}
	if attempt.AttemptID == "" {
		attempt.AttemptID = taskid.Random() // the route supplies none today: one per attempt, for the audit line
	}
	id, version := a.ID, a.Version
	if id == "" {
		id = s.Coordination.newID()
	}
	if version == 0 {
		version = coordinationVersion
	}
	res, err := coordination.ComposeCoordination(ctx, draft, coordination.ComposeInput{ID: id, Version: version,
		Primary: primary[0], Maintainer: "github:" + req.Login, Tier: coordinationTier, Candidates: candidates,
		Resolve: s.Coordination.Resolve, Attempt: attempt})
	if err != nil {
		var pe *coordination.PlanError
		if !errors.As(err, &pe) {
			return nil, err
		}
		// A PlanError names plan structure and a bounded reason, never plan text or an outsider.
		return s.finishCoordination(ctx, run, nil, toolError(pe.Error()))
	}
	return s.finishCoordination(ctx, run, &res, nil)
}

// coordinationCandidates holds every candidate to the allowlist and the public-only read before any
// text leaves the broker (design §seam): one that is private is private_repository_unsupported, one
// the allowlist does not serve is repository_not_allowed, and one GitHub cannot answer for is
// repository_unverified. Write is not asked: only the primary's requester must write, and each other
// child's own maintainer authorizes it later (MR5).
func (s *Server) coordinationCandidates(ctx context.Context, asked []string) ([]string, *intake.Refusal) {
	ctx, cancel := context.WithTimeout(ctx, accessWait)
	defer cancel()
	guard := intake.Guard{Allow: s.Intake.Allow, Public: s.Intake.Public,
		MayWrite: func(context.Context, string) (bool, error) { return true, nil }}
	out := make([]string, 0, len(asked))
	for _, repository := range asked {
		one, ref := guard.Candidates(ctx, []string{repository})
		if ref != nil {
			return nil, ref
		}
		if !slices.Contains(out, one[0]) {
			out = append(out, one[0])
		}
	}
	return out, nil
}

// finishCoordination writes the attempt's one audit line and answers it. The request's and the plan's
// text reach neither the log nor a metric.
func (s *Server) finishCoordination(ctx context.Context, run planRun, res *coordination.PlanAttempt, refusal error) (any, error) {
	outcome, reason := outcomePlanned, ""
	if refusal != nil {
		outcome, reason = outcomeRefused, refusalReason(refusal)
	}
	line := []any{"surface", string(run.surface), "requester", run.requester.Key, "outcome", outcome,
		"duration", time.Since(run.start)}
	if res != nil {
		line = append(line, "hash", res.Hash, "version", res.Plan.Version, "children", len(res.Plan.Children),
			"attempt", res.Reconciliation.AttemptID)
	}
	if reason != "" {
		line = append(line, "reason", reason)
	}
	if run.cause != "" {
		line = append(line, "cause", run.cause)
	}
	if n := run.usage.PromptTokens + run.usage.CompletionTokens; n > 0 {
		line = append(line, "promptTokens", run.usage.PromptTokens, "completionTokens", run.usage.CompletionTokens)
	}
	s.log().InfoContext(ctx, "coordination plan", line...)
	if refusal != nil {
		return nil, refusal
	}
	return viewResult{text: coordinationText(*res), data: res}, nil
}

// refusalReason is a refusal's bounded reason: intake's closed reason, or a tool error's leading code.
func refusalReason(err error) string {
	if ref, ok := asRefusal(err); ok {
		return ref.Reason
	}
	code, _, _ := strings.Cut(err.Error(), ":")
	return code
}

// coordinationText is the plan as a person reads it: what each repository is asked to do, by whom,
// within what, and that nothing was filed.
func coordinationText(a coordination.PlanAttempt) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** (plan %s, version %d, hash `%s`)\n\n", a.Plan.Title, a.Plan.ID, a.Plan.Version, a.Hash[:12])
	for _, id := range a.Plan.MergeOrder {
		for _, c := range a.Plan.Children {
			if c.ID != id {
				continue
			}
			primary := ""
			if strings.EqualFold(c.Repository, a.Plan.Primary.Repository) {
				primary = ", primary"
			}
			fmt.Fprintf(&b, "- `%s` in %s%s: %s (class %s, roles %s, up to $%.2f, maintainer %s)\n", c.ID, c.Repository, primary,
				c.Scope.Task, c.Class, strings.Join(c.Roles, ", "), float64(c.BudgetMicroUSD)/1e6, c.Maintainer)
		}
	}
	fmt.Fprintf(&b, "\nFamily ceiling $%.2f, integration up to %d runs in the primary repository.\n",
		float64(a.Plan.Budget.TotalMicroUSD)/1e6, a.Plan.Integration.MaxRuns)
	for _, d := range a.Diagnostics {
		fmt.Fprintf(&b, "\n- %s (%s): %s", d.Code, d.Field, d.Sentence)
	}
	b.WriteString("\n\nNothing was filed: the primary maintainer confirms this plan, and each child's maintainer authorizes their own.")
	return b.String()
}

// coordinationPlanSchema is coordination_plan's arguments, closed. on_behalf_of is a sponsored
// principal's alone, as intake_plan's is.
var coordinationPlanSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["text","candidates","primary"],` +
	`"properties":{"text":{"type":"string","minLength":1,"maxLength":16384},` +
	`"context":{"type":"string","maxLength":3900},` +
	`"candidates":{"type":"array","minItems":2,"maxItems":20,"items":{"type":"string","pattern":"^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$"}},` +
	`"primary":{"type":"string","pattern":"^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$","description":"the candidate where the parent issue and the integration check live"},` +
	`"id":{"type":"string","pattern":"^[a-z2-7]{8}$","description":"the draft's id, to replan it; absent, a new one"},` +
	`"version":{"type":"integer","minimum":1,"description":"the draft's version; absent, 1"},` +
	`"on_behalf_of":{"type":"string","pattern":"` + onBehalfRE.String() + `"}}}`)

// coordinationPlanOutput is coordination_plan's structuredContent: the four fields of a PlanAttempt.
var coordinationPlanOutput = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["plan","hash","diagnostics","planning"],` +
	`"properties":{"plan":{"type":"object","required":["apiVersion","kind","id","version","primary","children","integration","budget"]},` +
	`"hash":{"type":"string","pattern":"^[0-9a-f]{64}$"},` +
	`"diagnostics":{"type":"array","items":{"type":"object","required":["code","field","sentence"]}},` +
	`"planning":{"type":"object","required":["attemptID","effectiveRequester"]}}}`)

// coordinationTool is coordination_plan's tools/list entry.
func (s *Server) coordinationTool() mcpTool {
	return mcpTool{name: mcpwire.CoordinationPlan, call: s.coordinationPlanTool,
		description: "Plan one change across two public repositories: one bounded planner call returns a CoordinationPlan — each repository's " +
			"scoped child, its class, roles and allocation from the broker's config, the artifact one child hands the other, the merge order, " +
			"the integration check and the family budget — validated, with its canonical hash and diagnostics. Name 2 to 20 candidates and the " +
			"primary one. Show the text as it is. It files nothing and starts nothing: the primary maintainer confirms the plan, and each " +
			"child's maintainer authorizes their own, elsewhere.",
		schema: coordinationPlanSchema, output: coordinationPlanOutput}
}
