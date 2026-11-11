// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/intent"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/store"
)

// intent_classify is the broker's one read-only classification of a Slack thread reply (natural-language
// intents, SI3): the reply alone and the thread's phase as one closed word go to one bounded model call,
// and a closed label with the span of the reply it acts on comes back. It reads no room, holds no tool,
// decides nothing and stores a reply only for a channel the App says opted in (SI19).
//
// The order below is the security property: arguments, then who is asking, then whether that Slack
// user is linked, then the caps, then the one call. Nothing reaches the model before every check it can
// fail has been made.

// The closed refusals, one per guard: the App branches on the word and never reads a sentence.
const (
	intentInvalidArguments = "invalid_arguments"
	intentNotPermitted     = "not_permitted"
	intentNotLinked        = "not_linked"
	intentLinkUnverified   = "link_unverified"
	intentCapped           = "capped"
	intentUnavailable      = "unavailable"
)

// maxIntentReply bounds the argument: Slack's own messages are far under it, and the model sees at most
// MaxInputChars of it.
const maxIntentReply = 64 << 10

var slackIntentChannelRE = slackChannelRE

// IntentModel is the one call the tool makes; *intent.Model has it.
type IntentModel interface {
	Classify(ctx context.Context, reply, phase string) ([]byte, intent.Usage, error)
}

// IntentSamples is the opt-in reply store; *store.Store has it.
type IntentSamples interface {
	InsertIntentSample(ctx context.Context, in store.IntentSample, retentionDays int) (int64, error)
}

// IntentClassifier is human.slackIntents: who may call, for which workspace, the model, the link
// tables the author is resolved through, the caps and the bounds (SI3, SI12, SI13, SI18, SI19). Nil on
// the server is the switch off: the tool is on no list.
type IntentClassifier struct {
	// Principal is the one sponsored principal that may call, system:<name>; TeamID its workspace.
	Principal string
	TeamID    string
	Model     IntentModel
	// Alias is the model's alias, logged with each classification.
	Alias string
	// Links resolves a Slack user to the account it is linked to (SI12). Nil refuses every call.
	Links SlackLinkFinder
	// Samples stores a reply of a channel that opted in. Nil stores nothing. Redactor runs over the
	// text first, as every text the platform stores does.
	Samples  IntentSamples
	Redactor Redactor
	// MinConfidence, ConfirmTTL and MaxInputChars are SI18's thresholds; DailyCalls and PerUserPerHour
	// SI13's caps; RetentionDays SI19's.
	MinConfidence  float64
	ConfirmTTL     time.Duration
	MaxInputChars  int
	DailyCalls     int
	PerUserPerHour int
	RetentionDays  int
	// Now is the clock the caps and the expiry read; nil means time.Now.
	Now func() time.Time

	mu    sync.Mutex
	day   string
	calls int
	hours map[string]hourCount
}

// hourCount is one user's calls in one hour window (Unix hours).
type hourCount struct {
	hour int64
	n    int
}

func (c *IntentClassifier) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// take spends one call of the App's daily budget and one of the user's hour, or neither: both caps
// are exact at the boundary (the Nth is allowed, the N+1th is not), a refusal spends nothing, and the
// day rolls over at 00:00 UTC.
func (c *IntentClassifier) take(user string) bool {
	if c.DailyCalls < 1 || c.PerUserPerHour < 1 {
		return false
	}
	now := c.now().UTC()
	day, hour := now.Format("2006-01-02"), now.Unix()/3600
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.day != day {
		c.day, c.calls = day, 0
	}
	if c.hours == nil {
		c.hours = map[string]hourCount{}
	}
	if len(c.hours) > 4096 { // stale windows are dead weight: a user is a key only for their hour
		for k, v := range c.hours {
			if v.hour != hour {
				delete(c.hours, k)
			}
		}
	}
	u := c.hours[user]
	if u.hour != hour {
		u = hourCount{hour: hour}
	}
	if c.calls >= c.DailyCalls || u.n >= c.PerUserPerHour {
		return false
	}
	c.calls++
	u.n++
	c.hours[user] = u
	return true
}

// intentArgs is intent_classify's closed argument set.
type intentArgs struct {
	Team    string `json:"team"`
	User    string `json:"user"`
	Channel string `json:"channel"`
	Reply   string `json:"reply"`
	Phase   string `json:"phase"`
	Collect bool   `json:"collect"`
}

func (a intentArgs) shaped() bool {
	return slackTeamRE.MatchString(a.Team) && slackUserRE.MatchString(a.User) && slackIntentChannelRE.MatchString(a.Channel) &&
		strings.TrimSpace(a.Reply) != "" && len(a.Reply) <= maxIntentReply && utf8.ValidString(a.Reply) && intent.ValidPhase(a.Phase)
}

// intentAnswer is the tool's structuredContent: the label, the span a write intent acts on, its hash,
// the model's confidence and when a confirm for it expires. SampleID is set when the reply was stored.
type intentAnswer struct {
	Intent     intent.Intent `json:"intent"`
	Text       string        `json:"text"`
	TextHash   string        `json:"textHash"`
	Confidence float64       `json:"confidence"`
	Expires    string        `json:"expires"`
	SampleID   int64         `json:"sampleId,omitempty"`
}

// intentClassifySchema is intent_classify's arguments, closed.
var intentClassifySchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["team","user","channel","reply","phase"],` +
	`"properties":{"team":{"type":"string","pattern":"^T[A-Z0-9]{2,20}$"},"user":{"type":"string","pattern":"^[UW][A-Z0-9]{2,20}$"},` +
	`"channel":{"type":"string","pattern":"^[CG][A-Z0-9]{2,20}$"},"reply":{"type":"string","minLength":1,"maxLength":65536},` +
	`"phase":{"type":"string","enum":["queued","running","waiting-you","waiting-other","waiting-pr","waiting","done","failed","stopped","unknown"]},` +
	`"collect":{"type":"boolean","description":"true only for a channel that opted in to collecting replies"}}}`)

// intentClassifyOutput is the answer, declared.
var intentClassifyOutput = json.RawMessage(`{"type":"object","required":["intent","text","textHash","confidence","expires"],` +
	`"properties":{"intent":{"type":"string","enum":["status","needs","changes","pr","steer","amend","stop","retry","unknown"]},` +
	`"text":{"type":"string"},"textHash":{"type":"string"},"confidence":{"type":"number"},"expires":{"type":"string"},"sampleId":{"type":"integer"}}}`)

// intentClassifyTool lists intent_classify, for the one principal that may call it.
func (s *Server) intentClassifyTool() mcpTool {
	return mcpTool{name: mcpwire.IntentClassify, call: s.intentClassify,
		description: "Classify one reply of a filed Slack thread into status, needs, changes, pr, steer, amend, stop, retry or unknown, " +
			"with the span of the reply a steer or an amend acts on. The reply alone and the thread's phase go to one bounded call; it reads " +
			"no room, holds no tool and decides nothing. The answer is a label, never an action: a write intent only draws a confirm the reply's author presses. " +
			"Refusals are the words not_linked, link_unverified, capped and invalid_arguments.",
		schema: intentClassifySchema, output: intentClassifyOutput}
}

// intentClassify is the tool, in the guard's order.
func (s *Server) intentClassify(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
	c := s.Intents
	var a intentArgs
	if mcpwire.DecodeArgs(args, &a) != nil || !a.shaped() {
		return nil, toolError(intentInvalidArguments)
	}
	if !p.Sponsored || p.ID != c.Principal {
		return nil, toolError(intentNotPermitted)
	}
	// A peer workspace's user is nobody this broker has linked.
	if a.Team != c.TeamID || c.Links == nil {
		return nil, toolError(intentNotLinked)
	}
	linkCtx, cancel := context.WithTimeout(ctx, accessWait)
	_, found, err := c.Links.SlackLink(linkCtx, a.Team, a.User)
	cancel()
	switch {
	case err != nil:
		s.log().Warn("intent_classify: slack link unreadable", "err", err)
		return nil, toolError(intentLinkUnverified)
	case !found:
		return nil, toolError(intentNotLinked)
	}
	requester := "slack:" + a.Team + "/" + a.User
	if !c.take(requester) {
		s.intentCounted(ctx, string(intent.OutcomeCapped), 0)
		return nil, toolError(intentCapped)
	}

	reply := a.Reply
	if utf8.RuneCountInString(reply) > c.MaxInputChars {
		reply = string([]rune(reply)[:c.MaxInputChars])
	}
	start := time.Now()
	raw, _, err := c.Model.Classify(ctx, reply, a.Phase)
	took := time.Since(start)
	var (
		res     intent.Result
		outcome intent.Outcome
	)
	switch {
	case errors.Is(err, intent.ErrTimeout):
		s.intentCounted(ctx, string(intent.OutcomeTimeout), took)
		s.intentLogged(a, reply, took, c.Alias, string(intent.OutcomeTimeout), nil)
		return nil, toolError(intentUnavailable)
	case errors.Is(err, intent.ErrInvalid):
		res, outcome = intent.Result{Intent: intent.Unknown}, intent.OutcomeInvalidOutput
	case err != nil:
		s.intentCounted(ctx, string(intent.OutcomeError), took)
		s.intentLogged(a, reply, took, c.Alias, string(intent.OutcomeError), nil)
		return nil, toolError(intentUnavailable)
	default:
		res, outcome = intent.Parse(raw, reply, c.MinConfidence)
	}
	s.intentCounted(ctx, string(outcome), took)
	s.intentLogged(a, reply, took, c.Alias, string(outcome), &res)

	ans := intentAnswer{Intent: res.Intent, Text: res.Text, TextHash: intent.TextHash(res.Text), Confidence: res.Confidence,
		Expires: c.now().UTC().Add(c.ConfirmTTL).Format(time.RFC3339)}
	if a.Collect && c.Samples != nil {
		ans.SampleID = s.collectSample(ctx, a, res)
	}
	return ans, nil
}

// collectSample stores the reply of an opted-in channel with its label, redacted first, and returns
// the sample's id, 0 when it could not be stored: the classification is the answer and a failed copy
// is a warning, never a refusal.
func (s *Server) collectSample(ctx context.Context, a intentArgs, res intent.Result) int64 {
	c := s.Intents
	text, err := c.redactedText(ctx, a.Reply)
	if err != nil {
		s.log().Warn("intent_classify: sample not stored: redaction failed", "channel", a.Channel)
		return 0
	}
	id, err := c.Samples.InsertIntentSample(ctx, store.IntentSample{Channel: a.Channel, Reply: text,
		Intent: string(res.Intent), Confidence: res.Confidence}, c.RetentionDays)
	if err != nil {
		s.log().Warn("intent_classify: sample not stored", "channel", a.Channel, "err", err)
		return 0
	}
	return id
}

// redactedText is the reply through the redactor; no redactor refuses, since nothing is stored
// unredacted.
func (c *IntentClassifier) redactedText(ctx context.Context, text string) (string, error) {
	if c.Redactor == nil {
		return "", errors.New("no redactor")
	}
	raw, _, err := c.Redactor.Payload(ctx, envelope.Must(text))
	if err != nil {
		return "", err
	}
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out, nil
}

func (s *Server) intentCounted(ctx context.Context, outcome string, took time.Duration) {
	if s.Metrics == nil {
		return
	}
	s.Metrics.IntentClassify(ctx, outcome)
	if took > 0 {
		s.Metrics.IntentClassifySeconds(ctx, took)
	}
}

// intentLogged is the one structured line a classification leaves: the channel, the label, the
// confidence, the latency, the alias, and the reply's sha256 and length. Never the reply.
func (s *Server) intentLogged(a intentArgs, reply string, took time.Duration, alias, outcome string, res *intent.Result) {
	sum := sha256.Sum256([]byte(a.Reply))
	line := []any{"channel", a.Channel, "phase", a.Phase, "outcome", outcome, "latency", took, "model", alias,
		"sha256", hex.EncodeToString(sum[:]), "length", utf8.RuneCountInString(a.Reply), "sent", utf8.RuneCountInString(reply)}
	if res != nil {
		line = append(line, "intent", string(res.Intent), "confidence", res.Confidence)
	}
	s.log().Log(context.Background(), slog.LevelInfo, "intent classified", line...)
}
