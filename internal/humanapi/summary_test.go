// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/summary"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func logEvent(seq int64, typ envelope.Type, actor envelope.Actor, payload any) envelope.Event {
	return envelope.Event{V: 1, Seq: seq, RoomID: roomID, Type: typ, Actor: actor, TS: time.Now(), Payload: envelope.Must(payload)}
}

func progress(seq int64, text string) envelope.Event {
	return logEvent(seq, envelope.Message, envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:x", Role: "implementer"},
		envelope.MessagePayload{Kind: envelope.KindProgress, Text: text, Delivery: envelope.DeliveryNone})
}

// factsLog is a room with task facts, two progress notes and a pending approval.
func factsLog(l *memLog) {
	l.evs = []envelope.Event{
		{V: 1, Seq: 1, RoomID: roomID, Type: envelope.StateChanged, TS: time.Now(),
			Actor:   envelope.Actor{Kind: envelope.ActorSystem, ID: "system:factory"},
			Payload: envelope.TaskStatePayload(envelope.TaskFacts{Phase: "Implementing"})},
		progress(2, "planning"),
		logEvent(3, envelope.ApprovalRequested, envelope.Actor{Kind: envelope.ActorSystem, ID: "system:policy"},
			envelope.ApprovalRequestedPayload{ApprovalID: "01M4A", CallID: "c1", Class: "forge.pr",
				Action: envelope.Must(map[string]string{"command": "push"}), ExpiresAt: time.Now().Add(time.Hour)}),
		progress(4, "checks pass"),
	}
}

type summaryBody struct {
	APIVersion string `json:"apiVersion"`
	Room       string `json:"room"`
	URL        string `json:"url"`
	Status     struct {
		Phase string `json:"phase"`
	} `json:"status"`
	NeedsYou []struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	} `json:"needsYou"`
	Notes struct {
		Untrusted bool `json:"untrusted"`
		Items     []struct {
			Text string `json:"text"`
		} `json:"items"`
	} `json:"notes"`
}

func summaryOf(t *testing.T, e env, room, query string, h http.Header) summaryBody {
	t.Helper()
	code, body := get(t, e, "/api/rooms/"+room+"/summary"+query, h)
	var s summaryBody
	if code != http.StatusOK || json.Unmarshal([]byte(body), &s) != nil {
		t.Fatalf("summary: %d %s", code, body)
	}
	return s
}

func TestSummaryEndpoint(t *testing.T) {
	e := setup(t, abc(t), func(s *Server, _ *fanout.Hub, _ *hubView) { s.PublicURL = "https://rooms.example" })
	factsLog(e.log)
	s := summaryOf(t, e, roomID, "", admin("boss"))
	if s.APIVersion != "summary/v1" || s.Room != roomID || s.URL != "https://rooms.example/r/"+roomID ||
		s.Status.Phase != "Implementing" || !s.Notes.Untrusted || len(s.Notes.Items) != 2 || len(s.NeedsYou) != 1 {
		t.Fatalf("%+v", s)
	}
	// A member who is not an approver is not asked to decide.
	if m := summaryOf(t, e, roomID, "", header("dev1")); len(m.NeedsYou) != 0 {
		t.Fatalf("a watcher needs %+v", m.NeedsYou)
	}
	// ?after= keeps the newer notes only.
	s = summaryOf(t, e, roomID, "?after=2", admin("boss"))
	if len(s.Notes.Items) != 1 || s.Notes.Items[0].Text != "checks pass" {
		t.Fatalf("after=2: %+v", s.Notes)
	}
	if code, _ := get(t, e, "/api/rooms/"+roomID+"/summary?after=x", admin("boss")); code != http.StatusBadRequest {
		t.Fatalf("a bad cursor: %d", code)
	}
	if code, _ := get(t, e, "/api/rooms/"+roomID+"/summary", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
}

// CD26 at the summary's door: room_summary lists the merge need for a maintainer who is not the room's
// owner, because the server resolves the standing and hands it to View (B1).
func TestRoomSummaryKeepsTheMergeNeedForAMaintainer(t *testing.T) {
	w := world(nil) // dev1 may write Smana/a and owns no room, as a maintainer who is not the owner
	w.set(func() { w.writes = map[string][]string{"Smana/a": {"dev1"}} })
	e := setup(t, abc(t), w.option(), func(s *Server, _ *fanout.Hub, v *hubView) {
		s.PublicURL = "https://rooms.test"
		v.evs = append(v.evs, envelope.Event{V: 1, Seq: 11, RoomID: roomID, Type: envelope.StateChanged, TS: time.Now(),
			Payload: envelope.TaskStatePayload(envelope.TaskFacts{Phase: "AwaitingHuman",
				PR: &envelope.PRFact{Number: 2239, URL: "https://github.com/Smana/a/pull/2239"}})})
	})
	s := summaryOf(t, e, roomID, "", header("dev1"))
	if len(s.NeedsYou) != 1 || s.NeedsYou[0].Kind != summary.NeedMerge {
		t.Fatalf("%+v", s.NeedsYou)
	}
}

// The summary is a room read: D7 applies as it does to the WebSocket, and an unreadable room
// answers byte-for-byte like a missing one.
func TestSummaryFollowsTheGate(t *testing.T) {
	w := world(map[string][]string{"Smana/a": {"dev1"}}, "nolink")
	e := setup(t, abc(t), w.option())
	factsLog(e.log)
	_, missing := get(t, e, "/api/rooms/6kq7x2md/summary", header("dev1"))
	if missing != "no such room\n" {
		t.Fatalf("a missing room answers %q", missing)
	}
	for _, c := range []struct {
		name, room string
		h          http.Header
	}{
		{"a repository the caller cannot read", roomB, header("dev1")},
		{"a room with no repository", roomC, header("dev1")},
		{"no GitHub link", roomID, header("nolink")},
		{"a reader of nothing", roomID, header("dev2")},
	} {
		if code, body := get(t, e, "/api/rooms/"+c.room+"/summary", c.h); code != http.StatusNotFound || body != missing {
			t.Errorf("%s: %d %q, want a missing room's %q", c.name, code, body, missing)
		}
	}
	summaryOf(t, e, roomID, "", header("dev1"))
	w.set(func() { w.down, w.now = true, w.now.Add(10*time.Minute) })
	code, body := get(t, e, "/api/rooms/"+roomID+"/summary", header("dev1"))
	if code != http.StatusServiceUnavailable || body != "access_unverified\n" {
		t.Fatalf("past the cache: %d %q", code, body)
	}
}

type failingLog struct{ *memLog }

func (failingLog) Range(context.Context, string, int64, int) ([]envelope.Event, error) {
	return nil, errors.New("db down")
}

func TestSummaryLogFailureIs503(t *testing.T) {
	e := setup(t, abc(t))
	e.srv.Log = failingLog{e.log}
	code, body := get(t, e, "/api/rooms/"+roomID+"/summary", admin("boss"))
	if code != http.StatusServiceUnavailable || body != "room log unreadable\n" {
		t.Fatalf("%d %q", code, body)
	}
}

// The whole log is read, not its first page.
func TestSummaryReadsEveryPage(t *testing.T) {
	e := setup(t, abc(t))
	e.log.evs = nil
	e.log.add(1200)
	e.log.evs = append(e.log.evs, progress(1201, "last"))
	s := summaryOf(t, e, roomID, "", admin("boss"))
	if len(s.Notes.Items) != 1 || s.Notes.Items[0].Text != "last" {
		t.Fatalf("%+v", s.Notes)
	}
}

// filteredRooms serves A (Smana/a, issue by Dev1), B (Smana/b, dev1 reviews, one approval
// pending) and C (Smana/a, two approvals pending). The approvals are the ledger's rows, which is what
// needs/v1 lists: the rooms' pending count is only the projection (CD21).
func filteredRooms(t *testing.T) option {
	return func(s *Server, _ *fanout.Hub, v *hubView) {
		a := repoRoom(roomID, "Smana/a")
		a.Status.Task = &v1alpha1.TaskStatus{IssueAuthor: "Dev1"}
		b := repoRoom(roomB, "Smana/b")
		b.Status.Task = &v1alpha1.TaskStatus{PRReviewers: []string{"x", "dev1"}}
		b.Status.PendingApprovals = 1
		c := repoRoom(roomC, "Smana/a")
		c.Status.PendingApprovals = 2
		s.Rooms = roomClient(t, a, b, c)
		live := time.Now().Add(time.Hour)
		v.approvals = []store.Approval{
			{ID: "01J9B", RoomID: roomB, Class: "shell", ExpiresAt: live},
			{ID: "01J9C", RoomID: roomC, Class: "shell", ExpiresAt: live},
			{ID: "01J9D", RoomID: roomC, Class: "shell", ExpiresAt: live},
		}
	}
}

func TestRoomFilters(t *testing.T) {
	e := setup(t, filteredRooms(t))
	rowsOf := func(q string, h http.Header) []roomRow {
		code, body := get(t, e, "/api/rooms"+q, h)
		var rows []roomRow
		if code != http.StatusOK || json.Unmarshal([]byte(body), &rows) != nil {
			t.Fatalf("%s: %d %s", q, code, body)
		}
		return rows
	}
	want := func(q string, h http.Header, w ...string) {
		t.Helper()
		got := []string{}
		for _, r := range rowsOf(q, h) {
			got = append(got, r.ID)
		}
		slices.Sort(got)
		slices.Sort(w)
		if !slices.Equal(got, w) {
			t.Errorf("%s: %v, want %v", q, got, w)
		}
	}
	want("?repo=Smana/a", header("dev1"), roomID, roomC)
	want("?repo=Smana/b", header("dev1"), roomB)
	want("?repo=Smana/none", header("dev1"))
	// mine: the linked login, case-insensitively, as issue author or reviewer.
	want("?mine=1", header("dev1"), roomID, roomB)
	want("?mine=1", header("dev2"))
	want("?mine=1&repo=Smana/b", header("dev1"), roomB)
	// needs_me: a pending approval the caller could decide. A watcher cannot; an admin can.
	want("?needs_me=1", header("dev1"))
	want("?needs_me=1", admin("boss"), roomB, roomC)
	want("?needs_me=1&repo=Smana/a", admin("boss"), roomC)
	rows := rowsOf("?repo=Smana/b", admin("boss"))
	if len(rows) != 1 || rows[0].Repository != "Smana/b" || !rows[0].NeedsMe {
		t.Fatalf("%+v", rows)
	}
	if rows := rowsOf("?repo=Smana/b", header("dev1")); len(rows) != 1 || rows[0].NeedsMe {
		t.Fatalf("a watcher's needsMe: %+v", rows)
	}
}

// #78: a factory task held by a person in its room needs its driver and its owner on the room
// list too, as the room's summary says (N3), and no one else; the factory's next facts end it.
func TestNeedsMeListsAHoldForWhoeverCanReleaseIt(t *testing.T) {
	e := setup(t, func(s *Server, _ *fanout.Hub, _ *hubView) {
		a := repoRoom(roomID, "Smana/a")
		a.Status.Driver = "human:dev1"
		a.Status.Task = &v1alpha1.TaskStatus{Phase: "Queued"}
		s.Rooms = roomClient(t, a, repoRoom(roomB, "Smana/a"))
	})
	factory := envelope.Actor{Kind: envelope.ActorSystem, ID: "system:factory"}
	facts := func(phase, reason string) {
		e.log.mu.Lock()
		defer e.log.mu.Unlock()
		e.log.evs = append(e.log.evs, envelope.Event{V: 1, Seq: int64(len(e.log.evs) + 1), RoomID: roomID,
			Type: envelope.StateChanged, Actor: factory, TS: time.Now(),
			Payload: envelope.TaskStatePayload(envelope.TaskFacts{Phase: phase, Reason: reason})})
	}
	needs := func(h http.Header) []string {
		t.Helper()
		code, body := get(t, e, "/api/rooms?needs_me=1", h)
		var rows []roomRow
		if code != http.StatusOK || json.Unmarshal([]byte(body), &rows) != nil {
			t.Fatalf("%d %s", code, body)
		}
		ids := []string{}
		for _, r := range rows {
			if !r.NeedsMe {
				t.Fatalf("a needs_me row without needsMe: %+v", r)
			}
			ids = append(ids, r.ID)
		}
		return ids
	}
	facts("Queued", "waiting_human_driver")
	for name, h := range map[string]http.Header{"driver": header("dev1"), "owner": header("own")} {
		if got := needs(h); !slices.Equal(got, []string{roomID}) {
			t.Errorf("%s: %v", name, got)
		}
	}
	if got := needs(header("dev2")); len(got) != 0 {
		t.Errorf("a member who neither drives nor owns the room cannot release it: %v", got)
	}
	facts("Queued", "waiting_run_slot") // the factory's own wait needs no one
	if got := needs(header("dev1")); len(got) != 0 {
		t.Errorf("the hold ended: %v", got)
	}
}

// A filter never reveals a room the caller cannot read.
func TestRoomFiltersRespectTheGate(t *testing.T) {
	w := world(map[string][]string{"Smana/a": {"dev1"}})
	e := setup(t, filteredRooms(t), w.option())
	code, body := get(t, e, "/api/rooms?repo=Smana/b&mine=1", header("dev1"))
	if code != http.StatusOK || body != "[]\n" {
		t.Fatalf("%d %q", code, body)
	}
}

// mine resolves the login once per request, not once per room.
func TestMineResolvesTheLoginOnce(t *testing.T) {
	w := world(nil)
	e := setup(t, filteredRooms(t), w.option())
	before := w.links
	get(t, e, "/api/rooms?mine=1", header("dev1"))
	if w.links-before != 1 {
		t.Fatalf("%d ZITADEL link reads", w.links-before)
	}
}

// A room whose repository the App cannot see must not disable mine for the rest of the request.
func TestMineSurvivesAnUnresolvableRoom(t *testing.T) {
	w := world(nil)
	w.unseen = map[string]bool{"Smana/b": true}
	e := setup(t, func(s *Server, _ *fanout.Hub, _ *hubView) {
		a := repoRoom(roomID, "Smana/b") // listed first
		a.Status.Task = &v1alpha1.TaskStatus{IssueAuthor: "boss"}
		b := repoRoom(roomB, "Smana/a")
		b.Status.Task = &v1alpha1.TaskStatus{PRAuthor: "boss"}
		s.Rooms = roomClient(t, a, b, repoRoom(roomC, "Smana/a"))
	}, w.option())
	before := w.links
	code, body := get(t, e, "/api/rooms?mine=1", admin("boss"))
	var rows []roomRow
	if code != http.StatusOK || json.Unmarshal([]byte(body), &rows) != nil || len(rows) != 1 || rows[0].ID != roomB {
		t.Fatalf("%d %s", code, body)
	}
	if n := w.links - before; n > maxLoginTries {
		t.Fatalf("%d link reads, cap %d", n, maxLoginTries)
	}
}

// The missing-repository warning reads the task facts, never the rendered need text.
func TestWarnNoRepository(t *testing.T) {
	owner := policy.Subject{Kind: envelope.ActorHuman, ID: "human:own", Role: policy.Owner}
	issue := &envelope.IssueFact{Number: 7, URL: "https://github.com/Smana/ref/issues/7"}
	merge := summary.Summary{NeedsYou: []summary.Need{{Kind: "merge", What: "mentions --repo but has none"}}}
	for _, c := range []struct {
		name string
		v    summary.Summary
		f    *envelope.TaskFacts
		sub  policy.Subject
		warn bool
	}{
		{"an issue with its repository", summary.Summary{}, &envelope.TaskFacts{Issue: issue}, owner, false},
		{"an issue with no repository, for its owner", summary.Summary{}, &envelope.TaskFacts{Issue: &envelope.IssueFact{Number: 7}}, owner, true},
		{"an issue with no repository, for a watcher", summary.Summary{}, &envelope.TaskFacts{Issue: &envelope.IssueFact{Number: 7}},
			policy.Subject{Kind: envelope.ActorHuman, ID: "human:w", Role: policy.Watcher}, false},
		{"a merge need with no repository, whatever its text says", merge, &envelope.TaskFacts{PR: &envelope.PRFact{Number: 3}}, owner, true},
		{"a merge need with its repository", merge, &envelope.TaskFacts{PR: &envelope.PRFact{Number: 3, URL: "https://github.com/Smana/ref/pull/3"}}, owner, false},
		{"no facts", summary.Summary{}, nil, owner, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := &Server{Logger: slog.New(slog.NewTextHandler(&buf, nil))}
			s.warnNoRepository(c.v, c.f, roomID, c.sub)
			if got := strings.Contains(buf.String(), "name no repository"); got != c.warn {
				t.Fatalf("warned %v, want %v: %q", got, c.warn, buf.String())
			}
		})
	}
}

// O12: the summary carries the room's still-queued notes, as the queue holds them.
func TestSummaryCarriesTheQueue(t *testing.T) {
	e := setup(t, abc(t), func(s *Server, _ *fanout.Hub, _ *hubView) { s.PublicURL = "https://rooms.example" })
	factsLog(e.log)
	e.log.mu.Lock()
	e.log.queue = []store.Queued{{Ref: 7, Author: "human:dev1", Text: "use the v2 flag", State: "queued"}}
	e.log.mu.Unlock()
	code, body := get(t, e, "/api/rooms/"+roomID+"/summary", admin("boss"))
	var s struct {
		Queued struct {
			Untrusted bool `json:"untrusted"`
			Items     []struct {
				Ref          int64
				Author, Text string
			} `json:"items"`
		} `json:"queued"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &s) != nil || !s.Queued.Untrusted || len(s.Queued.Items) != 1 || s.Queued.Items[0].Text != "use the v2 flag" {
		t.Fatalf("%d %s", code, body)
	}
}

// Audit S-9: the room list and a summary spend a per-principal read budget, 10 a second, burst 20,
// per replica. It is apart from acts, so a page that refreshes never refuses its viewer's next act.
func TestRoomReadsAreRateLimited(t *testing.T) {
	for _, path := range []string{"/api/rooms", "/api/rooms/" + roomID + "/summary"} {
		t.Run(path, func(t *testing.T) {
			e := setup(t, abc(t))
			for i := range 20 {
				if code, body := get(t, e, path, admin("boss")); code != http.StatusOK {
					t.Fatalf("read %d of the burst: %d %s", i+1, code, body)
				}
			}
			limited := false
			for range 20 {
				code, body := get(t, e, path, admin("boss"))
				if code == http.StatusTooManyRequests {
					if body != "rate_limited\n" {
						t.Fatalf("429 body %q", body)
					}
					limited = true
					break
				}
			}
			if !limited {
				t.Fatal("40 reads at once were all served")
			}
			if code, body := get(t, e, path, admin("other")); code != http.StatusOK {
				t.Fatalf("the budget is per principal: %d %s", code, body)
			}
		})
	}
}

// rangeLog is the Server's log, recording the after of each event page it reads. The hub reads
// memLog directly, so its polls are not counted.
type rangeLog struct {
	*memLog
	readMu sync.Mutex
	afters []int64
}

func (l *rangeLog) Range(ctx context.Context, room string, after int64, limit int) ([]envelope.Event, error) {
	l.readMu.Lock()
	l.afters = append(l.afters, after)
	l.readMu.Unlock()
	return l.memLog.Range(ctx, room, after, limit)
}

func (l *rangeLog) reads() []int64 {
	l.readMu.Lock()
	defer l.readMu.Unlock()
	return slices.Clone(l.afters)
}

// Audit S-9: a summary folds only what the log appended since the room's last fold, and the
// summary of an unchanged room reads no event page at all.
func TestSummaryFoldsOnlyNewEvents(t *testing.T) {
	var rl *rangeLog
	e := setup(t, abc(t), func(s *Server, _ *fanout.Hub, v *hubView) {
		rl = &rangeLog{memLog: v.memLog}
		s.Log = rl
	})
	factsLog(e.log)
	for _, step := range []struct {
		name      string
		appended  []envelope.Event
		wantReads []int64
		wantNotes int
	}{
		{"the first summary folds the whole log", nil, []int64{0}, 2},
		{"an unchanged room reads no page", nil, []int64{0}, 2},
		{"one append is read from the cached seq", []envelope.Event{progress(5, "merged")}, []int64{0, 4}, 3},
	} {
		e.log.mu.Lock()
		e.log.evs = append(e.log.evs, step.appended...)
		e.log.mu.Unlock()
		s := summaryOf(t, e, roomID, "", admin("boss"))
		if got := rl.reads(); !slices.Equal(got, step.wantReads) || len(s.Notes.Items) != step.wantNotes {
			t.Fatalf("%s: pages read after %v, %d notes; want %v, %d", step.name, got, len(s.Notes.Items), step.wantReads, step.wantNotes)
		}
	}
}

// A Room re-created under the same id is another room: its summary folds its own log from the
// start, whether that log is shorter than the old room's fold or has passed it.
func TestSummaryOfARecreatedRoomFoldsItsOwnLog(t *testing.T) {
	var rooms client.Client // the Server's Rooms, with the writes the fake reader hides
	e := setup(t, abc(t), func(s *Server, _ *fanout.Hub, _ *hubView) {
		sch := runtime.NewScheme()
		if err := v1alpha1.AddToScheme(sch); err != nil {
			t.Fatal(err)
		}
		rooms = fake.NewClientBuilder().WithScheme(sch).WithObjects(&v1alpha1.Room{
			ObjectMeta: metav1.ObjectMeta{Name: roomID, Namespace: namespace},
			Spec:       v1alpha1.RoomSpec{Owner: "human:own", Driver: "system:factory", DataClass: "public", Repository: "Smana/maku"},
			Status:     v1alpha1.RoomStatus{Phase: "Active", LastSeq: 10, Driver: "system:factory"}}).Build()
		s.Rooms = rooms
	})
	factsLog(e.log)
	notes := func() []string {
		var out []string
		for _, n := range summaryOf(t, e, roomID, "", admin("boss")).Notes.Items {
			out = append(out, n.Text)
		}
		return out
	}
	recreate := func(uid types.UID, evs ...envelope.Event) {
		t.Helper()
		ctx := context.Background()
		var old v1alpha1.Room
		if err := rooms.Get(ctx, client.ObjectKey{Namespace: namespace, Name: roomID}, &old); err != nil {
			t.Fatal(err)
		}
		fresh := &v1alpha1.Room{ObjectMeta: metav1.ObjectMeta{Name: roomID, Namespace: namespace, UID: uid},
			Spec: old.Spec, Status: old.Status}
		if err := rooms.Delete(ctx, &old); err != nil {
			t.Fatal(err)
		}
		if err := rooms.Create(ctx, fresh); err != nil {
			t.Fatal(err)
		}
		e.log.mu.Lock()
		e.log.evs = evs
		e.log.mu.Unlock()
	}
	if got := notes(); !slices.Equal(got, []string{"planning", "checks pass"}) {
		t.Fatalf("the first room: notes %v", got)
	}
	recreate("uid-2", progress(1, "shorter"))
	if got := notes(); !slices.Equal(got, []string{"shorter"}) {
		t.Fatalf("a shorter log of a re-created room: notes %v", got)
	}
	var longer []envelope.Event
	for i, txt := range []string{"a", "b", "c", "d"} {
		longer = append(longer, progress(int64(i+1), txt))
	}
	recreate("uid-3", longer...)
	if got := notes(); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("a log past the cached seq of a re-created room: notes %v", got)
	}
}
