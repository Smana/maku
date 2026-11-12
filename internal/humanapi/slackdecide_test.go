// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/ghidentity"
	"github.com/Smana/maku/internal/repoaccess"
	"github.com/Smana/maku/internal/runwatch"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// The route's fixtures: the App's workspace, the branch a forge.push approval binds, the run the
// approvals were requested for, and the ids the cases name.
const (
	slackTeam   = "T01ABC"         // human.slackDecide.teamID
	slackBranch = "agent/26zfnuxm" // the run's branch: the one thing a forge.push approval binds
	slackRun    = "7f3cq2xz"       // the run ap1 was requested for
	slackPath   = "/api/slack/decisions"

	// slackSub is the linked human the actor and four-eyes cases click as: the sub the design pins
	// its actor, its idempotency scope and its prompter on (D8, D7, OD-16).
	slackSub = "2918"

	// ap1 is the approval every case names; ap2 is the sealed room's; ap3 is ap1's row with no
	// binding; ap4 is ap1's with a run no watch holds; ap5 has the room's owner as a prompter.
	// apMissing is a well-formed ULID the log does not hold. apActor and apActorDenied are the two
	// rows of the actor case, whose ids the design pins its idempotency scope on.
	ap1           = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	ap2           = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	ap3           = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	ap4           = "01ARZ3NDEKTSV4RRFFQ69G5FAY"
	ap5           = "01ARZ3NDEKTSV4RRFFQ69G5FAZ"
	apActor       = "01M4A1B2C3D4E5F6G7H8J9K0MN"
	apActorDenied = "01M4A1B2C3D4E5F6G7H8J9K0MP"
	apMissing     = "01M4A1B2C3D4E5F6G7H8J9K0MN"
)

// The store's own closes, as internal/store writes them: the broker's actor, its key, and the
// reasons it closes a row a human was too late for.
const (
	slackBroker        = "system:room-broker"
	slackReasonExpired = "no decision before the deadline"
)

// classApprovals are the ids of five approvals whose class the route never decides, one per class
// TestSlackDecideClasses presses.
var classApprovals = []string{
	"01ARZ3NDEKTSV4RRFFQ69G5FB1", "01ARZ3NDEKTSV4RRFFQ69G5FB2", "01ARZ3NDEKTSV4RRFFQ69G5FB3",
	"01ARZ3NDEKTSV4RRFFQ69G5FB4", "01ARZ3NDEKTSV4RRFFQ69G5FB5",
}

// decided is what reached the store: one decision, with the draft's idempotency scope and seq.
type decided struct {
	by, decision, reason, originClient string
	seq                                int64
}

// slackLinks is the rig's link table. U03 has no row; U04's account has no GitHub identity; U05's
// cannot be read and U06's permission cannot be; U07 is an approver with read only, U08 writes the
// repository with no standing in the room, and U09 is an agents-admin with no entry in the Room CR.
func slackLinks() map[string]string {
	return map[string]string{
		slackTeam + "/U02": "own", // the room's owner, who writes its repository
		slackTeam + "/U04": "nogh",
		slackTeam + "/U05": "boom",
		slackTeam + "/U06": "nowrite",
		slackTeam + "/U07": "dana",
		slackTeam + "/U08": "writer",
		slackTeam + "/U09": "admin",
	}
}

// slackRow is one of the rig's rows: pending, bound to binding (empty for none), requested a minute
// ago and due in an hour, by the approvers a case names.
func slackRow(id, room, run, binding string, prompters ...string) store.Approval {
	now := time.Now()
	return store.Approval{ID: id, RoomID: room, RunID: run, EventID: "ev", CallID: "call-1", Class: slackDecidableClass,
		State: store.ApprovalPending, BindingBranch: binding, Prompters: prompters,
		RequestedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
}

// slackApprovals is the rig's rows.
func slackApprovals() map[string]store.Approval {
	return map[string]store.Approval{
		ap1: slackRow(ap1, roomID, slackRun, slackBranch),
		ap2: slackRow(ap2, roomB, slackRun, slackBranch),
		ap3: slackRow(ap3, roomID, slackRun, ""),
		ap4: slackRow(ap4, roomID, "9zzzzzzz", slackBranch),
		ap5: slackRow(ap5, roomID, slackRun, slackBranch, "human:own"),
	}
}

// memDecides is the route's store: the rig's log for the room's state, the link table, the rows by
// id, and the decisions the route recorded.
type memDecides struct {
	*memLog
	mu        sync.Mutex
	links     map[string]string // team/user -> sub
	rows      map[string]store.Approval
	decided   *[]decided
	sealed    map[string]bool // room -> sealed, as the log would say
	decideErr error
	linkErr   error
	roomErr   error
	rowErr    error

	// drafts and events are what the store was handed and what it appended, in order: the actor,
	// the origin and the idempotency key, which the counters and the event body do not carry.
	drafts []envelope.Draft
	events []envelope.Event
	keys   map[string]stored // originClient:seq -> what that key recorded
	// supersede is the approvals the store refuses because their call already has a result or their
	// run ended, and the reason it closes each with.
	supersede map[string]string
}

// stored is what one idempotency key recorded: the decision and the event its first write appended.
type stored struct {
	decision string
	ev       envelope.Event
}

func (m *memDecides) SlackLink(_ context.Context, team, user string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.linkErr != nil {
		return "", false, m.linkErr
	}
	sub, ok := m.links[team+"/"+user]
	return sub, ok, nil
}

func (m *memDecides) Approval(_ context.Context, id string) (store.Approval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rowErr != nil {
		return store.Approval{}, m.rowErr
	}
	a, ok := m.rows[id]
	if !ok {
		return store.Approval{}, store.ErrNoApproval
	}
	return a, nil
}

// Room is the store's row, sealed for the rooms the rig says are sealed.
func (m *memDecides) Room(ctx context.Context, id string) (store.RoomState, error) {
	m.mu.Lock()
	sealed, err := m.sealed[id], m.roomErr
	m.mu.Unlock()
	switch {
	case err != nil:
		return store.RoomState{}, err
	case sealed:
		return store.RoomState{ID: id, Sealed: true}, nil
	}
	return m.memLog.Room(ctx, id)
}

// Decide is store.Decide as approvals.go documents it: the key is checked first, so the replay of a
// click that won answers as itself; a call already answered or a run already ended closes the row as
// superseded and is ErrAlreadyDecided; a decision past the deadline closes it as expired and is
// ErrApprovalExpired. Every one of those closes is the store's — the broker's actor and key — so a
// route that appended anything of its own would leave a second event in the log (D7).
func (m *memDecides) Decide(_ context.Context, id, decision, by, reason string, d envelope.Draft) (envelope.Event, store.Approval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.decideErr != nil {
		return envelope.Event{}, store.Approval{}, m.decideErr
	}
	a, ok := m.rows[id]
	if !ok {
		return envelope.Event{}, a, store.ErrNoApproval
	}
	key := d.OriginClient + ":" + strconv.FormatInt(d.OriginSeq, 10)
	if prev, ok := m.keys[key]; ok {
		if prev.decision != decision {
			return envelope.Event{}, a, store.ErrKeyConflict
		}
		return prev.ev, a, nil
	}
	closeAs, why, refusal := "", "", error(nil)
	switch {
	case a.State == store.ApprovalExpired:
		return envelope.Event{}, a, store.ErrApprovalExpired
	case a.State != store.ApprovalPending:
		return envelope.Event{}, a, store.ErrAlreadyDecided
	case m.supersede[id] != "": // the call answered and the run ended both come before the deadline
		closeAs, why, refusal = store.ApprovalSuperseded, m.supersede[id], store.ErrAlreadyDecided
	case time.Now().After(a.ExpiresAt):
		closeAs, why, refusal = store.ApprovalExpired, slackReasonExpired, store.ErrApprovalExpired
	}
	if closeAs != "" { // too late for anyone: closed for what it is, then refused
		m.closeLocked(a, closeAs, why)
		return envelope.Event{}, a, refusal
	}
	a.State = decision
	m.rows[id] = a
	*m.decided = append(*m.decided, decided{by: by, decision: decision, reason: reason,
		originClient: d.OriginClient, seq: d.OriginSeq})
	d.RoomID, d.RunID, d.Type = a.RoomID, a.RunID, envelope.ApprovalDecided
	d.Payload = envelope.Must(envelope.ApprovalDecidedPayload{ApprovalID: id, Decision: decision, Reason: reason})
	ev := m.appendLocked(d)
	m.keys[key] = stored{decision: decision, ev: ev}
	return ev, a, nil
}

// appendLocked is the store's append: the event joins the room's log at the next seq, carrying the
// draft's actor and origin, and the case keeps it.
func (m *memDecides) appendLocked(d envelope.Draft) envelope.Event {
	m.memLog.mu.Lock()
	defer m.memLog.mu.Unlock()
	ev := envelope.Event{V: envelope.Version, Seq: int64(len(m.evs) + 1), RoomID: d.RoomID, RunID: d.RunID,
		Actor: d.Actor, Type: d.Type, Origin: d.Origin, Payload: d.Payload, TS: time.Now()}
	m.evs = append(m.evs, ev)
	m.events = append(m.events, ev)
	m.drafts = append(m.drafts, d)
	return ev
}

// closeLocked is the store's own close of a row it refuses: the broker's actor and key, one
// approval_decided, and the row moved to closeAs. Nothing of the route's is in it.
func (m *memDecides) closeLocked(a store.Approval, closeAs, why string) {
	a.State = closeAs
	m.rows[a.ID] = a
	d := envelope.Draft{RoomID: a.RoomID, RunID: a.RunID,
		Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: slackBroker},
		Type:  envelope.ApprovalDecided, Origin: envelope.OriginBroker,
		OriginClient: "broker:approval:" + a.ID, OriginSeq: 1,
		Payload: envelope.Must(envelope.ApprovalDecidedPayload{ApprovalID: a.ID, Decision: closeAs, Reason: why})}
	m.appendLocked(d)
	*m.decided = append(*m.decided, decided{by: slackBroker, decision: closeAs, reason: why,
		originClient: d.OriginClient, seq: d.OriginSeq})
}

// sweepExpired is store.ExpireDue: every pending row past its deadline, closed by the broker. It
// returns how many it closed, so a case can prove the click already closed its own row.
func (m *memDecides) sweepExpired() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, a := range m.rows {
		if a.State == store.ApprovalPending && time.Now().After(a.ExpiresAt) {
			m.closeLocked(a, store.ApprovalExpired, slackReasonExpired)
			n++
		}
	}
	return n
}

// row is the store's row as it now stands.
func (m *memDecides) row(id string) store.Approval {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[id]
}

// draft is the last decision the store was handed: its actor and its idempotency key.
func (m *memDecides) draft() envelope.Draft {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.drafts) == 0 {
		return envelope.Draft{}
	}
	return m.drafts[len(m.drafts)-1]
}

// decidedEvents is every approval_decided the store appended, in order: its own closes included.
func (m *memDecides) decidedEvents() []envelope.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []envelope.Event
	for _, ev := range m.events {
		if ev.Type == envelope.ApprovalDecided {
			out = append(out, ev)
		}
	}
	return out
}

// decideGitHub is the rig's GitHub gate, answering per sub: writes lists the logins that write a
// repository, failSub the subs whose account cannot be read, failPerm the logins whose permission
// cannot be asked, unlinked the subs with no GitHub identity at all. It counts every question it
// is asked, so a class the route never decides can prove it asked none.
type decideGitHub struct {
	mu       sync.Mutex
	writes   map[string]bool
	unlinked map[string]bool
	failSub  map[string]bool
	failPerm map[string]bool
	subs     []string // a linked sub's GitHub id is its index + 1
	calls    int
}

// slackGitHub is the rig's gate: the owner, the writer and the agents-admin write Smana/a; dana
// reads it; nogh has no GitHub identity; boom's account and nowrite's permission cannot be read.
func slackGitHub() *decideGitHub {
	return &decideGitHub{
		writes:   map[string]bool{"own": true, "writer": true, "admin": true},
		unlinked: map[string]bool{"nogh": true},
		failSub:  map[string]bool{"boom": true},
		failPerm: map[string]bool{"nowrite": true},
	}
}

func (g *decideGitHub) checked() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// gate wires the world into d as the broker wires ZITADEL and GitHub.
func (g *decideGitHub) gate(d *SlackDecider) {
	d.Identity = &ghidentity.Resolver{IDPID: "github-idp", TTL: 5 * time.Minute, Now: time.Now,
		Links: func(_ context.Context, sub string) ([]ghidentity.Link, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.calls++
			if g.failSub[sub] {
				return nil, errors.New("zitadel down")
			}
			if g.unlinked[sub] {
				return []ghidentity.Link{{IDPID: "google-idp", UserID: "g-" + sub}}, nil
			}
			i := slices.Index(g.subs, sub)
			if i < 0 {
				g.subs = append(g.subs, sub)
				i = len(g.subs) - 1
			}
			return []ghidentity.Link{{IDPID: "github-idp", UserID: strconv.Itoa(i + 1)}}, nil
		},
		LoginOf: func(_ context.Context, _ string, id int64) (string, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.calls++
			if id < 1 || int(id) > len(g.subs) {
				return "", errors.New("unknown github id")
			}
			return g.subs[id-1], nil
		}}
	d.Access = &repoaccess.Checker{TTL: 5 * time.Minute, Now: time.Now,
		Perm: func(_ context.Context, _, _, login string) (string, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.calls++
			switch {
			case g.failPerm[login]:
				return "", errors.New("github down")
			case g.writes[login]:
				return "write", nil
			}
			return "read", nil
		}}
}

// slackDecideOn serves POST /api/slack/decisions as the App: the team T01ABC, the rig's rooms, the
// rig's GitHub gate, and memLog-backed readings plus a decision log the test inspects.
func slackDecideOn(t *testing.T, links map[string]string, aps map[string]store.Approval, decisions *[]decided) option {
	_, opt := slackDecideOnStore(t, links, aps, decisions)
	return opt
}

// slackDecideOnStore is slackDecideOn with the store handed back, for the cases that read what the
// store recorded, and in what order, rather than only what the route answered.
func slackDecideOnStore(t *testing.T, links map[string]string, aps map[string]store.Approval, decisions *[]decided) (*memDecides, option) {
	t.Helper()
	st := slackDecideStore(links, aps, decisions)
	return st, slackDecideRig(t, st)
}

// slackDecideStore is the route's store over links, aps and decisions, apart from the server that
// serves it: the limiter's state is the SlackDecider's, so a case that proves it per instance mounts
// one store on two servers.
func slackDecideStore(links map[string]string, aps map[string]store.Approval, decisions *[]decided) *memDecides {
	return &memDecides{links: links, rows: aps, decided: decisions, sealed: map[string]bool{roomB: true},
		keys: map[string]stored{}}
}

// slackDecideRig serves st as the App's decider on the server, the team T01ABC, the rig's rooms and
// the rig's GitHub gate.
func slackDecideRig(t *testing.T, st *memDecides) option {
	return func(s *Server, _ *fanout.Hub, v *hubView) {
		if st.memLog == nil { // one log for the store: a second server over it keeps the first's
			st.memLog = v.memLog
		}
		rooms := roomClient(t, slackRoomA(), repoRoom(roomB, "Smana/b"))
		// The run the approvals were requested for is live, on its branch: the binding is read from
		// the watch, never from the body (D6).
		if w, ok := s.Runs.(*runwatch.Watcher); ok {
			w.Upsert(t.Context(), slackRunClaim(slackRun, roomID, slackBranch))
		}
		d := &SlackDecider{Store: st, Principal: "system:slack-app", TeamID: slackTeam, Rooms: rooms,
			Namespace: namespace, Runs: s.Runs, Metrics: s.Metrics, Logger: s.log()}
		slackGitHub().gate(d)
		s.Rooms, s.SlackDecide = rooms, d
	}
}

// slackRoomOwnedBy is room A with sub as its owner and the four-eyes flag the case sets: the
// standing the clicker's own principal must hold is the human's, not the App's, so a case that
// clicks as someone the shared fixture does not have needs a room of its own.
func slackRoomOwnedBy(t *testing.T, sub string, fourEyes bool) option {
	t.Helper()
	return func(s *Server, _ *fanout.Hub, _ *hubView) {
		room := slackRoomA()
		room.Spec.Owner = "human:" + sub
		room.Spec.Approvals.FourEyes = fourEyes
		rooms := roomClient(t, room, repoRoom(roomB, "Smana/b"))
		s.Rooms = rooms
		if s.SlackDecide != nil {
			s.SlackDecide.Rooms = rooms
		}
	}
}

// gateOn replaces the rig's GitHub gate with w's: the readings a case needs, for a sub the shared
// gate does not know.
func gateOn(w *decideGitHub) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) { w.gate(s.SlackDecide) }
}

// slackRunClaim is the run an approval binds, as runwatch reads a claim: the room's implementer on
// the branch the row recorded.
func slackRunClaim(id, room, branch string) *unstructured.Unstructured {
	u := agentRun(id, room, "implementer")
	_ = unstructured.SetNestedField(u.Object, branch, "spec", "branch")
	return u
}

// slackRoomA is room A as the route's cases read it: owner human:own, four-eyes on, and the four
// approvers whose standing the rows need.
func slackRoomA() *v1alpha1.Room {
	a := repoRoom(roomID, "Smana/a")
	a.Spec.Approvals.FourEyes = true
	a.Spec.Members = []v1alpha1.Member{
		{Principal: "human:dana", Role: "watcher", Approver: true},
		{Principal: "human:nogh", Role: "watcher", Approver: true},
		{Principal: "human:boom", Role: "watcher", Approver: true},
		{Principal: "human:nowrite", Role: "watcher", Approver: true},
	}
	return a
}

// press is a body of §1 as the App sends it: the team, the clicker, the approval, the hint's branch
// and the decision. Every case names its team, so a peer workspace's click is expressible.
func press(team, user, ap, branch, decision string) SlackDecision {
	return SlackDecision{Team: team, User: user, Channel: "C0CC4M8SNVD", MessageTS: "1730822400.123456",
		ApprovalID: ap, Branch: branch, Decision: decision}
}

// decide posts b as the App and returns the HTTP status and the result code. The answer carries the
// code and nothing else, on every path.
func decide(t *testing.T, e env, b SlackDecision) (int, string) {
	t.Helper()
	body, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	status, raw := postDecide(t, e, bearer("slack-app"), string(body))
	return answerOf(t, status, raw)
}

// answerOf is the route's one-key answer: the status and the code, and nothing else in the body.
func answerOf(t *testing.T, status int, raw string) (int, string) {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("%d: the answer is not §1's body: %q", status, raw)
	}
	if len(out) != 1 {
		t.Fatalf("%d: the answer says more than its code: %q", status, raw)
	}
	result, ok := out["result"].(string)
	if !ok {
		t.Fatalf("%d: no result code: %q", status, raw)
	}
	return status, result
}

// postDecide posts body to the route with h: the status and the raw body.
func postDecide(t *testing.T, e env, h http.Header, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.ts.URL+slackDecidePath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = h.Clone()
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	raw, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(raw)
}

// ack reads the connection's ack, past its state frame and replay.
func ack(t *testing.T, c *websocket.Conn) wire.ServerFrame {
	t.Helper()
	for range 64 {
		if f := read(t, c); f.Type == wire.FrameAck {
			return f
		}
	}
	t.Fatal("no ack")
	return wire.ServerFrame{}
}

// D1, D15, D3: one case per row of §1's table, each with its exact code, and the refusal says
// nothing else.
func TestSlackDecideRefusals(t *testing.T) {
	var decisions []decided
	e := setup(t, withSponsored(sponsoredP), slackDecideOn(t, slackLinks(), slackApprovals(), &decisions))
	for name, c := range map[string]struct {
		body   SlackDecision
		status int
		result string
	}{
		"a peer workspace":          {press("T02PEER", "U02", ap1, slackBranch, "approved"), 200, "not_permitted"},
		"an unlinked user":          {press(slackTeam, "U03", ap1, slackBranch, "approved"), 200, "not_linked"},
		"an account with no GitHub": {press(slackTeam, "U04", ap1, slackBranch, "approved"), 200, "not_linked"},
		"no such approval":          {press(slackTeam, "U02", apMissing, slackBranch, "approved"), 200, "not_found"},
		"a sealed room":             {press(slackTeam, "U02", ap2, slackBranch, "approved"), 200, "sealed"},
		"the wrong branch":          {press(slackTeam, "U02", ap1, "agent/other", "approved"), 200, "binding_mismatch"},
		// §1's table row for a ZITADEL or GitHub read that cannot be made: it fails closed, with no
		// detail, and never as a decision.
		"the account cannot be read":    {press(slackTeam, "U05", ap1, slackBranch, "approved"), 200, "link_unverified"},
		"the permission cannot be read": {press(slackTeam, "U06", ap1, slackBranch, "approved"), 200, "link_unverified"},
		"a malformed decision":          {press(slackTeam, "U02", ap1, slackBranch, "maybe"), 400, "bad_request"},
		"a bad approval id":             {press(slackTeam, "U02", "not-a-ulid", slackBranch, "approved"), 400, "bad_request"},
		"a bad branch shape":            {press(slackTeam, "U02", ap1, "main", "approved"), 400, "bad_request"},
	} {
		t.Run(name, func(t *testing.T) {
			got, result := decide(t, e, c.body)
			if got != c.status || result != c.result {
				t.Fatalf("%d %s, want %d %s", got, result, c.status, c.result)
			}
			if len(decisions) != 0 {
				t.Fatalf("a refusal decided: %+v", decisions)
			}
		})
	}
}

// D4: both standings, both required. An approver without repository write and a writer without
// standing are each refused with their own code; owner and writer decides. A ZITADEL admin outside
// the Room CR is refused here (D15) and may still decide on the room page.
func TestSlackDecideNeedsBothStandings(t *testing.T) {
	for name, c := range map[string]struct {
		body   SlackDecision
		result string
	}{
		"an approver without repository write": {press(slackTeam, "U07", ap1, slackBranch, "approved"), "repository_permission"},
		"a writer without standing":            {press(slackTeam, "U08", ap1, slackBranch, "approved"), "not_permitted"},
		"the owner, who writes it":             {press(slackTeam, "U02", ap1, slackBranch, "approved"), "approved"},
		"an agents-admin outside the Room CR":  {press(slackTeam, "U09", ap1, slackBranch, "approved"), "not_permitted"},
	} {
		t.Run(name, func(t *testing.T) {
			var decisions []decided
			e := setup(t, withSponsored(sponsoredP), slackDecideOn(t, slackLinks(), slackApprovals(), &decisions))
			if got, result := decide(t, e, c.body); got != http.StatusOK || result != c.result {
				t.Fatalf("%d %s, want 200 %s", got, result, c.result)
			}
			if c.result != "approved" {
				if len(decisions) != 0 {
					t.Fatalf("a refusal decided: %+v", decisions)
				}
				return
			}
			want := []decided{{by: "human:own", decision: "approved", originClient: "human:own:slack:" + ap1, seq: 1}}
			if !slices.Equal(decisions, want) {
				t.Fatalf("the store got %+v, want %+v", decisions, want)
			}
		})
	}

	t.Run("the same agents-admin decides on the room page", func(t *testing.T) {
		log := &actLog{st: store.RoomState{ID: roomID, Driver: "system:factory", DriverEpoch: 3},
			approvals: map[string]store.Approval{ap1: {ID: ap1, RoomID: roomID, RunID: slackRun,
				Class: slackDecidableClass, State: store.ApprovalPending}}}
		e := setup(t, actorOn(log))
		c, _, err := connect(t, e, header("admin", "X-Test-Admin", "1"))
		if err != nil {
			t.Fatal(err)
		}
		send(t, c, hello(nil, 0))
		send(t, c, wire.ClientFrame{Type: wire.FrameAct, ClientSeq: 7,
			Action: json.RawMessage(`{"kind":"decide","approvalId":"` + ap1 + `","decision":"approved"}`)})
		if f := ack(t, c); f.Rejected != "" || f.Seq == 0 {
			t.Fatalf("the admin on the room page: %+v", f)
		}
		if d := log.decisions; !slices.Equal(d, []string{"human:admin approved "}) {
			t.Fatalf("the store got %q", d)
		}
	})
}

// D6: the hint is a hint. The request's branch must be the run's, and an approval with no branch
// decides nothing.
func TestSlackDecideBinding(t *testing.T) {
	for name, c := range map[string]struct {
		body   SlackDecision
		result string
	}{
		"the run's branch":            {press(slackTeam, "U02", ap1, slackBranch, "approved"), "approved"},
		"another branch in the hint":  {press(slackTeam, "U02", ap1, "agent/other", "approved"), "binding_mismatch"},
		"an approval with no binding": {press(slackTeam, "U02", ap3, slackBranch, "approved"), "binding_mismatch"},
		"a run no watch holds":        {press(slackTeam, "U02", ap4, slackBranch, "approved"), "binding_mismatch"},
		"the run's branch, denied":    {press(slackTeam, "U02", ap1, slackBranch, "denied"), "denied"},
	} {
		t.Run(name, func(t *testing.T) {
			var decisions []decided
			e := setup(t, withSponsored(sponsoredP), slackDecideOn(t, slackLinks(), slackApprovals(), &decisions))
			if got, result := decide(t, e, c.body); got != http.StatusOK || result != c.result {
				t.Fatalf("%d %s, want 200 %s", got, result, c.result)
			}
			switch c.result {
			case "approved", "denied":
				if len(decisions) != 1 || decisions[0].decision != c.result {
					t.Fatalf("the store got %+v", decisions)
				}
			default:
				if len(decisions) != 0 {
					t.Fatalf("a refusal decided: %+v", decisions)
				}
			}
		})
	}
}

// HR28: a digest-bound row decides with its run ended, when the press's branch is the row's
// binding; while the watch still holds the run, the run's branch must agree. A branch-bound row
// with no live run is refused as today.
func TestSlackDecideDigestBound(t *testing.T) {
	const ended, moved = "01ARZ3NDEKTSV4RRFFQ69G5FC1", "01ARZ3NDEKTSV4RRFFQ69G5FC2"
	digestRow := func(id, run, branch string) store.Approval {
		a := slackRow(id, roomID, run, branch)
		a.BindingDigest, a.BindingOutput, a.CallID = strings.Repeat("e", 64), "0123456789abcdef", "output:0123456789abcdef"
		return a
	}
	for name, c := range map[string]struct {
		body   SlackDecision
		result string
	}{
		"the binding, the run ended":         {press(slackTeam, "U02", ended, slackBranch, "approved"), "approved"},
		"another branch, the run ended":      {press(slackTeam, "U02", ended, "agent/other", "approved"), "binding_mismatch"},
		"the binding, the live run moved":    {press(slackTeam, "U02", moved, "agent/old", "approved"), "binding_mismatch"},
		"a branch-bound row, no live run":    {press(slackTeam, "U02", ap4, slackBranch, "approved"), "binding_mismatch"},
		"the binding, the run ended, denied": {press(slackTeam, "U02", ended, slackBranch, "denied"), "denied"},
	} {
		t.Run(name, func(t *testing.T) {
			var decisions []decided
			aps := slackApprovals()
			aps[ended] = digestRow(ended, "9zzzzzzz", slackBranch)
			aps[moved] = digestRow(moved, slackRun, "agent/old") // the watch holds slackRun on slackBranch
			e := setup(t, withSponsored(sponsoredP), slackDecideOn(t, slackLinks(), aps, &decisions))
			if got, result := decide(t, e, c.body); got != http.StatusOK || result != c.result {
				t.Fatalf("%d %s, want 200 %s", got, result, c.result)
			}
			if decidedOne := c.result == "approved" || c.result == "denied"; decidedOne != (len(decisions) == 1) {
				t.Fatalf("the store got %+v", decisions)
			}
		})
	}
}

// D8: the log names the person, never the App. One actor is stamped, and the surface lives in the
// idempotency scope.
func TestSlackDecideActorIsHuman(t *testing.T) {
	var decisions []decided
	rows := map[string]store.Approval{
		apActor:       slackRow(apActor, roomID, slackRun, slackBranch),
		apActorDenied: slackRow(apActorDenied, roomID, slackRun, slackBranch),
	}
	st, rig := slackDecideOnStore(t, map[string]string{slackTeam + "/U02": slackSub}, rows, &decisions)
	w := &decideGitHub{writes: map[string]bool{slackSub: true}}
	e := setup(t, withSponsored(sponsoredP), rig, slackRoomOwnedBy(t, slackSub, true), gateOn(w))

	if got, result := decide(t, e, press(slackTeam, "U02", apActor, slackBranch, store.ApprovalApproved)); got != http.StatusOK || result != store.ApprovalApproved {
		t.Fatalf("%d %s, want 200 approved", got, result)
	}
	evs := st.decidedEvents()
	if len(evs) != 1 {
		t.Fatalf("the store appended %d approval_decided, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Actor.Kind != envelope.ActorHuman || ev.Actor.ID != "human:"+slackSub {
		t.Fatalf("the event's actor is %+v, want the linked human", ev.Actor)
	}
	if ev.Origin != envelope.OriginClient {
		t.Fatalf("the event's origin is %q, want %q", ev.Origin, envelope.OriginClient)
	}
	// The App is nowhere in the record: a payload that named it would make it an actor in the room.
	if ev.Actor.ID == "system:slack-app" || strings.Contains(string(ev.Payload), "slack-app") {
		t.Fatalf("the App is in the record: %+v %s", ev.Actor, ev.Payload)
	}
	// The surface is in the key instead: one key per approval, and one per decision within it.
	dr := st.draft()
	if dr.Actor != ev.Actor || dr.Origin != ev.Origin {
		t.Fatalf("the draft's actor and origin are %+v %q, want the event's", dr.Actor, dr.Origin)
	}
	if want := "human:" + slackSub + ":slack:" + apActor; dr.OriginClient != want {
		t.Fatalf("the idempotency key is %q, want %q", dr.OriginClient, want)
	}
	if dr.OriginSeq != 1 {
		t.Fatalf("the approval's seq is %d, want 1", dr.OriginSeq)
	}
	// A denial is its own key: the same approval's no is not the replay of its yes.
	if got, result := decide(t, e, press(slackTeam, "U02", apActorDenied, slackBranch, store.ApprovalDenied)); got != http.StatusOK || result != store.ApprovalDenied {
		t.Fatalf("%d %s, want 200 denied", got, result)
	}
	if dr := st.draft(); dr.OriginSeq != 2 || dr.OriginClient != "human:"+slackSub+":slack:"+apActorDenied {
		t.Fatalf("the denial's key is %q:%d, want the second seq under its own approval", dr.OriginClient, dr.OriginSeq)
	}
	if len(decisions) != 2 || decisions[1].by != "human:"+slackSub {
		t.Fatalf("the store got %+v", decisions)
	}
}

// D7: the same click twice is one decision and the same answer; approve then deny is
// already_decided and still one approval_decided.
func TestSlackDecideIdempotent(t *testing.T) {
	var decisions []decided
	st, rig := slackDecideOnStore(t, slackLinks(), slackApprovals(), &decisions)
	e := setup(t, withSponsored(sponsoredP), rig)

	for i := range 2 {
		if got, result := decide(t, e, press(slackTeam, "U02", ap1, slackBranch, store.ApprovalApproved)); got != http.StatusOK || result != store.ApprovalApproved {
			t.Fatalf("press %d: %d %s, want 200 approved", i+1, got, result)
		}
		if len(decisions) != 1 {
			t.Fatalf("press %d: the store recorded %+v, want one decision", i+1, decisions)
		}
	}
	if got, result := decide(t, e, press(slackTeam, "U02", ap1, slackBranch, store.ApprovalDenied)); got != http.StatusOK || result != slackAlreadyDecided {
		t.Fatalf("the opposite press: %d %s, want 200 already_decided", got, result)
	}
	if len(decisions) != 1 {
		t.Fatalf("the opposite press recorded %+v, want the one decision kept", decisions)
	}
	if n := len(st.decidedEvents()); n != 1 {
		t.Fatalf("the log holds %d approval_decided, want 1", n)
	}
	if a := st.row(ap1); a.State != store.ApprovalApproved {
		t.Fatalf("the row is %q, want the first decision's", a.State)
	}
}

// OD-16: four eyes hold on this surface too, on the row's own prompters as the room page compares
// them. The person who asked is refused while they are on, another approver who writes the
// repository decides, and with four eyes off the prompter decides their own request.
func TestSlackDecideFourEyes(t *testing.T) {
	links := map[string]string{slackTeam + "/U02": slackSub, slackTeam + "/U07": "dana"}
	for name, c := range map[string]struct {
		user    string
		four    bool
		result  string
		decided int
	}{
		"the prompter, four eyes on":     {"U02", true, "four_eyes", 0},
		"another approver, four eyes on": {"U07", true, "approved", 1},
		"the prompter, four eyes off":    {"U02", false, "approved", 1},
	} {
		t.Run(name, func(t *testing.T) {
			var decisions []decided
			rows := map[string]store.Approval{ap5: slackRow(ap5, roomID, slackRun, slackBranch, "human:"+slackSub)}
			st, rig := slackDecideOnStore(t, links, rows, &decisions)
			w := &decideGitHub{writes: map[string]bool{slackSub: true, "dana": true}}
			e := setup(t, withSponsored(sponsoredP), rig, slackRoomOwnedBy(t, slackSub, c.four), gateOn(w))
			if got, result := decide(t, e, press(slackTeam, c.user, ap5, slackBranch, store.ApprovalApproved)); got != http.StatusOK || result != c.result {
				t.Fatalf("%d %s, want 200 %s", got, result, c.result)
			}
			if len(decisions) != c.decided {
				t.Fatalf("the store recorded %+v, want %d", decisions, c.decided)
			}
			if n := len(st.decidedEvents()); n != c.decided {
				t.Fatalf("the log holds %d approval_decided, want %d", n, c.decided)
			}
		})
	}
}

// D7's other closes: a click past the deadline, before any sweep. The store's own deadline fence
// closes the row as expired and refuses; the route answers approval_expired and writes nothing of
// its own, so the log keeps the store's one event and a sweep afterwards has nothing left to close.
func TestSlackDecideExpired(t *testing.T) {
	var decisions []decided
	row := slackRow(ap1, roomID, slackRun, slackBranch)
	row.ExpiresAt = time.Now().Add(-time.Second)
	st, rig := slackDecideOnStore(t, slackLinks(), map[string]store.Approval{ap1: row}, &decisions)
	e := setup(t, withSponsored(sponsoredP), rig)

	if got, result := decide(t, e, press(slackTeam, "U02", ap1, slackBranch, store.ApprovalApproved)); got != http.StatusOK || result != slackApprovalExpired {
		t.Fatalf("%d %s, want 200 approval_expired", got, result)
	}
	if a := st.row(ap1); a.State != store.ApprovalExpired {
		t.Fatalf("the row is %q, want expired", a.State)
	}
	// The one write is the store's refusal path, not the route's: the broker's close of the row.
	if len(decisions) != 1 || decisions[0].by != slackBroker || decisions[0].decision != store.ApprovalExpired {
		t.Fatalf("the store recorded %+v, want the broker's one close", decisions)
	}
	evs := st.decidedEvents()
	if len(evs) != 1 {
		t.Fatalf("the log holds %d approval_decided, want the store's one", len(evs))
	}
	if evs[0].Actor.ID != slackBroker || evs[0].Origin != envelope.OriginBroker {
		t.Fatalf("the event is %+v, want the broker's close", evs[0])
	}
	if !strings.Contains(string(evs[0].Payload), store.ApprovalExpired) {
		t.Fatalf("the event says %s, want the expiry", evs[0].Payload)
	}
	// The sweep the route may have beaten appends nothing: the row is no longer pending.
	if n := st.sweepExpired(); n != 0 {
		t.Fatalf("the sweep closed %d rows after the click", n)
	}
	if n := len(st.decidedEvents()); n != 1 {
		t.Fatalf("the log holds %d approval_decided after the sweep, want 1", n)
	}
}

// D7: a call answered after the request, and a run that ended, are both already_decided with the row
// superseded: the store closes it for what it is, and the click appends nothing.
func TestSlackDecideSuperseded(t *testing.T) {
	for name, why := range map[string]string{
		"the call answered after the request": "the call already has a result",
		"the run ended":                       "the run ended",
	} {
		t.Run(name, func(t *testing.T) {
			var decisions []decided
			st, rig := slackDecideOnStore(t, slackLinks(),
				map[string]store.Approval{ap1: slackRow(ap1, roomID, slackRun, slackBranch)}, &decisions)
			st.supersede = map[string]string{ap1: why}
			e := setup(t, withSponsored(sponsoredP), rig)

			if got, result := decide(t, e, press(slackTeam, "U02", ap1, slackBranch, store.ApprovalApproved)); got != http.StatusOK || result != slackAlreadyDecided {
				t.Fatalf("%d %s, want 200 already_decided", got, result)
			}
			if a := st.row(ap1); a.State != store.ApprovalSuperseded {
				t.Fatalf("the row is %q, want superseded", a.State)
			}
			if len(decisions) != 1 || decisions[0].reason != why {
				t.Fatalf("the store recorded %+v, want its own close for %q", decisions, why)
			}
			evs := st.decidedEvents()
			if len(evs) != 1 || evs[0].Actor.ID != slackBroker || !strings.Contains(string(evs[0].Payload), store.ApprovalSuperseded) {
				t.Fatalf("the log holds %+v, want the store's one superseded close", evs)
			}
		})
	}
}

// D13: one class. Every other class is not_supported, whatever the clicker's standing, and no
// GitHub call is made for it.
func TestSlackDecideClasses(t *testing.T) {
	classes := []string{"forge.pr", "forge.other", "mcp.write", "shell.high", "egress.new"}
	rows := map[string]store.Approval{}
	for i, class := range classes {
		rows[classApprovals[i]] = store.Approval{ID: classApprovals[i], RoomID: roomID, RunID: slackRun,
			EventID: "ev", CallID: "call-1", Class: class, State: store.ApprovalPending, BindingBranch: slackBranch}
	}
	var decisions []decided
	w := &decideGitHub{}
	e := setup(t, withSponsored(sponsoredP), slackDecideOn(t, slackLinks(), rows, &decisions), gateOn(w))
	for i, class := range classes {
		// U09 stands in for any clicker: an agents-admin with no entry in the Room CR, refused as
		// not_supported rather than as not_permitted, since the class is checked first.
		if got, result := decide(t, e, press(slackTeam, "U09", classApprovals[i], slackBranch, "approved")); got != http.StatusOK || result != "not_supported" {
			t.Errorf("%s: %d %s, want 200 not_supported", class, got, result)
		}
	}
	if n := w.checked(); n != 0 {
		t.Fatalf("a class the route never decides asked GitHub %d times", n)
	}
	if len(decisions) != 0 {
		t.Fatalf("a class the route never decides decided: %+v", decisions)
	}
}

// D14: off, the path is the mux's 404, as /mcp is when MCP is false.
func TestSlackDecideOff(t *testing.T) {
	e := setup(t, withSponsored(sponsoredP))
	body, err := json.Marshal(press(slackTeam, "U02", ap1, slackBranch, "approved"))
	if err != nil {
		t.Fatal(err)
	}
	if got, raw := postDecide(t, e, bearer("slack-app"), string(body)); got != http.StatusNotFound {
		t.Fatalf("off: %d %q", got, raw)
	}
}

// D14: every transport failure is its own status, and a store, room-reader or log failure is 503
// unavailable so the App may retry. The limiter is 10 a minute, burst 5, per (team,user), and its
// state is the instance's.
func TestSlackDecideUnavailable(t *testing.T) {
	t.Run("a read the route cannot make is 503 unavailable", func(t *testing.T) {
		for name, breakIt := range map[string]func(*memDecides){
			"the link table":   func(st *memDecides) { st.linkErr = errors.New("links down") },
			"the approval row": func(st *memDecides) { st.rowErr = errors.New("approvals down") },
			"the room state":   func(st *memDecides) { st.roomErr = errors.New("log down") },
			"the decision":     func(st *memDecides) { st.decideErr = errors.New("write failed") },
		} {
			t.Run(name, func(t *testing.T) {
				var decisions []decided
				st := slackDecideStore(slackLinks(), slackApprovals(), &decisions)
				breakIt(st)
				e := setup(t, withSponsored(sponsoredP), slackDecideRig(t, st))
				if got, result := decide(t, e, press(slackTeam, "U02", ap1, slackBranch, "approved")); got != http.StatusServiceUnavailable || result != "unavailable" {
					t.Fatalf("%d %s, want 503 unavailable", got, result)
				}
				if len(decisions) != 0 {
					t.Fatalf("an unreadable store decided: %+v", decisions)
				}
			})
		}
	})

	// The Room CR is the fourth read, and its failure is the same retryable status: the code says
	// nothing about what could not be read.
	t.Run("the Room CR cannot be read", func(t *testing.T) {
		var decisions []decided
		st := slackDecideStore(slackLinks(), slackApprovals(), &decisions)
		e := setup(t, withSponsored(sponsoredP), slackDecideRig(t, st),
			func(s *Server, _ *fanout.Hub, _ *hubView) {
				s.Rooms, s.SlackDecide.Rooms = brokenRooms{}, brokenRooms{}
			})
		if got, result := decide(t, e, press(slackTeam, "U02", ap1, slackBranch, "approved")); got != http.StatusServiceUnavailable || result != "unavailable" {
			t.Fatalf("%d %s, want 503 unavailable", got, result)
		}
	})

	t.Run("a body the route will not read is 400 bad_request", func(t *testing.T) {
		good, err := json.Marshal(press(slackTeam, "U02", ap1, slackBranch, "approved"))
		if err != nil {
			t.Fatal(err)
		}
		e := setup(t, withSponsored(sponsoredP), slackDecideOn(t, slackLinks(), slackApprovals(), &[]decided{}))
		for name, c := range map[string]struct {
			body  string
			ctype string
		}{
			// §1's cap is 4 KiB, and a body over it never reaches the decoder whole.
			"a 5 KiB body":      {body: string(good) + strings.Repeat(" ", 5<<10), ctype: "application/json"},
			"a text/plain body": {body: string(good), ctype: "text/plain"},
			"a body with an unknown key": {body: `{"team":"` + slackTeam + `","user":"U02","channel":"C0CC4M8SNVD",` +
				`"messageTS":"1730822400.123456","approvalId":"` + ap1 + `","branch":"` + slackBranch + `",` +
				`"decision":"approved","reason":"x"}`, ctype: "application/json"},
		} {
			t.Run(name, func(t *testing.T) {
				h := bearer("slack-app")
				h.Set("Content-Type", c.ctype)
				status, raw := postDecide(t, e, h, c.body)
				if got, result := answerOf(t, status, raw); got != http.StatusBadRequest || result != "bad_request" {
					t.Fatalf("%d %s, want 400 bad_request", got, result)
				}
			})
		}
	})

	t.Run("ten a minute, burst five, per (team,user), per instance", func(t *testing.T) {
		var decisions []decided
		st := slackDecideStore(slackLinks(), slackApprovals(), &decisions)
		e := setup(t, withSponsored(sponsoredP), slackDecideRig(t, st))
		for i := range 5 {
			if got, result := decide(t, e, press(slackTeam, "U02", ap1, slackBranch, "approved")); got != http.StatusOK || result != "approved" {
				t.Fatalf("press %d: %d %s, want 200 approved", i+1, got, result)
			}
		}
		if got, result := decide(t, e, press(slackTeam, "U02", ap1, slackBranch, "approved")); got != http.StatusTooManyRequests || result != "rate_limited" {
			t.Fatalf("the sixth press: %d %s, want 429 rate_limited", got, result)
		}
		// Five presses of one click are one decision: the burst is what the limiter took, not the log.
		if len(decisions) != 1 {
			t.Fatalf("the store recorded %+v, want one decision", decisions)
		}
		// The counter is the answer, per code: rooms_slack_decisions_total{result, decision}, the
		// decision empty when the answer carried none ([operations](../docs/operations.md)). A
		// replayed click counts as the decision it answered with, and one decision is in the log.
		body := scrape(t, e)
		for _, want := range []string{
			`maku_rooms_slack_decisions_total{decision="approved",result="approved"} 5`,
			`maku_rooms_slack_decisions_total{decision="",result="rate_limited"} 1`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("the counter holds no %s:\n%s", want, body)
			}
		}
		// No sleep proves the refill: the limiter's state is the SlackDecider's, so a fresh one over
		// the same store and the same fakes serves the next press at once.
		e2 := setup(t, withSponsored(sponsoredP), slackDecideRig(t, st))
		if got, result := decide(t, e2, press(slackTeam, "U02", ap1, slackBranch, "approved")); got != http.StatusOK || result != "approved" {
			t.Fatalf("a fresh decider: %d %s, want 200 approved", got, result)
		}
		// Another person's presses are their own: the key is the pair, not the route.
		if got, result := decide(t, e2, press(slackTeam, "U07", ap1, slackBranch, "approved")); got != http.StatusOK {
			t.Fatalf("another person after the burst: %d %s", got, result)
		}
	})
}

// brokenRooms is the room reader that cannot answer: the Room CR read fails as an API server that
// is down does, which the route may only answer as retryable.
type brokenRooms struct{}

func (brokenRooms) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("the API server is unavailable")
}

func (brokenRooms) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("the API server is unavailable")
}
