// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
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
