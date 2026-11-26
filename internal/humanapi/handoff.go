// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/repoaccess"
	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/store"
)

const (
	handoffAPIVersion = "handoff/v1"
	handoffTTL        = 24 * time.Hour
	noPlannerLine     = "Planning is not available here; file on GitHub."
	errHandoffExpired = toolError("handoff_expired: start a new handoff")
)

const handoffDescription = "Use when the developer wants Maku to do a piece of work. Returns a plan to show as it is and, when something is unclear, one question to ask."

// HandoffStore stores and reads in-progress handoff plans on the broker.
// *store.Store implements it.
type HandoffStore interface {
	PutHandoff(ctx context.Context, h store.Handoff) error
	GetHandoff(ctx context.Context, id string, now time.Time) (store.Handoff, error)
}

type handoffArgs struct {
	Text       string   `json:"text"`
	ID         string   `json:"id"`
	Answer     string   `json:"answer"`
	AcceptRest bool     `json:"acceptRest"`
	Repository string   `json:"repository"`
	Context    string   `json:"context"`
	Candidates []string `json:"candidates"`
	Evidence   []string `json:"evidence"`
	OnBehalfOf string   `json:"on_behalf_of"`
	Ceiling    float64  `json:"ceiling"`
}

func (a handoffArgs) toIntakeArgs() intakeArgs {
	return intakeArgs{
		Text:       a.Text,
		Repository: a.Repository,
		Context:    a.Context,
		Candidates: a.Candidates,
		Evidence:   a.Evidence,
		OnBehalfOf: a.OnBehalfOf,
		Ceiling:    a.Ceiling,
	}
}

func (a handoffArgs) check(p authn.Principal) error {
	if a.ID == "" {
		if a.Answer != "" || a.AcceptRest {
			return toolError("invalid_arguments: answer and acceptRest require id")
		}
		return a.toIntakeArgs().check(p)
	}
	if len(a.ID) > 128 || strings.TrimSpace(a.ID) == "" {
		return toolError("invalid_arguments: id is a handoff identifier")
	}
	if a.Text != "" || a.Repository != "" || a.Context != "" || len(a.Candidates) > 0 || len(a.Evidence) > 0 {
		return toolError("invalid_arguments: a continuation takes id with answer or acceptRest")
	}
	hasAnswer := strings.TrimSpace(a.Answer) != ""
	if hasAnswer == a.AcceptRest {
		return toolError("invalid_arguments: pass either answer or acceptRest: true with id")
	}
	if len(a.Answer) > maxPlanText {
		return toolError("invalid_arguments: answer is at most " + strconv.Itoa(maxPlanText>>10) + " KiB")
	}
	return claims(p, a.OnBehalfOf, a.Ceiling)
}

type handoffQuestion struct {
	Text        string `json:"text"`
	Recommended string `json:"recommended"`
}

type handoffResult struct {
	APIVersion string           `json:"apiVersion"`
	ID         string           `json:"id"`
	Text       string           `json:"text"`
	Question   *handoffQuestion `json:"question"`
	Ready      bool             `json:"ready"`
	Workflow   string           `json:"workflow"`
	Budget     float64          `json:"budget"`
}

// WorkflowChoice describes one workflow offered by the catalogue for a handoff plan.
type WorkflowChoice struct {
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	CeilingUSD       float64  `json:"ceilingUSD,omitempty"`
	StepUpCeilingUSD float64  `json:"stepUpCeilingUSD,omitempty"`
	Path             []string `json:"path,omitempty"`
}

type handoffIssue struct {
	Index     int    `json:"index"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	DependsOn []int  `json:"dependsOn,omitempty"`
}

type handoffDraft struct {
	Title           string           `json:"title"`
	Body            string           `json:"body"`
	Repository      string           `json:"repository"`
	WholePlans      bool             `json:"wholePlans,omitempty"`
	Issues          []handoffIssue   `json:"issues,omitempty"`
	Workflows       []string         `json:"workflows,omitempty"`
	WorkflowChoices []WorkflowChoice `json:"workflowChoices,omitempty"`
	DefaultChoice   *WorkflowChoice  `json:"defaultChoice,omitempty"`
	ActiveChoice    *WorkflowChoice  `json:"activeChoice,omitempty"`
}

type handoffChecked struct {
	planChecked
	Workflows       []string         `json:"workflows,omitempty"`
	WorkflowChoices []WorkflowChoice `json:"workflowChoices,omitempty"`
	DefaultChoice   *WorkflowChoice  `json:"defaultChoice,omitempty"`
	ActiveChoice    *WorkflowChoice  `json:"activeChoice,omitempty"`
	Title           string           `json:"title"`
	Body            string           `json:"body"`
	Repository      string           `json:"repository"`
	WholePlans      bool             `json:"wholePlans,omitempty"`
	Issues          []handoffIssue   `json:"issues,omitempty"`
}

// newHandoffChecked is the one checked view both handoff_check (MCP) and POST
// /api/handoffs/{id}/check (the terminal's maku start) answer with: the draft's workflow choices,
// first issue, wholePlans switch and, under wholePlans, every issue of the plan. Built in one place
// so the two surfaces cannot drift again (the HTTP route once dropped wholePlans and issues, and
// maku start filed only the first issue of a whole plan).
func newHandoffChecked(checked planChecked, draft handoffDraft) handoffChecked {
	return handoffChecked{
		planChecked:     checked,
		Workflows:       draft.Workflows,
		WorkflowChoices: draft.WorkflowChoices,
		DefaultChoice:   draft.DefaultChoice,
		ActiveChoice:    draft.ActiveChoice,
		Title:           draft.Title,
		Body:            draft.Body,
		Repository:      draft.Repository,
		WholePlans:      draft.WholePlans,
		Issues:          draft.Issues,
	}
}

func (s *Server) wholePlans() bool {
	return s.Intake != nil && s.Intake.WholePlans
}

type handoffCheckArgs struct {
	ID         string  `json:"id"`
	Workflow   *string `json:"workflow,omitempty"`
	Outcome    *string `json:"outcome,omitempty"`
	OnBehalfOf string  `json:"on_behalf_of"`
}

var handoffSchema = json.RawMessage(`{"type":"object","additionalProperties":false,` +
	`"properties":{"text":{"type":"string","minLength":1,"maxLength":16384},` +
	`"id":{"type":"string","minLength":1,"maxLength":128},` +
	`"answer":{"type":"string","minLength":1,"maxLength":16384},` +
	`"acceptRest":{"type":"boolean"},` +
	`"repository":{"type":"string","pattern":"^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$"},` +
	`"context":{"type":"string","maxLength":4096},` +
	`"candidates":{"type":"array","maxItems":20,"items":{"type":"string","pattern":"^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$"}},` +
	`"evidence":{"type":"array","maxItems":5,"items":{"type":"string","maxLength":280}},` +
	`"on_behalf_of":{"type":"string","pattern":"^slack:T[A-Z0-9]{2,20}/[UW][A-Z0-9]{2,20}$"},` +
	`"ceiling":{"type":"number","minimum":0}}}`)

func (s *Server) handoffToolEntry() mcpTool {
	return mcpTool{
		name:        "handoff",
		description: handoffDescription,
		schema:      handoffSchema,
		call:        s.handoffTool,
	}
}

func (s *Server) handoffNow() time.Time {
	if s.Intake != nil {
		return s.Intake.now()
	}
	return s.now()
}

func newHandoffID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
	return strings.ToLower(enc), nil
}

func (s *Server) handoffTool(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
	var a handoffArgs
	if mcpwire.DecodeArgs(args, &a) != nil {
		return nil, toolError("invalid_arguments: pass {text} to begin, or {id, answer} / {id, acceptRest: true} to continue")
	}
	if err := a.check(p); err != nil {
		return nil, err
	}
	if s.Intake == nil {
		return nil, toolError(noPlannerLine)
	}
	if s.Handoffs == nil {
		return nil, errors.New("humanapi: no handoff store wired")
	}

	now := s.handoffNow()
	surface := intake.SurfaceSession
	if p.Sponsored {
		surface = intake.SurfaceSlack
	}
	if a.ID == "" {
		ia := a.toIntakeArgs()
		plan, degraded, choices, err := s.executeIntakePlan(ctx, p, ia, planRev)
		if err != nil {
			return nil, err
		}
		if plan, err = s.oneQuestion(ctx, surface, *plan, ia, false, choices); err != nil {
			return nil, err
		}
		id, err := newHandoffID()
		if err != nil {
			return nil, err
		}
		if err := s.saveHandoff(ctx, id, p.ID, ia, *plan, now, now.Add(handoffTTL)); err != nil {
			return nil, err
		}
		return s.handoffAnswer(ctx, p, ia.OnBehalfOf, id, *plan, degraded)
	}

	row, storedArgs, base, err := s.readHandoff(ctx, p, a.ID, a.OnBehalfOf, true, surface)
	if err != nil {
		return nil, err
	}
	if a.Ceiling > 0 {
		storedArgs.Ceiling = a.Ceiling
	}

	if a.AcceptRest {
		var cand *intake.Plan
		if len(base.Gaps) > 0 {
			accepted, err := acceptAllGaps(base)
			if err != nil {
				return nil, s.planCheckRefused(ctx, surface, err)
			}
			cand = &accepted
		}
		finalPlan, err := s.validateStoredPlan(ctx, p, storedArgs.OnBehalfOf, base, row.Hash, cand, false, surface)
		if err != nil {
			return nil, err
		}
		if err := s.saveHandoff(ctx, row.ID, row.Principal, storedArgs, finalPlan, row.CreatedAt, now.Add(handoffTTL)); err != nil {
			return nil, err
		}
		return s.handoffAnswer(ctx, p, storedArgs.OnBehalfOf, row.ID, finalPlan, false)
	}

	cleanAnswer := strings.Join(strings.Fields(a.Answer), " ")
	if strings.Contains(cleanAnswer, "[NEEDS CLARIFICATION:") {
		return nil, toolError("invalid_arguments: answer cannot contain a clarification marker")
	}

	// Every answer is kept in the handoff's context, so each later re-plan of this id sends the
	// planner what the person already settled (#625).
	var revision *intake.Plan
	if len(base.Gaps) > 0 {
		gap := base.Gaps[0]
		storedArgs = appendHandoffContext(storedArgs, answeredLine(gap, cleanAnswer))
		if !intake.IsRepositoryGap(gap) && !s.groundedElsewhere(ctx, p, storedArgs, base, cleanAnswer) {
			cand, err := answerFirstGap(base, cleanAnswer)
			if err != nil {
				return nil, s.planCheckRefused(ctx, surface, err)
			}
			revision = &cand
		}
		// The repository question, or an answer that points at another repository, re-plans from
		// the request: a revision keeps the repository its base was planned for.
	} else {
		storedArgs = appendHandoffContext(storedArgs, "Reply: "+cleanAnswer)
	}
	ia, rev := storedArgs, base.Rev+1
	if revision != nil {
		ia.Plan, ia.Hash, ia.Candidate, rev = &base, row.Hash, revision, revision.Rev
	}
	replanned, degraded, _, err := s.executeIntakePlan(ctx, p, ia, rev)
	if err != nil {
		return nil, err
	}
	// The person has answered once: a re-plan asks nothing more, and assumes what it would re-open.
	final, err := s.oneQuestion(ctx, surface, *replanned, storedArgs, true, nil)
	if err != nil {
		return nil, err
	}
	if err := s.saveHandoff(ctx, row.ID, row.Principal, storedArgs, *final, row.CreatedAt, now.Add(handoffTTL)); err != nil {
		return nil, err
	}
	return s.handoffAnswer(ctx, p, storedArgs.OnBehalfOf, row.ID, *final, degraded)
}

// answeredLine is the context line that carries an answer to gap into every later re-plan. The
// repository question's line leaves out the choices it listed, so the answer alone names one.
func answeredLine(gap intake.Gap, answer string) string {
	q := gap.Text
	if intake.IsRepositoryGap(gap) {
		q = "Which repository should this change go to?"
	}
	return "Answered: " + q + " — " + answer
}

// oneQuestion holds a handoff's plan to at most one question (intake.OneQuestion): the request and
// the answers so far are what a stated done-when is read from.
func (s *Server) oneQuestion(ctx context.Context, surface intake.Surface, plan intake.Plan, a intakeArgs, answered bool, choices []string) (*intake.Plan, error) {
	out, err := intake.OneQuestion(plan, a.Text+"\n"+a.Context, answered, choices)
	if err != nil {
		return nil, s.planCheckRefused(ctx, surface, err)
	}
	return &out, nil
}

// groundedElsewhere is whether an answer names a repository, or quotes a path held only by
// repositories, other than the one base was planned for (#626).
func (s *Server) groundedElsewhere(ctx context.Context, p authn.Principal, a intakeArgs, base intake.Plan, answer string) bool {
	known := s.knownRepositories(ctx, p, a, nil)
	if named, ref := intake.CheckNamed(answer, known, known, known, nil); ref == nil && !slices.Equal(named, known) &&
		!slices.ContainsFunc(named, func(r string) bool { return strings.EqualFold(r, base.Repository) }) {
		return true
	}
	pool := a.asked()
	if len(pool) == 0 {
		served, err := s.planRepositories(ctx, p)
		if err != nil {
			return false
		}
		pool = served
	}
	holding, found := s.groundPaths(ctx, answer, pool)
	return found && !slices.ContainsFunc(holding, func(r string) bool { return strings.EqualFold(r, base.Repository) })
}

// appendHandoffContext adds line to the handoff's context, dropping the oldest lines first when the
// context would pass its bound.
func appendHandoffContext(a intakeArgs, line string) intakeArgs {
	evLen := len(a.evidence())
	budget := maxPlanContext - evLen
	if budget <= 0 {
		return a
	}
	next := line
	if strings.TrimSpace(a.Context) != "" {
		next = a.Context + "\n" + line
	}
	for len(next) > budget && strings.Contains(next, "\n") {
		_, rest, _ := strings.Cut(next, "\n")
		next = rest
	}
	if len(next) > budget {
		cut := 0
		for i := range next {
			if i > budget {
				break
			}
			cut = i
		}
		next = next[:cut]
	}
	a.Context = next
	return a
}

// executeIntakePlan runs intakePlanTool's exact pipeline and extracts the resulting plan.
// choices are the candidates nothing grounded, when more than one is left.
func (s *Server) executeIntakePlan(ctx context.Context, p authn.Principal, a intakeArgs, initialRev int) (*intake.Plan, bool, []string, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, false, nil, err
	}
	out, err := s.intakePlanTool(ctx, p, raw)
	if err != nil {
		return nil, false, nil, err
	}
	vr, ok := out.(viewResult)
	if !ok {
		return nil, false, nil, errors.New("humanapi: unexpected intake_plan result")
	}
	pa, ok := vr.data.(planAnswer)
	if !ok {
		return nil, false, nil, errors.New("humanapi: unexpected intake_plan data")
	}
	plan := pa.Plan
	if a.Candidate == nil && initialRev > planRev {
		plan.Rev = initialRev
	}
	return &plan, pa.Degraded, pa.choices, nil
}

func (s *Server) saveHandoff(ctx context.Context, id, principal string, a intakeArgs, plan intake.Plan, createdAt, expiresAt time.Time) error {
	storedA := a
	storedA.Plan = nil
	storedA.Hash = ""
	storedA.Candidate = nil
	argsRaw, err := json.Marshal(storedA)
	if err != nil {
		return err
	}
	planRaw, err := plan.JSON()
	if err != nil {
		return err
	}
	return s.Handoffs.PutHandoff(ctx, store.Handoff{
		ID:        id,
		Principal: principal,
		Args:      argsRaw,
		Plan:      planRaw,
		Hash:      plan.Hash,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	})
}

func (s *Server) readHandoff(ctx context.Context, p authn.Principal, id, onBehalfOf string, matchAuthor bool, surface intake.Surface) (store.Handoff, intakeArgs, intake.Plan, error) {
	row, err := s.Handoffs.GetHandoff(ctx, id, s.handoffNow())
	if errors.Is(err, store.ErrNoHandoff) {
		return store.Handoff{}, intakeArgs{}, intake.Plan{}, errHandoffExpired
	}
	if err != nil {
		return store.Handoff{}, intakeArgs{}, intake.Plan{}, err
	}
	if row.Principal != p.ID {
		return store.Handoff{}, intakeArgs{}, intake.Plan{}, errHandoffExpired
	}
	var storedArgs intakeArgs
	if err := json.Unmarshal(row.Args, &storedArgs); err != nil {
		return store.Handoff{}, intakeArgs{}, intake.Plan{}, err
	}
	if p.Sponsored && matchAuthor && storedArgs.OnBehalfOf != onBehalfOf {
		return store.Handoff{}, intakeArgs{}, intake.Plan{}, errHandoffExpired
	}
	var base intake.Plan
	if err := mcpwire.DecodeArgs(row.Plan, &base); err != nil {
		return store.Handoff{}, intakeArgs{}, intake.Plan{}, err
	}
	if intake.Hash(base) != row.Hash {
		return store.Handoff{}, intakeArgs{}, intake.Plan{}, s.planCheckRefused(ctx, surface, intake.ReasonRefusal(intake.ReasonPlanChanged))
	}
	base.Hash = row.Hash
	return row, storedArgs, base, nil
}

func (s *Server) handoffWorkflowName(ctx context.Context, p authn.Principal, onBehalfOf string, plan intake.Plan) string {
	if len(plan.Issues) > 0 && plan.Issues[0].Profile != "" {
		return plan.Issues[0].Profile
	}
	if s.Catalogue != nil {
		if cat, err := s.Catalogue.Get(ctx, p, plan.Repository, onBehalfOf); err == nil && cat.DefaultWorkflow != "" {
			return cat.DefaultWorkflow
		}
	}
	if len(plan.Issues) > 0 && plan.Issues[0].Suggestion != nil && plan.Issues[0].Suggestion.Profile != "" {
		return plan.Issues[0].Suggestion.Profile
	}
	return "default"
}

func (s *Server) handoffAnswer(ctx context.Context, p authn.Principal, onBehalfOf, id string, plan intake.Plan, degraded bool) (viewResult, error) {
	wf := s.handoffWorkflowName(ctx, p, onBehalfOf, plan)
	text, err := renderHandoffPlan(plan, wf, degraded, s.handoffNow())
	if err != nil {
		return viewResult{}, err
	}
	text = "Handoff " + id + "\n" + text
	var q *handoffQuestion
	if len(plan.Gaps) > 0 {
		plan = intake.WithGaps(plan, plan.Gaps)
	}
	if plan.Question != nil {
		q = &handoffQuestion{
			Text:        plan.Question.Text,
			Recommended: plan.Question.Recommended.Answer,
		}
	}
	res := handoffResult{
		APIVersion: handoffAPIVersion,
		ID:         id,
		Text:       text,
		Question:   q,
		Ready:      q == nil && len(plan.Gaps) == 0,
		Workflow:   wf,
		Budget:     plan.Budget.CeilingUSD,
	}
	return viewResult{text: text, data: res}, nil
}

func renderHandoffPlan(plan intake.Plan, workflow string, degraded bool, now time.Time) (string, error) {
	shown := plan
	shown.Budget.Source = ""
	shown.Issues = slices.Clone(plan.Issues)
	for i := range shown.Issues {
		shown.Issues[i].Class = ""
		shown.Issues[i].Suggestion = nil
	}
	label := ""
	if degraded {
		label = intake.TemplateLabel
	}
	var b strings.Builder
	if err := roomview.Plan(&b, shown, label, roomview.Options{Format: roomview.Markdown, Now: now}); err != nil {
		return "", err
	}
	out := strings.Replace(b.String(), "\nCeiling ", "\nWorkflow "+workflow+"\nBudget up to ", 1)
	return out, nil
}

func lastDecisionNumber(p intake.Plan) (int, error) {
	last := 0
	for _, is := range p.Issues {
		recs, err := sanitize.DecisionRecords(is.Body)
		if err != nil {
			return 0, err
		}
		for _, r := range recs {
			last = max(last, r.Number)
		}
	}
	return last, nil
}

func appendDecisionLine(body, record string) string {
	if !hasDecisionsHeading(body) {
		switch {
		case strings.HasSuffix(body, "\n\n"):
			body += "## Decisions\n"
		case strings.HasSuffix(body, "\n"):
			body += "\n## Decisions\n"
		default:
			body += "\n\n## Decisions\n"
		}
	} else if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + record
}

func hasDecisionsHeading(body string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if strings.TrimSpace(line) == "## Decisions" {
			return true
		}
	}
	return false
}

func clonePlanForEdit(p intake.Plan) intake.Plan {
	out := p
	out.Issues = slices.Clone(p.Issues)
	for i := range out.Issues {
		out.Issues[i].Roles = slices.Clone(p.Issues[i].Roles)
		out.Issues[i].DependsOn = slices.Clone(p.Issues[i].DependsOn)
	}
	return out
}

func answerFirstGap(base intake.Plan, answer string) (intake.Plan, error) {
	last, err := lastDecisionNumber(base)
	if err != nil {
		return intake.Plan{}, intake.ReasonRefusal(intake.ReasonPlannerInvalid)
	}
	gap := base.Gaps[0]
	cand := clonePlanForEdit(base)
	cand.Rev = base.Rev + 1
	resolved := false
	for i := range cand.Issues {
		markers, err := sanitize.ClarificationMarkers(cand.Issues[i].Body)
		if err != nil {
			return intake.Plan{}, intake.ReasonRefusal(intake.ReasonPlannerInvalid)
		}
		for _, m := range markers {
			if m.ID != gap.ID {
				continue
			}
			body := cand.Issues[i].Body
			body = body[:m.Start] + answer + body[m.End:]
			body = appendDecisionLine(body, fmt.Sprintf("D%d [%s]: %s", last+1, gap.ID, answer))
			cand.Issues[i].Body = body
			resolved = true
			break
		}
		if resolved {
			break
		}
	}
	if !resolved {
		return intake.Plan{}, intake.ReasonRefusal(intake.ReasonPlannerInvalid)
	}
	return cand, nil
}

func acceptAllGaps(base intake.Plan) (intake.Plan, error) {
	last, err := lastDecisionNumber(base)
	if err != nil {
		return intake.Plan{}, intake.ReasonRefusal(intake.ReasonPlannerInvalid)
	}
	gapsByID := make(map[string]intake.Gap, len(base.Gaps))
	for _, g := range base.Gaps {
		gapsByID[g.ID] = g
	}
	cand := clonePlanForEdit(base)
	cand.Rev = base.Rev + 1
	for i := range cand.Issues {
		markers, err := sanitize.ClarificationMarkers(cand.Issues[i].Body)
		if err != nil {
			return intake.Plan{}, intake.ReasonRefusal(intake.ReasonPlannerInvalid)
		}
		type pending struct {
			marker sanitize.ClarificationMarker
			answer string
			record string
		}
		added := make([]pending, 0, len(markers))
		for _, m := range markers {
			g, ok := gapsByID[m.ID]
			if !ok || strings.TrimSpace(g.Recommended.Answer) == "" {
				return intake.Plan{}, intake.ReasonRefusal(intake.ReasonPlannerInvalid)
			}
			last++
			text := "assumed " + g.Recommended.Answer
			if strings.TrimSpace(g.Recommended.Why) != "" {
				text += ", because " + g.Recommended.Why
			}
			added = append(added, pending{
				marker: m,
				answer: g.Recommended.Answer,
				record: fmt.Sprintf("A%d [%s]: %s", last, m.ID, text),
			})
		}
		body := cand.Issues[i].Body
		for j := len(added) - 1; j >= 0; j-- {
			m := added[j].marker
			body = body[:m.Start] + added[j].answer + body[m.End:]
		}
		for _, a := range added {
			body = appendDecisionLine(body, a.record)
		}
		cand.Issues[i].Body = body
	}
	return cand, nil
}

func (s *Server) validateStoredPlan(ctx context.Context, p authn.Principal, onBehalfOf string, base intake.Plan, baseHash string, candidate *intake.Plan, profileRev bool, surface intake.Surface) (intake.Plan, error) {
	plan := base
	plan.Hash = baseHash
	if candidate != nil {
		revised, err := intake.Revise(base, baseHash, *candidate, s.Intake.MaxRounds)
		if err != nil {
			return intake.Plan{}, s.planCheckRefused(ctx, surface, err)
		}
		plan = revised
	}
	_, candidates, ref := s.requester(ctx, p, intakeArgs{
		Repository: plan.Repository, Candidates: alternativeRepos(plan), OnBehalfOf: onBehalfOf,
	})
	if ref != nil {
		return intake.Plan{}, s.planCheckRefused(ctx, surface, ref)
	}
	classes := s.planClasses(ctx, p, onBehalfOf, plan.Repository)
	if profileRev {
		plan = intake.MaterialRoles(plan, classes)
	}
	if ref := intake.CheckFinal(plan, candidates, classes); ref != nil {
		return intake.Plan{}, s.planCheckRefused(ctx, surface, ref)
	}
	if ref := s.checkPins(ctx, p, onBehalfOf, plan, surface); ref != nil {
		return intake.Plan{}, s.planCheckRefused(ctx, surface, ref)
	}
	plan = intake.Resuggest(plan, classes)
	plan = intake.RecordProfiles(plan)
	if err := s.checkPlanDrafts(ctx, plan); err != nil {
		return intake.Plan{}, s.planCheckRefused(ctx, surface, err)
	}
	return plan, nil
}

// handoffCheck validates a stored handoff plan (optionally pinning workflow) and returns the
// checked plan and its first issue draft.
func (s *Server) handoffCheck(ctx context.Context, p authn.Principal, id, workflow string) (planChecked, handoffDraft, error) {
	var wf *string
	if workflow != "" {
		wf = &workflow
	}
	return s.handoffCheckFor(ctx, p, id, wf, nil, "")
}

func (s *Server) handoffCheckTool(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
	var a handoffCheckArgs
	if mcpwire.DecodeArgs(args, &a) != nil || strings.TrimSpace(a.ID) == "" || len(a.ID) > 128 {
		return nil, toolError("invalid_arguments: id is required, and no field outside {id, workflow, outcome, on_behalf_of} is taken")
	}
	if err := claims(p, a.OnBehalfOf, 0); err != nil {
		return nil, err
	}
	var (
		checked planChecked
		draft   handoffDraft
		err     error
	)
	if a.OnBehalfOf == "" && a.Outcome == nil && (a.Workflow == nil || *a.Workflow != "") {
		wf := ""
		if a.Workflow != nil {
			wf = *a.Workflow
		}
		checked, draft, err = s.handoffCheck(ctx, p, a.ID, wf)
	} else {
		checked, draft, err = s.handoffCheckFor(ctx, p, a.ID, a.Workflow, a.Outcome, a.OnBehalfOf)
	}
	if err != nil {
		return nil, err
	}
	wf := s.handoffWorkflowName(ctx, p, a.OnBehalfOf, checked.Plan)
	text, err := renderHandoffPlan(checked.Plan, wf, false, s.handoffNow())
	if err != nil {
		return nil, err
	}
	return viewResult{
		text: text,
		data: newHandoffChecked(checked, draft),
	}, nil
}

func (s *Server) handoffCheckFor(ctx context.Context, p authn.Principal, id string, workflow, outcome *string, onBehalfOf string) (planChecked, handoffDraft, error) {
	if s.Intake == nil {
		return planChecked{}, handoffDraft{}, toolError(noPlannerLine)
	}
	if s.Handoffs == nil {
		return planChecked{}, handoffDraft{}, errors.New("humanapi: no handoff store wired")
	}
	surface := intake.SurfaceSession
	if p.Sponsored {
		surface = intake.SurfaceSlack
	}
	row, storedArgs, base, err := s.readHandoff(ctx, p, id, onBehalfOf, false, surface)
	if err != nil {
		return planChecked{}, handoffDraft{}, err
	}

	var workflows []string
	var workflowChoices []WorkflowChoice
	var defaultChoice, activeChoice *WorkflowChoice
	var maintainer *bool
	defaultNotOffered := ""
	var catProfiles map[string]catalogue.Profile
	var catDefault string
	if s.Catalogue != nil {
		if cat, err := s.Catalogue.Get(ctx, p, base.Repository, onBehalfOf); err == nil {
			m := cat.Maintainer
			maintainer = &m
			catProfiles = cat.Profiles
			catDefault = cat.DefaultWorkflow
			if cat.DefaultWorkflow != "" && !cat.Profiles[cat.DefaultWorkflow].Offered {
				defaultNotOffered = cat.DefaultWorkflow
			}
			choiceOf := func(name string) WorkflowChoice {
				pr := cat.Profiles[name]
				return WorkflowChoice{
					Name:             name,
					Description:      pr.Description,
					CeilingUSD:       pr.CeilingUSD,
					StepUpCeilingUSD: pr.StepUpCeilingUSD,
					Path:             slices.Clone(pr.Path),
				}
			}
			if cat.DefaultWorkflow != "" {
				dc := choiceOf(cat.DefaultWorkflow)
				defaultChoice = &dc
			}
			if cat.Maintainer {
				offered := cat.Workflows()
				if len(offered) > 0 {
					if cat.DefaultWorkflow != "" && !slices.Contains(offered, cat.DefaultWorkflow) {
						workflows = append([]string{cat.DefaultWorkflow}, offered...)
					} else {
						workflows = offered
					}
					for _, name := range workflows {
						workflowChoices = append(workflowChoices, choiceOf(name))
					}
				}
			}
		}
	}

	var cand *intake.Plan
	profileRev := false
	if workflow != nil && len(base.Issues) > 0 {
		want := *workflow
		if want != "" && want == defaultNotOffered {
			want = ""
		}
		if base.Issues[0].Profile != want {
			edited := clonePlanForEdit(base)
			for i := range edited.Issues {
				edited.Issues[i].Profile = want
			}
			cand = &edited
			profileRev = true
		}
	}

	plan, err := s.validateStoredPlan(ctx, p, onBehalfOf, base, row.Hash, cand, profileRev, surface)
	if err != nil {
		return planChecked{}, handoffDraft{}, err
	}
	if outcome != nil && len(plan.Issues) > 0 {
		want := intake.NormalizeOutcome(strings.TrimSpace(*outcome))
		if !intake.ValidOutcome(want) {
			return planChecked{}, handoffDraft{}, toolError("invalid_arguments: outcome is change, plan-first, review or report")
		}
		if intake.NormalizeOutcome(plan.Issues[0].Outcome) != want {
			edited := clonePlanForEdit(plan)
			for i := range edited.Issues {
				edited.Issues[i].Outcome = want
			}
			plan, err = s.validateStoredPlan(ctx, p, onBehalfOf, plan, plan.Hash, &edited, false, surface)
			if err != nil {
				return planChecked{}, handoffDraft{}, err
			}
		}
	}
	if len(catProfiles) > 0 {
		activeName := catDefault
		if len(plan.Issues) > 0 && plan.Issues[0].Profile != "" {
			activeName = plan.Issues[0].Profile
		}
		if pr, ok := catProfiles[activeName]; ok {
			plan.Budget.StepUpCeilingUSD = roomview.PlanStepUpUSD(plan.Budget.CeilingUSD, pr.CeilingUSD, pr.StepUpCeilingUSD, len(plan.Issues))
			ac := WorkflowChoice{
				Name:             activeName,
				Description:      pr.Description,
				CeilingUSD:       pr.CeilingUSD,
				StepUpCeilingUSD: pr.StepUpCeilingUSD,
				Path:             slices.Clone(pr.Path),
			}
			activeChoice = &ac
		}
	}
	if err := s.saveHandoff(ctx, row.ID, row.Principal, storedArgs, plan, row.CreatedAt, row.ExpiresAt); err != nil {
		return planChecked{}, handoffDraft{}, err
	}

	canStart, why := false, startSponsored
	if !p.Sponsored {
		canStart, why = s.canStart(ctx, p, plan.Repository)
	}
	checked := planChecked{
		Plan:        plan,
		Hash:        plan.Hash,
		CanStart:    canStart,
		CanStartWhy: why,
		Maintainer:  maintainer,
	}
	if s.choices.first(plan.Hash) {
		s.countChoices(ctx, plan, surface)
	}
	draft := handoffDraft{
		Repository:      plan.Repository,
		WholePlans:      s.wholePlans(),
		Workflows:       workflows,
		WorkflowChoices: workflowChoices,
		DefaultChoice:   defaultChoice,
		ActiveChoice:    activeChoice,
	}
	if len(plan.Issues) > 0 {
		draft.Title = plan.Issues[0].Title
		draft.Body = plan.Issues[0].Body
	}
	if draft.WholePlans && len(plan.Issues) > 0 {
		draft.Issues = make([]handoffIssue, 0, len(plan.Issues))
		for i, is := range plan.Issues {
			idx := is.Index
			if idx == 0 {
				idx = i + 1
			}
			draft.Issues = append(draft.Issues, handoffIssue{
				Index:     idx,
				Title:     is.Title,
				Body:      is.Body,
				DependsOn: slices.Clone(is.DependsOn),
			})
		}
	}
	return checked, draft, nil
}

// StartIssueRequest is the issue POST /api/handoffs/{id}/start files on GitHub for a linked viewer.
type StartIssueRequest struct {
	Repository string
	Title      string
	Body       string
	Labels     []string
	Viewer     string
	Principal  string
}

// StartIssueResult is the filed GitHub issue's number and URL.
type StartIssueResult struct {
	Number int    `json:"issue"`
	URL    string `json:"url"`
}

// IssueStarter files a handoff's issue on GitHub and applies its labels for a linked viewer.
type IssueStarter interface {
	StartIssue(ctx context.Context, req StartIssueRequest) (StartIssueResult, error)
}

// HandoffStartRecorder records a web start, so the factory can confirm the App's factory/ready on the
// issue it filed (broker-confirmed handoff). *store.Store implements it.
type HandoffStartRecorder interface {
	RecordHandoffStart(ctx context.Context, h store.HandoffStart) error
}

type handoffCreateResponse struct {
	handoffResult
	Repository string `json:"repository,omitempty"`
	Title      string `json:"title,omitempty"`
	Body       string `json:"body,omitempty"`
}

type handoffCheckResponse struct {
	handoffChecked
	ID       string `json:"id"`
	Workflow string `json:"workflow"`
	Text     string `json:"text"`
	Linked   bool   `json:"linked"`
}

type handoffStartResponse struct {
	Repository string   `json:"repository"`
	Issue      int      `json:"issue"`
	URL        string   `json:"url"`
	Labels     []string `json:"labels"`
	Viewer     string   `json:"viewer"`
}

// writeHandoffRefusal writes a JSON refusal {"error": code, "message": message} for the HTTP handoff routes.
func writeHandoffRefusal(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error   string `json:"error"`
		Message string `json:"message,omitempty"`
	}{Error: code, Message: message})
}

func (s *Server) writeHandoffError(w http.ResponseWriter, err error) {
	if errors.Is(err, errHandoffExpired) {
		writeHandoffRefusal(w, http.StatusNotFound, "handoff_expired", err.Error())
		return
	}
	if ref, ok := asRefusal(err); ok {
		status := http.StatusBadRequest
		switch ref.Reason {
		case intake.ReasonNotLinked, intake.ReasonNoWrite, intake.ReasonRepositoryNotAllowed,
			intake.NotMaintainer, intake.ReasonPrivateRepository:
			status = http.StatusForbidden
		case intake.ReasonPlanChanged:
			status = http.StatusConflict
		case intake.ReasonDailyPlansReached:
			status = http.StatusTooManyRequests
		case intake.ReasonPlannerTimeout, intake.ReasonPlannerUnavailable,
			intake.ReasonRepositoryUnverified, intake.CatalogueUnavailable:
			status = http.StatusServiceUnavailable
		}
		writeHandoffRefusal(w, status, ref.Reason, ref.Sentence)
		return
	}
	var te toolError
	if errors.As(err, &te) {
		msg := te.Error()
		if msg == noPlannerLine {
			writeHandoffRefusal(w, http.StatusServiceUnavailable, "planner_unavailable", msg)
			return
		}
		code := "invalid_arguments"
		if prefix, _, ok := strings.Cut(msg, ":"); ok && prefix != "" && !strings.ContainsAny(prefix, " \t\n") {
			code = prefix
		}
		writeHandoffRefusal(w, http.StatusBadRequest, code, msg)
		return
	}
	s.log().Warn("handoff route failed", "err", err)
	writeHandoffRefusal(w, http.StatusInternalServerError, "unavailable", "Planning is unavailable right now.")
}

func (s *Server) viewerGitHubLogin(ctx context.Context, p authn.Principal, repository string) (string, error) {
	if s.Identity == nil {
		return "", nil
	}
	lctx, cancel := context.WithTimeout(ctx, accessWait)
	defer cancel()
	login, err := s.Identity.Login(lctx, p.Sub, repository)
	if errors.Is(err, repoaccess.ErrNoRepository) {
		return "", nil
	}
	return login, err
}

// handoffCreateRoute is POST /api/handoffs: member-authenticated planning over the handoff engine.
func (s *Server) handoffCreateRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	if !s.mcpAllow(p.ID) {
		s.rejected(r.Context(), rejectRateLimited)
		writeHandoffRefusal(w, http.StatusTooManyRequests, rejectRateLimited, "rate_limited: 10 requests a second, burst 20")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpBody))
	if err != nil {
		writeHandoffRefusal(w, http.StatusBadRequest, "invalid_arguments", "invalid_arguments: request body is unreadable or too large")
		return
	}
	ctx, cancel := mcpContext(r)
	defer cancel()
	out, err := s.handoffTool(ctx, p, raw)
	if err != nil {
		s.writeHandoffError(w, err)
		return
	}
	vr, ok := out.(viewResult)
	if !ok {
		s.writeHandoffError(w, errors.New("humanapi: unexpected handoff result"))
		return
	}
	hr, ok := vr.data.(handoffResult)
	if !ok {
		s.writeHandoffError(w, errors.New("humanapi: unexpected handoff data"))
		return
	}
	resp := handoffCreateResponse{handoffResult: hr}
	if s.Handoffs != nil {
		if row, gerr := s.Handoffs.GetHandoff(ctx, hr.ID, s.handoffNow()); gerr == nil {
			var pl intake.Plan
			if json.Unmarshal(row.Plan, &pl) == nil {
				resp.Repository = pl.Repository
				if len(pl.Issues) > 0 {
					resp.Title = pl.Issues[0].Title
					resp.Body = pl.Issues[0].Body
				}
			}
		}
	}
	writeJSON(w, resp)
}

// handoffCheckRoute is POST /api/handoffs/{id}/check: re-validates a stored handoff with optional
// workflow pin and outcome (plan-first / change).
func (s *Server) handoffCheckRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	if !s.mcpAllow(p.ID) {
		s.rejected(r.Context(), rejectRateLimited)
		writeHandoffRefusal(w, http.StatusTooManyRequests, rejectRateLimited, "rate_limited: 10 requests a second, burst 20")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || len(id) > 128 {
		writeHandoffRefusal(w, http.StatusBadRequest, "invalid_arguments", "invalid_arguments: handoff id is required")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		writeHandoffRefusal(w, http.StatusBadRequest, "invalid_arguments", "invalid_arguments: request body is unreadable or too large")
		return
	}
	var in struct {
		Workflow *string `json:"workflow,omitempty"`
		Outcome  *string `json:"outcome,omitempty"`
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if mcpwire.DecodeArgs(raw, &in) != nil {
			writeHandoffRefusal(w, http.StatusBadRequest, "invalid_arguments", "invalid_arguments: only {workflow, outcome} are taken")
			return
		}
	}
	ctx, cancel := mcpContext(r)
	defer cancel()
	checked, draft, err := s.handoffCheckFor(ctx, p, id, in.Workflow, in.Outcome, "")
	if err != nil {
		s.writeHandoffError(w, err)
		return
	}
	wf := s.handoffWorkflowName(ctx, p, "", checked.Plan)
	text, err := renderHandoffPlan(checked.Plan, wf, false, s.handoffNow())
	if err != nil {
		s.writeHandoffError(w, err)
		return
	}
	linked := checked.CanStart
	if !linked && checked.CanStartWhy != startNotLinked {
		if login, lerr := s.viewerGitHubLogin(ctx, p, draft.Repository); lerr == nil && login != "" {
			linked = true
		}
	}
	checked = webStartable(checked)
	writeJSON(w, handoffCheckResponse{
		handoffChecked: newHandoffChecked(checked, draft),
		ID:             id,
		Workflow:       wf,
		Text:           text,
		Linked:         linked,
	})
}

// notMaintainerStart is the web start's refusal for a caller who maintains no repository it starts on.
const notMaintainerStart = "Only a maintainer of this repository can start a task. File the issue on GitHub and ask a maintainer to label it factory/ready."

// webStartable holds the web Start to the factory's own rule: the factory starts a web start's task
// only for a maintainer (broker-confirmed handoff), so a caller the catalogue names as no maintainer
// is offered no Start, rather than an issue the factory publicly refuses. An unread catalogue (nil)
// leaves the factory to decide.
func webStartable(c planChecked) planChecked {
	if c.CanStart && c.Maintainer != nil && !*c.Maintainer {
		c.CanStart, c.CanStartWhy = false, startNotMaintainer
	}
	return c
}

// handoffStartRoute is POST /api/handoffs/{id}/start: files the checked handoff's issue on GitHub
// through the factory App for the viewer's linked GitHub identity, applies its workflow, outcome and
// factory/ready labels, and records the start, which the factory's intake confirms the App's label
// against (broker-confirmed handoff).
func (s *Server) handoffStartRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	if !s.mcpAllow(p.ID) {
		s.rejected(r.Context(), rejectRateLimited)
		writeHandoffRefusal(w, http.StatusTooManyRequests, rejectRateLimited, "rate_limited: 10 requests a second, burst 20")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || len(id) > 128 {
		writeHandoffRefusal(w, http.StatusBadRequest, "invalid_arguments", "invalid_arguments: handoff id is required")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		writeHandoffRefusal(w, http.StatusBadRequest, "invalid_arguments", "invalid_arguments: request body is unreadable or too large")
		return
	}
	var in struct {
		Hash     string  `json:"hash,omitempty"`
		Workflow *string `json:"workflow,omitempty"`
		Outcome  *string `json:"outcome,omitempty"`
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if mcpwire.DecodeArgs(raw, &in) != nil {
			writeHandoffRefusal(w, http.StatusBadRequest, "invalid_arguments", "invalid_arguments: only {hash, workflow, outcome} are taken")
			return
		}
	}
	ctx, cancel := mcpContext(r)
	defer cancel()
	checked, draft, err := s.handoffCheckFor(ctx, p, id, in.Workflow, in.Outcome, "")
	if err != nil {
		s.writeHandoffError(w, err)
		return
	}
	if in.Hash != "" && in.Hash != checked.Hash {
		ref := intake.ReasonRefusal(intake.ReasonPlanChanged)
		writeHandoffRefusal(w, http.StatusConflict, ref.Reason, ref.Sentence)
		return
	}
	if len(checked.Plan.Issues) == 0 {
		writeHandoffRefusal(w, http.StatusBadRequest, "invalid_plan", "the plan has no issue to file")
		return
	}
	checked = webStartable(checked)
	login, lerr := s.viewerGitHubLogin(ctx, p, draft.Repository)
	if lerr != nil {
		writeHandoffRefusal(w, http.StatusServiceUnavailable, startUnverified, "GitHub access could not be verified right now.")
		return
	}
	if login == "" {
		writeHandoffRefusal(w, http.StatusForbidden, startNotLinked, "Link your GitHub account before starting a task.")
		return
	}
	if !checked.CanStart {
		switch checked.CanStartWhy {
		case startNotLinked:
			writeHandoffRefusal(w, http.StatusForbidden, startNotLinked, "Link your GitHub account before starting a task.")
		case startNoWrite:
			writeHandoffRefusal(w, http.StatusForbidden, startNoWrite, "You do not have write access to this repository on GitHub.")
		case startNotOffered:
			writeHandoffRefusal(w, http.StatusForbidden, startNotOffered, "Starting tasks from here is not offered on this repository.")
		case startAccessUnwired, startUnverified:
			writeHandoffRefusal(w, http.StatusServiceUnavailable, checked.CanStartWhy, "GitHub access could not be verified right now.")
		case startNotMaintainer:
			writeHandoffRefusal(w, http.StatusForbidden, startNotMaintainer, notMaintainerStart)
		default:
			writeHandoffRefusal(w, http.StatusForbidden, checked.CanStartWhy, "Starting tasks from here is not allowed.")
		}
		return
	}
	d, err := s.checkFinalDraft(ctx, draft.Title, draft.Body)
	if err != nil {
		s.writeHandoffError(w, err)
		return
	}
	if s.IssueStarter == nil || s.HandoffStarts == nil {
		writeHandoffRefusal(w, http.StatusServiceUnavailable, "start_unavailable", "Starting tasks from the web UI is not configured on this broker.")
		return
	}
	is := checked.Plan.Issues[0]
	var labels []string
	if is.Profile != "" {
		labels = append(labels, "workflow:"+is.Profile)
	}
	if is.Outcome == intake.OutcomePlanFirst || is.Outcome == intake.OutcomeReport {
		labels = append(labels, is.Outcome)
	}
	labels = append(labels, "factory/ready")
	filed, err := s.IssueStarter.StartIssue(ctx, StartIssueRequest{
		Repository: draft.Repository,
		Title:      d.Title,
		Body:       d.Body,
		Labels:     labels,
		Viewer:     login,
		Principal:  p.ID,
	})
	if err != nil {
		s.log().Warn("handoff start failed", "handoff", id, "repository", draft.Repository, "err", err)
		writeHandoffRefusal(w, http.StatusServiceUnavailable, "start_failed", "Could not file the issue on GitHub right now.")
		return
	}
	// The App applied factory/ready, so the factory's intake starts the task only against this record:
	// who the broker filed the issue for, never a marker in the issue (broker-confirmed handoff).
	now := s.handoffNow()
	if err := s.HandoffStarts.RecordHandoffStart(ctx, store.HandoffStart{Repository: draft.Repository, Issue: filed.Number,
		Handoff: id, Principal: p.ID, Sub: p.Sub, Login: login, Labels: labels, StartedAt: now,
		ExpiresAt: now.Add(store.HandoffStartTTL)}); err != nil {
		s.log().Error("handoff start not recorded", "handoff", id, "repository", draft.Repository, "issue", filed.Number, "err", err)
		writeHandoffRefusal(w, http.StatusServiceUnavailable, "start_unrecorded",
			"The issue was filed at "+filed.URL+", but its start could not be recorded: a maintainer can label it factory/ready.")
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, handoffStartResponse{
		Repository: draft.Repository,
		Issue:      filed.Number,
		URL:        filed.URL,
		Labels:     labels,
		Viewer:     login,
	})
}
