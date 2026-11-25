// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
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
// and the broker's cannot drift. handoff is the third way (UX2): the id of a plan the broker stored,
// in place of plan and plan_hash, and of the repository, title and text the stored plan already
// carries; repo and text are therefore required by the tool itself unless a handoff is passed.
var runCreateSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["dataClass","max_cost"],` +
	`"properties":{"repo":{"type":"string","pattern":"^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$"},"title":{"type":"string","maxLength":1024},` +
	`"text":{"type":"string","minLength":1,"maxLength":16384},"dataClass":{"type":"string","enum":["public"]},"max_cost":{"type":"number","exclusiveMinimum":0},` +
	`"on_behalf_of":{"type":"string","pattern":"^slack:T[A-Z0-9]{2,20}/[UW][A-Z0-9]{2,20}$"},"ref":{"type":"string","maxLength":256},` +
	`"idempotency_key":{"type":"string","minLength":1,"maxLength":128},` +
	`"plan":{"type":"object"},"plan_hash":{"type":"string","pattern":"` + hashRE.String() + `"},` +
	`"handoff":{"type":"string","minLength":1,"maxLength":128}}}`)

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
	// Handoff is the id of a plan the broker stored (UX2): the call files that plan, after
	// handoffCheck, and carries no plan of its own. PlanHash may ride beside it, binding the plan the
	// person was shown to the one stored.
	Handoff string `json:"handoff"`
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
	if !s.wholePlans() && len(plan.Issues) != slackFiles {
		return argRefusal("Slack files exactly one issue: the plan carries " + strconv.Itoa(len(plan.Issues)))
	}
	if len(plan.Issues) == 0 {
		return argRefusal("the plan carries no issues")
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
	first, _, err := s.createTasks(ctx, p, args)
	return first, err
}

func (s *Server) createTasks(ctx context.Context, p authn.Principal, args json.RawMessage) (runrequest.TaskResult, []runrequest.TaskResult, error) {
	if !p.Sponsored {
		return runrequest.TaskResult{}, nil, toolError("unknown tool: run_create")
	}
	var a runCreateArgs
	bad := toolError("invalid_arguments: repo, text, dataClass public and max_cost above 0 are required; " +
		"idempotency_key is at most 128 characters")
	if mcpwire.DecodeArgs(args, &a) != nil || a.MaxCost <= 0 || a.DataClass != "public" || len(a.IdempotencyKey) > maxIdempotencyKey {
		return runrequest.TaskResult{}, nil, bad
	}
	if a.Handoff != "" {
		if err := s.fromHandoff(ctx, p, &a); err != nil {
			return runrequest.TaskResult{}, nil, err
		}
	}
	if !repositoryRE.MatchString(a.Repo) || a.Text == "" {
		return runrequest.TaskResult{}, nil, bad
	}
	if !sponsored.ValidRef(a.Ref) {
		return runrequest.TaskResult{}, nil, toolError("bad_ref: ref is a git ref of at most 256 characters")
	}
	if a.OnBehalfOf != "" && !onBehalfRE.MatchString(a.OnBehalfOf) {
		return runrequest.TaskResult{}, nil, toolError("invalid_arguments: on_behalf_of is slack:<team>/<user>")
	}
	if !planPresent(a.Plan) && a.PlanHash != "" {
		// Half the pair names nothing to check: a hash is a claim about a plan, and there is no plan
		// here to hold it to. Refused rather than filed, because a caller that sends the hash meant to
		// send a plan and silently filing the arguments alone would file what nobody confirmed.
		return runrequest.TaskResult{}, nil, toolError("invalid_arguments: plan and plan_hash are passed together: " +
			"a plan_hash with no plan names nothing to check")
	}
	if planPresent(a.Plan) {
		if err := s.checkPlan(ctx, a, p); err != nil {
			var rp replanned
			if errors.As(err, &rp) {
				return runrequest.TaskResult{}, nil, rp
			}
			return runrequest.TaskResult{}, nil, toolError(err.Error())
		}
	}
	planIssues, stepUpShown := s.plannedIssues(ctx, p, a)
	if len(planIssues) > 1 && s.wholePlans() {
		return s.createWholePlan(ctx, p, args, a, planIssues, stepUpShown)
	}
	var planned plannedTask
	if len(planIssues) == 1 {
		planned = planIssues[0]
	}
	if a.Title != "" {
		d, err := s.checkFinalDraft(ctx, a.Title, a.Text)
		if err != nil {
			return runrequest.TaskResult{}, nil, err
		}
		a.Title, a.Text = d.Title, d.Body
	} else if err := s.redactCheck(ctx, a.Text); err != nil {
		return runrequest.TaskResult{}, nil, err
	}
	if s.Tasks == nil {
		return runrequest.TaskResult{}, nil, toolError("no_factory: the factory is not configured")
	}
	key := a.IdempotencyKey
	if key == "" {
		sum := sha256.Sum256(args)
		key = hex.EncodeToString(sum[:16])
	}
	res, err := s.Tasks.CreateTask(ctx, runrequest.TaskRequest{Repository: a.Repo, Title: a.Title, Text: a.Text,
		DataClass: a.DataClass, MaxCostUSD: a.MaxCost, OnBehalfOf: a.OnBehalfOf, Ref: a.Ref,
		AccessToken: p.AccessToken, IdempotencyKey: p.ID + ":" + key,
		Class: planned.Class, Suggested: planned.suggested(), Profile: planned.Profile, StepUpShown: stepUpShown,
		Outcome: planned.Outcome})
	var refused runrequest.ErrRefused
	if errors.As(err, &refused) {
		return runrequest.TaskResult{}, nil, toolError("rejected: " + refused.Reason)
	}
	if err == nil && planned.Class != "" && !res.Duplicate {
		s.countChoices(ctx, intake.Plan{Issues: []intake.Issue{intake.Issue(planned)}}, intake.SurfaceSlack)
	}
	return res, nil, err
}

func (s *Server) createWholePlan(ctx context.Context, p authn.Principal, rawArgs json.RawMessage, a runCreateArgs,
	issues []plannedTask, stepUpShown bool) (runrequest.TaskResult, []runrequest.TaskResult, error) {
	if s.Tasks == nil {
		return runrequest.TaskResult{}, nil, toolError("no_factory: the factory is not configured")
	}
	type checkedDraft struct {
		title, body string
		index       int
		task        plannedTask
	}
	drafts := make([]checkedDraft, 0, len(issues))
	for i, is := range issues {
		d, err := s.checkFinalDraft(ctx, is.Title, is.Body)
		if err != nil {
			return runrequest.TaskResult{}, nil, err
		}
		idx := is.Index
		if idx == 0 {
			idx = i + 1
		}
		drafts = append(drafts, checkedDraft{title: d.Title, body: d.Body, index: idx, task: is})
	}
	baseKey := a.IdempotencyKey
	if baseKey == "" {
		sum := sha256.Sum256(rawArgs)
		baseKey = hex.EncodeToString(sum[:16])
	}
	filedByIdx := make(map[int]int, len(drafts))
	results := make([]runrequest.TaskResult, 0, len(drafts))
	for i, d := range drafts {
		var deps []int32
		for _, depIdx := range d.task.DependsOn {
			if num := filedByIdx[depIdx]; num > 0 && num <= math.MaxInt32 {
				n32 := int32(num)
				if !slices.Contains(deps, n32) && len(deps) < 4 {
					deps = append(deps, n32)
				}
			}
		}
		body := appendDependsOn(d.body, deps)
		key := baseKey
		if i > 0 {
			key = baseKey + "/" + strconv.Itoa(i+1)
		}
		res, err := s.Tasks.CreateTask(ctx, runrequest.TaskRequest{
			Repository: a.Repo, Title: d.title, Text: body,
			DataClass: a.DataClass, MaxCostUSD: a.MaxCost, OnBehalfOf: a.OnBehalfOf, Ref: a.Ref,
			AccessToken: p.AccessToken, IdempotencyKey: p.ID + ":" + key,
			Class: d.task.Class, Suggested: d.task.suggested(), Profile: d.task.Profile, StepUpShown: stepUpShown,
			Outcome: d.task.Outcome, DependsOn: deps,
		})
		var refused runrequest.ErrRefused
		if errors.As(err, &refused) {
			return runrequest.TaskResult{}, nil, toolError("rejected: " + refused.Reason)
		}
		if err != nil {
			return runrequest.TaskResult{}, nil, err
		}
		if res.Issue > 0 {
			filedByIdx[d.index] = res.Issue
		}
		if d.task.Class != "" && !res.Duplicate {
			s.countChoices(ctx, intake.Plan{Issues: []intake.Issue{intake.Issue(d.task)}}, intake.SurfaceSlack)
		}
		results = append(results, res)
	}
	return results[0], results, nil
}

func appendDependsOn(body string, deps []int32) string {
	if len(deps) == 0 {
		return body
	}
	refs := make([]string, len(deps))
	for i, d := range deps {
		refs[i] = fmt.Sprintf("#%d", d)
	}
	list := strings.Join(refs, ", ")
	return strings.TrimRight(body, "\n") + "\n\nWaits for " + list + "\n\n<!-- maku depends-on: " + list + " -->\n"
}

// fromHandoff turns a run_create {handoff} into the plan-carrying call it stands for (UX2): the id's
// stored plan, re-validated by handoffCheck for the Slack user the call names, becomes the call's plan,
// hash, title and text, so everything below holds it exactly as it holds a plan the App carried. The
// call carries none of those itself — a plan or a body beside the id would be two answers to what is
// filed — and a repository it names must be the stored plan's, as GitHub reads a name. An id that
// expired, is unknown or is another principal's answers handoff_expired, and nothing of the stored plan
// reaches the refusal.
func (s *Server) fromHandoff(ctx context.Context, p authn.Principal, a *runCreateArgs) error {
	switch {
	case len(a.Handoff) > 128 || strings.TrimSpace(a.Handoff) == "":
		return toolError("invalid_arguments: handoff is the id handoff answered")
	case planPresent(a.Plan) || a.Title != "" || a.Text != "":
		return toolError("invalid_arguments: handoff replaces plan, title and text: the broker files the plan it stored")
	case a.OnBehalfOf == "":
		return toolError("invalid_arguments: on_behalf_of is required with a handoff: it is the Slack user who confirmed it")
	case !onBehalfRE.MatchString(a.OnBehalfOf):
		return toolError("invalid_arguments: on_behalf_of is slack:<team>/<user>")
	case a.PlanHash != "" && !hashRE.MatchString(a.PlanHash):
		return toolError("invalid_arguments: plan_hash is the sha256: value the plan was shown with")
	}
	checked, _, err := s.handoffCheckFor(ctx, p, a.Handoff, nil, nil, a.OnBehalfOf)
	if err != nil {
		return err
	}
	if a.PlanHash != "" && a.PlanHash != checked.Hash {
		return toolError(intake.ReasonRefusal(intake.ReasonPlanChanged).Error())
	}
	plan := checked.Plan
	if !s.wholePlans() && len(plan.Issues) != slackFiles {
		return toolError("invalid_arguments: Slack files exactly one issue: the plan carries " + strconv.Itoa(len(plan.Issues)))
	}
	if len(plan.Issues) == 0 {
		return toolError("invalid_arguments: the plan carries no issues")
	}
	if a.Repo != "" && !strings.EqualFold(a.Repo, plan.Repository) {
		return toolError(intake.ReasonRefusal(intake.ReasonPlanChanged).Error())
	}
	raw, err := plan.JSON()
	if err != nil {
		return err
	}
	if a.Repo == "" {
		a.Repo = plan.Repository
	}
	a.Plan, a.PlanHash = raw, checked.Hash
	a.Title, a.Text = plan.Issues[0].Title, plan.Issues[0].Body
	return nil
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

// runCreated is run_create's answer without a task: the factory's, and the room page. When
// wholePlans filed multiple issues, every filed issue is also listed under issues.
func (s *Server) runCreated(r runrequest.TaskResult, all ...runrequest.TaskResult) map[string]any {
	out := map[string]any{"task": r.Task, "room": r.Room, "issue": r.Issue, "duplicate": r.Duplicate, "url": s.PublicURL + "/r/" + r.Room}
	if r.Sponsor != "" {
		out["sponsor"] = r.Sponsor
	}
	if len(all) > 1 {
		items := make([]map[string]any, 0, len(all))
		for _, item := range all {
			items = append(items, map[string]any{
				"task": item.Task, "room": item.Room, "issue": item.Issue, "duplicate": item.Duplicate,
				"url": s.PublicURL + "/r/" + item.Room,
			})
		}
		out["issues"] = items
	}
	return out
}

// plannedIssues are the issues a checked plan files (P5, P7, H2): their class and pin are bound by
// the plan's hash; suggestions are rebuilt from the repository's catalogue first (review B2).
func (s *Server) plannedIssues(ctx context.Context, p authn.Principal, a runCreateArgs) ([]plannedTask, bool) {
	var plan intake.Plan
	if !planPresent(a.Plan) || json.Unmarshal(a.Plan, &plan) != nil || len(plan.Issues) == 0 {
		return nil, false
	}
	if !s.wholePlans() && len(plan.Issues) != slackFiles {
		return nil, false
	}
	plan = intake.Resuggest(plan, s.planClasses(ctx, p, a.OnBehalfOf, plan.Repository))
	out := make([]plannedTask, 0, len(plan.Issues))
	for _, is := range plan.Issues {
		out = append(out, plannedTask(is))
	}
	return out, plan.Budget.StepUpCeilingUSD > 0
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
