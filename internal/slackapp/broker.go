// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/summary"
)

const (
	protocolVersion = "2025-11-25"
	maxReply        = 256 << 10
)

// Tokens caches the App's client-credentials token until it expires or the broker refuses it
// (L9): clientcredentials' own TokenSource reuses a token until it expires and cannot be told
// that the broker refused it. Fetch should go through httpx (oauth2.HTTPClient in the context).
type Tokens struct {
	Fetch func(ctx context.Context) (*oauth2.Token, error)

	mu  sync.Mutex
	tok *oauth2.Token
}

// Token is the cached access token, fetched when there is none or it expired.
func (s *Tokens) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tok.Valid() {
		t, err := s.Fetch(ctx)
		if err != nil {
			return "", fmt.Errorf("slackapp: token: %w", err)
		}
		s.tok = t
	}
	return s.tok.AccessToken, nil
}

// Invalidate drops the cached token after a 401, so the retry fetches a new one.
func (s *Tokens) Invalidate() {
	s.mu.Lock()
	s.tok = nil
	s.mu.Unlock()
}

// Refused is a tool's refusal ("rejected: <code>"), shown in the thread as is.
type Refused string

func (r Refused) Error() string { return string(r) }

// Broker is the room-broker's MCP endpoint, called as the App's own principal. It never calls
// tasks/*: the App follows a room through webhooks.
type Broker struct {
	URL    string // the broker's base URL, from config only
	Tokens *Tokens
	HC     *http.Client // httpx.New in production
}

// RunCreate is a draft filed as a run for the person who asked. Handoff is set when the run is a
// proposal (UX2): the id of the plan the broker stored, so the broker files that plan after
// re-validating it, and PlanHash binds the plan the message showed to the one stored — the broker
// refuses `plan_changed` when they disagree (CD20, CD27). A draft sets neither, and its arguments stay
// exactly what they were.
type RunCreate struct {
	Draft
	Handoff    string // the handoff id the proposal's metadata carries
	PlanHash   string // the hash the proposal showed, "" to file whatever the id holds
	MaxCostUSD float64
	OnBehalfOf string // slack:<team>/<user>
	Ref        string // slack/<team>/<channel>/<thread_ts>
	Key        string // the idempotency key
}

// RunResult is what run_create answered: Sponsor is the GitHub login the factory filed as, "" when
// it did not say. Replanned is set, and nothing was filed, when the stored plan was a plan/v1: the
// broker answered the plan/v2 to show and confirm instead (P14).
type RunResult struct {
	Room      string
	Issue     int
	Duplicate bool
	Sponsor   string
	Replanned *intake.Plan
}

// ErrUnknownTool is the broker without the tool asked: the self-serve link is off there.
var ErrUnknownTool = errors.New("slackapp: the broker does not offer this tool")

// decidePath is the route human.slackDecide registers on the broker's human listener (D2); unset
// there, it is the mux's 404, so a press answers not_supported-class transport and nothing happens.
const decidePath = "/api/slack/decisions"

// Decision is one press, as POST /api/slack/decisions takes it (§1 of the design). The App fills it
// from the signed click only; no field comes from the card's text.
type Decision struct {
	Team, User, Channel, MessageTS, ApprovalID, Branch, Decision string
}

// DecisionResult is the broker's closed code; Result is the decision when it recorded one.
type DecisionResult struct{ Result string }

// Decide posts one press to the broker's human listener as the App's principal, with the same token
// cache and the same 401 retry every other call uses. The route answers a closed code with 200 for
// every refusal, so anything but 200 is transport, not a decision.
func (b Broker) Decide(ctx context.Context, d Decision) (DecisionResult, error) {
	body, err := json.Marshal(map[string]string{"team": d.Team, "user": d.User, "channel": d.Channel,
		"messageTS": d.MessageTS, "approvalId": d.ApprovalID, "branch": d.Branch, "decision": d.Decision})
	if err != nil {
		return DecisionResult{}, err
	}
	for attempt := 0; ; attempt++ {
		raw, status, err := b.send(ctx, decidePath, body, false)
		if err != nil {
			return DecisionResult{}, err
		}
		if status == http.StatusUnauthorized && attempt == 0 {
			b.Tokens.Invalidate()
			continue
		}
		if status != http.StatusOK {
			return DecisionResult{}, fmt.Errorf("slackapp: broker answered %d", status)
		}
		var out struct {
			Result string `json:"result"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return DecisionResult{}, fmt.Errorf("slackapp: decide result: %w", err)
		}
		if out.Result == "" {
			return DecisionResult{}, errors.New("slackapp: broker reply has no result")
		}
		return DecisionResult{Result: out.Result}, nil
	}
}

// IssueDraft asks the broker to judge a draft against the issue template. Only the draft grammar
// calls it, which release N keeps behind proposals.enabled: false.
func (b Broker) IssueDraft(ctx context.Context, d Draft) (Draft, error) {
	sc, err := b.call(ctx, "issue_draft", map[string]any{"repo": d.Repo, "title": d.Title, "body": d.Body})
	if err != nil {
		return Draft{}, err
	}
	var out struct{ Repo, Title, Body string }
	if err := json.Unmarshal(sc, &out); err != nil {
		return Draft{}, fmt.Errorf("slackapp: issue_draft result: %w", err)
	}
	return Draft(out), nil
}

// RunCreate files the run. Its arguments are run_create's schema exactly: `text` is the draft's
// body and dataClass is always public. A proposal sends `handoff` (and `plan_hash`, the hash it
// showed) in place of the repository's issue: the broker files the plan it stored, so the App carries
// no plan and no body (UX2). A draft's arguments are the ones they always were.
func (b Broker) RunCreate(ctx context.Context, c RunCreate) (RunResult, error) {
	args := map[string]any{"dataClass": "public", "max_cost": c.MaxCostUSD, "on_behalf_of": c.OnBehalfOf,
		"ref": c.Ref, "idempotency_key": c.Key}
	if c.Repo != "" {
		args["repo"] = c.Repo
	}
	if c.Handoff != "" {
		args["handoff"] = c.Handoff
		if c.PlanHash != "" {
			args["plan_hash"] = c.PlanHash
		}
	} else {
		args["title"], args["text"] = c.Title, c.Body
	}
	sc, err := b.call(ctx, "run_create", args)
	if err != nil {
		return RunResult{}, err
	}
	var out struct {
		Room      string      `json:"room"`
		Issue     int         `json:"issue"`
		Duplicate bool        `json:"duplicate"`
		Sponsor   string      `json:"sponsor"`
		Status    string      `json:"status"`
		Plan      intake.Plan `json:"plan"`
		Hash      string      `json:"hash"`
	}
	if err := json.Unmarshal(sc, &out); err != nil {
		return RunResult{}, fmt.Errorf("slackapp: run_create result: %w", err)
	}
	if out.Status == "replanned" {
		// The App's copy is checked like any proposal's: the hash binds the plan it came with.
		if out.Hash == "" || intake.Hash(out.Plan) != out.Hash {
			return RunResult{}, errors.New("slackapp: run_create replanned with a hash that does not bind its plan")
		}
		plan := out.Plan
		plan.Hash = out.Hash
		return RunResult{Replanned: &plan}, nil
	}
	return RunResult{Room: out.Room, Issue: out.Issue, Duplicate: out.Duplicate, Sponsor: out.Sponsor}, nil
}

// HandoffRequest is handoff's arguments, as the App has them (UX2). A mention begins one: the
// requester's own words, the channel's repositories as the candidate set, the linked Slack user the
// plan is made for and the channel's ceiling. A reply continues one: the id the proposal's metadata
// carries, with the person's answer or AcceptRest. The App picks no repository and holds no plan: the
// broker stores the plan under the id and applies the answer to it.
type HandoffRequest struct {
	Text       string
	Candidates []string
	OnBehalfOf string
	CeilingUSD float64
	// ID, Answer and AcceptRest continue a handoff: ID with exactly one of the other two.
	ID         string
	Answer     string
	AcceptRest bool
}

// HandoffQuestion is the one question a plan asks, and the answer the planner recommends.
type HandoffQuestion struct {
	Text        string `json:"text"`
	Recommended string `json:"recommended"`
}

// HandoffResult is handoff/v1: the id the plan is stored under, the plan as text to show as it is,
// the one question when something is unclear, whether the plan is ready to start, and the workflow
// and budget it would run with.
type HandoffResult struct {
	ID       string           `json:"id"`
	Text     string           `json:"text"`
	Question *HandoffQuestion `json:"question"`
	Ready    bool             `json:"ready"`
	Workflow string           `json:"workflow"`
	Budget   float64          `json:"budget"`
}

// Handoff is that tool's one call. A key the tool does not take is never sent: a sponsored principal
// states whom it plans for and the channel's ceiling on every call, since the broker checks both
// again on a continuation. ErrUnknownTool comes back untouched, and so does the planner-off line
// (a Refused), so the caller answers the mention's own line.
func (b Broker) Handoff(ctx context.Context, in HandoffRequest) (HandoffResult, error) {
	args := map[string]any{"on_behalf_of": in.OnBehalfOf, "ceiling": in.CeilingUSD}
	switch {
	case in.ID == "":
		args["text"], args["candidates"] = in.Text, in.Candidates
	case in.AcceptRest:
		args["id"], args["acceptRest"] = in.ID, true
	default:
		args["id"], args["answer"] = in.ID, in.Answer
	}
	sc, err := b.call(ctx, mcpwire.Handoff, args)
	if err != nil {
		return HandoffResult{}, err
	}
	var out HandoffResult
	if err := json.Unmarshal(sc, &out); err != nil {
		return HandoffResult{}, fmt.Errorf("slackapp: handoff result: %w", err)
	}
	if !linkCodeRE.MatchString(out.ID) {
		return HandoffResult{}, errors.New("slackapp: handoff answered no handoff id")
	}
	return out, nil
}

// HandoffChecked is handoff_check's answer: the stored plan re-validated as start and run_create
// validate it, the hash it is stored under, the workflows the Slack user may choose (none unless
// they maintain the repository) and the issue it files.
type HandoffChecked struct {
	Plan       intake.Plan `json:"plan"`
	Hash       string      `json:"hash"`
	Workflows  []string    `json:"workflows"`
	Repository string      `json:"repository"`
	Title      string      `json:"title"`
}

// handoffCheckTool is the unlisted broker tool the workflow select and the start form share.
const handoffCheckTool = "handoff_check"

// HandoffCheck is handoff_check for the Slack user named: the stored plan, its hash and the
// workflows they may choose. workflow nil changes nothing; a value pins that workflow ("" for the
// factory's choice), which re-validates the stored plan and stores its new hash. It files nothing.
func (b Broker) HandoffCheck(ctx context.Context, id, onBehalfOf string, workflow *string) (HandoffChecked, error) {
	args := map[string]any{"id": id, "on_behalf_of": onBehalfOf}
	if workflow != nil {
		args["workflow"] = *workflow
	}
	sc, err := b.call(ctx, handoffCheckTool, args)
	if err != nil {
		return HandoffChecked{}, err
	}
	var out HandoffChecked
	if err := json.Unmarshal(sc, &out); err != nil {
		return HandoffChecked{}, fmt.Errorf("slackapp: handoff_check result: %w", err)
	}
	if out.Hash == "" || intake.Hash(out.Plan) != out.Hash {
		return HandoffChecked{}, errors.New("slackapp: handoff_check answered a hash that does not bind its plan")
	}
	out.Plan.Hash = out.Hash
	return out, nil
}

// RoomSummary is room_summary: the room's summary/v1 object with its coordination view attached. Only
// a coordination parent's thread reads it, since task/v1 carries no coordination view; every other
// read of a task goes through Task.
func (b Broker) RoomSummary(ctx context.Context, room string) (summary.Summary, error) {
	sc, err := b.call(ctx, "room_summary", map[string]any{"room": room})
	if err != nil {
		return summary.Summary{}, err
	}
	var out summary.Summary
	if err := json.Unmarshal(sc, &out); err != nil {
		return summary.Summary{}, fmt.Errorf("slackapp: room_summary result: %w", err)
	}
	return out, nil
}

// TaskResult is task/v1 as a thread reads it (UX4): the task's room, its page, its status (the
// summary's, with the spend and the gate's decision), its runs, what it needs and the milestones since
// the cursor.
type TaskResult struct {
	Room     string               `json:"room"`
	URL      string               `json:"url"`
	Text     string               `json:"text"`
	Cursor   string               `json:"cursor"`
	Runs     []envelope.RunFact   `json:"runs"`
	NeedsYou []summary.Need       `json:"needsYou"`
	Items    []roomview.Milestone `json:"items"`
	More     int                  `json:"more"`
	// Status is summary/v1's status object, decoded into a summary so the thread's answers read the
	// same fields the room page renders.
	Status json.RawMessage `json:"status"`
}

// Summary is the task as summary/v1: the room, its page, its status, its runs and its needs — the
// fields a thread's answers and the end line read. A status that does not decode is left empty.
func (t TaskResult) Summary() summary.Summary {
	var s summary.Summary
	s.Room, s.URL, s.Runs, s.NeedsYou = t.Room, t.URL, t.Runs, t.NeedsYou
	if len(t.Status) > 0 {
		_ = json.Unmarshal(t.Status, &s.Status)
	}
	return s
}

// Changes is the task's milestones as the changes view's set.
func (t TaskResult) Changes() roomview.ChangeSet {
	return roomview.ChangeSet{Items: t.Items, More: t.More, Cursor: t.Cursor}
}

// Task is task: one read of where the task stands, what it needs and what changed (UX4). The room is
// the id the thread's Filed line named; the App never asks for a room it guessed.
func (b Broker) Task(ctx context.Context, room string) (TaskResult, error) {
	sc, err := b.call(ctx, mcpwire.Task, map[string]any{"task": room})
	if err != nil {
		return TaskResult{}, err
	}
	var out TaskResult
	if err := json.Unmarshal(sc, &out); err != nil {
		return TaskResult{}, fmt.Errorf("slackapp: task result: %w", err)
	}
	return out, nil
}

// Inbox is inbox for one room: the needs/v1 items alone. A sponsored caller reads a room's needs,
// never a login's, and each item's action is a link it cannot follow for anyone (CD26).
func (b Broker) Inbox(ctx context.Context, room string) ([]summary.Need, error) {
	sc, err := b.call(ctx, mcpwire.Inbox, map[string]any{"room": room})
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []summary.Need `json:"items"`
	}
	if err := json.Unmarshal(sc, &out); err != nil {
		return nil, fmt.Errorf("slackapp: inbox result: %w", err)
	}
	return out.Items, nil
}

// IntentRequest is intent_classify's arguments: the reply alone, its author, the channel and the
// thread's phase as one closed word. Collect is true only for a channel that opted in to collecting
// replies; the App never sends thread history.
type IntentRequest struct {
	Team, User, Channel string
	Reply, Phase        string
	Collect             bool
}

// IntentResult is intent_classify's answer: a label, never an action. Text is the span of the reply a
// steer or an amend acts on, TextHash its hash, Expires when a confirm for it lapses and SampleID the
// stored sample's id when the channel collects.
type IntentResult struct {
	Intent     string  `json:"intent"`
	Text       string  `json:"text"`
	TextHash   string  `json:"textHash"`
	Confidence float64 `json:"confidence"`
	Expires    string  `json:"expires"`
	SampleID   int64   `json:"sampleId"`
}

// IntentClassify is that tool's one call. ErrUnknownTool is a broker without human.slackIntents, and a
// Refused is one of the closed words (not_linked, capped, ...): the caller branches on neither more than
// to fall back to today's line.
func (b Broker) IntentClassify(ctx context.Context, in IntentRequest) (IntentResult, error) {
	sc, err := b.call(ctx, mcpwire.IntentClassify, map[string]any{"team": in.Team, "user": in.User, "channel": in.Channel,
		"reply": in.Reply, "phase": in.Phase, "collect": in.Collect})
	if err != nil {
		return IntentResult{}, err
	}
	var out IntentResult
	if err := json.Unmarshal(sc, &out); err != nil {
		return IntentResult{}, fmt.Errorf("slackapp: intent_classify result: %w", err)
	}
	return out, nil
}

// linkPath is the page a link code opens on the broker's public URL.
const linkPath = "/link/slack"

// SlackLinkCode asks the broker for a one-time link for the Slack user, handle and name shown on
// the page. The answer must be the link page with one code, or it is not passed on to the user.
func (b Broker) SlackLinkCode(ctx context.Context, team, user, handle, name string) (string, error) {
	sc, err := b.call(ctx, "slack_link_code", map[string]any{"team": team, "user": user, "handle": handle, "name": name})
	if err != nil {
		return "", err
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(sc, &out); err != nil {
		return "", fmt.Errorf("slackapp: slack_link_code result: %w", err)
	}
	u, err := url.Parse(out.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != linkPath || u.User != nil ||
		u.Fragment != "" || len(u.Query()) != 1 || !linkCodeRE.MatchString(u.Query().Get("code")) {
		return "", errors.New("slackapp: slack_link_code answered no link page")
	}
	return u.String(), nil
}

var linkCodeRE = regexp.MustCompile(`^[a-z2-7]{26}$`)

// SlackUnlink asks the broker to remove the Slack user's link; unlinked is false when none was.
func (b Broker) SlackUnlink(ctx context.Context, team, user string) (bool, error) {
	sc, err := b.call(ctx, "slack_unlink", map[string]any{"team": team, "user": user})
	if err != nil {
		return false, err
	}
	var out struct {
		Unlinked bool `json:"unlinked"`
	}
	if err := json.Unmarshal(sc, &out); err != nil {
		return false, fmt.Errorf("slackapp: slack_unlink result: %w", err)
	}
	return out.Unlinked, nil
}

// call is one stateless tools/call. A 401 drops the token and retries once with a new one.
func (b Broker) call(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		raw, status, err := b.post(ctx, body)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized && attempt == 0 {
			b.Tokens.Invalidate()
			continue
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("slackapp: broker answered %d", status)
		}
		return decodeResult(raw)
	}
}

func (b Broker) post(ctx context.Context, body []byte) ([]byte, int, error) {
	return b.send(ctx, "/mcp", body, true)
}

// send is one POST to the broker, as the App's principal: path is the listener's route, mcp adds
// the tools endpoint's own headers (the decide route takes none).
func (b Broker) send(ctx context.Context, path string, body []byte, mcp bool) ([]byte, int, error) {
	tok, err := b.Tokens.Token(ctx)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(b.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if mcp {
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	hc := b.HC
	if hc == nil {
		hc = httpx.New(30*time.Second, nil)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("slackapp: broker: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxReply)
	if err != nil {
		return nil, 0, fmt.Errorf("slackapp: broker reply: %w", err)
	}
	return raw, resp.StatusCode, nil
}

func decodeResult(raw []byte) (json.RawMessage, error) {
	var msg struct {
		Error  *struct{ Message string } `json:"error"`
		Result *struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("slackapp: broker reply: %w", err)
	}
	switch {
	case msg.Error != nil && strings.HasPrefix(msg.Error.Message, "unknown tool"):
		return nil, ErrUnknownTool
	case msg.Error != nil:
		return nil, fmt.Errorf("slackapp: broker: %s", msg.Error.Message)
	case msg.Result == nil:
		return nil, errors.New("slackapp: broker reply has no result")
	case msg.Result.IsError:
		text := "rejected"
		if len(msg.Result.Content) > 0 {
			text = msg.Result.Content[0].Text
		}
		return nil, Refused(text)
	case len(msg.Result.StructuredContent) == 0:
		return nil, errors.New("slackapp: broker result has no structuredContent")
	}
	return msg.Result.StructuredContent, nil
}

// CoordinationPlanRequest is coordination_plan's arguments: the mention's words, the user's primary
// first, the two candidates, the user it plans for and, to replan, the draft's id and next version.
type CoordinationPlanRequest struct {
	Text, Context, Primary string
	Candidates             []string
	OnBehalfOf             string
	ID                     string `json:"id,omitempty"`
	Version                int32  `json:"version,omitempty"`
}

// CoordinationPlanResult is coordination_plan's answer, as the broker serves it.
type CoordinationPlanResult struct {
	Plan        v1alpha1.CoordinationPlan           `json:"plan"`
	Hash        string                              `json:"hash"`
	Diagnostics []coordination.Diagnostic           `json:"diagnostics"`
	Planning    coordination.PlanningReconciliation `json:"planning"`
}

// CoordinationPlan is coordination_plan, the broker's governed multi-repository planner: it files
// nothing.
func (b Broker) CoordinationPlan(ctx context.Context, in CoordinationPlanRequest) (CoordinationPlanResult, error) {
	args := map[string]any{"text": in.Text, "primary": in.Primary, "candidates": in.Candidates, "on_behalf_of": in.OnBehalfOf}
	if in.Context != "" {
		args["context"] = in.Context
	}
	if in.ID != "" {
		args["id"], args["version"] = in.ID, in.Version
	}
	sc, err := b.call(ctx, mcpwire.CoordinationPlan, args)
	if err != nil {
		return CoordinationPlanResult{}, err
	}
	var out CoordinationPlanResult
	if err := json.Unmarshal(sc, &out); err != nil {
		return CoordinationPlanResult{}, fmt.Errorf("slackapp: coordination_plan result: %w", err)
	}
	return out, nil
}

// CoordinationDraft is coordination_draft: the plan stored as its parent's draft, for the user. It
// confirms nothing; the parent is confirmed on its room page.
func (b Broker) CoordinationDraft(ctx context.Context, parent string, in coordination.CoordinationConfirmation, onBehalfOf string) (coordination.CoordinationConfirmation, error) {
	if parent != in.Plan.ID {
		return coordination.CoordinationConfirmation{}, fmt.Errorf("slackapp: coordination_draft: %q is not the plan's parent", parent)
	}
	_, err := b.call(ctx, mcpwire.CoordinationDraft, map[string]any{"plan": in.Plan, "planHash": in.PlanHash, "planning": in.Planning,
		"on_behalf_of": onBehalfOf})
	if err != nil {
		return coordination.CoordinationConfirmation{}, err
	}
	return in, nil
}
