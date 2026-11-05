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

	// ap1 is the approval every case names; ap2 is the sealed room's; ap3 is ap1's row with no
	// binding; ap4 is ap1's with a run no watch holds; ap5 has the room's owner as a prompter.
	// apMissing is a well-formed ULID the log does not hold.
	ap1       = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	ap2       = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	ap3       = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	ap4       = "01ARZ3NDEKTSV4RRFFQ69G5FAY"
	ap5       = "01ARZ3NDEKTSV4RRFFQ69G5FAZ"
	apMissing = "01M4A1B2C3D4E5F6G7H8J9K0MN"
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

// slackApprovals is the rig's rows.
func slackApprovals() map[string]store.Approval {
	now := time.Now()
	row := func(id, room, run, binding string, prompters ...string) store.Approval {
		return store.Approval{ID: id, RoomID: room, RunID: run, EventID: "ev", CallID: "call-1", Class: slackDecidableClass,
			State: store.ApprovalPending, BindingBranch: binding, Prompters: prompters,
			RequestedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	}
	return map[string]store.Approval{
		ap1: row(ap1, roomID, slackRun, slackBranch),
		ap2: row(ap2, roomB, slackRun, slackBranch),
		ap3: row(ap3, roomID, slackRun, ""),
		ap4: row(ap4, roomID, "9zzzzzzz", slackBranch),
		ap5: row(ap5, roomID, slackRun, slackBranch, "human:own"),
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

// Decide is the store's fence as the tests need it: the first decision wins, an expired row is
// ErrApprovalExpired and a later decision ErrAlreadyDecided.
func (m *memDecides) Decide(_ context.Context, id, decision, by, reason string, d envelope.Draft) (envelope.Event, store.Approval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.decideErr != nil {
		return envelope.Event{}, store.Approval{}, m.decideErr
	}
	a, ok := m.rows[id]
	switch {
	case !ok:
		return envelope.Event{}, a, store.ErrNoApproval
	case a.State == store.ApprovalExpired:
		return envelope.Event{}, a, store.ErrApprovalExpired
	case a.State != store.ApprovalPending:
		return envelope.Event{}, a, store.ErrAlreadyDecided
	}
	a.State = decision
	m.rows[id] = a
	*m.decided = append(*m.decided, decided{by: by, decision: decision, reason: reason,
		originClient: d.OriginClient, seq: d.OriginSeq})
	return envelope.Event{V: 1, Seq: 1, RoomID: a.RoomID}, a, nil
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
	return func(s *Server, _ *fanout.Hub, v *hubView) {
		rooms := roomClient(t, slackRoomA(), repoRoom(roomB, "Smana/b"))
		// The run the approvals were requested for is live, on its branch: the binding is read from
		// the watch, never from the body (D6).
		if w, ok := s.Runs.(*runwatch.Watcher); ok {
			w.Upsert(t.Context(), slackRunClaim(slackRun, roomID, slackBranch))
		}
		d := &SlackDecider{
			Store: &memDecides{memLog: v.memLog, links: links, rows: aps, decided: decisions,
				sealed: map[string]bool{roomB: true}},
			Principal: "system:slack-app", TeamID: slackTeam, Rooms: rooms, Namespace: namespace,
			Runs: s.Runs, Metrics: s.Metrics, Logger: s.log(),
		}
		slackGitHub().gate(d)
		s.Rooms, s.SlackDecide = rooms, d
	}
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

// OD-16: four eyes hold on this surface too, comparing the row's prompters as the room page does.
func TestSlackDecideFourEyes(t *testing.T) {
	var decisions []decided
	e := setup(t, withSponsored(sponsoredP), slackDecideOn(t, slackLinks(), slackApprovals(), &decisions))
	if got, result := decide(t, e, press(slackTeam, "U02", ap5, slackBranch, "approved")); got != http.StatusOK || result != "four_eyes" {
		t.Fatalf("%d %s, want 200 four_eyes", got, result)
	}
	if len(decisions) != 0 {
		t.Fatalf("four eyes decided: %+v", decisions)
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
	e := setup(t, withSponsored(sponsoredP), slackDecideOn(t, slackLinks(), rows, &decisions),
		func(s *Server, _ *fanout.Hub, _ *hubView) { w.gate(s.SlackDecide) })
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
