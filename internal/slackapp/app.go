// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
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

	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/summary"
	"github.com/Smana/maku/internal/webhook"
)

const (
	// filedEventType marks the App's "Filed" line: a proposal whose thread has one is no longer edited.
	filedEventType = "factory_filed"
	// threadTooLongLine is a reply's answer in a thread Slack will not return whole.
	threadTooLongLine = "This thread is too long for me to read: open the task's room page from the Filed line, or start a new thread with a mention."
	handleTimeout     = 30 * time.Second
	// maxMetadata bounds a proposal's metadata JSON, well under Slack's message-metadata cap.
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
	RunCreate(ctx context.Context, c RunCreate) (RunResult, error)
	SlackLinkCode(ctx context.Context, team, user, handle, name string) (string, error)
	SlackUnlink(ctx context.Context, team, user string) (bool, error)
	// Handoff and HandoffCheck are the governed planner and its stored-plan check (UX2): a mention or a
	// reply goes through Handoff, and HandoffCheck reads or pins the stored plan's workflow without a
	// model call. Neither files anything, and ErrUnknownTool is a broker without the tool.
	Handoff(ctx context.Context, in HandoffRequest) (HandoffResult, error)
	HandoffCheck(ctx context.Context, id, onBehalfOf string, workflow *string) (HandoffChecked, error)
	// Decide records one press of an approval's card, on the broker's decide route (§1): the only
	// thing the App decides, and only for forge.push.
	Decide(ctx context.Context, d Decision) (DecisionResult, error)
	// Task and Inbox are the reads a filed thread makes (UX4): Task for status, coordination, changes
	// and PR, Inbox for what needs a person in that room.
	Task(ctx context.Context, room string) (TaskResult, error)
	Inbox(ctx context.Context, room string) ([]summary.Need, error)
	// Stop, Retry and Answer record one confirmed press on the broker's stop, retry and answer routes.
	Stop(ctx context.Context, a Act) (ActResult, error)
	Retry(ctx context.Context, a Act) (ActResult, error)
	Answer(ctx context.Context, a Answer) (ActResult, error)
	// IntentClassify is tier 1's one read-only classification of a reply tier 0 does not know
	// (natural-language intents): a label, never an action. ErrUnknownTool is a broker without it.
	IntentClassify(ctx context.Context, in IntentRequest) (IntentResult, error)
	// CoordinationPlan and CoordinationDraft are a two-repository mention's planner and its parent's
	// draft (v4): neither confirms, files nor runs anything.
	CoordinationPlan(ctx context.Context, in CoordinationPlanRequest) (CoordinationPlanResult, error)
	CoordinationDraft(ctx context.Context, parent string, in coordination.CoordinationConfirmation, onBehalfOf string) (coordination.CoordinationConfirmation, error)
}

// Recorder counts requests for maku_slack_app_requests_total{route, outcome}, the proposal actions
// (proposal_updates_total{action}) and the answers a filed proposal's thread gives
// (thread_answers_total{question}). The two last are closed vocabularies the App passes: the
// instruments' names and their serving are metrics' (CD7, CD25).
type Recorder interface {
	Request(ctx context.Context, route, outcome string)
	ProposalUpdate(ctx context.Context, action string)
	ThreadAnswer(ctx context.Context, question string)
	// SlackIntent counts one reply tier 1 read: tier, intent and outcome from the closed lists
	// (maku_slack_intents_total).
	SlackIntent(ctx context.Context, tier, intent, outcome string)
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
	Slack         Slack
	Broker        BrokerAPI
	Threads       Threads
	Verifier      Verifier
	WebhookKeys   func() ([][]byte, error)
	TeamID        string
	BotUserID     string
	RoomsURL      string
	Channels      map[string]Channel
	PlanningAfter time.Duration // how long a plan runs before the thread shows a "planning…" status; zero means 4s
	Async         func(func())  // runs an event's handling after the 200 (tests); nil means the App's workers
	Now           func() time.Time
	Metrics       Recorder
	Logger        *slog.Logger

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
	switch command(ev.Text, a.BotUserID) {
	case "link":
		return a.link(ctx, ev, thread)
	case "unlink":
		return a.unlink(ctx, ev, thread)
	}
	code := a.propose(ctx, ev, ch, thread, commandless(ev.Text, a.BotUserID), 1)
	a.proposalUpdate(ctx, countMention)
	return code
}

// command is the mention's one-word command, "link" or "unlink", or "" for a proposal.
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

// isCheckMark is a reply that is only a ✅ or a heavy check mark, as an emoji or its Slack code.
func isCheckMark(text string) bool {
	switch strings.TrimSpace(text) {
	case "✅", "✔️", "✔", ":white_check_mark:", ":heavy_check_mark:":
		return true
	}
	return false
}

// checkMarkReply files the thread's live proposal for the replier, like a ✅ reaction on it. Who may
// file is the broker's call (the linked maintainer check), as for the reaction. A thread with no
// proposal is silent: the reply is about nothing the App posted.
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
	if prop, closed := a.threadProposal(msgs); prop != nil && !closed {
		return a.file(ctx, a.TeamID, ev.Channel, prop.TS, ev.User, true)
	}
	return "ignored"
}

// reply revises the thread's live proposal with a reply from the author of the mention, or answers
// the five read questions once the proposal's task is filed (CD25).
func (a *App) reply(ctx context.Context, ev slackEvent) string {
	ch, ok := a.Channels[ev.Channel]
	if !ok || ev.Subtype != "" || ev.ThreadTS == "" || ev.ThreadTS == ev.TS || ev.User == a.BotUserID ||
		!channelRE.MatchString(ev.Channel) || !userRE.MatchString(ev.User) || !tsRE.MatchString(ev.ThreadTS) ||
		strings.TrimSpace(ev.Text) == "" || strings.Contains(ev.Text, "<@"+a.BotUserID+">") {
		return "ignored" // a mention in a reply is the mention flow's, through app_mention
	}
	if isCheckMark(ev.Text) {
		return a.checkMarkReply(ctx, ev)
	}
	a.editMu.Lock()
	defer a.editMu.Unlock()
	msgs, err := a.Slack.Thread(ctx, ev.Channel, ev.ThreadTS)
	if err != nil {
		a.log().Warn("slack thread fetch failed", "channel", ev.Channel, "err", err)
		if errors.Is(err, ErrThreadTooLong) {
			if _, perr := a.Slack.Post(ctx, ev.Channel, ev.ThreadTS, threadTooLongLine, nil); perr != nil {
				return "error"
			}
			return "refused"
		}
		return "error"
	}
	if m, cp, ok := a.coordinationThread(msgs); ok {
		return a.replanCoordination(ctx, ev, ev.ThreadTS, msgs, m, cp)
	}
	prop, closed := a.threadProposal(msgs)
	if closed && a.threadCancelled(msgs) {
		return "ignored"
	}
	if closed {
		room := a.threadRoom(msgs)
		q := questionOf(ev.Text)
		if (q == QuestionStop && ch.actsOn(actStop)) || (q == QuestionRetry && ch.actsOn(actRetry)) {
			a.answerCount(ctx, q)
			if !roomRE.MatchString(room) {
				if _, err := a.Slack.Post(ctx, ev.Channel, ev.ThreadTS, unknownRoomLine, nil); err != nil {
					a.log().Warn("slack post failed", "channel", ev.Channel, "err", err)
					return "error"
				}
				return "answered"
			}
			return a.actConfirm(ctx, Thread{Channel: ev.Channel, TS: ev.ThreadTS}, string(q), room, ev.User)
		}
		if q == QuestionUnknown && ch.actsOn(actAnswer) && roomRE.MatchString(room) {
			if hasOpenQuestion(a.taskSummary(ctx, room)) {
				return a.answerConfirm(ctx, Thread{Channel: ev.Channel, TS: ev.ThreadTS}, room, ev.User, ev.Text)
			}
		}
		line := a.answer(ctx, ev, room)
		if _, err := a.Slack.Post(ctx, ev.Channel, ev.ThreadTS, line, nil); err != nil {
			a.log().Warn("slack post failed", "channel", ev.Channel, "err", err)
			return "error"
		}
		if q == QuestionUnknown {
			a.tierOne(ctx, ev, ch, room)
		}
		return "answered"
	}
	if prop != nil {
		if p, ok := prop.Metadata.proposalPayload(); ok {
			return a.replan(ctx, ev, ch, ev.ThreadTS, msgs, prop, p)
		}
	}
	return "ignored"
}

// reaction files the proposal a ✅ is on.
func (a *App) reaction(ctx context.Context, team string, ev slackEvent) string {
	return a.file(ctx, team, ev.Item.Channel, ev.Item.TS, ev.User, false)
}

// file files the proposal at itemTS for user, as a ✅ reaction on it does or, with viaReply, as a ✅
// reply in its thread does.
func (a *App) file(ctx context.Context, _ string, channel, itemTS, user string, _ bool) string {
	if !channelRE.MatchString(channel) || !userRE.MatchString(user) || !tsRE.MatchString(itemTS) || user == a.BotUserID {
		return "ignored"
	}
	msg, err := a.Slack.Message(ctx, channel, itemTS)
	if err != nil {
		a.log().Warn("slack message fetch failed", "channel", channel, "err", err)
		return "error"
	}
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
	if p, ok := m.proposalTaggedPayload(); ok {
		code, _ := a.fileProposal(ctx, channel, thread, itemTS, user, p, false)
		return code
	}
	return "ignored"
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
var refusalCodeRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func notFiled(refusal, user, repo string, confirm bool) (code, text string, passing bool) {
	code, coded := strings.CutPrefix(refusal, "rejected: ")
	if !coded {
		if head, _, ok := strings.Cut(refusal, ": "); ok && refusalCodeRE.MatchString(head) {
			code, coded = head, true
		}
	}
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
		// The broker checks the stored handoff against the hash shown (UX2, CD27): the message's plan is
		// no longer the one it was asked about, so nothing is filed from it. The same sentence this
		// family's own hash check says before the broker is called.
		return code, planChangedLine, false
	case code == "handoff_expired":
		return code, "Not filed: this proposal expired; mention me again to plan it.", false
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
	// v4: a coordination parent's thread is told only what its channel may see of the family now.
	if th.Coordination {
		ch, served := a.Channels[th.Channel]
		if !served || !coordinationNotifies(ch, e.Type) {
			return "ignored"
		}
		return a.deliverCoordination(ctx, th, ch, e.Room)
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
	ch, served := a.Channels[th.Channel]
	if !served {
		return "ignored"
	}
	// UX7: room.merged and room.reverted edit the thread's one end line before the notify filter.
	switch e.Type {
	case "room.merged":
		if th.EndTS == "" && !ch.notifies("room.ended") && !ch.notifies("room.merged") {
			return "ignored"
		}
		return a.upsertEndLine(ctx, e.Room, th, endLine(e, a.taskSummary(ctx, e.Room)), nil)
	case "room.reverted":
		wantLine := ch.Notify != nil && slices.Contains(ch.Notify, "room.reverted")
		if th.EndTS == "" && !wantLine {
			return "ignored"
		}
		if th.EndTS != "" {
			text := endLine(e, a.taskSummary(ctx, e.Room))
			if err := a.Slack.Update(ctx, th.Channel, th.EndTS, text, nil); err != nil {
				a.log().Warn("slack update failed", "room", e.Room, "err", err)
				return "error"
			}
		}
		if wantLine {
			return a.postWaitLine(ctx, th, e)
		}
		return "posted"
	}
	// UX8, CD11: a line is posted only for a transition the thread's channel is told about (its notify
	// list, defaultNotify when it gave none), so a run start says nothing by default.
	if !ch.notifies(e.Type) {
		return "ignored"
	}
	switch e.Type {
	case "room.ended":
		text := endLine(e, a.taskSummary(ctx, e.Room))
		return a.upsertEndLine(ctx, e.Room, th, text, actLineBlocks(e, ch, text))
	case "room.task_triaged":
		sum := a.taskSummary(ctx, e.Room)
		url := strings.TrimSuffix(a.RoomsURL, "/") + "/r/" + e.Room
		text := "Started. Room: " + url
		if sum.Status.Issue != nil && sum.Status.Issue.Number > 0 {
			text = fmt.Sprintf("Started #%d. Room: %s", sum.Status.Issue.Number, url)
		}
		if _, err := a.Slack.Post(ctx, th.Channel, th.TS, text, nil); err != nil {
			a.log().Warn("slack post failed", "room", e.Room, "err", err)
			return "error"
		}
		return "posted"
	case "room.waiting_for_human":
		sum := a.taskSummary(ctx, e.Room)
		text := waitingLine(e, sum, a.RoomsURL)
		if blocks := actLineBlocks(e, ch, text); len(blocks) > 0 {
			if _, err := a.Slack.PostCard(ctx, th.Channel, th.TS, text, blocks, nil); err != nil {
				a.log().Warn("slack post card failed", "room", e.Room, "err", err)
				return "error"
			}
			return "posted"
		}
		if _, err := a.Slack.Post(ctx, th.Channel, th.TS, text, nil); err != nil {
			a.log().Warn("slack post failed", "room", e.Room, "err", err)
			return "error"
		}
		return "posted"
	}
	return a.postWaitLine(ctx, th, e)
}

func (a *App) taskSummary(ctx context.Context, room string) summary.Summary {
	tv, err := a.Broker.Task(ctx, room)
	if err != nil {
		a.log().Warn("task read for webhook failed", "room", room, "err", err)
		return summary.Summary{}
	}
	return tv.Summary()
}

func (a *App) upsertEndLine(ctx context.Context, room string, th Thread, text string, blocks json.RawMessage) string {
	if th.EndTS != "" {
		if err := a.Slack.Update(ctx, th.Channel, th.EndTS, text, blocks); err != nil {
			a.log().Warn("slack update failed", "room", room, "err", err)
			return "error"
		}
		return "posted"
	}
	var (
		ts  string
		err error
	)
	if len(blocks) > 0 {
		ts, err = a.Slack.PostCard(ctx, th.Channel, th.TS, text, blocks, nil)
	} else {
		ts, err = a.Slack.Post(ctx, th.Channel, th.TS, text, nil)
	}
	if err != nil {
		a.log().Warn("slack post failed", "room", room, "err", err)
		return "error"
	}
	th.EndTS = ts
	if th.At.IsZero() {
		th.At = a.now()
	}
	if err := a.Threads.Put(ctx, room, th); err != nil {
		a.log().Warn("thread map put failed", "room", room, "err", err)
	}
	return "posted"
}

// line is the event's thread template. Every value is clipped to the broker's own vocabulary
// first (M4), so no free text reaches Slack (invariant 7).
func (a *App) line(e webhook.Event) (string, bool) {
	url := strings.TrimSuffix(a.RoomsURL, "/") + "/r/" + e.Room
	reason, limit, state := webhook.Clip("reason", e.Reason), webhook.Clip("limit", e.Limit), webhook.Clip("state", e.State)
	switch e.Type {
	case "room.task_triaged":
		return "Started. Room: " + url, true
	case "room.run_started":
		return "A run started.", true
	case "room.waiting_for_human":
		return fmt.Sprintf("Needs you (%s): answer it on the room page, %s. Nothing posted here reaches the run.", reason, url), true
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
		return fmt.Sprintf("Ended: %s. Room: %s", state, url), true
	case "room.approval_closed":
		// D10: the thread that draws no card still hears how its wait closed; a channel that draws
		// cards has the card itself as that message (closeCard), and this line is never reached.
		return fmt.Sprintf("The approval closed: %s. Room: %s", webhook.Clip("decision", e.Decision), url), true
	}
	return "", false
}

func coordinationNotifies(ch Channel, event string) bool {
	if ch.notifies(event) {
		return true
	}
	return ch.Notify == nil && (event == "room.merged" || event == "room.reverted")
}
