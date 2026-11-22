// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/intake"
)

// blockingHandoffBroker gates Handoff calls whose text contains gateMatch, letting unrelated
// calls proceed freely while tracking concurrency per key.
type blockingHandoffBroker struct {
	*stubBroker
	gateMatch string
	entered   chan struct{}
	release   chan struct{}

	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (b *blockingHandoffBroker) Handoff(ctx context.Context, in HandoffRequest) (HandoffResult, error) {
	if b.gateMatch != "" && (strings.Contains(in.Text, b.gateMatch) || strings.Contains(in.Answer, b.gateMatch)) {
		b.entered <- struct{}{}
		<-b.release
	}
	cur := b.inFlight.Add(1)
	for {
		prev := b.maxSeen.Load()
		if cur <= prev || b.maxSeen.CompareAndSwap(prev, cur) {
			break
		}
	}
	defer b.inFlight.Add(-1)
	return b.stubBroker.Handoff(ctx, in)
}

// blockingTaskBroker gates Broker.Task for a specific room, verifying that read-only queries on
// already-filed threads do not hold the thread edit lock across broker calls.
type blockingTaskBroker struct {
	*stubBroker
	gateOnce sync.Once
	entered  chan struct{}
	release  chan struct{}
}

func (b *blockingTaskBroker) Task(ctx context.Context, room string) (TaskResult, error) {
	gated := false
	b.gateOnce.Do(func() { gated = true })
	if gated {
		b.entered <- struct{}{}
		<-b.release
	}
	return b.stubBroker.Task(ctx, room)
}

// REL-H1: a slow planner call on thread A must never block a mention on unrelated thread B.
func TestPerThreadLockDoesNotBlockUnrelatedThreads(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.app.PlanningAfter = time.Hour // keep planning status messages out of the test
	rig.broker.storedPlan = planFixture(t, "slack-one-issue")
	br := &blockingHandoffBroker{
		stubBroker: rig.broker,
		gateMatch:  "slow-thread-A",
		entered:    make(chan struct{}, 1),
		release:    make(chan struct{}),
	}
	rig.app.Broker = br

	doneA := make(chan string, 1)
	go func() {
		doneA <- rig.app.mention(t.Context(), slackEvent{
			Type: "app_mention", Channel: "C03", User: "U02DEF", TS: "1729.0001",
			Text: "<@U0BOT> slow-thread-A",
		})
	}()
	select {
	case <-br.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("thread A never entered Handoff")
	}

	doneB := make(chan string, 1)
	go func() {
		doneB <- rig.app.mention(t.Context(), slackEvent{
			Type: "app_mention", Channel: "C03", User: "U02DEF", TS: "1729.0002",
			Text: "<@U0BOT> fast-thread-B",
		})
	}()
	select {
	case out := <-doneB:
		if out != "proposed" {
			t.Fatalf("thread B outcome = %q, want proposed", out)
		}
	case <-time.After(2 * time.Second):
		close(br.release)
		t.Fatal("thread B was blocked by thread A's slow Handoff")
	}

	close(br.release)
	if out := <-doneA; out != "proposed" {
		t.Fatalf("thread A outcome = %q, want proposed", out)
	}
	if n := rig.app.edits.len(); n != 0 {
		t.Fatalf("thread lock map leaked %d entries", n)
	}
}

// REL-H1: concurrent replies on the same thread serialize cleanly and clean up their lock entry.
func TestPerThreadLockSerializesSameThreadAndCleansUp(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.app.PlanningAfter = time.Hour
	plan := planFixture(t, "slack-one-issue")
	p, err := newProposal(plan, intake.Hash(plan), "U02DEF", 1)
	if err != nil {
		t.Fatalf("newProposal: %v", err)
	}
	rig.broker.storedPlan = plan
	rig.slack.seed(Message{TS: "1729.0001", User: "U02DEF", Text: "<@U0BOT> fix the bug"})
	rig.slack.seed(Message{TS: "1729.0002", ThreadTS: "1729.0001", User: "U0BOT", Text: "Proposal v1",
		Metadata: metaOf(t, proposalEventType, p)})

	br := &blockingHandoffBroker{
		stubBroker: rig.broker,
		gateMatch:  "first-reply",
		entered:    make(chan struct{}, 1),
		release:    make(chan struct{}),
	}
	rig.app.Broker = br

	done1 := make(chan string, 1)
	go func() {
		done1 <- rig.app.reply(t.Context(), slackEvent{
			Type: "message", Channel: "C03", User: "U02DEF", TS: "1729.0003", ThreadTS: "1729.0001",
			Text: "first-reply",
		})
	}()
	select {
	case <-br.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reply 1 never entered Handoff")
	}

	done2 := make(chan string, 1)
	go func() {
		done2 <- rig.app.reply(t.Context(), slackEvent{
			Type: "message", Channel: "C03", User: "U02DEF", TS: "1729.0004", ThreadTS: "1729.0001",
			Text: "second-reply",
		})
	}()
	select {
	case out := <-done2:
		close(br.release)
		t.Fatalf("reply 2 finished (%q) while reply 1 still held the thread lock", out)
	case <-time.After(80 * time.Millisecond):
	}

	close(br.release)
	if out := <-done1; out != "updated" {
		t.Fatalf("reply 1 outcome = %q, want updated", out)
	}
	if out := <-done2; out != "updated" {
		t.Fatalf("reply 2 outcome = %q, want updated", out)
	}
	if got := br.maxSeen.Load(); got != 1 {
		t.Fatalf("max concurrent Handoff on same thread = %d, want 1", got)
	}
	if n := rig.app.edits.len(); n != 0 {
		t.Fatalf("thread lock map leaked %d entries", n)
	}
}

// REL-H1: read-only status queries on an already-filed thread release the thread lock before
// querying the broker, so two questions on the same filed thread do not block each other.
func TestFiledThreadReadOnlyRepliesDoNotHoldLockAcrossBroker(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.slack.seed(Message{TS: "1729.0001", User: "U02DEF", Text: "<@U0BOT> fix the bug"})
	rig.slack.seed(Message{TS: "1729.0002", ThreadTS: "1729.0001", User: "U0BOT", Text: "Filed #42",
		Metadata: metaOf(t, filedEventType, MarkerPayload{Repo: "Smana/ai-native-ref", Room: "aaaaaaaa"})})
	s := answerSummary()
	rig.broker.summary = &s

	br := &blockingTaskBroker{
		stubBroker: rig.broker,
		entered:    make(chan struct{}, 1),
		release:    make(chan struct{}),
	}
	rig.app.Broker = br

	done1 := make(chan string, 1)
	go func() {
		done1 <- rig.app.reply(t.Context(), slackEvent{
			Type: "message", Channel: "C03", User: "U02DEF", TS: "1729.0003", ThreadTS: "1729.0001",
			Text: "status",
		})
	}()
	select {
	case <-br.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first status query never entered Broker.Task")
	}

	done2 := make(chan string, 1)
	go func() {
		done2 <- rig.app.reply(t.Context(), slackEvent{
			Type: "message", Channel: "C03", User: "U02DEF", TS: "1729.0004", ThreadTS: "1729.0001",
			Text: "status",
		})
	}()
	select {
	case out := <-done2:
		if out != "answered" {
			t.Fatalf("second status query = %q, want answered", out)
		}
	case <-time.After(2 * time.Second):
		close(br.release)
		t.Fatal("second read-only query on filed thread was blocked by first query's broker call")
	}

	close(br.release)
	if out := <-done1; out != "answered" {
		t.Fatalf("first status query = %q, want answered", out)
	}
	if n := rig.app.edits.len(); n != 0 {
		t.Fatalf("thread lock map leaked %d entries", n)
	}
}

func TestThreadLocksRefCountAndCleanup(t *testing.T) {
	var tl threadLocks
	const goroutines = 16
	const threads = 4
	var wg sync.WaitGroup
	var active [threads]atomic.Int32
	for g := range goroutines {
		wg.Go(func() {
			idx := g % threads
			th := "1729.000" + string(rune('1'+idx))
			for range 20 {
				unlock := tl.lock("C03", th)
				if n := active[idx].Add(1); n != 1 {
					t.Errorf("concurrent holders on thread %d: %d", idx, n)
				}
				active[idx].Add(-1)
				unlock()
				unlock() // idempotent
			}
		})
	}
	wg.Wait()
	if n := tl.len(); n != 0 {
		t.Fatalf("threadLocks retained %d entries after all unlocks", n)
	}
}
