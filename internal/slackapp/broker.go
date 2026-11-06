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

	"github.com/Smana/maku/internal/httpx"
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

// RunCreate is a draft filed as a run for the person who asked. Plan and PlanHash are set when the
// run is a proposal: the plan the message showed and the hash it bound, so the broker recomposes the
// plan from the fields it is sent, recomputes the hash over the plan's material set and refuses
// `plan_changed` when they disagree (CD20, CD27). A draft sets neither, and its arguments stay
// exactly what they were.
type RunCreate struct {
	Draft
	Plan       json.RawMessage // the plan/v1 the proposal bound, as its message carried it
	PlanHash   string          // intake.Hash of that plan
	MaxCostUSD float64
	OnBehalfOf string // slack:<team>/<user>
	Ref        string // slack/<team>/<channel>/<thread_ts>
	Key        string // the idempotency key
}

// RunResult is what run_create answered: Sponsor is the GitHub login the factory filed as, "" when
// it did not say.
type RunResult struct {
	Room      string
	Issue     int
	Duplicate bool
	Sponsor   string
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

// IssueDraft asks the broker to judge a draft against the issue template.
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
// body and dataClass is always public. A proposal adds `plan` and `plan_hash` — the pair the tool
// checks the confirmation against — and nothing else: a draft's arguments are the ones they always
// were, so an App that has no plan sends the call it always sent.
func (b Broker) RunCreate(ctx context.Context, c RunCreate) (RunResult, error) {
	args := map[string]any{"repo": c.Repo, "title": c.Title, "text": c.Body,
		"dataClass": "public", "max_cost": c.MaxCostUSD, "on_behalf_of": c.OnBehalfOf, "ref": c.Ref,
		"idempotency_key": c.Key}
	if len(c.Plan) > 0 {
		args["plan"], args["plan_hash"] = c.Plan, c.PlanHash
	}
	sc, err := b.call(ctx, "run_create", args)
	if err != nil {
		return RunResult{}, err
	}
	var out struct {
		Room      string `json:"room"`
		Issue     int    `json:"issue"`
		Duplicate bool   `json:"duplicate"`
		Sponsor   string `json:"sponsor"`
	}
	if err := json.Unmarshal(sc, &out); err != nil {
		return RunResult{}, fmt.Errorf("slackapp: run_create result: %w", err)
	}
	return RunResult(out), nil
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
