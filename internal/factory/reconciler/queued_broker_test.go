// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/bridgeapi"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/redact"
	"github.com/Smana/maku/internal/runwatch"
	"github.com/Smana/maku/internal/store"
)

// brokerQueue is the store's queue behind the broker's own handler, in memory: ConsumeQueued moves
// a queued row once and remembers the run, as the store does; err fails every consume.
type brokerQueue struct {
	rows  []store.Queued
	moved map[int64]string
	err   error
}

func (q *brokerQueue) Enqueue(context.Context, envelope.Draft, string, string) (envelope.Event, error) {
	return envelope.Event{}, errors.New("unused")
}

func (q *brokerQueue) Queue(context.Context, string) ([]store.Queued, error) {
	var out []store.Queued
	for _, r := range q.rows {
		if r.State == "queued" {
			out = append(out, r)
		}
	}
	return out, nil
}

func (q *brokerQueue) ConsumeQueued(_ context.Context, _ envelope.Draft, runID string, refs []int64) ([]int64, error) {
	if q.err != nil {
		return nil, q.err
	}
	var moved []int64
	for i := range q.rows {
		if q.rows[i].State == "queued" && slices.Contains(refs, q.rows[i].Ref) {
			q.rows[i].State, q.moved[q.rows[i].Ref] = "consumed", runID
			moved = append(moved, q.rows[i].Ref)
		}
	}
	return moved, nil
}

func (q *brokerQueue) Cursor(context.Context, string, string) (int64, error) { return 0, nil }

// factorySystem admits the bearer "factory-token" as system:factory.
type factorySystem struct{}

func (factorySystem) Authenticate(r *http.Request) (authn.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer factory-token" {
		return authn.Principal{}, authn.ErrUnauthenticated
	}
	return authn.Principal{Kind: envelope.ActorSystem, ID: "system:factory", Sub: "sub", Expiry: time.Now().Add(time.Hour)}, nil
}

type noLiveRuns struct{}

func (noLiveRuns) Live(string) (runwatch.Run, bool, bool) { return runwatch.Run{}, false, false }

// brokeredQueue is the rig's room log with the queue reached over the broker's system API: the
// real client against bridgeapi's own routes, so a consume is the call the live factory makes.
type brokeredQueue struct {
	*fakeLog
	c *rooms.Client
}

func (b brokeredQueue) Queue(ctx context.Context, room string) ([]rooms.Queued, error) {
	return b.c.Queue(ctx, room)
}

func (b brokeredQueue) Consume(ctx context.Context, room string, refs []int64, runID string) error {
	return b.c.Consume(ctx, room, refs, runID)
}

// throughBroker points g's queue at a broker serving q, and its log at the returned buffers: the
// factory's and the broker's.
func throughBroker(t *testing.T, g *rig, q *brokerQueue) (factoryLog, brokerLog *bytes.Buffer) {
	t.Helper()
	red, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	brokerLog, factoryLog = &bytes.Buffer{}, &bytes.Buffer{}
	srv := &bridgeapi.Server{Redactor: red, Runs: factorySystem{}, Systems: factorySystem{}, Watch: noLiveRuns{}, Queue: q,
		Logger: slog.New(slog.NewTextHandler(brokerLog, nil))}
	ts := httptest.NewTLSServer(srv.Routes())
	t.Cleanup(ts.Close)
	roots := x509.NewCertPool()
	roots.AddCert(ts.Certificate())
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("factory-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := rooms.New(ts.URL, tok, httpx.New(5*time.Second, roots), nil)
	if err != nil {
		t.Fatal(err)
	}
	g.r.Rooms = brokeredQueue{g.log, c}
	g.r.Log = slog.New(slog.NewTextHandler(factoryLog, nil))
	return factoryLog, brokerLog
}

// The live path of room ffogmecu (v0.54.8 drill): a note queued while the scoper runs is quoted by
// the implementer the approved plan starts, and that run consumes it through the broker's consume
// route, which both sides log.
func TestAnApprovedPlansImplementerConsumesThroughTheBroker(t *testing.T) {
	g, tk := planGateRig(t, "alice")
	q := &brokerQueue{moved: map[int64]string{}}
	factoryLog, brokerLog := throughBroker(t, g, q)
	tk = g.reconcile(t, tk.Name, 3)
	if len(tk.Status.Runs) != 1 || tk.Status.Runs[0].Role != "triager" {
		t.Fatalf("no scoper run: %+v", tk.Status.Runs)
	}
	q.rows = []store.Queued{{Ref: 8, Author: "human:alice", Text: "Keep the added sentence under 25 words.", State: "queued"}}
	tk = endScoperReady(g, t, tk, rid(0), nil)
	if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman {
		t.Fatalf("phase %s, want the plan gate", tk.Status.Phase)
	}
	appendComment(g, 7, forge.Comment{ID: 102, Author: "alice", Body: "/factory plan approve", At: now.Add(5 * time.Minute)})
	tk = g.reconcile(t, tk.Name, 2)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(tk.Status.Runs) != 2 {
		t.Fatalf("%s %+v", tk.Status.Phase, tk.Status.Runs)
	}
	if !strings.Contains(g.runs.specs[rid(1)].TaskText, "Queued message seq 8 by") {
		t.Fatalf("the implementer's brief does not quote the note:\n%s", g.runs.specs[rid(1)].TaskText)
	}
	if q.moved[8] != rid(1) {
		t.Fatalf("note 8 consumed by %q, want the implementer %s", q.moved[8], rid(1))
	}
	if !strings.Contains(factoryLog.String(), "queued notes consumed") || !strings.Contains(brokerLog.String(), "queued messages consumed") {
		t.Fatalf("consume not logged:\nfactory: %s\nbroker: %s", factoryLog, brokerLog)
	}
}

// A consume the broker refuses is logged with the refs it leaves queued, and the run still starts.
func TestAFailedConsumeIsLogged(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "# Fix the link"))
	q := &brokerQueue{moved: map[int64]string{}, err: errors.New("database down"),
		rows: []store.Queued{{Ref: 3, Author: "human:alice", Text: "Also fix the footer link.", State: "queued"}}}
	factoryLog, _ := throughBroker(t, g, q)
	tk := g.reconcile(t, "3buqdlot", 3)
	if tk.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("phase %s", tk.Status.Phase)
	}
	out := factoryLog.String()
	if !strings.Contains(out, "queue consume failed") || !strings.Contains(out, "refs=[3]") || !strings.Contains(out, "run.id="+rid(0)) {
		t.Fatalf("the failed consume is not logged with its refs:\n%s", out)
	}
	if q.rows[0].State != "queued" {
		t.Fatalf("note state %s, want still queued for the next brief", q.rows[0].State)
	}
}
