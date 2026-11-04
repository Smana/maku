// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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
	if err := rig.app.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
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
	go func() { _ = rig.app.Wait(context.Background()); close(waited) }()
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

// F-10: Wait is bounded by its context; past it, it names how many events are left.
func TestWaitReturnsAtItsDeadlineAndCountsTheUnfinished(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	var logs syncBuffer
	rig.app.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	gate := &gatedSlack{fakeSlack: rig.slack, entered: make(chan struct{}, 2), release: make(chan struct{})}
	rig.app.Slack, rig.app.Async = gate, nil
	if st := rig.postEvents(rig.eventBody("Ev-stuck", checkMark), true).Code; st != http.StatusOK {
		t.Fatalf("events: %d", st)
	}
	<-gate.entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := rig.app.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait: %v, want DeadlineExceeded", err)
	}
	if got := logs.String(); !strings.Contains(got, "events=1") {
		t.Fatalf("the unfinished count was not logged: %q", got)
	}
	close(gate.release)
	if err := rig.app.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after release: %v", err)
	}
}

// syncBuffer is a log sink a worker and the test can both write.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
