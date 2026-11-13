// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
)

const (
	ns       = "agents"
	taskID   = "3kq7x2ma"
	room     = "3kq7x2ma"
	runID    = "7f3cq2xz"
	otherRun = "4abc2def"
	repo     = "owner/repo"
	canary   = "mkc-0123456789abcdef0123"
	agents   = "ogenki-agents[bot]"
	factory  = "ogenki-agent-factory[bot]"
	issueMD  = "## What is wrong\nx\n## What it should be\ny\n## Acceptance check\nz\n"
)

// fakeRooms is the broker's log for one room: requests seeded by the test, and the applier's
// answers stamped system:factory as the broker stamps them, so a later tick reads them back.
type fakeRooms struct {
	mu      sync.Mutex
	events  []envelope.Event
	answers []answer
	failOut int // the next failOut Output calls fail
	page    int // EventsSince's cap per call, as rooms.Client's 10,000; 0 is none
	now     func() time.Time
}

func (f *fakeRooms) LastSeq(context.Context, string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.events)), nil
}

type answer struct {
	kind envelope.MessageKind
	rec  envelope.OutputRecord
}

func (f *fakeRooms) add(e envelope.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e.Seq = int64(len(f.events) + 1)
	e.RoomID = room
	f.events = append(f.events, e)
}

func (f *fakeRooms) EventsSince(_ context.Context, _ string, after int64, keep func(envelope.Event) bool) ([]envelope.Event, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []envelope.Event
	cursor, read := after, 0
	for _, e := range f.events {
		if e.Seq > after && (f.page == 0 || read < f.page) {
			cursor = e.Seq
			read++
			if keep == nil || keep(e) {
				out = append(out, e)
			}
		}
	}
	return out, cursor, nil
}

func (f *fakeRooms) Output(_ context.Context, r, run string, kind envelope.MessageKind, rec envelope.OutputRecord) error {
	if err := rec.Validate(kind); err != nil {
		return err
	}
	f.mu.Lock()
	if f.failOut > 0 {
		f.failOut--
		f.mu.Unlock()
		return errors.New("broker: 503")
	}
	f.answers = append(f.answers, answer{kind, rec})
	f.mu.Unlock()
	raw, _ := json.Marshal(envelope.MessagePayload{Kind: kind, Output: &rec})
	f.add(envelope.Event{RunID: run, Type: envelope.Message, Origin: envelope.OriginClient,
		Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: envelope.FactoryActor}, TS: f.now(), Payload: raw})
	_ = r
	return nil
}

func (f *fakeRooms) got() []answer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]answer(nil), f.answers...)
}

type fakeRuns map[string]runs.Run

func (f fakeRuns) Get(_ context.Context, id string) (runs.Run, bool, error) {
	r, ok := f[id]
	return r, ok, nil
}

// scanner is T3's gitleaks half: an AWS access key id is a secret.
type scanner struct{}

func (scanner) String(s string) (string, []string) {
	if strings.Contains(s, "AKIA") {
		return s, []string{"aws-access-token"}
	}
	return s, nil
}

type classifier struct{ hit bool }

func (c classifier) Injection(context.Context, []string) (bool, error) { return c.hit, nil }

type latency struct {
	mu   sync.Mutex
	seen map[string][]time.Duration
	t5   []string
}

// HardeningT5 records each T5 call as "type result drill" (HR39).
func (l *latency) HardeningT5(_ context.Context, typ, result string, drill bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.t5 = append(l.t5, fmt.Sprintf("%s %s %v", typ, result, drill))
}

func (l *latency) HardeningOutput(_ context.Context, typ string, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string][]time.Duration{}
	}
	l.seen[typ] = append(l.seen[typ], d)
}

type rig struct {
	a        *Applier
	rooms    *fakeRooms
	pub, fac *forgetest.Fake
	log      *bytes.Buffer
	now      time.Time
	metrics  *latency
	c        client.Client
}

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// newRig is one task with a live implementer run, every type enforced, the forge fake as the
// publisher (the agents App) and another as the factory App.
func newRig(t *testing.T, issue int, mutate ...func(*Applier)) *rig {
	t.Helper()
	r := &rig{now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC), log: &bytes.Buffer{}, metrics: &latency{}}
	r.rooms = &fakeRooms{now: func() time.Time { return r.now }}
	r.pub, r.fac = forgetest.NewFake(), forgetest.NewFake()
	r.pub.Author = agents
	r.pub.Now, r.fac.Now = func() time.Time { return r.now }, func() time.Time { return r.now }
	started := metav1.NewTime(r.now.Add(-time.Hour))
	task := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: taskID, Namespace: ns},
		Spec: v1alpha1.TaskSpec{Repository: repo, Issue: issue, Text: "fix it"},
		Status: v1alpha1.TaskStatus{RoomRef: room, Runs: []v1alpha1.RunRecord{
			{ID: runID, Role: "implementer", Started: &started}, {ID: otherRun, Role: "reviewer", Started: &started}}}}
	r.c = fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(task).WithStatusSubresource(task).Build()
	r.a = &Applier{Reader: r.c, Namespace: ns, Rooms: r.rooms,
		Runs:      fakeRuns{runID: {ID: runID, Canary: canary, Model: "agent-default"}},
		Publisher: func(context.Context, string) (Publisher, error) { return r.pub, nil },
		Factory:   func(string) Filer { return r.fac },
		Checker: func(string) hardening.Checker {
			return hardening.Checker{Secrets: scanner{}, LabelAllowlist: []string{"documentation", "needs-human"}}
		},
		Enforced: func(string) []hardening.OutputType {
			return []hardening.OutputType{hardening.PR, hardening.Comment, hardening.Labels, hardening.Issue}
		},
		AgentsLogin: agents, FactoryLogin: factory, Now: func() time.Time { return r.now }, Metrics: r.metrics,
		Log: slog.New(slog.NewTextHandler(r.log, nil))}
	for _, m := range mutate {
		m(r.a)
	}
	return r
}

var outputSeq int

// request seeds one output_requested from run, stamped as the broker stamps the run's own agent.
func (r *rig) request(t *testing.T, run string, typ hardening.OutputType, payload any) string {
	t.Helper()
	return r.requestAs(t, run, envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + run}, typ, payload)
}

func (r *rig) requestAs(t *testing.T, run string, actor envelope.Actor, typ hardening.OutputType, payload any) string {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	outputSeq++
	id := fmt.Sprintf("%016x", outputSeq)
	rec := envelope.OutputRecord{ID: id, Type: string(typ), Digest: hardening.Digest(body), Bytes: len(body), Payload: body}
	raw, _ := json.Marshal(envelope.MessagePayload{Kind: envelope.KindOutputRequested, Output: &rec})
	r.rooms.add(envelope.Event{RunID: run, Type: envelope.Message, Origin: envelope.OriginClient, Actor: actor,
		TS: r.now.Add(-1500 * time.Millisecond), Payload: raw})
	return id
}

func (r *rig) tick(t *testing.T) {
	t.Helper()
	if err := r.a.Tick(t.Context()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// only is the one answer to id, failing unless there is exactly one.
func (r *rig) only(t *testing.T, id string) answer {
	t.Helper()
	var out []answer
	for _, a := range r.rooms.got() {
		if a.rec.ID == id {
			out = append(out, a)
		}
	}
	if len(out) != 1 {
		t.Fatalf("output %s has %d answers: %+v", id, len(out), out)
	}
	return out[0]
}

func prPayload(title string) hardening.PRPayload {
	return hardening.PRPayload{Title: title, Body: "Fixes the broken link in the guide.", Draft: true}
}

// TestApplierApplies: each enforced type is applied through its identity and answered once with
// the object and the sanitised payload's digest.
func TestApplierApplies(t *testing.T) {
	t.Run("pr opens, then edits", func(t *testing.T) {
		r := newRig(t, 7)
		id := r.request(t, runID, hardening.PR, prPayload("docs: fix a link"))
		r.tick(t)
		got := r.only(t, id)
		if got.kind != envelope.KindOutputApplied {
			t.Fatalf("answer %+v", got)
		}
		n, _ := r.pub.PullRequestForBranch(t.Context(), "agent/"+taskID)
		pr, _ := r.pub.PullRequest(t.Context(), n)
		if got.rec.Object != pr.URL || pr.Author != agents || pr.Title != "docs: fix a link" {
			t.Errorf("object %q, pr %+v", got.rec.Object, pr)
		}
		if !strings.HasSuffix(pr.Body, "Agent-Model: agent-default\nAgent-Output: "+id+"\n") || !strings.Contains(pr.Body, "Agent-Run: "+runID) ||
			!strings.Contains(pr.Body, "Agent-Task-URL: https://github.com/owner/repo/issues/7") {
			t.Errorf("the factory's footer: %q", pr.Body)
		}
		if calls := strings.Join(r.pub.Calls(), ";"); !strings.Contains(calls, "create-pr main agent/"+taskID+" draft=true") {
			t.Errorf("calls %s", calls)
		}
		sanitised, _ := json.Marshal(prPayload("docs: fix a link"))
		if got.rec.Digest != hardening.Digest(sanitised) || got.rec.Payload != nil {
			t.Errorf("digest %s, want the sanitised payload's", got.rec.Digest)
		}
		id2 := r.request(t, runID, hardening.PR, prPayload("docs: fix two links"))
		r.tick(t)
		if a := r.only(t, id2); a.kind != envelope.KindOutputApplied || a.rec.Object != pr.URL {
			t.Fatalf("edit answer %+v", a)
		}
		if pr, _ = r.pub.PullRequest(t.Context(), n); pr.Title != "docs: fix two links" || !strings.HasSuffix(pr.Body, "Agent-Output: "+id2+"\n") {
			t.Errorf("edited %+v", pr)
		}
		if strings.Count(strings.Join(r.pub.Calls(), ";"), "create-pr") != 1 {
			t.Error("a second pr opened another pull request")
		}
	})
	for _, target := range []string{hardening.TargetPR, hardening.TargetIssue} {
		t.Run("comment on the "+target, func(t *testing.T) {
			r := newRig(t, 7)
			r.request(t, runID, hardening.PR, prPayload("docs: fix a link"))
			r.tick(t)
			id := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: target, Body: "Done <!-- hidden -->."})
			r.tick(t)
			a := r.only(t, id)
			n := 7
			want := "https://github.com/owner/repo/issues/7#issuecomment-"
			if target == hardening.TargetPR {
				n, _ = r.pub.PullRequestForBranch(t.Context(), "agent/"+taskID)
				want = fmt.Sprintf("https://github.com/owner/repo/pull/%d#issuecomment-", n)
			}
			if a.kind != envelope.KindOutputApplied || !strings.HasPrefix(a.rec.Object, want) {
				t.Fatalf("answer %+v, want an object under %s", a, want)
			}
			cs := r.pub.Comments(n)
			if len(cs) != 1 || cs[0] != "Done .\n\n"+hardening.Marker(id) {
				t.Errorf("comments %q: T2's body, then the marker", cs)
			}
		})
	}
	t.Run("labels on the pr", func(t *testing.T) {
		r := newRig(t, 7)
		r.request(t, runID, hardening.PR, prPayload("docs: fix a link"))
		r.tick(t)
		id := r.request(t, runID, hardening.Labels, hardening.LabelsPayload{Add: []string{"documentation"}})
		r.tick(t)
		n, _ := r.pub.PullRequestForBranch(t.Context(), "agent/"+taskID)
		if a := r.only(t, id); a.kind != envelope.KindOutputApplied || !strings.HasSuffix(a.rec.Object, fmt.Sprintf("/pull/%d", n)) {
			t.Fatalf("answer %+v", a)
		}
		if got := r.pub.Added(n); len(got) != 1 || got[0] != "documentation" {
			t.Errorf("labels %q", got)
		}
	})
}

// TestApplierRefuses: each refusal is answered once and reaches no forge.
func TestApplierRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		issue   int
		setup   func(*testing.T, *rig)
		typ     hardening.OutputType
		payload any
		want    hardening.Refusal
		alert   bool
	}{
		"a seventh pr": {issue: 7, setup: func(t *testing.T, r *rig) {
			for range 6 {
				r.request(t, runID, hardening.PR, prPayload("docs: fix a link"))
			}
		}, typ: hardening.PR, payload: prPayload("docs: once more"), want: hardening.RefuseCap},
		"the canary in a comment": {issue: 7, typ: hardening.Comment, alert: true,
			payload: hardening.CommentPayload{Target: hardening.TargetIssue, Body: "the secret is " + canary}, want: hardening.RefuseCanary},
		"an AWS key in a comment": {issue: 7, typ: hardening.Comment,
			payload: hardening.CommentPayload{Target: hardening.TargetIssue, Body: "key AKIAIOSFODNN7EXAMPLE"}, want: hardening.RefuseSecret},
		"labels with no pr": {issue: 7, typ: hardening.Labels,
			payload: hardening.LabelsPayload{Add: []string{"documentation"}}, want: hardening.RefuseTarget},
		"a comment on a text task's issue": {typ: hardening.Comment,
			payload: hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"}, want: hardening.RefuseTarget},
		"a type the run is not served": {issue: 7, typ: hardening.Push,
			payload: hardening.PushPayload{Bundle: []byte("x")}, want: hardening.RefuseType},
		"a label off the allowlist": {issue: 7, typ: hardening.Labels,
			payload: hardening.LabelsPayload{Add: []string{"merge-now"}}, want: hardening.RefuseSchema},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, tc.issue)
			if tc.setup != nil {
				tc.setup(t, r)
				r.tick(t)
			}
			before := len(r.pub.Calls()) + len(r.fac.Calls())
			id := r.request(t, runID, tc.typ, tc.payload)
			r.tick(t)
			a := r.only(t, id)
			if a.kind != envelope.KindOutputRefused || a.rec.Reason != string(tc.want) {
				t.Fatalf("answer %+v, want refused %s", a, tc.want)
			}
			if after := len(r.pub.Calls()) + len(r.fac.Calls()); after != before {
				t.Errorf("a refusal reached the forge: %q", r.pub.Calls())
			}
			if alert := strings.Contains(r.log.String(), "canary leaked"); alert != tc.alert {
				t.Errorf("canary alert %t, want %t:\n%s", alert, tc.alert, r.log.String())
			}
			if strings.Contains(r.log.String(), canary) || strings.Contains(r.log.String(), "AKIA") {
				t.Errorf("payload text reached the log:\n%s", r.log.String())
			}
		})
	}
}

// A payload the broker's gitleaks pass rewrote held a secret: it is refused, never published.
func TestApplierRefusesARedactedRequest(t *testing.T) {
	r := newRig(t, 7)
	id := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "key [REDACTED]"})
	r.rooms.mu.Lock()
	r.rooms.events[len(r.rooms.events)-1].Redactions = []string{"aws-access-token"}
	r.rooms.mu.Unlock()
	r.tick(t)
	if a := r.only(t, id); a.kind != envelope.KindOutputRefused || a.rec.Reason != string(hardening.RefuseSecret) {
		t.Fatalf("answer %+v", a)
	}
	if len(r.pub.Comments(7)) != 0 {
		t.Error("a redacted payload was published")
	}
}

// TestApplierExactlyOnce: a restart answers nothing twice, and a write whose answer was lost is
// found by its marker and answered without writing again (HR16).
func TestApplierExactlyOnce(t *testing.T) {
	t.Run("a restart", func(t *testing.T) {
		r := newRig(t, 7)
		ids := []string{r.request(t, runID, hardening.PR, prPayload("docs: fix a link")),
			r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})}
		r.tick(t)
		r.tick(t)
		calls := len(r.pub.Calls())
		restarted := *r.a
		restarted.rooms = nil
		if err := restarted.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			r.only(t, id)
		}
		if len(r.pub.Calls()) != calls || len(r.pub.Comments(7)) != 1 {
			t.Errorf("a restart wrote again: %q", r.pub.Calls())
		}
		// The restarted applier rebuilt the caps from the answers: five pr remain.
		for range 5 {
			r.request(t, runID, hardening.PR, prPayload("docs: again"))
		}
		last := r.request(t, runID, hardening.PR, prPayload("docs: one too many"))
		if err := restarted.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if a := r.only(t, last); a.rec.Reason != string(hardening.RefuseCap) {
			t.Errorf("the seventh pr after a restart: %+v", a)
		}
	})
	for name, tc := range map[string]struct {
		typ     hardening.OutputType
		payload any
		written func(r *rig) int
	}{
		"a comment": {typ: hardening.Comment, payload: hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"},
			written: func(r *rig) int { return len(r.pub.Comments(7)) }},
		"a pr": {typ: hardening.PR, payload: prPayload("docs: fix a link"),
			written: func(r *rig) int { return strings.Count(strings.Join(r.pub.Calls(), ";"), "-pr ") }},
		"an issue": {typ: hardening.Issue, payload: hardening.IssuePayload{Title: "docs: a follow-up", Body: issueMD},
			written: func(r *rig) int { return len(r.fac.Created()) }},
	} {
		t.Run("a crash after "+name+" was written", func(t *testing.T) {
			r := newRig(t, 7)
			id := r.request(t, runID, tc.typ, tc.payload)
			r.rooms.failOut = 1 // written on GitHub, the answer lost
			if err := r.a.Tick(t.Context()); err == nil {
				t.Fatal("a lost answer is the tick's error")
			}
			restarted := *r.a
			restarted.rooms = nil
			if err := restarted.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if a := r.only(t, id); a.kind != envelope.KindOutputApplied {
				t.Fatalf("answer %+v", a)
			}
			if n := tc.written(r); n != 1 {
				t.Errorf("written %d times", n)
			}
		})
	}
}

// TestApplierReadsOnlyStampedRequests: a request posted by a human, or by another run's agent in
// this run's name, is not a request.
func TestApplierReadsOnlyStampedRequests(t *testing.T) {
	r := newRig(t, 7)
	human := r.requestAs(t, runID, envelope.Actor{Kind: envelope.ActorHuman, ID: "human:dev1"}, hardening.Comment,
		hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})
	other := r.requestAs(t, runID, envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + otherRun}, hardening.Comment,
		hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})
	reviewer := r.request(t, otherRun, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})
	r.tick(t)
	for _, a := range r.rooms.got() {
		if a.rec.ID == human || a.rec.ID == other || a.rec.ID == reviewer {
			t.Errorf("an unstamped or non-writer request was answered: %+v", a)
		}
	}
	if len(r.pub.Comments(7)) != 0 {
		t.Error("an unstamped request was applied")
	}
}

// TestApplierAuditTypes: a type that is not enforced is refused output_type: requests arrive only
// for served types, so this guards the gate.
func TestApplierAuditTypes(t *testing.T) {
	r := newRig(t, 7, func(a *Applier) {
		a.Enforced = func(string) []hardening.OutputType { return []hardening.OutputType{hardening.PR} }
	})
	id := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})
	r.tick(t)
	if a := r.only(t, id); a.kind != envelope.KindOutputRefused || a.rec.Reason != string(hardening.RefuseType) {
		t.Fatalf("answer %+v", a)
	}
	if len(r.pub.Comments(7)) != 0 {
		t.Error("an unenforced type was applied")
	}
}

// TestApplierLatency: the wait from the request's timestamp to its answer is observed per type.
func TestApplierLatency(t *testing.T) {
	r := newRig(t, 7)
	r.request(t, runID, hardening.PR, prPayload("docs: fix a link"))
	r.tick(t)
	r.metrics.mu.Lock()
	defer r.metrics.mu.Unlock()
	if got := r.metrics.seen["pr"]; len(got) != 1 || got[0] != 1500*time.Millisecond {
		t.Errorf("observed %v", r.metrics.seen)
	}
}

// TestApplierT5Audit: a classifier hit on a pr is recorded on its answer, and it is still applied.
func TestApplierT5Audit(t *testing.T) {
	r := newRig(t, 7, func(a *Applier) {
		a.Checker = func(string) hardening.Checker {
			return hardening.Checker{Secrets: scanner{}, Classifier: classifier{hit: true},
				Layers: hardening.Layers{Classifier: hardening.ModeEnforce}} // T5 is at most audit, whatever is asked
		}
	})
	id := r.request(t, runID, hardening.PR, prPayload("docs: fix a link"))
	r.tick(t)
	a := r.only(t, id)
	if a.kind != envelope.KindOutputApplied || len(a.rec.Audit) != 1 ||
		a.rec.Audit[0] != (envelope.OutputFinding{Layer: "t5_classifier", Reason: "injection_suspected"}) {
		t.Fatalf("answer %+v", a)
	}
}

func (r *rig) finish(t *testing.T, ago time.Duration) {
	t.Helper()
	var task v1alpha1.Task
	if err := r.c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: taskID}, &task); err != nil {
		t.Fatal(err)
	}
	ended := metav1.NewTime(r.now.Add(-ago))
	task.Status.Runs[0].Finished = &ended
	if err := r.c.Status().Update(t.Context(), &task); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) stop(t *testing.T, on bool) {
	t.Helper()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: killswitch.ConfigMap, Namespace: ns}}
	var err error
	if on {
		err = r.c.Create(t.Context(), cm)
	} else {
		err = r.c.Delete(t.Context(), cm)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// Nothing is applied under the stop. A run ended within AnswerWait is still answered; one past it
// gets output_expired for whatever is still unanswered, stop or not, and is then no longer read.
func TestApplierWindowAndStop(t *testing.T) {
	r := newRig(t, 7)
	id := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})
	r.stop(t, true)
	r.tick(t)
	if len(r.rooms.got()) != 0 {
		t.Fatal("answered under the stop")
	}
	r.finish(t, time.Minute)
	r.tick(t)
	if len(r.rooms.got()) != 0 {
		t.Fatal("answered under the stop within the answer wait")
	}
	r.finish(t, DefaultAnswerWait+time.Second)
	r.tick(t)
	if a := r.only(t, id); a.kind != envelope.KindOutputRefused || a.rec.Reason != string(hardening.RefuseExpired) {
		t.Fatalf("a request past the answer wait under the stop: %+v", a)
	}
	if len(r.pub.Comments(7)) != 0 {
		t.Error("written under the stop")
	}

	r = newRig(t, 7)
	id = r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})
	r.finish(t, time.Minute)
	r.tick(t)
	if a := r.only(t, id); a.kind != envelope.KindOutputApplied {
		t.Fatalf("a run ended a minute ago is still answered: %+v", a)
	}
	r.finish(t, 3*DefaultAnswerWait)
	late := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "late"})
	r.tick(t)
	for _, a := range r.rooms.got() {
		if a.rec.ID == late {
			t.Errorf("a run long ended is still read: %+v", a)
		}
	}
}

// stopAfter is a Reader whose kill switch engages after its first read: a stop that lands between
// the tick's start and the write.
type stopAfter struct {
	client.Client
	mu    sync.Mutex
	reads int
}

func (s *stopAfter) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.ConfigMap); ok && key.Name == killswitch.ConfigMap {
		s.mu.Lock()
		s.reads++
		n := s.reads
		s.mu.Unlock()
		if n > 1 {
			return nil // found: engaged
		}
	}
	return s.Client.Get(ctx, key, obj, opts...)
}

// The kill switch is read again before each write, not once per tick.
func TestApplierStopBeforeTheWrite(t *testing.T) {
	r := newRig(t, 7)
	r.a.Reader = &stopAfter{Client: r.c}
	r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})
	_ = r.a.Tick(t.Context())
	if len(r.pub.Comments(7)) != 0 || len(r.rooms.got()) != 0 {
		t.Fatalf("written after the stop engaged: %q", r.pub.Comments(7))
	}
}

// The catch-up read pages to the room's last seq before answering anything: a request whose answer
// lies past the first page is never applied again (HR16).
func TestApplierCatchesUpBeforeAnswering(t *testing.T) {
	r := newRig(t, 7)
	r.rooms.page = 3
	id := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})
	for range 5 {
		r.rooms.add(envelope.Event{RunID: runID, Type: envelope.Message, Origin: envelope.OriginHarness,
			Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + runID}, TS: r.now, Payload: json.RawMessage(`{"kind":"chat","text":"x"}`)})
	}
	// An earlier leader applied it and answered: the answer sits past the first page.
	if err := r.rooms.Output(t.Context(), room, runID, envelope.KindOutputApplied, envelope.OutputRecord{ID: id, Type: "comment",
		Digest: strings.Repeat("a", 64), Object: "https://github.com/owner/repo/issues/7#issuecomment-1"}); err != nil {
		t.Fatal(err)
	}
	r.tick(t)
	if len(r.pub.Comments(7)) != 0 {
		t.Fatalf("a request answered past the first page was applied again: %q", r.pub.Comments(7))
	}
	if n := len(r.rooms.got()); n != 1 {
		t.Errorf("%d answers, want the earlier leader's one", n)
	}
	// And a new request after the catch-up is answered.
	next := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "again"})
	r.tick(t)
	r.tick(t)
	if a := r.only(t, next); a.kind != envelope.KindOutputApplied {
		t.Fatalf("after the catch-up: %+v", a)
	}
}

// A refusal no retry changes is answered output_unavailable instead of blocking the run's queue,
// and the next request is still answered.
func TestApplierRefusesUnappliable(t *testing.T) {
	r := newRig(t, 7, func(a *Applier) { a.Factory = func(string) Filer { return nil } })
	issue := r.request(t, runID, hardening.Issue, hardening.IssuePayload{Title: "docs: a follow-up", Body: issueMD})
	r.pub.FailCreates(&forge.PermanentError{Status: 422, Err: errors.New("validation failed")})
	pr := r.request(t, runID, hardening.PR, prPayload("docs: fix a link"))
	comment := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})
	r.tick(t)
	for _, id := range []string{issue, pr} {
		if a := r.only(t, id); a.kind != envelope.KindOutputRefused || a.rec.Reason != string(hardening.RefuseUnavailable) {
			t.Errorf("answer %+v, want output_unavailable", a)
		}
	}
	if a := r.only(t, comment); a.kind != envelope.KindOutputApplied {
		t.Errorf("the queue after them: %+v", a)
	}
}

// A request whose own write is already on GitHub is answered applied without being checked again,
// so it can never get both answers.
func TestApplierFoundWriteIsApplied(t *testing.T) {
	r := newRig(t, 7, func(a *Applier) {
		a.Checker = func(string) hardening.Checker { return hardening.Checker{} } // no scanner: T3 would refuse everything
	})
	id := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})
	if _, err := r.pub.CommentWithID(t.Context(), 7, "done\n\n"+hardening.Marker(id)); err != nil {
		t.Fatal(err)
	}
	r.tick(t)
	if a := r.only(t, id); a.kind != envelope.KindOutputApplied || !strings.Contains(a.rec.Object, "#issuecomment-") {
		t.Fatalf("answer %+v", a)
	}
	if len(r.pub.Comments(7)) != 1 {
		t.Errorf("written again: %q", r.pub.Comments(7))
	}
}

// One open and five edits per run: a closed pull request on the branch is not replaced, and a
// pull request someone else opened on it is not edited. Both are output_target, not output_cap:
// the request spent its cap, and a cap refusal rebuilt from the log would not (#488's review).
func TestApplierPRCapAndAuthor(t *testing.T) {
	for name, tc := range map[string]struct {
		pr   forge.PR
		want hardening.Refusal
	}{
		"the branch's pull request was closed": {forge.PR{Number: 40, State: "CLOSED", Author: agents, HeadRef: "agent/" + taskID}, hardening.RefuseTarget},
		"the branch's pull request was merged": {forge.PR{Number: 40, State: "MERGED", Author: agents, HeadRef: "agent/" + taskID}, hardening.RefuseTarget},
		"someone else's pull request is open":  {forge.PR{Number: 40, State: "OPEN", Author: "mallory", HeadRef: "agent/" + taskID}, hardening.RefuseTarget},
		// #491's review B-1: a run that still holds pull_requests: write opened it directly; editing
		// it would launder that write into an applied one.
		"an agents-App pull request no applied output opened": {forge.PR{Number: 40, State: "OPEN", Author: agents, HeadRef: "agent/" + taskID,
			Body: "Fixes #7\n\n---\nAgent-Run: " + runID}, hardening.RefuseTarget},
		"one whose footer names another task's output": {forge.PR{Number: 40, State: "OPEN", Author: agents, HeadRef: "agent/" + taskID,
			Body: "Fixes #7\n\n---\nAgent-Output: 00000000000000ff"}, hardening.RefuseTarget},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, 7)
			tc.pr.URL = "https://github.com/owner/repo/pull/40"
			r.pub.SetPR(tc.pr)
			r.pub.SetBranch("agent/"+taskID, 40)
			id := r.request(t, runID, hardening.PR, prPayload("docs: fix a link"))
			r.tick(t)
			if a := r.only(t, id); a.kind != envelope.KindOutputRefused || a.rec.Reason != string(tc.want) {
				t.Fatalf("answer %+v, want %s", a, tc.want)
			}
			if calls := strings.Join(r.pub.Calls(), ";"); strings.Contains(calls, "-pr ") {
				t.Errorf("written: %s", calls)
			}
		})
	}
}

// A pull request an earlier run's applied output opened is the task's: a later run's output_pr
// edits it.
func TestApplierEditsAnEarlierRunsPR(t *testing.T) {
	r := newRig(t, 7)
	const earlier = "00000000000000e1"
	r.rooms.add(appliedEvent(t, "zzzzzzz2", envelope.OutputRecord{ID: earlier, Type: string(hardening.PR),
		Digest: strings.Repeat("a", 64), Object: "https://github.com/owner/repo/pull/40"}))
	r.pub.SetPR(forge.PR{Number: 40, State: "OPEN", Author: agents, HeadRef: "agent/" + taskID,
		URL: "https://github.com/owner/repo/pull/40", Body: "Fixes #7\n\n---\nAgent-Output: " + earlier})
	r.pub.SetBranch("agent/"+taskID, 40)
	id := r.request(t, runID, hardening.PR, prPayload("docs: fix a link"))
	r.tick(t)
	if a := r.only(t, id); a.kind != envelope.KindOutputApplied {
		t.Fatalf("answer %+v", a)
	}
}

// appliedEvent is an output_applied as the factory posts it, for run.
func appliedEvent(t *testing.T, run string, rec envelope.OutputRecord) envelope.Event {
	t.Helper()
	raw, err := json.Marshal(envelope.MessagePayload{Kind: envelope.KindOutputApplied, Output: &rec})
	if err != nil {
		t.Fatal(err)
	}
	return envelope.Event{RunID: run, Type: envelope.Message, Origin: envelope.OriginClient,
		Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: envelope.FactoryActor}, Payload: raw}
}

// HR39: a text output refused by T3 is still read by T5 in audit, its finding recorded on the
// refusal, and every T5 call is counted by type, result and whether the task is a drill's.
func TestT5CounterOnRefusals(t *testing.T) {
	r := newRig(t, 7, func(a *Applier) {
		a.Checker = func(string) hardening.Checker {
			return hardening.Checker{Secrets: scanner{}, Classifier: classifier{hit: true},
				Layers: hardening.Layers{Classifier: hardening.ModeAudit}}
		}
	})
	var task v1alpha1.Task
	if err := r.c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: taskID}, &task); err != nil {
		t.Fatal(err)
	}
	task.Labels = map[string]string{v1alpha1.LabelDrillInjection: "true"}
	if err := r.c.Update(t.Context(), &task); err != nil {
		t.Fatal(err)
	}
	leaked := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "here: " + canary})
	applied := r.request(t, runID, hardening.PR, prPayload("docs: fix a link"))
	r.tick(t)
	a := r.only(t, leaked)
	if a.kind != envelope.KindOutputRefused || a.rec.Reason != string(hardening.RefuseCanary) || len(a.rec.Audit) != 1 ||
		a.rec.Audit[0] != (envelope.OutputFinding{Layer: "t5_classifier", Reason: "injection_suspected"}) {
		t.Fatalf("the refusal %+v", a)
	}
	if b := r.only(t, applied); b.kind != envelope.KindOutputApplied {
		t.Fatalf("the pr %+v", b)
	}
	r.metrics.mu.Lock()
	defer r.metrics.mu.Unlock()
	if strings.Join(r.metrics.t5, ",") != "comment hit true,pr hit true" {
		t.Fatalf("t5 %q", r.metrics.t5)
	}
}
