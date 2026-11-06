// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/repoaccess"
	"github.com/Smana/maku/internal/roomview"
)

// intake_plan is the broker's one governed planner on /mcp (v3.x conversational intake, CD18): a
// sentence becomes plan/v1 through one bounded model call, with the repository bounded by the
// allowlist and by what the caller may write, the roles and the money written by config, and the
// plan's hash the value every later door re-validates. It files nothing and stores nothing.
//
// The order below is the security property, not a convenience: arguments, then who is asking and
// which repositories they may use, then the daily cap, then the one call, then the compose, then the
// check — so that nothing reaches a model before every check it can fail has been made, and nothing
// is shown before the plan has been held to the contract.

// The plan's one revision on this door: the first plan of a session or a thread. A later revision
// belongs to the consumer that owns the message (PR 4's run_create), and the hash covers the plan,
// never the revision.
const planRev = 1

// ceilingSource is how a plan's budget says the channel's ceiling was the binding one: the class's
// taskUSD was higher, so the channel's maxCostUSD is what the work is charged against (CD17).
const ceilingSource = "channel:maxCostUSD"

// intakeFallbackComposer is the config's one value that makes the degraded composer reachable:
// `human.intake.plan.fallback: composer`. Any other value leaves it unreachable (CD18).
const intakeFallbackComposer = "composer"

// The outcomes intake_plan_total{outcome} counts: a closed list, as every label vocabulary here is.
const (
	outcomePlanned  = "planned"  // a plan, with nothing left to ask
	outcomeAsked    = "asked"    // a plan that carries the one question the broker must ask first
	outcomeDegraded = "degraded" // the template draft, because the config named the composer
	outcomeRefused  = "refused"  // no plan, for one reason of the closed list
)

// startSponsored joins issuedraft.go's reasons (off, not_linked, no_write, unverified): a sponsored
// principal starts no developer's session, so its plan's canStart is false for a reason of its own
// rather than an empty string a caller would have to invent.
const startSponsored = "sponsored"

// The request's bounds, from the contract (§model-call): text ≤ 16 KiB, context ≤ 4 KiB, candidates
// ≤ 20, and the plan's evidence at most five items of at most 280 characters. The schema repeats them
// for the client; this file enforces them, because a schema is advice to a client and never a check.
const (
	maxPlanText          = 16 << 10
	maxPlanContext       = 4 << 10
	maxPlanCandidates    = 20
	maxPlanEvidence      = 5
	maxPlanEvidenceRunes = 280
)

// SlackLinkFinder is the part of the store a sponsored plan resolves its requester through: the
// ZITADEL account a Slack user is linked to. *store.Store has it; nil refuses every sponsored plan
// as not_linked, which is the one reason that sends the person somewhere they can act.
type SlackLinkFinder interface {
	SlackLink(ctx context.Context, team, user string) (sub string, found bool, err error)
}

// Intake is the planner as the broker serves it (human.intake.plan): the class table and the
// allowlist from config, the one bounded model call, the visibility read the public-only guard asks,
// the link tables a sponsored plan resolves its requester through, the daily cap, the degraded
// composer's switch and the clock. Nil on the server means the switch is off: neither tool exists on
// any list, so the list a deployment sees is the list it has.
//
// It carries no channel ceiling: a channel is the caller's to state (the Slack App passes its
// channel's maxCostUSD as the tool's ceiling argument, as it passes max_cost to run_create), and
// Compose takes the lower of it and the class's.
type Intake struct {
	Classes map[string]intake.Class
	Allow   map[string]bool
	Model   *intake.Model
	// Public answers whether GitHub reports a repository public: the guard's public-only read.
	Public intake.Publicity
	// Links resolves a Slack user to the account it is linked to. Nil refuses every sponsored plan.
	Links SlackLinkFinder
	// Daily is the per-requester cap in a UTC day. Below one, no plan is made: a floor of zero would
	// otherwise read as "no cap", which is the opposite of what an operator typed.
	Daily int
	// Fallback is the config's fallback: `off` leaves the degraded composer unreachable, `composer`
	// makes it the answer when the call fails rather than a plan.
	Fallback string
	// Now is the clock the daily cap and the plan's composition read; nil means time.Now.
	Now func() time.Time

	limits dailyLimits
}

// dailyLimits is the per-requester daily cap (CD18): how many plans each effective requester asked
// for in the current UTC day, in memory and per replica, beside the per-human limiter. It is keyed
// by the requester's own key — a human's GitHub login, a Slack user's slack:<team>/<user> — and never
// by the principal, which for the App is one string for a whole workspace.
type dailyLimits struct {
	mu   sync.Mutex
	days map[string]dailyCount
}

// dailyCount is one requester's day and how many plans it has spent.
type dailyCount struct {
	day string // 2006-01-02, in UTC
	n   int
}

// take is whether requester may plan now, counting the plan when it may: the cap is exact at the
// boundary — the Nth plan is allowed and the N+1th is not — a refusal sends no request, and the day
// rolls over at 00:00 UTC on the clock this broker reads.
func (in *Intake) take(requester string) bool {
	if in.Daily < 1 {
		return false
	}
	day := in.now().UTC().Format("2006-01-02")
	in.limits.mu.Lock()
	defer in.limits.mu.Unlock()
	if in.limits.days == nil {
		in.limits.days = map[string]dailyCount{}
	}
	c := in.limits.days[requester]
	if c.day != day {
		c = dailyCount{day: day}
	}
	if c.n >= in.Daily {
		return false
	}
	c.n++
	in.limits.days[requester] = c
	return true
}

// now is the injected clock, or the wall clock.
func (in *Intake) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

// alias is the model the plans name: the alias the call uses, so `planner.model` is the call's own
// value and not a second spelling of it.
func (in *Intake) alias() string {
	if in.Model == nil {
		return ""
	}
	return in.Model.Alias
}

// intakeArgs is intake_plan's closed argument set: what the person asked, the repository or the
// repositories they name, the context and the evidence they bring, and — for a sponsored principal
// alone — whom it plans for and the ceiling its channel imposes.
type intakeArgs struct {
	Text       string   `json:"text"`
	Repository string   `json:"repository"`
	Context    string   `json:"context"`
	Candidates []string `json:"candidates"`
	Evidence   []string `json:"evidence"`
	OnBehalfOf string   `json:"on_behalf_of"`
	Ceiling    float64  `json:"ceiling"`
}

// check holds the arguments to the contract's bounds and to what this principal may ask for at all:
// a session cannot claim a person or widen its ceiling — both are a sponsored principal's, which
// reaches the planner on the channel's behalf — and a sponsored principal plans for a linked user,
// never for itself, which has no write access to name.
func (a intakeArgs) check(p authn.Principal) error {
	switch {
	case strings.TrimSpace(a.Text) == "":
		return toolError("invalid_arguments: text is one sentence about what is wrong")
	case len(a.Text) > maxPlanText:
		return toolError("invalid_arguments: text is at most " + strconv.Itoa(maxPlanText>>10) + " KiB")
	case len(a.Candidates) > maxPlanCandidates:
		return toolError("invalid_arguments: candidates holds at most " + strconv.Itoa(maxPlanCandidates) + " repositories")
	case len(a.Evidence) > maxPlanEvidence:
		return toolError("invalid_arguments: evidence holds at most " + strconv.Itoa(maxPlanEvidence) + " items")
	}
	for _, e := range a.Evidence {
		if utf8.RuneCountInString(e) > maxPlanEvidenceRunes {
			return toolError("invalid_arguments: each evidence item is at most " + strconv.Itoa(maxPlanEvidenceRunes) + " characters")
		}
	}
	if a.Repository != "" && !repositoryRE.MatchString(a.Repository) {
		return toolError("invalid_arguments: repository is owner/name")
	}
	for _, c := range a.Candidates {
		if !repositoryRE.MatchString(c) {
			return toolError("invalid_arguments: every candidate is owner/name")
		}
	}
	// The context and the evidence together are what the model is shown as context, and the
	// contract bounds that at 4 KiB: a request that fills both is refused whole rather than trimmed.
	if n := len(a.Context) + len(a.evidence()); n > maxPlanContext {
		return toolError("invalid_arguments: context and evidence together are at most " + strconv.Itoa(maxPlanContext>>10) + " KiB")
	}
	if !p.Sponsored {
		if a.OnBehalfOf != "" {
			return toolError("invalid_arguments: on_behalf_of is a sponsored principal's: a session plans for its own token")
		}
		if a.Ceiling != 0 {
			return toolError("invalid_arguments: ceiling is a sponsored principal's: a session's ceiling is the class's, from config")
		}
		return nil
	}
	if a.OnBehalfOf == "" {
		return toolError("invalid_arguments: on_behalf_of is required: a sponsored principal plans for a linked Slack user")
	}
	if !onBehalfRE.MatchString(a.OnBehalfOf) {
		return toolError("invalid_arguments: on_behalf_of is slack:<team>/<user>")
	}
	if a.Ceiling < 0 {
		return toolError("invalid_arguments: ceiling is a positive amount of dollars, or absent")
	}
	return nil
}

// asked is the repositories the request named: the one field first, then the candidates, so that a
// single explicitly named repository is the one the guard refuses by name (CD5).
func (a intakeArgs) asked() []string {
	out := make([]string, 0, len(a.Candidates)+1)
	if a.Repository != "" {
		out = append(out, a.Repository)
	}
	return append(out, a.Candidates...)
}

// evidence is the person's own evidence, as the model reads it: their words, one bullet each.
func (a intakeArgs) evidence() string {
	if len(a.Evidence) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nWhat they say they saw:\n")
	for _, e := range a.Evidence {
		b.WriteString("- ")
		b.WriteString(e)
		b.WriteString("\n")
	}
	return b.String()
}

// modelContext is the context the model is given: the person's context, then the evidence they
// brought, both as background — data, never instructions.
func (a intakeArgs) modelContext() string {
	return strings.TrimSpace(a.Context + "\n" + a.evidence())
}

// intakePlanTool is intake_plan, in the guard's order (the order is the security property). Every
// refusal it returns is one reason of intake's closed list with that reason's one sentence, so the
// model reads a line a person could act on rather than a transport failure.
func (s *Server) intakePlanTool(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
	var a intakeArgs
	if mcpwire.DecodeArgs(args, &a) != nil {
		return nil, toolError("invalid_arguments: text is required, and no field outside the schema is taken")
	}
	if err := a.check(p); err != nil {
		return nil, err
	}
	run := planRun{principal: p, surface: intake.SurfaceSession, start: time.Now()}
	if p.Sponsored {
		run.surface = intake.SurfaceSlack
	}

	req, candidates, ref := s.requester(ctx, p, a)
	run.requester = req
	if ref != nil {
		return s.finishPlan(ctx, run, nil, false, ref)
	}
	if !s.Intake.take(req.Key) {
		return s.finishPlan(ctx, run, nil, false, intake.ReasonRefusal(intake.ReasonDailyPlansReached))
	}

	draft, usage, err := s.Intake.Model.Draft(ctx, intake.DraftRequest{
		Text: a.Text, Context: a.modelContext(), Candidates: candidates, Classes: classNames(s.Intake.Classes)})
	run.usage = usage
	if err != nil {
		ref, ok := intake.RefusalOf(err)
		if !ok {
			return nil, err // not one of the call's three failures: the tool answers "unavailable"
		}
		// The degraded composer, when the config names it and the failure is the service's: the
		// plan is a template draft and every surface says so (CD18).
		if plan, ok := s.degraded(a, candidates, run.surface, ref); ok {
			return s.finishPlan(ctx, run, plan, true, nil)
		}
		return s.finishPlan(ctx, run, nil, false, ref)
	}

	plan, err := intake.Compose(draft, intake.ComposeIn{
		Surface: run.surface, Model: s.Intake.alias(), Now: s.Intake.now(), Rev: planRev,
		Classes: s.Intake.Classes, CeilingUSD: a.Ceiling, CeilingSource: ceilingSource, Logger: s.log()})
	if err != nil {
		ref, ok := asRefusal(err)
		if !ok {
			return nil, err
		}
		return s.finishPlan(ctx, run, nil, false, ref)
	}
	// The plan is checked before it is shown: a reply that names a repository, an alternative or a
	// class outside what this request may use is refused whole, and the outsider is never echoed.
	if ref := intake.Check(plan, candidates, s.Intake.Classes); ref != nil {
		return s.finishPlan(ctx, run, nil, false, ref)
	}
	return s.finishPlan(ctx, run, &plan, false, nil)
}

// degraded is the template draft, when the config's fallback names it and the failure is the plan
// service's: a reply that is not the contract's shape is not something a template answers for. The
// composer writes neither the surface nor the revision, and a served plan may not leave either
// empty, so the caller writes them here.
func (s *Server) degraded(a intakeArgs, candidates []string, surface intake.Surface, ref *intake.Refusal) (*intake.Plan, bool) {
	if s.Intake.Fallback != intakeFallbackComposer {
		return nil, false
	}
	if ref.Reason != intake.ReasonPlannerUnavailable && ref.Reason != intake.ReasonPlannerTimeout {
		return nil, false
	}
	plan, refuse := intake.ComposeTemplate(a.Text, a.modelContext(), candidates, s.Intake.Classes)
	if refuse != nil {
		// A request the composer cannot carry either: the model's own refusal is the honest answer,
		// and a degraded mode that replaces it with another sentence helps nobody.
		return nil, false
	}
	plan.Planner.Surface = string(surface)
	plan.Rev = planRev
	return &plan, true
}

// requester resolves steps 2 and 3 of the order — who is asking, and which repositories this plan
// may name — in one place, so every door that needs them (this tool now, plan_check and run_create
// later) resolves its caller identically. A sponsored principal's plan is resolved through the link
// tables to the linked account: never through the App's own identity, which is one string for a whole
// workspace, and never through a claim the request makes about itself.
func (s *Server) requester(ctx context.Context, p authn.Principal, a intakeArgs) (intake.Requester, []string, *intake.Refusal) {
	var req intake.Requester
	ctx, cancel := context.WithTimeout(ctx, accessWait) // as the gate's lookup is: a hung one is unverified
	defer cancel()

	sub := p.Sub
	if p.Sponsored {
		if s.Intake.Links == nil {
			return req, nil, intake.ReasonRefusal(intake.ReasonNotLinked)
		}
		team, user, _ := strings.Cut(strings.TrimPrefix(a.OnBehalfOf, "slack:"), "/")
		linked, found, err := s.Intake.Links.SlackLink(ctx, team, user)
		switch {
		case err != nil:
			s.log().Warn("intake: slack link unreadable", "err", err)
			return req, nil, intake.ReasonRefusal(intake.ReasonRepositoryUnverified)
		case !found:
			return req, nil, intake.ReasonRefusal(intake.ReasonNotLinked)
		}
		sub, req.Key = linked, "slack:"+team+"/"+user
	}

	probe := s.probe(a)
	if len(probe) == 0 {
		// Nothing is served: the honest reason is that, not that the caller is unlinked.
		return req, nil, intake.ReasonRefusal(intake.ReasonRepositoryNotAllowed)
	}
	login, ref := s.intakeLogin(ctx, sub, probe)
	if ref != nil {
		return req, nil, ref
	}
	if req.Key == "" {
		req.Key = login // a human's own login is what the cap and the audit line are keyed by
	}
	req.Login = login

	candidates, ref := s.intakeGuard(login).Candidates(ctx, a.asked())
	if ref != nil {
		return req, nil, ref
	}
	return req, candidates, nil
}

// intakeLogin is the GitHub login behind a ZITADEL account, resolved through the repositories this
// request may use: an account with no GitHub link has no write access to name (`not_linked`), and a
// GitHub or ZITADEL that cannot answer leaves the guard nothing to stand on
// (`repository_unverified`). An installation that cannot see one repository still sees the next, so
// the resolution walks the list rather than giving up on the first refusal.
func (s *Server) intakeLogin(ctx context.Context, sub string, repos []string) (string, *intake.Refusal) {
	if s.Identity == nil {
		// No D7 check is wired, so nothing can say what this caller may write, and "not linked" is
		// the one sentence that sends them somewhere they can act.
		return "", intake.ReasonRefusal(intake.ReasonNotLinked)
	}
	unverified := false
	for _, repository := range repos {
		login, err := s.Identity.Login(ctx, sub, repository)
		switch {
		case err == nil && login != "":
			return login, nil
		case err == nil:
			continue // no GitHub link on the account: the next repository asks the same question
		case errors.Is(err, repoaccess.ErrNoRepository):
			continue // the App cannot see this one; it may see the next
		default:
			s.log().Warn("intake: github login unverified", "err", err)
			unverified = true
		}
	}
	if unverified {
		return "", intake.ReasonRefusal(intake.ReasonRepositoryUnverified)
	}
	return "", intake.ReasonRefusal(intake.ReasonNotLinked)
}

// probe is the repositories the caller's login is resolved through: the ones the request named, else
// the allowlist in its one order (sorted: a map has none, and two replicas must agree).
func (s *Server) probe(a intakeArgs) []string {
	if asked := a.asked(); len(asked) > 0 {
		return asked
	}
	served := make([]string, 0, len(s.Intake.Allow))
	for repository, on := range s.Intake.Allow {
		if on {
			served = append(served, repository)
		}
	}
	slices.Sort(served)
	return served
}

// intakeGuard is the guard for one effective requester: the allowlist, the public-only read, and the
// write check closing over the login — the same pair canStart uses (ghidentity.Resolver into
// repoaccess.Checker), so an intake plan and an issue_draft cannot disagree about who may file. A
// broker with no write check wired verifies nothing, and the guard then refuses rather than planning
// on the assumption.
func (s *Server) intakeGuard(login string) intake.Guard {
	return intake.Guard{Allow: s.Intake.Allow, Public: s.Intake.Public,
		MayWrite: func(ctx context.Context, repository string) (bool, error) {
			if s.Access == nil || login == "" {
				return false, errors.New("humanapi: no GitHub write check is wired")
			}
			return s.Access.CanWrite(ctx, repository, login)
		}}
}

// planRun is one intake_plan attempt as the metric, the audit line and the answer read it.
type planRun struct {
	principal authn.Principal
	surface   intake.Surface
	requester intake.Requester
	start     time.Time
	usage     intake.Usage
}

// planAnswer is intake_plan's structuredContent: the plan, the hash the broker computed over it, the
// one question it must ask first, whether this caller may start the factory on it and why not, and
// whether a template rather than the planner wrote it.
type planAnswer struct {
	Plan        intake.Plan      `json:"plan"`
	Hash        string           `json:"hash"`
	Question    *intake.Question `json:"question"`
	CanStart    bool             `json:"canStart"`
	CanStartWhy string           `json:"canStartWhy,omitempty"`
	Degraded    bool             `json:"degraded"`
}

// finishPlan counts a plan attempt, writes its one audit line and answers it: the refusal, the plan
// and its view, or the degraded plan and its label. The request's and the plan's text reach neither
// the metric nor the log; a refusal names no repository, since the request may have named one this
// caller may not know exists (CD5, §security).
func (s *Server) finishPlan(ctx context.Context, run planRun, plan *intake.Plan, degraded bool, ref *intake.Refusal) (any, error) {
	outcome, reason := outcomePlanned, ""
	switch {
	case ref != nil:
		outcome, reason = outcomeRefused, ref.Reason
	case degraded:
		outcome = outcomeDegraded
	case plan != nil && plan.Question != nil:
		outcome = outcomeAsked
	}
	took := time.Since(run.start)
	if s.Metrics != nil {
		s.Metrics.IntakePlan(ctx, string(run.surface), outcome, reason)
		s.Metrics.IntakePlanSeconds(ctx, string(run.surface), outcome, took)
		s.Metrics.IntakePlanTokens(ctx, string(run.surface), "prompt", int64(run.usage.PromptTokens))
		s.Metrics.IntakePlanTokens(ctx, string(run.surface), "completion", int64(run.usage.CompletionTokens))
	}
	line := []any{"surface", string(run.surface), "requester", run.requester.Key, "outcome", outcome, "duration", took}
	if plan != nil {
		line = append(line, "repository", plan.Repository, "hash", plan.Hash, "model", plan.Planner.Model)
	}
	if ref != nil {
		line = append(line, "reason", ref.Reason)
	}
	if n := run.usage.PromptTokens + run.usage.CompletionTokens; n > 0 {
		line = append(line, "promptTokens", run.usage.PromptTokens, "completionTokens", run.usage.CompletionTokens)
	}
	s.log().Info("intake plan", line...)

	if ref != nil {
		return nil, s.withWritable(ctx, run.requester, ref)
	}
	canStart, why := false, startSponsored
	if !run.principal.Sponsored {
		canStart, why = s.canStart(ctx, run.principal, plan.Repository)
	}
	label := ""
	if degraded {
		label = intake.TemplateLabel
	}
	var b strings.Builder
	if err := roomview.Plan(&b, *plan, label, roomview.Options{Format: roomview.Markdown, Now: s.Intake.now()}); err != nil {
		return nil, err
	}
	return viewResult{text: b.String(), data: planAnswer{Plan: *plan, Hash: plan.Hash, Question: plan.Question,
		CanStart: canStart, CanStartWhy: why, Degraded: degraded}}, nil
}

// withWritable is the design's failure contract for the one refusal a person can act on: when no
// repository matched, the answer names the repositories this requester may actually file into —
// served by the allowlist, writable and public — rather than leaving them to guess (CD5's
// repositories are never named back, and the ones they may use are not a secret from them). Every
// other refusal is about the request itself and stands alone.
func (s *Server) withWritable(ctx context.Context, req intake.Requester, ref *intake.Refusal) *intake.Refusal {
	if ref == nil || ref.Reason != intake.ReasonRepositoryNotAllowed {
		return ref
	}
	served, err := s.intakeGuard(req.Login).Candidates(ctx, nil)
	if err != nil || len(served) == 0 {
		return ref
	}
	return &intake.Refusal{Reason: ref.Reason, Sentence: ref.Sentence + "; you can file into " + strings.Join(served, ", ")}
}

// asRefusal is err as the closed refusal it is, if it is one: Compose and Check answer a Refusal, and
// anything else is the broker's own failure rather than a person's.
func asRefusal(err error) (*intake.Refusal, bool) {
	var refusal *intake.Refusal
	if errors.As(err, &refusal) {
		return refusal, refusal != nil
	}
	return nil, false
}

// classNames is the class table's names, sorted: what the model is told it may name (CD17), in the
// one order two processes agree on.
func classNames(classes map[string]intake.Class) []string {
	out := make([]string, 0, len(classes))
	for name := range classes {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// intakePlanSchema is intake_plan's arguments, closed: a schema is the client's copy of intakeArgs'
// check, and no field outside it is taken.
var intakePlanSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["text"],` +
	`"properties":{"text":{"type":"string","minLength":1,"maxLength":16384},` +
	`"repository":{"type":"string","pattern":"^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$"},` +
	`"context":{"type":"string","maxLength":4096},` +
	`"candidates":{"type":"array","maxItems":20,"items":{"type":"string","pattern":"^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$"}},` +
	`"evidence":{"type":"array","maxItems":5,"items":{"type":"string","maxLength":280}},` +
	`"on_behalf_of":{"type":"string","pattern":"^slack:T[A-Z0-9]{2,20}/[UW][A-Z0-9]{2,20}$"},` +
	`"ceiling":{"type":"number","exclusiveMinimum":0}}}`)

// intakeTools are the planner's tools, listed only when the switch is on and on both lists: the
// coding agent's session and the Slack App plan through the same boundary.
func (s *Server) intakeTools() []mcpTool {
	return []mcpTool{
		{name: mcpwire.IntakePlan, call: s.intakePlanTool,
			description: "Plan the factory work a sentence asks for: one bounded planner call returns plan/v1 — the repository it files into, " +
				"one to three issues whose class, roles and ceiling the broker's config sets, and at most one question to ask the developer first — " +
				"with its hash. Show the text as it is; data carries the same plan, its hash, canStart (whether you may file it), the question and " +
				"degraded (a template draft, not the planner's). It files nothing: show the plan, then use issue_file.",
			schema: intakePlanSchema},
	}
}
