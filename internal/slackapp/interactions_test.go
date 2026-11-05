// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The card's own values, as §5 writes them into the button's value and the message's timestamp.
const (
	cardApprovalID = "01M4A1B2C3D4E5F6G7H8J9K0MN"
	cardBranch     = "agent/26zfnuxm"
	cardTS         = "1729.0500"
)

// pressPayload is a block_actions payload with every shape §6 accepts; mutate changes one thing, so
// a case tests exactly that and nothing else.
func pressPayload(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	m := map[string]any{
		"type":                  "block_actions",
		"is_enterprise_install": false,
		"team":                  map[string]any{"id": "T01ABC"},
		"user":                  map[string]any{"id": "U02DEF"},
		"channel":               map[string]any{"id": "C03"},
		"actions": []any{map[string]any{
			"action_id": actionApprove,
			"value":     "v1|forge.push|" + cardApprovalID + "|" + cardBranch,
			"action_ts": "1729.0600",
		}},
		"container": map[string]any{"type": "message", "message_ts": cardTS, "channel_id": "C03"},
		"message":   map[string]any{"ts": cardTS},
	}
	if mutate != nil {
		mutate(m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal the press: %v", err)
	}
	return string(raw)
}

// action is a case's rewrite of the payload's one action.
func action(actionID, value string) []any {
	return []any{map[string]any{"action_id": actionID, "value": value, "action_ts": "1729.0600"}}
}

func approvalsRig(t *testing.T) *appRig {
	t.Helper()
	return newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2, Approvals: true})
}

// Invariant 5 over the second body shape: unsigned, stale and oversized interactivity posts are 401
// bad_signature, counted, and reach no broker call; the JSON events path still works from the same
// handler.
func TestSlackInteractivitySignature(t *testing.T) {
	rig := approvalsRig(t)
	press := formBody(pressPayload(t, nil))

	// Unsigned: the signature is the first thing, and nothing of the body is read before it.
	if st := rig.postInteractionAs(press, formCT, rig.now.Unix(), false); st != http.StatusUnauthorized {
		t.Fatalf("an unsigned press: %d, want 401", st)
	}
	if n := rig.metrics.count("interactions", "bad_signature"); n != 1 {
		t.Fatalf("interactions/bad_signature counted %d times, want 1", n)
	}
	if got := rig.broker.decided(); len(got) != 0 {
		t.Fatalf("an unsigned press reached the broker: %+v", got)
	}

	// Stale: six minutes old, correctly signed for its own timestamp.
	if st := rig.postInteractionAs(press, formCT, rig.now.Add(-6*time.Minute).Unix(), true); st != http.StatusUnauthorized {
		t.Fatalf("a stale press: %d, want 401", st)
	}
	if n := rig.metrics.count("interactions", "bad_signature"); n != 2 {
		t.Fatalf("interactions/bad_signature counted %d times, want 2", n)
	}

	// Over Slack's 1 MiB body cap.
	if st := rig.postInteraction("payload=" + strings.Repeat("a", maxSlackBody)); st != http.StatusUnauthorized {
		t.Fatalf("an oversized press: %d, want 401", st)
	}
	if n := rig.metrics.count("interactions", "bad_signature"); n != 3 {
		t.Fatalf("interactions/bad_signature counted %d times, want 3", n)
	}

	// Signed and shaped: the press is handled, once.
	if st := rig.postInteraction(press); st != http.StatusOK {
		t.Fatalf("a signed press: %d, want 200", st)
	}
	if n := rig.metrics.count("interactions", "decided"); n != 1 {
		t.Fatalf("interactions/decided counted %d times, want 1", n)
	}

	// The JSON body shape still arrives at today's handling: a mention drafts from the same handler.
	rig.mention("C03", "1729.0001", draftText)
	if p := rig.lastPost(); !strings.HasPrefix(p.text, "Draft for ") {
		t.Fatalf("the JSON path no longer drafts: %q", p.text)
	}
	if n := rig.metrics.count("events", "drafted"); n != 1 {
		t.Fatalf("events/drafted counted %d times, want 1", n)
	}
	if n := rig.metrics.count("events", "bad_signature"); n != 0 {
		t.Fatalf("a press was counted on the events route %d times", n)
	}
}

// §6 step 3: a shape the App does not recognise answers 200 ignored, with no broker call and no
// message.
func TestInteractivityShape(t *testing.T) {
	press := func(t *testing.T, mutate func(map[string]any)) string {
		t.Helper()
		return formBody(pressPayload(t, mutate))
	}
	for _, tc := range []struct {
		name string
		// body is the form post; contentType overrides Slack's own; want names where the outcome is
		// counted: the press route, or the JSON route a body of this shape is not one of.
		body        func(t *testing.T) string
		contentType string
		want        int
		wantRoute   string
		wantOutcome string
	}{
		{
			// §6 step 2: the content type decides, so a form body labelled JSON is not a press and
			// today's JSON path answers it — the one case here that is not 200 ignored.
			name:        "a form body under Slack's JSON content type",
			body:        func(t *testing.T) string { return press(t, nil) },
			contentType: "application/json",
			want:        http.StatusBadRequest, wantRoute: "events", wantOutcome: "bad_request",
		},
		{
			name: "two payload fields",
			body: func(t *testing.T) string {
				return url.Values{"payload": {pressPayload(t, nil), pressPayload(t, nil)}}.Encode()
			},
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
		{
			name: "a form with no payload",
			body: func(*testing.T) string { return "other=1" },
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
		{
			name: "a view_submission",
			body: func(t *testing.T) string {
				return press(t, func(m map[string]any) { m["type"] = "view_submission" })
			},
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
		{
			name: "two actions",
			body: func(t *testing.T) string {
				return press(t, func(m map[string]any) {
					m["actions"] = append(action(actionApprove, "v1|forge.push|"+cardApprovalID+"|"+cardBranch),
						action(actionDeny, "v1|forge.push|"+cardApprovalID+"|"+cardBranch)...)
				})
			},
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
		{
			name: "an unknown action id",
			body: func(t *testing.T) string {
				return press(t, func(m map[string]any) {
					m["actions"] = action("factory_approval_maybe", "v1|forge.push|"+cardApprovalID+"|"+cardBranch)
				})
			},
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
		{
			name: "a value without the version prefix",
			body: func(t *testing.T) string {
				return press(t, func(m map[string]any) {
					m["actions"] = action(actionApprove, "forge.push|"+cardApprovalID+"|"+cardBranch)
				})
			},
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
		{
			name: "a value whose class is not forge.push",
			body: func(t *testing.T) string {
				return press(t, func(m map[string]any) {
					m["actions"] = action(actionApprove, "v1|forge.pr|"+cardApprovalID+"|"+cardBranch)
				})
			},
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
		{
			name: "a value whose branch is not an agent branch",
			body: func(t *testing.T) string {
				return press(t, func(m map[string]any) {
					m["actions"] = action(actionApprove, "v1|forge.push|"+cardApprovalID+"|main")
				})
			},
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
		{
			name: "another workspace",
			body: func(t *testing.T) string {
				return press(t, func(m map[string]any) { m["team"] = map[string]any{"id": "T09XYZ"} })
			},
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
		{
			name: "an enterprise install",
			body: func(t *testing.T) string {
				return press(t, func(m map[string]any) { m["is_enterprise_install"] = true })
			},
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
		{
			name: "a user id that is not one",
			body: func(t *testing.T) string {
				return press(t, func(m map[string]any) { m["user"] = map[string]any{"id": "bob"} })
			},
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
		{
			name: "a channel id that is not one",
			body: func(t *testing.T) string {
				return press(t, func(m map[string]any) { m["channel"] = map[string]any{"id": "general"} })
			},
			want: http.StatusOK, wantRoute: "interactions", wantOutcome: "ignored",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := approvalsRig(t)
			body := tc.body(t)
			var st int
			if tc.contentType != "" {
				st = rig.postInteractionAs(body, tc.contentType, rig.now.Unix(), true)
			} else {
				st = rig.postInteraction(body)
			}
			if st != tc.want {
				t.Fatalf("%d, want %d", st, tc.want)
			}
			if n := rig.metrics.count(tc.wantRoute, tc.wantOutcome); n != 1 {
				t.Fatalf("%s/%s counted %d times, want 1", tc.wantRoute, tc.wantOutcome, n)
			}
			if got := rig.broker.decided(); len(got) != 0 {
				t.Fatalf("the broker was called: %+v", got)
			}
			if calls := rig.broker.all(); len(calls) != 0 {
				t.Fatalf("the broker was called: %+v", calls)
			}
			if e := rig.slack.whispers(); len(e) != 0 {
				t.Fatalf("an ignored press said something: %+v", e)
			}
		})
	}

	// Review focus 3: container.message_ts names the message Slack rendered the press from, so it
	// names the card when both are present, whether or not message.ts agrees; an absent container
	// falls back to message.ts rather than refusing the click.
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		want   string // the message the press names, "" for a press the App ignores
	}{
		{"both present and equal", nil, cardTS},
		{
			name: "no container, message.ts set",
			mutate: func(m map[string]any) {
				m["container"] = map[string]any{"type": "message", "channel_id": "C03"}
				m["message"] = map[string]any{"ts": "1729.0700"}
			},
			want: "1729.0700",
		},
		{
			name: "both present and disagreeing",
			mutate: func(m map[string]any) {
				m["message"] = map[string]any{"ts": "1729.0700"}
			},
			want: cardTS,
		},
		{
			name:   "neither present",
			mutate: func(m map[string]any) { m["container"], m["message"] = map[string]any{}, map[string]any{} },
			want:   "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := approvalsRig(t)
			if st := rig.interact(pressPayload(t, tc.mutate)); st != http.StatusOK {
				t.Fatalf("%d, want 200", st)
			}
			got := rig.broker.decided()
			if tc.want == "" {
				if len(got) != 0 || rig.metrics.count("interactions", "ignored") != 1 {
					t.Fatalf("a press naming no message was handled: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].MessageTS != tc.want {
				t.Fatalf("the press named %+v, want the message %s", got, tc.want)
			}
		})
	}

	// §6 step 3: a click in a channel whose approvals flag is off is answered locally, with no
	// broker call, and the clicker alone is told where the decision lives.
	t.Run("a channel whose approvals are off", func(t *testing.T) {
		rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
		if st := rig.interact(pressPayload(t, nil)); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		if n := rig.metrics.count("interactions", "not_enabled"); n != 1 {
			t.Fatalf("interactions/not_enabled counted %d times, want 1", n)
		}
		if got := rig.broker.decided(); len(got) != 0 {
			t.Fatalf("a click in a channel with the flag off reached the broker: %+v", got)
		}
		e := rig.slack.whispers()
		if len(e) != 1 || e[0].user != "U02DEF" || e[0].channel != "C03" ||
			!strings.Contains(e[0].text, "https://rooms.example") {
			t.Fatalf("the clicker was not told where the decision lives: %+v", e)
		}
	})
}

// slowSlack holds chat.postEphemeral until its context ends, as a Slack that will not answer in time
// does: the call is recorded first, so a test can see the App did try to tell the clicker.
type slowSlack struct {
	*fakeSlack
	entered chan struct{}
}

func (s *slowSlack) PostEphemeral(ctx context.Context, _, _, _, _ string) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second): // a fast failure for a door with no bound, never a hung test
		return errors.New("slack did not answer")
	}
}

// §6 step 5 and 6: the door's answer is Slack's three seconds, so the one Slack call it makes on the
// not_enabled path is bounded by the App, not by the client's ten-second timeout. A slow Slack leaves
// the press answered 200 inside the window, answered not_enabled once, and never turned into a
// decision — the failed ephemeral is logged and counted, never retried and never enqueued.
func TestNotEnabledAnswersWithinSlacksWindow(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	slow := &slowSlack{fakeSlack: rig.slack, entered: make(chan struct{}, 1)}
	rig.app.Slack = slow

	start := time.Now()
	if st := rig.interact(pressPayload(t, nil)); st != http.StatusOK {
		t.Fatalf("%d, want 200", st)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the door held the press for %s, past Slack's three-second window", d)
	}
	select {
	case <-slow.entered:
	default:
		t.Fatal("the clicker was never told where the decision lives")
	}
	if n := rig.metrics.count("interactions", "not_enabled"); n != 1 {
		t.Fatalf("interactions/not_enabled counted %d times, want 1", n)
	}
	// The slow answer is never a decision, and it never becomes one later either.
	if got := rig.broker.decided(); len(got) != 0 {
		t.Fatalf("a channel with the flag off reached the broker: %+v", got)
	}
	for _, outcome := range []string{"decided", "duplicate", "refused", "error", "queued"} {
		if n := rig.metrics.count("interactions", outcome); n != 0 {
			t.Fatalf("interactions/%s counted %d times, want 0", outcome, n)
		}
	}
}

// A press answered 503 busy must be retried by Slack and handled then, never read as a duplicate and
// dropped: the dedup id is forgotten on the busy path, as the events path already does (F-10).
func TestInteractivityBusyRetries(t *testing.T) {
	rig := approvalsRig(t)
	press := formBody(pressPayload(t, nil))

	// The App set up as full: one queue slot, already taken, and no Async hook to run the work
	// instead, so enqueue reports false on the path a real backlog takes.
	rig.app.Async = nil
	rig.app.poolOnce.Do(func() {})
	rig.app.poolMu.Lock()
	rig.app.queue = make(chan func(), 1)
	rig.app.queue <- func() {}
	rig.app.draining = false
	rig.app.poolMu.Unlock()

	for i := range 2 {
		if st := rig.postInteraction(press); st != http.StatusServiceUnavailable {
			t.Fatalf("press %d while every slot is taken: %d, want 503", i, st)
		}
	}
	if n := rig.metrics.count("interactions", "busy"); n != 2 {
		t.Fatalf("interactions/busy counted %d times, want 2", n)
	}
	if got := rig.broker.decided(); len(got) != 0 {
		t.Fatalf("a press answered busy reached the broker: %+v", got)
	}

	// Slack's retry, once a slot is free: handled once, because the 503 forgot the dedup key.
	rig.app.Async = func(f func()) { f() }
	if st := rig.postInteraction(press); st != http.StatusOK {
		t.Fatalf("the retried press: %d, want 200", st)
	}
	if n := rig.metrics.count("interactions", "decided"); n != 1 {
		t.Fatalf("interactions/decided counted %d times, want 1", n)
	}
	if got := rig.broker.decided(); len(got) != 1 {
		t.Fatalf("the broker saw %d presses, want 1: %+v", len(got), got)
	}
}

// pressThread is the thread the seeded card is a reply in: a press names its channel and its message
// ts and nothing that names the thread, so the worker reads the pressed message to find it.
const pressThread = "1729.0001"

// seedCard puts the card the App posted into the rig's Slack, at ts, as a reply in pressThread.
func seedCard(t *testing.T, r *appRig, ts, approvalID string) {
	t.Helper()
	r.slack.seed(Message{TS: ts, ThreadTS: pressThread, User: "U0BOT", Text: "Approval needed: push to the forge",
		Metadata: metaOf(t, approvalEventType, ApprovalPayload{ApprovalID: approvalID, Class: pressClass, Branch: cardBranch})})
}

// pressOf is the payload of a press on the card at ts whose button carries approvalID.
func pressOf(t *testing.T, ts, approvalID, actionID string) string {
	t.Helper()
	return pressPayload(t, func(m map[string]any) {
		m["container"] = map[string]any{"type": "message", "message_ts": ts, "channel_id": "C03"}
		m["message"] = map[string]any{"ts": ts}
		m["actions"] = action(actionID, pressVersion+"|"+pressClass+"|"+approvalID+"|"+cardBranch)
	})
}

// §1 and §6: an Approve reaches the broker exactly once with the body of §1 and the clicker sees one
// answer; a Deny likewise. The card is the only surface: a thread reply saying "approve" reaches
// nothing (TestSlackNeverDecides' half, amended in Task 17).
func TestSlackDecidesForgePushOnly(t *testing.T) {
	t.Run("an approve press", func(t *testing.T) {
		rig := approvalsRig(t)
		seedCard(t, rig, cardTS, cardApprovalID)
		if st := rig.interact(pressPayload(t, nil)); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		got := rig.broker.decided()
		want := Decision{Team: "T01ABC", User: "U02DEF", Channel: "C03", MessageTS: cardTS,
			ApprovalID: cardApprovalID, Branch: cardBranch, Decision: "approved"}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("the broker was asked to record %+v, want one %+v", got, want)
		}
		e := rig.slack.whispers()
		if len(e) != 1 || e[0].user != "U02DEF" || e[0].channel != "C03" || e[0].thread != pressThread || e[0].text != lineApproved {
			t.Fatalf("the clicker saw %+v, want one %q in the card's thread", e, lineApproved)
		}
		if n := rig.metrics.count("interactions", "decided"); n != 1 {
			t.Fatalf("interactions/decided counted %d times, want 1", n)
		}
		// The card is inert afterwards: one update of the pressed message, the outcome as its text,
		// and no button left in its blocks.
		u := rig.slack.updatesMade()
		if len(u) != 1 || u[0].channel != "C03" || u[0].ts != cardTS || u[0].text != lineApproved ||
			strings.Contains(string(u[0].blocks), "factory_approval_") {
			t.Fatalf("the card was updated %+v, want one inert update of %s", u, cardTS)
		}
	})

	// A second card in the same thread for another approval: pressing one decides only that one and
	// updates only its message.
	t.Run("a second card in the same thread", func(t *testing.T) {
		const otherID, otherTS = "01M4A1B2C3D4E5F6G7H8J9K0MP", "1729.0800"
		rig := approvalsRig(t)
		seedCard(t, rig, cardTS, cardApprovalID)
		seedCard(t, rig, otherTS, otherID)
		if st := rig.interact(pressOf(t, otherTS, otherID, actionApprove)); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		got := rig.broker.decided()
		if len(got) != 1 || got[0].ApprovalID != otherID || got[0].MessageTS != otherTS {
			t.Fatalf("pressing the second card decided %+v", got)
		}
		u := rig.slack.updatesMade()
		if len(u) != 1 || u[0].ts != otherTS {
			t.Fatalf("the press updated %+v, want only %s", u, otherTS)
		}
	})

	// Review focus 4: the config drifted between the card and the click. The press is still the
	// broker's to decide — the interaction carries no repository, and the App must not invent one.
	t.Run("a channel whose repositories no longer list the room's", func(t *testing.T) {
		rig := newAppRig(t, Channel{Repositories: []string{"Smana/other-repo"}, MaxCostUSD: 2, Approvals: true})
		seedCard(t, rig, cardTS, cardApprovalID)
		if st := rig.interact(pressPayload(t, nil)); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		if got := rig.broker.decided(); len(got) != 1 {
			t.Fatalf("a channel's config drift stopped a press the config does not decide: %+v", got)
		}
	})

	t.Run("a deny press", func(t *testing.T) {
		rig := approvalsRig(t)
		seedCard(t, rig, cardTS, cardApprovalID)
		if st := rig.interact(pressOf(t, cardTS, cardApprovalID, actionDeny)); st != http.StatusOK {
			t.Fatalf("%d, want 200", st)
		}
		got := rig.broker.decided()
		if len(got) != 1 || got[0].Decision != "denied" {
			t.Fatalf("the broker was asked to record %+v, want one denied press", got)
		}
		if e := rig.slack.whispers(); len(e) != 1 || e[0].text != lineDenied {
			t.Fatalf("the clicker saw %+v, want %q", e, lineDenied)
		}
	})
}

// §6 step 4: Slack's own retry of one interaction is one broker call, one chat.update and one
// ephemeral. Two concurrent redeliveries of one press (the known race) are one decision too: the
// store's fence answers the second already_decided, and the record is one event.
func TestSlackInteractivityDedup(t *testing.T) {
	rig := approvalsRig(t)
	seedCard(t, rig, cardTS, cardApprovalID)
	press := formBody(pressPayload(t, nil))

	for i := range 2 {
		if st := rig.postInteraction(press); st != http.StatusOK {
			t.Fatalf("press %d: %d, want 200", i, st)
		}
	}
	if got := rig.broker.decided(); len(got) != 1 {
		t.Fatalf("a retried press reached the broker %d times, want 1: %+v", len(got), got)
	}
	if e := rig.slack.whispers(); len(e) != 1 {
		t.Fatalf("the clicker was answered %d times, want 1: %+v", len(e), e)
	}
	if u := rig.slack.updatesMade(); len(u) != 1 {
		t.Fatalf("the card was updated %d times, want 1: %+v", len(u), u)
	}
	if n := rig.metrics.count("interactions", "decided"); n != 1 {
		t.Fatalf("interactions/decided counted %d times, want 1", n)
	}
	if n := rig.metrics.count("interactions", "duplicate"); n != 1 {
		t.Fatalf("interactions/duplicate counted %d times, want 1", n)
	}

	// A distinct action_ts from the same user is a second press: the store's fence refuses it, and
	// the clicker reads the closed line while the card, already inert, is left alone.
	rig.broker.answerWith("already_decided")
	second := pressPayload(t, func(m map[string]any) {
		m["actions"] = []any{map[string]any{"action_id": actionApprove,
			"value": pressVersion + "|" + pressClass + "|" + cardApprovalID + "|" + cardBranch, "action_ts": "1729.0700"}}
	})
	if st := rig.interact(second); st != http.StatusOK {
		t.Fatalf("the second press: %d, want 200", st)
	}
	if got := rig.broker.decided(); len(got) != 2 {
		t.Fatalf("the broker saw %d presses, want 2: %+v", len(got), got)
	}
	e := rig.slack.whispers()
	if len(e) != 2 || e[1].text != lineDecided {
		t.Fatalf("the second press's answer: %+v, want %q once", e, lineDecided)
	}
	if u := rig.slack.updatesMade(); len(u) != 1 {
		t.Fatalf("a press that decided nothing rewrote the card: %+v", u)
	}
	if n := rig.metrics.count("interactions", "duplicate"); n != 2 {
		t.Fatalf("interactions/duplicate counted %d times, want 2", n)
	}

	// The known race: two goroutines post the same body, and the store (the stub, fenced) answers
	// already_decided to the loser. However many of them reached the broker, one decision is
	// recorded and the card is rewritten once.
	t.Run("two concurrent redeliveries", func(t *testing.T) {
		rig := approvalsRig(t)
		seedCard(t, rig, cardTS, cardApprovalID)
		rig.broker.fenceDecisions()
		press := formBody(pressPayload(t, nil))
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() { rig.postInteraction(press) })
		}
		wg.Wait()

		calls := rig.broker.decided()
		if len(calls) < 1 || len(calls) > 2 {
			t.Fatalf("the broker was called %d times, want 1 or 2: %+v", len(calls), calls)
		}
		recorded := 0
		for _, code := range rig.broker.decideAnswers() {
			if code == "approved" || code == "denied" {
				recorded++
			}
		}
		if recorded > 1 {
			t.Fatalf("%d decisions were recorded, want at most 1: %+v", recorded, rig.broker.decideAnswers())
		}
		if u := rig.slack.updatesMade(); len(u) != 1 {
			t.Fatalf("the card was updated %d times, want 1: %+v", len(u), u)
		}
	})
}

// Every refusal code has one fixed line, in a closed vocabulary that names no repository, branch,
// GitHub login or other person. The two success lines are fixed too, and an unknown code is
// unavailable's line: never the broker's own words.
func TestSlackDecideRefusalLines(t *testing.T) {
	const pageURL, user = "https://rooms.example", "U02DEF"
	cases := []struct{ code, line string }{
		{"not_linked", "Not decided: <@U02DEF> is not linked to a GitHub account here. Mention me with `link`, then press the button again."},
		{"link_unverified", "Not decided: the factory could not check your GitHub account just now. Press the button again in a minute."},
		{"not_permitted", "Not decided: only this room's approvers decide here. Ask an owner to add you as an approver."},
		{"repository_permission", "Not decided: your GitHub account cannot write to this room's repository."},
		{"four_eyes", "Not decided: you prompted this run, and four-eyes asks another approver to decide it."},
		{"already_decided", "Already decided; nothing changed."},
		{"approval_expired", "Not decided: the deadline had passed."},
		{"binding_mismatch", "Not decided: this card no longer matches the request. Open the room page: https://rooms.example"},
		{"not_supported", "Not decided from Slack: this approval is on the room page only: https://rooms.example"},
		{"not_found", "Not decided: this request is no longer in the room's log."},
		{"sealed", "Not decided: this room is sealed."},
		{"rate_limited", "Too many presses just now; wait a minute."},
		{"unavailable", "Not decided: the factory could not be reached just now. Press the button again."},
		{"approved", "Decided: the push goes out."},
		{"denied", "Decided: nothing is pushed."},
	}
	for _, tc := range cases {
		if got := answerLine(tc.code, user, pageURL); got != tc.line {
			t.Errorf("%s: %q, want %q", tc.code, got, tc.line)
		}
	}
	if got := answerLine("invented", user, pageURL); got != lineUnavailable {
		t.Fatalf("an unknown code says %q, want unavailable's line", got)
	}
	// The one line a press in a channel without the flag gets is fixed too (and not a broker code:
	// no press there ever reaches the broker).
	if got := notEnabledLine("https://rooms.example/"); got !=
		"Not decided from Slack: approvals are not turned on in this channel. Decide it on the room page: https://rooms.example" {
		t.Fatalf("not_enabled: %q", got)
	}

	// The anti-oracle rule, over the exact lines above: a line names the refusal's own subject and
	// nothing else — no repository, no branch, no GitHub login, no other person.
	for _, tc := range cases {
		if strings.Contains(tc.line, "Smana/") || strings.Contains(tc.line, "agent/") {
			t.Errorf("%s names a repository or a branch: %q", tc.code, tc.line)
		}
		if rest := strings.ReplaceAll(tc.line, "<@"+user+">", ""); strings.Contains(rest, "@") {
			t.Errorf("%s names a login or a person: %q", tc.code, tc.line)
		}
		// binding_mismatch's own sentence is the one allowed to speak of the card and the request,
		// and it names no branch either.
		if tc.code != "binding_mismatch" && strings.Contains(strings.ToLower(tc.line), "branch") {
			t.Errorf("%s names a branch: %q", tc.code, tc.line)
		}
	}
}

// The refusals that are not duplicates: a closed code reaches the clicker as its one line, the
// outcome is the counter's bounded word, and a press that decided nothing leaves the card's buttons
// on it (its own line says to press again). A broker that cannot be reached is the unavailable line.
func TestSlackDecideRefusalsAreAnswered(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     string
		err      error
		outcome  string
		wantLine string
	}{
		{"a room's approvers only", "not_permitted", nil, "refused", lineNotPermit},
		{"four eyes", "four_eyes", nil, "refused", lineFourEyes},
		{"the deadline passed", "approval_expired", nil, "refused", lineExpired},
		{"another class", "not_supported", nil, "not_supported",
			"Not decided from Slack: this approval is on the room page only: https://rooms.example"},
		{"the broker is unreachable", "", errors.New("connection refused"), "error", lineUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := approvalsRig(t)
			seedCard(t, rig, cardTS, cardApprovalID)
			if tc.err != nil {
				rig.broker.decideFails(tc.err)
			} else {
				rig.broker.answerWith(tc.code)
			}
			if st := rig.interact(pressPayload(t, nil)); st != http.StatusOK {
				t.Fatalf("%d, want 200", st)
			}
			if n := rig.metrics.count("interactions", tc.outcome); n != 1 {
				t.Fatalf("interactions/%s counted %d times, want 1", tc.outcome, n)
			}
			if e := rig.slack.whispers(); len(e) != 1 || e[0].user != "U02DEF" || e[0].text != tc.wantLine {
				t.Fatalf("the clicker saw %+v, want one %q", e, tc.wantLine)
			}
			if u := rig.slack.updatesMade(); len(u) != 0 {
				t.Fatalf("a refusal took the card's buttons off: %+v", u)
			}
		})
	}
}
