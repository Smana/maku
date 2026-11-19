// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
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
// sentence becomes plan/v2 through one bounded model call, with the repository bounded by the
// allowlist and by what the caller may write, the roles and the money written by config, and the
// plan's hash the value every later door re-validates. It files nothing and stores nothing.
//
// The order below is the security property, not a convenience: arguments, then who is asking and
// which repositories they may use, then the daily cap, then the one call, then the compose, then the
// check — so that nothing reaches a model before every check it can fail has been made, and nothing
// is shown before the plan has been held to the contract.

// The consumer revision of an initial plan. A verified intake revision carries its own Rev forward
// into the re-composed plan; the hash covers the plan's material, never this display counter.
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
	// Paths answers whether a served repository holds a file at path, through the factory App's
	// read. Nil grounds no quoted path: the planner then chooses among the candidates (#626).
	Paths func(ctx context.Context, repository, path string) (bool, error)
	// Links resolves a Slack user to the account it is linked to. Nil refuses every sponsored plan.
	Links SlackLinkFinder
	// Daily is the per-requester cap in a UTC day. Below one, no plan is made: a floor of zero would
	// otherwise read as "no cap", which is the opposite of what an operator typed.
	Daily int
	// MaxRounds is the clarification-round cap. A revision reaching it accepts the remaining
	// recommendations and returns a final plan without another model call.
	MaxRounds int
	// Fallback is the config's fallback: `off` leaves the degraded composer unreachable, `composer`
	// makes it the answer when the call fails rather than a plan.
	Fallback string
	// Now is the clock the daily cap and the plan's composition read; nil means time.Now.
	Now func() time.Time

	limits   dailyLimits
	unserved unservedWarnings
}

type unservedWarnings struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (in *Intake) warnUnserved(log *slog.Logger, repository string) {
	key := strings.ToLower(strings.TrimSpace(repository))
	in.unserved.mu.Lock()
	if in.unserved.seen == nil {
		in.unserved.seen = map[string]bool{}
	}
	already := in.unserved.seen[key]
	in.unserved.seen[key] = true
	in.unserved.mu.Unlock()
	if !already {
		log.Warn("human.intake.plan.repositories names a repository the factory does not serve", "repository", repository)
	}
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
// alone — whom it plans for and the ceiling its channel imposes. Plan, Hash and Candidate are the
// optional revision envelope; all three arrive together or none does.
type intakeArgs struct {
	Text       string       `json:"text"`
	Repository string       `json:"repository"`
	Context    string       `json:"context"`
	Candidates []string     `json:"candidates"`
	Evidence   []string     `json:"evidence"`
	OnBehalfOf string       `json:"on_behalf_of"`
	Ceiling    float64      `json:"ceiling"`
	Plan       *intake.Plan `json:"plan,omitempty"`
	Hash       string       `json:"hash,omitempty"`
	Candidate  *intake.Plan `json:"candidate,omitempty"`
}

// check holds the arguments to the contract's bounds and to what this principal may ask for at all:
// a session cannot claim a person or widen its ceiling — both are a sponsored principal's, which
// reaches the planner on the channel's behalf — and a sponsored principal plans for a linked user,
// never for itself, which has no write access to name.
func (a intakeArgs) check(p authn.Principal) error {
	switch {
	case a.Candidate == nil && (a.Plan != nil || a.Hash != ""), a.Candidate != nil && a.Plan == nil:
		return toolError("invalid_arguments: a revision carries plan, hash and candidate together")
	case a.Candidate != nil && !hashRE.MatchString(a.Hash):
		return toolError("invalid_arguments: a revision's hash is the base plan's sha256: value")
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
	// on_behalf_of and ceiling are a sponsored principal's alone: the rule is the plan's and the
	// re-validation's, so it lives in claims and both doors read it there.
	return claims(p, a.OnBehalfOf, a.Ceiling)
}

// claims holds the arguments that are a principal's rather than a request's, so every door applies
// them the same way: a session plans for its own token and cannot name a person or widen a ceiling,
// and a sponsored principal plans for a linked user rather than for itself — which has no write
// access to name, since its identity is one string for a whole workspace.
func claims(p authn.Principal, onBehalfOf string, ceiling float64) error {
	if !p.Sponsored {
		if onBehalfOf != "" {
			return toolError("invalid_arguments: on_behalf_of is a sponsored principal's: a session plans for its own token")
		}
		if ceiling != 0 {
			return toolError("invalid_arguments: ceiling is a sponsored principal's: a session's ceiling is the class's, from config")
		}
		return nil
	}
	if onBehalfOf == "" {
		return toolError("invalid_arguments: on_behalf_of is required: a sponsored principal plans for a linked Slack user")
	}
	if !onBehalfRE.MatchString(onBehalfOf) {
		return toolError("invalid_arguments: on_behalf_of is slack:<team>/<user>")
	}
	if ceiling < 0 {
		return toolError("invalid_arguments: ceiling is a positive amount of dollars, or absent")
	}
	return nil
}

// asked is the repositories the request named: the one field first, then the candidates, so that a
// single explicitly named repository is the one the guard refuses by name (CD5).
func (a intakeArgs) asked() []string {
	var out []string
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

	// The revision is verified before any GitHub or model work. In particular, base drift and an
	// above-cap round cannot spend a lookup, a daily slot or a model call.
	if a.Plan != nil && a.Plan.APIVersion == intake.APIVersionV1 {
		// P14: a revision of a plan made before profiles is upgraded, not re-planned: no daily plan
		// and no model call are spent on it (review nit 1). Its hash binds it first.
		if intake.Hash(*a.Plan) != a.Hash {
			return s.finishPlan(ctx, run, nil, false, intake.ReasonRefusal(intake.ReasonPlanChanged))
		}
		next, ref := s.upgrade(ctx, p, a.OnBehalfOf, *a.Plan)
		if ref != nil {
			return s.finishPlan(ctx, run, nil, false, ref)
		}
		return replanned{plan: next}.result(s)
	}
	var revised *intake.Plan
	if a.Candidate != nil {
		next, err := intake.Revise(*a.Plan, a.Hash, *a.Candidate, s.Intake.MaxRounds)
		if err != nil {
			ref, ok := asRefusal(err)
			if !ok {
				return nil, err
			}
			return s.finishPlan(ctx, run, nil, false, ref)
		}
		revised = &next
	}

	req, candidates, ref := s.requester(ctx, p, a)
	run.requester = req
	if ref != nil {
		return s.finishPlan(ctx, run, nil, false, ref)
	}
	if revised != nil && intake.IsProfileRevision(*a.Plan, *a.Candidate) {
		// A pin is a revision with no model call and no daily plan: the plan is final (Revise
		// admits a pin only once every gap is answered), so it is checked as plan_check checks it.
		return s.finishPinned(ctx, run, *revised, candidates, a.OnBehalfOf)
	}
	// A path the request quotes picks the repositories that hold it (#626); a revision keeps the
	// repository its base was planned for.
	said := a.Text + "\n" + a.Context
	var grounded []string
	if revised == nil {
		if holding, found := s.groundPaths(ctx, said, candidates); found {
			grounded = holding
		}
	}
	// A request that names a repository is planned for that repository or refused, before a daily
	// slot or a model call is spent: the planner never picks a different one (#469). The reply that
	// re-plans carries its words in the context, so it is read the same way. Whether a repository
	// is served is asked of the whole served set, never of the narrowed candidates: a served one
	// named in passing beside the anchored candidate is ignored (#635).
	known := s.knownRepositories(ctx, p, a, candidates)
	candidates, ref = intake.CheckNamed(said, known, s.servedRepositories(ctx, p, candidates), candidates, grounded)
	if ref != nil {
		return s.finishPlan(ctx, run, nil, false, ref)
	}
	if revised == nil && a.Repository == "" && len(candidates) > 1 {
		run.choices = candidates
	}
	if !s.Intake.take(req.Key) {
		return s.finishPlan(ctx, run, nil, false, intake.ReasonRefusal(intake.ReasonDailyPlansReached))
	}
	// Each candidate's classes from the factory's catalogue (P2, G4); a candidate the broker could not
	// read one for plans from the config table, with no suggestion (until stage 5).
	offers := s.planOffers(ctx, p, a.OnBehalfOf, candidates)
	classesOf := func(pl intake.Plan) map[string]intake.Class {
		return intake.ClassesFor(offers, s.Intake.Classes, pl.Repository)
	}

	if revised != nil {
		// The verified candidate is both a plan a person may see and, below the cap, the model's
		// input. Hold it to the normal guard, template and redactor before either use. At the cap
		// there are no markers left, so the final validator is the one that applies.
		var ref *intake.Refusal
		if revised.RoundsLeft == 0 {
			ref = intake.CheckFinal(*revised, candidates, classesOf(*revised))
		} else {
			ref = intake.Check(*revised, candidates, classesOf(*revised))
		}
		if ref != nil {
			return s.finishPlan(ctx, run, nil, false, ref)
		}
		if err := s.checkPlanDrafts(ctx, *revised); err != nil {
			ref, ok := asRefusal(err)
			if !ok {
				return nil, err
			}
			return s.finishPlan(ctx, run, nil, false, ref)
		}
		if revised.RoundsLeft == 0 {
			final := intake.Resuggest(*revised, classesOf(*revised)) // the candidate's suggestion is the client's
			return s.finishPlan(ctx, run, &final, false, nil)
		}
	}

	draft, usage, err := s.Intake.Model.Draft(ctx, intake.DraftRequest{
		Text: a.Text, Context: a.modelContext(), Candidates: candidates, Classes: classNames(s.Intake.Classes),
		Offers: offers, Candidate: revised})
	run.usage = usage
	if err != nil {
		ref, ok := intake.RefusalOf(err)
		if !ok {
			return nil, err // not one of the call's three failures: the tool answers "unavailable"
		}
		run.cause = causeOf(err)
		// Only an initial plan may degrade to the template. A failed revision must never discard
		// the person's settled answers in favour of a freshly composed draft.
		if revised == nil {
			if plan, ok := s.degraded(a, candidates, run.surface, ref); ok {
				return s.finishPlan(ctx, run, plan, true, nil)
			}
		}
		return s.finishPlan(ctx, run, nil, false, ref)
	}

	compose := intake.ComposeIn{Surface: run.surface, Model: s.Intake.alias(), ServedModel: usage.Model, Now: s.Intake.now(),
		Rev: planRev, Round: 1, RoundsLeft: s.Intake.MaxRounds - 1,
		Classes: s.Intake.Classes, Offers: offers, CeilingUSD: a.Ceiling, CeilingSource: ceilingSource, Logger: s.log()}
	if revised != nil {
		compose.Rev, compose.Round, compose.RoundsLeft = revised.Rev, revised.Round, revised.RoundsLeft
	}
	plan, err := intake.Compose(draft, compose)
	if err != nil {
		ref, ok := asRefusal(err)
		if !ok {
			return nil, err
		}
		return s.finishPlan(ctx, run, nil, false, ref)
	}
	// The deterministic floor: gaps the model did not ask for, from the bodies and the request alone.
	// It runs before the guard so the guard checks the plan that will be shown.
	plan = intake.ApplyFloor(plan, a.Text+"\n"+a.modelContext())
	// The plan is checked before it is shown: a reply that names a repository, an alternative or a
	// class outside what this request may use is refused whole, and the outsider is never echoed.
	if ref := intake.Check(plan, candidates, classesOf(plan)); ref != nil {
		return s.finishPlan(ctx, run, nil, false, ref)
	}
	if revised != nil {
		if ref := intake.CheckCarryForward(*a.Plan, *a.Candidate, plan); ref != nil {
			return s.finishPlan(ctx, run, nil, false, ref)
		}
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
	plan.Rev, plan.Round, plan.RoundsLeft = planRev, 1, s.Intake.MaxRounds-1
	return &plan, true
}

// planChecked holds the plan validated at confirmation with the hash recomputed over it, and whether
// this caller may start the factory on it.
type planChecked struct {
	Plan        intake.Plan `json:"plan"`
	Hash        string      `json:"hash"`
	CanStart    bool        `json:"canStart"`
	CanStartWhy string      `json:"canStartWhy,omitempty"`
	// Maintainer is the caller's flag from the plan repository's catalogue (P5): start skips a
	// class: label the factory would not count. Absent when the broker read no catalogue.
	Maintainer *bool `json:"maintainer,omitempty"`
}

// planCheckRefused counts a closed refusal during final plan validation under its own reason on the
// planner's series.
func (s *Server) planCheckRefused(ctx context.Context, surface intake.Surface, err error) error {
	if ref, ok := asRefusal(err); ok && s.Metrics != nil {
		s.Metrics.IntakePlan(ctx, string(surface), outcomeRefused, ref.Reason)
	}
	return err
}

// alternativeRepos is the plan's alternatives as candidates, so the guard holds them to the same
// allowlist, write and public checks as the plan's own repository: an alternative is display, and
// display is shown to the person, so an outsider in it refuses the plan whole (Review Focus 1).
func alternativeRepos(p intake.Plan) []string {
	out := make([]string, 0, len(p.Alternatives))
	for _, a := range p.Alternatives {
		out = append(out, a.Repository)
	}
	return out
}

// checkPlanDrafts applies the same issue template and redactor to every issue, on both the final
// plan_check door and a verified revision before it is shown or sent back to the model.
func (s *Server) checkPlanDrafts(ctx context.Context, plan intake.Plan) error {
	for _, is := range plan.Issues {
		if _, err := s.checkDraft(ctx, is.Title, is.Body); err != nil {
			return draftRefusal(err)
		}
	}
	return nil
}

// draftRefusal is checkDraft's answer as the closed list's invalid_draft: the tool error already reads
// `invalid_draft: …`, so the refusal's sentence is that message and Error() shows it once. An error
// that is not a tool error — no redactor wired, or a redactor that failed — is not a person's to read
// and stays the broker's own.
func draftRefusal(err error) error {
	var te toolError
	if !errors.As(err, &te) {
		return err
	}
	return &intake.Refusal{Reason: intake.ReasonInvalidDraft, Sentence: te.Error()}
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

	served, err := s.planRepositories(ctx, p)
	if err != nil {
		return req, nil, (&intake.ProfileRefusal{Code: intake.CatalogueUnavailable}).Refusal()
	}
	probe := s.probe(a, served)
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

	candidates, ref := s.intakeGuard(login, allowMap(served)).Candidates(ctx, a.asked())
	if ref != nil {
		return req, nil, ref
	}
	return req, candidates, nil
}

// maxGroundPaths bounds how many quoted paths one plan looks up in each candidate.
const maxGroundPaths = 3

// groundPaths narrows candidates to the ones that hold a repository path text quotes (#626): a path
// that exists in one served repository is the strongest evidence of where the change goes. found is
// false, and candidates come back unchanged, when text quotes no path, no candidate holds one, or
// nothing can look one up; a lookup that fails counts as not holding it. A single candidate is looked
// up too: the path it holds anchors the request on it, so a served repository named in passing is
// not taken for the target (#635).
func (s *Server) groundPaths(ctx context.Context, text string, candidates []string) ([]string, bool) {
	if s.Intake.Paths == nil || len(candidates) == 0 {
		return candidates, false
	}
	paths := intake.PathsIn(text)
	if len(paths) == 0 {
		return candidates, false
	}
	paths = paths[:min(len(paths), maxGroundPaths)]
	ctx, cancel := context.WithTimeout(ctx, accessWait)
	defer cancel()
	var holding []string
	for _, c := range candidates {
		for _, path := range paths {
			ok, err := s.Intake.Paths(ctx, c, path)
			if err != nil {
				s.log().Warn("intake: path lookup failed", "repository", c, "err", err)
				continue
			}
			if ok {
				holding = append(holding, c)
				break
			}
		}
	}
	if len(holding) == 0 {
		return candidates, false
	}
	return holding, true
}

// servedRepositories is every repository served here: the factory's catalogue (or the allowlist)
// and the candidates. It is what `repository_not_served_here` is decided against (#635).
func (s *Server) servedRepositories(ctx context.Context, p authn.Principal, candidates []string) []string {
	served := slices.Clone(candidates)
	if more, err := s.planRepositories(ctx, p); err == nil {
		served = append(served, more...)
	}
	return served
}

// knownRepositories is every repository the broker can tell a request names: the allowlist, the
// ones the request listed and the candidates.
func (s *Server) knownRepositories(ctx context.Context, p authn.Principal, a intakeArgs, candidates []string) []string {
	known := slices.Clone(candidates)
	known = append(known, a.asked()...)
	if served, err := s.planRepositories(ctx, p); err == nil {
		known = append(known, served...)
	}
	for repository, on := range s.Intake.Allow {
		if on {
			known = append(known, repository)
		}
	}
	return known
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
// the served allowlist in its one order (sorted: two replicas must agree).
func (s *Server) probe(a intakeArgs, served []string) []string {
	if asked := a.asked(); len(asked) > 0 {
		return asked
	}
	return served
}

// planRepositories is the sorted list of repositories the planner serves for p (WF12): the factory's
// served list when human.intake.plan.repositories is unset, or its intersection with Allow when set
// (logging a warning naming any Allow entry the factory does not serve).
func (s *Server) planRepositories(ctx context.Context, p authn.Principal) ([]string, error) {
	if s.Catalogue == nil {
		if len(s.Intake.Allow) > 0 {
			out := make([]string, 0, len(s.Intake.Allow))
			seen := make(map[string]bool, len(s.Intake.Allow))
			for repository, on := range s.Intake.Allow {
				key := strings.ToLower(strings.TrimSpace(repository))
				if on && key != "" && !seen[key] {
					seen[key] = true
					out = append(out, key)
				}
			}
			slices.Sort(out)
			return out, nil
		}
		return nil, errCatalogueUnavailable
	}
	served, err := s.Catalogue.Repositories(ctx, p)
	if err != nil {
		if !errors.Is(err, errCatalogueUnavailable) {
			err = fmt.Errorf("%w: %w", errCatalogueUnavailable, err)
		}
		return nil, err
	}
	servedSet := make(map[string]bool, len(served))
	fromFactory := make([]string, 0, len(served))
	for _, repository := range served {
		key := strings.ToLower(strings.TrimSpace(repository))
		if key == "" || servedSet[key] {
			continue
		}
		servedSet[key] = true
		fromFactory = append(fromFactory, key)
	}
	slices.Sort(fromFactory)
	if len(s.Intake.Allow) == 0 {
		return fromFactory, nil
	}
	narrow := make(map[string]string, len(s.Intake.Allow))
	for repository, on := range s.Intake.Allow {
		if on {
			if key := strings.ToLower(strings.TrimSpace(repository)); key != "" {
				narrow[key] = repository
			}
		}
	}
	narrowKeys := make([]string, 0, len(narrow))
	for key := range narrow {
		narrowKeys = append(narrowKeys, key)
	}
	slices.Sort(narrowKeys)
	for _, key := range narrowKeys {
		if !servedSet[key] {
			s.Intake.warnUnserved(s.log(), narrow[key])
		}
	}
	out := make([]string, 0, len(fromFactory))
	for _, key := range fromFactory {
		if _, ok := narrow[key]; ok {
			out = append(out, key)
		}
	}
	return out, nil
}

func allowMap(repos []string) map[string]bool {
	out := make(map[string]bool, len(repos))
	for _, r := range repos {
		out[r] = true
	}
	return out
}

// intakeGuard is the guard for one effective requester: the allowlist, the public-only read, and the
// write check closing over the login — the same pair canStart uses (ghidentity.Resolver into
// repoaccess.Checker), so an intake plan and an issue_draft cannot disagree about who may file. A
// broker with no write check wired verifies nothing, and the guard then refuses rather than planning
// on the assumption.
func (s *Server) intakeGuard(login string, allow map[string]bool) intake.Guard {
	return intake.Guard{Allow: allow, Public: s.Intake.Public,
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
	// cause is why the planner call failed, for the operator's audit line alone: the model's
	// error names a status, a deadline or a missing key file, never the key itself.
	cause string
	// choices are the candidates left when nothing the request said grounded one of them: more
	// than one is the repository question a handoff asks (#626).
	choices []string
}

// maxCause bounds a cause in the log.
const maxCause = 300

// causeOf is err's text for the audit line, bounded.
func causeOf(err error) string {
	c := err.Error()
	if len(c) > maxCause {
		c = c[:maxCause] + "..."
	}
	return c
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
	// choices is planRun's, for handoff alone: never on the wire.
	choices []string
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
	if run.cause != "" {
		line = append(line, "cause", run.cause)
	}
	if n := run.usage.PromptTokens + run.usage.CompletionTokens; n > 0 {
		line = append(line, "promptTokens", run.usage.PromptTokens, "completionTokens", run.usage.CompletionTokens)
	}
	s.log().Info("intake plan", line...)

	if ref != nil {
		return nil, s.withWritable(ctx, run.principal, run.requester, ref)
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
		CanStart: canStart, CanStartWhy: why, Degraded: degraded, choices: run.choices}}, nil
}

// withWritable is the design's failure contract for the one refusal a person can act on: when no
// repository matched, the answer names the repositories this requester may actually file into —
// served by the allowlist, writable and public — rather than leaving them to guess (CD5's
// repositories are never named back, and the ones they may use are not a secret from them). Every
// other refusal is about the request itself and stands alone.
func (s *Server) withWritable(ctx context.Context, p authn.Principal, req intake.Requester, ref *intake.Refusal) *intake.Refusal {
	if ref == nil || ref.Reason != intake.ReasonRepositoryNotAllowed {
		return ref
	}
	repos, rerr := s.planRepositories(ctx, p)
	if rerr != nil {
		return ref
	}
	served, err := s.intakeGuard(req.Login, allowMap(repos)).Candidates(ctx, nil)
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
// check, and no field outside it is taken. One bound a JSON Schema cannot express — context and
// evidence together are a single 4 KiB budget, which intakeArgs.check holds the sum of — is stated on
// both properties, where a client reads it.
// hashRE is a plan hash's shape: what Hash answers with.
var hashRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
