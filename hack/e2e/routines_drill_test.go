// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/oauth2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/app"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/bridgeapi"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/api"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/intake"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/reconciler"
	"github.com/Smana/maku/internal/factory/resources"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/factory/taskid"
	"github.com/Smana/maku/internal/factory/triage"
	"github.com/Smana/maku/internal/factory/upkeep"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/humanapi"
	"github.com/Smana/maku/internal/maku"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/redact"
	"github.com/Smana/maku/internal/runrequest"
	"github.com/Smana/maku/internal/runwatch"
	"github.com/Smana/maku/internal/slackapp"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/webhook"
)

const (
	drillNS   = "agent-system"
	drillRepo = "owner/repo"
)

type memRoom struct {
	events   []envelope.Event
	cursors  map[string]int64
	seenKeys map[string]int64
	queue    []store.Queued
	consumed map[int64]string
	holder   string
	sealed   bool
}

// memBroker implements bridgeapi.Log, bridgeapi.QueueStore, and webhook.LogReader in memory.
type memBroker struct {
	mu    sync.Mutex
	cl    client.Reader
	rooms map[string]*memRoom
	subs  []func(roomID string, seq int64)
	now   func() time.Time
}

func newMemBroker(cl client.Reader, now func() time.Time) *memBroker {
	return &memBroker{cl: cl, rooms: map[string]*memRoom{}, now: now}
}

func (b *memBroker) ensureLocked(ctx context.Context, roomID string) (*memRoom, bool) {
	if rm, ok := b.rooms[roomID]; ok {
		return rm, true
	}
	if b.cl != nil {
		var cr roomv1.Room
		if err := b.cl.Get(ctx, types.NamespacedName{Namespace: drillNS, Name: roomID}, &cr); err == nil {
			rm := &memRoom{cursors: map[string]int64{}, seenKeys: map[string]int64{}, consumed: map[int64]string{}}
			b.rooms[roomID] = rm
			return rm, true
		}
	}
	return nil, false
}

func (b *memBroker) Append(ctx context.Context, d envelope.Draft) (envelope.Event, bool, error) {
	b.mu.Lock()
	rm, ok := b.ensureLocked(ctx, d.RoomID)
	if !ok {
		rm = &memRoom{cursors: map[string]int64{}, seenKeys: map[string]int64{}, consumed: map[int64]string{}}
		b.rooms[d.RoomID] = rm
	}
	if rm.sealed {
		b.mu.Unlock()
		return envelope.Event{}, false, store.ErrSealed
	}
	var key string
	if d.OriginClient != "" && d.OriginSeq > 0 {
		key = fmt.Sprintf("%s/%d", d.OriginClient, d.OriginSeq)
		if seq, seen := rm.seenKeys[key]; seen {
			var found envelope.Event
			for _, ev := range rm.events {
				if ev.Seq == seq {
					found = ev
					break
				}
			}
			b.mu.Unlock()
			return found, true, nil
		}
	}
	seq := int64(len(rm.events) + 1)
	ev := envelope.Event{
		V:          envelope.Version,
		ID:         fmt.Sprintf("ev%06d", seq),
		Seq:        seq,
		RoomID:     d.RoomID,
		RunID:      d.RunID,
		TS:         b.now().UTC(),
		Type:       d.Type,
		Origin:     d.Origin,
		Actor:      d.Actor,
		Redactions: d.Redactions,
		Payload:    d.Payload,
	}
	rm.events = append(rm.events, ev)
	if key != "" {
		rm.seenKeys[key] = seq
		if d.OriginSeq > rm.cursors[d.OriginClient] {
			rm.cursors[d.OriginClient] = d.OriginSeq
		}
	}
	subs := slices.Clone(b.subs)
	b.mu.Unlock()

	for _, fn := range subs {
		fn(d.RoomID, seq)
	}
	return ev, false, nil
}

func (b *memBroker) AppendAsBridge(ctx context.Context, bridgeRun string, d envelope.Draft) (envelope.Event, bool, error) {
	b.mu.Lock()
	rm, ok := b.ensureLocked(ctx, d.RoomID)
	if !ok {
		b.mu.Unlock()
		return envelope.Event{}, false, store.ErrNoRoom
	}
	if rm.holder != "" && rm.holder != bridgeRun {
		b.mu.Unlock()
		return envelope.Event{}, false, store.ErrLeaseLost
	}
	b.mu.Unlock()
	return b.Append(ctx, d)
}

func (b *memBroker) Range(ctx context.Context, roomID string, afterSeq int64, limit int) ([]envelope.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rm, ok := b.ensureLocked(ctx, roomID)
	if !ok {
		return nil, store.ErrNoRoom
	}
	var out []envelope.Event
	for _, ev := range rm.events {
		if ev.Seq > afterSeq {
			out = append(out, ev)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (b *memBroker) Deliveries(ctx context.Context, roomID, _ string, after, through int64, limit int) ([]envelope.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rm, ok := b.ensureLocked(ctx, roomID)
	if !ok {
		return nil, store.ErrNoRoom
	}
	var out []envelope.Event
	for _, ev := range rm.events {
		if ev.Seq > after && ev.Seq <= through {
			out = append(out, ev)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (b *memBroker) Cursor(ctx context.Context, roomID, originClient string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rm, ok := b.ensureLocked(ctx, roomID)
	if !ok {
		return 0, store.ErrNoRoom
	}
	return rm.cursors[originClient], nil
}

func (b *memBroker) Room(ctx context.Context, id string) (store.RoomState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rm, ok := b.ensureLocked(ctx, id)
	if !ok {
		return store.RoomState{}, store.ErrNoRoom
	}
	return store.RoomState{
		ID:      id,
		LastSeq: int64(len(rm.events)),
		Driver:  "system:factory",
		Sealed:  rm.sealed,
	}, nil
}

func (b *memBroker) ClaimBridge(ctx context.Context, roomID, runID string, _ func(context.Context, string) bool) (string, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rm, ok := b.ensureLocked(ctx, roomID)
	if !ok {
		return "", false, store.ErrNoRoom
	}
	if rm.holder != "" && rm.holder != runID {
		return rm.holder, false, nil
	}
	rm.holder = runID
	return runID, true, nil
}

func (b *memBroker) TouchBridge(ctx context.Context, roomID, runID string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rm, ok := b.ensureLocked(ctx, roomID)
	if !ok {
		return false, store.ErrNoRoom
	}
	return rm.holder == runID, nil
}

func (b *memBroker) Enqueue(ctx context.Context, d envelope.Draft, author, text string) (envelope.Event, error) {
	ev, dup, err := b.Append(ctx, d)
	if err != nil {
		return envelope.Event{}, err
	}
	if !dup {
		b.mu.Lock()
		rm, ok := b.ensureLocked(ctx, d.RoomID)
		if ok {
			rm.queue = append(rm.queue, store.Queued{Ref: ev.Seq, Author: author, Text: text, State: "queued"})
		}
		b.mu.Unlock()
	}
	return ev, nil
}

func (b *memBroker) Queue(ctx context.Context, roomID string) ([]store.Queued, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rm, ok := b.ensureLocked(ctx, roomID)
	if !ok {
		return nil, store.ErrNoRoom
	}
	var out []store.Queued
	for _, q := range rm.queue {
		if _, gone := rm.consumed[q.Ref]; !gone {
			out = append(out, q)
		}
	}
	return out, nil
}

func (b *memBroker) ConsumeQueued(ctx context.Context, d envelope.Draft, runID string, refs []int64) ([]int64, error) {
	b.mu.Lock()
	rm, ok := b.ensureLocked(ctx, d.RoomID)
	if !ok {
		b.mu.Unlock()
		return nil, store.ErrNoRoom
	}
	var moved []int64
	for _, ref := range refs {
		if _, gone := rm.consumed[ref]; !gone {
			rm.consumed[ref] = runID
			moved = append(moved, ref)
		}
	}
	b.mu.Unlock()
	return moved, nil
}

func (b *memBroker) Listen(ctx context.Context, ready func(), fn func(roomID string, seq int64)) error {
	b.mu.Lock()
	b.subs = append(b.subs, fn)
	b.mu.Unlock()
	if ready != nil {
		ready()
	}
	<-ctx.Done()
	return nil
}

func (b *memBroker) AppendRunEnd(roomID, runID, phase, reason string) {
	_, _, _ = b.Append(context.Background(), envelope.Draft{
		RoomID:  roomID,
		RunID:   runID,
		Type:    envelope.StateChanged,
		Origin:  envelope.OriginBroker,
		Actor:   envelope.Actor{Kind: envelope.ActorSystem, ID: "system:room-broker"},
		Payload: envelope.StatePayload("run_phase", map[string]any{"phase": phase, "reason": reason}),
	})
}

func (b *memBroker) AppendHandoff(roomID, runID, fromRole, toRole, commit, summary string) {
	_, _, _ = b.Append(context.Background(), envelope.Draft{
		RoomID:  roomID,
		RunID:   runID,
		Type:    envelope.Handoff,
		Origin:  envelope.OriginClient,
		Actor:   envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + runID, Role: fromRole},
		Payload: envelope.Must(envelope.HandoffPayload{FromRole: fromRole, ToRole: toRole, Commit: commit, Summary: summary}),
	})
}

func (b *memBroker) AppendVerdict(roomID, runID, verdict, commit, text string) {
	_, _, _ = b.Append(context.Background(), envelope.Draft{
		RoomID:  roomID,
		RunID:   runID,
		Type:    envelope.Message,
		Origin:  envelope.OriginClient,
		Actor:   envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + runID, Role: "reviewer"},
		Payload: envelope.Must(envelope.MessagePayload{Kind: envelope.KindReviewVerdict, Verdict: verdict, Commit: commit, Text: text, Delivery: envelope.DeliveryNone}),
	})
}

// memWebhookState implements webhook.StateStore in memory.
type memWebhookState struct {
	mu       sync.Mutex
	failures map[string]int
	disabled map[string]bool
	cursors  map[[2]string]int64
}

func newMemWebhookState() *memWebhookState {
	return &memWebhookState{
		failures: map[string]int{},
		disabled: map[string]bool{},
		cursors:  map[[2]string]int64{},
	}
}

func (s *memWebhookState) Subscriber(_ context.Context, sub string, _ int) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures[sub], s.disabled[sub], nil
}

func (s *memWebhookState) WebhookCursor(_ context.Context, sub, room string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[[2]string{sub, room}], nil
}

func (s *memWebhookState) SetWebhookCursor(_ context.Context, sub, room string, seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[[2]string{sub, room}] = seq
	return nil
}

func (s *memWebhookState) Lagging(_ context.Context, _ string, _ int) ([]string, error) {
	return nil, nil
}

func (s *memWebhookState) Failed(_ context.Context, sub string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[sub]++
	return s.failures[sub], nil
}

func (s *memWebhookState) Succeeded(_ context.Context, sub string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[sub] = 0
	return nil
}

func (s *memWebhookState) Disable(_ context.Context, sub string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disabled[sub] = true
	return nil
}

// drillRuns implements reconciler.RunClient in memory.
type drillRuns struct {
	mu    sync.Mutex
	specs map[string]runs.Spec
	runs  map[string]runs.Run
	now   func() time.Time
}

var drillAgentBranch = regexp.MustCompile(`^agent/[a-z0-9][a-z0-9._/-]{0,100}$`)

func newDrillRuns(now func() time.Time) *drillRuns {
	return &drillRuns{specs: map[string]runs.Spec{}, runs: map[string]runs.Run{}, now: now}
}

func (d *drillRuns) Create(_ context.Context, s runs.Spec) error {
	if s.Branch != "" && !drillAgentBranch.MatchString(s.Branch) {
		return apierrors.NewInvalid(schema.GroupKind{Group: "maku.ogenki.io", Kind: "AgentRun"}, s.RunID, field.ErrorList{
			field.Invalid(field.NewPath("spec", "branch"), s.Branch, "invalid branch"),
		})
	}
	if err := s.Validate(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.specs[s.RunID] = s
	d.runs[s.RunID] = runs.Run{
		ID: s.RunID, TaskID: s.TaskID, Role: s.Role, Principal: s.Principal,
		ChargedTo: s.ChargedTo, RoomRef: s.RoomRef, Phase: "Pending",
		MaxTokens: s.MaxTokens, StartSeq: s.StartSeq, Head: s.Head,
		Created: d.now(), TaskText: s.TaskText,
	}
	return nil
}

func (d *drillRuns) Get(_ context.Context, id string) (runs.Run, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.runs[id]
	return r, ok, nil
}

func (d *drillRuns) List(context.Context) ([]runs.Run, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]runs.Run, 0, len(d.runs))
	for _, r := range d.runs {
		out = append(out, r)
	}
	return out, nil
}

func (d *drillRuns) Annotate(context.Context, string, map[string]string) error { return nil }

func (d *drillRuns) Delete(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.runs, id)
	return nil
}

func (d *drillRuns) set(id, phase string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[id]
	r.Phase = phase
	d.runs[id] = r
}

func (d *drillRuns) spec(id string) (runs.Spec, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.specs[id]
	return s, ok
}

type slackPost struct {
	Method   string
	Channel  string
	TS       string
	ThreadTS string
	Text     string
}

type fakeSlackAPI struct {
	mu      sync.Mutex
	seq     int
	posts   []slackPost
	updates []slackPost
	srv     *httptest.Server
}

func newFakeSlackAPI(t *testing.T) *fakeSlackAPI {
	t.Helper()
	f := &fakeSlackAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		var payload struct {
			Channel  string `json:"channel"`
			TS       string `json:"ts"`
			ThreadTS string `json:"thread_ts"`
			Text     string `json:"text"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case "chat.postMessage":
			f.mu.Lock()
			f.seq++
			ts := fmt.Sprintf("1732000%03d.0001", f.seq)
			f.posts = append(f.posts, slackPost{
				Method:   "chat.postMessage",
				Channel:  payload.Channel,
				TS:       ts,
				ThreadTS: payload.ThreadTS,
				Text:     payload.Text,
			})
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": ts})
		case "chat.update":
			f.mu.Lock()
			f.updates = append(f.updates, slackPost{
				Method:  "chat.update",
				Channel: payload.Channel,
				TS:      payload.TS,
				Text:    payload.Text,
			})
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": payload.TS})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "messages": []any{}})
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlackAPI) snapshot() ([]slackPost, []slackPost) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.posts), slices.Clone(f.updates)
}

type drillAuth struct {
	principals map[string]authn.Principal
}

func (a drillAuth) Authenticate(r *http.Request) (authn.Principal, error) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p, ok := a.principals[tok]
	if !ok {
		return authn.Principal{}, authn.ErrUnauthenticated
	}
	return p, nil
}

type drillSponsoredAuth struct {
	principals map[string]authn.Principal
}

func (a drillSponsoredAuth) Authenticate(_ context.Context, raw string) (authn.Principal, error) {
	p, ok := a.principals[raw]
	if !ok {
		return authn.Principal{}, authn.ErrUnauthenticated
	}
	return p, nil
}

type drillLogins struct {
	logins map[string]string
}

func (l drillLogins) Login(_ context.Context, sub, _ string) (string, error) {
	return l.logins[sub], nil
}

type drillAccess struct{}

func (drillAccess) CanRead(context.Context, authn.Principal, string) (bool, error)  { return true, nil }
func (drillAccess) CanWrite(context.Context, authn.Principal, string) (bool, error) { return true, nil }
func (drillAccess) CanWriteLogin(context.Context, string, string) (bool, error)     { return true, nil }

type drillWatch struct{}

func (drillWatch) Live(string) (runwatch.Run, bool, bool) { return runwatch.Run{}, false, false }

type drillRoutineMetrics struct{}

func (drillRoutineMetrics) RoutineRun(context.Context, string, string, string, float64) {}
func (drillRoutineMetrics) RoutinePaused(context.Context, string, string, bool)         {}
func (drillRoutineMetrics) RoutineMergeRate(context.Context, string, string, float64)   {}
func (drillRoutineMetrics) RoutineSkip(context.Context, string, string, string)         {}
func (drillRoutineMetrics) RoutinePromptRead(context.Context, string, string, string)   {}

func drillPool(certs ...*x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return pool
}

func eventually(t *testing.T, timeout time.Duration, desc string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if fn() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", desc)
		}
		select {
		case <-t.Context().Done():
			t.Fatalf("context cancelled waiting for %s", desc)
		case <-ticker.C:
		}
	}
}

func doJSONReq(t *testing.T, hc *http.Client, method, url, token, origin string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, url, err)
	}
	return resp.StatusCode, out
}

func drillScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := factoryv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := roomv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

type drillHarness struct {
	t         *testing.T
	cl        client.Client
	fg        *forgetest.Fake
	runs      *drillRuns
	memBroker *memBroker
	slackAPI  *fakeSlackAPI
	slackApp  *slackapp.App
	bindings  *upkeep.BindingsStore
	state     *upkeep.StateStore
	breaker   *upkeep.Breaker
	src       *configsource.Source
	sched     *intake.Scheduler
	rec       *reconciler.Reconciler
	humanTS   *httptest.Server
	factoryTS *httptest.Server
	bridgeTS  *httptest.Server
	roomsCli  *rooms.Client

	mu  sync.Mutex
	cur time.Time
}

func (h *drillHarness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur
}

func (h *drillHarness) setNow(tm time.Time) {
	h.mu.Lock()
	h.cur = tm.UTC()
	h.mu.Unlock()
}

func (h *drillHarness) advance(d time.Duration) {
	h.mu.Lock()
	h.cur = h.cur.Add(d)
	h.mu.Unlock()
}

func (h *drillHarness) reconcile(taskName string, times int) *factoryv1.Task {
	h.t.Helper()
	for range times {
		if _, err := h.rec.Reconcile(h.t.Context(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: drillNS, Name: taskName},
		}); err != nil {
			h.t.Fatalf("reconcile %s: %v", taskName, err)
		}
	}
	var tk factoryv1.Task
	if err := h.cl.Get(h.t.Context(), types.NamespacedName{Namespace: drillNS, Name: taskName}, &tk); err != nil {
		h.t.Fatalf("get task %s: %v", taskName, err)
	}
	return &tk
}

func newDrillHarness(t *testing.T) *drillHarness {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	startAt := time.Date(2026, 11, 23, 3, 0, 10, 0, time.UTC) // Monday 03:00:10 UTC
	h := &drillHarness{t: t, cur: startAt}

	factoryYAML := []byte(`
factory: prod
factoryLogin: ogenki-agent-factory[bot]
agentsLogin: ogenki-agents[bot]
roomsURL: https://rooms.example.com
broker:
  url: https://room-broker.example.com:8443
  caFile: /etc/ca.crt
  tokenFile: /etc/tok
github:
  appIDFile: /etc/app-id
  privateKeyFile: /etc/app-key
  mergerAppIDFile: /etc/app-id
  mergerKeyFile: /etc/app-key
poll:
  issues: 60s
  tasks: 30s
  meter: 30s
defaults:
  dataClass: internal
  predictedClass: docs-links
triage:
  controlPercent: 0
tiers:
  light:
    model: tier-light
    runMinutes: 30
    runTokens: 500000
    taskTokens: 1000000
    runUSD: 0.50
    taskUSD: 1.00
  standard:
    model: agent-default
    runMinutes: 45
    runTokens: 1500000
    taskTokens: 3000000
    runUSD: 2.55
    taskUSD: 5.10
  frontier:
    model: tier-frontier
    runMinutes: 90
    runTokens: 4000000
    taskTokens: 8000000
    runUSD: 6.80
    taskUSD: 13.60
templates:
  solo:
    roles: [implementer]
  pair:
    roles: [implementer, reviewer]
    maxReviewRounds: 2
  investigate:
    roles: [triager]
caps:
  activeTasks: 5
  concurrentRuns: 5
  tasksPerDay: 20
  maxTextBytes: 14336
  awaitingHumanWIP: 5
  upkeepAwaitingHumanWIP: 3
budgets:
  enforceTask: true
  factoryDailyUSD: 50.00
  upkeepDailyUSD: 10.00
  humanDailyUSD: 10.00
meter:
  url: http://meter.example.local:8428
  query: sum(usage)
  throttle:
    promQuery: sum(rate_limit)
  prices:
    agent-default:
      input: 1.40
      cacheRead: 0.26
      output: 4.40
    tier-light:
      input: 0.50
      cacheRead: 0.10
      output: 1.50
    tier-frontier:
      input: 3.00
      cacheRead: 0.50
      output: 10.00
routines:
  uiWrite: true
api:
  listen: ":8082"
  humanIssuer: https://idp.example.com
  humanJWKS: https://idp.example.com/jwks
  projectIDFile: /etc/project-id
  clientIDFiles: [/etc/cid]
  access:
    links: broker
    ttl: 1m
  sponsored:
    - name: slack-app
      clientIDFile: /etc/slack-cid
      sponsor: alice
      repositories: [owner/repo]
      dailyUSD: 5.00
      maxCostUSD: 2.00
repositories: []
`)

	repoObj := &factoryv1.Repository{
		ObjectMeta: metav1.ObjectMeta{Name: "owner-repo", Namespace: drillNS, Generation: 1},
		Spec: factoryv1.RepositorySpec{
			FactoryRef:   "prod",
			Repository:   drillRepo,
			Maintainers:  []string{"alice"},
			TriggerLabel: "factory/ready",
			MergePolicies: []config.Binding{
				{Name: "docs-links", Mode: config.ModeLive},
			},
			Merge: &config.Merge{
				RequiredChecks: []string{"ci/test", "leak-scan"},
				VerifyChecks:   []string{"ci/test", "leak-scan"},
				LeakScanCheck:  "leak-scan",
				MergerLogin:    "ogenki-agent-factory[bot]",
				MaxPerDay:      10,
				FixRuns:        1,
				VerifyFor:      config.Duration{Duration: 30 * time.Minute},
				RevertWindow:   config.Duration{Duration: 168 * time.Hour},
				ApproveToMerge: true,
				Breaker:        config.Breaker{Window: 10, MaxReverts: 1},
			},
			Routines: []config.RoutineBinding{
				{Name: "r-trip", Mode: config.RoutineModeDraft, Cron: "0 3 * * *", Skill: "upkeep-audit"},
			},
		},
	}

	mpObj := &factoryv1.MergePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "docs-links", Namespace: drillNS, Generation: 1},
		Spec: factoryv1.MergePolicySpec{
			AllowedFiles:     []string{`^docs/.+\.md$`},
			MaxPatchFiles:    5,
			MaxModifiedLines: 100,
			DiffShape:        "links-only",
		},
	}

	rtPropose := &factoryv1.Routine{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r-propose", Namespace: drillNS, Generation: 1,
			Labels: map[string]string{"maku.ogenki.io/catalog": "true"},
		},
		Spec: factoryv1.RoutineSpec{RoutineSpec: config.RoutineSpec{
			Description:   "Audit docs for drift.",
			Workflow:      "quick",
			DataClass:     "public",
			DefaultPrompt: "## Goal\nAudit docs.\n\n## Verify\n- Review docs diff\n",
			Probe:         config.ProbeSpec{Kind: config.ProbeNone},
			MaxOpenPRs:    2,
			MaxCostUSD:    2.00,
		}},
	}

	rtDraft := &factoryv1.Routine{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r-draft", Namespace: drillNS, Generation: 1,
			Labels: map[string]string{"maku.ogenki.io/catalog": "true"},
		},
		Spec: factoryv1.RoutineSpec{RoutineSpec: config.RoutineSpec{
			Description:   "Fix dead doc links in draft PRs.",
			Workflow:      "standard",
			DataClass:     "public",
			DefaultPrompt: "## Goal\nFix links.\n\n## Verify\n- Verify links\n",
			Probe:         config.ProbeSpec{Kind: config.ProbeNone},
			MaxOpenPRs:    2,
			MaxCostUSD:    2.00,
		}},
	}

	rtTrip := &factoryv1.Routine{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r-trip", Namespace: drillNS, Generation: 1,
			Labels: map[string]string{"maku.ogenki.io/catalog": "true"},
		},
		Spec: factoryv1.RoutineSpec{RoutineSpec: config.RoutineSpec{
			Description:   "Trip test routine.",
			Workflow:      "quick",
			DataClass:     "public",
			DefaultPrompt: "## Goal\nClean docs.\n\n## Verify\n- Check docs\n",
			Probe:         config.ProbeSpec{Kind: config.ProbeNone},
			MaxOpenPRs:    2,
			MaxCostUSD:    2.00,
			Breaker: config.BreakerSpec{
				Window:                 10,
				MinSamples:             5,
				MaxConsecutiveClosed:   3,
				MaxConsecutiveFailures: 3,
			},
		}},
	}

	s := drillScheme(t)
	h.cl = fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&factoryv1.Task{}, &roomv1.Room{}).
		WithObjects(repoObj, mpObj, rtPropose, rtDraft, rtTrip).
		Build()

	if err := ledger.StampMeter(t.Context(), h.cl, drillNS, startAt, 2000*time.Hour); err != nil {
		t.Fatal(err)
	}

	h.fg = forgetest.NewFake()
	h.fg.Author = "ogenki-agents[bot]"
	h.fg.Now = h.now
	h.fg.SetFile(".github/agent-factory/upkeep/r-propose.md", "## Scope\nAudit docs.\n\n## Verify\n- Verify docs accuracy\n")
	h.fg.SetFile(".github/agent-factory/upkeep/r-draft.md", "## Scope\nFix broken doc links.\n\n## Verify\n- Check markdown links\n")
	h.fg.SetFile(".github/agent-factory/upkeep/r-trip.md", "## Scope\nClean up docs.\n\n## Verify\n- Run doc check\n")
	h.fg.SetFile(".agents/skills/upkeep-audit/SKILL.md", "# Upkeep Audit Skill\nVerify links and references before opening PRs.\n")

	h.bindings = &upkeep.BindingsStore{Client: h.cl, Reader: h.cl, Namespace: drillNS, Now: h.now, Log: slog.New(slog.DiscardHandler)}
	h.state = &upkeep.StateStore{Client: h.cl, Reader: h.cl, Namespace: drillNS, Now: h.now, Log: slog.New(slog.DiscardHandler)}
	h.breaker = &upkeep.Breaker{Store: h.state, Now: h.now, Log: slog.New(slog.DiscardHandler)}

	h.src = &configsource.Source{
		Reader:    h.cl,
		Namespace: drillNS,
		File:      factoryYAML,
		Log:       slog.New(slog.DiscardHandler),
		Now:       h.now,
	}
	if err := h.src.Sync(t.Context()); err != nil {
		out, synced := h.src.Outcomes()
		t.Fatalf("initial configsource.Sync: %v (outcomes=%+v synced=%+v)", err, out, synced)
	}

	red, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}

	whKey := bytes.Repeat([]byte{0x42}, 32)
	whKeyFile := filepath.Join(t.TempDir(), "wh.key")
	if err := os.WriteFile(whKeyFile, []byte(base64.StdEncoding.EncodeToString(whKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "broker.tok")
	if err := os.WriteFile(tokenFile, []byte("factory-sa-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	h.memBroker = newMemBroker(h.cl, h.now)
	h.slackAPI = newFakeSlackAPI(t)

	var slackRef *slackapp.App
	slackWH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackRef.Webhooks().ServeHTTP(w, r)
	}))
	t.Cleanup(slackWH.Close)

	emCtx, emCancel := context.WithCancel(t.Context())
	t.Cleanup(emCancel)

	emitter := &webhook.Emitter{
		Subs: []webhook.Subscriber{{
			Name:         "slack-app",
			Network:      "cluster",
			URL:          slackWH.URL + "/webhooks",
			SecretFile:   whKeyFile,
			Events:       []string{"room.task_triaged", "room.waiting_for_human", "room.pr_opened", "room.merged", "room.ended", "routine.paused"},
			Repositories: []string{drillRepo},
		}},
		Events: h.memBroker,
		Rooms: func(ctx context.Context, roomID string) (string, error) {
			var rm roomv1.Room
			if err := h.cl.Get(ctx, types.NamespacedName{Namespace: drillNS, Name: roomID}, &rm); err != nil {
				if apierrors.IsNotFound(err) {
					return "", nil
				}
				return "", err
			}
			return rm.Spec.Repository, nil
		},
		State:            newMemWebhookState(),
		Logger:           slog.New(slog.DiscardHandler),
		Now:              h.now,
		SweepEvery:       10 * time.Millisecond,
		MinBackoff:       10 * time.Millisecond,
		FactoryPrincipal: "system:factory",
	}

	bridgeSrv := &bridgeapi.Server{
		Log:           h.memBroker,
		Queue:         h.memBroker,
		Redactor:      red,
		Runs:          drillAuth{},
		Systems:       drillAuth{principals: map[string]authn.Principal{"factory-sa-token": {Kind: envelope.ActorSystem, ID: "system:factory"}}},
		Watch:         drillWatch{},
		Limits:        bridgeapi.Limits{Rate: 1000, Burst: 1000, InFlight: 32},
		RoutinePaused: emitter,
		Now:           h.now,
		Logger:        slog.New(slog.DiscardHandler),
	}
	h.bridgeTS = httptest.NewTLSServer(bridgeSrv.Routes())
	t.Cleanup(h.bridgeTS.Close)

	h.roomsCli, err = rooms.New(
		h.bridgeTS.URL,
		tokenFile,
		httpx.New(5*time.Second, drillPool(h.bridgeTS.Certificate())),
		noop.NewTracerProvider(),
	)
	if err != nil {
		t.Fatal(err)
	}

	onPaused := func(ctx context.Context, repository, routine, reason string) {
		if err := h.roomsCli.RoutinePaused(ctx, repository, routine, reason); err != nil {
			t.Errorf("roomsCli.RoutinePaused(%s, %s): %v", repository, routine, err)
		}
	}

	h.sched = &intake.Scheduler{
		Clients: func(repo string) (intake.ScheduleForge, intake.ScheduleMerger, bool) {
			if !strings.EqualFold(repo, drillRepo) {
				return nil, nil, false
			}
			return h.fg, h.fg, true
		},
		Client:          h.cl,
		APIReader:       h.cl,
		Ledger:          h.cl,
		State:           h.state,
		Breaker:         h.breaker,
		RoutineMetrics:  drillRoutineMetrics{},
		OnRoutinePaused: onPaused,
		Namespace:       drillNS,
		Cfg:             h.src.Live,
		Owned:           func(repo string) bool { return strings.EqualFold(repo, drillRepo) },
		Stopped:         func(context.Context) bool { return false },
		Now:             h.now,
		Nonce:           func() string { return "n0nce001" },
		Log:             slog.New(slog.DiscardHandler),
	}

	fm, err := fmetrics.New(nil, nil, drillNS, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	h.runs = newDrillRuns(h.now)
	h.rec = &reconciler.Reconciler{
		Client:          h.cl,
		Ledger:          h.cl,
		Namespace:       drillNS,
		Cfg:             h.src.Live,
		Forge:           h.fg,
		Merger:          h.fg,
		Owned:           func(repo string) bool { return strings.EqualFold(repo, drillRepo) },
		CanWrite:        func(context.Context, string, string) (bool, error) { return true, nil },
		Runs:            h.runs,
		Kind:            runwatch.DefaultKind,
		Rooms:           h.roomsCli,
		Triage:          triage.Classify{},
		Metrics:         fm,
		RoutineMetrics:  drillRoutineMetrics{},
		RoutineBreaker:  h.breaker,
		OnRoutinePaused: onPaused,
		Now:             h.now,
		Nonce:           func() string { return "n0nce001" },
		Log:             slog.New(slog.DiscardHandler),
		Redact:          red,
	}

	farExpiry := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	fAPISrv := &api.Server{
		Auth: drillAuth{principals: map[string]authn.Principal{
			"maint": {Kind: envelope.ActorHuman, ID: "human:1", Sub: "sub-alice", Groups: []string{"agents-member"}, AccessToken: "maint", Expiry: farExpiry},
			"slack": {Kind: envelope.ActorSystem, ID: "system:slack-app", Sponsored: true, Sub: "sub-slack", AccessToken: "slack", Expiry: farExpiry},
		}},
		Groups:          policy.Groups{Member: "agents-member", Admin: "agents-admin"},
		Cfg:             h.src.Live,
		Rooms:           h.cl,
		APIReader:       h.cl,
		Tasks:           h.cl,
		Namespace:       drillNS,
		Stopped:         func(context.Context) bool { return false },
		Now:             h.now,
		Metrics:         fm,
		Owned:           func(repo string) bool { return strings.EqualFold(repo, drillRepo) },
		Access:          drillAccess{},
		Logins:          drillLogins{logins: map[string]string{"sub-alice": "alice"}},
		RoutineState:    h.state,
		RoutineBindings: h.bindings,
		RoutineTrigger:  h.sched,
		ConfigKick:      func() { _ = h.src.Sync(t.Context()) },
		RoutinePrompts: func(repo string) upkeep.DefaultBranchReader {
			if strings.EqualFold(repo, drillRepo) {
				return h.fg
			}
			return nil
		},
		Log: slog.New(slog.DiscardHandler),
	}
	h.factoryTS = httptest.NewTLSServer(fAPISrv.Handler())
	t.Cleanup(h.factoryTS.Close)

	humanSrv := &humanapi.Server{
		Humans: drillAuth{principals: map[string]authn.Principal{
			"maint": {Kind: envelope.ActorHuman, ID: "human:1", Sub: "sub-alice", Groups: []string{"agents-member"}, AccessToken: "maint", Expiry: farExpiry},
		}},
		Sponsored: drillSponsoredAuth{principals: map[string]authn.Principal{
			"slack": {Kind: envelope.ActorSystem, ID: "system:slack-app", Sponsored: true, AccessToken: "slack", Expiry: farExpiry},
		}},
		SponsoredRepositories: map[string]map[string]bool{
			"system:slack-app": {drillRepo: true},
		},
		Now:       h.now,
		Groups:    policy.Groups{Member: "agents-member", Admin: "agents-admin"},
		PublicURL: "https://rooms.example.com",
		Routines: &runrequest.Factory{
			URL: h.factoryTS.URL,
			HC:  httpx.New(5*time.Second, drillPool(h.factoryTS.Certificate())),
		},
		Logger: slog.New(slog.DiscardHandler),
	}
	h.humanTS = httptest.NewServer(humanSrv.Routes())
	t.Cleanup(h.humanTS.Close)

	h.slackApp = &slackapp.App{
		Slack: &slackapp.SlackClient{
			BaseURL: h.slackAPI.srv.URL + "/",
			Token:   func() string { return "xoxb-test" },
			HC:      h.slackAPI.srv.Client(),
		},
		Broker: slackapp.Broker{
			URL: h.humanTS.URL,
			HC:  h.humanTS.Client(),
			Tokens: &slackapp.Tokens{
				Fetch: func(context.Context) (*oauth2.Token, error) {
					return &oauth2.Token{AccessToken: "slack", Expiry: h.now().Add(time.Hour)}, nil
				},
			},
		},
		Threads:     slackapp.ConfigMapThreads{C: h.cl, Namespace: drillNS, Now: h.now},
		WebhookKeys: func() ([][]byte, error) { return [][]byte{whKey}, nil },
		TeamID:      "T01DRILL",
		BotUserID:   "U0BOT",
		RoomsURL:    "https://rooms.example.com",
		Channels: map[string]slackapp.Channel{
			"C03UP": {
				Upkeep: &slackapp.UpkeepChannel{
					Repositories: []string{drillRepo},
					Digest:       &slackapp.DigestSchedule{Cron: "0 9 * * 1"},
				},
			},
		},
		DigestEvery: 10 * time.Millisecond,
		Now:         h.now,
		Logger:      slog.New(slog.DiscardHandler),
	}
	slackRef = h.slackApp

	emDone := make(chan struct{})
	go func() {
		defer close(emDone)
		_ = emitter.Start(emCtx)
	}()
	t.Cleanup(func() {
		emCancel()
		<-emDone
		wctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.slackApp.Wait(wctx)
	})

	return h
}

func TestRoutinesClosedLoopDrill(t *testing.T) {
	h := newDrillHarness(t)
	ctx := t.Context()

	// ── Stage 1: Opt-in uiWrite, BindingsStore, DefinedTwice, configsource & maku config export --routines ──
	// Write r-draft and r-propose via PUT /api/routines/{owner}/{name}/{routine} through humanapi -> factory/api -> BindingsStore.
	for _, b := range []config.RoutineBinding{
		{Name: "r-draft", Mode: config.RoutineModeDraft, Cron: "0 3 * * 1", Skill: "upkeep-audit"},
		{Name: "r-propose", Mode: config.RoutineModePropose, Cron: "0 3 * * 1"},
	} {
		code, body := doJSONReq(t, h.humanTS.Client(), http.MethodPut,
			h.humanTS.URL+"/api/routines/"+drillRepo+"/"+b.Name, "maint", "https://rooms.example.com", b)
		if code != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", b.Name, code, body)
		}
	}

	// Colliding write against GitOps-defined r-trip returns 409 defined_twice.
	code, body := doJSONReq(t, h.humanTS.Client(), http.MethodPut,
		h.humanTS.URL+"/api/routines/"+drillRepo+"/r-trip", "maint", "https://rooms.example.com",
		config.RoutineBinding{Name: "r-trip", Mode: config.RoutineModeDraft, Cron: "0 3 * * *"})
	if code != http.StatusConflict || !strings.Contains(string(body), "defined_twice") {
		t.Fatalf("colliding PUT r-trip = %d %s, want 409 defined_twice", code, body)
	}

	// Direct resources.Assemble check: colliding GitOps + UIBinding emits DefinedTwice.
	uiMap, err := h.bindings.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := resources.Assemble(h.src.File, []resources.Object{
		{
			Kind: resources.KindRoutine, Name: "r-draft", Namespace: drillNS, Generation: 1,
			Spec: map[string]any{
				"workflow":      "standard",
				"dataClass":     "public",
				"defaultPrompt": "## Goal\nFix\n\n## Verify\n- ok\n",
				"probe":         map[string]any{"kind": "none"},
			},
		},
		{
			Kind: resources.KindRepository, Name: "owner-repo", Namespace: drillNS, Generation: 1,
			Spec: map[string]any{
				"factoryRef":   "prod",
				"repository":   drillRepo,
				"maintainers":  []any{"alice"},
				"triggerLabel": "factory/ready",
				"merge": map[string]any{
					"requiredChecks": []any{"ci/test", "leak-scan"},
					"verifyChecks":   []any{"ci/test", "leak-scan"},
					"leakScanCheck":  "leak-scan",
					"mergerLogin":    "ogenki-agent-factory[bot]",
					"verifyFor":      "30m",
					"revertWindow":   "168h",
					"breaker":        map[string]any{"window": 10, "maxReverts": 1},
				},
				"routines": []any{map[string]any{"name": "r-draft", "mode": "draft", "cron": "0 3 * * *"}},
			},
		},
	}, resources.Options{UIWrite: true, UIBindings: uiMap})
	if err != nil {
		t.Fatalf("resources.Assemble: %v (refused=%+v)", err, res.Refused)
	}
	if !slices.ContainsFunc(res.RoutineBindings, func(ro resources.RoutineOutcome) bool {
		return ro.Name == "r-draft" && !ro.Accepted && ro.Reason == config.ReasonDefinedTwice
	}) {
		t.Fatalf("expected DefinedTwice in Assemble outcomes: %+v", res.RoutineBindings)
	}

	// Verify live config now holds r-propose, r-draft (UI) and r-trip (GitOps).
	liveCfg := h.src.Live.Current()
	rc := liveCfg.RepositoryFor(drillRepo)
	if rc == nil || len(rc.ResolvedRoutines) != 3 || !rc.UIRoutines["r-draft"] || !rc.UIRoutines["r-propose"] || rc.UIRoutines["r-trip"] {
		t.Fatalf("unexpected merged routines: rc=%+v", rc)
	}

	// Verify `maku config export --routines` exports UI bindings cleanly.
	var exportBuf bytes.Buffer
	makuCLI := app.Maku{
		Dir: t.TempDir(),
		Out: &exportBuf,
		Err: &exportBuf,
		Kube: func(_, _ string) (maku.PreflightCluster, string, error) {
			return maku.PreflightCluster{Client: h.cl}, drillNS, nil
		},
	}
	if err := makuCLI.Run(ctx, []string{"config", "export", "--routines"}); err != nil {
		t.Fatalf("maku config export --routines: %v\n%s", err, exportBuf.String())
	}
	exportedObjs, err := resources.Decode(exportBuf.Bytes())
	if err != nil || len(exportedObjs) != 1 || exportedObjs[0].Kind != resources.KindRepository {
		t.Fatalf("exported objects = %+v err=%v", exportedObjs, err)
	}

	// ── Stage 2: Prompt+Skill SHA, propose NoOp (0 issues, silent Slack) + Public Proposal ──
	loadedDraft, err := upkeep.LoadPrompt(ctx, h.fg, drillRepo, rc.ResolvedRoutines["r-draft"])
	if err != nil {
		t.Fatalf("LoadPrompt r-draft: %v", err)
	}
	if loadedDraft.Source != "repo" || loadedDraft.SkillText == "" || len(loadedDraft.EffectiveSHA) != 64 {
		t.Fatalf("unexpected LoadedPrompt: %+v", loadedDraft)
	}

	// Tick Monday 03:00:10 UTC -> creates Tasks for r-propose, r-draft, r-trip.
	if err := h.sched.Tick(ctx); err != nil {
		t.Fatalf("sched.Tick: %v", err)
	}
	var allTasks factoryv1.TaskList
	if err := h.cl.List(ctx, &allTasks, client.InNamespace(drillNS)); err != nil {
		t.Fatal(err)
	}
	findTask := func(routine string) *factoryv1.Task {
		for i := range allTasks.Items {
			tk := &allTasks.Items[i]
			if tk.Spec.Routine != nil && tk.Spec.Routine.Name == routine && tk.Status.Phase == "" {
				return tk
			}
		}
		return nil
	}
	proposeNoOpTask := findTask("r-propose")
	draftTask := findTask("r-draft")
	if proposeNoOpTask == nil || draftTask == nil {
		t.Fatalf("expected scheduled tasks for r-propose and r-draft, got %+v", allTasks.Items)
	}

	// Drive r-propose Case A: NoOp (triager succeeds with no handoff -> 0 issues, silent Slack).
	tkPropose := h.reconcile(proposeNoOpTask.Name, 3)
	if tkPropose.Status.Phase != factoryv1.PhaseImplementing || tkPropose.Spec.DataClass != "public" || tkPropose.Spec.Template != "investigate" {
		t.Fatalf("propose start = %s/%s template=%s", tkPropose.Status.Phase, tkPropose.Spec.DataClass, tkPropose.Spec.Template)
	}
	propRunID := taskid.Run(tkPropose.Name, 0)
	h.runs.set(propRunID, "Succeeded")
	h.memBroker.AppendRunEnd(tkPropose.Name, propRunID, "Succeeded", "agent_finished")
	tkPropose = h.reconcile(tkPropose.Name, 1)
	if tkPropose.Status.Phase != factoryv1.PhaseNoOp || tkPropose.Status.Reason != "no_action" {
		t.Fatalf("propose NoOp = %s/%s", tkPropose.Status.Phase, tkPropose.Status.Reason)
	}
	if len(h.fg.Created()) != 0 {
		t.Fatalf("propose NoOp created unexpected GitHub issues: %+v", h.fg.Created())
	}
	// Verify Slack stays silent on task_received, task_triaged, and no_op.
	eventually(t, 2*time.Second, "propose NoOp cursor flush", func() bool {
		posts, _ := h.slackAPI.snapshot()
		return len(posts) == 0
	})
	if posts, _ := h.slackAPI.snapshot(); len(posts) != 0 {
		t.Fatalf("Slack posted on propose NoOp: %+v", posts)
	}

	// Drive r-propose Case B: manual trigger via API -> public factory/proposed issue.
	code, runBody := doJSONReq(t, h.humanTS.Client(), http.MethodPost,
		h.humanTS.URL+"/api/routines/"+drillRepo+"/r-propose/runs", "maint", "https://rooms.example.com",
		map[string]string{"mode": "propose", "idempotencyKey": "drill-propose-1"})
	if code != http.StatusCreated {
		t.Fatalf("manual run r-propose = %d %s", code, runBody)
	}
	var runResp struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(runBody, &runResp); err != nil {
		t.Fatal(err)
	}
	tkProp2 := h.reconcile(runResp.Task, 3)
	prop2RunID := taskid.Run(tkProp2.Name, 0)
	h.memBroker.AppendHandoff(tkProp2.Name, prop2RunID, "triager", "implementer", "",
		"docs: refresh routine guide\n## What is wrong\nMissing CLI flags.\n## What it should be\nDocument flags.\n## Acceptance check\nAll flags listed.\n## Files\n- `docs/cli.md`")
	h.runs.set(prop2RunID, "Succeeded")
	h.memBroker.AppendRunEnd(tkProp2.Name, prop2RunID, "Succeeded", "agent_finished")
	tkProp2 = h.reconcile(tkProp2.Name, 1)
	if tkProp2.Status.Phase != factoryv1.PhaseDone || tkProp2.Status.Reason != "proposal_filed" || tkProp2.Spec.Issue == 0 {
		t.Fatalf("propose public filing = %s/%s issue=%d", tkProp2.Status.Phase, tkProp2.Status.Reason, tkProp2.Spec.Issue)
	}
	iss, err := h.fg.Issue(ctx, tkProp2.Spec.Issue)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(iss.Labels, "factory/proposed") || !slices.Contains(iss.Labels, "factory/routine:r-propose") ||
		!slices.Contains(iss.Labels, intake.PublishedFromLabel(tkProp2.Name)) {
		t.Fatalf("missing expected proposal labels: %v", iss.Labels)
	}

	// ── Stage 3: UPKEEP-HISTORY, upkeep_path guard, draft hold / 48h silent / Ready merge & Slack thread ──
	// Seed a closed routine PR #5 in fake forge so cold-start fallback populates UPKEEP-HISTORY-n0nce001.
	h.fg.SetPR(forge.PR{
		Number: 5, NodeID: "PR_5", State: "CLOSED", Title: "chore: prior rejected attempt",
		Author: "ogenki-agents[bot]", ClosedAt: h.now().Add(-48 * time.Hour),
		Labels: []string{"factory/routine:r-draft"},
	})
	h.fg.SetIssue(forge.Issue{
		Number: 5, State: "CLOSED", Title: "chore: prior rejected attempt",
		Labels: []string{"factory/routine:r-draft"},
	})

	tkDraft := h.reconcile(draftTask.Name, 3)
	if tkDraft.Status.Phase != factoryv1.PhaseImplementing {
		t.Fatalf("draft start = %s/%s", tkDraft.Status.Phase, tkDraft.Status.Reason)
	}
	draftImplID := taskid.Run(tkDraft.Name, 0)
	draftSpec, ok := h.runs.spec(draftImplID)
	if !ok || !draftSpec.PRDraft || !strings.Contains(draftSpec.TaskText, "ROUTINE-PROMPT-n0nce001") ||
		!strings.Contains(draftSpec.TaskText, "ROUTINE-SKILL-n0nce001") || !strings.Contains(draftSpec.TaskText, "UPKEEP-HISTORY-n0nce001") {
		t.Fatalf("unexpected draft implementer spec: ok=%v PRDraft=%v\n%s", ok, draftSpec.PRDraft, draftSpec.TaskText)
	}

	// Implementer opens draft PR #12 -> webhook posts top-level room.pr_opened in Slack C03UP.
	h.runs.set(draftImplID, "Running")
	h.fg.SetBranch("agent/"+tkDraft.Name, 12)
	h.fg.SetActivity("agent/"+tkDraft.Name, forge.Push{Type: "branch_creation", Actor: "ogenki-agents[bot]", After: "1111111a", At: h.now()})
	h.fg.SetPR(forge.PR{
		Number: 12, NodeID: "PR_12", URL: "https://github.com/" + drillRepo + "/pull/12",
		State: "OPEN", Draft: true, Title: "docs: fix broken links", Author: "ogenki-agents[bot]",
		BaseRef: "main", HeadRef: "agent/" + tkDraft.Name, HeadSHA: "1111111a",
		HeadCommittedAt: h.now().Add(-time.Minute),
		HeadMessage:     "docs: fix broken links\n\nAgent-Run: " + draftImplID,
	})
	h.fg.SetFiles(
		map[string]string{"docs/guide.md": "[old](https://example.com/old)"},
		map[string]string{"docs/guide.md": "[new](https://example.com/new)"},
	)
	tkDraft = h.reconcile(tkDraft.Name, 2)

	eventually(t, 3*time.Second, "Slack room.pr_opened thread", func() bool {
		posts, _ := h.slackAPI.snapshot()
		return slices.ContainsFunc(posts, func(p slackPost) bool {
			return p.Channel == "C03UP" && p.ThreadTS == "" && strings.Contains(p.Text, "[r-draft]") && strings.Contains(p.Text, "#12")
		})
	})

	// Implementer succeeds -> reviewer approves while PR is still draft -> AwaitingCI -> draft_pr hold.
	h.runs.set(draftImplID, "Succeeded")
	h.memBroker.AppendHandoff(tkDraft.Name, draftImplID, "implementer", "reviewer", "1111111a", "done")
	h.memBroker.AppendRunEnd(tkDraft.Name, draftImplID, "Succeeded", "agent_finished")
	tkDraft = h.reconcile(tkDraft.Name, 2)
	if tkDraft.Status.Phase != factoryv1.PhaseReviewing {
		t.Fatalf("draft reviewing = %s/%s", tkDraft.Status.Phase, tkDraft.Status.Reason)
	}
	draftRevID := taskid.Run(tkDraft.Name, 1)
	h.runs.set(draftRevID, "Succeeded")
	h.memBroker.AppendVerdict(tkDraft.Name, draftRevID, "approve", "1111111a", "LGTM")
	h.memBroker.AppendRunEnd(tkDraft.Name, draftRevID, "Succeeded", "agent_finished")
	tkDraft = h.reconcile(tkDraft.Name, 1)
	if tkDraft.Status.Phase != factoryv1.PhaseAwaitingCI {
		t.Fatalf("after reviewer approve = %s/%s", tkDraft.Status.Phase, tkDraft.Status.Reason)
	}

	h.fg.SetChecks(12, forge.Checks{Runs: []forge.Check{
		{Name: "ci/test", State: "SUCCESS", App: forge.ActionsApp, Conclusion: "SUCCESS"},
		{Name: "leak-scan", State: "SUCCESS", App: forge.ActionsApp, Conclusion: "SUCCESS"},
	}})
	pr12, _ := h.fg.PullRequest(ctx, 12)
	pr12.Reviews = []forge.Review{{ID: 90, Author: "alice", State: "APPROVED", Commit: "1111111a", At: h.now()}}
	h.fg.SetPR(pr12)
	tkDraft = h.reconcile(tkDraft.Name, 1)
	if tkDraft.Status.Phase != factoryv1.PhaseAwaitingHuman || tkDraft.Status.Reason != "draft_pr" || len(h.fg.Armed()) != 0 {
		t.Fatalf("expected draft_pr hold, got %s/%s armed=%v", tkDraft.Status.Phase, tkDraft.Status.Reason, h.fg.Armed())
	}

	// Advance clock > 48h while draft -> verify 0 reminder comments or body updates posted.
	beforeBodies := slices.Clone(h.fg.Comments(12))
	h.advance(49 * time.Hour)
	tkDraft = h.reconcile(tkDraft.Name, 1)
	afterBodies := h.fg.Comments(12)
	if tkDraft.Status.Phase != factoryv1.PhaseAwaitingHuman || !slices.Equal(afterBodies, beforeBodies) {
		t.Fatalf("expected 0 reminders on 49h draft, phase=%s before=%v after=%v", tkDraft.Status.Phase, beforeBodies, afterBodies)
	}

	// Mark Ready for review and refresh maintainer approval while diff touches protected upkeep prompt -> re-enters without spending GateReentries, then refuses upkeep_path!
	h.fg.SetDraft(12, false)
	pr12, _ = h.fg.PullRequest(ctx, 12)
	pr12.Reviews = append(pr12.Reviews, forge.Review{ID: 91, Author: "alice", State: "APPROVED", Commit: "1111111a", At: h.now()})
	h.fg.SetPR(pr12)
	tkDraft = h.reconcile(tkDraft.Name, 1)
	if tkDraft.Status.Phase != factoryv1.PhaseAwaitingCI || tkDraft.Status.Reason != "reentered_ready" || tkDraft.Status.GateReentries != 0 {
		t.Fatalf("after Ready = %s/%s reentries=%d", tkDraft.Status.Phase, tkDraft.Status.Reason, tkDraft.Status.GateReentries)
	}
	h.fg.SetFiles(
		map[string]string{"docs/guide.md": "[old](https://example.com/old)"},
		map[string]string{"docs/guide.md": "[new](https://example.com/new)", ".github/agent-factory/upkeep/r-draft.md": "tampered"},
	)
	h.fg.SetFileStats(forge.FileStat{Path: ".github/agent-factory/upkeep/r-draft.md"})
	h.fg.SetPullFiles(12, forge.FilePatch{Path: ".github/agent-factory/upkeep/r-draft.md", Status: "modified"})
	tkDraft = h.reconcile(tkDraft.Name, 1)
	if tkDraft.Status.Phase != factoryv1.PhaseAwaitingHuman || tkDraft.Status.Reason != "upkeep_path" || len(h.fg.Armed()) != 0 {
		t.Fatalf("expected upkeep_path refusal under approveToMerge, got %s/%s armed=%v", tkDraft.Status.Phase, tkDraft.Status.Reason, h.fg.Armed())
	}

	// Maintainer merges PR #12 by hand after upkeep_path notice -> Done/merged & Slack room.merged updates existing thread in place!
	pr12, _ = h.fg.PullRequest(ctx, 12)
	pr12.State = "MERGED"
	pr12.MergedBy = "alice"
	pr12.MergedAt = h.now()
	pr12.MergeCommitSHA = "2222222b"
	h.fg.SetPR(pr12)
	tkDraft = h.reconcile(tkDraft.Name, 2)
	if tkDraft.Status.Phase != factoryv1.PhaseDone {
		t.Fatalf("expected PhaseDone after manual merge, got %s/%s", tkDraft.Status.Phase, tkDraft.Status.Reason)
	}
	eventually(t, 3*time.Second, "Slack room.merged end-line update", func() bool {
		posts, updates := h.slackAPI.snapshot()
		hasPost := slices.ContainsFunc(posts, func(p slackPost) bool {
			return p.Channel == "C03UP" && p.ThreadTS != "" && strings.Contains(p.Text, "Merged #12")
		})
		hasUpdate := slices.ContainsFunc(updates, func(p slackPost) bool {
			return p.Channel == "C03UP" && strings.Contains(p.Text, "Merged #12")
		})
		return hasPost || hasUpdate
	})

	// ── Stage 4: 14d Stale Closure -> 3-Rejection Breaker Trip -> routine.paused Alert -> digest.cron -> SHA Bump Recovery ──
	// Find the scheduled r-trip task from Monday 03:00 tick (or drive 3 closed/stale iterations).
	var tripTask1 *factoryv1.Task
	for i := range allTasks.Items {
		tk := &allTasks.Items[i]
		if tk.Spec.Routine != nil && tk.Spec.Routine.Name == "r-trip" {
			tripTask1 = tk
			break
		}
	}
	if tripTask1 == nil {
		t.Fatal("missing initial r-trip task")
	}

	// Iteration 1: Draft PR ages > 14d -> factory/stale auto-close -> OutcomeStale (1).
	tkTrip1 := h.reconcile(tripTask1.Name, 3)
	rID1 := taskid.Run(tkTrip1.Name, 0)
	h.runs.set(rID1, "Running")
	h.fg.SetBranch("agent/"+tkTrip1.Name, 21)
	h.fg.SetActivity("agent/"+tkTrip1.Name, forge.Push{Type: "branch_creation", Actor: "ogenki-agents[bot]", After: "3333333c", At: h.now()})
	h.fg.SetChecks(21, forge.Checks{Runs: []forge.Check{
		{Name: "ci/test", State: "SUCCESS", App: forge.ActionsApp, Conclusion: "SUCCESS"},
		{Name: "leak-scan", State: "SUCCESS", App: forge.ActionsApp, Conclusion: "SUCCESS"},
	}})
	h.fg.SetPR(forge.PR{
		Number: 21, NodeID: "PR_21", URL: "https://github.com/" + drillRepo + "/pull/21",
		State: "OPEN", Draft: true, Author: "ogenki-agents[bot]", BaseRef: "main",
		HeadRef: "agent/" + tkTrip1.Name, HeadSHA: "3333333c", HeadCommittedAt: h.now().Add(-time.Minute),
		HeadMessage: "chore: cleanup 1\n\nAgent-Run: " + rID1,
	})
	h.reconcile(tkTrip1.Name, 2)
	h.runs.set(rID1, "Succeeded")
	h.memBroker.AppendHandoff(tkTrip1.Name, rID1, "implementer", "factory", "3333333c", "done")
	h.memBroker.AppendRunEnd(tkTrip1.Name, rID1, "Succeeded", "agent_finished")
	tkTrip1 = h.reconcile(tkTrip1.Name, 2) // AwaitingHuman draft_pr
	if tkTrip1.Status.Phase != factoryv1.PhaseAwaitingHuman || tkTrip1.Status.Reason != "draft_pr" {
		t.Fatalf("trip1 draft_pr = %s/%s", tkTrip1.Status.Phase, tkTrip1.Status.Reason)
	}
	h.advance(14*24*time.Hour + time.Minute)
	tkTrip1 = h.reconcile(tkTrip1.Name, 1)
	if tkTrip1.Status.Phase != factoryv1.PhaseClosed || tkTrip1.Status.Reason != "stale" || !slices.Contains(h.fg.Added(21), "factory/stale") {
		t.Fatalf("expected 14d stale close on trip1, got %s/%s labels=%v", tkTrip1.Status.Phase, tkTrip1.Status.Reason, h.fg.Added(21))
	}

	// Iterations 2 and 3: 2 more closed PRs -> 3rd trips breaker to BreakerPaused ("consecutive_closed") -> routine.paused Slack alert!
	for iter, prNum := range []int{22, 23} {
		idem := fmt.Sprintf("trip-closed-%d", iter+2)
		tkManual, err := h.sched.TriggerManual(ctx, drillRepo, "r-trip", "draft", "alice", idem)
		if err != nil {
			t.Fatalf("TriggerManual %s: %v", idem, err)
		}
		tkTripN := h.reconcile(tkManual.Name, 3)
		rID := taskid.Run(tkTripN.Name, 0)
		h.runs.set(rID, "Running")
		h.fg.SetBranch("agent/"+tkTripN.Name, prNum)
		h.fg.SetPR(forge.PR{
			Number: prNum, NodeID: fmt.Sprintf("PR_%d", prNum), URL: fmt.Sprintf("https://github.com/%s/pull/%d", drillRepo, prNum),
			State: "OPEN", Draft: true, Author: "ogenki-agents[bot]", BaseRef: "main",
			HeadRef: "agent/" + tkTripN.Name, HeadSHA: fmt.Sprintf("44444%02d", prNum),
			HeadCommittedAt: h.now().Add(-time.Minute),
			HeadMessage:     fmt.Sprintf("chore: cleanup\n\nAgent-Run: %s", rID),
		})
		h.reconcile(tkTripN.Name, 2)
		h.runs.set(rID, "Succeeded")
		h.memBroker.AppendRunEnd(tkTripN.Name, rID, "Succeeded", "agent_finished")
		h.reconcile(tkTripN.Name, 1)
		h.fg.SetPR(forge.PR{
			Number: prNum, NodeID: fmt.Sprintf("PR_%d", prNum),
			State: "CLOSED", ClosedAt: h.now(), HeadRef: "agent/" + tkTripN.Name, HeadSHA: fmt.Sprintf("44444%02d", prNum),
		})
		h.advance(time.Minute)
		ended := h.reconcile(tkTripN.Name, 1)
		if ended.Status.Phase != factoryv1.PhaseClosed {
			t.Fatalf("trip iter %d phase=%s, want Closed", iter+2, ended.Status.Phase)
		}
	}

	stTrip, err := h.state.Get(ctx, drillRepo, "r-trip")
	if err != nil {
		t.Fatal(err)
	}
	if stTrip.BreakerState != upkeep.BreakerPaused || stTrip.BreakerReason != upkeep.BreakerReasonConsecutiveClosed {
		t.Fatalf("expected BreakerPaused/consecutive_closed, got %+v", stTrip)
	}

	eventually(t, 3*time.Second, "Slack routine.paused alert", func() bool {
		posts, _ := h.slackAPI.snapshot()
		return slices.ContainsFunc(posts, func(p slackPost) bool {
			return p.Channel == "C03UP" &&
				strings.Contains(p.Text, "[r-trip]") &&
				strings.Contains(p.Text, "consecutive_closed") &&
				strings.Contains(p.Text, "https://github.com/"+drillRepo+"/edit/main/.github/agent-factory/upkeep/r-trip.md")
		})
	})

	// StartDigest fires weekly scorecard via humanapi -> factory/api when clock hits the following Monday 09:00 UTC.
	nextMon := time.Date(2026, 12, 14, 9, 0, 5, 0, time.UTC)
	h.setNow(nextMon)
	digestCtx, digestCancel := context.WithCancel(ctx)
	defer digestCancel()
	h.slackApp.StartDigest(digestCtx)
	eventually(t, 3*time.Second, "Slack weekly upkeep scorecard", func() bool {
		posts, _ := h.slackAPI.snapshot()
		return slices.ContainsFunc(posts, func(p slackPost) bool {
			return p.Channel == "C03UP" &&
				strings.Contains(p.Text, "Weekly upkeep scorecard (7d):") &&
				strings.Contains(p.Text, "[r-trip]") &&
				strings.Contains(p.Text, "paused (consecutive_closed)")
		})
	})
	digestCancel()

	// Unrelated file commit keeps breaker paused; updating bound skill bumps EffectiveSHA -> half_open -> trial merge -> ok!
	h.fg.SetFile("README.md", "Unrelated docs edit\n")
	loadedUnchanged, err := upkeep.LoadPrompt(ctx, h.fg, drillRepo, rc.ResolvedRoutines["r-trip"])
	if err != nil {
		t.Fatal(err)
	}
	stCheck, transitioned, err := h.breaker.CheckPromptReset(ctx, drillRepo, "r-trip", loadedUnchanged.EffectiveSHA)
	if err != nil || transitioned || stCheck.BreakerState != upkeep.BreakerPaused {
		t.Fatalf("unrelated commit should not reset breaker: transitioned=%v st=%+v err=%v", transitioned, stCheck, err)
	}

	// Update bound Agent Skill -> EffectiveSHA changes -> paused transitions to half_open.
	h.fg.SetFile(".agents/skills/upkeep-audit/SKILL.md", "# Upkeep Audit Skill v2\nTightened criteria and verification steps.\n")
	loadedBumped, err := upkeep.LoadPrompt(ctx, h.fg, drillRepo, rc.ResolvedRoutines["r-trip"])
	if err != nil {
		t.Fatal(err)
	}
	if loadedBumped.EffectiveSHA == loadedUnchanged.EffectiveSHA {
		t.Fatal("expected skill bump to change EffectiveSHA")
	}
	stHalf, transitioned, err := h.breaker.CheckPromptReset(ctx, drillRepo, "r-trip", loadedBumped.EffectiveSHA)
	if err != nil || !transitioned || stHalf.BreakerState != upkeep.BreakerHalfOpen {
		t.Fatalf("skill bump should reset breaker to half_open: transitioned=%v st=%+v err=%v", transitioned, stHalf, err)
	}

	// Trial run in half_open merges -> breaker recovers to ok!
	tkTrial, err := h.sched.TriggerManual(ctx, drillRepo, "r-trip", "", "alice", "trial-recover-1")
	if err != nil {
		t.Fatalf("TriggerManual trial: %v", err)
	}
	h.reconcile(tkTrial.Name, 3)
	trialRunID := taskid.Run(tkTrial.Name, 0)
	h.runs.set(trialRunID, "Succeeded")
	h.fg.SetBranch("agent/"+tkTrial.Name, 24)
	h.fg.SetPR(forge.PR{
		Number: 24, NodeID: "PR_24", URL: "https://github.com/" + drillRepo + "/pull/24",
		State: "MERGED", MergedBy: "alice", MergedAt: h.now(),
		Author: "ogenki-agents[bot]", BaseRef: "main",
		MergeCommitSHA: "5555555d", HeadRef: "agent/" + tkTrial.Name,
	})
	h.memBroker.AppendRunEnd(tkTrial.Name, trialRunID, "Succeeded", "agent_finished")
	tkTrial = h.reconcile(tkTrial.Name, 2)
	if tkTrial.Status.Phase != factoryv1.PhaseDone {
		t.Fatalf("trial merge phase=%s/%s, want Done", tkTrial.Status.Phase, tkTrial.Status.Reason)
	}

	stRecovered, err := h.state.Get(ctx, drillRepo, "r-trip")
	if err != nil {
		t.Fatal(err)
	}
	if stRecovered.BreakerState != upkeep.BreakerOK || stRecovered.BreakerReason != "" || stRecovered.TrippedPromptSHA != "" || stRecovered.BreakerResetAt == nil {
		t.Fatalf("expected breaker recovered to ok, got %+v", stRecovered)
	}
}
