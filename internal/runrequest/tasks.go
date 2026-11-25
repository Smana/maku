// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/httpx"
)

// TaskRequest is one api task to create: the factory's POST /v1/tasks fields, the caller's own
// access token (C4) and the key that names the act. Neither of the last two is in the body.
type TaskRequest struct {
	Repository, Title, Text, DataClass, OnBehalfOf, Ref string
	MaxCostUSD                                          float64
	AccessToken, IdempotencyKey                         string
	// Class and Suggested are a planned task's class and suggested profile (P7), and Profile the
	// profile its plan pinned (P5); empty otherwise.
	Class, Suggested, Profile string
	// StepUpShown is true when the confirmed plan showed the "if it steps up" cost figure (WR4).
	StepUpShown bool
	// Outcome is the task outcome the confirmed plan carried (phase F); empty is change.
	Outcome string
	// DependsOn lists earlier issue numbers in the same repository that must merge first (phase H2).
	DependsOn []int32
}

// TaskResult is the factory's answer: the task and its room (the same id), the issue filed (0
// when none), whether the key had already created it, and the GitHub login that sponsors it ("" on
// a duplicate, or when the factory's answer is not a login).
type TaskResult struct {
	Task, Room string
	Issue      int
	Duplicate  bool
	Sponsor    string
}

// sponsorLogin is GitHub's login syntax: the only shape of sponsor passed on.
var sponsorLogin = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

// ErrRefused is the factory refusing a task request: its status and its code, "refused" for a
// code outside POST /v1/tasks's closed list. A value, so errors.As matches it as written.
type ErrRefused struct {
	Status int
	Reason string
}

func (e ErrRefused) Error() string {
	return fmt.Sprintf("runrequest: factory refused (%d): %s", e.Status, e.Reason)
}

// refusalCodes is POST /v1/tasks's closed list of refusals the caller may read; anything else is
// clipped to "refused", so a factory's free text never reaches a model.
var refusalCodes = []string{"kill_switch", "bad_json", "idempotency_key_required", "bad_data_class", "bad_ref",
	"repository_not_allowed", "repository_not_owned", "meter_unpriced", "max_cost_required", "max_cost_over_principal",
	"max_cost_over_class", "unauthenticated", "not_permitted", "not_linked", "link_unverified", "access_unverified", "not_a_maintainer", "sponsored_only", "invalid_draft", "invalid_decisions", "bad_text", "tasks_unavailable",
	"principal_task_cap", "daily_task_cap", "ledger_unavailable", "principal_budget_exhausted", "intake_failed", "idempotency_conflict",
	"human_only"}

var actResults = []string{
	"stopped", "retried", "stepped_up", "cancelled", "discarded",
	"answered_restarted", "answered",
	"split_accepted", "split_declined", "no_split",
	"plan_approved", "plan_revised", "plan_split", "no_plan",
	"not_linked", "not_maintainer", "not_allowed",
	"not_running", "not_escalated", "no_question", "unknown_task",
	"no_deeper_workflow", "deeper_over_cap",
}

// RetryResult is the factory's answer to POST /v1/tasks/{task}/retries (WR4).
type RetryResult struct {
	Result          string `json:"result"`
	Workflow        string `json:"workflow,omitempty"`
	CeilingMicroUSD int64  `json:"ceilingMicroUSD,omitempty"`
}

// StopTask posts onBehalfOf to the factory's POST /v1/tasks/{task}/stops with accessToken.
func (f Factory) StopTask(ctx context.Context, task, onBehalfOf, accessToken string) (string, error) {
	return f.actTask(ctx, "stops", task, onBehalfOf, "", accessToken)
}

// RetryTask posts onBehalfOf to the factory's POST /v1/tasks/{task}/retries with accessToken.
func (f Factory) RetryTask(ctx context.Context, task, onBehalfOf, accessToken string) (string, error) {
	res, err := f.RetryTaskDeeper(ctx, task, onBehalfOf, false, accessToken)
	return res.Result, err
}

// RetryTaskDeeper posts a sponsored retry (optionally deeper) to POST /v1/tasks/{task}/retries.
func (f Factory) RetryTaskDeeper(ctx context.Context, task, onBehalfOf string, deeper bool, accessToken string) (RetryResult, error) {
	return f.postRetry(ctx, task, struct {
		OnBehalfOf string `json:"onBehalfOf"`
		Deeper     bool   `json:"deeper,omitempty"`
	}{OnBehalfOf: onBehalfOf, Deeper: deeper}, accessToken)
}

// RetryTaskHuman posts a human's own retry (optionally deeper) to POST /v1/tasks/{task}/retries.
func (f Factory) RetryTaskHuman(ctx context.Context, task string, deeper bool, accessToken string) (RetryResult, error) {
	return f.postRetry(ctx, task, struct {
		Deeper bool `json:"deeper,omitempty"`
	}{Deeper: deeper}, accessToken)
}

// AnswerTask posts onBehalfOf and text to the factory's POST /v1/tasks/{task}/answers with accessToken.
func (f Factory) AnswerTask(ctx context.Context, task, onBehalfOf, text, accessToken string) (string, error) {
	return f.actTask(ctx, "answers", task, onBehalfOf, text, accessToken)
}

// AnswerTaskHuman posts a human's own answer to the factory's POST /v1/tasks/{task}/answers with accessToken.
func (f Factory) AnswerTaskHuman(ctx context.Context, task, text, accessToken string) (string, error) {
	return f.postAct(ctx, "answers", task, struct {
		Text string `json:"text"`
	}{Text: text}, accessToken)
}

// SplitTaskHuman posts a human's split decision (accept or decline) to POST /v1/tasks/{task}/splits.
func (f Factory) SplitTaskHuman(ctx context.Context, task, decision, accessToken string) (string, error) {
	return f.postAct(ctx, "splits", task, struct {
		Decision string `json:"decision"`
	}{Decision: decision}, accessToken)
}

// PlanTaskHuman posts a human's plan decision (approve, revise with note, or split) to POST /v1/tasks/{task}/plans.
func (f Factory) PlanTaskHuman(ctx context.Context, task, decision, note, accessToken string) (string, error) {
	return f.postAct(ctx, "plans", task, struct {
		Decision string `json:"decision"`
		Note     string `json:"note,omitempty"`
	}{Decision: decision, Note: note}, accessToken)
}

// CancelTask posts the caller's own cancel, or with discard their discard, to the factory's POST
// /v1/tasks/{task}/cancellations with accessToken (#644).
func (f Factory) CancelTask(ctx context.Context, task string, discard bool, accessToken string) (string, error) {
	return f.postAct(ctx, "cancellations", task, struct {
		Discard bool `json:"discard"`
	}{discard}, accessToken)
}

func (f Factory) actTask(ctx context.Context, subpath, task, onBehalfOf, text, accessToken string) (string, error) {
	return f.postAct(ctx, subpath, task, struct {
		OnBehalfOf string `json:"onBehalfOf"`
		Text       string `json:"text,omitempty"`
	}{OnBehalfOf: onBehalfOf, Text: text}, accessToken)
}

func (f Factory) postAct(ctx context.Context, subpath, task string, in any, accessToken string) (string, error) {
	res, err := f.postActResult(ctx, subpath, task, in, accessToken)
	return res.Result, err
}

func (f Factory) postRetry(ctx context.Context, task string, in any, accessToken string) (RetryResult, error) {
	return f.postActResult(ctx, "retries", task, in, accessToken)
}

func (f Factory) postActResult(ctx context.Context, subpath, task string, in any, accessToken string) (RetryResult, error) {
	if accessToken == "" {
		return RetryResult{}, ErrForbidden
	}
	if !envelope.ValidID(task) {
		return RetryResult{}, fmt.Errorf("runrequest: invalid task id")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return RetryResult{}, fmt.Errorf("runrequest: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL+"/v1/tasks/"+task+"/"+subpath, bytes.NewReader(body))
	if err != nil {
		return RetryResult{}, fmt.Errorf("runrequest: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	hc, err := f.client()
	if err != nil {
		return RetryResult{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return RetryResult{}, fmt.Errorf("runrequest: factory: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxResponse)
	if err != nil {
		return RetryResult{}, fmt.Errorf("runrequest: factory: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		var out RetryResult
		if err := json.Unmarshal(raw, &out); err != nil {
			return RetryResult{}, fmt.Errorf("runrequest: factory answer: %w", err)
		}
		if !slices.Contains(actResults, out.Result) {
			return RetryResult{}, fmt.Errorf("runrequest: factory answer: unknown result %q", out.Result)
		}
		return out, nil
	}
	if resp.StatusCode >= 400 {
		var out struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &out)
		reason := "refused"
		if slices.Contains(refusalCodes, out.Error) {
			reason = out.Error
		}
		return RetryResult{}, ErrRefused{Status: resp.StatusCode, Reason: reason}
	}
	return RetryResult{}, fmt.Errorf("runrequest: factory: %s", resp.Status)
}

// CreateTask posts r to the factory's POST /v1/tasks with the caller's own token, as Request does.
func (f Factory) CreateTask(ctx context.Context, r TaskRequest) (TaskResult, error) {
	if r.AccessToken == "" { // never an unauthenticated or asserted request (C4)
		return TaskResult{}, ErrForbidden
	}
	if r.IdempotencyKey == "" {
		return TaskResult{}, ErrNoKey
	}
	body, err := json.Marshal(struct {
		Repository  string  `json:"repository"`
		Title       string  `json:"title,omitempty"`
		Text        string  `json:"text"`
		DataClass   string  `json:"dataClass"`
		MaxCostUSD  float64 `json:"maxCostUSD"`
		OnBehalfOf  string  `json:"onBehalfOf,omitempty"`
		Ref         string  `json:"ref,omitempty"`
		Class       string  `json:"class,omitempty"`
		Suggested   string  `json:"suggested,omitempty"`
		Profile     string  `json:"profile,omitempty"`
		StepUpShown bool    `json:"stepUpShown,omitempty"`
		Outcome     string  `json:"outcome,omitempty"`
		DependsOn   []int32 `json:"dependsOn,omitempty"`
	}{r.Repository, r.Title, r.Text, r.DataClass, r.MaxCostUSD, r.OnBehalfOf, r.Ref, r.Class, r.Suggested, r.Profile, r.StepUpShown, r.Outcome, r.DependsOn})
	if err != nil {
		return TaskResult{}, fmt.Errorf("runrequest: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL+"/v1/tasks", bytes.NewReader(body))
	if err != nil {
		return TaskResult{}, fmt.Errorf("runrequest: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", r.IdempotencyKey)
	hc, err := f.client()
	if err != nil {
		return TaskResult{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		// The client's error names the URL, never the headers: the token stays out of it.
		return TaskResult{}, fmt.Errorf("runrequest: factory: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxResponse)
	if err != nil {
		return TaskResult{}, fmt.Errorf("runrequest: factory: %w", err)
	}
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		var out struct {
			Task      string `json:"task"`
			Room      string `json:"room"`
			Issue     int    `json:"issue"`
			Duplicate bool   `json:"duplicate"`
			Sponsor   string `json:"sponsor"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return TaskResult{}, fmt.Errorf("runrequest: factory answer: %w", err)
		}
		if !envelope.ValidID(out.Room) || !envelope.ValidID(out.Task) { // never a task the caller cannot poll
			return TaskResult{}, fmt.Errorf("runrequest: factory answer: no valid task and room")
		}
		if !sponsorLogin.MatchString(out.Sponsor) {
			out.Sponsor = ""
		}
		return TaskResult{Task: out.Task, Room: out.Room, Issue: out.Issue, Duplicate: out.Duplicate, Sponsor: out.Sponsor}, nil
	}
	if resp.StatusCode >= 400 {
		var out struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &out)
		reason := "refused"
		if slices.Contains(refusalCodes, out.Error) {
			reason = out.Error
		}
		return TaskResult{}, ErrRefused{Status: resp.StatusCode, Reason: reason}
	}
	return TaskResult{}, fmt.Errorf("runrequest: factory: %s", resp.Status)
}
