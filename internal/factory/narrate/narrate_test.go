// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	summ "github.com/Smana/maku/internal/summary"
	"github.com/Smana/maku/internal/wire"
)

const bot = "ogenki-agent-factory[bot]" // forgetest.Fake's author, config.factoryLogin

func task() *v1alpha1.Task {
	return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"},
		Spec:   v1alpha1.TaskSpec{Issue: 7, Budget: v1alpha1.Budget{Tier: "standard"}},
		Status: v1alpha1.TaskStatus{RoomRef: "3buqdlot"}}
}

// Δ6: started carries the persona, run id, branch, budget and watch link.
func TestStartedCarriesWhatAHumanNeeds(t *testing.T) {
	e := Started(task(), runs.Spec{RunID: "7f3cq2xz", Role: "implementer", Persona: "implementer", Branch: "agent/3buqdlot",
		MaxTokens: 1_500_000, MaxMinutes: 45}, "https://rooms.priv.aws.ogenki.io/")
	for _, want := range []string{"the implementer's run", "7f3cq2xz", "agent/3buqdlot", "1.5 M tokens", "45 minutes",
		"https://rooms.priv.aws.ogenki.io/r/3buqdlot", "factory/stop"} {
		if !strings.Contains(e.Body, want) {
			t.Errorf("started lacks %q:\n%s", want, e.Body)
		}
	}
	if strings.Contains(e.Body, "tier ") {
		t.Errorf("started mentions tier:\n%s", e.Body)
	}
	if e.Key != "run-7f3cq2xz-started" {
		t.Errorf("key %q", e.Key)
	}
}

func TestTheRunStartNamesThePersonaAndDollars(t *testing.T) {
	tk := task()
	tk.Spec.Budget.Tier, tk.Spec.Budget.RunMicroUSD = "standard", 2_550_000
	ev := Started(tk, runs.Spec{RunID: "r-01", Role: "triager", Persona: "scoper", Branch: "agent/3buqdlot", MaxMinutes: 45}, "https://rooms.example")
	if !strings.Contains(ev.Body, "the scoper's run") || !strings.Contains(ev.Body, "up to $2.55 for this run") {
		t.Fatalf("got:\n%s", ev.Body)
	}
	for _, bad := range []string{"(triager)", "tier ", "tokens"} {
		if strings.Contains(ev.Body, bad) {
			t.Errorf("the run-start comment says %q", bad)
		}
	}
}

func TestTheTaskCardIsPostedBeforeTheFirstRun(t *testing.T) {
	cardText := "- **Workflow:** standard · this repository's default\n" +
		"- **Will run:** implementer → reviewer → CI → merge gate\n" +
		"- **Cost:** up to $5.10\n"
	ev := TaskCard(task(), cardText)
	if ev.Key != "task-card" || !strings.Contains(ev.Body, "Workflow:") ||
		!strings.Contains(ev.Body, "To change it, a maintainer applies the label `workflow:<name>` before `factory/ready`") {
		t.Fatalf("%s:\n%s", ev.Key, ev.Body)
	}
}

func TestEndedExplainsTheReason(t *testing.T) {
	tk := task()
	tk.Status.Usage.Tokens = 1_600_000
	e := Ended(tk, v1alpha1.PhaseEscalated, "budget-run")
	if !strings.Contains(e.Body, "needs a maintainer: the run spent its token budget.") || !strings.Contains(e.Body, "1.6 M") ||
		!strings.Contains(e.Body, "Comment `/factory retry` on this issue to run it again (a maintainer only)") {
		t.Fatal(e.Body)
	}
	// F-4: at the task cap a plain retry is refused, so the ending does not offer it.
	if c := Ended(tk, v1alpha1.PhaseEscalated, "budget-task"); strings.Contains(c.Body, "to run it again (a maintainer only)") ||
		!strings.Contains(c.Body, "`/factory retry deeper`") || !strings.Contains(c.Body, "less than a whole run needs") {
		t.Fatal(c.Body)
	}
	if s := Ended(tk, v1alpha1.PhaseClosed, "stale"); !strings.Contains(s.Body,
		"was closed: the pull request had no maintainer activity (a review, a comment or a push) for 14 days.") {
		t.Fatal(s.Body)
	}
	if Ended(tk, v1alpha1.PhaseEscalated, "pod_lost").Key == Ended(tk, v1alpha1.PhaseDone, "merged").Key {
		t.Fatal("each ending has its own key")
	}
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, MergedBy: "Smana"}
	if d := Ended(tk, v1alpha1.PhaseDone, "merged"); !strings.Contains(d.Body, "Merged by @Smana.") {
		t.Fatal(d.Body)
	}
	if r := Ended(tk, v1alpha1.PhaseReverted, "main_red"); strings.Contains(r.Body, "Merged by") {
		t.Fatalf("only a done task credits its merger: %s", r.Body)
	}
}

// The reminder mentions every maintainer, once per quiet spell of each AwaitingHuman stay; a
// retry is narrated once per retry.
func TestReminderAndRetrying(t *testing.T) {
	tk := task()
	spell := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	e := Reminder(tk, []string{"Smana", "alice"}, spell, false)
	if !strings.HasPrefix(e.Body, "@Smana @alice: the pull request of agent factory task `3buqdlot` has had no maintainer activity for 48 hours.") ||
		!strings.Contains(e.Body, "closes itself after 14 days") || !strings.Contains(e.Body, "or a push to its branch keeps it open") {
		t.Fatal(e.Body)
	}
	if a := Reminder(tk, []string{"Smana"}, spell, true); a.Key != e.Key || !strings.Contains(a.Body, "is approved and has waited 48 hours for a merge") ||
		strings.Contains(a.Body, "closes itself") {
		t.Fatalf("an approved pull request is never closed: %+v", a)
	}
	if e.Key == Reminder(tk, nil, spell.Add(time.Hour), false).Key {
		t.Fatal("a new quiet spell has its own reminder")
	}
	tk.Status.Runs = append(tk.Status.Runs, v1alpha1.RunRecord{ID: "aaaaaaaa"})
	if e.Key == Reminder(tk, nil, spell, false).Key {
		t.Fatal("a new AwaitingHuman stay has its own reminder")
	}
	// RemindedSince: the latest spell of this stay, posted or still queued; another stay's is not.
	tk.Status.Narrated = []string{"remind-0-9999999999", "remind-1-100", "remind-1-junk", "remind-11-900"}
	tk.Status.Outbox = []v1alpha1.Narration{{Key: "remind-1-300", Number: 7}}
	if at, ok := RemindedSince(tk); !ok || at.Unix() != 300 {
		t.Fatalf("%v %v", at, ok)
	}
	tk.Status.Outbox = nil
	if at, ok := RemindedSince(tk); !ok || at.Unix() != 100 {
		t.Fatalf("%v %v", at, ok)
	}
	tk.Status.Narrated = []string{"remind-0-100"}
	if _, ok := RemindedSince(tk); ok {
		t.Fatal("another stay's reminder")
	}
	tk.Status.Retries = 1
	r := Retrying(tk, "Smana", "")
	if r.Body != "Agent factory task `3buqdlot` is retrying, as @Smana asked." {
		t.Fatal(r.Body)
	}
	tk.Status.Retries = 2
	if r.Key == Retrying(tk, "Smana", "").Key {
		t.Fatal("each retry is narrated")
	}
}

func TestPROpenedAndRefused(t *testing.T) {
	if e := PROpened(task(), 12, "https://github.com/Smana/cloud-native-ref/pull/12", "7f3cq2xz"); e.Key != "pr-opened" ||
		!strings.Contains(e.Body, "#12") || !strings.Contains(e.Body, "7f3cq2xz") {
		t.Fatalf("%+v", e)
	}
	at := time.Unix(1_790_000_000, 0)
	e := Refused(7, "edited_after_label", at)
	if e.Key != "refused-edited_after_label-1790000000" || !strings.Contains(e.Body, "edited after it was labelled") {
		t.Fatalf("%+v", e)
	}
	if Refused(7, "edited_after_label", at.Add(time.Second)).Key == e.Key {
		t.Fatal("each label is answered once: the key carries its time")
	}
}

func TestTokensAndReasons(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1_000: "1 k", 300_000: "300 k", 1_000_000: "1.0 M", 1_550_000: "1.6 M"} {
		if got := Tokens(n); got != want {
			t.Errorf("Tokens(%d) = %q, want %q", n, got, want)
		}
	}
	if got := Reason("class_unarmed"); got != "the factory does not merge this class: a maintainer reviews and merges it" ||
		strings.Contains(got, "predicted") {
		t.Fatalf("class_unarmed is not a mismatch: %q", got)
	}
	if Reason("kill_switch") != "the factory's kill switch is engaged" || Reason("something_new") != "something_new" {
		t.Fatal("known reasons read as prose; an unknown one is shown as it is")
	}
	if got := Reason("overlap_active_duplicate"); got != "an active task is already pursuing the same outcome" {
		t.Fatalf("overlap_active_duplicate reads %q", got)
	}
}

// gcp-0, 2026-10-29, task xlcx2775: a judge continuation ran into the wall clock and escalated as
// "the run hit its wall-clock limit". The harness now ends a continuation at its time box, as stalled
// or not_met, and the escalation says that a continuation's box is one way there.
func TestALoopEndNamesTheContinuationsTimeBox(t *testing.T) {
	for _, r := range []string{"stalled", "not_met"} {
		if got := Reason(r); !strings.Contains(got, "continuation") || !strings.Contains(got, "time box") {
			t.Errorf("Reason(%q) = %q, want it to name a continuation's time box", r, got)
		}
	}
}

// R22: one comment per event, even across a crash between posting and recording.
func TestPostIsIdempotent(t *testing.T) {
	f := forgetest.NewFake()
	n := Narrator{Forge: f, Login: bot}
	tk := task()
	e := Event{Key: "pr-opened", Body: "Run 7f3cq2xz opened #12."}
	for range 2 {
		if err := n.Post(t.Context(), tk, 7, e); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(tk.Status.Narrated, []string{"pr-opened"}) {
		t.Fatalf("a key is recorded once: %q", tk.Status.Narrated)
	}
	tk.Status.Narrated = nil // the status write was lost
	if err := n.Post(t.Context(), tk, 7, e); err != nil {
		t.Fatal(err)
	}
	if got := f.Comments(7); len(got) != 1 || !strings.Contains(got[0], Marker("3buqdlot", "pr-opened")) {
		t.Fatalf("%q", got)
	}
	if !slices.Equal(tk.Status.Narrated, []string{"pr-opened"}) {
		t.Fatalf("the marker found is recorded: %q", tk.Status.Narrated)
	}
	if err := n.Post(t.Context(), tk, 0, Event{Key: "ended", Body: "x"}); err != nil || len(f.Comments(0)) != 0 {
		t.Fatal("number 0 means nowhere to narrate")
	}
	// The factory's own comment carrying another event's marker is no dedup for this one.
	if err := n.Post(t.Context(), tk, 7, Event{Key: "ended", Body: "Done."}); err != nil || len(f.Comments(7)) != 2 {
		t.Fatalf("%v %q", err, f.Comments(7))
	}
}

// Anyone can comment on a public issue: a marker only counts in the factory's own comments, so
// nobody can silence a narration by posting it first.
func TestAMarkerCountsOnlyInTheFactorysComments(t *testing.T) {
	f := &scripted{Fake: forgetest.NewFake(), recent: []forge.Comment{
		{Author: "someone", Body: "hi " + Marker("3buqdlot", "pr-opened")},
		{Author: "ogenki-agent-factory", Body: Marker("3buqdlot", "pr-opened")}, // a user, not the App
	}}
	n := Narrator{Forge: f, Login: bot}
	if err := n.PostOnce(t.Context(), 7, "3buqdlot", Event{Key: "pr-opened", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(f.Comments(7)) != 1 {
		t.Fatal("a forged marker silenced the narration")
	}
	f.recent = append(f.recent, forge.Comment{Author: bot, Body: "x\n\n" + Marker("3buqdlot", "pr-opened")})
	if err := n.PostOnce(t.Context(), 7, "3buqdlot", Event{Key: "pr-opened", Body: "x"}); err != nil || len(f.Comments(7)) != 1 {
		t.Fatal("the factory's own marker is the dedup")
	}
}

func TestAFailedCallRecordsNothing(t *testing.T) {
	for name, f := range map[string]*scripted{
		"listing fails": {Fake: forgetest.NewFake(), listErr: errors.New("502")},
		"posting fails": {Fake: forgetest.NewFake(), postErr: errors.New("502")},
	} {
		t.Run(name, func(t *testing.T) {
			tk := task()
			err := Narrator{Forge: f, Login: bot}.Post(t.Context(), tk, 7, Event{Key: "pr-opened", Body: "x"})
			if err == nil || len(tk.Status.Narrated) != 0 {
				t.Fatalf("%v %q", err, tk.Status.Narrated)
			}
		})
	}
}

func TestNarratorNeedsItsLogin(t *testing.T) {
	f := forgetest.NewFake()
	if err := (Narrator{Forge: f}).PostOnce(t.Context(), 7, "3buqdlot", Event{Key: "k", Body: "x"}); err == nil || len(f.Comments(7)) != 0 {
		t.Fatal("without the factory's login no marker can be trusted")
	}
}

// The CRD holds 512 keys (review I3): the oldest go, the newest stay.
func TestNarratedStaysBounded(t *testing.T) {
	f := forgetest.NewFake()
	n := Narrator{Forge: f, Login: bot}
	tk := task()
	for i := range MaxNarrated + 40 {
		if err := n.Post(t.Context(), tk, 7, Event{Key: fmt.Sprintf("k%d", i), Body: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if l := len(tk.Status.Narrated); l != MaxNarrated || tk.Status.Narrated[l-1] != fmt.Sprintf("k%d", MaxNarrated+39) ||
		slices.Contains(tk.Status.Narrated, "k39") || !slices.Contains(tk.Status.Narrated, "k40") {
		t.Fatalf("%d keys, first %q", l, tk.Status.Narrated[0])
	}
}

type scripted struct {
	*forgetest.Fake
	recent           []forge.Comment
	listErr, postErr error
}

func (s *scripted) RecentComments(context.Context, int) ([]forge.Comment, error) {
	return s.recent, s.listErr
}

func (s *scripted) Comment(ctx context.Context, n int, body string) error {
	if s.postErr != nil {
		return s.postErr
	}
	return s.Fake.Comment(ctx, n, body)
}

// poster is the broker as the room sees it: it dedups on clientSeq alone, as bridgeapi does.
type poster struct {
	calls []string // "persist <narrated>" and "post <seq>", in order
	seen  map[int64]string
	err   error
}

func (p *poster) TaskState(_ context.Context, room, text string, clientSeq int64) error {
	p.calls = append(p.calls, fmt.Sprintf("post %s %d", room, clientSeq))
	if p.err != nil {
		return p.err
	}
	if p.seen == nil {
		p.seen = map[int64]string{}
	}
	if _, ok := p.seen[clientSeq]; !ok {
		p.seen[clientSeq] = text
	}
	return nil
}

func (p *poster) persist(t *v1alpha1.Task) func(context.Context) error {
	return func(context.Context) error {
		p.calls = append(p.calls, fmt.Sprintf("persist %d %s", t.Status.RoomSeq, t.Status.Narrated[len(t.Status.Narrated)-1]))
		return nil
	}
}

// Ruling SK: the clientSeq is status.roomSeq + 1, persisted before the post, so a restart replays
// the same seq and the broker keeps one message.
func TestRoomClientSeqIsRoomSeqPlusOnePersistedFirst(t *testing.T) {
	p := &poster{}
	tk := task()
	tk.Status.RoomSeq = 2
	tk.Status.Narrated = []string{"run-7f3cq2xz-started", "pr-opened"}
	if err := Room(t.Context(), p, tk, "snapshot", "the snapshot", p.persist(tk)); err != nil {
		t.Fatal(err)
	}
	want := []string{"persist 3 room/3/snapshot", "post 3buqdlot 3"}
	if !slices.Equal(p.calls, want) || tk.Status.RoomSeq != 3 {
		t.Fatalf("%q roomSeq %d", p.calls, tk.Status.RoomSeq)
	}
	if err := Room(t.Context(), p, tk, "round-1", "round 1", p.persist(tk)); err != nil {
		t.Fatal(err)
	}
	want = append(want, "persist 4 room/4/round-1", "post 3buqdlot 4")
	if !slices.Equal(p.calls, want) || len(p.seen) != 2 {
		t.Fatalf("%q %v", p.calls, p.seen)
	}
}

// A restart between the post and whatever the caller records next replays the post: the seq it
// sends is the one it sent, unchanged, so the broker answers 200 and stores nothing new.
func TestARestartReplaysTheSameClientSeq(t *testing.T) {
	p := &poster{err: errors.New("connection reset")}
	tk := task()
	if err := Room(t.Context(), p, tk, "snapshot", "the snapshot", p.persist(tk)); err == nil {
		t.Fatal("the post failed")
	}
	p.err = nil
	restarted := tk.DeepCopy() // what the persisted status holds
	for range 2 {
		if err := Room(t.Context(), p, restarted, "snapshot", "the snapshot", p.persist(restarted)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"persist 1 room/1/snapshot", "post 3buqdlot 1", "post 3buqdlot 1", "post 3buqdlot 1"}
	if !slices.Equal(p.calls, want) || restarted.Status.RoomSeq != 1 || len(p.seen) != 1 {
		t.Fatalf("%q roomSeq %d", p.calls, restarted.Status.RoomSeq)
	}
}

// The review's B2: narrated is capped at 512 and trimmed, so the seq cannot come from it. Past the
// cap, every room message still gets its own, increasing clientSeq.
func TestEveryClientSeqIsDistinctAndIncreasingPastTheCap(t *testing.T) {
	p := &poster{}
	n := Narrator{Forge: forgetest.NewFake(), Login: bot}
	tk := task()
	for i := range MaxNarrated + 88 {
		if err := n.Post(t.Context(), tk, 7, Event{Key: fmt.Sprintf("k%d", i), Body: "x"}); err != nil {
			t.Fatal(err)
		}
		if err := Room(t.Context(), p, tk, fmt.Sprintf("r%d", i), "m", p.persist(tk)); err != nil {
			t.Fatal(err)
		}
	}
	var last int64
	for _, c := range p.calls {
		var seq int64
		if _, err := fmt.Sscanf(c, "post 3buqdlot %d", &seq); err != nil {
			continue
		}
		if seq <= last {
			t.Fatalf("clientSeq %d after %d", seq, last)
		}
		last = seq
	}
	if last != MaxNarrated+88 || len(p.seen) != MaxNarrated+88 || len(tk.Status.Narrated) > MaxNarrated {
		t.Fatalf("last %d, %d stored, %d keys", last, len(p.seen), len(tk.Status.Narrated))
	}
}

func TestRoomSendsNothingItCouldNotRecord(t *testing.T) {
	p := &poster{}
	tk := task()
	err := Room(t.Context(), p, tk, "snapshot", "s", func(context.Context) error { return errors.New("conflict") })
	if err == nil || len(p.calls) != 0 || len(tk.Status.Narrated) != 0 || tk.Status.RoomSeq != 0 {
		t.Fatalf("%v %q %q %d", err, p.calls, tk.Status.Narrated, tk.Status.RoomSeq)
	}
	if err := Room(t.Context(), p, tk, "snapshot", "s", nil); err == nil || len(p.calls) != 0 {
		t.Fatal("no persist, no seq")
	}
	tk.Status.RoomRef = ""
	if err := Room(t.Context(), p, tk, "snapshot", "s", p.persist(tk)); err == nil || len(p.calls) != 0 {
		t.Fatal("a task without a room has nowhere to post")
	}
}

// The broker's refusals stay typed: no_room is a retry, and not_permitted is expected until FR-1
// enables the factory's systemPrincipals entry.
func TestRoomRefusalsStayTyped(t *testing.T) {
	for _, c := range []struct {
		status int
		reason string
		is     error
	}{
		{http.StatusNotFound, wire.ReasonNoRoom, rooms.ErrNoRoom},
		{http.StatusForbidden, wire.ReasonNotPermitted, rooms.ErrNotPermitted},
	} {
		p := &poster{err: &rooms.APIError{Status: c.status, Reason: c.reason}}
		tk := task()
		err := Room(t.Context(), p, tk, "snapshot", "s", p.persist(tk))
		if !errors.Is(err, c.is) {
			t.Errorf("%s: %v", c.reason, err)
		}
		if tk.Status.RoomSeq != 1 || !slices.Contains(tk.Status.Narrated, "room/1/snapshot") {
			t.Errorf("%s: the seq stays recorded, so the retry reuses it", c.reason)
		}
	}
}

// A review run without a verdict the factory acts on is narrated once per run, with why.
func TestNoVerdict(t *testing.T) {
	e := NoVerdict(task(), "rrrrrrrr", "verdict_stale")
	if e.Key != "noverdict-rrrrrrrr" || !strings.Contains(e.Body, "`rrrrrrrr`") || !strings.Contains(e.Body, "not for the pull request's current head") {
		t.Fatalf("%+v", e)
	}
}

// F4: the last verdict goes on the pull request inert, within the outbox's bound, and only for a
// public task (C7). The key is per escalation, so a retried task that escalates again says so.
func TestRoundsExhaustedQuotesTheVerdictInert(t *testing.T) {
	tk := task()
	tk.Spec.DataClass = "public"
	tk.Status.ReviewRounds = 2
	tk.Status.Runs = make([]v1alpha1.RunRecord, 5)
	v := rooms.Verdict{Verdict: "changes", Commit: "4be1c9d", RunID: "rrrrrrrr",
		Text: "Still wrong.\n@Smana ![x](https://evil.example/p.png) <!-- agent-room:3buqdlot:9 --> <!-- agent-factory scope=3buqdlot event=x -->"}
	e := RoundsExhausted(tk, v)
	if e.Key != "rounds-2-5" || !strings.Contains(e.Body, "used its 2 review rounds") || !strings.Contains(e.Body, "`rrrrrrrr`") ||
		!strings.Contains(e.Body, "`changes` on `4be1c9d`") || !strings.Contains(e.Body, "```\nStill wrong.\n") {
		t.Fatalf("%+v", e)
	}
	for _, bad := range []string{"@Smana", "](https://", "<!--"} {
		if strings.Contains(e.Body, bad) {
			t.Fatalf("%q survived:\n%s", bad, e.Body)
		}
	}
	tk.Status.Runs = make([]v1alpha1.RunRecord, 8)
	if again := RoundsExhausted(tk, v); again.Key == e.Key {
		t.Fatal("a second escalation is narrated too")
	}
	for name, text := range map[string]string{
		"long":      strings.Repeat("word ", 2000),
		"backticks": strings.Repeat("`", 5000),
		"mixed":     strings.Repeat("a`", 3000),
	} {
		v.Text = text
		e := RoundsExhausted(tk, v)
		if len(e.Body) > 4096 || !strings.Contains(e.Body, "⟦clipped by the factory") {
			t.Fatalf("%s: %d bytes", name, len(e.Body))
		}
	}
	v.Text = strings.Repeat("x", 3800)
	if e := RoundsExhausted(tk, v); len(e.Body) > 4096 || strings.Contains(e.Body, "⟦clipped") {
		t.Fatalf("a summary that fits is whole: %d", len(e.Body))
	}
	tk.Spec.DataClass = "internal"
	v.Text = "secret"
	if e := RoundsExhausted(tk, v); strings.Contains(e.Body, "secret") || !strings.Contains(e.Body, "stays in the room") {
		t.Fatalf("an internal task's summary stays in the room: %s", e.Body)
	}
}

// M5: a retry never gives review rounds back, and after they ran out it says what it buys.
func TestRetryingAfterTheRoundsSaysWhatItBuys(t *testing.T) {
	for _, after := range []string{"review_rounds_exhausted", "no_verdict"} {
		if e := Retrying(task(), "Smana", after); !strings.Contains(e.Body, "review rounds stay spent") || !strings.Contains(e.Body, "escalates it again") {
			t.Fatalf("%s: %s", after, e.Body)
		}
	}
	if e := Retrying(task(), "Smana", "agent_error"); strings.Contains(e.Body, "rounds") {
		t.Fatalf("%s", e.Body)
	}
}

// Disruption design §4: the issue says the factory resumes on its own, n of the cap.
func TestResuming(t *testing.T) {
	e := Resuming(task(), "7f3cq2xz", "implementer", 1, 2)
	if e.Key != "resume-1" || e.Body != "Agent factory task `3buqdlot`: run `7f3cq2xz` stopped because the sandbox was lost "+
		"(spot reclaim or eviction); resuming automatically (1/2)." {
		t.Fatalf("%+v", e)
	}
	if r := Resuming(task(), "7f3cq2xz", "reviewer", 2, 2); r.Key == e.Key || !strings.HasSuffix(r.Body, "The new review run uses no review round.") {
		t.Fatalf("%+v", r)
	}
}

// The room link has one slash before /r/ whatever roomsURL ends with, as ProposalReady's does.
func TestNeedsAuthorTrimsTheRoomsURL(t *testing.T) {
	tk := task()
	tk.Spec.DataClass = "internal"
	tk.Status.RoomRef = "3kq7x2ma"
	if b := NeedsAuthor(tk, "Which?", "A.", nil, "https://rooms.example/").Body; !strings.Contains(b, "(https://rooms.example/r/3kq7x2ma)") {
		t.Fatalf("%s", b)
	}
}

// A sponsored principal that pays is "the principal", never "the person"; an unknown principal and
// an outdated Rooms CRD say what to do.
func TestEndedNamesASponsoredPayerAsAPrincipal(t *testing.T) {
	tk := task()
	tk.Spec.Source = v1alpha1.Source{Kind: "api", RequestedBy: "system:slack-app", Sponsor: "github:smana"}
	b := Ended(tk, v1alpha1.PhaseEscalated, "budget-person").Body
	if !strings.Contains(b, "the principal its next run is charged to") || strings.Contains(b, "the person") ||
		!strings.Contains(b, "`system:slack-app`; it resets at 00:00 UTC") {
		t.Fatal(b)
	}
	person := task()
	person.Spec.Source = v1alpha1.Source{Kind: "issue", RequestedBy: "github:Smana"}
	if b := Ended(person, v1alpha1.PhaseEscalated, "budget-person").Body; !strings.Contains(b, "the person its next run") {
		t.Fatal(b)
	}
	for _, r := range []string{"principal_unknown", "rooms_crd_outdated"} {
		if got := Reason(r); got == r || got == "" {
			t.Errorf("%s has no narration", r)
		}
	}
}

// #182: the hand-off says how long the task waited, as the caller measured it, and what is missing.
func TestChecksNeverReportedSaysWhatAndHowLong(t *testing.T) {
	tk := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"}, Status: v1alpha1.TaskStatus{PullRequest: &v1alpha1.PullRequestRef{HeadSHA: "abc"}}}
	e := ChecksNeverReported(tk, 90*time.Minute, []string{"External links", "Unit"})
	if e.Key != "ci-wait-expired-abc" || !strings.Contains(e.Body, "waited 1h30m0s for checks that had not reported as completed: External links, Unit.") {
		t.Fatalf("%+v", e)
	}
}

// A regression is demoted only when the breaker counts follow-ups and regressions
// (breaker.maxFollowUps > 0); at 0 it is recorded and counts toward nothing, and the text says so.
func TestAMonitorRegressionSaysWhatItCosts(t *testing.T) {
	tk := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{PredictedClass: "dead-links"}}
	on := MonitorVerdict(tk, 12, "regression", []string{"main CI: regression"}, false, true).Body
	off := MonitorVerdict(tk, 12, "regression", []string{"main CI: regression"}, false, false).Body
	if !strings.Contains(on, "demoted at its next decision") || strings.Contains(off, "demoted") || !strings.Contains(off, "not counted") {
		t.Fatalf("on %q off %q", on, off)
	}
	if got := Reason("class_paused"); !strings.Contains(got, "follow-up") || !strings.Contains(got, "revert") {
		t.Fatalf("class_paused names every cause: %q", got)
	}
}

// RA9: the proposal comment names factory/publish; the answers to it say what was filed or why not.
func TestPublishNarration(t *testing.T) {
	tk := task()
	tk.Status.RoomRef = "3kq7x2ma"
	if b := ProposalReady(tk, "https://rooms.example", false).Body; !strings.Contains(b, "label this issue `factory/publish`") || !strings.Contains(b, "`factory/ready`") {
		t.Fatalf("%s", b)
	}
	if b := ProposalReady(tk, "https://rooms.example", true).Body; !strings.Contains(b, "the fix starts at once") || !strings.Contains(b, "charged to you") {
		t.Fatalf("%s", b)
	}
	if e := PublishedStarted(501, "smana", "x7k2m4pq", "alert-rules"); e.Key != "published-501" || !strings.Contains(e.Body, "#501") ||
		!strings.Contains(e.Body, "`x7k2m4pq`") || !strings.Contains(e.Body, "`alert-rules`") {
		t.Fatalf("%+v", e)
	}
	if e := Published(7, 42, "smana"); e.Key != "published-42" || e.Body != "Published as #42 by @smana. Read it, then label it `factory/ready` to start the fix." {
		t.Fatalf("%+v", e)
	}
	at := time.Unix(1_700_000_000, 0)
	if e := PublishRefused(7, "unauthorised_labeller", at); e.Key != "publish-refused-unauthorised_labeller-1700000000" ||
		e.Body != "The agent factory did not publish #7's proposal: only a maintainer's factory/publish files a proposal." {
		t.Fatalf("%+v", e)
	}
	for _, r := range []string{"no_proposal", "task_not_ended", "invalid_draft", "redacted", "live_reference"} {
		if got := Reason(r); got == r || got == "" {
			t.Errorf("%s has no narration", r)
		}
		if b := PublishRefused(7, r, at).Body; !strings.Contains(b, Reason(r)) {
			t.Errorf("%s: %s", r, b)
		}
	}
}

// HR2: the scoper's tool-call limit reads as prose wherever a run's end is told.
func TestTheScopersToolCallLimitReadsAsProse(t *testing.T) {
	if got := Reason("budget_calls"); !strings.Contains(got, "tool-call limit") {
		t.Fatalf("Reason(budget_calls) = %q", got)
	}
}

// Human path AM3: with approveToMerge, the hand-off names the one action; the by-hand reasons keep
// the bypass instructions; an approved merge says who approved.
func TestTheHandOffNamesTheOneAction(t *testing.T) {
	tk := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"}, Spec: v1alpha1.TaskSpec{Repository: "Smana/x"},
		Status: v1alpha1.TaskStatus{PullRequest: &v1alpha1.PullRequestRef{Number: 12}}}
	if got := WaitingForApproval(tk, "class_unarmed").Body; !strings.Contains(got, "To merge: approve this pull request") || strings.Contains(got, "--admin") {
		t.Fatalf("%q", got)
	}
	// The reason says approve too: under approveToMerge no maintainer merges by hand.
	if got := WaitingForApproval(tk, "class_unarmed").Body; strings.Contains(got, "reviews and merges it") ||
		!strings.Contains(got, "it merges once a maintainer approves the current commit") {
		t.Fatalf("%q", got)
	}
	if got := WaitingForHuman(tk, "gate_path").Body; !strings.Contains(got, "--admin") {
		t.Fatalf("%q", got)
	}
	if got := ArmedByApproval(tk, "Smana").Body; !strings.Contains(got, "@Smana approved") {
		t.Fatalf("%q", got)
	}
}

// The hand-back comment never shows a raw reason: every return and stale reason has prose.
func TestTheNewReturnReasonsReadAsProse(t *testing.T) {
	for _, r := range []string{"approval_stale", "reentered_approved", "reentered_day_turned", "reentered_unpaused"} {
		if got := Reason(r); got == r || got == "" {
			t.Errorf("%s reads raw: %q", r, got)
		}
	}
}

func TestTheWouldArmMarkerSaysWhatBlocked(t *testing.T) {
	tk := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"}}
	if k := WouldArm(tk, "docs-links", "abc", true, false).Key; k != "would-arm-0 head=abc paused=true capped=false" {
		t.Fatalf("%q", k)
	}
}

// Fast-follow 1: the comment a maintainer reads on GitHub for a commit the factory cannot vouch for
// carries the room's own sentence, verbatim: only a repository admin can merge it, and why. Two
// surfaces telling one hand-off differently is the defect, so the sentence is the summary's.
func TestTheHumanCommitHandOffCarriesTheRoomsSentence(t *testing.T) {
	tk := task()
	tk.Spec.Repository = "Smana/cloud-native-ref"
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 2267, URL: "https://github.com/Smana/cloud-native-ref/pull/2267"}
	body := WaitingForHuman(tk, "human_commit_in_pr").Body
	_, next := summ.Waiting(&envelope.TaskFacts{Phase: v1alpha1.PhaseAwaitingHuman, Reason: "human_commit_in_pr",
		PR: &envelope.PRFact{Number: 2267, URL: "https://github.com/Smana/cloud-native-ref/pull/2267"}})
	if next == "" || !strings.Contains(body, next) {
		t.Fatalf("the comment must carry the room's sentence %q:\n%s", next, body)
	}
	for _, want := range []string{"a person pushed a commit the factory cannot vouch for", "--admin"} {
		if !strings.Contains(body, want) {
			t.Fatalf("no %q in\n%s", want, body)
		}
	}
	// It is the by-hand hand-off: the approveToMerge wording is not this one's.
	if strings.Contains(body, "To merge: approve this pull request") {
		t.Fatalf("an approve-to-merge comment for a by-hand reason:\n%s", body)
	}
}

func TestPrivateRepositoryReason(t *testing.T) {
	want := "The agent factory did not start a task for #7: This factory runs as a demo, which serves public repositories only: in the demo, an agent can read the model key and the GitHub token."
	if got := Refused(7, "private_repository", time.Time{}).Body; got != want {
		t.Fatalf("%q", got)
	}
}

// With approveToMerge off, a maintainer's approval is shown once per review beside the hand-off's
// reason, which keeps its own words; the bypass goes with a current approval only.
func TestAnApprovalShownSaysWhoAndWhatRemains(t *testing.T) {
	tk := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"}, Spec: v1alpha1.TaskSpec{Repository: "Smana/x"},
		Status: v1alpha1.TaskStatus{Reason: "not_mergeable", PullRequest: &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/Smana/x/pull/12"}}}
	e := ApprovalShown(tk, 77, "Smana", "abc", "")
	if !strings.Contains(e.Body, "approved by @Smana at commit `abc`") || !strings.Contains(e.Body, "GitHub does not allow merging it as it stands") ||
		!strings.Contains(e.Body, "--admin") {
		t.Fatalf("%q", e.Body)
	}
	if s := ApprovalShown(tk, 77, "Smana", "abc", envelope.ApprovalOlderCommit); s.Key == e.Key || !strings.Contains(s.Body, "an older commit: new commits since") ||
		strings.Contains(s.Body, "--admin") {
		t.Fatalf("stale: %q %q", s.Key, s.Body)
	}
	if s := ApprovalShown(tk, 77, "Smana", "abc", envelope.ApprovalBeforeRunsEnded); !strings.Contains(s.Body, "before the factory's last push ended: approve it again") ||
		strings.Contains(s.Body, "new commits since") || strings.Contains(s.Body, "--admin") {
		t.Fatalf("early: %q", s.Body)
	}
	if ApprovalShown(tk, 78, "Smana", "abc", "").Key == e.Key {
		t.Fatal("a new review is a new narration")
	}
}

// TE: the workflow a task runs, said once, with the floor marked and the scoper's why (WF1).
// SEC-H5: the scoper's why is untrusted LLM output — quoted inert in a fenced code block on public
// tasks only, never posted raw and never leaked on non-public tasks.
func TestProfileChosen(t *testing.T) {
	pub := task()
	pub.Spec.DataClass = "public"
	for name, c := range map[string]struct {
		tk                        *v1alpha1.Task
		name, source, reason, why string
		want, not                 []string
	}{
		"variant": {pub, "review-search", "scoper", "", "the change spans three packages",
			[]string{"`review-search`", "```\nthe change spans three packages\n```"}, []string{"(default)"}},
		"floor": {pub, "review-floor", "floor", "not_allowed", "",
			[]string{"`review-floor` (default)", "not_allowed"}, nil},
		"treatment": {pub, "careful", "treatment", "", "",
			[]string{"`careful` (the team evaluation's treatment arm)"}, []string{"(default)"}},
		"untrusted markup and mentions defused": {pub, "review-search", "scoper", "",
			"Reason.\n@Smana ![x](https://evil.example/p.png) <img src=x> <!-- agent-factory scope=3buqdlot event=x -->\n```\nescaped",
			[]string{"`review-search`", "````\nReason.\n", "＠Smana"},
			[]string{"@Smana", "](https://", "<img", "<!--"}},
		"internal task hides why": {func() *v1alpha1.Task {
			tk := task()
			tk.Spec.DataClass = "internal"
			return tk
		}(), "review-search", "scoper", "", "internal-only secret rationale",
			[]string{"`review-search`"}, []string{"internal-only secret rationale"}},
		"unset data class hides why": {task(), "review-search", "scoper", "", "unknown-class rationale",
			[]string{"`review-search`"}, []string{"unknown-class rationale"}},
	} {
		t.Run(name, func(t *testing.T) {
			e := ProfileChosen(c.tk, c.name, c.source, c.reason, c.why, 1)
			if e.Key != "profile-chosen-1" {
				t.Fatalf("key %q", e.Key)
			}
			if !strings.Contains(e.Body, "runs the workflow `") || strings.Contains(e.Body, "profile") {
				t.Errorf("the noun is workflow: %s", e.Body)
			}
			for _, w := range c.want {
				if !strings.Contains(e.Body, w) {
					t.Errorf("body lacks %q:\n%s", w, e.Body)
				}
			}
			for _, w := range c.not {
				if strings.Contains(e.Body, w) {
					t.Errorf("body has %q:\n%s", w, e.Body)
				}
			}
		})
	}
	t.Run("long why stays bounded", func(t *testing.T) {
		e := ProfileChosen(pub, "review-search", "scoper", "", strings.Repeat("word ", 2000), 1)
		if len(e.Body) > maxBody || !strings.Contains(e.Body, "⟦clipped by the factory") {
			t.Fatalf("len %d, want <= %d with clip mark", len(e.Body), maxBody)
		}
	})
}

func TestProfileEscalated(t *testing.T) {
	ev := ProfileEscalated("standard", "careful", "stalled", 13_600_000, 2)
	for _, want := range []string{"from `standard` to `careful`", "a run that committed nothing", "New ceiling: $13.60", "`factory/stop`"} {
		if !strings.Contains(ev.Body, want) {
			t.Errorf("missing %q in:\n%s", want, ev.Body)
		}
	}
	if ev.Key != "profile-escalated-2" {
		t.Fatalf("key %q", ev.Key)
	}
	withSpent := ProfileEscalated("standard", "careful", "stalled", 13_600_000, 2, 1_840_000)
	if !strings.Contains(withSpent.Body, "$1.84 spent so far") {
		t.Errorf("missing spent so far in:\n%s", withSpent.Body)
	}
}

func TestAnOfferSaysWhyItDidNotStep(t *testing.T) {
	for why, want := range map[string]string{
		"pinned":    "a maintainer pinned its workflow",
		"capped":    "its budget is capped below $13.60",
		"ask":       "this repository asks first",
		"not_shown": "the plan you confirmed did not show the stepped-up cost",
	} {
		ev := StepUpOffered(task(), "standard", "careful", "budget-run", 13_600_000, why)
		if ev.Key != "step-up-offered" || !strings.Contains(ev.Body, want) || !strings.Contains(ev.Body, "`/factory retry deeper`") {
			t.Errorf("%s: got key=%q body:\n%s", why, ev.Key, ev.Body)
		}
	}
}

// WF1: a refused pin names the workflow, whichever label pinned it.
func TestProfileRefusedNamesTheWorkflow(t *testing.T) {
	e := ProfileRefused("careful", "Only a maintainer of this repository can choose the workflow.")
	if e.Key != "profile-refused" || !strings.Contains(e.Body, "the pinned workflow `careful` was not applied") {
		t.Fatalf("%+v", e)
	}
}

func TestEndedNamesTheStepTried(t *testing.T) {
	tk := task()
	tk.Status.Profile = &v1alpha1.ProfileStatus{Name: "review-frontier", Source: "escalation", Reason: "out_of_budget"}
	if b := Ended(tk, v1alpha1.PhaseEscalated, "stalled").Body; !strings.Contains(b, "`review-frontier`") {
		t.Fatal(b)
	}
	tk.Status.Profile.Source = "scoper"
	if b := Ended(tk, v1alpha1.PhaseEscalated, "stalled").Body; strings.Contains(b, "review-frontier") {
		t.Fatal(b)
	}
}

// Task G6: every exported Event constructor in package narrate assigns one of the three
// notification classes (needs_you, changed, done). Walking the package's AST ensures a new
// constructor cannot land without a class.
func TestEveryNarrationEventHasAClass(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	discovered := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			if id, ok := fn.Type.Results.List[0].Type.(*ast.Ident); ok && id.Name == "Event" {
				discovered[fn.Name.Name] = true
			}
		}
	}
	if len(discovered) == 0 {
		t.Fatal("AST walk found no Event constructors")
	}

	tk := task()
	tk.Spec.Repository = "Smana/x"
	tk.Spec.PredictedClass = "docs-links"
	tk.Spec.IssueAuthor = "alice"
	tk.Spec.Split = &v1alpha1.SplitSpec{ChildTier: "standard", FamilyMicroUSD: 4_000_000}
	tk.Spec.Parent = &v1alpha1.Parent{Task: "parent01", Index: 1, Of: 2}
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/Smana/x/pull/12", HeadSHA: "abcdef1", MergeCommitSHA: "1234567"}
	tk.Status.Split = &v1alpha1.SplitStatus{Of: 2}
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	constructors := map[string]struct {
		ev   Event
		want Class
	}{
		"Started":                {Started(tk, runs.Spec{RunID: "r1", Role: "implementer", Branch: "agent/3buqdlot", MaxTokens: 1000, MaxMinutes: 10}, "https://rooms.example"), ClassChanged},
		"TaskCard":               {TaskCard(tk, "- **Workflow:** standard"), ClassChanged},
		"ProposalReady":          {ProposalReady(tk, "https://rooms.example", false), ClassNeedsYou},
		"ReviewPosted":           {ReviewPosted(tk, rooms.Verdict{Verdict: "approve", Commit: "abcdef1", Text: "looks good"}, "https://rooms.example"), ClassDone},
		"FindingsPosted":         {FindingsPosted(tk, "root cause found", "https://rooms.example"), ClassDone},
		"FixFiled":               {FixFiled(tk, "4buqdlot", "smana"), ClassChanged},
		"OutcomeConflict":        {OutcomeConflict(7, []string{"plan-first", "report"}, now), ClassChanged},
		"PublishedStarted":       {PublishedStarted(12, "smana", "3buqdlot", "docs-links"), ClassChanged},
		"Published":              {Published(7, 12, "smana"), ClassNeedsYou},
		"PublishRefused":         {PublishRefused(7, "no_proposal", now), ClassChanged},
		"PublishRejected":        {PublishRejected(7, "3buqdlot", "daily_task_cap", now), ClassChanged},
		"NeedsAuthor":            {NeedsAuthor(tk, "Which command?", "Use task check", []string{"smana"}, "https://rooms.example"), ClassNeedsYou},
		"SplitProposed":          {SplitProposed(tk, "too wide", []string{"part 1", "part 2"}, []string{"smana"}, "https://rooms.example"), ClassNeedsYou},
		"PlanProposed":           {PlanProposed(tk, nil, nil, "https://rooms.example"), ClassNeedsYou},
		"PlanParked":             {PlanParked(tk, 72, []string{"smana"}, "https://rooms.example"), ClassNeedsYou},
		"PlanRevisionLimit":      {PlanRevisionLimit(tk, "alice", []string{"smana"}, "https://rooms.example"), ClassNeedsYou},
		"Revising":               {Revising(tk, "smana"), ClassChanged},
		"Revised":                {Revised(tk, Revision{PR: 12, Repository: "Smana/x", From: "aaa", Head: "bbb", Files: []string{"a.go"}, FilesRead: true}, "https://rooms.example"), ClassNeedsYou},
		"Reminder":               {Reminder(tk, []string{"smana"}, now, false), ClassNeedsYou},
		"Retrying":               {Retrying(tk, "smana", ""), ClassChanged},
		"TaskCapHolds":           {TaskCapHolds(tk, 99), ClassNeedsYou},
		"CommandHeld":            {CommandHeld(tk, 99, "/factory retry", "is escalated"), ClassChanged},
		"PlanCommandNoted":       {PlanCommandNoted(tk, 99, "/factory plan approve"), ClassChanged},
		"CommandApplied":         {CommandApplied(tk, 99, "/factory retry"), ClassChanged},
		"CommandExpired":         {CommandExpired(tk, 99, "/factory retry", "smana", "the task ended"), ClassNeedsYou},
		"CommandNotApplicable":   {CommandNotApplicable(tk, 99, "/factory fix", "not a report task"), ClassChanged},
		"NotActedOn":             {NotActedOn(tk, 99, "`/factory retry`"), ClassChanged},
		"Resuming":               {Resuming(tk, "r1", "implementer", 1, 2), ClassChanged},
		"SandboxRetry":           {SandboxRetry(tk, "r1"), ClassChanged},
		"ModelHeld":              {ModelHeld(tk, "r1", "model_unavailable_quota", 5*time.Minute, 1, 6), ClassChanged},
		"ContractReady":          {ContractReady(tk, &v1alpha1.ContractStatus{IDs: []string{"T1"}, Files: []string{"a_test.go"}}), ClassChanged},
		"NoContract":             {NoContract(tk, "skipped"), ClassChanged},
		"PROpened":               {PROpened(tk, 12, "https://github.com/Smana/x/pull/12", "r1"), ClassChanged},
		"Armed":                  {Armed(tk, "docs-links"), ClassChanged},
		"WouldArm":               {WouldArm(tk, "docs-links", "abcdef1", false, false), ClassNeedsYou},
		"WouldArmAdopted":        {WouldArmAdopted(tk, config.Class{}, "renovate", "abcdef1", false, false), ClassNeedsYou},
		"WaitingForHuman":        {WaitingForHuman(tk, "file_protected"), ClassNeedsYou},
		"WaitingForApproval":     {WaitingForApproval(tk, "class_unarmed"), ClassNeedsYou},
		"ArmedByApproval":        {ArmedByApproval(tk, "smana"), ClassChanged},
		"ApprovalShown":          {ApprovalShown(tk, 77, "smana", "abcdef1", ""), ClassNeedsYou},
		"ChecksNeverReported":    {ChecksNeverReported(tk, 30*time.Minute, []string{"unit"}), ClassNeedsYou},
		"BackToGate":             {BackToGate(tk, "reentered_policy", 1), ClassChanged},
		"Held":                   {Held(tk, "waiting_room_busy", "https://rooms.example"), ClassChanged},
		"CIFixing":               {CIFixing(tk, 12, "abcdef1", []string{"unit: failure"}, 1, 2), ClassChanged},
		"CIExhausted":            {CIExhausted(tk, []string{"unit"}), ClassNeedsYou},
		"RevertOpened":           {RevertOpened(tk, 13, "main_red", false), ClassChanged},
		"ClassDrill":             {ClassDrill(tk), ClassChanged},
		"ClassDemoted":           {ClassDemoted(tk, 10, 1), ClassChanged},
		"RevertStalled":          {RevertStalled(tk, 13), ClassNeedsYou},
		"MonitorVerdict":         {MonitorVerdict(tk, 12, "healthy", []string{"main CI: green"}, false, false), ClassChanged},
		"FollowUpFound":          {FollowUpFound(tk, 12, 14, false), ClassChanged},
		"Ended":                  {Ended(tk, v1alpha1.PhaseDone, "merged"), ClassDone},
		"MergedUnverified":       {MergedUnverified(tk, now.Add(time.Hour)), ClassDone},
		"StopIgnored":            {StopIgnored(7, "unauthorised_labeller", now), ClassChanged},
		"Refused":                {Refused(7, "edited_after_label", now), ClassChanged},
		"PersonOverBudget":       {PersonOverBudget(7, "github:smana", 5_000_000, now), ClassNeedsYou},
		"RevertNotFactoryMerged": {RevertNotFactoryMerged("Smana/maku", 7, now), ClassChanged},
		"ApproverOverBudget":     {ApproverOverBudget(7, "github:smana", 5_000_000, now), ClassNeedsYou},
		"SplitPart":              {SplitPart(tk, 1, 15), ClassChanged},
		"SplitEnded":             {SplitEnded(tk, v1alpha1.PhaseDone, "children_merged", []Part{{Index: 1, Issue: 15, Outcome: "done"}}), ClassDone},
		"PartReverted":           {PartReverted(tk, 16, true), ClassNeedsYou},
		"NoVerdict":              {NoVerdict(tk, "r2", "verdict_missing"), ClassChanged},
		"RoundsExhausted":        {RoundsExhausted(tk, rooms.Verdict{Verdict: "changes", Commit: "abcdef1", RunID: "r2", Text: "fix"}), ClassNeedsYou},
		"ProfileChosen":          {ProfileChosen(tk, "standard", "scoper", "", "", 1), ClassChanged},
		"ProfileRefused":         {ProfileRefused("careful", "Only a maintainer can pin."), ClassChanged},
		"HarnessRefused":         {HarnessRefused("opencode", "not_maintainer"), ClassChanged},
		"TriageNote":             {TriageNote(0, "note"), ClassChanged},
		"ProfileEscalated":       {ProfileEscalated("standard", "careful", "stalled", 13_600_000, 2), ClassChanged},
		"StepUpOffered":          {StepUpOffered(tk, "standard", "careful", "budget-run", 13_600_000, "pinned"), ClassNeedsYou},
		"CoordinationIncident":   {CoordinationIncident("parent01", "child-a", 19), ClassChanged},
		"RoleFindings":           {RoleFindings(tk, "security-reviewer", "r2", "comment", "no findings"), ClassChanged},
	}

	for name := range discovered {
		if _, ok := constructors[name]; !ok {
			t.Errorf("exported Event constructor %s is missing from TestEveryNarrationEventHasAClass", name)
		}
	}
	for name, tc := range constructors {
		if !discovered[name] {
			t.Errorf("constructor %s in test table was not found in package AST", name)
		}
		if !tc.ev.Class.Valid() {
			t.Errorf("%s returned invalid Class %q", name, tc.ev.Class)
		}
		if tc.ev.Class != tc.want {
			t.Errorf("%s.Class = %q, want %q", name, tc.ev.Class, tc.want)
		}
		if tc.ev.Key == "" || tc.ev.Body == "" {
			t.Errorf("%s returned empty key or body: %+v", name, tc.ev)
		}
	}

	// Branch checks: escalated endings and human-driver holds are needs_you; open-parent part
	// reverts are changed; mention rule holds on every class.
	if got := Ended(tk, v1alpha1.PhaseEscalated, "stalled", "smana").Class; got != ClassNeedsYou {
		t.Errorf("Ended(Escalated).Class = %q, want %q", got, ClassNeedsYou)
	}
	if got := SplitEnded(tk, v1alpha1.PhaseEscalated, "budget-family", nil, "smana").Class; got != ClassNeedsYou {
		t.Errorf("SplitEnded(Escalated).Class = %q, want %q", got, ClassNeedsYou)
	}
	if got := Held(tk, "waiting_human_driver", "https://rooms.example").Class; got != ClassNeedsYou {
		t.Errorf("Held(waiting_human_driver).Class = %q, want %q", got, ClassNeedsYou)
	}
	if got := PartReverted(tk, 16, false).Class; got != ClassChanged {
		t.Errorf("PartReverted(open parent).Class = %q, want %q", got, ClassChanged)
	}
	if !ClassNeedsYou.Mentions() || ClassChanged.Mentions() || !ClassDone.Mentions() {
		t.Errorf("mention rules: needs_you=%v changed=%v done=%v", ClassNeedsYou.Mentions(), ClassChanged.Mentions(), ClassDone.Mentions())
	}
}

func TestTheParentCommentListsEveryPart(t *testing.T) {
	tk := task()
	tk.Status.Phase = v1alpha1.PhaseSplit
	sf := StatusFacts{
		Workflow: "standard",
		Strip:    "scoper ✓ · 3 parts: 1 merged · 1 working · 1 waiting for #12",
		Split: &envelope.SplitFact{
			Of: 3,
			Parts: []envelope.PartFact{
				{Index: 1, Issue: 11, Room: "aaaaaaaa", Outcome: "landed", State: "merged", CostMicroUSD: 420_000, Strip: "implementer ✓ · reviewer ✓"},
				{Index: 2, Issue: 12, Room: "bbbbbbbb", State: "working", CostMicroUSD: 180_000, Strip: "implementer ● · reviewer ○"},
				{Index: 3, Issue: 13, Room: "cccccccc", State: "waiting", DependsOn: []int{12}},
			},
		},
	}
	body := Status(tk, "https://rooms.example", sf)
	for _, want := range []string{
		"Path: scoper ✓ · 3 parts: 1 merged · 1 working · 1 waiting for #12",
		"| Part | Issue | Path | State | Cost |",
		"| 1 | #11 | implementer ✓ · reviewer ✓ | merged | $0.42 |",
		"| 2 | #12 | implementer ● · reviewer ○ | working | $0.18 |",
		"| 3 | #13 | — | waiting for #12 | — |",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("parent status comment missing %q:\n%s", want, body)
		}
	}
	// SyncStatus keeps one pinned comment and edits it in place as parts progress.
	f := &statusForge{Fake: forgetest.NewFake()}
	n := Narrator{Forge: f, Login: bot}
	if err := n.SyncStatus(t.Context(), tk, "https://rooms.example", sf); err != nil {
		t.Fatal(err)
	}
	sf.Split.Parts[1].Outcome, sf.Split.Parts[1].State, sf.Split.Parts[1].Strip = "landed", "merged", "implementer ✓ · reviewer ✓"
	sf.Split.Parts[2].State, sf.Split.Parts[2].DependsOn, sf.Split.Parts[2].Strip = "working", nil, "implementer ● · reviewer ○"
	if err := n.SyncStatus(t.Context(), tk, "https://rooms.example", sf); err != nil {
		t.Fatal(err)
	}
	if len(f.Comments(tk.Spec.Issue)) != 1 || f.count("create") != 1 || f.count("edit") != 1 {
		t.Fatalf("expected one updated comment: %d comments, calls %q", len(f.Comments(tk.Spec.Issue)), f.calls)
	}
	updated := f.Comments(tk.Spec.Issue)[0]
	if !strings.Contains(updated, "| 2 | #12 | implementer ✓ · reviewer ✓ | merged | $0.18 |") ||
		!strings.Contains(updated, "| 3 | #13 | implementer ● · reviewer ○ | working | — |") {
		t.Fatalf("updated comment did not reflect new part states:\n%s", updated)
	}
	// Unopened parts still appear as queued rows.
	unopened := Status(tk, "https://rooms.example", StatusFacts{
		Split: &envelope.SplitFact{Of: 2, Parts: []envelope.PartFact{{Index: 1, Issue: 11, Room: "aaaaaaaa", State: "working"}}},
	})
	if !strings.Contains(unopened, "| 2 | — | — | queued | — |") {
		t.Fatalf("unopened part missing:\n%s", unopened)
	}
}
