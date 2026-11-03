// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// gatedSlack holds every Message call until release closes, signalling each arrival on entered:
// it pins a worker on an event before the handler takes editMu.
type gatedSlack struct {
	*fakeSlack
	entered chan struct{}
	release chan struct{}
}

func (g *gatedSlack) Message(ctx context.Context, channel, ts string) (Message, error) {
	g.entered <- struct{}{}
	<-g.release
	return g.fakeSlack.Message(ctx, channel, ts)
}

// gatedBroker holds every RunCreate until release closes.
type gatedBroker struct {
	*stubBroker
	entered chan struct{}
	release chan struct{}
}

func (g *gatedBroker) RunCreate(ctx context.Context, c RunCreate) (RunResult, error) {
	g.entered <- struct{}{}
	<-g.release
	return g.stubBroker.RunCreate(ctx, c)
}

const checkMark = `{"type":"reaction_added","user":"U02DEF","reaction":"white_check_mark","item":{"type":"message","channel":"C03","ts":"1729.0009"}}`

// F-10: 8 workers each hold an event and 64 wait; the next event answers 503, so Slack retries
// it, and its id is forgotten, so the retry is handled rather than read as a duplicate.
func TestAFullEventQueueAnswers503(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	gate := &gatedSlack{fakeSlack: rig.slack, entered: make(chan struct{}, eventWorkers+eventQueue+1), release: make(chan struct{})}
	rig.app.Slack, rig.app.Async = gate, nil
	react := func(id string) int { return rig.postEvents(rig.eventBody(id, checkMark), true).Code }

	for i := range eventWorkers {
		if st := react(fmt.Sprintf("Ev-busy-%d", i)); st != http.StatusOK {
			t.Fatalf("event %d for a worker: %d", i, st)
		}
	}
	for range eventWorkers { // every worker now holds one event
		<-gate.entered
	}
	for i := range eventQueue {
		if st := react(fmt.Sprintf("Ev-queued-%d", i)); st != http.StatusOK {
			t.Fatalf("queued event %d: %d", i, st)
		}
	}
	if st := react("Ev-over"); st != http.StatusServiceUnavailable {
		t.Fatalf("the event past the queue: %d, want 503", st)
	}
	if n := rig.metrics.count("events", "busy"); n != 1 {
		t.Fatalf("busy counted %d times", n)
	}
	if !rig.app.events.add("Ev-over") {
		t.Fatal("the refused event's id is still marked seen: Slack's retry would be dropped as a duplicate")
	}
	close(gate.release)
	rig.app.Wait()
	if st := react("Ev-after-wait"); st != http.StatusServiceUnavailable {
		t.Fatalf("an event after Wait: %d, want 503", st)
	}
}

// F-10: an event in flight when shutdown starts finishes, its thread map Put and its reply
// included, before Wait returns; its request's context being cancelled changes nothing.
func TestWaitFinishesTheEventInFlight(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.slack.seed(Message{TS: "1729.0009", ThreadTS: "1729.0001", User: "U0BOT", Text: "draft",
		Metadata: &Metadata{EventType: draftEventType, EventPayload: draftPayload("Smana/ai-native-ref", "t", "b")}})
	gate := &gatedBroker{stubBroker: rig.broker, entered: make(chan struct{}, 1), release: make(chan struct{})}
	rig.app.Broker, rig.app.Async = gate, nil

	ctx, cancel := context.WithCancel(context.Background())
	body := rig.eventBody("Ev-inflight", checkMark)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/slack/events", strings.NewReader(body))
	ts := strconv.FormatInt(rig.now.Unix(), 10)
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", slackSign(rigSecret, ts, body))
	w := httptest.NewRecorder()
	rig.app.Events().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("events: %d", w.Code)
	}
	<-gate.entered // RunCreate has the event
	cancel()       // SIGTERM: the request's context goes

	waited := make(chan struct{})
	go func() { rig.app.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("Wait returned with an event in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(gate.release)
	<-waited
	if th, ok := rig.threads.get("aaaaaaaa"); !ok || th.TS != "1729.0001" {
		t.Fatalf("the thread map was not written before Wait returned: %+v %v", th, ok)
	}
	if !strings.Contains(rig.lastPost().text, "Filed #42") {
		t.Fatalf("the reply was not posted before Wait returned: %q", rig.lastPost().text)
	}
}
