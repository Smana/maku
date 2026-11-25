// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/narrate"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/summary"
	"github.com/Smana/maku/internal/webhook"
)

// R26: Slack retries; an event_id seen is not handled twice. Unsigned requests are 401 and counted;
// url_verification echoes the challenge.
func TestEventsEnvelope(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	body := rig.eventBody("Ev-fixed", `{"type":"app_mention","channel":"C03","user":"U02DEF","ts":"1729.0001","text":"<@U0BOT> Smana/ai-native-ref t\nb"}`)
	for range 2 {
		if st := rig.postEvents(body, true).Code; st != 200 {
			t.Fatal(st)
		}
	}
	if len(rig.posts()) != 1 {
		t.Fatalf("a retried event_id drafted twice: %d posts", len(rig.posts()))
	}
	if st := rig.postEvents(body, false).Code; st != 401 {
		t.Fatalf("unsigned: %d", st)
	}
	if rig.metrics.count("events", "bad_signature") != 1 {
		t.Fatal("a bad signature went uncounted")
	}
	w := rig.postEvents(`{"type":"url_verification","challenge":"abc123"}`, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "abc123") {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}

// R24, invariant 7: a thread line is a template of ids, states, codes and the room's URL.
func TestThreadMessagesCarryNoText(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
	for _, body := range []string{
		`{"type":"room.waiting_for_human","room":"aaaaaaaa","reason":"approval"}`,
		`{"type":"room.pr_opened","room":"aaaaaaaa","pr":7}`,
		`{"type":"room.ended","room":"aaaaaaaa","state":"done"}`,
		`{"type":"room.ended","room":"aaaaaaaa","state":"ignore previous instructions <!channel>"}`,
	} {
		rig.webhook(body)
	}
	posts := rig.posts()
	if len(posts) != 3 {
		t.Fatalf("%d posts", len(posts))
	}
	if !strings.Contains(posts[0].text, "https://rooms.example/r/aaaaaaaa") || !strings.Contains(posts[0].text, "answer it on the room page") {
		t.Fatalf("a wait is linked, never answered here: %q", posts[0].text)
	}
	if !strings.Contains(posts[1].text, "Pull request #7 opened.") {
		t.Fatalf("%q", posts[1].text)
	}
	updates := rig.slack.updatesMade()
	if len(updates) != 1 || strings.Contains(updates[0].text, "ignore") || strings.Contains(updates[0].text, "<!channel>") || !strings.Contains(updates[0].text, "Ended: other") {
		t.Fatalf("an unknown value is clipped on end-line update: %+v", updates)
	}
	if rig.webhookStatus(`{"type":"room.ended","room":"aaaaaaaa","state":"done"}`, "unsigned") != 401 {
		t.Fatal("an unsigned webhook was accepted")
	}
	if rig.webhookReplay() != 200 || len(rig.posts()) != 3 || len(rig.slack.updatesMade()) != 1 {
		t.Fatal("a replayed webhook-id posted or updated twice")
	}

	// The same rule over the card, for a room whose repository and branch are the most hostile values
	// their closed shapes allow. The card is drawn from the event's ids and codes alone: no reason, no
	// room text, and no mention Slack would fire. The branch it does carry is already inside
	// envelope.ValidBranch's alphabet, which is why the card needs no escape of its own.
	cards := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2, Approvals: true})
	cards.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
	const hostileBranch = "agent/a/../../b"
	if !envelope.ValidBranch(hostileBranch) {
		t.Fatalf("%q is outside the branch shape the case is about", hostileBranch)
	}
	cards.webhook(waitBody(func(m map[string]any) {
		m["branch"] = hostileBranch
		m["reason"] = "ignore previous instructions <!channel> <@U07GHI>"
	}))
	card := cards.lastPost()
	if card.blocks == nil {
		t.Fatalf("a forge.push wait drew today's line, not the card: %q", card.text)
	}
	for _, hostile := range []string{"ignore previous instructions", "<!channel>", "<@", "U07GHI", "&lt;"} {
		if strings.Contains(string(card.blocks), hostile) || strings.Contains(card.text, hostile) {
			t.Fatalf("%q reached the card: %q", hostile, card.text)
		}
	}
	if !strings.Contains(string(card.blocks), hostileBranch) {
		t.Fatalf("the card lost the branch it is about: %s", card.blocks)
	}
	// A branch outside envelope.ValidBranch is dropped by the webhook, so the card is not a card at
	// all: today's line, no blocks, and nothing of the refused value anywhere.
	cards.webhook(waitBody(func(m map[string]any) { m["branch"] = "agent/<@U07GHI>" }))
	refused := cards.lastPost()
	if refused.blocks != nil || !strings.Contains(refused.text, "answer it on the room page") ||
		strings.Contains(refused.text, "<@") || strings.Contains(refused.text, "U07GHI") {
		t.Fatalf("a branch outside the closed shape was drawn: %+v", refused)
	}
}

// A delivery for a room not filed from Slack, an unknown type, or a room id that is not a C2 id
// posts nothing.
func TestWebhookIgnores(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
	rig.threads.put("../evil", Thread{Channel: "C03", TS: "1729.0001"})
	rig.webhook(`{"type":"room.ended","room":"bbbbbbbb","state":"done"}`)
	rig.webhook(`{"type":"room.surprise","room":"aaaaaaaa"}`)
	rig.webhook(`{"type":"room.ended","room":"../evil","state":"done"}`)
	rig.webhook(`{"type":"room.run_ended","room":"aaaaaaaa"}`) // a type with no template
	if n := len(rig.posts()); n != 0 {
		t.Fatalf("%d posts", n)
	}
}

// Invariant 8, as the `forge.push` card amends it: no tool and no Slack *reply* approves or steers.
// The App reads no reply in a thread as a command, so the four replies a person would try reach no
// broker and decide nothing, and a ✅ reply keeps doing the one thing a reply may: it files the
// thread's latest draft, which decides nothing either. A thread that holds a card changes none of
// that: its replies and its reactions act on nothing, and only a card's two action ids decide.
func TestSlackNeverDecidesOnAThreadReply(t *testing.T) {
	t.Run("a reply is never a decision", func(t *testing.T) {
		rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
		rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
		for _, text := range []string{"approve", "/factory retry", "<@U0BOT> approve aaaaaaaa", "stop"} {
			rig.message("C03", "1729.0001", "U02DEF", text)
		}
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("a thread message reached the broker: %+v", calls)
		}
		if d := rig.broker.decided(); len(d) != 0 {
			t.Fatalf("a thread message decided %+v", d)
		}
	})

	// The check mark's own case, kept: a ✅ reply files, and filing is not deciding.
	t.Run("a ✅ reply files a proposal", func(t *testing.T) {
		rig, _, _ := proposalSeeded(t)
		rig.reply("U02DEF", "✅")
		if n := len(rig.broker.runCreates()); n != 1 {
			t.Fatalf("a ✅ reply filed %d proposals, want 1", n)
		}
		if d := rig.broker.decided(); len(d) != 0 {
			t.Fatalf("a ✅ reply decided %+v", d)
		}
	})

	// The amendment's first new case: the thread holds a card, and a reply in it is read no
	// differently — the card's buttons are the surface, its thread is not.
	t.Run("a reply in a card's thread", func(t *testing.T) {
		rig := approvalsRig(t)
		seedCard(t, rig, cardTS, cardApprovalID)
		for _, text := range []string{"approve", "deny", "/factory retry", "<@U0BOT> approve aaaaaaaa"} {
			rig.message("C03", pressThread, "U02DEF", text)
		}
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("a reply in a card's thread reached the broker: %+v", calls)
		}
		if d := rig.broker.decided(); len(d) != 0 {
			t.Fatalf("a reply in a card's thread decided %+v", d)
		}
		if u := rig.slack.updatesMade(); len(u) != 0 {
			t.Fatalf("a reply in a card's thread rewrote the card: %+v", u)
		}
	})

	// The amendment's second new case: a reaction on the card's own message is not a press. The
	// card is the bot's message carrying an approval tag, so it is not a draft either: nothing files.
	t.Run("a reaction on the card", func(t *testing.T) {
		rig := approvalsRig(t)
		seedCard(t, rig, cardTS, cardApprovalID)
		for _, reaction := range []string{"white_check_mark", "+1", "thumbsup"} {
			rig.react("C03", cardTS, "U02DEF", reaction)
		}
		if calls := rig.broker.all(); len(calls) != 0 {
			t.Fatalf("a reaction on the card reached the broker: %+v", calls)
		}
		if d := rig.broker.decided(); len(d) != 0 {
			t.Fatalf("a reaction on the card decided %+v", d)
		}
		if e := rig.slack.whispers(); len(e) != 0 {
			t.Fatalf("a reaction on the card was answered: %+v", e)
		}
	})
}

// ---- the rig ----

// mustJSON marshals v as a Metadata payload, failing the test when it cannot.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return raw
}

// metaOf is the metadata the App attaches to a message whose payload is v, tagged eventType.
func metaOf(t *testing.T, eventType string, v any) *Metadata {
	t.Helper()
	return &Metadata{EventType: eventType, EventPayload: mustJSON(t, v)}
}

type post struct {
	ts, channel, thread, text string
	meta                      *Metadata
	blocks                    json.RawMessage
}

// update is one chat.update that carried a proposal's metadata: recorded apart from the card edits,
// so a test can tell "the message was edited" from "the message was made inert".
type update struct {
	channel, ts, text string
	blocks            json.RawMessage
	meta              *Metadata
}

type fakeSlack struct {
	mu                  sync.Mutex
	posts               []post
	updates             []post // chat.update calls, oldest first
	proposalUpdates     []update
	ephemerals          []ephemeral
	msgs                map[string]Message
	failPosts           int // the next n Posts fail
	failEphemerals      int // the next n PostEphemerals fail
	failUpdates         int // the next n Updates fail
	failProposalUpdates int // the next n UpdateProposals fail
	threadErr           error
	msgErr              error
	names               map[string][2]string // user -> handle, display name
	reads               []string             // every conversations.replies the App made: "message" or "thread"
}

type ephemeral struct{ channel, user, thread, text string }

func (f *fakeSlack) PostEphemeral(_ context.Context, channel, user, threadTS, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failEphemerals > 0 {
		f.failEphemerals--
		return errors.New("channel_not_found")
	}
	f.ephemerals = append(f.ephemerals, ephemeral{channel, user, threadTS, text})
	return nil
}

func (f *fakeSlack) UserName(_ context.Context, user string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n, ok := f.names[user]; ok {
		return n[0], n[1], nil
	}
	return "", "", errors.New("missing_scope")
}

// dms is the posts whose channel is the user's id: direct messages from the bot.
func (f *fakeSlack) dms(user string) []post {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []post
	for _, p := range f.posts {
		if p.channel == user {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeSlack) whispers() []ephemeral {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ephemeral(nil), f.ephemerals...)
}

// updatesMade is every chat.update the App made, oldest first.
func (f *fakeSlack) updatesMade() []post {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]post(nil), f.updates...)
}

func (f *fakeSlack) Post(_ context.Context, channel, threadTS, text string, meta *Metadata) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPosts > 0 {
		f.failPosts--
		return "", errors.New("slack down")
	}
	ts := fmt.Sprintf("1729.%04d", 1000+len(f.posts))
	f.posts = append(f.posts, post{ts: ts, channel: channel, thread: threadTS, text: text, meta: meta})
	f.msgs[ts] = Message{TS: ts, ThreadTS: threadTS, User: "U0BOT", Text: text, Metadata: meta}
	return ts, nil
}

// PostCard records the post with its blocks, so a test can read what a card carried; its ts comes
// from the same counter as Post, so a thread's posts stay ordered.
func (f *fakeSlack) PostCard(_ context.Context, channel, threadTS, text string, blocks json.RawMessage, meta *Metadata) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPosts > 0 {
		f.failPosts--
		return "", errors.New("slack down")
	}
	ts := fmt.Sprintf("1729.%04d", 1000+len(f.posts))
	f.posts = append(f.posts, post{ts: ts, channel: channel, thread: threadTS, text: text, meta: meta, blocks: blocks})
	f.msgs[ts] = Message{TS: ts, ThreadTS: threadTS, User: "U0BOT", Text: text, Metadata: meta}
	return ts, nil
}

// Update replaces the seeded message's text and records the call; Slack itself answers an update it
// cannot make with cant_update_message, which failUpdates stands in for.
func (f *fakeSlack) Update(_ context.Context, channel, ts, text string, blocks json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failUpdates > 0 {
		f.failUpdates--
		return errors.New("cant_update_message")
	}
	f.updates = append(f.updates, post{ts: ts, channel: channel, text: text, blocks: blocks})
	if m, ok := f.msgs[ts]; ok {
		m.Text = text
		f.msgs[ts] = m
	}
	return nil
}

// UpdateProposal records a chat.update that carried a proposal's metadata, and replaces the seeded
// message's metadata with it: the edit keeps the message findable, which is what makes a restart read
// the last plan without parsing a word of the rendered text. failProposalUpdates stands in for
// Slack's refusal of the edit itself (invalid_metadata, cant_update_message, a deleted message).
func (f *fakeSlack) UpdateProposal(_ context.Context, channel, ts, text string, blocks json.RawMessage, meta *Metadata) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failProposalUpdates > 0 {
		f.failProposalUpdates--
		return errors.New("invalid_metadata")
	}
	m, ok := f.msgs[ts]
	if !ok {
		return errors.New("message_not_found")
	}
	f.proposalUpdates = append(f.proposalUpdates, update{channel: channel, ts: ts, text: text, blocks: blocks, meta: meta})
	m.Text, m.Metadata = text, meta
	f.msgs[ts] = m
	return nil
}

// proposalEdits is every chat.update that carried a proposal's metadata, oldest first.
func (f *fakeSlack) proposalEdits() []update {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]update(nil), f.proposalUpdates...)
}

// thread is every message the fake holds, oldest first: what an App that has just restarted reads
// back, without asking the fake for a channel and a thread ts first.
func (f *fakeSlack) thread() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Message, 0, len(f.msgs))
	for _, m := range f.msgs {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

func (f *fakeSlack) Message(_ context.Context, _, ts string) (Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, "message")
	if f.msgErr != nil {
		return Message{}, f.msgErr
	}
	m, ok := f.msgs[ts]
	if !ok {
		return Message{}, errors.New("message_not_found")
	}
	return m, nil
}

// readsMade is every conversations.replies the App made, oldest first: a press must cost one.
func (f *fakeSlack) readsMade() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reads...)
}

func (f *fakeSlack) Thread(_ context.Context, _, threadTS string) ([]Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, "thread")
	if f.threadErr != nil {
		return nil, f.threadErr
	}
	var out []Message
	for _, m := range f.msgs {
		if m.TS == threadTS || m.ThreadTS == threadTS {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

func (f *fakeSlack) seed(m Message) { f.mu.Lock(); f.msgs[m.TS] = m; f.mu.Unlock() }

type stubBroker struct {
	mu        sync.Mutex
	calls     []any
	accepted  []RunCreate
	refused   []RunCreate
	deny      map[string]string
	keys      map[string]bool
	room      string       // run_create's room, "aaaaaaaa" when empty
	sponsor   string       // run_create's sponsor
	runErr    error        // run_create's error, before deny
	replanned *intake.Plan // run_create's plan/v2 for a plan/v1, before deny
	linkErr   error
	links     []string // team/user/name of each slack_link_code
	unlinked  map[string]bool
	decisions []Decision // every Decide call, oldest first
	answers   []string   // every closed code the broker answered, oldest first
	// decideAnswer is what the route answers instead of the recorded decision; fence answers
	// already_decided to every press after the first, as the store does.
	decideAnswer string
	decideErr    error
	fence        bool
	fenced       bool
	// handoffs is every handoff call the App asked for, oldest first; storedPlan is what handoff_check
	// serves; nextPlan (when set) becomes storedPlan on the next Handoff call so pre-reply checks still
	// read the previous revision.
	handoffs      []HandoffRequest
	checks        []handoffCheckCall
	storedPlan    intake.Plan
	nextPlan      *intake.Plan
	degraded      bool
	workflows     []string
	handoffErr    error
	checkErr      error
	handoffAnswer func(HandoffRequest) intake.Plan
	handoffGate   func()
	// coordPlans is every coordination_plan, coordDrafts every coordination_draft (parent id and
	// on_behalf_of), coordResult the planner's answer, coordErr its failure; summary is room_summary's.
	coordPlans  []CoordinationPlanRequest
	coordDrafts []string
	coordResult func(CoordinationPlanRequest) CoordinationPlanResult
	coordErr    error
	summary     *summary.Summary
	// intents is every intent_classify the App asked for, oldest first; intentResult is the answer and
	// intentErr the failure it answers instead.
	intents      []IntentRequest
	intentResult IntentResult
	intentErr    error
	stops        []Act
	retries      []Act
	answeredActs []Answer
	actAnswer    string
	actErr       error
}

func (b *stubBroker) Stop(_ context.Context, a Act) (ActResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stops = append(b.stops, a)
	if b.actErr != nil {
		return ActResult{}, b.actErr
	}
	if b.actAnswer != "" {
		return ActResult{Result: b.actAnswer}, nil
	}
	return ActResult{Result: "stopped"}, nil
}

func (b *stubBroker) Retry(_ context.Context, a Act) (ActResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.retries = append(b.retries, a)
	if b.actErr != nil {
		return ActResult{}, b.actErr
	}
	if b.actAnswer != "" {
		return ActResult{Result: b.actAnswer}, nil
	}
	return ActResult{Result: "retried"}, nil
}

func (b *stubBroker) Answer(_ context.Context, a Answer) (ActResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.answeredActs = append(b.answeredActs, a)
	if b.actErr != nil {
		return ActResult{}, b.actErr
	}
	if b.actAnswer != "" {
		return ActResult{Result: b.actAnswer}, nil
	}
	return ActResult{Result: "answered_restarted"}, nil
}

func (b *stubBroker) answeredCalls() []Answer {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Answer(nil), b.answeredActs...)
}

func (b *stubBroker) stoppedCalls() []Act {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Act(nil), b.stops...)
}

func (b *stubBroker) retriedCalls() []Act {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Act(nil), b.retries...)
}

func (b *stubBroker) IntentClassify(_ context.Context, in IntentRequest) (IntentResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.intents = append(b.intents, in)
	if b.intentErr != nil {
		return IntentResult{}, b.intentErr
	}
	return b.intentResult, nil
}

func (b *stubBroker) SlackLinkCode(_ context.Context, team, user, handle, name string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.links = append(b.links, team+"/"+user+"/"+handle+"/"+name)
	if b.linkErr != nil {
		return "", b.linkErr
	}
	return "https://rooms.example/link/slack?code=abcdefghijklmnopqrstuvwxyz", nil
}

func (b *stubBroker) SlackUnlink(_ context.Context, team, user string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.linkErr != nil {
		return false, b.linkErr
	}
	key := team + "/" + user
	was := b.unlinked[key]
	b.unlinked[key] = false
	return was, nil
}

func (b *stubBroker) refuse(onBehalfOf, text string) {
	b.mu.Lock()
	b.deny[onBehalfOf] = text
	b.mu.Unlock()
}

func (b *stubBroker) RunCreate(_ context.Context, c RunCreate) (RunResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, c)
	if b.runErr != nil {
		return RunResult{}, b.runErr
	}
	if b.replanned != nil {
		return RunResult{Replanned: b.replanned}, nil
	}
	if text, ok := b.deny[c.OnBehalfOf]; ok {
		b.refused = append(b.refused, c)
		return RunResult{}, Refused(text)
	}
	b.accepted = append(b.accepted, c)
	dup := b.keys[c.Key]
	b.keys[c.Key] = true
	room := b.room
	if room == "" {
		room = "aaaaaaaa"
	}
	return RunResult{Room: room, Issue: 42, Duplicate: dup, Sponsor: b.sponsor}, nil
}

// Decide records every press and answers the closed code the test told it to: a recorded decision by
// default, else the code the route answers (already_decided, not_supported, four_eyes…), or a
// transport error. fence is the store's own single-decision fence (D7): the first press is recorded
// and every later one answers already_decided, which is what makes the concurrent redelivery case
// one decision.
func (b *stubBroker) Decide(_ context.Context, d Decision) (DecisionResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.decisions = append(b.decisions, d)
	switch {
	case b.decideErr != nil:
		return DecisionResult{}, b.decideErr
	case b.fence:
		if b.fenced {
			b.answers = append(b.answers, "already_decided")
			return DecisionResult{Result: "already_decided"}, nil
		}
		b.fenced = true
	case b.decideAnswer != "":
		b.answers = append(b.answers, b.decideAnswer)
		return DecisionResult{Result: b.decideAnswer}, nil
	}
	b.answers = append(b.answers, d.Decision)
	return DecisionResult{Result: d.Decision}, nil
}

type handoffCheckCall struct {
	id         string
	onBehalfOf string
	workflow   *string
}

func (b *stubBroker) setPlan(p intake.Plan, degraded bool, workflows []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.storedPlan = p
	b.nextPlan = nil
	b.degraded = degraded
	b.workflows = workflows
}

func (b *stubBroker) setNextPlan(p intake.Plan, degraded bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := p
	b.nextPlan = &cp
	b.degraded = degraded
}

// Handoff records the request and advances storedPlan when nextPlan or handoffAnswer is set.
func (b *stubBroker) Handoff(_ context.Context, in HandoffRequest) (HandoffResult, error) {
	b.mu.Lock()
	gate, answer := b.handoffGate, b.handoffAnswer
	b.mu.Unlock()
	if gate != nil {
		gate()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, in)
	b.handoffs = append(b.handoffs, in)
	if b.handoffErr != nil {
		return HandoffResult{}, b.handoffErr
	}
	if answer != nil {
		b.storedPlan = answer(in)
	} else if b.nextPlan != nil {
		b.storedPlan = *b.nextPlan
		b.nextPlan = nil
	}
	text := "Repository " + b.storedPlan.Repository
	if b.degraded {
		text = intake.TemplateLabel + "\n" + text
	}
	wf := "default"
	if len(b.storedPlan.Issues) > 0 && b.storedPlan.Issues[0].Profile != "" {
		wf = b.storedPlan.Issues[0].Profile
	} else if len(b.storedPlan.Issues) > 0 && b.storedPlan.Issues[0].Suggestion != nil && b.storedPlan.Issues[0].Suggestion.Profile != "" {
		wf = b.storedPlan.Issues[0].Suggestion.Profile
	}
	var q *HandoffQuestion
	if b.storedPlan.Question != nil {
		q = &HandoffQuestion{Text: b.storedPlan.Question.Text, Recommended: b.storedPlan.Question.Recommended.Answer}
	}
	return HandoffResult{
		ID:       defaultHandoffID,
		Text:     text,
		Question: q,
		Ready:    q == nil && len(b.storedPlan.Gaps) == 0,
		Workflow: wf,
		Budget:   b.storedPlan.Budget.CeilingUSD,
	}, nil
}

func (b *stubBroker) HandoffCheck(_ context.Context, id, onBehalfOf string, workflow *string) (HandoffChecked, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var wfCopy *string
	if workflow != nil {
		v := *workflow
		wfCopy = &v
	}
	b.checks = append(b.checks, handoffCheckCall{id: id, onBehalfOf: onBehalfOf, workflow: wfCopy})
	if b.checkErr != nil {
		return HandoffChecked{}, b.checkErr
	}
	if len(b.storedPlan.Gaps) > 0 { // intake.CheckFinal, as the broker's handoff_check runs it
		return HandoffChecked{}, Refused("unresolved_marker: the plan still has a clarification to answer; " +
			"resolve every gap and remove every marker before filing")
	}
	if workflow != nil && len(b.storedPlan.Issues) > 0 && b.storedPlan.Issues[0].Profile != *workflow {
		next := b.storedPlan
		next.Issues = append([]intake.Issue(nil), b.storedPlan.Issues...)
		next.Issues[0].Profile = *workflow
		next.Issues[0].Body += "\n\n## Decisions\nD1: profile: " + *workflow + ", pinned by the developer"
		b.storedPlan = next
	}
	title := ""
	if len(b.storedPlan.Issues) > 0 {
		title = b.storedPlan.Issues[0].Title
	}
	hash := intake.Hash(b.storedPlan)
	plan := b.storedPlan
	plan.Hash = hash
	return HandoffChecked{
		Plan:       plan,
		Hash:       hash,
		Workflows:  b.workflows,
		Repository: plan.Repository,
		Title:      title,
	}, nil
}

func (b *stubBroker) handoffCalls() []HandoffRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]HandoffRequest(nil), b.handoffs...)
}

func (b *stubBroker) handoffChecks() []handoffCheckCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]handoffCheckCall(nil), b.checks...)
}

// answerWith makes the broker answer every press with code, as the route answers a refusal.
func (b *stubBroker) answerWith(code string) {
	b.mu.Lock()
	b.decideAnswer = code
	b.mu.Unlock()
}

// decideFails makes every press a transport failure: the broker was never reached.
func (b *stubBroker) decideFails(err error) {
	b.mu.Lock()
	b.decideErr = err
	b.mu.Unlock()
}

// fenceDecisions makes the broker answer like the store: one recorded decision, then already_decided.
func (b *stubBroker) fenceDecisions() {
	b.mu.Lock()
	b.fence = true
	b.mu.Unlock()
}

// decideAnswers is every closed code the broker answered, oldest first.
func (b *stubBroker) decideAnswers() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.answers...)
}

// decided is every press the broker was asked to record, oldest first.
func (b *stubBroker) decided() []Decision {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Decision(nil), b.decisions...)
}

func (b *stubBroker) newIssues() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.keys) }
func (b *stubBroker) created() int   { b.mu.Lock(); defer b.mu.Unlock(); return len(b.accepted) }
func (b *stubBroker) runCreates() []RunCreate {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]RunCreate(nil), b.accepted...)
}
func (b *stubBroker) all() []any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]any(nil), b.calls...)
}

type fakeThreads struct {
	mu      sync.Mutex
	m       map[string]Thread
	putFail int // the next n Puts fail
	puts    int
}

func (f *fakeThreads) Put(_ context.Context, room string, t Thread) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	if f.putFail > 0 {
		f.putFail--
		return errors.New("conflict")
	}
	f.m[room] = t
	return nil
}

func (f *fakeThreads) Get(_ context.Context, room string) (Thread, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.m[room]
	return t, ok, nil
}

func (f *fakeThreads) get(room string) (Thread, bool) {
	t, ok, _ := f.Get(context.Background(), room)
	return t, ok
}
func (f *fakeThreads) put(room string, t Thread) { _ = f.Put(context.Background(), room, t) }

type fakeMetrics struct {
	mu sync.Mutex
	n  map[string]int
}

func (m *fakeMetrics) Request(_ context.Context, route, outcome string) {
	m.mu.Lock()
	m.n[route+"/"+outcome]++
	m.mu.Unlock()
}

func (m *fakeMetrics) count(route, outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.n[route+"/"+outcome]
}

// ProposalUpdate is maku_slack_proposal_updates_total{action}, recorded as its one label reads.
func (m *fakeMetrics) ProposalUpdate(_ context.Context, action string) {
	m.mu.Lock()
	m.n["proposal/"+action]++
	m.mu.Unlock()
}

// ThreadAnswer is maku_slack_thread_answers_total{question}.
func (m *fakeMetrics) ThreadAnswer(_ context.Context, question string) {
	m.mu.Lock()
	m.n["answer/"+question]++
	m.mu.Unlock()
}

// SlackIntent is maku_slack_intents_total{tier, intent, outcome}, recorded as "tier/intent/outcome".
func (m *fakeMetrics) SlackIntent(_ context.Context, tier, intent, outcome string) {
	m.mu.Lock()
	m.n["intent/"+tier+"/"+intent+"/"+outcome]++
	m.mu.Unlock()
}

// counted is every count recorded under route, by the label it was recorded with: what a test reads
// to say "this action and nothing else".
func (m *fakeMetrics) counted(route string) map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for k, v := range m.n {
		if label, ok := strings.CutPrefix(k, route+"/"); ok {
			out[label] = v
		}
	}
	return out
}

type appRig struct {
	t       *testing.T
	app     *App
	slack   *fakeSlack
	broker  *stubBroker
	threads *fakeThreads
	metrics *fakeMetrics
	key     []byte
	now     time.Time
	seq     int
	lastWH  struct {
		body string
		h    http.Header
	}
}

const rigSecret = "slack-signing-secret"

func newAppRig(t *testing.T, ch Channel) *appRig {
	t.Helper()
	r := &appRig{t: t, now: time.Unix(1_790_000_000, 0), key: []byte("0123456789abcdef0123456789abcdef"),
		slack:   &fakeSlack{msgs: map[string]Message{"1729.0001": {TS: "1729.0001", User: "U02DEF", Text: "a human"}}},
		broker:  &stubBroker{deny: map[string]string{}, keys: map[string]bool{}, unlinked: map[string]bool{}},
		threads: &fakeThreads{m: map[string]Thread{}}, metrics: &fakeMetrics{n: map[string]int{}}}
	r.app = &App{Slack: r.slack, Broker: r.broker, Threads: r.threads,
		Verifier:    Verifier{Secret: func() string { return rigSecret }, Now: func() time.Time { return r.now }},
		WebhookKeys: func() ([][]byte, error) { return [][]byte{r.key}, nil },
		TeamID:      "T01ABC", BotUserID: "U0BOT", RoomsURL: "https://rooms.example", Channels: map[string]Channel{"C03": ch},
		Async: func(f func()) { f() }, Now: func() time.Time { return r.now }, Metrics: r.metrics}
	return r
}

func (r *appRig) eventBody(id, event string) string {
	return r.eventBodyTeam("T01ABC", id, event)
}

func (r *appRig) eventBodyTeam(team, id, event string) string {
	return `{"type":"event_callback","team_id":"` + team + `","event_id":"` + id + `","event":` + event + `}`
}

func (r *appRig) postEvents(body string, signed bool) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/slack/events", strings.NewReader(body))
	if signed {
		ts := strconv.FormatInt(r.now.Unix(), 10)
		req.Header.Set("X-Slack-Request-Timestamp", ts)
		req.Header.Set("X-Slack-Signature", slackSign(rigSecret, ts, body))
	}
	w := httptest.NewRecorder()
	r.app.Events().ServeHTTP(w, req)
	return w
}

func (r *appRig) event(event string) {
	r.t.Helper()
	r.seq++
	if st := r.postEvents(r.eventBody("Ev"+strconv.Itoa(r.seq), event), true).Code; st != 200 {
		r.t.Fatalf("events: %d", st)
	}
}

// formBody is the body Slack's interactivity sends: one url-encoded payload field. Shared by the
// events door's second body shape and the tests of it.
func formBody(payload string) string { return url.Values{"payload": {payload}}.Encode() }

// postInteraction posts body to the events door as Slack's interactivity does: form-encoded and
// signed at the rig's clock.
func (r *appRig) postInteraction(body string) int {
	r.t.Helper()
	return r.postInteractionAs(body, formCT, r.now.Unix(), true)
}

// postInteractionAs is postInteraction with the content type, the clock the signature claims and
// whether it carries one at all: the ways a press can be wrong before its body is read.
func (r *appRig) postInteractionAs(body, contentType string, at int64, signed bool) int {
	r.t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/slack/events", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if signed {
		ts := strconv.FormatInt(at, 10)
		req.Header.Set("X-Slack-Request-Timestamp", ts)
		req.Header.Set("X-Slack-Signature", slackSign(rigSecret, ts, body))
	}
	w := httptest.NewRecorder()
	r.app.Events().ServeHTTP(w, req)
	return w.Code
}

// interact is one button press: the payload Slack sends, in the body it sends it in.
func (r *appRig) interact(payload string) int {
	r.t.Helper()
	return r.postInteraction(formBody(payload))
}

// formCT is the content type Slack's button presses carry.
const formCT = "application/x-www-form-urlencoded"

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

func (r *appRig) mention(channel, ts, text string) {
	r.t.Helper()
	r.event(`{"type":"app_mention","channel":"` + channel + `","user":"U02DEF","ts":"` + ts + `","text":` + jsonString(text) + `}`)
}

func (r *appRig) react(channel, itemTS, user, reaction string) {
	r.t.Helper()
	r.event(`{"type":"reaction_added","user":"` + user + `","reaction":"` + reaction + `","item":{"type":"message","channel":"` + channel + `","ts":"` + itemTS + `"}}`)
}

func (r *appRig) message(channel, threadTS, user, text string) {
	r.t.Helper()
	r.event(`{"type":"message","channel":"` + channel + `","user":"` + user + `","thread_ts":"` + threadTS + `","ts":"1729.5000","text":` + jsonString(text) + `}`)
}

func (r *appRig) lastPost() post {
	r.t.Helper()
	p := r.posts()
	if len(p) == 0 {
		r.t.Fatal("nothing was posted")
	}
	return p[len(p)-1]
}

func (r *appRig) posts() []post {
	r.slack.mu.Lock()
	defer r.slack.mu.Unlock()
	return append([]post(nil), r.slack.posts...)
}

func (r *appRig) postWebhook(body string, h http.Header) int {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/factory", strings.NewReader(body))
	for k, v := range h {
		req.Header[k] = v
	}
	w := httptest.NewRecorder()
	r.app.Webhooks().ServeHTTP(w, req)
	return w.Code
}

func (r *appRig) webhook(body string) {
	r.t.Helper()
	if st := r.webhookStatus(body, "signed"); st != 200 {
		r.t.Fatalf("webhook: %d", st)
	}
}

func (r *appRig) webhookStatus(body, mode string) int {
	r.seq++
	var h http.Header
	if mode == "signed" {
		h = webhook.Sign([][]byte{r.key}, "evt_"+strconv.Itoa(r.seq), r.now, []byte(body))
		r.lastWH.body, r.lastWH.h = body, h
	}
	return r.postWebhook(body, h)
}

func (r *appRig) webhookReplay() int { return r.postWebhook(r.lastWH.body, r.lastWH.h) }

func (r *appRig) eventTeam(team, event string) {
	r.t.Helper()
	r.seq++
	if st := r.postEvents(r.eventBodyTeam(team, "Ev"+strconv.Itoa(r.seq), event), true).Code; st != 200 {
		r.t.Fatalf("events: %d", st)
	}
}

const draftText = "<@U0BOT> Smana/ai-native-ref docs(agents): the link is dead\n## What is wrong\nx\n## What it should be\ny\n## Acceptance check\nz"

// M1: a thread-map failure after run_create must not leave the room unfollowed forever: the next
// ✅ is a duplicate and puts the thread again.
func TestDuplicateReactionPutsTheThread(t *testing.T) {
	rig, _, ts := proposalSeeded(t)
	rig.broker.room = "aaaaaaaa"
	rig.slack.failProposalUpdates, rig.slack.failPosts = 1, 1 // the proposal still reads as one: a second ✅ is a duplicate
	rig.threads.putFail = 1
	rig.react("C03", ts, "U02DEF", "white_check_mark")
	if _, ok := rig.threads.get("aaaaaaaa"); ok {
		t.Fatal("the failed Put left an entry")
	}
	rig.react("C03", ts, "U07GHI", "white_check_mark")
	if th, ok := rig.threads.get("aaaaaaaa"); !ok || th.TS != "1729.0001" {
		t.Fatalf("a duplicate did not restore the thread: %+v", th)
	}
}

// M2: an unreadable key file is the App's fault, not a bad signature: 503, counted no_key.
func TestWebhookKeyProblemIs503(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.app.WebhookKeys = func() ([][]byte, error) { return nil, errors.New("no such file") }
	if st := rig.webhookStatus(`{"type":"room.run_started","room":"aaaaaaaa"}`, "signed"); st != 503 {
		t.Fatalf("%d", st)
	}
	if rig.metrics.count("webhooks", "no_key") != 1 || rig.metrics.count("webhooks", "bad_signature") != 0 {
		t.Fatal("outcome")
	}
}

// M4: one workspace. An event from another team (a Slack Connect peer) does nothing.
func TestOtherWorkspaceIsDropped(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.mention("C03", "1729.0001", draftText)
	draft := rig.lastPost()
	n := len(rig.posts())
	rig.eventTeam("T09OTHER", `{"type":"app_mention","channel":"C03","user":"U02DEF","ts":"1729.0002","text":`+jsonString(draftText)+`}`)
	rig.eventTeam("T09OTHER", `{"type":"reaction_added","user":"U02DEF","reaction":"white_check_mark","item":{"type":"message","channel":"C03","ts":"`+draft.ts+`"}}`)
	if len(rig.posts()) != n || len(rig.broker.all()) != 1 { // the one IssueDraft of the first mention
		t.Fatalf("a foreign workspace's event was handled: %d posts, %d calls", len(rig.posts()), len(rig.broker.all()))
	}
}

// M5: a mention from a malformed user id, or any event with bot_id, is ignored.
func TestMentionSenderChecks(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.event(`{"type":"app_mention","channel":"C03","user":"u<!channel>","ts":"1729.0001","text":` + jsonString(draftText) + `}`)
	rig.event(`{"type":"app_mention","channel":"C03","user":"U02DEF","bot_id":"B01","ts":"1729.0002","text":` + jsonString(draftText) + `}`)
	rig.event(`{"type":"reaction_added","user":"U02DEF","bot_id":"B01","reaction":"white_check_mark","item":{"type":"message","channel":"C03","ts":"1729.0001"}}`)
	if len(rig.posts()) != 0 || len(rig.broker.all()) != 0 {
		t.Fatal("handled")
	}
}

// M7: a task was filed under a room id that cannot be followed: say so, with no free text.
func TestUnfollowableRoomIsSaid(t *testing.T) {
	rig, _, ts := proposalSeeded(t)
	rig.broker.room = "../evil <!channel>"
	rig.react("C03", ts, "U02DEF", "white_check_mark")
	p := rig.lastPost().text
	if !strings.Contains(p, "filed") || !strings.Contains(p, "cannot be followed") || strings.Contains(p, "evil") || strings.Contains(p, "channel>") {
		t.Fatalf("%q", p)
	}
	if len(rig.threads.m) != 0 {
		t.Fatal("an invalid room was mapped")
	}
}

// M8: a Slack failure is a 502, the id is forgotten, and the redelivery posts.
func TestWebhookRedeliveryAfterSlackFailure(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
	rig.slack.failPosts = 1
	body := `{"type":"room.merged","room":"aaaaaaaa","pr":7}` // a transition the default channel posts
	if st := rig.webhookStatus(body, "signed"); st != 502 {
		t.Fatalf("%d", st)
	}
	if st := rig.webhookReplay(); st != 200 || len(rig.posts()) != 1 {
		t.Fatalf("redelivery: %d, %d posts", st, len(rig.posts()))
	}
}

// M8: a delivery whose timestamp is outside the tolerance is refused by the App.
func TestWebhookStaleTimestamp(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
	body := `{"type":"room.run_started","room":"aaaaaaaa"}`
	h := webhook.Sign([][]byte{rig.key}, "evt_stale", rig.now.Add(-10*time.Minute), []byte(body))
	if st := rig.postWebhook(body, h); st != 401 || len(rig.posts()) != 0 {
		t.Fatalf("%d", st)
	}
}

// ---- draft editing in the thread ----

func (r *appRig) reply(user, text string) {
	r.t.Helper()
	r.seq++
	r.event(`{"type":"message","channel":"C03","user":"` + user + `","thread_ts":"1729.0001","ts":"1729.9` + strconv.Itoa(100+r.seq) + `","text":` + jsonString(text) + `}`)
}

// With proposals on, a thread too long to read may be a proposal, a filed one asking a question, or a
// draft: the App cannot tell which, so its one line names none of them and says where to go instead.
func TestLongProposalThreadSaysNoDraft(t *testing.T) {
	rig, _, _ := proposalAt(t, 1)
	rig.slack.seed(Message{TS: "1729.0003", ThreadTS: proposalThreadTS, User: "U0BOT", Text: "Filed #42",
		Metadata: metaOf(t, filedEventType, MarkerPayload{Repo: "Smana/ai-native-ref", Room: "26zfnuxm"})})
	rig.slack.threadErr = ErrThreadTooLong
	rig.replySaid("U02DEF", "1729.9001", "status")
	if p := rig.lastPost(); p.text != threadTooLongLine || strings.Contains(p.text, "draft") {
		t.Fatalf("the reply got %q, want %q", p.text, threadTooLongLine)
	}
	if got := rig.metrics.count("events", "refused"); got != 1 {
		t.Fatalf("events/refused counted %d times, want 1", got)
	}
}

// ---- a check mark typed as a reply ----

// ---- the ✅ fallback on a proposal ----

// proposalSeeded is a rig serving the fixture's repository and holding one proposal message in a
// thread: the message a ✅ is added to. The room the broker answers is the plan's own example, so a
// line naming it is exact.
func proposalSeeded(t *testing.T) (rig *appRig, plan intake.Plan, ts string) {
	t.Helper()
	rig = newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.broker.room = "26zfnuxm"
	plan = planFixture(t, "slack-one-issue")
	rig.broker.setPlan(plan, false, nil)
	p, err := newProposal(plan, intake.Hash(plan), "U02DEF", 1)
	if err != nil {
		t.Fatalf("newProposal: %v", err)
	}
	ts = "1729.0002"
	rig.slack.seed(Message{TS: ts, ThreadTS: "1729.0001", User: "U0BOT", Text: "Proposal v1 · " + p.Hash,
		Metadata: metaOf(t, proposalEventType, p)})
	return rig, plan, ts
}

// CD8's fallback and acceptance 10's last row: a ✅ on a proposal files it through the same
// run_create validation the Confirm uses, and the proposal message becomes the Filed line in place.
func TestCheckMarkFilesAProposal(t *testing.T) {
	t.Run("a maintainer's ✅ files the plan the message showed", func(t *testing.T) {
		rig, plan, ts := proposalSeeded(t)
		rig.react("C03", ts, "U02DEF", "white_check_mark")

		calls := rig.broker.runCreates()
		if len(calls) != 1 {
			t.Fatalf("%d run_create calls, want one: %+v", len(calls), rig.broker.all())
		}
		c := calls[0]
		if c.Handoff != defaultHandoffID || c.PlanHash != intake.Hash(plan) {
			t.Fatalf("the call's handoff/hash is %q/%q, want %q/%q", c.Handoff, c.PlanHash, defaultHandoffID, intake.Hash(plan))
		}
		if c.Repo != plan.Repository || c.Title != "" || c.Body != "" ||
			c.MaxCostUSD != 2 || c.OnBehalfOf != "slack:T01ABC/U02DEF" ||
			c.Ref != "slack/T01ABC/C03/1729.0001" || c.Key != "T01ABC/C03/1729.0001" {
			t.Fatalf("the call: %+v", c)
		}

		// The proposal message is the Filed line, in place: one message, no second line.
		edits := rig.slack.proposalEdits()
		if len(edits) != 1 || edits[0].ts != ts || edits[0].channel != "C03" {
			t.Fatalf("the proposal was not rewritten in place: %+v", edits)
		}
		if edits[0].meta == nil || edits[0].meta.EventType != filedEventType {
			t.Fatalf("the rewritten message's tag: %+v", edits[0].meta)
		}
		if mark, ok := edits[0].meta.markerPayload(); !ok || mark.Room != "26zfnuxm" || mark.Repo != plan.Repository {
			t.Fatalf("the rewritten message's marker: %+v %v", mark, ok)
		}
		// No button survives the filing (Review Focus 1): an emptied block list is Slack's way to take
		// a message's blocks off, and an absent key would leave Confirm and Cancel live on a message
		// whose plan is filed.
		if string(edits[0].blocks) != "[]" {
			t.Fatalf("the filed message's blocks are %q, want an emptied list", edits[0].blocks)
		}
		if want := "Filed #42 for <@U02DEF>; the factory starts it. Room: https://rooms.example/r/26zfnuxm"; edits[0].text != want {
			t.Fatalf("the filed line is %q, want %q", edits[0].text, want)
		}
		if n := len(rig.posts()); n != 0 {
			t.Fatalf("%d posts: filing rewrites the message, it does not post beside it", n)
		}
		if th, ok := rig.threads.get("26zfnuxm"); !ok || th.Channel != "C03" || th.TS != "1729.0001" {
			t.Fatalf("the thread map: %+v %v", th, ok)
		}
	})

	// The duplicate the store answers, reached the way it is reached today: the message could not be
	// rewritten and the Filed line it falls back to was lost too, so the thread still reads the
	// proposal and a second ✅ files the same key again. One issue, whatever the revision.
	t.Run("a second ✅ the store already knows answers Already filed", func(t *testing.T) {
		rig, _, ts := proposalSeeded(t)
		rig.slack.failProposalUpdates, rig.slack.failPosts = 1, 1
		rig.react("C03", ts, "U02DEF", "white_check_mark")
		if n := len(rig.broker.runCreates()); n != 1 || rig.broker.newIssues() != 1 {
			t.Fatalf("%d calls, %d issues", n, rig.broker.newIssues())
		}
		rig.react("C03", ts, "U07GHI", "white_check_mark")
		if n := len(rig.broker.runCreates()); n != 2 || rig.broker.newIssues() != 1 {
			t.Fatalf("%d calls, %d issues: a duplicate filed a second task", n, rig.broker.newIssues())
		}
		edits := rig.slack.proposalEdits()
		if len(edits) != 1 || !strings.Contains(edits[0].text, "Already filed: https://rooms.example/r/26zfnuxm") {
			t.Fatalf("the duplicate's line: %+v", edits)
		}
	})

	// CD20: the message's metadata is the proposal, and its hash is the whole of the check. A payload
	// whose hash does not bind its plan is refused with its own line and nothing is called.
	t.Run("a proposal whose hash does not hold is refused, nothing called", func(t *testing.T) {
		rig, plan, _ := proposalSeeded(t)
		forged := ProposalPayload{ID: defaultHandoffID, Repo: plan.Repository, Hash: "sha256:bad", Author: "U02DEF", Rev: 2}
		rig.slack.seed(Message{TS: "1729.0003", ThreadTS: "1729.0001", User: "U0BOT", Text: "Proposal v2",
			Metadata: metaOf(t, proposalEventType, forged)})
		rig.react("C03", "1729.0003", "U02DEF", "white_check_mark")
		if len(rig.broker.all()) != 0 {
			t.Fatalf("a stale proposal reached the broker: %+v", rig.broker.all())
		}
		if p := rig.lastPost(); !strings.HasPrefix(p.text, "Not filed: this proposal no longer matches its check") || p.meta != nil {
			t.Fatalf("%+v", p)
		}
	})

	// A thread whose proposal was already filed or cancelled says so in the message itself: a ✅ on it
	// is ignored, with no line and no broker call, exactly as today's ✅ on a message that is not the
	// App's draft.
	t.Run("a ✅ on a proposal whose thread closed it is ignored", func(t *testing.T) {
		rig, _, ts := proposalSeeded(t)
		rig.slack.seed(Message{TS: "1729.0003", ThreadTS: "1729.0001", User: "U0BOT",
			Text:     "Proposal cancelled by <@U02DEF>",
			Metadata: metaOf(t, filedEventType, MarkerPayload{Repo: "Smana/ai-native-ref"})})
		ignored := rig.metrics.count("events", "ignored")
		rig.react("C03", ts, "U02DEF", "white_check_mark")
		if len(rig.broker.all()) != 0 || len(rig.posts()) != 0 || len(rig.slack.proposalEdits()) != 0 {
			t.Fatalf("a closed proposal was filed or answered: %+v %+v", rig.posts(), rig.slack.proposalEdits())
		}
		if rig.metrics.count("events", "ignored") != ignored+1 {
			t.Fatal("the ✅ on a closed thread was not ignored")
		}
	})

	// §Failure and refusal cases: the broker's own plan check, said with its own line.
	t.Run("the broker's plan_changed has its own line", func(t *testing.T) {
		rig, _, ts := proposalSeeded(t)
		rig.broker.refuse("slack:T01ABC/U02DEF", "rejected: plan_changed")
		rig.react("C03", ts, "U02DEF", "white_check_mark")
		if rig.broker.newIssues() != 0 {
			t.Fatal("a refused plan filed an issue")
		}
		want := "Not filed: the plan changed after this was drawn; mention me again to re-plan it."
		if p := rig.lastPost(); p.text != want || p.meta != nil {
			t.Fatalf("%q", p.text)
		}
	})

	// The same fallback, typed: a ✅ reply in a proposal's thread finds the proposal and files it, so
	// the two shapes of the check mark are one behaviour.
	t.Run("a ✅ typed as a reply files the proposal too", func(t *testing.T) {
		rig, _, _ := proposalSeeded(t)
		rig.reply("U02DEF", "✅")
		if n := len(rig.broker.runCreates()); n != 1 || rig.broker.newIssues() != 1 {
			t.Fatalf("%d calls, %d issues", n, rig.broker.newIssues())
		}
	})
}

// ---- a mention plans, and posts one proposal (CD1, §the Slack journey) ----

// mentionWords is the request every test here sends: a sentence a person would write, with no
// repository, no title and no grammar in it.
const mentionWords = "the install link in the README is dead"

// withProposals is a rig with the proposal flow on and a planner answering this package's fixture.
// The channel serves two repositories, so a candidate set that was the plan's own repository rather
// than the channel's is visible in the call.
func withProposals(t *testing.T) (*appRig, intake.Plan) {
	t.Helper()
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref", "Smana/cloud-native-ref"}, MaxCostUSD: 2})
	plan := planFixture(t, "slack-one-issue")
	rig.broker.setPlan(plan, false, nil)
	return rig, plan
}

// CD1 and the Slack journey: with proposals on, a mention in plain words is planned by the broker and
// posted as one proposal — the App parses no repository line, guesses no repository and files nothing.
func TestMentionProposes(t *testing.T) {
	t.Run("a mention in plain words is planned and posted as one proposal", func(t *testing.T) {
		rig, plan := withProposals(t)
		rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)

		posts := rig.posts()
		if len(posts) != 1 {
			t.Fatalf("%d posts, want the one proposal: %+v", len(posts), posts)
		}
		p := posts[0]
		if p.channel != "C03" || p.thread != "1729.0001" || len(p.blocks) == 0 {
			t.Fatalf("the proposal is not one card in the mention's thread: %+v", p)
		}
		if p.meta == nil || p.meta.EventType != proposalEventType {
			t.Fatalf("the message's tag: %+v", p.meta)
		}
		prop, ok := p.meta.proposalPayload()
		if !ok || prop.Rev != 1 || prop.Author != "U02DEF" || prop.Hash != intake.Hash(plan) {
			t.Fatalf("the message's payload: %+v (%v)", prop, ok)
		}
		for _, want := range []string{plan.Repository, plan.Issues[0].Title, actionProposalConfirm, actionProposalCancel} {
			if !strings.Contains(string(p.blocks), want) {
				t.Fatalf("the blocks do not carry %q: %s", want, p.blocks)
			}
		}

		// The call is intake_plan's contract: the requester's own words, the channel's repositories
		// as the candidate set, the App's principal and the channel's ceiling. No repository the App
		// picked, and no context it does not have — a mention is the person's words alone (CD1).
		calls := rig.broker.handoffCalls()
		if len(calls) != 1 {
			t.Fatalf("%d handoff calls, want one", len(calls))
		}
		want := HandoffRequest{Text: mentionWords, OnBehalfOf: "slack:T01ABC/U02DEF",
			Candidates: []string{"Smana/ai-native-ref", "Smana/cloud-native-ref"}, CeilingUSD: 2}
		if !reflect.DeepEqual(calls[0], want) {
			t.Fatalf("the handoff request is %+v, want %+v", calls[0], want)
		}
		if n := len(rig.broker.all()); n != 1 { // the handoff, and nothing else: no draft, no filing
			t.Fatalf("%d broker calls, want the handoff alone: %+v", n, rig.broker.all())
		}
		if got := rig.metrics.count("events", "proposed"); got != 1 {
			t.Fatalf("the proposal went uncounted: %d", got)
		}
	})

}

// A thread is the kind it started as. The planner went away after a proposal was posted: a reply
// cannot re-plan it, and a mention there plans nothing beside it — both say the same line, which
// names the two ways left.
func TestProposalThreadWithoutPlanner(t *testing.T) {
	t.Run("a proposal thread whose planner went away plans nothing", func(t *testing.T) {
		rig, _, _ := proposalAt(t, 1)
		rig.broker.handoffErr = ErrUnknownTool
		rig.replySaid("U02DEF", "1729.9001", "it belongs in owner/docs")
		if p := rig.lastPost(); p.text != planUnsupported || p.meta != nil {
			t.Fatalf("the reply got %+v, want %q", p, planUnsupported)
		}
		rig.mention("C03", proposalThreadTS, "<@U0BOT> Smana/ai-native-ref docs: fix a dead link")
		posts := rig.posts()
		if len(posts) != 2 || posts[1].text != planUnsupported || posts[1].meta != nil {
			t.Fatalf("the mention got %+v, want the one line %q", posts, planUnsupported)
		}
		if n := len(rig.broker.calls); n != 2 { // the two handoffs asked for
			t.Fatalf("%d broker calls, want the two handoff calls: %+v", n, rig.broker.all())
		}
		if got := rig.metrics.count("events", "refused"); got != 2 {
			t.Fatalf("events/refused counted %d times, want 2", got)
		}
	})

	t.Run("a reply in a proposal thread re-plans in place", func(t *testing.T) {
		rig, _, ts := proposalAt(t, 1)
		rig.replySaid("U02DEF", "1729.9001", "Acceptance: the link resolves")
		if n := len(rig.posts()); n != 0 {
			t.Fatalf("%d posts: the reply did not re-plan in place", n)
		}
		if edits := rig.slack.proposalEdits(); len(edits) != 1 || edits[0].ts != ts {
			t.Fatalf("the proposal was not re-planned in place: %+v", edits)
		}
	})
}

// The refusals a person actually meets, one fixed line each, and the one that names where to go
// instead: a private repository, a model that is down, a plan too large, more than one issue.
func TestProposalRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, b *stubBroker)
		want    string
		outcome string
	}{
		{
			name: "a private repository points at the coding agent",
			setup: func(_ *testing.T, b *stubBroker) {
				b.handoffErr = Refused("private_repository_unsupported: the planner's internal route is not configured, and this repository is not public; configure human.intake.plan.internal to plan private repositories, or file the issue on GitHub")
			},
			want:    "Not proposed: the planner's internal route is not configured for private repositories; ask an operator to enable human.intake.plan.internal, or file the issue on GitHub.",
			outcome: "refused",
		},
		{
			name: "planner_unavailable says when to mention again",
			setup: func(_ *testing.T, b *stubBroker) {
				b.handoffErr = Refused("planner_unavailable: the plan service is unavailable; try again later")
			},
			want:    "Not proposed: the plan service is unavailable; mention me again in a minute.",
			outcome: "error", // a passing failure: the person's fix is to mention again
		},
		{
			name: "planner_timeout has its own line",
			setup: func(_ *testing.T, b *stubBroker) {
				b.handoffErr = Refused("planner_timeout: the plan service did not answer inside its deadline; try again")
			},
			want:    "Not proposed: the plan service did not answer in time; mention me again in a minute.",
			outcome: "error",
		},
		{
			name: "planner_invalid has its own line",
			setup: func(_ *testing.T, b *stubBroker) {
				b.handoffErr = Refused("planner_invalid: the plan is not the shape the contract asks for (issues: 0, the contract asks for 1 to 3); ask again")
			},
			want:    "Not proposed: the plan service answered something that is not a plan; mention me again, or hand it off from your coding agent.",
			outcome: "refused",
		},
		{
			name: "daily_plans_reached has its own line",
			setup: func(_ *testing.T, b *stubBroker) {
				b.handoffErr = Refused("daily_plans_reached: you have used today's plans; the cap resets at 00:00 UTC")
			},
			want:    "Not proposed: you have used today's plans; mention me again after 00:00 UTC, or hand it off from your coding agent.",
			outcome: "refused",
		},
		{
			name: "an unknown refusal is shown as the broker gave it",
			setup: func(_ *testing.T, b *stubBroker) {
				b.handoffErr = Refused(`invalid_class: the class is not one this broker's config defines (issue 1: class "nope")`)
			},
			want:    `Not proposed: invalid_class: the class is not one this broker's config defines (issue 1: class "nope")`,
			outcome: "refused",
		},
		{
			name: "a plan with two issues is refused whole",
			setup: func(t *testing.T, b *stubBroker) {
				plan := clonePlan(t, b.storedPlan)
				second := plan.Issues[0]
				second.Index, second.DependsOn = 2, []int{1}
				plan.Issues = append(plan.Issues, second)
				b.setPlan(plan, false, nil)
			},
			want:    "Proposal not posted: this plan covers 2 issues and Slack files one: use your coding agent or New task on https://rooms.example.",
			outcome: "multi_issue",
		},
		{
			name: "a plan over 7 KiB is refused whole",
			setup: func(t *testing.T, b *stubBroker) {
				plan := clonePlan(t, b.storedPlan)
				plan.Issues[0].Body = strings.Repeat("a", maxProposalPlanBytes)
				b.setPlan(plan, false, nil)
			},
			want:    "Proposal not posted: the plan is too long for Slack; use your coding agent or the web UI.",
			outcome: "too_large",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, _ := withProposals(t)
			tc.setup(t, rig.broker)
			rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)
			posts := rig.posts()
			if len(posts) != 1 || posts[0].text != tc.want || posts[0].meta != nil {
				t.Fatalf("the thread says %+v, want one line %q and no metadata", posts, tc.want)
			}
			if n := len(rig.broker.handoffCalls()); n != 1 {
				t.Fatalf("%d handoff calls, want the one the refusal came from", n)
			}
			if n := len(rig.broker.runCreates()); n != 0 {
				t.Fatalf("a refused plan filed %d issues", n)
			}
			if got := rig.metrics.count("events", tc.outcome); got != 1 {
				t.Fatalf("the refusal was counted %d times as %q", got, tc.outcome)
			}
		})
	}

	// The bound is measured on the planner's input, not on the HTTP body: a request past intake_plan's
	// 16 KiB is refused here, before the broker is called at all.
	t.Run("a request past 16 KiB is refused before the planner is called", func(t *testing.T) {
		rig, _ := withProposals(t)
		rig.mention("C03", "1729.0001", "<@U0BOT> "+strings.Repeat("a", maxProposalRequestBytes+1))
		posts := rig.posts()
		if len(posts) != 1 || posts[0].text != RequestedTextTooLong.Error() || posts[0].meta != nil {
			t.Fatalf("the thread says %+v, want %q", posts, RequestedTextTooLong.Error())
		}
		if n := len(rig.broker.handoffCalls()); n != 0 {
			t.Fatalf("%d handoff calls for an oversized request", n)
		}
		if got := rig.metrics.count("events", "too_large"); got != 1 {
			t.Fatalf("the refusal was counted %d times as too_large", got)
		}
	})

	// The idempotency key is the thread, so a thread whose task is filed plans nothing more: the line
	// names the room the marker holds, which is the one place a closed thread says where it went.
	t.Run("a thread that already filed its issue names the room", func(t *testing.T) {
		rig, _ := withProposals(t)
		rig.slack.seed(Message{TS: "1729.0002", ThreadTS: "1729.0001", User: "U0BOT",
			Text:     "Filed #42 for <@U02DEF>; the factory starts it. Room: https://rooms.example/r/26zfnuxm",
			Metadata: metaOf(t, filedEventType, MarkerPayload{Repo: "Smana/ai-native-ref", Room: "26zfnuxm"})})
		rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)
		want := "Not proposed: this thread's task is already filed, in room https://rooms.example/r/26zfnuxm; start a new mention for another."
		if p := rig.lastPost(); p.text != want || p.meta != nil {
			t.Fatalf("%q, want %q", p.text, want)
		}
		if n := len(rig.broker.handoffCalls()); n != 0 {
			t.Fatalf("a closed thread planned %d times", n)
		}
	})

	// CD18: a degraded plan is the composer's template, and the proposal says so in its header — the
	// person is never left to believe a planner read the request.
	t.Run("a degraded plan posts, and its header says it is a template", func(t *testing.T) {
		rig, plan := withProposals(t)
		rig.broker.setPlan(plan, true, nil)
		rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)
		posts := rig.posts()
		if len(posts) != 1 || len(posts[0].blocks) == 0 {
			t.Fatalf("%d posts, want the one proposal: %+v", len(posts), posts)
		}
		if !strings.Contains(string(posts[0].blocks), "template draft: the plan service is unavailable") {
			t.Fatalf("the header does not say the plan is a template: %s", posts[0].blocks)
		}
		prop, ok := posts[0].meta.proposalPayload()
		if !ok || !prop.Degraded {
			t.Fatalf("the payload does not carry the label a restart reads: %+v (%v)", prop, ok)
		}
	})
}

// ---- a reply re-plans the one proposal message (CD7, CD14) ----

// proposalThreadTS is the parent's ts in every proposal test: the mention's own message, which is
// what a re-plan reads as the request.
const proposalThreadTS = "1729.0001"

// proposalAt is a rig with the proposal flow on, holding the thread an App that has just restarted
// reads: the mention as the parent and one proposal at rev carrying the fixture's plan. Nothing of
// the thread is in the App's memory, so every assertion below is about the metadata alone (CD20).
func proposalAt(t *testing.T, rev int) (rig *appRig, plan intake.Plan, ts string) {
	t.Helper()
	rig, plan = withProposals(t)
	rig.slack.seed(Message{TS: proposalThreadTS, User: "U02DEF", Text: "<@U0BOT> " + mentionWords})
	p, err := newProposal(plan, intake.Hash(plan), "U02DEF", rev)
	if err != nil {
		t.Fatalf("newProposal: %v", err)
	}
	ts = "1729.0002"
	rig.slack.seed(Message{TS: ts, ThreadTS: proposalThreadTS, User: "U0BOT",
		Text: "Proposal v" + strconv.Itoa(rev) + " · " + p.Hash, Metadata: metaOf(t, proposalEventType, p)})
	return rig, plan, ts
}

// replySaid is a reply the thread really holds: the message is in the fake, as conversations.replies
// returns it, and the event that carries it arrives after, as Slack's does.
func (r *appRig) replySaid(user, ts, text string) {
	r.t.Helper()
	r.slack.seed(Message{TS: ts, ThreadTS: proposalThreadTS, User: user, Text: text})
	r.seq++
	r.event(`{"type":"message","channel":"C03","user":"` + user + `","thread_ts":"` + proposalThreadTS +
		`","ts":"` + ts + `","text":` + jsonString(text) + `}`)
}

// replyEvent is a reply as the events door decodes it: what a worker hands the flow, so a test can
// run two of them at once.
func replyEvent(user, ts, text string) slackEvent {
	return slackEvent{Type: "message", Channel: "C03", User: user, ThreadTS: proposalThreadTS, TS: ts, Text: text}
}

// CD7 and CD14: a reply is the same message's next revision, with the sections that moved marked and
// the header saying a new confirmation is required; five replies are still one message.
func TestReplyReplansInPlace(t *testing.T) {
	t.Run("a reply re-plans the one message and marks what moved", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		moved := clonePlan(t, plan)
		moved.Repository = "Smana/cloud-native-ref" // the channel's other repository: what the reply named
		rig.broker.setNextPlan(moved, false)
		rig.replySaid("U02DEF", "1729.9001", "it belongs in owner/docs")

		if n := len(rig.posts()); n != 0 {
			t.Fatalf("%d posts: a re-plan edits the message, it does not post beside it", n)
		}
		edits := rig.slack.proposalEdits()
		if len(edits) != 1 || edits[0].ts != ts || edits[0].channel != "C03" {
			t.Fatalf("the proposal was not edited in place: %+v", edits)
		}
		next, ok := edits[0].meta.proposalPayload()
		if !ok || next.Rev != 2 || next.Hash != intake.Hash(moved) || next.Author != "U02DEF" {
			t.Fatalf("the edit's payload: %+v (%v)", next, ok)
		}
		if next.Hash == intake.Hash(plan) {
			t.Fatal("the reply did not move the plan")
		}
		// The material set moved, so the section a click would file differently is marked and the
		// header asks for a new confirmation (CD14, CD27).
		if !strings.Contains(string(edits[0].blocks), "*Repository ‹changed›*") {
			t.Fatalf("the repository section is not marked: %s", edits[0].blocks)
		}
		if !strings.Contains(string(edits[0].blocks), "requires a new confirmation") {
			t.Fatalf("the header does not ask for a new confirmation: %s", edits[0].blocks)
		}
		// Handoff continues by id and answer: the App holds no plan and sends no reconstructed history.
		calls := rig.broker.handoffCalls()
		if len(calls) != 1 {
			t.Fatalf("%d handoff calls, want one", len(calls))
		}
		want := HandoffRequest{ID: defaultHandoffID, Answer: "it belongs in owner/docs",
			OnBehalfOf: "slack:T01ABC/U02DEF", CeilingUSD: 2}
		if !reflect.DeepEqual(calls[0], want) {
			t.Fatalf("the handoff request is %+v, want %+v", calls[0], want)
		}
		if n := len(rig.broker.runCreates()); n != 0 {
			t.Fatalf("a re-plan filed %d issues", n)
		}
		if live := liveProposals(t, rig.slack); len(live) != 1 || live[0].TS != ts {
			t.Fatalf("the thread has %d live proposals: %+v", len(live), live)
		}
		if got := rig.metrics.count("events", "updated"); got != 1 {
			t.Fatalf("the re-plan was counted %d times as updated", got)
		}
	})

	t.Run("five replies are still one message", func(t *testing.T) {
		rig, _, ts := proposalAt(t, 1)
		for i := 1; i <= 5; i++ {
			rig.replySaid("U02DEF", fmt.Sprintf("1729.900%d", i), fmt.Sprintf("and the table, note %d", i))
		}
		if n := len(rig.posts()); n != 0 {
			t.Fatalf("%d posts: five replies posted beside the proposal", n)
		}
		edits := rig.slack.proposalEdits()
		if len(edits) != 5 {
			t.Fatalf("%d edits, want one per reply: %+v", len(edits), edits)
		}
		for i, e := range edits {
			p, ok := e.meta.proposalPayload()
			if !ok || p.Rev != i+2 || e.ts != ts {
				t.Fatalf("edit %d is %+v (%v) on %q", i, p, ok, e.ts)
			}
		}
		if live := liveProposals(t, rig.slack); len(live) != 1 || live[0].TS != ts {
			t.Fatalf("the thread has %d live proposals: %+v", len(live), live)
		}
		if n := len(rig.broker.runCreates()); n != 0 {
			t.Fatalf("five revisions filed %d issues", n)
		}
	})

	// Review Focus 1, reached through the reply: an edit Slack refuses is reposted and the message it
	// replaced is made inert, so the thread still holds one live proposal and one set of buttons.
	t.Run("an edit Slack refuses is reposted, and the thread still holds one proposal", func(t *testing.T) {
		rig, plan, ts := proposalAt(t, 1)
		moved := clonePlan(t, plan)
		moved.Repository = "Smana/cloud-native-ref"
		rig.broker.setNextPlan(moved, false)
		rig.slack.failProposalUpdates = 1
		rig.replySaid("U02DEF", "1729.9001", "it belongs in owner/docs")

		posts := rig.posts()
		if len(posts) != 1 {
			t.Fatalf("%d posts, want the repost beside the card it replaced", len(posts))
		}
		if p, ok := posts[0].meta.proposalPayload(); !ok || p.Rev != 2 || p.Hash != intake.Hash(moved) {
			t.Fatalf("the repost's payload: %+v (%v)", p, ok)
		}
		var inert []post
		for _, u := range rig.slack.updatesMade() {
			if u.ts == ts {
				inert = append(inert, u)
			}
		}
		if len(inert) != 1 || inert[0].text != proposalReplacedLine || string(inert[0].blocks) != "[]" {
			t.Fatalf("the replaced card: %+v", inert)
		}
		if live := liveProposals(t, rig.slack); len(live) != 1 || live[0].TS != posts[0].ts {
			t.Fatalf("the thread has %d live proposals: %+v", len(live), live)
		}
		if got := rig.metrics.count("events", "reposted"); got != 1 {
			t.Fatalf("the repost was counted %d times", got)
		}
	})
}

// Review Focus 2: two replies inside one re-plan serialise on the App's edit lock; the thread ends
// with one message carrying the last plan at revision N+2, and neither word is lost from the
// planner's context — the second reply's re-plan reads the thread again under the lock.
func TestReplySerialised(t *testing.T) {
	rig, plan, ts := proposalAt(t, 1)
	seen := 0
	rig.broker.handoffAnswer = func(in HandoffRequest) intake.Plan {
		seen++
		next := plan
		next.Issues = append([]intake.Issue(nil), plan.Issues...)
		next.Issues[0].Title = fmt.Sprintf("docs: planned from %d replies (%s)", seen, in.Answer)
		return next
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	rig.broker.handoffGate = func() { once.Do(func() { close(entered); <-release }) } // the first plan blocks

	const first, second = "it belongs in owner/docs", "and the acceptance check is one click"
	rig.slack.seed(Message{TS: "1729.9001", ThreadTS: proposalThreadTS, User: "U02DEF", Text: first})
	// The waits are bounded so a reply that stopped re-planning fails this test instead of hanging it.
	bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	outcomes := make(chan string, 2)
	go func() { outcomes <- rig.app.reply(bounded, replyEvent("U02DEF", "1729.9001", first)) }()
	select {
	case <-entered: // the first re-plan is inside the planner, holding the lock
	case <-bounded.Done():
		t.Fatal("the first re-plan never reached the planner: a reply no longer re-plans")
	}
	// The second reply lands while the first plan is in flight: its re-plan's read is after the first
	// one's write, so its revision is 3 and the message shows the second plan.
	rig.slack.seed(Message{TS: "1729.9002", ThreadTS: proposalThreadTS, User: "U02DEF", Text: second})
	go func() { outcomes <- rig.app.reply(bounded, replyEvent("U02DEF", "1729.9002", second)) }()
	close(release)
	for range 2 {
		select {
		case code := <-outcomes:
			if code != "updated" {
				t.Fatalf("a re-plan answered %q, want updated", code)
			}
		case <-bounded.Done():
			t.Fatal("a re-plan never finished")
		}
	}

	calls := rig.broker.handoffCalls()
	if len(calls) != 2 {
		t.Fatalf("%d handoff calls, want two: %+v", len(calls), calls)
	}
	if calls[0].ID != defaultHandoffID || calls[0].Answer != first {
		t.Fatalf("first handoff continuation: %+v", calls[0])
	}
	if calls[1].ID != defaultHandoffID || calls[1].Answer != second {
		t.Fatalf("second handoff continuation: %+v", calls[1])
	}

	edits := rig.slack.proposalEdits()
	if len(edits) != 2 || edits[0].ts != ts || edits[1].ts != ts {
		t.Fatalf("the edits: %+v", edits)
	}
	second2, ok := edits[0].meta.proposalPayload()
	if !ok || second2.Rev != 2 {
		t.Fatalf("the first edit's revision: %+v (%v)", second2, ok)
	}
	last, ok := edits[1].meta.proposalPayload()
	if !ok || last.Rev != 3 || !strings.Contains(edits[1].text, "docs: planned from 2 replies ("+second+")") {
		t.Fatalf("the last edit holds %+v (%v), text %q", last, ok, edits[1].text)
	}
	if live := liveProposals(t, rig.slack); len(live) != 1 || live[0].TS != ts {
		t.Fatalf("the thread has %d live proposals: %+v", len(live), live)
	}
}

// CD14's bound, and the rules the draft path already has: over maxProposalRevisions the thread says to
// confirm, only the proposal's author's replies edit it, a closed thread is never edited, and a
// deployment that closed the switch re-plans nothing.
func TestReplyBounds(t *testing.T) {
	t.Run("a thread at the revision cap says to confirm", func(t *testing.T) {
		rig, _, _ := proposalAt(t, maxProposalRevisions)
		rig.replySaid("U02DEF", "1729.9001", "one more change")
		if p := rig.lastPost(); p.text != replyPlannedMost || p.meta != nil {
			t.Fatalf("%q, want %q", p.text, replyPlannedMost)
		}
		if n := len(rig.broker.handoffCalls()); n != 0 {
			t.Fatalf("a capped thread planned %d times", n)
		}
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("a capped thread was edited %d times", n)
		}
	})

	// The planner's text on a re-plan is the thread's first message, so the 16 KiB bound that guards a
	// mention guards it too (CD1): a thread rooted in the mention that planned it, and a thread rooted
	// in someone else's message the mention was typed into, both refuse before the broker is called —
	// a plan drawn from part of a request is a plan nobody read.
	t.Run("an overlong parent mention is refused before the planner", func(t *testing.T) {
		rig, _, _ := proposalAt(t, 1)
		rig.slack.seed(Message{TS: proposalThreadTS, User: "U02DEF",
			Text: "<@U0BOT> " + strings.Repeat("a", maxProposalRequestBytes+1)})
		rig.replySaid("U02DEF", "1729.9001", "it belongs in owner/docs")
		if p := rig.lastPost(); p.text != RequestedTextTooLong.Error() || p.meta != nil {
			t.Fatalf("the thread says %+v, want %q", p, RequestedTextTooLong.Error())
		}
		if n := len(rig.broker.handoffCalls()); n != 0 {
			t.Fatalf("an oversized request planned %d times", n)
		}
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("an oversized request edited the proposal %d times", n)
		}
		if got := rig.metrics.count("events", "too_large"); got != 1 {
			t.Fatalf("the refusal was counted %d times as too_large", got)
		}
	})

	t.Run("an overlong thread root is refused before the planner", func(t *testing.T) {
		rig, _, _ := proposalAt(t, 1)
		// A thread the mention was typed into: its root is the other person's message, and nothing
		// bounded it when they wrote it.
		rig.slack.seed(Message{TS: proposalThreadTS, User: "U07GHI",
			Text: strings.Repeat("a pasted log line\n", maxProposalRequestBytes/len("a pasted log line\n")+1)})
		rig.replySaid("U02DEF", "1729.9001", "it belongs in owner/docs")
		if p := rig.lastPost(); p.text != RequestedTextTooLong.Error() || p.meta != nil {
			t.Fatalf("the thread says %+v, want %q", p, RequestedTextTooLong.Error())
		}
		if n := len(rig.broker.handoffCalls()); n != 0 {
			t.Fatalf("an oversized thread root planned %d times", n)
		}
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("an oversized thread root edited the proposal %d times", n)
		}
		if got := rig.metrics.count("events", "too_large"); got != 1 {
			t.Fatalf("the refusal was counted %d times as too_large", got)
		}
	})

	t.Run("a reply that is not the author's is ignored", func(t *testing.T) {
		rig, _, _ := proposalAt(t, 1)
		rig.replySaid("U07GHI", "1729.9001", "it belongs in owner/docs")
		if n := len(rig.posts()); n != 0 {
			t.Fatalf("someone else's reply was answered: %+v", rig.posts())
		}
		if n := len(rig.broker.handoffCalls()); n != 0 {
			t.Fatalf("someone else's reply planned %d times", n)
		}
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("someone else's reply edited the proposal %d times", n)
		}
		if got := rig.metrics.count("events", "ignored"); got != 1 {
			t.Fatalf("the reply was counted %d times as ignored", got)
		}
	})

	t.Run("a closed thread is never edited", func(t *testing.T) {
		rig, _, _ := proposalAt(t, 1)
		rig.slack.seed(Message{TS: "1729.0003", ThreadTS: proposalThreadTS, User: "U0BOT",
			Text:     "Filed #42 for <@U02DEF>; the factory starts it. Room: https://rooms.example/r/26zfnuxm",
			Metadata: metaOf(t, filedEventType, MarkerPayload{Repo: "Smana/ai-native-ref", Room: "26zfnuxm"})})
		rig.replySaid("U02DEF", "1729.9001", "it belongs in owner/docs")
		if n := len(rig.slack.proposalEdits()); n != 0 {
			t.Fatalf("a filed thread was edited: %+v", rig.slack.proposalEdits())
		}
		// CD25: the filed thread is not edited and not re-planned, and a reply that is no question of
		// the five is answered with where things happen rather than left silent.
		want := "I only understand these here: `status`, `needs me`, `what changed`, `the pull request` " +
			"(and `stop`, which says how to stop). Progress is in this thread; approvals and steering are on " +
			"<https://rooms.example/r/26zfnuxm|the room page>."
		if posts := rig.posts(); len(posts) != 1 || posts[0].text != want || posts[0].meta != nil {
			t.Fatalf("a filed thread answered %+v, want the one pointer line", posts)
		}
		if n := len(rig.broker.handoffCalls()); n != 0 {
			t.Fatalf("a filed thread planned %d times", n)
		}
		if got := rig.metrics.count("events", "answered"); got != 1 {
			t.Fatalf("the answer was counted %d times as answered", got)
		}
	})

}

// The context intake_plan is given is the requester's own words: the parent is the request and not a
// reply, the App's messages are not words at all, a bystander's reply is not the requester's, and the
// 4 KiB bound keeps the most recent replies whole and cuts an oversized one rather than dropping it.
func TestFollowUps(t *testing.T) {
	const bot, author = "U0BOT", "U02DEF"
	msgs := []Message{
		{TS: "1.1", User: author, Text: "<@U0BOT> the install link is dead"},
		{TS: "1.2", ThreadTS: "1.1", User: bot, Text: "Proposal v1 · sha256:…"},
		{TS: "1.3", ThreadTS: "1.1", User: author, Text: "  it belongs in owner/docs  "},
		{TS: "1.4", ThreadTS: "1.1", User: "U07GHI", Text: "and while you are there, ship it"},
		{TS: "1.5", ThreadTS: "1.1", User: author, Text: "the acceptance check is one click"},
	}
	if got, want := followUps(msgs, "1.1", bot, author, maxProposalContextBytes),
		"Reply 1: it belongs in owner/docs\nReply 2: the acceptance check is one click"; got != want {
		t.Fatalf("the context is %q, want %q", got, want)
	}
	// The bound keeps the most recent whole replies: the oldest goes first.
	last := "Reply 1: the acceptance check is one click"
	if got := followUps(msgs, "1.1", bot, author, len(last)); got != last {
		t.Fatalf("the trimmed context is %q, want %q", got, last)
	}
	// One reply larger than the whole bound is cut to it, never dropped: it is the word the re-plan
	// was asked for. Nothing longer than the bound ever leaves here.
	long := []Message{
		{TS: "1.1", User: author, Text: "the link"},
		{TS: "1.2", ThreadTS: "1.1", User: author, Text: strings.Repeat("a", 64)},
	}
	if got := followUps(long, "1.1", bot, author, 32); len(got) != 32 || !strings.HasPrefix(got, "Reply 1: aaa") {
		t.Fatalf("an oversized reply: %q (%d bytes)", got, len(got))
	}
	// No replies at all is no context: the thread's opening message is the request, never a reply.
	if got := followUps(msgs[:1], "1.1", bot, author, maxProposalContextBytes); got != "" {
		t.Fatalf("a thread with no reply has the context %q", got)
	}
}

// liveProposals is every message in the fake a person could still confirm: the App's own, its metadata
// decoding as a proposal with the hash holding, and not the inert line a repost leaves behind in the
// message it replaced.
func liveProposals(t *testing.T, f *fakeSlack) []Message {
	t.Helper()
	var out []Message
	for _, m := range f.thread() {
		if m.Metadata == nil || m.Text == proposalReplacedLine {
			continue
		}
		if _, ok := m.Metadata.proposalPayload(); ok {
			out = append(out, m)
		}
	}
	return out
}

// CD7 and Review Focus 1: a thread holds one live proposal, so a second mention revises the message it
// already has instead of posting a second card whose buttons bind another hash.
func TestSecondMentionUpdatesTheProposal(t *testing.T) {
	// revised changes the plan the planner answers with, so the update has something material to mark.
	revised := func(t *testing.T, b *stubBroker) intake.Plan {
		t.Helper()
		plan := clonePlan(t, b.storedPlan)
		plan.Issues[0].Title = "docs: the install link points at the page it moved to"
		b.setNextPlan(plan, false)
		return plan
	}

	t.Run("a second mention is the same message's next revision", func(t *testing.T) {
		rig, _ := withProposals(t)
		rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)
		first := rig.lastPost()
		plan := revised(t, rig.broker)
		rig.mention("C03", "1729.0001", "<@U0BOT> the install link still points at the moved page")

		if n := len(rig.posts()); n != 1 {
			t.Fatalf("%d posts: a second mention posted a second proposal", n)
		}
		edits := rig.slack.proposalEdits()
		if len(edits) != 1 || edits[0].ts != first.ts || edits[0].channel != "C03" {
			t.Fatalf("the live proposal was not edited in place: %+v", edits)
		}
		prop, ok := edits[0].meta.proposalPayload()
		if !ok || prop.Rev != 2 || prop.Hash != intake.Hash(plan) {
			t.Fatalf("the edit's payload: %+v (%v)", prop, ok)
		}
		if !strings.Contains(edits[0].text, plan.Issues[0].Title) {
			t.Fatalf("the revised message does not show the new plan: %q", edits[0].text)
		}
		if live := liveProposals(t, rig.slack); len(live) != 1 || live[0].TS != first.ts {
			t.Fatalf("the thread has %d live proposals: %+v", len(live), live)
		}
		if n := len(rig.broker.runCreates()); n != 0 {
			t.Fatalf("a revision filed %d issues", n)
		}
		if got := rig.metrics.count("events", "updated"); got != 1 {
			t.Fatalf("the revision went uncounted: %d", got)
		}
	})

	t.Run("an edit Slack refuses is reposted, and the old card is made inert", func(t *testing.T) {
		rig, _ := withProposals(t)
		rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)
		first := rig.lastPost()
		plan := revised(t, rig.broker)
		rig.slack.failProposalUpdates = 1
		rig.mention("C03", "1729.0001", "<@U0BOT> the install link still points at the moved page")

		posts := rig.posts()
		if len(posts) != 2 {
			t.Fatalf("%d posts, want the repost beside the card it replaced", len(posts))
		}
		newest := posts[1]
		if p, ok := newest.meta.proposalPayload(); !ok || p.Rev != 2 || p.Hash != intake.Hash(plan) {
			t.Fatalf("the repost's payload: %+v (%v)", p, ok)
		}
		// The replaced card keeps its place in the thread and loses its buttons, so no click binds a
		// hash its metadata no longer holds.
		var inert []post
		for _, u := range rig.slack.updatesMade() {
			if u.ts == first.ts {
				inert = append(inert, u)
			}
		}
		if len(inert) != 1 || inert[0].text != proposalReplacedLine || string(inert[0].blocks) != "[]" {
			t.Fatalf("the replaced card: %+v", inert)
		}
		if live := liveProposals(t, rig.slack); len(live) != 1 || live[0].TS != newest.ts {
			t.Fatalf("the thread has %d live proposals: %+v", len(live), live)
		}
		if n := len(rig.broker.runCreates()); n != 0 {
			t.Fatalf("a revision filed %d issues", n)
		}
	})

	// The cap is the thread's, not a mention's, so the reply flow's re-plan meets the same line.
	t.Run("a thread at the revision cap refuses another revision", func(t *testing.T) {
		rig, plan := withProposals(t)
		full, err := newProposal(plan, intake.Hash(plan), "U02DEF", maxProposalRevisions)
		if err != nil {
			t.Fatalf("newProposal: %v", err)
		}
		rig.slack.seed(Message{TS: "1729.0002", ThreadTS: "1729.0001", User: "U0BOT", Text: "Proposal v40",
			Metadata: metaOf(t, proposalEventType, full)})
		rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)
		want := "Not proposed: this thread has all the revisions one proposal may have; confirm it, or start a new mention."
		if p := rig.lastPost(); p.text != want || p.meta != nil {
			t.Fatalf("%q, want %q", p.text, want)
		}
		if n := len(rig.broker.handoffCalls()); n != 0 {
			t.Fatalf("a capped thread planned %d times", n)
		}
	})
}

// ---- the proposal counter, one action per series (CD7) ----

// CD7's evidence at the App: maku_slack_proposal_updates_total{six closed actions} says what the App
// did with a proposal, one count per action and none on a path that answered nothing. The counter's
// vocabulary is the six actions; a refusal code stays on maku_slack_app_requests_total{route, outcome},
// where a dashboard can read both without the label values leaking into each other.
func TestProposalActionsCounted(t *testing.T) {
	// moved is the plan a reply or a second mention re-plans to: one the thread's message does not
	// carry, so the update has something material to mark.
	moved := func(t *testing.T, rig *appRig, plan intake.Plan) intake.Plan {
		t.Helper()
		next := clonePlan(t, plan)
		next.Repository = "Smana/cloud-native-ref"
		rig.broker.setNextPlan(next, false)
		return next
	}
	for _, tc := range []struct {
		name string
		run  func(t *testing.T) *appRig
		want map[string]int
	}{
		{
			name: "a mention counts mention",
			run: func(t *testing.T) *appRig {
				rig, _ := withProposals(t)
				rig.mention("C03", proposalThreadTS, "<@U0BOT> "+mentionWords)
				return rig
			},
			want: map[string]int{"mention": 1},
		},
		{
			// A plan the broker refuses is still the one action the mention took: the refusal code is
			// the other counter's business, and no other action happened.
			name: "a refused plan counts nothing but mention",
			run: func(t *testing.T) *appRig {
				rig, _ := withProposals(t)
				rig.broker.handoffErr = Refused("planner_unavailable: the plan service is unavailable; try again later")
				rig.mention("C03", proposalThreadTS, "<@U0BOT> "+mentionWords)
				return rig
			},
			want: map[string]int{"mention": 1},
		},
		{
			name: "a reply counts reply",
			run: func(t *testing.T) *appRig {
				rig, plan, _ := proposalAt(t, 1)
				moved(t, rig, plan)
				rig.replySaid(proposalAuthor, "1729.9001", "it belongs in owner/docs")
				return rig
			},
			want: map[string]int{"reply": 1},
		},
		{
			// Someone else's reply answered nothing: no line, no plan, and so no action to count.
			name: "a reply that is not the author's counts nothing",
			run: func(t *testing.T) *appRig {
				rig, plan, _ := proposalAt(t, 1)
				moved(t, rig, plan)
				rig.replySaid(proposalPresser, "1729.9001", "it belongs in owner/docs")
				return rig
			},
			want: map[string]int{},
		},
		{
			// Review Focus 1: Slack refuses the edit and the App reposts, which is two actions — the
			// re-plan the person asked for and the write the refusal forced.
			name: "an edit Slack refuses counts repost as well as reply",
			run: func(t *testing.T) *appRig {
				rig, plan, _ := proposalAt(t, 1)
				moved(t, rig, plan)
				rig.slack.failProposalUpdates = 1
				rig.replySaid(proposalAuthor, "1729.9001", "it belongs in owner/docs")
				return rig
			},
			want: map[string]int{"reply": 1, "repost": 1},
		},
		{
			name: "a Confirm counts confirm",
			run: func(t *testing.T) *appRig {
				rig, plan, ts := proposalAt(t, 1)
				rig.broker.room = "26zfnuxm"
				rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
					proposalValue(intake.Hash(plan), 1)))
				return rig
			},
			want: map[string]int{"confirm": 1},
		},
		{
			// The Filed line could not be written in place, so it went to the thread: the App did both
			// the action the press asked for and the write Slack refused.
			name: "a Confirm whose Filed line Slack refuses counts unrewritten as well as confirm",
			run: func(t *testing.T) *appRig {
				rig, plan, ts := proposalAt(t, 1)
				rig.broker.room = "26zfnuxm"
				rig.slack.failProposalUpdates = 1
				rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
					proposalValue(intake.Hash(plan), 1)))
				return rig
			},
			want: map[string]int{"confirm": 1, "unrewritten": 1},
		},
		{
			name: "a Cancel counts cancel",
			run: func(t *testing.T) *appRig {
				rig, plan, ts := proposalAt(t, 1)
				rig.interact(proposalPressOf(t, ts, proposalAuthor, actionProposalCancel,
					proposalValue(intake.Hash(plan), 1)))
				return rig
			},
			want: map[string]int{"cancel": 1},
		},
		{
			// Slack retries a press it did not see answered: the dedup set answers the second one, and
			// one press is one action whatever Slack sends.
			name: "a second press of the same button counts nothing",
			run: func(t *testing.T) *appRig {
				rig, plan, ts := proposalAt(t, 1)
				rig.broker.room = "26zfnuxm"
				press := proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
					proposalValue(intake.Hash(plan), 1))
				rig.interact(press)
				rig.interact(press)
				return rig
			},
			want: map[string]int{"confirm": 1},
		},
		{
			// A value this family cannot read is refused before any control is taken up: the person is
			// answered, and no action of the six is what happened.
			name: "a press whose value is another family's counts nothing",
			run: func(t *testing.T) *appRig {
				rig, _, ts := proposalAt(t, 1)
				rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm, "v1|forge.push|99|main"))
				return rig
			},
			want: map[string]int{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := tc.run(t)
			if got := rig.metrics.counted("proposal"); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("the proposal counter holds %v, want %v", got, tc.want)
			}
		})
	}
}

// CD11: a run starting is not news. A default channel posts nothing for room.run_started and does
// post for room.waiting_for_human; a channel that lists run_started posts "A run started."; a
// transition no channel lists is dropped, and the rendering table is untouched for a subscriber
// that asks for it. A card is the approvals switch's, not a line: a notify list that leaves the wait
// out still draws it.
func TestNotifyProjection(t *testing.T) {
	base := Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2}
	listed := base
	listed.Notify = []string{"room.run_started"}
	for _, tc := range []struct {
		name    string
		channel Channel
		body    string
		want    string // "" is no post
		outcome string
	}{
		{"a default channel hears no run start", base, `{"type":"room.run_started","room":"aaaaaaaa"}`, "", "ignored"},
		{"a default channel hears a wait", base, `{"type":"room.waiting_for_human","room":"aaaaaaaa","reason":"approval"}`,
			waitLine, "posted"},
		{"a channel that lists run_started hears it", listed, `{"type":"room.run_started","room":"aaaaaaaa"}`,
			"A run started.", "posted"},
		{"a channel hears nothing it does not list", listed, `{"type":"room.merged","room":"aaaaaaaa","pr":7}`, "", "ignored"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newAppRig(t, tc.channel)
			rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
			rig.webhook(tc.body)
			posts := rig.posts()
			if tc.want == "" && len(posts) != 0 {
				t.Fatalf("posted %+v, want nothing", posts)
			}
			if tc.want != "" && (len(posts) != 1 || posts[0].text != tc.want) {
				t.Fatalf("posted %+v, want %q", posts, tc.want)
			}
			if n := rig.metrics.count("webhooks", tc.outcome); n != 1 {
				t.Fatalf("webhooks/%s counted %d times, want 1", tc.outcome, n)
			}
		})
	}

	t.Run("a channel the config dropped posts nothing", func(t *testing.T) {
		rig := newAppRig(t, base)
		rig.threads.put("aaaaaaaa", Thread{Channel: "C99", TS: "1729.0001"})
		rig.webhook(`{"type":"room.waiting_for_human","room":"aaaaaaaa","reason":"approval"}`)
		if n := len(rig.posts()); n != 0 {
			t.Fatalf("%d posts in a channel no longer served", n)
		}
		if n := rig.metrics.count("webhooks", "ignored"); n != 1 {
			t.Fatalf("webhooks/ignored counted %d times, want 1: a dropped channel is not an error the broker retries", n)
		}
	})

	t.Run("a card closes whatever the notify list says", func(t *testing.T) {
		ch := liveCardChannel()
		ch.Notify = []string{"room.merged"}
		rig := newAppRig(t, ch)
		rig.threads.put(cardRoom, Thread{Channel: "C03", TS: pressThread})
		seedCard(t, rig, cardTS, cardApprovalID)
		rig.webhook(closeBody("approved"))
		if u := rig.slack.updatesMade(); len(u) != 1 || u[0].ts != cardTS {
			t.Fatalf("the close updated %+v, want the card made inert: a live card past its decision", u)
		}
	})

	t.Run("a card ignores the notify list", func(t *testing.T) {
		ch := liveCardChannel()
		ch.Notify = []string{"room.merged"}
		rig := newAppRig(t, ch)
		rig.threads.put(cardRoom, Thread{Channel: "C03", TS: pressThread})
		rig.webhook(waitBody(nil))
		if p := rig.posts(); len(p) != 1 || p[0].blocks == nil {
			t.Fatalf("posted %+v, want the forge.push card", p)
		}
	})

	t.Run("the table keeps run_started for a subscriber that asks", func(t *testing.T) {
		rig := newAppRig(t, base)
		if line, ok := rig.app.line(webhook.Event{Type: "room.run_started", Room: "aaaaaaaa"}); !ok || line != "A run started." {
			t.Fatalf("line(run_started) = %q, %v", line, ok)
		}
	})
}

// §5: a card's metadata decodes as an approval payload; an unknown event type or an unreadable
// payload is not one — no payload, no panic.
func TestMetadataShapes(t *testing.T) {
	card := metaOf(t, approvalEventType, ApprovalPayload{ApprovalID: "ap1", Class: "forge.push", Branch: "agent/x"})
	if p, ok := card.approvalPayload(); !ok || p.ApprovalID != "ap1" || p.Class != "forge.push" || p.Branch != "agent/x" {
		t.Fatalf("a card's payload: %+v %v", p, ok)
	}
	for name, meta := range map[string]*Metadata{
		"nil":                   nil,
		"another event type":    metaOf(t, filedEventType, ApprovalPayload{ApprovalID: "ap1"}),
		"an unreadable payload": {EventType: approvalEventType, EventPayload: json.RawMessage(`"nope"`)},
	} {
		if _, ok := meta.approvalPayload(); ok {
			t.Fatalf("%s decoded as a card", name)
		}
	}
}

func TestTheThreadSaysAStepUpOnce(t *testing.T) {
	ch := Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 5}
	rig := newAppRig(t, ch)
	rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1700.1"})
	body := `{"type":"room.stepped_up","room":"aaaaaaaa","from":"standard","to":"careful","reason":"stalled","ceilingMicroUSD":13600000}`
	rig.webhook(body)
	rig.webhook(body) // second delivery (different webhook-id) posts nothing
	posts := rig.posts()
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1: %+v", len(posts), posts)
	}
	if strings.Contains(posts[0].text, "<@") || !strings.Contains(posts[0].text, "from standard to careful") || !strings.Contains(posts[0].text, "$13.60") {
		t.Fatalf("post = %q", posts[0].text)
	}
}

// Task G6: every needs_you message mentions the one person who can act, states the recommended
// action (button, reply, command, or room link), and names what happens if nothing is done.
func TestANeedsYouMessageNamesTheDefault(t *testing.T) {
	cases := []struct {
		name        string
		ch          Channel
		sum         *summary.Summary
		body        string
		wantMention string
		notMention  string
		wantAction  string
		wantDefault string
	}{
		{
			name: "scoper question mentions author and names 24h stop",
			ch:   Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2},
			sum: func() *summary.Summary {
				var s summary.Summary
				s.Room = "aaaaaaaa"
				s.Status.Phase = "Escalated"
				s.Status.ReasonCode = "needs_author"
				s.Status.Question = "Which check proves it?"
				return &s
			}(),
			body:        `{"type":"room.waiting_for_human","room":"aaaaaaaa","reason":"needs_author"}`,
			wantMention: "<@U01REQ>",
			notMention:  "<@U02DEF>",
			wantAction:  "Reply to answer.",
			wantDefault: "If nothing is done, it stops in 24 hours.",
		},
		{
			name: "merge hold mentions filer and says it stays parked",
			ch:   Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2},
			sum: func() *summary.Summary {
				var s summary.Summary
				s.Room = "aaaaaaaa"
				s.Status.Phase = "AwaitingHuman"
				s.Status.ReasonCode = "shadow_would_arm"
				s.Status.PR = &envelope.PRFact{Number: 418}
				s.Status.Policy = &summary.PolicyView{PolicyFact: envelope.PolicyFact{Name: "docs", Mode: envelope.PolicyShadow}}
				return &s
			}(),
			body:        `{"type":"room.waiting_for_human","room":"aaaaaaaa","reason":"shadow_would_arm"}`,
			wantMention: "<@U02DEF>",
			notMention:  "<@U01REQ>",
			wantAction:  "answer it on the room page",
			wantDefault: "If nothing is done, it stays parked.",
		},
		{
			name:        "non-card approval mentions filer and names deadline default",
			ch:          Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2},
			body:        `{"type":"room.waiting_for_human","room":"aaaaaaaa","reason":"approval"}`,
			wantMention: "<@U02DEF>",
			notMention:  "<@U01REQ>",
			wantAction:  "answer it on the room page",
			wantDefault: "If nothing is done, it stays parked until the deadline.",
		},
		{
			name:        "escalated end without retry button names command and 24h stop",
			ch:          Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2},
			body:        `{"type":"room.ended","room":"aaaaaaaa","state":"failed","reason":"budget-task"}`,
			wantMention: "<@U02DEF>",
			notMention:  "<@U01REQ>",
			wantAction:  "/factory retry",
			wantDefault: "If nothing is done, it stops in 24 hours.",
		},
		{
			name:        "escalated end with retry button names 24h stop",
			ch:          Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2, IntentActions: []string{"retry"}},
			body:        `{"type":"room.ended","room":"aaaaaaaa","state":"failed","reason":"stalled"}`,
			wantMention: "<@U02DEF>",
			notMention:  "<@U01REQ>",
			wantAction:  "Ended: escalated",
			wantDefault: "If nothing is done, it stops in 24 hours.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newAppRig(t, tc.ch)
			rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001", Author: "U01REQ", Filer: "U02DEF"})
			if tc.sum != nil {
				rig.broker.summary = tc.sum
			}
			rig.webhook(tc.body)
			posts := rig.posts()
			if len(posts) != 1 {
				t.Fatalf("posts = %+v, want 1", posts)
			}
			got := posts[0].text
			if !strings.HasPrefix(got, tc.wantMention+" ") {
				t.Errorf("text = %q, want prefix %q", got, tc.wantMention+" ")
			}
			if strings.Contains(got, tc.notMention) {
				t.Errorf("text mentioned wrong actor %q: %q", tc.notMention, got)
			}
			if !strings.Contains(got, tc.wantAction) {
				t.Errorf("text lacks action %q: %q", tc.wantAction, got)
			}
			if !strings.Contains(got, tc.wantDefault) {
				t.Errorf("text lacks default %q: %q", tc.wantDefault, got)
			}
		})
	}
}

// Task G6: every changed notification is one line in the thread and mentions nobody.
func TestAChangedLineMentionsNobody(t *testing.T) {
	ch := Channel{
		Repositories: []string{"Smana/ai-native-ref"},
		MaxCostUSD:   2,
		Notify: []string{
			"room.task_triaged",
			"room.run_started",
			"room.pr_opened",
			"room.stepped_up",
			"room.verdict_posted",
			"room.budget_reached",
			"room.approval_closed",
		},
	}
	rig := newAppRig(t, ch)
	rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001", Author: "U01REQ", Filer: "U02DEF"})

	events := []struct {
		typ  string
		body string
	}{
		{"room.task_triaged", `{"type":"room.task_triaged","room":"aaaaaaaa"}`},
		{"room.run_started", `{"type":"room.run_started","room":"aaaaaaaa"}`},
		{"room.pr_opened", `{"type":"room.pr_opened","room":"aaaaaaaa","pr":12}`},
		{"room.stepped_up", `{"type":"room.stepped_up","room":"aaaaaaaa","from":"standard","to":"careful","reason":"stalled","ceilingMicroUSD":13600000}`},
		{"room.verdict_posted", `{"type":"room.verdict_posted","room":"aaaaaaaa","pr":12}`},
		{"room.budget_reached", `{"type":"room.budget_reached","room":"aaaaaaaa","limit":"task"}`},
		{"room.approval_closed", `{"type":"room.approval_closed","room":"aaaaaaaa","decision":"approved"}`},
	}

	for _, ev := range events {
		if got := notificationClass(webhook.Event{Type: ev.typ}); got != narrate.ClassChanged {
			t.Errorf("notificationClass(%s) = %q, want %q", ev.typ, got, narrate.ClassChanged)
		}
		rig.webhook(ev.body)
	}

	posts := rig.posts()
	if len(posts) != len(events) {
		t.Fatalf("got %d posts, want %d: %+v", len(posts), len(events), posts)
	}
	for i, p := range posts {
		for _, forbidden := range []string{"<@", "@", "U01REQ", "U02DEF", "\n"} {
			if strings.Contains(p.text, forbidden) {
				t.Errorf("%s line contained %q: %q", events[i].typ, forbidden, p.text)
			}
		}
	}
}

// Task G6: a done notification mentions the filer once across room.ended, room.merged, and
// room.reverted updates, whichever arrives first.
func TestDoneMentionsOnce(t *testing.T) {
	countMentions := func(rig *appRig) int {
		n := 0
		for _, p := range rig.posts() {
			n += strings.Count(p.text, "<@")
		}
		for _, u := range rig.slack.updatesMade() {
			n += strings.Count(u.text, "<@")
		}
		return n
	}

	t.Run("ended then merged then reverted mentions once", func(t *testing.T) {
		ch := Channel{
			Repositories: []string{"Smana/ai-native-ref"},
			MaxCostUSD:   2,
			Notify:       []string{"room.ended", "room.reverted"},
		}
		rig := newAppRig(t, ch)
		rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001", Author: "U01REQ", Filer: "U02DEF"})

		rig.webhook(`{"type":"room.ended","room":"aaaaaaaa","state":"done"}`)
		rig.webhook(`{"type":"room.merged","room":"aaaaaaaa","pr":418}`)
		rig.webhook(`{"type":"room.merged","room":"aaaaaaaa","pr":418}`)
		rig.webhook(`{"type":"room.reverted","room":"aaaaaaaa","pr":418}`)

		if got := countMentions(rig); got != 1 {
			t.Fatalf("total mentions = %d, want 1; posts=%+v updates=%+v", got, rig.posts(), rig.slack.updatesMade())
		}
		if !strings.HasPrefix(rig.posts()[0].text, "<@U02DEF> ") {
			t.Fatalf("first done post = %q, want <@U02DEF> prefix", rig.posts()[0].text)
		}
		th, ok := rig.threads.get("aaaaaaaa")
		if !ok || !th.DoneMentioned {
			t.Fatalf("thread = %+v (ok=%v), want DoneMentioned=true", th, ok)
		}
	})

	t.Run("merged arriving before ended mentions once", func(t *testing.T) {
		rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
		rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001", Author: "U01REQ", Filer: "U02DEF"})

		rig.webhook(`{"type":"room.merged","room":"aaaaaaaa","pr":418}`)
		rig.webhook(`{"type":"room.ended","room":"aaaaaaaa","state":"done"}`)

		if got := countMentions(rig); got != 1 {
			t.Fatalf("total mentions = %d, want 1; posts=%+v updates=%+v", got, rig.posts(), rig.slack.updatesMade())
		}
		if !strings.HasPrefix(rig.posts()[0].text, "<@U02DEF> Merged #418") {
			t.Fatalf("first merged post = %q, want <@U02DEF> Merged #418", rig.posts()[0].text)
		}
	})

	t.Run("escalated needs_you does not consume the done mention", func(t *testing.T) {
		rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
		rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001", Author: "U01REQ", Filer: "U02DEF"})

		rig.webhook(`{"type":"room.ended","room":"aaaaaaaa","state":"failed","reason":"stalled"}`)
		th, _ := rig.threads.get("aaaaaaaa")
		if th.DoneMentioned {
			t.Fatalf("escalated room.ended set DoneMentioned=true")
		}

		rig.webhook(`{"type":"room.merged","room":"aaaaaaaa","pr":418}`)
		rig.webhook(`{"type":"room.ended","room":"aaaaaaaa","state":"done"}`)

		th, _ = rig.threads.get("aaaaaaaa")
		if !th.DoneMentioned {
			t.Fatalf("done sequence did not set DoneMentioned=true")
		}
		// 1 mention for escalated (needs_you) + 1 mention for merged (done), 0 for trailing ended.
		if got := countMentions(rig); got != 2 {
			t.Fatalf("total mentions = %d, want 2 (1 needs_you + 1 done); posts=%+v updates=%+v", got, rig.posts(), rig.slack.updatesMade())
		}
	})
}
