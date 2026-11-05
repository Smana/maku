// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

const (
	slackAPI      = "https://slack.com/api/"
	maxSlackReply = 1 << 20
)

// Metadata is what the App attaches to its own messages (R23); Slack keeps it off the visible text.
// EventPayload is tagged: the App decodes it on demand by EventType, so a card and a draft share one
// message shape (v3.x Slack push approval §5). A draft's bytes on the wire do not change.
type Metadata struct {
	EventType    string          `json:"event_type"`
	EventPayload json.RawMessage `json:"event_payload"`
}

// DraftPayload is the checked draft and its hash, sha256 hex of repo, title and body joined by NUL.
type DraftPayload struct {
	Repo   string `json:"repo"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	SHA256 string `json:"sha256"`
	// Author is the Slack user who wrote the mention: the only one whose thread replies edit the
	// draft. Rev counts the drafts the thread has had, 1 for the first.
	Author string `json:"author,omitempty"`
	Rev    int    `json:"rev,omitempty"`
}

// ApprovalPayload is a card's metadata (§5): what finds the card again without a search scope.
type ApprovalPayload struct {
	ApprovalID string `json:"approvalId"`
	Class      string `json:"class"`
	Branch     string `json:"branch"`
}

// approvalEventType marks a card the App posted for an approval.
const approvalEventType = "factory_approval"

// draftPayload decodes the payload as the draft the event type claims; false when it is anything
// else, which reads as "not a draft", the branch a foreign message already takes.
func (m *Metadata) draftPayload() (DraftPayload, bool) {
	if m == nil || m.EventType != draftEventType {
		return DraftPayload{}, false
	}
	var p DraftPayload
	if err := json.Unmarshal(m.EventPayload, &p); err != nil {
		return DraftPayload{}, false
	}
	return p, true
}

// approvalPayload decodes the payload as a card's; false when it is anything else.
func (m *Metadata) approvalPayload() (ApprovalPayload, bool) {
	if m == nil || m.EventType != approvalEventType {
		return ApprovalPayload{}, false
	}
	var p ApprovalPayload
	if err := json.Unmarshal(m.EventPayload, &p); err != nil {
		return ApprovalPayload{}, false
	}
	return p, true
}

// metaPayload marshals a payload the App builds itself. The error is returned rather than dropped:
// the payload is what finds the message again, so a caller that cannot tag must not post as if it
// had. Nothing the App builds today can fail to marshal; this is the barrier if one ever can.
func metaPayload(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("slackapp: slack: the message metadata: %w", err)
	}
	return raw, nil
}

// Message is a Slack message as the ✅ flow reads it (M9). User is who posted it: the App's own
// bot user for its drafts.
type Message struct {
	TS, ThreadTS, User, Text string
	Metadata                 *Metadata
}

// Slack is the Web API calls the App makes.
type Slack interface {
	// Thread is the thread's messages, oldest first, the parent included, with their metadata.
	Thread(ctx context.Context, channel, threadTS string) ([]Message, error)
	Post(ctx context.Context, channel, threadTS, text string, meta *Metadata) (ts string, err error)
	// PostCard posts text with Block Kit blocks: the approval card and the conversational design's
	// proposal message ride it, so Post's own wire bytes do not move.
	PostCard(ctx context.Context, channel, threadTS, text string, blocks json.RawMessage, meta *Metadata) (ts string, err error)
	// Update rewrites a message the App posted (chat.update), for a card that must turn inert. Slack
	// refuses a message the App does not own; the error carries Slack's code.
	Update(ctx context.Context, channel, ts, text string, blocks json.RawMessage) error
	Message(ctx context.Context, channel, ts string) (Message, error)
	// PostEphemeral is a message only user sees, in channel (and threadTS, when set).
	PostEphemeral(ctx context.Context, channel, user, threadTS, text string) error
	// UserName is the user's handle and display name, for the link page only; "" when Slack does
	// not say.
	UserName(ctx context.Context, user string) (handle, display string, err error)
}

// SlackClient is Slack's Web API over httpx. Token is read at use, so a rotated Secret applies.
type SlackClient struct {
	BaseURL string // https://slack.com/api/ when empty
	Token   func() string
	HC      *http.Client // httpx.New in production
	Logger  *slog.Logger // Slack's error code on every failed call; discarded when nil
}

func (c *SlackClient) log() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.New(slog.DiscardHandler)
}

type slackAnswer struct {
	OK               bool   `json:"ok"`
	Error            string `json:"error"`
	TS               string `json:"ts"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
	User *struct {
		Name    string `json:"name"`
		Profile struct {
			DisplayName string `json:"display_name"`
			RealName    string `json:"real_name"`
		} `json:"profile"`
	} `json:"user"`
	Messages []struct {
		TS       string `json:"ts"`
		ThreadTS string `json:"thread_ts"`
		User     string `json:"user"`
		Text     string `json:"text"`
		Metadata *struct {
			EventType    string          `json:"event_type"`
			EventPayload json.RawMessage `json:"event_payload"`
		} `json:"metadata"`
	} `json:"messages"`
}

// Post is chat.postMessage: no unfurling, the metadata attached when there is one.
func (c *SlackClient) Post(ctx context.Context, channel, threadTS, text string, meta *Metadata) (string, error) {
	body := map[string]any{"channel": channel, "text": text, "unfurl_links": false, "unfurl_media": false}
	if threadTS != "" {
		body["thread_ts"] = threadTS
	}
	if meta != nil {
		body["metadata"] = meta
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("chat.postMessage"), bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	a, err := c.do(req)
	if err != nil {
		return "", err
	}
	return a.TS, nil
}

// PostCard is chat.postMessage for a message that carries Block Kit blocks (§5, §6): the card's
// prose is its text, so a client without blocks still reads it. Separate from Post on purpose —
// Post's own call sites and the draft's wire bytes stay untouched.
func (c *SlackClient) PostCard(ctx context.Context, channel, threadTS, text string, blocks json.RawMessage, meta *Metadata) (string, error) {
	body := map[string]any{"channel": channel, "text": text, "unfurl_links": false, "unfurl_media": false}
	if threadTS != "" {
		body["thread_ts"] = threadTS
	}
	if len(blocks) > 0 {
		body["blocks"] = blocks
	}
	if meta != nil {
		body["metadata"] = meta
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("chat.postMessage"), bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	a, err := c.do(req)
	if err != nil {
		return "", err
	}
	return a.TS, nil
}

// Update is chat.update (chat:write already covers it): it rewrites a message the App posted, in
// place. No metadata — an update is the App's own change to its own message, never a new tag — and
// no unfurling, as on Post. blocks replaces the message's own; empty leaves them as they are.
func (c *SlackClient) Update(ctx context.Context, channel, ts, text string, blocks json.RawMessage) error {
	body := map[string]any{"channel": channel, "ts": ts, "text": text, "unfurl_links": false, "unfurl_media": false}
	if len(blocks) > 0 {
		body["blocks"] = blocks
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("chat.update"), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	_, err = c.do(req)
	return err
}

// PostEphemeral is chat.postEphemeral (chat:write): text that only user sees, never stored in the
// channel's history.
func (c *SlackClient) PostEphemeral(ctx context.Context, channel, user, threadTS, text string) error {
	body := map[string]any{"channel": channel, "user": user, "text": text, "unfurl_links": false, "unfurl_media": false}
	if threadTS != "" {
		body["thread_ts"] = threadTS
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("chat.postEphemeral"), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	_, err = c.do(req)
	return err
}

// UserName is users.info (users:read): the handle, unique in the workspace, and the profile's
// display name, else its real name (both set by the user). Without the scope Slack answers
// missing_scope, an error the caller shrugs off.
func (c *SlackClient) UserName(ctx context.Context, user string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("users.info")+"?"+url.Values{"user": {user}}.Encode(), nil)
	if err != nil {
		return "", "", err
	}
	a, err := c.do(req)
	if err != nil || a.User == nil {
		return "", "", err
	}
	display := strings.TrimSpace(a.User.Profile.DisplayName)
	if display == "" {
		display = strings.TrimSpace(a.User.Profile.RealName)
	}
	return strings.TrimSpace(a.User.Name), display, nil
}

// Message is conversations.replies for one ts (M9): the draft is a thread reply, which
// conversations.history never returns. The answer may also hold the thread's parent, so it keeps
// only the message whose ts is the one asked.
func (c *SlackClient) Message(ctx context.Context, channel, ts string) (Message, error) {
	q := url.Values{"channel": {channel}, "ts": {ts}, "limit": {"1"}, "inclusive": {"true"}, "include_all_metadata": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("conversations.replies")+"?"+q.Encode(), nil)
	if err != nil {
		return Message{}, err
	}
	a, err := c.do(req)
	if err != nil {
		return Message{}, err
	}
	for _, m := range a.Messages {
		if m.TS != ts {
			continue
		}
		out := Message{TS: m.TS, ThreadTS: m.ThreadTS, User: m.User, Text: m.Text}
		if m.Metadata != nil {
			out.Metadata = &Metadata{EventType: m.Metadata.EventType, EventPayload: m.Metadata.EventPayload}
		}
		return out, nil
	}
	return Message{}, errors.New("slackapp: slack: the message is not in the answer")
}

const (
	threadPageSize = 200
	threadMaxPages = 5
)

// ErrThreadTooLong is a thread with more pages than Thread follows: its latest draft cannot be
// known, so nothing is edited or filed from it.
var ErrThreadTooLong = errors.New("slackapp: slack: the thread is too long")

// Thread is conversations.replies for a thread, followed over a few pages at most.
func (c *SlackClient) Thread(ctx context.Context, channel, threadTS string) ([]Message, error) {
	var out []Message
	cursor := ""
	for range threadMaxPages {
		q := url.Values{"channel": {channel}, "ts": {threadTS}, "limit": {strconv.Itoa(threadPageSize)}, "include_all_metadata": {"true"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("conversations.replies")+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		a, err := c.do(req)
		if err != nil {
			return nil, err
		}
		for _, m := range a.Messages {
			msg := Message{TS: m.TS, ThreadTS: m.ThreadTS, User: m.User, Text: m.Text}
			if m.Metadata != nil {
				msg.Metadata = &Metadata{EventType: m.Metadata.EventType, EventPayload: m.Metadata.EventPayload}
			}
			out = append(out, msg)
		}
		if cursor = a.ResponseMetadata.NextCursor; cursor == "" {
			return out, nil
		}
	}
	return nil, ErrThreadTooLong
}

func (c *SlackClient) url(method string) string {
	base := c.BaseURL
	if base == "" {
		base = slackAPI
	}
	return strings.TrimSuffix(base, "/") + "/" + method
}

// do sends the request as the bot and decodes the answer; a non-ok answer is an error whose text
// is Slack's own code, never the token.
func (c *SlackClient) do(req *http.Request) (slackAnswer, error) {
	req.Header.Set("Authorization", "Bearer "+c.Token())
	hc := c.HC
	if hc == nil {
		hc = httpx.New(10*time.Second, nil)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return slackAnswer{}, fmt.Errorf("slackapp: slack: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxSlackReply)
	if err != nil {
		return slackAnswer{}, fmt.Errorf("slackapp: slack reply: %w", err)
	}
	var a slackAnswer
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, fmt.Errorf("slackapp: slack answered %d: %w", resp.StatusCode, err)
	}
	if !a.OK {
		c.log().Warn("slack call failed", "method", path.Base(req.URL.Path), "status", resp.StatusCode, "slack_error", a.Error)
		return a, fmt.Errorf("slackapp: slack: %s", a.Error)
	}
	return a, nil
}
