// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/summary"
	"github.com/Smana/maku/internal/webhook"
)

const (
	draftEventType = "factory_draft"
	// filedEventType marks the App's "Filed" line: a draft whose thread has one is no longer edited.
	filedEventType = "factory_filed"
	// maxRevisions bounds the drafts one thread can hold (each edit posts a new one).
	maxRevisions  = 20
	handleTimeout = 30 * time.Second
	// maxMetadata bounds the draft's metadata JSON, well under Slack's message-metadata cap.
	maxMetadata    = 8 << 10
	maxWebhookBody = 64 << 10
	seenCap        = 4096
	maxShown       = 300
	// eventWorkers handle Slack events after their 200; eventQueue events wait for one. Past
	// both, an event answers 503 and Slack retries it (F-10).
	eventWorkers = 8
	eventQueue   = 64
)

// The ids that reach a ref, a key or a URL are checked against Slack's and the factory's shapes
// first; anything else is ignored rather than escaped.
var (
	channelRE = regexp.MustCompile(`^[CG][A-Z0-9]{2,20}$`)
	teamRE    = regexp.MustCompile(`^T[A-Z0-9]{2,20}$`)
	userRE    = regexp.MustCompile(`^[UW][A-Z0-9]{2,20}$`)
	tsRE      = regexp.MustCompile(`^[0-9]{1,12}\.[0-9]{1,8}$`)
	roomRE    = regexp.MustCompile(`^[a-z2-7]{8}$`)
)

// BrokerAPI is what the flows call on the broker; Broker has it, and the test fakes it.
type BrokerAPI interface {
	IssueDraft(ctx context.Context, d Draft) (Draft, error)
	RunCreate(ctx context.Context, c RunCreate) (RunResult, error)
	SlackLinkCode(ctx context.Context, team, user, handle, name string) (string, error)
	SlackUnlink(ctx context.Context, team, user string) (bool, error)
	// IntakePlan is the governed planner (CD18): a mention's words become the plan/v1 the proposal
	// message carries. It files nothing, and ErrUnknownTool is a broker without the tool.
	IntakePlan(ctx context.Context, in IntakeRequest) (IntakeResult, error)
	// Decide records one press of an approval's card, on the broker's decide route (§1): the only
	// thing the App decides, and only for forge.push.
	Decide(ctx context.Context, d Decision) (DecisionResult, error)
	// The three reads a filed thread's questions make (CD25): the room's summary, its changes and its
	// needs, each one sponsored call for the room the thread's Filed line named.
	RoomSummary(ctx context.Context, room string) (summary.Summary, error)
	RoomChanges(ctx context.Context, room string) (roomview.ChangeSet, error)
	NeedsList(ctx context.Context, room string) ([]summary.Need, error)
}

// Recorder counts requests for factory_slack_requests_total{route, outcome}, the proposal actions
// (proposal_updates_total{action}) and the answers a filed proposal's thread gives
// (thread_answers_total{question}). The two last are closed vocabularies the App passes: the
// instruments' names and their serving are metrics' (CD7, CD25).
type Recorder interface {
	Request(ctx context.Context, route, outcome string)
	ProposalUpdate(ctx context.Context, action string)
	ThreadAnswer(ctx context.Context, question string)
}

// The six actions maku_slack_proposal_updates_total{action} is labelled with (CD7): a closed set, one
// word per action the App takes on a proposal. The flows pass these and never a refusal code, a hash,
// a repository or a person's word, so a series stays one of six.
const (
	countMention     = "mention"     // a mention reached the planner
	countReply       = "reply"       // the author's reply reached the re-plan
	countConfirm     = "confirm"     // a press was taken up as a Confirm
	countCancel      = "cancel"      // a press was taken up as a Cancel
	countRepost      = "repost"      // an edit Slack refused was reposted beside the message it replaced
	countUnrewritten = "unrewritten" // the Filed line could not be written in place and went to the thread
)

// App is the flows: a mention drafts, the draft's author amends it with replies in its thread, a
// maintainer's check mark files, the broker's webhooks tell the thread, and — for forge.push only,
// where the channel's approvals flag is on — a card's button decides. A thread reply is read only
// to amend an unfiled draft; none is ever a command (invariant 8) and no reply is ever a decision.
type App struct {
	Slack       Slack
	Broker      BrokerAPI
	Threads     Threads
	Verifier    Verifier
	WebhookKeys func() ([][]byte, error)
	TeamID      string
	BotUserID   string
	RoomsURL    string
	Channels    map[string]Channel
	Proposals   Proposals    // the proposal flow's switch (CD7): on, a mention is planned and posted as one proposal
	Async       func(func()) // runs an event's handling after the 200 (tests); nil means the App's workers
	Now         func() time.Time
	Metrics     Recorder
	Logger      *slog.Logger

	events, deliveries seen       // event_id and webhook-id
	answered           seen       // the ✅ refusals said, by draft, user and reason: each is said once
	retried            throttle   // the ✅ passing failures said, by draft, user and reason: once per retryEvery
	editMu             sync.Mutex // one draft edit at a time, so two quick replies do not branch the draft

	poolOnce sync.Once
	poolMu   sync.Mutex  // guards draining and every send on queue
	draining bool        // Wait has run: queue is closed
	queue    chan func() // eventQueue slots, fed by enqueue, drained by eventWorkers
	workers  sync.WaitGroup
	pending  atomic.Int64 // events taken by a worker's queue and not yet finished
}

// seen is a bounded set of ids: Slack's event_id and a delivery's webhook-id (R26).
type seen struct {
	mu    sync.Mutex
	order []string
	set   map[string]struct{}
}

// add reports whether id is new.
func (s *seen) add(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.set[id]; ok {
		return false
	}
	if s.set == nil {
		s.set = map[string]struct{}{}
	}
	if len(s.order) >= seenCap {
		delete(s.set, s.order[0])
		s.order = s.order[1:]
	}
	s.set[id] = struct{}{}
	s.order = append(s.order, id)
	return true
}

// retryEvery is how often one passing failure is said again for a draft, user and reason.
const retryEvery = 5 * time.Minute

// throttle is a bounded map of when each key was last allowed.
type throttle struct {
	mu    sync.Mutex
	order []string
	at    map[string]time.Time
}

// allow reports whether key was not allowed within retryEvery of now, and records now if so.
func (t *throttle) allow(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.at[key]; ok && now.Sub(last) < retryEvery {
		return false
	}
	if t.at == nil {
		t.at = map[string]time.Time{}
	}
	if _, ok := t.at[key]; !ok {
		if len(t.order) >= seenCap {
			delete(t.at, t.order[0])
			t.order = t.order[1:]
		}
		t.order = append(t.order, key)
	}
	t.at[key] = now
	return true
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (s *seen) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.set, id)
	s.order = slices.DeleteFunc(s.order, func(o string) bool { return o == id })
}

func draftHash(repo, title, body string) string {
	sum := sha256.Sum256([]byte(repo + "\x00" + title + "\x00" + body))
	return hex.EncodeToString(sum[:])
}

func (a *App) count(ctx context.Context, route, outcome string) {
	if a.Metrics != nil {
		a.Metrics.Request(ctx, route, outcome)
	}
}

// proposalUpdate counts one proposal action (CD7), with one of the six closed values above. A path that
// answered nothing counts nothing, which is why the call sites are where an action is actually taken —
// the mention's flow, the author's re-plan, the press taken up, the repost a refusal forced — and never
// a dedup hit, an ignored message or a value the family could not read.
func (a *App) proposalUpdate(ctx context.Context, action string) {
	if a.Metrics != nil {
		a.Metrics.ProposalUpdate(ctx, action)
	}
}

func (a *App) log() *slog.Logger {
	if a.Logger != nil {
		return a.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// enqueue hands f to a worker and reports whether it was taken: false when eventQueue events
// already wait, or Wait has run. Async, when set, runs f itself.
func (a *App) enqueue(f func()) bool {
	if a.Async != nil {
		a.Async(f)
		return true
	}
	a.poolOnce.Do(a.startWorkers)
	a.poolMu.Lock()
	defer a.poolMu.Unlock()
	if a.draining {
		return false
	}
	a.pending.Add(1)
	select {
	case a.queue <- f:
		return true
	default:
		a.pending.Add(-1)
		return false
	}
}

// startWorkers starts the eventWorkers that drain queue until Wait closes it.
func (a *App) startWorkers() {
	a.queue = make(chan func(), eventQueue)
	for range eventWorkers {
		a.workers.Go(func() {
			for f := range a.queue {
				f()
				a.pending.Add(-1)
			}
		})
	}
}

// Wait stops taking events and returns once every queued and running one has finished, or
// ctx ends first: then it logs how many were left and returns the context's error, wrapped.
// Each of those was answered 200, so Slack will not resend it (F-10); the bound is the caller's,
// since 72 events at handleTimeout over eight workers can outlast a pod's grace period. Safe to
// call more than once.
func (a *App) Wait(ctx context.Context) error {
	a.poolOnce.Do(a.startWorkers)
	a.poolMu.Lock()
	if !a.draining {
		a.draining = true
		close(a.queue)
	}
	a.poolMu.Unlock()
	done := make(chan struct{})
	go func() { a.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		n := a.pending.Load()
		a.log().ErrorContext(context.WithoutCancel(ctx), "slack events unfinished at shutdown", "events", n)
		return fmt.Errorf("wait for %d slack events: %w", n, ctx.Err())
	}
}

// slackText is user text made inert in a Slack message: Slack's three escapes, so a <!channel>,
// a <@user> or a link in a draft never fires, and no fence to close a code block early.
func slackText(s string) string {
	s = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	return strings.ReplaceAll(s, "```", "'''")
}

// shown is a refusal as the thread shows it: inert and short.
func shown(s string) string {
	s = strings.TrimPrefix(s, "slackapp: ") // the package's internal prefix is not for people
	if r := []rune(s); len(r) > maxShown {
		s = string(r[:maxShown]) + "…"
	}
	return slackText(s)
}

type slackEnvelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	TeamID    string          `json:"team_id"`
	EventID   string          `json:"event_id"`
	Event     json.RawMessage `json:"event"`
}

type slackEvent struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	Channel  string `json:"channel"`
	User     string `json:"user"`
	BotID    string `json:"bot_id"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
	Reaction string `json:"reaction"`
	Item     struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	} `json:"item"`
}

// Events is the events listener's handler: the signature first, nothing read before it. Two body
// shapes arrive at this one door (D11, §6): Slack's JSON events and its form-encoded button presses,
// told apart by content type after the same verification.
func (a *App) Events() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		// The header alone names the route, so a refusal before the body is read is counted where
		// the request was going: a press that does not verify is an interactions refusal.
		press := isForm(r.Header.Get("Content-Type"))
		route := "events"
		if press {
			route = "interactions"
		}
		body, err := a.Verifier.Body(r)
		if err != nil {
			a.count(ctx, route, "bad_signature")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if press {
			a.handleInteraction(w, r, body) // §6: the second body shape, one door and one lock
			return
		}
		var env slackEnvelope
		if json.Unmarshal(body, &env) != nil {
			a.count(ctx, "events", "bad_request")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch env.Type {
		case "url_verification":
			a.count(ctx, "events", "ok")
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, env.Challenge)
		case "event_callback":
			if env.EventID == "" || !a.events.add(env.EventID) {
				a.count(ctx, "events", "duplicate")
				w.WriteHeader(http.StatusOK)
				return
			}
			if !a.enqueue(func() {
				hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handleTimeout)
				defer cancel()
				a.count(hctx, "events", a.handle(hctx, env))
			}) {
				// Full or draining: Slack retries a non-2xx, so the event is not lost, and its id
				// is forgotten so that retry is handled rather than read as a duplicate.
				a.events.forget(env.EventID)
				a.count(ctx, "events", "busy")
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			a.count(ctx, "events", "ignored")
			w.WriteHeader(http.StatusOK)
		}
	})
}

// isForm reports whether the request carries Slack's interactivity body shape (§6 step 2): the one
// content type its button presses come in. Anything else is the JSON events path, byte-for-byte.
func isForm(contentType string) bool {
	ct, _, err := mime.ParseMediaType(contentType)
	return err == nil && ct == "application/x-www-form-urlencoded"
}

// handle runs one event and names its outcome for the counter.
func (a *App) handle(ctx context.Context, env slackEnvelope) string {
	var ev slackEvent
	// One workspace: a Slack Connect peer's events carry its own team_id. A bot's event (bot_id)
	// is never a person's mention or check mark.
	if json.Unmarshal(env.Event, &ev) != nil || !teamRE.MatchString(env.TeamID) || env.TeamID != a.TeamID || ev.BotID != "" {
		return "ignored"
	}
	switch {
	case ev.Type == "app_mention":
		return a.mention(ctx, ev)
	case ev.Type == "reaction_added" && ev.Reaction == "white_check_mark" && ev.Item.Type == "message":
		return a.reaction(ctx, env.TeamID, ev)
	case ev.Type == "message":
		return a.reply(ctx, ev)
	}
	return "ignored"
}

func (a *App) mention(ctx context.Context, ev slackEvent) string {
	ch, ok := a.Channels[ev.Channel]
	thread := ev.ThreadTS
	if thread == "" {
		thread = ev.TS
	}
	if !ok || ev.User == a.BotUserID || !userRE.MatchString(ev.User) || !tsRE.MatchString(thread) {
		return "ignored"
	}
	refuse := func(why string) string {
		if _, err := a.Slack.Post(ctx, ev.Channel, thread, "Draft refused: "+shown(why), nil); err != nil {
			a.log().Warn("slack post failed", "channel", ev.Channel, "err", err)
			return "error"
		}
		return "refused"
	}
	switch command(ev.Text, a.BotUserID) {
	case "link":
		return a.link(ctx, ev, thread)
	case "unlink":
		return a.unlink(ctx, ev, thread)
	}
	// CD1, §the Slack journey: with proposals on, a mention in plain words is planned by the broker and
	// posted as one proposal. The App parses no repository line and guesses no repository: the person's
	// own words are the planner's input (commandless), which is where the 16 KiB bound is measured.
	if a.Proposals.Enabled {
		code := a.propose(ctx, ev, ch, thread, commandless(ev.Text, a.BotUserID), 1)
		a.proposalUpdate(ctx, countMention) // one mention, one action, whatever the planner answered
		return code
	}
	d, err := ParseMention(ev.Text, a.BotUserID, ch.Repositories)
	if err != nil {
		return refuse(err.Error())
	}
	if !slices.Contains(ch.Repositories, d.Repo) {
		return refuse("this channel does not serve that repository")
	}
	d, err = a.Broker.IssueDraft(ctx, d)
	if err != nil {
		if r, ok := err.(Refused); ok { //nolint:errorlint // Refused is returned bare, never wrapped
			return refuse(string(r))
		}
		a.log().Warn("issue_draft failed", "err", err)
		return refuse("the draft could not be checked, try again")
	}
	return a.postDraft(ctx, ev.Channel, thread, d, ev.User, 1)
}

// command is the mention's one-word command, "link" or "unlink", or "" for a draft.
func command(text, bot string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), "<@"+bot+">")
	if !ok {
		return ""
	}
	switch c := strings.ToLower(strings.TrimSpace(rest)); c {
	case "link", "unlink":
		return c
	}
	return ""
}

// whisper answers the user alone, as a direct message from the bot (chat.postMessage with the
// user's id as channel: chat:write only, no im:write): an ephemeral inside a thread is seen only
// while that thread is open and vanishes on reload, so a link code would be lost. The thread gets
// a short pointer, never the text. When the DM fails, an ephemeral in the channel itself (no
// thread_ts) is the fallback.
func (a *App) whisper(ctx context.Context, ev slackEvent, thread, text, outcome string) string {
	if _, err := a.Slack.Post(ctx, ev.User, "", text, nil); err != nil {
		a.log().Warn("slack direct message failed, answering in the channel", "user", ev.User, "err", err)
		if err := a.Slack.PostEphemeral(ctx, ev.Channel, ev.User, "", text); err != nil {
			a.log().Warn("slack ephemeral post failed", "channel", ev.Channel, "err", err)
			return "error"
		}
		return outcome
	}
	if _, err := a.Slack.Post(ctx, ev.Channel, thread, "<@"+ev.User+"> I've sent you the answer in a direct message.", nil); err != nil {
		a.log().Warn("slack pointer post failed", "channel", ev.Channel, "err", err)
	}
	return outcome
}

const linkOff = "Self-serve linking is not turned on here: ask an operator to add you to the factory's `onBehalfOf.slack`."

// link answers `@factory link` with a one-time link to the rooms page, for this Slack user only.
// The name is the page's, display only; without users:read it is empty and the page shows the id.
func (a *App) link(ctx context.Context, ev slackEvent, thread string) string {
	handle, name, err := a.Slack.UserName(ctx, ev.User)
	if err != nil {
		a.log().Info("slack user name unread", "err", err)
	}
	u, err := a.Broker.SlackLinkCode(ctx, a.TeamID, ev.User, handle, name)
	var r Refused
	switch {
	case errors.Is(err, ErrUnknownTool):
		return a.whisper(ctx, ev, thread, linkOff, "refused")
	case errors.As(err, &r):
		return a.whisper(ctx, ev, thread, "Link refused: "+shown(string(r)), "refused")
	case err != nil:
		a.log().Warn("slack_link_code failed", "err", err)
		return a.whisper(ctx, ev, thread, "The link could not be made, mention me with `link` again.", "error")
	}
	return a.whisper(ctx, ev, thread, "Open the link page: <"+u+"|link your Slack user>\n"+u+"\n"+
		"It works once, within 10 minutes, after you sign in. Keep it to yourself and never send it to anyone: "+
		"whoever confirms it is who your :white_check_mark: files as.", "link")
}

// unlink answers `@factory unlink`: this Slack user's self-serve link goes.
func (a *App) unlink(ctx context.Context, ev slackEvent, thread string) string {
	gone, err := a.Broker.SlackUnlink(ctx, a.TeamID, ev.User)
	var r Refused
	switch {
	case errors.Is(err, ErrUnknownTool):
		return a.whisper(ctx, ev, thread, linkOff, "refused")
	case errors.As(err, &r):
		return a.whisper(ctx, ev, thread, "Unlink refused: "+shown(string(r)), "refused")
	case err != nil:
		a.log().Warn("slack_unlink failed", "err", err)
		return a.whisper(ctx, ev, thread, "The unlink could not be made, mention me with `unlink` again.", "error")
	case !gone:
		return a.whisper(ctx, ev, thread, "Nothing to unlink: your Slack user has no self-serve link. "+
			"A link in the factory's config stays until an operator removes it.", "ignored")
	}
	return a.whisper(ctx, ev, thread, "Unlinked: your :white_check_mark: no longer files through your rooms account.", "unlinked")
}

// postDraft posts the draft as Slack shows it, its checked text and hash in the message metadata.
func (a *App) postDraft(ctx context.Context, channel, thread string, d Draft, author string, rev int) string {
	// The draft is a tag plus its own JSON (§5), so a card and a draft share the message's shape; the
	// bytes the metadata carries are the ones it always carried.
	payload, perr := json.Marshal(DraftPayload{Repo: d.Repo, Title: d.Title, Body: d.Body,
		SHA256: draftHash(d.Repo, d.Title, d.Body), Author: author, Rev: rev})
	meta := &Metadata{EventType: draftEventType, EventPayload: payload}
	if mj, err := json.Marshal(meta); perr != nil || err != nil || len(mj) > maxMetadata {
		// Slack would refuse the metadata: say so, with none of the draft's text.
		if _, err := a.Slack.Post(ctx, channel, thread, "Draft not posted: it is too long to file from Slack; file it from the web UI.", nil); err != nil {
			a.log().Warn("slack post failed", "channel", channel, "err", err)
			return "error"
		}
		return "too_large"
	}
	if _, err := a.Slack.Post(ctx, channel, thread, slackDraft(d), meta); err != nil {
		a.log().Warn("slack post failed", "channel", channel, "err", err)
		return "error"
	}
	if rev > 1 {
		return "edited"
	}
	return "drafted"
}

// slackDraft is the draft in Slack's mrkdwn: an intro line, then each section's bold label on its
// own line with its text below. Headings are not mrkdwn, so the issue body's `##` stays on GitHub.
// A body that is not the template's is shown whole in a code block.
func slackDraft(d Draft) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Draft for %s: *%s*\n\n", slackText(d.Repo), slackText(d.Title))
	if secs, ok := parseBody(d.Body); ok {
		for i, label := range [3]string{"What is wrong", "What it should be", "Acceptance check"} {
			fmt.Fprintf(&b, "*%s*\n%s\n\n", label, slackText(secs[i]))
		}
	} else {
		fmt.Fprintf(&b, "```%s```\n\n", slackText(d.Body))
	}
	b.WriteString("_Reply in this thread to change it (`Title:`, `Should:`, `Acceptance:`, or any text to add to what is wrong), " +
		"then a maintainer replies :white_check_mark: or reacts :white_check_mark: to file it and start the work._")
	return b.String()
}

// threadDraft is the thread's latest draft the App posted (hash holding) and whether a "Filed" line
// of its own is in the thread.
func (a *App) threadDraft(msgs []Message) (latest *Message, filed bool) {
	for i := range msgs {
		m := &msgs[i]
		if m.User == "" || m.User != a.BotUserID || m.Metadata == nil {
			continue
		}
		switch m.Metadata.EventType {
		case filedEventType:
			filed = true
		case draftEventType:
			if p, ok := m.Metadata.draftPayload(); ok && p.SHA256 == draftHash(p.Repo, p.Title, p.Body) {
				latest = m
			}
		}
	}
	return latest, filed
}

// isCheckMark is a reply that is only a ✅ or a heavy check mark, as an emoji or its Slack code.
func isCheckMark(text string) bool {
	switch strings.TrimSpace(text) {
	case "✅", "✔️", "✔", ":white_check_mark:", ":heavy_check_mark:":
		return true
	}
	return false
}

// checkMarkReply files the thread's latest draft for the replier, like a ✅ reaction on it. Who may
// file is the broker's call (the linked maintainer check), as for the reaction. A thread with no
// draft is silent: the reply is about nothing the App posted.
func (a *App) checkMarkReply(ctx context.Context, ev slackEvent) string {
	msgs, err := a.Slack.Thread(ctx, ev.Channel, ev.ThreadTS)
	if err != nil {
		a.log().Warn("slack thread fetch failed", "channel", ev.Channel, "err", err)
		if a.retried.allow(ev.ThreadTS+"/"+ev.User+"/slack", a.now()) {
			if _, perr := a.Slack.Post(ctx, ev.Channel, ev.ThreadTS, "Not filed: Slack could not be read just now; reply :white_check_mark: again to retry.", nil); perr != nil {
				a.log().Warn("slack post failed", "channel", ev.Channel, "err", perr)
			}
		}
		return "error"
	}
	latest, _ := a.threadDraft(msgs)
	if latest == nil {
		// A thread whose message is a proposal: the same fallback, typed. The filing path re-checks the
		// payload and the thread, so finding it here is the only thing this adds.
		if prop, closed := a.threadProposal(msgs); prop != nil && !closed {
			return a.file(ctx, a.TeamID, ev.Channel, prop.TS, ev.User, true)
		}
		return "ignored"
	}
	return a.file(ctx, a.TeamID, ev.Channel, latest.TS, ev.User, true)
}

// reply amends the thread's pending draft with a reply from the author of the mention. Anyone
// else's reply, a reply on a filed draft or in a thread without a draft is ignored without a word.
//
// With proposals on the thread's message is a proposal, not a draft: the reply is a re-plan, and the
// same message becomes its next revision (replan). One thread on this App is one or the other — a
// proposal is what a mention posts with the switch on — so the two branches never contend for one
// thread, and the draft path below is whatever it always was. A thread whose proposal was filed or
// cancelled answers the five questions instead (answer, CD25).
func (a *App) reply(ctx context.Context, ev slackEvent) string {
	ch, ok := a.Channels[ev.Channel]
	if !ok || ev.Subtype != "" || ev.ThreadTS == "" || ev.ThreadTS == ev.TS || ev.User == a.BotUserID ||
		!channelRE.MatchString(ev.Channel) || !userRE.MatchString(ev.User) || !tsRE.MatchString(ev.ThreadTS) ||
		strings.TrimSpace(ev.Text) == "" || strings.Contains(ev.Text, "<@"+a.BotUserID+">") {
		return "ignored" // a mention in a reply is the mention flow's, through app_mention
	}
	if isCheckMark(ev.Text) { // never draft text: it files the latest draft when its author is a maintainer
		return a.checkMarkReply(ctx, ev)
	}
	a.editMu.Lock()
	defer a.editMu.Unlock()
	msgs, err := a.Slack.Thread(ctx, ev.Channel, ev.ThreadTS)
	if err != nil {
		a.log().Warn("slack thread fetch failed", "channel", ev.Channel, "err", err)
		if errors.Is(err, ErrThreadTooLong) {
			if _, perr := a.Slack.Post(ctx, ev.Channel, ev.ThreadTS, "This thread is too long to edit the draft: start a new one with a mention.", nil); perr != nil {
				return "error"
			}
			return "refused"
		}
		return "error"
	}
	// With proposals on, a thread's reply re-plans its one proposal (CD7, CD14). The branch is taken
	// above the draft path and below the read, which is under a.editMu: the proposal a reply edits is
	// the one the message shows, and two replies in flight cannot each plan from the revision they saw.
	if a.Proposals.Enabled {
		prop, closed := a.threadProposal(msgs)
		if closed && a.threadCancelled(msgs) {
			return "ignored" // a cancelled proposal filed nothing: its own message says so, and there is no room to ask about
		}
		if closed {
			// CD25: a thread whose task is filed answers the five questions, and nothing else — the
			// room is the one its own Filed line names, never a guess from the issue number. Still a
			// reply and still no command: the answer says where approving, steering and stopping happen.
			line := a.answer(ctx, ev, a.threadRoom(msgs))
			if _, err := a.Slack.Post(ctx, ev.Channel, ev.ThreadTS, line, nil); err != nil {
				a.log().Warn("slack post failed", "channel", ev.Channel, "err", err)
				return "error"
			}
			return "answered"
		}
		if prop != nil {
			if p, ok := prop.Metadata.proposalPayload(); ok {
				return a.replan(ctx, ev, ch, ev.ThreadTS, msgs, prop, p)
			}
		}
	}
	latest, filed := a.threadDraft(msgs)
	if latest == nil || filed {
		return "ignored"
	}
	p, ok := latest.Metadata.draftPayload()
	if !ok {
		return "ignored"
	}
	if p.Author == "" || p.Author != ev.User || !slices.Contains(ch.Repositories, p.Repo) {
		return "ignored"
	}
	say := func(text string) string {
		if _, err := a.Slack.Post(ctx, ev.Channel, ev.ThreadTS, text, nil); err != nil {
			a.log().Warn("slack post failed", "channel", ev.Channel, "err", err)
			return "error"
		}
		return "refused"
	}
	d, changed := ApplyReply(Draft{Repo: p.Repo, Title: p.Title, Body: p.Body}, ev.Text)
	if !changed {
		return "ignored"
	}
	if p.Rev >= maxRevisions {
		return say("This draft was changed the most it can be here: react :white_check_mark: to file it, or start a new one with a mention.")
	}
	d, err = a.Broker.IssueDraft(ctx, d)
	if err != nil {
		if r, ok := err.(Refused); ok { //nolint:errorlint // Refused is returned bare, never wrapped
			return say("Change refused: " + shown(string(r)))
		}
		a.log().Warn("issue_draft failed", "err", err)
		return say("The change could not be checked, try again.")
	}
	return a.postDraft(ctx, ev.Channel, ev.ThreadTS, d, p.Author, p.Rev+1)
}

// reaction files the draft a ✅ is on. Every refusal is one thread line naming the reason and the
// fix, said once per draft, user and reason (answered). Silent, on purpose: malformed ids, the App's
// own user, a message that cannot be read (no thread is known to answer in), and a ✅ on a message
// that is not the App's draft. handle already dropped another workspace and a bot's event.
func (a *App) reaction(ctx context.Context, team string, ev slackEvent) string {
	return a.file(ctx, team, ev.Item.Channel, ev.Item.TS, ev.User, false)
}

// file files the draft at itemTS for user, as a ✅ reaction on it does or, with viaReply, as a ✅
// reply in its thread does: the same checks, the same thread key, the same lines.
func (a *App) file(ctx context.Context, team, channel, itemTS, user string, viaReply bool) string {
	if !channelRE.MatchString(channel) || !userRE.MatchString(user) || !tsRE.MatchString(itemTS) || user == a.BotUserID {
		return "ignored"
	}
	msg, err := a.Slack.Message(ctx, channel, itemTS)
	if err != nil {
		a.log().Warn("slack message fetch failed", "channel", channel, "err", err)
		return "error"
	}
	// Only the App's own draft counts: posted by its bot user, carrying a draft (R23).
	m := msg.Metadata
	if msg.User == "" || msg.User != a.BotUserID || m == nil {
		return "ignored"
	}
	thread := msg.ThreadTS
	if thread == "" {
		thread = msg.TS
	}
	if !tsRE.MatchString(thread) {
		return "ignored"
	}
	// CD8's fallback: a ✅ on a proposal files the plan the message showed through the same run_create
	// validation the Confirm uses. The branch is on the tag alone — the plan's hash is checked there,
	// so a proposal that no longer binds its plan is refused with its own line instead of being read as
	// a foreign message nobody answers.
	if p, ok := m.proposalTaggedPayload(); ok {
		code, _ := a.fileProposal(ctx, channel, thread, itemTS, user, p, false) // a ✅'s verdict goes to the thread
		return code
	}
	p, ok := m.draftPayload()
	if !ok {
		return "ignored"
	}
	sayMeta := func(text string, meta *Metadata) { // the verdict goes to the thread; a failure to say it is logged, not retried
		if _, err := a.Slack.Post(ctx, channel, thread, text, meta); err != nil {
			a.log().Warn("slack post failed", "channel", channel, "err", err)
		}
	}
	// refuse says a refusal once per draft, user and reason: a re-added ✅ does not repeat it.
	refuse := func(reason, text string) string {
		if a.answered.add(itemTS + "/" + user + "/" + reason) {
			sayMeta(text, nil)
		}
		return "refused"
	}
	// retry says a passing failure, the fix being to react again: at most once per retryEvery for a
	// draft, user and reason, so a burst of ✅ does not spam the thread.
	retry := func(reason, text string) string {
		if a.retried.allow(itemTS+"/"+user+"/"+reason, a.now()) {
			sayMeta(text, nil)
		}
		return "error"
	}
	ch, served := a.Channels[channel]
	switch {
	case p.SHA256 != draftHash(p.Repo, p.Title, p.Body):
		return refuse("hash", "Not filed: this draft no longer matches its check; start a new one with a mention.")
	case !served:
		return refuse("channel", "Not filed: this channel is no longer served; ask an operator to add it to the Slack App's channels.")
	case !slices.Contains(ch.Repositories, p.Repo):
		return refuse("repository", fmt.Sprintf("Not filed: %s isn't served in this channel; ask an operator to add it to the channel's repositories.",
			slackText(p.Repo)))
	}
	a.editMu.Lock() // an edit in flight finishes first, so the latest draft is the one filed
	defer a.editMu.Unlock()
	// Only the thread's latest draft files: an earlier one was replaced by an edit.
	msgs, err := a.Slack.Thread(ctx, channel, thread)
	if err != nil {
		a.log().Warn("slack thread fetch failed", "channel", channel, "err", err)
		if errors.Is(err, ErrThreadTooLong) {
			return refuse("too_long", "Not filed: this thread is too long to find its latest draft; start a new one with a mention.")
		}
		return retry("slack", "Not filed: Slack could not be read just now; react again to retry.")
	}
	if latest, _ := a.threadDraft(msgs); latest == nil || latest.TS != itemTS {
		refuse("replaced", "This draft was replaced by a newer one: react on the latest draft.")
		return "ignored"
	}
	// The mark's tag is what stops the draft being edited; its payload is inert, nothing decodes a
	// factory_filed. A marshal error is therefore logged with that consequence named rather than
	// dropped: the tag still goes out, the payload is the empty object every decoder refuses.
	filedPayload, perr := metaPayload(DraftPayload{Repo: p.Repo})
	if perr != nil {
		a.log().Error("filed mark payload: the draft stays editable", "err", perr)
		filedPayload = json.RawMessage(`{}`)
	}
	filedMark := &Metadata{EventType: filedEventType, EventPayload: filedPayload}
	res, err := a.Broker.RunCreate(ctx, RunCreate{
		Draft:      Draft{Repo: p.Repo, Title: p.Title, Body: p.Body},
		MaxCostUSD: ch.MaxCostUSD,
		OnBehalfOf: "slack:" + team + "/" + user,
		Ref:        "slack/" + team + "/" + channel + "/" + thread,
		Key:        team + "/" + channel + "/" + thread, // one issue per thread, whatever the revision
	})
	var r Refused
	if errors.As(err, &r) {
		code, text, passing := notFiled(string(r), user, p.Repo, false)
		if viaReply && !passing && code != "not_a_maintainer" {
			text += " A maintainer reacts :white_check_mark: to file it."
		}
		if passing {
			return retry(code, text)
		}
		return refuse(code, text)
	}
	if err != nil {
		a.log().Warn("run_create failed", "err", err)
		return retry("unreachable", "Not filed: the factory could not be reached, react again to retry.")
	}
	if !roomRE.MatchString(res.Room) {
		a.log().Warn("run_create answered an unusable room id")
		sayMeta(fmt.Sprintf("The task is filed (#%d, for <@%s>), but its room cannot be followed here.", res.Issue, user), filedMark)
		return "error"
	}
	// Put on a duplicate too (idempotent): a Put that failed the first time is retried by the next ✅.
	if err := a.Threads.Put(ctx, res.Room, Thread{Channel: channel, TS: thread, At: a.Now()}); err != nil {
		a.log().Warn("thread map put failed", "room", res.Room, "err", err)
	}
	sayMeta(a.filedText(res, user), filedMark)
	if res.Duplicate {
		return "duplicate"
	}
	return "filed"
}

// githubLoginRE is a GitHub login, the only sponsor the Filed line names.
var githubLoginRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

// notFiled is a run_create refusal as the thread says it: the reason and its fix in one line. A
// passing one (the factory could not check just now) is said on every ✅, since reacting again is
// its fix; any other is said once. Codes outside the list are shown as the broker gave them.
//
// confirm is the surface's verb, not a second vocabulary: a ✅ is reacted and a Confirm is pressed, so
// the one line that names who may file names it in the caller's own word (CD8's two callers are one
// code path).
func notFiled(refusal, user, repo string, confirm bool) (code, text string, passing bool) {
	code, coded := strings.CutPrefix(refusal, "rejected: ")
	who := "<@" + user + ">"
	switch {
	case !coded:
		return "refused", "Not filed: " + shown(refusal), false
	case code == "not_linked":
		return code, "Not filed: " + who + " is not linked to a GitHub account here; mention me with `link` to link it, then react again.", false
	case code == "not_a_maintainer":
		must := "a maintainer must react."
		if confirm {
			must = "a maintainer must confirm it."
		}
		return code, "Not filed: " + who + "'s GitHub account is not a maintainer of " + slackText(repo) + "; " + must, false
	case code == "repository_not_allowed" || code == "repository_not_owned":
		return code, "Not filed: the factory does not take tasks on " + slackText(repo) + " from Slack; ask an operator to add it to the Slack App's repositories.", false
	case code == "kill_switch":
		return code, "Not filed: the factory is paused (kill switch); react again once an operator resumes it.", true
	case code == "plan_changed":
		// The broker recomposes the plan from the submitted fields and re-hashes it (CD20, CD27): the
		// message's plan is no longer the one it was asked about, so nothing is filed from it. The
		// same sentence this family's own hash check says before the broker is called.
		return code, planChangedLine, false
	case code == "principal_budget_exhausted":
		return code, "Not filed: the Slack App's budget for today is spent; react again tomorrow.", false
	case code == "daily_task_cap":
		return code, "Not filed: the factory's daily task cap is reached; react again after 00:00 UTC.", false
	case code == "principal_task_cap":
		return code, "Not filed: the Slack App has the most tasks open it may; react again once one ends.", true
	case slices.Contains([]string{"link_unverified", "ledger_unavailable", "tasks_unavailable", "intake_failed", "meter_unpriced"}, code):
		return code, "Not filed: the factory could not check this just now; react again to retry.", true
	}
	return code, "Not filed: " + shown(refusal), false
}

// Webhooks is the webhooks listener's handler: the delivery is verified with the broker's keys
// (read at use) before it is decoded. It answers once the thread is told, so a Slack failure is a
// 502 and the broker's at-least-once retry brings the event back.
func (a *App) Webhooks() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
		keys, kerr := a.WebhookKeys()
		if kerr != nil {
			// Our own key problem, not a bad signature: a 503 so it is told apart in the metrics. It is
			// not a hold: the broker retries a non-2xx after 10s, 1m and 5m, then counts a failure, and
			// three given-up deliveries disable the subscriber until its generation is bumped.
			a.log().Error("webhook keys unreadable")
			a.count(ctx, "webhooks", "no_key")
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if err != nil || len(body) > maxWebhookBody {
			a.count(ctx, "webhooks", "bad_signature")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := webhook.Verify(keys, r.Header, body, a.Now())
		if err != nil {
			a.count(ctx, "webhooks", "bad_signature")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Known race (M3): two concurrent redeliveries of one id can both pass add before either
		// finishes; the worst case is one duplicate thread line, and a failure forgets the id.
		if !a.deliveries.add(id) {
			a.count(ctx, "webhooks", "duplicate")
			w.WriteHeader(http.StatusOK)
			return
		}
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handleTimeout)
		defer cancel()
		outcome := a.deliver(hctx, body)
		if outcome == "error" {
			a.deliveries.forget(id)
			a.count(ctx, "webhooks", outcome)
			http.Error(w, "try again", http.StatusBadGateway)
			return
		}
		// One request has one outcome: the approval-close path counts its own word where it happens
		// (closeCard), so it hands back countedInPlace and has nothing left to add here.
		if outcome != countedInPlace {
			a.count(ctx, "webhooks", outcome)
		}
		w.WriteHeader(http.StatusOK)
	})
}

// countedInPlace is the outcome a path returns when it counted its own word instead of handing one
// back: the approval-close path does (closeCard, counted in deliver, D10), so one request keeps one
// outcome and its word never becomes the 502 the broker retries.
const countedInPlace = ""

func (a *App) deliver(ctx context.Context, body []byte) string {
	var e webhook.Event
	if json.Unmarshal(body, &e) != nil || !slices.Contains(webhook.Types(), e.Type) || !roomRE.MatchString(e.Room) {
		return "ignored"
	}
	// The thread is read before the dispatch: the card needs it as much as the line does, and the
	// line's own template is decided after it.
	th, found, err := a.Threads.Get(ctx, e.Room)
	if err != nil {
		a.log().Warn("thread map get failed", "room", e.Room, "err", err)
		return "error"
	}
	if !found {
		return "ignored" // a room that was not filed from Slack
	}
	// §5: a forge.push wait in a channel that draws cards is one card, posted or updated by its
	// approval id. Everything else keeps today's line, with no button.
	if e.Type == "room.waiting_for_human" && a.cardWanted(e, th) {
		return a.postOrUpdateCard(ctx, th, e)
	}
	// D10: a closed approval makes its card inert, in a channel that draws cards; a channel that
	// draws none had the wait as a text line, so its close is the thread's line instead. The card is
	// a view, never the fence: the close's word is counted here rather than by the handler, so the
	// delivery itself is never the `error` the broker retries — a card the App cannot rewrite stays
	// readable, and the store refuses a press on it anyway.
	if e.Type == "room.approval_closed" {
		if ch, served := a.Channels[th.Channel]; served && ch.Approvals {
			a.count(ctx, "webhooks", a.closeCard(ctx, th, e))
			return countedInPlace
		}
	}
	// CD11: a line is posted only for a transition the thread's channel is told about (its notify
	// list, webhook.Notify() when it gave none), so a run start says nothing by default. The card
	// branches above are the approvals switch's, not a line, and are not filtered. A thread whose
	// channel the config no longer serves posts nothing rather than guessing.
	if ch, served := a.Channels[th.Channel]; !served || !ch.notifies(e.Type) {
		return "ignored"
	}
	return a.postWaitLine(ctx, th, e)
}

// line is the event's thread template. Every value is clipped to the broker's own vocabulary
// first (M4), so no free text reaches Slack (invariant 7).
func (a *App) line(e webhook.Event) (string, bool) {
	url := strings.TrimSuffix(a.RoomsURL, "/") + "/r/" + e.Room
	reason, limit, state := webhook.Clip("reason", e.Reason), webhook.Clip("limit", e.Limit), webhook.Clip("state", e.State)
	switch e.Type {
	case "room.task_triaged":
		return "Triaged. Room: " + url, true
	case "room.run_started":
		return "A run started.", true
	case "room.waiting_for_human":
		return fmt.Sprintf("The work waits for a human (%s): answer it on the room page, %s. Nothing posted here reaches the run.", reason, url), true
	case "room.pr_opened":
		return fmt.Sprintf("Pull request #%d opened.", e.PR), true
	case "room.verdict_posted":
		return fmt.Sprintf("A review verdict is on #%d.", e.PR), true
	case "room.merged":
		return fmt.Sprintf("#%d merged.", e.PR), true
	case "room.reverted":
		return fmt.Sprintf("#%d reverted.", e.PR), true
	case "room.budget_reached":
		return fmt.Sprintf("A budget was reached (%s).", limit), true
	case "room.ended":
		return fmt.Sprintf("Ended, state: %s. Room: %s", state, url), true
	case "room.approval_closed":
		// D10: the thread that draws no card still hears how its wait closed; a channel that draws
		// cards has the card itself as that message (closeCard), and this line is never reached.
		return fmt.Sprintf("The approval closed: %s. Room: %s", webhook.Clip("decision", e.Decision), url), true
	}
	return "", false
}
