// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func actsRig(t *testing.T, intentActions ...string) (*appRig, *answerBroker) {
	t.Helper()
	rig, _ := withProposals(t)
	ch := rig.app.Channels["C03"]
	ch.Intents = IntentsOff
	ch.IntentActions = intentActions
	rig.app.Channels["C03"] = ch
	filedThread(rig, answerRoom)
	rig.threads.put(answerRoom, Thread{Channel: "C03", TS: proposalThreadTS})
	ab := &answerBroker{stubBroker: rig.broker, sum: answerSummary()}
	rig.app.Broker = ab
	return rig, ab
}

func pressAct(t *testing.T, rig *appRig, user, msgTS, actionID, value, actionTS string) {
	t.Helper()
	st := rig.interact(pressPayload(t, func(m map[string]any) {
		m["user"] = map[string]any{"id": user}
		m["container"] = map[string]any{"type": "message", "message_ts": msgTS, "channel_id": "C03"}
		m["message"] = map[string]any{"ts": msgTS}
		m["actions"] = []any{map[string]any{
			"action_id": actionID,
			"value":     value,
			"action_ts": actionTS,
		}}
	}))
	if st != http.StatusOK {
		t.Fatalf("press %s on %s: status %d, want 200", actionID, msgTS, st)
	}
}

func TestStopPhraseDrawsConfirm(t *testing.T) {
	cases := []struct {
		phrase      string
		action      string
		wantText    string
		confirmText string
	}{
		{
			phrase:      "stop",
			action:      "stop",
			wantText:    "<@U02DEF>: stop task #42? Its run ends now and the pull request stays open.",
			confirmText: "Stop the task",
		},
		{
			phrase:      "stop it",
			action:      "stop",
			wantText:    "<@U02DEF>: stop task #42? Its run ends now and the pull request stays open.",
			confirmText: "Stop the task",
		},
		{
			phrase:      "how do i stop it?",
			action:      "stop",
			wantText:    "<@U02DEF>: stop task #42? Its run ends now and the pull request stays open.",
			confirmText: "Stop the task",
		},
		{
			phrase:      "retry",
			action:      "retry",
			wantText:    "<@U02DEF>: retry task #42? It runs again from where it stopped.",
			confirmText: "Retry the task",
		},
		{
			phrase:      "try again!",
			action:      "retry",
			wantText:    "<@U02DEF>: retry task #42? It runs again from where it stopped.",
			confirmText: "Retry the task",
		},
	}

	for i, tc := range cases {
		t.Run(tc.phrase, func(t *testing.T) {
			rig, ab := actsRig(t, "stop", "retry")
			rig.replySaid("U02DEF", "1729.90"+strconv.Itoa(i), tc.phrase)

			if n := len(rig.broker.intents); n != 0 {
				t.Fatalf("%q called the classifier %d times with intents off", tc.phrase, n)
			}
			if len(ab.rooms) != 1 || ab.rooms[0] != "task/"+answerRoom {
				t.Fatalf("%q read %v, want one task/%s", tc.phrase, ab.rooms, answerRoom)
			}

			p := rig.lastPost()
			if p.text != tc.wantText {
				t.Fatalf("text = %q, want %q", p.text, tc.wantText)
			}
			blocks := blocksOf(t, p.blocks)
			if len(blocks) != 2 || blocks[0].Type != "section" || blocks[0].Text == nil || blocks[0].Text.Text != tc.wantText || blocks[1].Type != "actions" {
				t.Fatalf("blocks = %+v", blocks)
			}
			btns := blocks[1].buttons(t)
			wantVal := "v1|act|" + tc.action + "|" + answerRoom
			if len(btns) != 2 ||
				btns[0].Text.Text != tc.confirmText || btns[0].ActionID != actionActConfirm || btns[0].Value != wantVal ||
				btns[1].Text.Text != "Cancel" || btns[1].ActionID != actionActCancel || btns[1].Value != wantVal {
				t.Fatalf("buttons = %+v", btns)
			}

			meta, ok := p.meta.actPayload()
			if !ok {
				t.Fatalf("metadata = %+v, want %s", p.meta, actEventType)
			}
			exp, err := time.Parse(time.RFC3339Nano, meta.Expires)
			if err != nil {
				t.Fatalf("expires %q: %v", meta.Expires, err)
			}
			wantExp := rig.now.UTC().Add(10 * time.Minute)
			if meta.Action != tc.action || meta.Room != answerRoom || meta.Presser != "U02DEF" || !exp.Equal(wantExp) {
				t.Fatalf("payload = %+v (expires %v, want %v)", meta, exp, wantExp)
			}
		})
	}
}

func TestStopPhraseWithoutSwitch(t *testing.T) {
	t.Run("stop without switch answers the label line", func(t *testing.T) {
		rig, ab := actsRig(t)
		rig.replySaid("U02DEF", "1729.9001", "stop")

		p := rig.lastPost()
		if !strings.Contains(p.text, "factory/stop") || len(p.blocks) != 0 || p.meta != nil {
			t.Fatalf("post = %+v, want plain label line", p)
		}
		if len(ab.rooms) != 0 || len(rig.broker.intents) != 0 {
			t.Fatalf("reads=%v intents=%d, want 0", ab.rooms, len(rig.broker.intents))
		}
	})

	t.Run("retry without switch answers the comment line", func(t *testing.T) {
		rig, ab := actsRig(t)
		rig.replySaid("U02DEF", "1729.9002", "try again")

		p := rig.lastPost()
		if !strings.Contains(p.text, "/factory retry") || len(p.blocks) != 0 || p.meta != nil {
			t.Fatalf("post = %+v, want plain retry comment line", p)
		}
		if len(ab.rooms) != 0 || len(rig.broker.intents) != 0 {
			t.Fatalf("reads=%v intents=%d, want 0", ab.rooms, len(rig.broker.intents))
		}
	})
}

func TestStopButtonNamesPresser(t *testing.T) {
	rig, _ := actsRig(t, "stop")
	rig.webhook(`{"type":"room.waiting_for_human","room":"` + answerRoom + `","reason":"review_requested"}`)

	waitPost := rig.lastPost()
	blocks := blocksOf(t, waitPost.blocks)
	if len(blocks) != 2 || blocks[1].Type != "actions" {
		t.Fatalf("wait post blocks = %+v, want section + actions", blocks)
	}
	btns := blocks[1].buttons(t)
	if len(btns) != 1 || btns[0].Text.Text != "Stop" || btns[0].ActionID != actionActStop || btns[0].Value != "v1|act|stop|"+answerRoom {
		t.Fatalf("wait buttons = %+v", btns)
	}

	pressAct(t, rig, "U07GHI", waitPost.ts, actionActStop, btns[0].Value, "1729.0601")

	confirm := rig.lastPost()
	want := "<@U07GHI>: stop task #42? Its run ends now and the pull request stays open."
	if confirm.text != want || confirm.thread != proposalThreadTS {
		t.Fatalf("confirm = %+v, want %q in thread %s", confirm, want, proposalThreadTS)
	}
	meta, ok := confirm.meta.actPayload()
	if !ok || meta.Presser != "U07GHI" || meta.Action != "stop" || meta.Room != answerRoom {
		t.Fatalf("confirm meta = %+v (ok=%v)", meta, ok)
	}
}

func TestStopConfirmOnlyNamedPresser(t *testing.T) {
	rig, _ := actsRig(t, "stop")
	rig.replySaid("U02DEF", "1729.9001", "stop")
	confirm := rig.lastPost()
	val := "v1|act|stop|" + answerRoom

	pressAct(t, rig, "U09XYZ", confirm.ts, actionActConfirm, val, "1729.0601")
	pressAct(t, rig, "U09XYZ", confirm.ts, actionActCancel, val, "1729.0602")

	whispers := rig.slack.whispers()
	if len(whispers) != 2 {
		t.Fatalf("whispers = %+v, want 2", whispers)
	}
	for _, w := range whispers {
		if w.user != "U09XYZ" || w.text != "Only <@U02DEF> confirms this." {
			t.Fatalf("whisper = %+v, want Only <@U02DEF> confirms this.", w)
		}
	}
	if got := rig.broker.stoppedCalls(); len(got) != 0 {
		t.Fatalf("another user's press called Broker.Stop: %+v", got)
	}
	if edits := rig.slack.proposalEdits(); len(edits) != 0 {
		t.Fatalf("another user's press rewrote the confirm: %+v", edits)
	}
}

func TestStopConfirmExpires(t *testing.T) {
	rig, _ := actsRig(t, "stop")
	rig.replySaid("U02DEF", "1729.9001", "stop")
	confirm := rig.lastPost()

	rig.now = rig.now.Add(10*time.Minute + time.Second)
	pressAct(t, rig, "U02DEF", confirm.ts, actionActConfirm, "v1|act|stop|"+answerRoom, "1729.0601")

	if got := rig.broker.stoppedCalls(); len(got) != 0 {
		t.Fatalf("expired confirm called Broker.Stop: %+v", got)
	}
	whispers := rig.slack.whispers()
	if len(whispers) != 1 || whispers[0].user != "U02DEF" || whispers[0].text != "This confirmation expired." {
		t.Fatalf("whispers = %+v, want This confirmation expired.", whispers)
	}
	edits := rig.slack.proposalEdits()
	if len(edits) != 1 || edits[0].ts != confirm.ts || edits[0].text != "This confirmation expired." || string(edits[0].blocks) != "[]" {
		t.Fatalf("edits = %+v, want confirm rewritten inert", edits)
	}
}

func TestStopConfirmedCallsRoute(t *testing.T) {
	stopCases := []struct {
		result   string
		wantLine string
	}{
		{result: "stopped", wantLine: "Stopped."},
		{result: "not_maintainer", wantLine: "Only a maintainer of this repository can stop a task."},
		{result: "not_running", wantLine: "This task is not running."},
	}
	for _, tc := range stopCases {
		t.Run("stop/"+tc.result, func(t *testing.T) {
			rig, _ := actsRig(t, "stop")
			rig.broker.actAnswer = tc.result
			rig.replySaid("U02DEF", "1729.9001", "stop")
			confirm := rig.lastPost()

			pressAct(t, rig, "U02DEF", confirm.ts, actionActConfirm, "v1|act|stop|"+answerRoom, "1729.0601")

			calls := rig.broker.stoppedCalls()
			wantAct := Act{Team: "T01ABC", User: "U02DEF", Channel: "C03", MessageTS: confirm.ts, Room: answerRoom}
			if len(calls) != 1 || calls[0] != wantAct {
				t.Fatalf("stoppedCalls = %+v, want [%+v]", calls, wantAct)
			}
			edits := rig.slack.proposalEdits()
			if len(edits) != 1 || edits[0].ts != confirm.ts || edits[0].text != tc.wantLine || string(edits[0].blocks) != "[]" {
				t.Fatalf("edits = %+v, want %q with [] blocks", edits, tc.wantLine)
			}

			// A second press after the confirm was resolved does not reach the broker again.
			pressAct(t, rig, "U02DEF", confirm.ts, actionActConfirm, "v1|act|stop|"+answerRoom, "1729.0602")
			if len(rig.broker.stoppedCalls()) != 1 {
				t.Fatalf("second press called Broker.Stop again: %+v", rig.broker.stoppedCalls())
			}
		})
	}

	retryCases := []struct {
		result   string
		wantLine string
	}{
		{result: "retried", wantLine: "Retried."},
		{result: "not_maintainer", wantLine: "Only a maintainer of this repository can retry a task."},
		{result: "not_escalated", wantLine: "This task is not escalated."},
	}
	for _, tc := range retryCases {
		t.Run("retry/"+tc.result, func(t *testing.T) {
			rig, _ := actsRig(t, "retry")
			rig.broker.actAnswer = tc.result
			rig.replySaid("U02DEF", "1729.9001", "retry")
			confirm := rig.lastPost()

			pressAct(t, rig, "U02DEF", confirm.ts, actionActConfirm, "v1|act|retry|"+answerRoom, "1729.0601")

			calls := rig.broker.retriedCalls()
			wantAct := Act{Team: "T01ABC", User: "U02DEF", Channel: "C03", MessageTS: confirm.ts, Room: answerRoom}
			if len(calls) != 1 || calls[0] != wantAct {
				t.Fatalf("retriedCalls = %+v, want [%+v]", calls, wantAct)
			}
			edits := rig.slack.proposalEdits()
			if len(edits) != 1 || edits[0].ts != confirm.ts || edits[0].text != tc.wantLine || string(edits[0].blocks) != "[]" {
				t.Fatalf("edits = %+v, want %q with [] blocks", edits, tc.wantLine)
			}
		})
	}

	t.Run("cancel rewrites in place without calling broker", func(t *testing.T) {
		rig, _ := actsRig(t, "stop")
		rig.replySaid("U02DEF", "1729.9001", "stop")
		confirm := rig.lastPost()

		pressAct(t, rig, "U02DEF", confirm.ts, actionActCancel, "v1|act|stop|"+answerRoom, "1729.0601")

		if len(rig.broker.stoppedCalls()) != 0 {
			t.Fatalf("cancel called Broker.Stop: %+v", rig.broker.stoppedCalls())
		}
		edits := rig.slack.proposalEdits()
		if len(edits) != 1 || edits[0].ts != confirm.ts || edits[0].text != "Cancelled by <@U02DEF>." || string(edits[0].blocks) != "[]" {
			t.Fatalf("edits = %+v, want Cancelled by <@U02DEF>.", edits)
		}
	})
}

func TestRetryOnlyOnEscalated(t *testing.T) {
	rig, _ := actsRig(t, "retry")

	// Escalated tasks emit room.ended with state "failed" (summary.StateFailed).
	rig.webhook(`{"type":"room.ended","room":"` + answerRoom + `","state":"failed"}`)
	escalated := rig.lastPost()
	blocks := blocksOf(t, escalated.blocks)
	if len(blocks) != 2 || blocks[1].Type != "actions" {
		t.Fatalf("escalated end post blocks = %+v, want section + actions", blocks)
	}
	btns := blocks[1].buttons(t)
	if len(btns) != 2 || btns[0].Text.Text != "Retry" || btns[0].ActionID != actionActRetry || btns[0].Value != "v1|act|retry|"+answerRoom ||
		btns[1].Text.Text != "Run again deeper" || btns[1].ActionID != actionActRetryDeeper || btns[1].Value != "v1|act|retry_deeper|"+answerRoom {
		t.Fatalf("escalated end buttons = %+v", btns)
	}

	// Merged and Ended: stopped (and done) must carry no Retry button.
	for _, ev := range []string{
		`{"type":"room.merged","room":"` + answerRoom + `","pr":12}`,
		`{"type":"room.ended","room":"` + answerRoom + `","state":"stopped"}`,
		`{"type":"room.ended","room":"` + answerRoom + `","state":"done"}`,
	} {
		rig.threads.put(answerRoom, Thread{Channel: "C03", TS: proposalThreadTS})
		rig.webhook(ev)
		p := rig.lastPost()
		if len(p.blocks) != 0 {
			t.Fatalf("%s carried blocks %s, want no button", ev, p.blocks)
		}
	}
}

func TestTheDeeperConfirmNamesTheCeiling(t *testing.T) {
	line := deeperConfirm("U123", 12, "careful", 13.60)
	if line != "<@U123>: run task #12 again under careful? It may spend up to $13.60 in all." {
		t.Fatalf("got %q", line)
	}
}

func TestBrokerStopAndRetry(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []map[string]string
		paths  []string
		answer = `{"result":"stopped"}`
		status = http.StatusOK
		fails  atomic.Int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		bodies = append(bodies, m)
		paths = append(paths, r.URL.Path)
		ans, st := answer, status
		mu.Unlock()
		if fails.Load() > 0 {
			fails.Add(-1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(st)
		_, _ = io.WriteString(w, ans)
	}))
	t.Cleanup(srv.Close)

	b := Broker{URL: srv.URL, Tokens: staticTokens("tok"), HC: srv.Client()}
	act := Act{Team: "T01ABC", User: "U02DEF", Channel: "C03", MessageTS: "1729.1001", Room: answerRoom}

	res, err := b.Stop(context.Background(), act)
	if err != nil || res != (ActResult{Result: "stopped"}) {
		t.Fatalf("Stop = %+v, %v", res, err)
	}
	wantBody := map[string]string{
		"team": "T01ABC", "user": "U02DEF", "channel": "C03",
		"messageTS": "1729.1001", "room": answerRoom,
	}
	if !maps.Equal(bodies[0], wantBody) || paths[0] != "/api/slack/stops" {
		t.Fatalf("body=%+v path=%s", bodies[0], paths[0])
	}

	mu.Lock()
	answer = `{"result":"retried"}`
	mu.Unlock()
	fails.Store(1)
	res, err = b.Retry(context.Background(), act)
	if err != nil || res != (ActResult{Result: "retried"}) || paths[len(paths)-1] != "/api/slack/retries" {
		t.Fatalf("Retry = %+v, %v (paths=%v)", res, err, paths)
	}

	mu.Lock()
	answer = `{"result":"rate_limited"}`
	status = http.StatusTooManyRequests
	mu.Unlock()
	res, err = b.Stop(context.Background(), act)
	if err != nil || res.Result != "rate_limited" {
		t.Fatalf("429 Stop = %+v, %v", res, err)
	}
}

type blockingStopBroker struct {
	BrokerAPI
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockingStopBroker) Stop(_ context.Context, _ Act) (ActResult, error) {
	if b.calls.Add(1) == 1 {
		close(b.entered)
		<-b.release
	}
	return ActResult{Result: "stopped"}, nil
}

func TestActPressSerializesDoubleClick(t *testing.T) {
	rig, _ := actsRig(t, "stop")
	rig.replySaid("U02DEF", "1729.9001", "stop")
	confirmTS := rig.lastPost().ts

	bb := &blockingStopBroker{
		BrokerAPI: rig.app.Broker,
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	rig.app.Broker = bb

	in := interaction{
		Team:      "T01ABC",
		User:      "U02DEF",
		Channel:   "C03",
		MessageTS: confirmTS,
		ActionID:  actionActConfirm,
		Value:     actValue(actStop, answerRoom),
	}
	outcomes := make([]string, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		outcomes[0] = rig.app.actPress(t.Context(), in)
	}()
	<-bb.entered
	go func() {
		defer wg.Done()
		outcomes[1] = rig.app.actPress(t.Context(), in)
	}()
	time.Sleep(15 * time.Millisecond)
	close(bb.release)
	wg.Wait()

	if bb.calls.Load() != 1 {
		t.Fatalf("Stop called %d times on double-click, want 1", bb.calls.Load())
	}
	if outcomes[0] != "decided" || outcomes[1] != "refused" {
		t.Fatalf("outcomes = %v, want [decided refused]", outcomes)
	}
}

func TestWebhooksNilNow(t *testing.T) {
	rig, _ := actsRig(t)
	rig.app.Now = nil
	body := `{"type":"room.ended","room":"` + answerRoom + `","state":"done"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhooks", strings.NewReader(body))
	// Invalid signature is fine: we are verifying Webhooks() does not panic when Now is nil.
	req.Header.Set("webhook-id", "evt_nil_now")
	req.Header.Set("webhook-timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("webhook-signature", "v1,AAAA")
	rr := httptest.NewRecorder()
	rig.app.Webhooks().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}
