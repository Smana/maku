// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/api"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/reconciler"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/factory/triage"
)

const actRepo = "Smana/cloud-native-ref"

var actNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

type actTokens map[string]authn.Principal

func (b actTokens) Authenticate(r *http.Request) (authn.Principal, error) {
	p, ok := b[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		return authn.Principal{}, authn.ErrUnauthenticated
	}
	return p, nil
}

type actSlackLinks map[string]string

func (l actSlackLinks) SlackLink(_ context.Context, team, user string) (string, bool, error) {
	sub, ok := l[team+"/"+user]
	return sub, ok, nil
}

type actLogins map[string]string

func (l actLogins) Login(_ context.Context, sub, _ string) (string, error) {
	return l[sub], nil
}

type actRuns struct {
	specs   map[string]runs.Spec
	runs    map[string]runs.Run
	patches map[string]map[string]string
}

func newActRuns() *actRuns {
	return &actRuns{specs: map[string]runs.Spec{}, runs: map[string]runs.Run{}, patches: map[string]map[string]string{}}
}

func (f *actRuns) Create(_ context.Context, s runs.Spec) error {
	f.specs[s.RunID] = s
	f.runs[s.RunID] = runs.Run{ID: s.RunID, TaskID: s.TaskID, Role: s.Role, Principal: s.Principal,
		ChargedTo: s.ChargedTo, RoomRef: s.RoomRef, Phase: "Pending", MaxTokens: s.MaxTokens,
		StartSeq: s.StartSeq, Head: s.Head, Created: actNow, TaskText: s.TaskText}
	return nil
}

func (f *actRuns) Get(_ context.Context, id string) (runs.Run, bool, error) {
	r, ok := f.runs[id]
	return r, ok, nil
}

func (f *actRuns) List(context.Context) ([]runs.Run, error) {
	out := make([]runs.Run, 0, len(f.runs))
	for _, r := range f.runs {
		out = append(out, r)
	}
	return out, nil
}

func (f *actRuns) Annotate(_ context.Context, id string, kv map[string]string) error {
	if f.patches[id] == nil {
		f.patches[id] = map[string]string{}
	}
	for k, v := range kv {
		f.patches[id][k] = v
	}
	return nil
}

func (f *actRuns) Delete(_ context.Context, id string) error { delete(f.runs, id); return nil }

type actRoomLog struct {
	evs []envelope.Event
}

func (l *actRoomLog) EventsSince(_ context.Context, _ string, after int64, keep func(envelope.Event) bool) ([]envelope.Event, int64, error) {
	var out []envelope.Event
	cursor := after
	for _, e := range l.evs {
		if e.Seq > after {
			cursor = e.Seq
			if keep == nil || keep(e) {
				out = append(out, e)
			}
		}
	}
	return out, cursor, nil
}

func (l *actRoomLog) LastSeq(context.Context, string) (int64, error) { return int64(len(l.evs)), nil }
func (l *actRoomLog) Enqueue(context.Context, string, string, string, int64) error {
	return nil
}
func (l *actRoomLog) Queue(context.Context, string) ([]rooms.Queued, error)  { return nil, nil }
func (l *actRoomLog) Consume(context.Context, string, []int64, string) error { return nil }
func (l *actRoomLog) TaskState(context.Context, string, string, int64) error { return nil }
func (l *actRoomLog) TaskFacts(context.Context, string, envelope.TaskFacts, int64) error {
	return nil
}
func (l *actRoomLog) Output(context.Context, string, string, envelope.MessageKind, envelope.OutputRecord) error {
	return nil
}

type actEnv struct {
	srv     *api.Server
	rec     *reconciler.Reconciler
	client  client.Client
	forge   *forgetest.Fake
	runs    *actRuns
	roomLog *actRoomLog
	writers map[string]bool
}

func newActEnv(t *testing.T, objs ...client.Object) *actEnv {
	t.Helper()
	sch := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(sch)
	_ = corev1.AddToScheme(sch)
	_ = roomv1.AddToScheme(sch)
	_ = factoryv1.AddToScheme(sch)

	cl := fake.NewClientBuilder().WithScheme(sch).
		WithStatusSubresource(&factoryv1.Task{}, &roomv1.Room{}).
		WithObjects(objs...).Build()
	if err := ledger.StampMeter(t.Context(), cl, "agent-system", actNow, time.Hour); err != nil {
		t.Fatal(err)
	}
	m, err := fmetrics.New(nil, nil, "agent-system", func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		RoomsURL: "https://rooms.priv.example",
		Repositories: []config.Repository{{
			Name:        actRepo,
			Maintainers: []string{"Smana", "nowrite"},
			Classes:     map[string]config.Class{"review": {Shadow: true}},
			Merge: config.Merge{
				RequiredChecks: []string{"ci"},
				VerifyChecks:   []string{"ci"},
				PolicyBotLogin: "ogenki-merge-gate[bot]",
				PolicyBot:      new(true),
				GateStatus:     new(false),
				MaxPerDay:      10,
				FixRuns:        2,
				VerifyFor:      config.Duration{Duration: 30 * time.Minute},
				RevertWindow:   config.Duration{Duration: 168 * time.Hour},
				Breaker:        config.Breaker{Window: 10, MaxReverts: 1},
			},
		}},
		FactoryLogin: "ogenki-agent-factory[bot]",
		Poll:         config.Poll{Tasks: config.Duration{Duration: 30 * time.Second}, Meter: config.Duration{Duration: 30 * time.Second}},
		Defaults:     config.Defaults{DataClass: "public", PredictedClass: "review"},
		Tiers: map[string]config.Tier{
			"light":    {Model: "agent-default", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20},
			"standard": {Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45},
			"frontier": {Model: "tier-frontier", RunTokens: 4_000_000, TaskTokens: 8_000_000, RunMinutes: 90},
		},
		Templates: map[string]config.Template{
			"solo": {Roles: []string{"implementer"}},
			"pair": {Roles: []string{"implementer", "reviewer"}, MaxReviewRounds: 2},
		},
		Loops:   map[string]config.Loop{"default": {Scope: config.ScopeScoper}},
		Caps:    config.Caps{ActiveTasks: 3, ConcurrentRuns: 4, TasksPerDay: 20, MaxTextBytes: 14336, MaxPendingMinutes: 30, AwaitingHumanWIP: 5},
		Resume:  config.Resume{MaxPerTask: 2},
		Budgets: config.Budgets{FactoryDailyUSD: 25, HumanDailyUSD: 5},
		Meter:   config.Meter{Prices: map[string]config.Price{"agent-default": {Input: 1.40, CacheRead: 0.26, Output: 4.40}}},
		API: config.API{
			Repositories: []string{actRepo},
			Sponsored: []config.Sponsored{{
				Name:         "slack-app",
				ClientIDFile: "/x",
				Sponsor:      "Smana",
				Repositories: []string{actRepo},
				DailyUSD:     10,
				MaxCostUSD:   5,
				OnBehalfOf:   config.OnBehalfOf{SlackLinks: true},
			}},
		},
		Hash: strings.Repeat("a", 64),
	}

	writers := map[string]bool{"smana": true, "contributor": true}
	canWrite := func(_ context.Context, _, login string) (bool, error) {
		return writers[strings.ToLower(login)], nil
	}
	fg := forgetest.NewFake()
	fg.Now = func() time.Time { return actNow }
	rs := newActRuns()
	rl := &actRoomLog{}

	srv := &api.Server{
		Auth: actTokens{
			"slack":    {Kind: envelope.ActorSystem, ID: "system:slack-app", Sponsored: true, AccessToken: "slack"},
			"human":    {Kind: envelope.ActorHuman, ID: "human:291", Sub: "zsub-smana", Groups: []string{"agents-admin"}},
			"internal": {Kind: envelope.ActorSystem, ID: "system:runlore", Sponsored: true, Internal: true},
		},
		Cfg:       cfg,
		Runs:      rs,
		Rooms:     cl,
		Namespace: "agent-system",
		Stopped:   func(context.Context) bool { return false },
		Now:       func() time.Time { return actNow },
		Metrics:   m,
		Owned:     func(string) bool { return true },
		SlackLinks: actSlackLinks{
			"T01ABC/UMAINT":   "zsub-smana",
			"T01ABC/UCONTRIB": "zsub-contrib",
			"T01ABC/UNOWRITE": "zsub-nowrite",
		},
		Logins: actLogins{
			"zsub-smana":   "Smana",
			"zsub-contrib": "contributor",
			"zsub-nowrite": "nowrite",
		},
		Writers: canWrite,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	rec := &reconciler.Reconciler{
		Client:    cl,
		Ledger:    cl,
		Namespace: "agent-system",
		Cfg:       cfg,
		Forge:     fg,
		Merger:    fg,
		Runs:      rs,
		Rooms:     rl,
		Triage:    triage.Static{Template: "solo"},
		Metrics:   m,
		Now:       func() time.Time { return actNow },
		Nonce:     func() string { return "n0nce234" },
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Owned:     func(string) bool { return true },
		CanWrite:  canWrite,
	}

	return &actEnv{srv: srv, rec: rec, client: cl, forge: fg, runs: rs, roomLog: rl, writers: writers}
}

func makeActTask(name, phase, reason string) *factoryv1.Task {
	since := metav1.NewTime(actNow.Add(-5 * time.Minute))
	return &factoryv1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "agent-system",
			CreationTimestamp: metav1.NewTime(actNow.Add(-10 * time.Minute)),
		},
		Spec: factoryv1.TaskSpec{
			Source: factoryv1.Source{
				Kind:          "issue",
				Ref:           actRepo + "#7",
				Key:           "k-" + name,
				RequestedBy:   "github:Smana",
				Trust:         "untrusted",
				ContentSHA256: strings.Repeat("b", 64),
			},
			Repository:     actRepo,
			Issue:          7,
			Text:           "# Fix the link\n\n## Acceptance check\nthe link resolves",
			DataClass:      "public",
			PredictedClass: "review",
			Budget:         factoryv1.Budget{Tier: "standard", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45},
			Template:       "solo",
		},
		Status: factoryv1.TaskStatus{
			Phase:      phase,
			Reason:     reason,
			PhaseSince: &since,
			RoomRef:    name,
		},
	}
}

func postAct(t *testing.T, srv *api.Server, token, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func actResult(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out.Result
}

func getActTask(t *testing.T, cl client.Client, name string) *factoryv1.Task {
	t.Helper()
	var tk factoryv1.Task
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: name}, &tk); err != nil {
		t.Fatal(err)
	}
	return &tk
}

func reconcileTask(t *testing.T, rec *reconciler.Reconciler, name string, times int) *factoryv1.Task {
	t.Helper()
	for range times {
		if _, err := rec.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "agent-system", Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	return getActTask(t, rec.Client, name)
}

func TestActsRefuseHumanToken(t *testing.T) {
	tk := makeActTask("3buqdlot", factoryv1.PhaseEscalated, "agent_error")
	env := newActEnv(t, tk)
	for _, path := range []string{"/v1/tasks/3buqdlot/stops", "/v1/tasks/3buqdlot/retries"} {
		for _, tok := range []string{"human", "internal"} {
			rec := postAct(t, env.srv, tok, path, `{"onBehalfOf":"slack:T01ABC/UMAINT"}`)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"sponsored_only"`) {
				t.Fatalf("%s as %s: got %d %s", path, tok, rec.Code, rec.Body.String())
			}
		}
		rec := postAct(t, env.srv, "", path, `{"onBehalfOf":"slack:T01ABC/UMAINT"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated: got %d %s", path, rec.Code, rec.Body.String())
		}
	}
	got := getActTask(t, env.client, "3buqdlot")
	if len(got.Annotations) != 0 || got.Status.Phase != factoryv1.PhaseEscalated {
		t.Fatalf("task mutated by refused caller: %+v %s", got.Annotations, got.Status.Phase)
	}
}

func TestStopNeedsMaintainerWrite(t *testing.T) {
	tk := makeActTask("3buqdlot", factoryv1.PhaseImplementing, "")
	env := newActEnv(t, tk)

	// Linked GitHub writer who is NOT a configured maintainer -> not_maintainer, task untouched.
	if got := actResult(t, postAct(t, env.srv, "slack", "/v1/tasks/3buqdlot/stops", `{"onBehalfOf":"slack:T01ABC/UCONTRIB"}`)); got != "not_maintainer" {
		t.Fatalf("linked non-maintainer: got %q, want not_maintainer", got)
	}
	// Configured maintainer without GitHub write -> not_maintainer, task untouched.
	if got := actResult(t, postAct(t, env.srv, "slack", "/v1/tasks/3buqdlot/stops", `{"onBehalfOf":"slack:T01ABC/UNOWRITE"}`)); got != "not_maintainer" {
		t.Fatalf("maintainer without write: got %q, want not_maintainer", got)
	}
	// Unlinked Slack user -> not_linked, task untouched.
	if got := actResult(t, postAct(t, env.srv, "slack", "/v1/tasks/3buqdlot/stops", `{"onBehalfOf":"slack:T01ABC/UUNKNOWN"}`)); got != "not_linked" {
		t.Fatalf("unlinked: got %q, want not_linked", got)
	}

	after := getActTask(t, env.client, "3buqdlot")
	if after.Annotations[factoryv1.AnnotationStop] != "" || after.Status.Phase != factoryv1.PhaseImplementing {
		t.Fatalf("task mutated on refusal: annotations=%v phase=%s", after.Annotations, after.Status.Phase)
	}
}

func TestStopQueuedOrRunning(t *testing.T) {
	queued := makeActTask("3buqdlot", factoryv1.PhaseQueued, "waiting_daily_budget")
	running := makeActTask("4buqdlot", factoryv1.PhaseImplementing, "")
	ended := makeActTask("5buqdlot", factoryv1.PhaseDone, "merged")
	stopped := makeActTask("6buqdlot", factoryv1.PhaseStopped, "stopped_by_label")
	env := newActEnv(t, queued, running, ended, stopped)

	for _, name := range []string{"3buqdlot", "4buqdlot"} {
		if got := actResult(t, postAct(t, env.srv, "slack", "/v1/tasks/"+name+"/stops", `{"onBehalfOf":"slack:T01ABC/UMAINT"}`)); got != "stopped" {
			t.Fatalf("stop %s: got %q, want stopped", name, got)
		}
		tk := reconcileTask(t, env.rec, name, 1)
		if tk.Status.Phase != factoryv1.PhaseStopped {
			t.Fatalf("%s reconciled to %s, want Stopped", name, tk.Status.Phase)
		}
	}
	for _, name := range []string{"5buqdlot", "6buqdlot"} {
		if got := actResult(t, postAct(t, env.srv, "slack", "/v1/tasks/"+name+"/stops", `{"onBehalfOf":"slack:T01ABC/UMAINT"}`)); got != "not_running" {
			t.Fatalf("stop ended %s: got %q, want not_running", name, got)
		}
		if got := getActTask(t, env.client, name); got.Annotations[factoryv1.AnnotationStop] != "" {
			t.Fatalf("ended task %s annotated: %v", name, got.Annotations)
		}
	}
	if got := actResult(t, postAct(t, env.srv, "slack", "/v1/tasks/9buqdlot/stops", `{"onBehalfOf":"slack:T01ABC/UMAINT"}`)); got != "unknown_task" {
		t.Fatalf("unknown task: got %q, want unknown_task", got)
	}
}

func TestRetryOnlyEscalated(t *testing.T) {
	modelHeld := makeActTask("3buqdlot", factoryv1.PhaseQueued, "model_unavailable_quota")
	modelHeld.Status.ModelHolds = 1
	running := makeActTask("4buqdlot", factoryv1.PhaseImplementing, "")
	ended := makeActTask("5buqdlot", factoryv1.PhaseStopped, "stopped_by_label")
	escalated := makeActTask("6buqdlot", factoryv1.PhaseEscalated, "agent_error")
	env := newActEnv(t, modelHeld, running, ended, escalated)

	for _, name := range []string{"3buqdlot", "4buqdlot", "5buqdlot"} {
		if got := actResult(t, postAct(t, env.srv, "slack", "/v1/tasks/"+name+"/retries", `{"onBehalfOf":"slack:T01ABC/UMAINT"}`)); got != "not_escalated" {
			t.Fatalf("retry %s: got %q, want not_escalated", name, got)
		}
		if tk := getActTask(t, env.client, name); len(tk.Annotations) != 0 {
			t.Fatalf("non-escalated task %s annotated: %v", name, tk.Annotations)
		}
	}
	if got := actResult(t, postAct(t, env.srv, "slack", "/v1/tasks/6buqdlot/retries", `{"onBehalfOf":"slack:T01ABC/UMAINT"}`)); got != "retried" {
		t.Fatalf("retry escalated: got %q, want retried", got)
	}
}

func TestRetryMeansFactoryRetryComment(t *testing.T) {
	// Drive a scoper task into Escalated(needs_author) with a first assumption, then retry through
	// POST /v1/tasks/{task}/retries and verify it proceeds identically to a /factory retry comment.
	raw := &factoryv1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "3buqdlot",
			Namespace:         "agent-system",
			CreationTimestamp: metav1.NewTime(actNow.Add(-10 * time.Minute)),
		},
		Spec: factoryv1.TaskSpec{
			Source: factoryv1.Source{
				Kind:          "issue",
				Ref:           actRepo + "#7",
				Key:           "k-3buqdlot",
				RequestedBy:   "github:Smana",
				Trust:         "untrusted",
				ContentSHA256: strings.Repeat("b", 64),
			},
			Repository: actRepo,
			Issue:      7,
			Text:       "# Fix the link\n\n## Acceptance check\nthe link resolves",
			DataClass:  "public",
		},
	}
	env := newActEnv(t, raw)

	// Reconcile into the scoper run, then record a question handoff with a first assumption.
	reconcileTask(t, env.rec, "3buqdlot", 3)
	var scoperRunID string
	for id, sp := range env.runs.specs {
		if sp.Role == "triager" {
			scoperRunID = id
		}
	}
	if scoperRunID == "" {
		t.Fatal("expected scoper run to start")
	}
	sc := envelope.Scope{
		State:       "question",
		Task:        "Fix the link to runbook 07 and change nothing else.",
		Touches:     []string{"docs/user-guide.md"},
		Plan:        []string{"Point the link at runbook 08."},
		Question:    "Which dashboard should the panel move to?",
		Assumptions: []string{"first assumption: agent-run, where the per-run cost panels are."},
		Acceptance:  []envelope.Acceptance{{Check: "task docs", Proves: "every relref resolves"}},
	}
	env.roomLog.evs = append(env.roomLog.evs,
		envelope.Event{
			Seq:   1,
			RunID: scoperRunID,
			Type:  envelope.Handoff,
			Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + scoperRunID, Role: "triager"},
			Payload: envelope.Must(envelope.HandoffPayload{
				FromRole: "triager",
				ToRole:   "implementer",
				Summary:  "Scoped.",
				Commit:   strings.Repeat("c", 40),
				Scope:    &sc,
			}),
		},
		envelope.Event{
			Seq:    2,
			RunID:  scoperRunID,
			Type:   envelope.StateChanged,
			Origin: envelope.OriginBroker,
			Actor:  envelope.Actor{Kind: envelope.ActorSystem, ID: "system:room-broker"},
			Payload: envelope.StatePayload("run_phase", map[string]any{
				"phase":  "Succeeded",
				"reason": "agent_finished",
			}),
		},
	)
	r0 := env.runs.runs[scoperRunID]
	r0.Phase = "Succeeded"
	env.runs.runs[scoperRunID] = r0

	tk := reconcileTask(t, env.rec, "3buqdlot", 1)
	if tk.Status.Phase != factoryv1.PhaseEscalated || tk.Status.Reason != "needs_author" {
		t.Fatalf("expected Escalated/needs_author, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}

	// Retry via the sponsored route.
	if got := actResult(t, postAct(t, env.srv, "slack", "/v1/tasks/3buqdlot/retries", `{"onBehalfOf":"slack:T01ABC/UMAINT"}`)); got != "retried" {
		t.Fatalf("retry result: got %q, want retried", got)
	}

	// Two reconcile passes: Escalated -> Queued -> Implementing on the scoper's first assumption.
	tk = reconcileTask(t, env.rec, "3buqdlot", 2)
	if tk.Status.Phase != factoryv1.PhaseImplementing || tk.Status.Scope == nil || tk.Status.Scope.State != "assumed" {
		t.Fatalf("after retry reconcile: phase=%s scope=%+v", tk.Status.Phase, tk.Status.Scope)
	}
	if tk.Status.Retries != 1 || tk.Status.RetriedBy != "github:smana" {
		t.Fatalf("retries=%d retriedBy=%q", tk.Status.Retries, tk.Status.RetriedBy)
	}
	var implSpec *runs.Spec
	for id, sp := range env.runs.specs {
		if id != scoperRunID {
			cp := sp
			implSpec = &cp
		}
	}
	if implSpec == nil || implSpec.Role != "implementer" || !strings.Contains(implSpec.TaskText, "first assumption") {
		t.Fatalf("expected implementer run carrying first assumption, got %+v", implSpec)
	}
	if !slices.Contains(env.forge.Removed(7), "factory/needs-author") {
		t.Fatalf("expected factory/needs-author removed, got %v", env.forge.Removed(7))
	}
	if say := strings.Join(env.forge.Comments(7), "\n"); !strings.Contains(say, "is retrying, as @smana asked") {
		t.Fatalf("expected retry narration, got %q", say)
	}
}
