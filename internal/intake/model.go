// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/httpx"
)

// The one call's fixed shape, from the contract (§model-call): the pinned alias is the only model it
// may name, temperature 0, at most 4 000 tokens of answer, 64 KiB of reply, and a deadline under
// `/mcp`'s 25 s bound.
const (
	// maxReply is the reply bound. A reply over it is refused whole: a truncated plan is a plan
	// nobody confirmed.
	maxReply = 64 << 10
	// replyTokens is the completion cap the contract sets: max_tokens ≤ 4 000.
	replyTokens = 4000
	// defaultDeadline is the call's deadline when Model carries none: the contract's 20 s, which
	// sits under the 25 s a `/mcp` request has.
	defaultDeadline = 20 * time.Second
	// maxTitleRunes and maxBodyBytes are `sanitize.CheckDraft`'s bounds, restated because a reply
	// has to have its own field named with its bound before the validator re-checks it.
	maxTitleRunes = 256
	maxBodyBytes  = 16 << 10
)

var gapIDRE = regexp.MustCompile(`^g[1-9][0-9]*$`)

// The sentinels the caller maps to the closed refusal list: each spells its own reason, so a wrapped
// error already reads as the line a person is shown, and `errors.Is` on it is the whole mapping —
// ErrInvalid to planner_invalid, ErrTimeout to planner_timeout, ErrUnavailable to
// planner_unavailable. Everything after the colon is the field and the bound, or why the call failed.
var (
	// ErrInvalid is a reply, or a request, that is not the shape the contract asks for:
	// `planner_invalid`. It is never retried — the reply is refused whole, and the field and the
	// bound are in the error.
	ErrInvalid = errors.New(ReasonPlannerInvalid)
	// ErrUnavailable is a gateway the broker could not reach, refused or could not authenticate
	// to: `planner_unavailable`. It is never retried inside this package: one call per attempt.
	ErrUnavailable = errors.New(ReasonPlannerUnavailable)
	// ErrTimeout is the one call outliving its deadline: `planner_timeout`. The deadline, not the
	// client's patience, ends the call.
	ErrTimeout = errors.New(ReasonPlannerTimeout)
)

// errPlannerInvalid is ErrInvalid with the field and the bound a person acts on. detail must never
// carry plan text or a repository the requester may not know exists.
func errPlannerInvalid(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalid, detail)
}

// errPlannerUnavailable is ErrUnavailable with why, in the same shape.
func errPlannerUnavailable(detail string) error {
	return fmt.Errorf("%w: %s", ErrUnavailable, detail)
}

// errPlannerTimeout is ErrTimeout with the deadline it outlived.
func errPlannerTimeout(detail string) error {
	return fmt.Errorf("%w: %s", ErrTimeout, detail)
}

// Model is the one bounded `chat/completions` call (CD18): the route the broker's config names, the
// pinned alias, the key read from a file at use, the audited egress client and a deadline. It holds
// no state — every field is configuration, so two callers share one value.
type Model struct {
	// URL is the gateway's base route (`human.intake.plan.url`); the call is appended to it. A
	// package never hard-codes a platform endpoint.
	URL string
	// APIKeyFile is the file holding the `planner` client's key, read at use so a rotated key is
	// picked up without a restart. The key is never in the body and never logged.
	APIKeyFile string
	// Alias is the one model alias the planner may name: `tier-frontier`.
	Alias string
	// HC is the audited egress client (`httpx.New`): timeouts, the redirect cap, the metadata
	// guard. A nil client makes no call.
	HC *http.Client
	// Deadline bounds the one call. Zero means the contract's 20 s.
	Deadline time.Duration
	// ReasoningEffort is the call's `reasoning_effort` (`human.intake.plan.reasoningEffort`); empty
	// sends no such field.
	ReasoningEffort string
}

// DraftRequest is one governed question: what the person asked, the context they brought (both
// public text, bounded), the candidate repositories the guard already allowed — the only
// repositories the reply may name — and the classes the broker's table defines, which are the only
// ones a reply may carry (CD17). Classes is the config's vocabulary shown to the model, so that a
// reply names one of them: a model guessing at classes the broker does not serve makes
// `invalid_class` the default outcome rather than the exception. Empty, the prompt says nothing
// about classes, which is the shape `Compose` refuses.
type DraftRequest struct {
	Text       string
	Context    string
	Candidates []string
	Classes    []string
	// Candidate is the broker-verified revision to re-plan. It is serialized as its own JSON
	// envelope, separately from Context's 4 KiB bound; settled text and Decisions are data to preserve.
	Candidate *Plan
}

// Draft is the model's answer, as it arrives and before config has a say: the repository and the
// evidence behind it, the alternatives, one to three issues, and its ranked clarification gaps.
type Draft struct {
	Repository   string        `json:"repository"`
	Evidence     []Evidence    `json:"evidence"`
	Alternatives []Alternative `json:"alternatives"`
	Issues       []DraftIssue  `json:"issues"`
	Gaps         []Gap         `json:"gaps"`
}

// DraftIssue is one issue as the reply composed it. Roles and BudgetUSD exist only so that a reply
// naming them can be dropped and logged (CD17): Compose never reads them, and no plan can carry a
// model's idea of who works or what it costs.
type DraftIssue struct {
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Class     string   `json:"class"`
	DependsOn []int    `json:"dependsOn"`
	Roles     []string `json:"roles"`
	BudgetUSD float64  `json:"budgetUSD"`
}

// Usage is what the call cost, from the reply's own `usage`: the broker attributes a spend it can
// see, and a reply without usage is refused. The planning attempt's reconciliation identifiers and
// amounts ride along when the governed route supplies them (v4 MR4); today's gateway supplies none,
// and a broker-side call has no AgentRun id to invent in their place.
type Usage struct {
	// Model is the `model` the gateway's reply named, empty when it names none.
	Model            string
	PromptTokens     int
	CompletionTokens int
	AttemptID        string
	ReservationID    string
	SettlementID     string
	ReservedMicroUSD int64
	SettledMicroUSD  int64
}

// GovernedRequest is one call on the governed route for any typed output (v4 MR4): what the person
// asked, their context, the candidate repositories the guard allowed, the classes the config defines,
// and the system prompt naming the output's one JSON shape. DraftRequest is this with plan/v1's
// prompt; the coordination planner is this with its own.
type GovernedRequest struct {
	Text         string
	Context      string
	Candidates   []string
	Classes      []string
	candidate    *Plan // plan/v1 revisions only; coordination's typed outputs have no candidate
	SystemPrompt string
}

// Draft is the one call for plan/v1: the governed request, the strict decode, and the three ways it
// fails. It makes exactly one attempt — no retry, no fallback, no second model — because the daily
// cap and the per-attempt accounting are the caller's, and a client that retried would spend a budget
// nobody approved.
func (m *Model) Draft(ctx context.Context, req DraftRequest) (Draft, Usage, error) {
	var d Draft
	prompt := systemPrompt
	if req.Candidate != nil {
		prompt += revisionSystemPrompt
	}
	usage, err := m.Generate(ctx, GovernedRequest{Text: req.Text, Context: req.Context, Candidates: req.Candidates,
		Classes: req.Classes, candidate: req.Candidate, SystemPrompt: prompt}, &d)
	if err != nil {
		return Draft{}, Usage{}, err
	}
	if detail := d.invalid(); detail != "" {
		return Draft{}, Usage{}, errPlannerInvalid(detail)
	}
	if detail := d.invalidModelGapCoverage(); detail != "" {
		return Draft{}, Usage{}, errPlannerInvalid(detail)
	}
	return d, usage, nil
}

// Generate is the governed call itself, for every typed output the route serves (CD18, v4 MR4): the
// pinned alias, temperature 0, the token and reply bounds, the deadline, the key read at use, one
// attempt. The answer is decoded strictly into out, unknown fields and trailing content refused, so
// each caller's own type is its contract; the bounds of its fields are the caller's to hold.
func (m *Model) Generate(ctx context.Context, req GovernedRequest, out any) (Usage, error) {
	user, err := userMessage(DraftRequest{Text: req.Text, Context: req.Context, Candidates: req.Candidates,
		Classes: req.Classes, Candidate: req.candidate})
	if err != nil {
		return Usage{}, err
	}
	if m.HC == nil || m.URL == "" || m.Alias == "" || strings.TrimSpace(req.SystemPrompt) == "" {
		// Nothing to call, nowhere to call it, or no shape to ask for: a configuration error, not a plan.
		return Usage{}, errPlannerUnavailable("the planner's client, route, alias or prompt is unset")
	}
	key, err := m.apiKey()
	if err != nil {
		return Usage{}, err
	}
	payload, err := json.Marshal(chatRequest{
		Model:           m.Alias,
		Temperature:     0,
		MaxTokens:       replyTokens,
		ReasoningEffort: m.ReasoningEffort,
		ResponseFormat:  responseFormat{Type: "json_object"},
		Messages:        []message{{Role: "system", Content: req.SystemPrompt}, {Role: "user", Content: user}},
	})
	if err != nil {
		return Usage{}, errPlannerUnavailable(fmt.Sprintf("marshal the request: %v", err))
	}

	deadline := m.Deadline
	if deadline <= 0 {
		deadline = defaultDeadline
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	url := strings.TrimSuffix(m.URL, "/") + "/chat/completions"
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Usage{}, errPlannerUnavailable(fmt.Sprintf("build the call: %v", err))
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	hr.Header.Set("Authorization", "Bearer "+key)

	resp, err := m.HC.Do(hr)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return Usage{}, errPlannerTimeout(fmt.Sprintf("the call outlived its %s deadline", deadline))
		}
		return Usage{}, errPlannerUnavailable(fmt.Sprintf("the call failed: %v", err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Usage{}, errPlannerUnavailable(fmt.Sprintf("the gateway answered %s", resp.Status))
	}
	body, err := httpx.ReadBody(resp.Body, maxReply)
	if err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			return Usage{}, errPlannerInvalid(fmt.Sprintf("the reply is over the %d KiB bound, and nothing was truncated", maxReply>>10))
		}
		return Usage{}, errPlannerUnavailable(fmt.Sprintf("read the reply: %v", err))
	}
	return decodeReply(body, out)
}

// apiKey reads the planner's key at use and trims it: a file that is not there, or is empty, is a
// gateway the broker cannot authenticate to.
func (m *Model) apiKey() (string, error) {
	b, err := os.ReadFile(m.APIKeyFile)
	if err != nil {
		return "", errPlannerUnavailable(fmt.Sprintf("read the planner key: %v", err))
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", errPlannerUnavailable("the planner's key file is empty")
	}
	return key, nil
}

// chatRequest is the request body, and the whole of it: the alias, temperature 0, the token cap, the
// JSON object mode the reply shape needs, the reasoning effort when one is configured, and the two
// messages. No provider hint, no key.
type chatRequest struct {
	Model           string         `json:"model"`
	Temperature     int            `json:"temperature"`
	MaxTokens       int            `json:"max_tokens"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	ResponseFormat  responseFormat `json:"response_format"`
	Messages        []message      `json:"messages"`
}

// responseFormat asks for one JSON object, which is the only shape a strict decode can read.
type responseFormat struct {
	Type string `json:"type"`
}

// message is one turn of the conversation: the system prompt, then the person's request.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatReply is the envelope the gateway answers with. It is decoded tolerantly — the vendor's own
// fields (`id`, `object`, `created`, `model`, `finish_reason`) are the gateway's, not the model's,
// and refusing a gateway for adding one would refuse every reply the day it ships — and the one
// field carrying the model's answer is then decoded strictly, which is where the contract is
// enforced.
type chatReply struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Model string `json:"model"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// decodeReply reads the reply body into out: the envelope, the one choice, the usage the cost is
// attributed from, then the answer itself with unknown fields refused. Every failure is ErrInvalid
// with the field named.
func decodeReply(body []byte, out any) (Usage, error) {
	var reply chatReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return Usage{}, errPlannerInvalid(fmt.Sprintf("the reply is not the gateway's JSON: %v", err))
	}
	if n := len(reply.Choices); n != 1 {
		return Usage{}, errPlannerInvalid(fmt.Sprintf("choices: %d, the reply names the one answer it composed", n))
	}
	if reply.Usage == nil {
		// A cost the broker cannot attribute is a cost it may not spend.
		return Usage{}, errPlannerInvalid("usage: the reply carries no token counts, and the broker attributes every plan's cost from them")
	}
	content := reply.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		return Usage{}, errPlannerInvalid("choices[0].message.content: empty, the reply is the one JSON object the prompt asks for")
	}

	dec := json.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return Usage{}, errPlannerInvalid(fmt.Sprintf("the reply is not the shape the prompt asks for: %v", err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		// Trailing content is not the one object either: a second value could carry a field the
		// strict decode above never saw.
		return Usage{}, errPlannerInvalid("the reply carries more than the one JSON object")
	}
	return Usage{Model: strings.TrimSpace(reply.Model), PromptTokens: reply.Usage.PromptTokens, CompletionTokens: reply.Usage.CompletionTokens}, nil
}

// invalid is "" when the reply is the shape and the bounds the contract sets, and otherwise the field
// and the bound a person acts on.
//
// It does not hold the draft to the issue template: `Compose` runs `sanitize.CheckDraft`, whose own
// message is the one a person reads (`invalid_draft`), and two spellings of one rule is two
// sentences for the same defect.
func (d Draft) invalid() string {
	if strings.TrimSpace(d.Repository) == "" {
		return "repository: empty, the reply names the one repository it chose"
	}
	if n := len(d.Issues); n < 1 || n > maxIssues {
		return fmt.Sprintf("issues: %d, the contract asks for 1 to %d", n, maxIssues)
	}
	for i, is := range d.Issues {
		if line := strings.TrimSpace(is.Title); line == "" || strings.Contains(is.Title, "\n") ||
			utf8.RuneCountInString(line) > maxTitleRunes {
			return fmt.Sprintf("issues[%d].title: it is one line, `<area>: <what is wrong>`, at most %d characters", i, maxTitleRunes)
		}
		if n := len(is.Body); n > maxBodyBytes {
			return fmt.Sprintf("issues[%d].body: %d bytes, the bound is %d KiB", i, n, maxBodyBytes>>10)
		}
		if strings.TrimSpace(is.Body) == "" {
			return fmt.Sprintf("issues[%d].body: empty, the body is the template's three headings", i)
		}
		if strings.TrimSpace(is.Class) == "" {
			return fmt.Sprintf("issues[%d].class: empty, the reply names the class it belongs to", i)
		}
	}
	if n := len(d.Evidence); n > maxEvidence {
		return fmt.Sprintf("evidence: %d items, the bound is %d", n, maxEvidence)
	}
	for i, e := range d.Evidence {
		if n := utf8.RuneCountInString(e.Text); n > maxEvidenceRunes {
			return fmt.Sprintf("evidence[%d].text: %d characters, the bound is %d", i, n, maxEvidenceRunes)
		}
	}
	seenGaps := make(map[string]struct{}, len(d.Gaps))
	for i, g := range d.Gaps {
		if strings.TrimSpace(g.ID) == "" {
			return fmt.Sprintf("gaps[%d].id: empty, each gap names its one clarification marker", i)
		}
		if !gapIDRE.MatchString(g.ID) {
			return fmt.Sprintf("gaps[%d].id: it is a canonical g[1-9][0-9]* identifier", i)
		}
		if _, ok := seenGaps[g.ID]; ok {
			return fmt.Sprintf("gaps[%d].id: already used by another gap", i)
		}
		seenGaps[g.ID] = struct{}{}
		switch g.Section {
		case "what-is-wrong", "what-it-should-be", "acceptance-check", "scope":
		default:
			return fmt.Sprintf("gaps[%d].section: it is what-is-wrong, what-it-should-be, acceptance-check, or scope", i)
		}
		if strings.TrimSpace(g.Text) == "" || strings.TrimSpace(g.Recommended.Answer) == "" {
			return fmt.Sprintf("gaps[%d]: it carries text and a recommended answer", i)
		}
		markers := 0
		for _, is := range d.Issues {
			markers += clarificationMarkerCount(is.Body, g.ID)
		}
		if markers != 1 {
			return fmt.Sprintf("gaps[%d].id: it names exactly one clarification marker already in an issue body", i)
		}
	}
	return ""
}

// invalidModelGapCoverage closes the model reply's marker set: Compose may hold an intermediate
// marker without gaps, but a model reply must account for every parsed marker exactly once.
func (d Draft) invalidModelGapCoverage() string {
	totalMarkers := 0
	for i, is := range d.Issues {
		markers, err := sanitize.ClarificationMarkers(is.Body)
		if err != nil {
			return fmt.Sprintf("issues[%d].body: it has a malformed clarification marker", i)
		}
		totalMarkers += len(markers)
	}
	if totalMarkers != len(d.Gaps) {
		return fmt.Sprintf("gaps: every clarification marker needs one ranked gap, got %d markers and %d gaps", totalMarkers, len(d.Gaps))
	}
	return ""
}

// clarificationMarkerCount counts complete, non-empty [NEEDS CLARIFICATION: id text] markers. The
// space after id keeps g1 from naming g10's marker.
func clarificationMarkerCount(body, id string) int {
	prefix := "[NEEDS CLARIFICATION: " + id + " "
	count := 0
	for {
		i := strings.Index(body, prefix)
		if i < 0 {
			return count
		}
		body = body[i+len(prefix):]
		end := strings.IndexByte(body, ']')
		if end < 0 {
			return count
		}
		if strings.TrimSpace(body[:end]) != "" {
			count++
		}
		body = body[end+1:]
	}
}
