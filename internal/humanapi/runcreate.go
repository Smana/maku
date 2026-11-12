// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/runrequest"
	"github.com/Smana/maku/internal/sponsored"
)

// TaskCreator is the factory's intake api, as the broker reaches it with the caller's own token.
type TaskCreator interface {
	CreateTask(ctx context.Context, r runrequest.TaskRequest) (runrequest.TaskResult, error)
}

// maxIdempotencyKey keeps the key the factory sees, system:<name>:<key>, under its 255.
const maxIdempotencyKey = 128

// runCreateSchema requires max_cost (F4: "refused without max_cost"); additionalProperties false
// keeps class, tier and auto-merge out (F8). idempotency_key is at most 128, so the key the
// factory sees, system:<name>:<key>, stays under its 255. plan and plan_hash are the pair a caller
// that was shown a plan passes (CD19, CD27): both are optional, so an App without a plan sends the
// four fields it always sent, and the hash's pattern is plan_check's, interpolated, so a client's copy
// and the broker's cannot drift.
var runCreateSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["repo","text","dataClass","max_cost"],` +
	`"properties":{"repo":{"type":"string","pattern":"^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$"},"title":{"type":"string","maxLength":1024},` +
	`"text":{"type":"string","minLength":1,"maxLength":16384},"dataClass":{"type":"string","enum":["public"]},"max_cost":{"type":"number","exclusiveMinimum":0},` +
	`"on_behalf_of":{"type":"string","pattern":"^slack:T[A-Z0-9]{2,20}/[UW][A-Z0-9]{2,20}$"},"ref":{"type":"string","maxLength":256},` +
	`"idempotency_key":{"type":"string","minLength":1,"maxLength":128},` +
	`"plan":{"type":"object"},"plan_hash":{"type":"string","pattern":"` + hashRE.String() + `"}}}`)

var onBehalfRE = regexp.MustCompile(`^slack:T[A-Z0-9]{2,20}/[UW][A-Z0-9]{2,20}$`)

type runCreateArgs struct {
	Repo           string  `json:"repo"`
	Title          string  `json:"title"`
	Text           string  `json:"text"`
	DataClass      string  `json:"dataClass"`
	MaxCost        float64 `json:"max_cost"`
	OnBehalfOf     string  `json:"on_behalf_of"`
	Ref            string  `json:"ref"`
	IdempotencyKey string  `json:"idempotency_key"`
	// Plan is the plan/v2 the task was confirmed from, and PlanHash the sha256: value the broker
	// answered beside it: the App's own copy of what intake_plan said, carried from the proposal
	// message's metadata (CD20). Neither is trusted — checkPlan recomputes the hash over the plan and
	// re-validates it — and a call carrying neither is the tool it always was.
	Plan     json.RawMessage `json:"plan"`
	PlanHash string          `json:"plan_hash"`
}

// slackFiles is how many issues one run_create call files: a task is one issue's work, and a plan of
// two or three is filed one call per issue by whoever owns the message (CD19: "Slack can file exactly
// one").
const slackFiles = 1

// checkPlan holds a run_create call to the plan it was confirmed from, and the plan itself to the
// broker that made it (CD19, CD27): the hash is recomputed over the plan the call carries, the four
// fields the App still sends are checked against the plan's own, and the plan is re-validated through
// the checks plan_check runs — the requester its guard resolves, the allowlist, the class table and
// the issue template. Nil means the call may be filed; the existing path below then runs untouched, so
// what passes is still checked by the one validator.
//
// The order is the security property, as it is in plan_check: the hash first, because it is the
// cheapest and the whole of what plan_changed means — no GitHub read is spent on a plan that is not
// the one anyone confirmed; then the shape and the money, which cost nothing; then the guard, which
// asks GitHub; then the contract. The plan is decoded as strictly as plan_check decodes it, so a field
// outside plan/v2 is refused here for the same reason it is refused there. The one half of plan_check
// not repeated here is the redactor's, and it is not skipped: the path below runs checkDraft — the
// template and the redactor both — on the title and body this function has just required to be the
// plan's.
//
// A plan/v1 is not refused and not filed: it comes back as replanned, the plan/v2 to confirm (P14).
func (s *Server) checkPlan(ctx context.Context, a runCreateArgs, p authn.Principal) error {
	var plan intake.Plan
	if mcpwire.DecodeArgs(a.Plan, &plan) != nil {
		return argRefusal("plan is the plan/v2 object intake_plan answered with, with no field outside it taken, " +
			"and plan_hash the value it answered beside it")
	}
	if !hashRE.MatchString(a.PlanHash) {
		return argRefusal("plan is passed with plan_hash, the sha256: value intake_plan answered with")
	}
	// The hash over the plan the call carries, never the plan's own claim about itself: a plan edited
	// after it was shown is not the plan a person confirmed.
	if intake.Hash(plan) != a.PlanHash {
		return intake.ReasonRefusal(intake.ReasonPlanChanged)
	}
	if len(plan.Issues) != slackFiles {
		return argRefusal("Slack files exactly one issue: the plan carries " + strconv.Itoa(len(plan.Issues)))
	}
	// The four fields the App still sends are the plan's, byte for byte but for the repository's case (the App
	// files into the channel's spelling, the planner lowercases): the hash covers the
	// repository, the title and the body, so a call that spells any of them differently is a call
	// about a different task.
	if issue := plan.Issues[0]; !strings.EqualFold(a.Repo, plan.Repository) || a.Title != issue.Title || a.Text != issue.Body {
		return intake.ReasonRefusal(intake.ReasonPlanChanged)
	}
	if a.MaxCost > plan.Budget.CeilingUSD {
		return argRefusal("max_cost is above the plan's ceiling: the plan's work is charged against the ceiling the broker set")
	}
	if s.Intake == nil {
		// A broker whose planner is off made no plan and can re-validate none: the plan came from
		// somewhere else, and it is refused rather than filed on checks that cannot be made. The
		// switch-off route carries no plan at all.
		return intake.ReasonRefusal(intake.ReasonPlannerUnavailable)
	}
	if a.OnBehalfOf == "" {
		// A plan is re-validated for the Slack user it was planned for: without one, nothing says
		// whose write access the guard should ask about, and the App's own identity is one string for
		// a whole workspace.
		return argRefusal("on_behalf_of is required with a plan: it is the Slack user the plan was made for")
	}
	if plan.APIVersion == intake.APIVersionV1 {
		// P14: a plan made before profiles is never filed; it is shown again as plan/v2 (replanned).
		next, ref := s.upgrade(ctx, p, a.OnBehalfOf, plan)
		if ref != nil {
			return ref
		}
		return replanned{plan: next}
	}
	_, candidates, ref := s.requester(ctx, p, intakeArgs{
		Repository: plan.Repository, Candidates: alternativeRepos(plan), OnBehalfOf: a.OnBehalfOf})
	if ref != nil {
		return ref
	}
	if ref := intake.CheckFinal(plan, candidates, s.planClasses(ctx, p, a.OnBehalfOf, plan.Repository)); ref != nil {
		return ref
	}
	// The pin, against the catalogue as the Slack user reads it (P5, P6); the factory checks it again.
	if ref := s.checkPins(ctx, p, a.OnBehalfOf, plan, intake.SurfaceSlack); ref != nil {
		return ref
	}
	return nil
}

// argRefusal is one of run_create's argument refusals, in the shape checkPlan answers with: a call
// whose fields disagree with the plan it carries is the call's own error rather than the planner's, so
// its reason is the tool error's word for it and its sentence is the line a client reads, once. A
// plan-level refusal keeps intake's own reason and sentence, which is what makes plan_changed here the
// same value issue_file reads.
func argRefusal(sentence string) *intake.Refusal {
	return &intake.Refusal{Reason: "invalid_arguments", Sentence: "invalid_arguments: " + sentence}
}

// planPresent is whether the call carries a plan: an older App sends neither field, and a client that
// sends the pair sends an object.
func planPresent(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null":
		return false
	}
	return true
}

// createTask asks the factory for an api task with the caller's own token (C4): the factory
// authenticates it again and decides everything (R4). Only a sponsored principal reaches it. A call
// carrying a plan and its hash is held to it before anything else is asked (checkPlan, CD19/CD27), so
// what is filed is what the person confirmed; a call carrying neither is the tool it always was; and a
// call carrying only one of the pair is refused as the arguments' error, since a hash with no plan
// names nothing to check. The text passes the broker's redactor whatever it is (C12: the factory has
// none), a titled draft issue_draft's whole check. The key defaults to the arguments' hash, so a
// retried call creates one task.
func (s *Server) createTask(ctx context.Context, p authn.Principal, args json.RawMessage) (runrequest.TaskResult, error) {
	if !p.Sponsored {
		return runrequest.TaskResult{}, toolError("unknown tool: run_create")
	}
	var a runCreateArgs
	if mcpwire.DecodeArgs(args, &a) != nil || !repositoryRE.MatchString(a.Repo) || a.MaxCost <= 0 || a.Text == "" ||
		a.DataClass != "public" || len(a.IdempotencyKey) > maxIdempotencyKey {
		return runrequest.TaskResult{}, toolError("invalid_arguments: repo, text, dataClass public and max_cost above 0 are required; " +
			"idempotency_key is at most 128 characters")
	}
	if !sponsored.ValidRef(a.Ref) {
		return runrequest.TaskResult{}, toolError("bad_ref: ref is a git ref of at most 256 characters")
	}
	if a.OnBehalfOf != "" && !onBehalfRE.MatchString(a.OnBehalfOf) {
		return runrequest.TaskResult{}, toolError("invalid_arguments: on_behalf_of is slack:<team>/<user>")
	}
	if !planPresent(a.Plan) && a.PlanHash != "" {
		// Half the pair names nothing to check: a hash is a claim about a plan, and there is no plan
		// here to hold it to. Refused rather than filed, because a caller that sends the hash meant to
		// send a plan and silently filing the arguments alone would file what nobody confirmed.
		return runrequest.TaskResult{}, toolError("invalid_arguments: plan and plan_hash are passed together: " +
			"a plan_hash with no plan names nothing to check")
	}
	if planPresent(a.Plan) {
		if err := s.checkPlan(ctx, a, p); err != nil {
			var rp replanned
			if errors.As(err, &rp) {
				return runrequest.TaskResult{}, rp
			}
			return runrequest.TaskResult{}, toolError(err.Error())
		}
	}
	planned := s.plannedIssue(ctx, p, a)
	if a.Title != "" {
		d, err := s.checkFinalDraft(ctx, a.Title, a.Text)
		if err != nil {
			return runrequest.TaskResult{}, err
		}
		a.Title, a.Text = d.Title, d.Body
	} else if err := s.redactCheck(ctx, a.Text); err != nil {
		return runrequest.TaskResult{}, err
	}
	if s.Tasks == nil {
		return runrequest.TaskResult{}, toolError("no_factory: the factory is not configured")
	}
	key := a.IdempotencyKey
	if key == "" {
		sum := sha256.Sum256(args)
		key = hex.EncodeToString(sum[:16])
	}
	res, err := s.Tasks.CreateTask(ctx, runrequest.TaskRequest{Repository: a.Repo, Title: a.Title, Text: a.Text,
		DataClass: a.DataClass, MaxCostUSD: a.MaxCost, OnBehalfOf: a.OnBehalfOf, Ref: a.Ref,
		AccessToken: p.AccessToken, IdempotencyKey: p.ID + ":" + key,
		Class: planned.Class, Suggested: planned.suggested(), Profile: planned.Profile})
	var refused runrequest.ErrRefused
	if errors.As(err, &refused) {
		return runrequest.TaskResult{}, toolError("rejected: " + refused.Reason)
	}
	if err == nil && planned.Class != "" && !res.Duplicate {
		s.countChoices(ctx, intake.Plan{Issues: []intake.Issue{intake.Issue(planned)}}, intake.SurfaceSlack)
	}
	return res, err
}

// redactCheck refuses text holding what the redactor flags, failing closed without a redactor.
func (s *Server) redactCheck(ctx context.Context, text string) error {
	if s.Actor == nil || s.Actor.Redactor == nil {
		return errors.New("humanapi: no redactor")
	}
	_, rules, err := s.Actor.Redactor.Payload(ctx, envelope.Must(map[string]string{"text": text}))
	if err != nil {
		return err
	}
	if len(rules) > 0 {
		return toolError("invalid_draft: it holds what looks like a secret (" + strings.Join(rules, ", ") +
			"): remove it, since the factory's agents read the task in full")
	}
	return nil
}

// runCreated is run_create's answer without a task: the factory's, and the room page.
func (s *Server) runCreated(r runrequest.TaskResult) map[string]any {
	out := map[string]any{"task": r.Task, "room": r.Room, "issue": r.Issue, "duplicate": r.Duplicate, "url": s.PublicURL + "/r/" + r.Room}
	if r.Sponsor != "" {
		out["sponsor"] = r.Sponsor
	}
	return out
}

// plannedIssue is the one issue a checked plan files (P5, P7): its class and pin are bound by the
// plan's hash; its suggestion is display and the client's copy, so it is rebuilt from the plan
// repository's catalogue first (review B2): a profile the class does not offer, or any on the config
// fallback, is never stored. The zero issue without a plan.
func (s *Server) plannedIssue(ctx context.Context, p authn.Principal, a runCreateArgs) plannedTask {
	var plan intake.Plan
	if !planPresent(a.Plan) || json.Unmarshal(a.Plan, &plan) != nil || len(plan.Issues) != slackFiles {
		return plannedTask{}
	}
	plan = intake.Resuggest(plan, s.planClasses(ctx, p, a.OnBehalfOf, plan.Repository))
	return plannedTask(plan.Issues[0])
}

// plannedTask is a planned issue as run_create passes it on.
type plannedTask intake.Issue

// suggested is the broker's suggested profile, "" for none or the factory's choice.
func (t plannedTask) suggested() string {
	if t.Suggestion == nil {
		return ""
	}
	return t.Suggestion.Profile
}
