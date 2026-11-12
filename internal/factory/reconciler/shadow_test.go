// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
)

const agents = "ogenki-agents[bot]"

// shadowRig is a rig under audit whose run rid(0) pushed twice, opened PR 12, commented once and
// labelled once, all as the agents App inside the run's window.
func shadowRig(t *testing.T, label string) (*rig, *v1alpha1.Task, *bytes.Buffer) {
	t.Helper()
	g := newRig(t)
	g.auditing(hardening.ModeAudit)
	c := g.r.Cfg.Current()
	c.AgentsLogin = agents
	c.Hardening.LabelAllowlist = []string{"documentation"}
	var buf bytes.Buffer
	g.r.Log = slog.New(slog.NewTextHandler(&buf, nil))
	started, finished := metav1.NewTime(now.Add(-10*time.Minute)), metav1.NewTime(now.Add(-time.Minute))
	tk := issueTask("3buqdlot", 7, "# Fix the link")
	tk.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseImplementing, RoomRef: "3buqdlot",
		PullRequest: &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12"},
		Runs:        []v1alpha1.RunRecord{{ID: rid(0), Role: "implementer", Trigger: "initial", Started: &started, Finished: &finished, Audited: true}}}
	g.runs.runs[rid(0)] = runs.Run{ID: rid(0), Role: "implementer", Canary: "mkc-0123456789abcdef0123"}
	in := now.Add(-5 * time.Minute)
	g.f.SetActivity("agent/3buqdlot",
		forge.Push{Type: "push", Actor: agents, Before: strings.Repeat("1", 40), After: strings.Repeat("2", 40), At: in.Add(time.Minute)},
		forge.Push{Type: "branch_creation", Actor: agents, Before: strings.Repeat("0", 40), After: strings.Repeat("1", 40), At: in},
		forge.Push{Type: "push", Actor: "someone-else", Before: strings.Repeat("2", 40), After: strings.Repeat("3", 40), At: in},
		forge.Push{Type: "push", Actor: agents, Before: strings.Repeat("3", 40), After: strings.Repeat("4", 40), At: now.Add(time.Hour)})
	g.f.SetCommit(strings.Repeat("1", 40), "docs: fix the link")
	g.f.SetCommit(strings.Repeat("2", 40), "docs: tidy")
	g.f.SetFiles(map[string]string{"docs/a.md": "old\n"}, map[string]string{"docs/a.md": "old\nnew line\n"})
	g.f.SetPR(forge.PR{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", State: "OPEN", Author: agents,
		Title: "docs: fix the link", Body: "Fixes the dead link in the runbook. Fixes #7\n\n---\nAgent-Room: 3buqdlot\nAgent-Run: " + rid(0) + "\n"})
	g.f.SetComments(12, forge.Comment{ID: 501, Author: agents, Body: "SECRET-BODY done", At: in},
		forge.Comment{ID: 502, Author: "Smana", Body: "thanks", At: in},
		forge.Comment{ID: 503, Author: agents, Body: "before the run", At: now.Add(-time.Hour)})
	g.f.SetEvents(12, forge.LabelEvent{Actor: agents, Label: label, At: in},
		forge.LabelEvent{Actor: "Smana", Label: "factory/ready", At: in})
	return g, tk, &buf
}

func shadowed(g *rig) map[string]envelope.OutputRecord {
	out := map[string]envelope.OutputRecord{}
	for _, e := range g.log.outputs {
		k, r, ok := envelope.ReadOutput(e, e.RunID)
		if ok && k == envelope.KindOutputShadowed {
			out[r.Type+" "+r.Object] = r
		}
	}
	return out
}

// HR1: each write the run made as the agents App inside its window is one output_shadowed record,
// checked as the request it would have sent; the run is marked shadowed, and a second pass posts
// nothing.
func TestShadowPass(t *testing.T) {
	g, tk, _ := shadowRig(t, "documentation")
	g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
	got := shadowed(g)
	commit := "https://github.com/Smana/cloud-native-ref/commit/"
	want := []string{
		"push " + commit + strings.Repeat("1", 40),
		"push " + commit + strings.Repeat("2", 40),
		"pr https://github.com/Smana/cloud-native-ref/pull/12",
		"comment https://github.com/Smana/cloud-native-ref/pull/12#issuecomment-501",
		"labels https://github.com/Smana/cloud-native-ref/pull/12",
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Fatalf("records %q, want %q", keys, want)
	}
	for k, r := range got {
		if r.Reason != "" || r.Payload != nil {
			t.Errorf("%s: %+v, want a clean would_apply with no payload", k, r)
		}
	}
	// The PR is measured without the harness footer (HR4).
	body := "Fixes the dead link in the runbook. Fixes #7"
	if d := got["pr https://github.com/Smana/cloud-native-ref/pull/12"].Digest; d != hardening.Digest([]byte(`{"body":"`+body+`","title":"docs: fix the link"}`)) {
		t.Fatalf("the PR digest is not its footerless payload's: %s", d)
	}
	if !tk.Status.Runs[0].Shadowed {
		t.Fatal("the run is not marked shadowed")
	}
	calls := g.log.outputCalls
	g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
	if g.log.outputCalls != calls {
		t.Fatal("a shadowed run was shadowed again")
	}
	// The replay keys hold too: a pass that lost its Shadowed write posts nothing new.
	tk.Status.Runs[0].Shadowed = false
	g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
	if len(g.log.outputs) != len(want) {
		t.Fatalf("%d records after a replay", len(g.log.outputs))
	}
}

// HR3: a would-refuse is recorded with its bounded reason, and a protected path's approval is a
// would_wait, never a refusal.
func TestShadowPassVerdicts(t *testing.T) {
	g, tk, _ := shadowRig(t, "bug")
	g.f.SetFiles(map[string]string{}, map[string]string{".github/workflows/ci.yaml": "on: push\n"})
	g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
	got := shadowed(g)
	if r := got["labels https://github.com/Smana/cloud-native-ref/pull/12"]; r.Reason != "output_schema" {
		t.Fatalf("an unlisted label: %+v", r)
	}
	if r := got["push https://github.com/Smana/cloud-native-ref/commit/"+strings.Repeat("1", 40)]; r.Reason != "would_wait" {
		t.Fatalf("a CI push: %+v", r)
	}
}

// A shadow error never moves a task: the pass logs it and the next reconcile retries it.
func TestShadowPassNeverMovesTheTask(t *testing.T) {
	for name, breakIt := range map[string]func(g *rig){
		"a forge error":  func(g *rig) { g.f.SetActivityErr("agent/3buqdlot", errors.New("github down")) },
		"a refused post": func(g *rig) { g.log.failOutput = errors.New("broker down") },
		"a diff error":   func(g *rig) { g.f.SetDiffError(errors.New("compare failed")) },
	} {
		t.Run(name, func(t *testing.T) {
			g, tk, buf := shadowRig(t, "documentation")
			breakIt(g)
			phase, reason := tk.Status.Phase, tk.Status.Reason
			g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
			if tk.Status.Phase != phase || tk.Status.Reason != reason || tk.Status.Runs[0].Shadowed {
				t.Fatalf("%s %q shadowed %v", tk.Status.Phase, tk.Status.Reason, tk.Status.Runs[0].Shadowed)
			}
			if !strings.Contains(buf.String(), "shadow pass failed") {
				t.Fatalf("log %s", buf)
			}
			// B1: a write or a source that cannot be read skips itself, never the rest of the run.
			if name != "a refused post" && len(shadowed(g)) != 3 {
				t.Fatalf("%d records posted beside the failing pushes, want the 3 other writes", len(shadowed(g)))
			}
			g.f.SetActivityErr("agent/3buqdlot", nil)
			g.log.failOutput = nil
			g.f.SetDiffError(nil)
			g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
			if !tk.Status.Runs[0].Shadowed || len(shadowed(g)) != 5 {
				t.Fatalf("the retry: shadowed %v, %d records", tk.Status.Runs[0].Shadowed, len(shadowed(g)))
			}
		})
	}
}

// Under off, in the demo profile or with no hardening ports, nothing is read or posted.
func TestShadowPassOffAndDemo(t *testing.T) {
	for name, edit := range map[string]func(g *rig){
		"off":      func(g *rig) { g.r.Cfg.Current().Hardening.Mode = hardening.ModeOff },
		"demo":     func(g *rig) { g.r.Cfg.Current().Demo = true },
		"no ports": func(g *rig) { g.r.Hardening = nil },
	} {
		t.Run(name, func(t *testing.T) {
			g, tk, _ := shadowRig(t, "documentation")
			edit(g)
			g.f.SetActivityErr("agent/3buqdlot", errors.New("must not be read"))
			g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
			if g.log.outputCalls != 0 || tk.Status.Runs[0].Shadowed {
				t.Fatalf("%d posts", g.log.outputCalls)
			}
		})
	}
}

type shadowCounter struct {
	*fakeMetrics
	mu  sync.Mutex
	got []string
}

func (s *shadowCounter) HardeningShadow(_ context.Context, typ, verdict, layer, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, strings.Join([]string{typ, verdict, layer, reason}, " "))
}

// Each record is counted by type, verdict, layer and reason; a would-refuse or would_wait is
// logged with the task, run, type, layer, reason and object, never the payload's text.
func TestShadowMetrics(t *testing.T) {
	g, tk, buf := shadowRig(t, "zz-unlisted")
	m := &shadowCounter{fakeMetrics: g.metrics}
	g.r.Metrics = m
	g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
	slices.Sort(m.got)
	want := []string{"comment would_apply  ", "labels would_refuse t1_schema output_schema", "pr would_apply  ",
		"push would_apply  ", "push would_apply  "}
	if !slices.Equal(m.got, want) {
		t.Fatalf("%q", m.got)
	}
	line := ""
	for l := range strings.SplitSeq(buf.String(), "\n") {
		if strings.Contains(l, "would_refuse") {
			line = l
		}
	}
	for _, kv := range []string{"task.id=3buqdlot", "run.id=" + rid(0), "type=labels", "layer=t1_schema", "reason=output_schema",
		"object=https://github.com/Smana/cloud-native-ref/pull/12"} {
		if !strings.Contains(line, kv) {
			t.Errorf("the line lacks %s: %s", kv, line)
		}
	}
	if strings.Contains(buf.String(), "SECRET-BODY") || strings.Contains(buf.String(), "zz-unlisted") {
		t.Fatalf("payload text in the log: %s", buf)
	}
}

// The reconciler shadows a writer run once it is terminal, before its outcome; under off, the
// run's end is today's.
func TestShadowPassAtRunEnd(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "# Fix the link"))
	g.f.SetIssue(forge.Issue{Number: 7, Author: "stranger", Association: "NONE"})
	g.auditing(hardening.ModeAudit)
	g.r.Cfg.Current().AgentsLogin = agents
	tk := g.reconcile(t, "3buqdlot", 3)
	if tk.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatal(tk.Status.Phase)
	}
	g.runs.set(rid(0), "Running")
	g.f.SetBranch("agent/3buqdlot", 12)
	g.f.SetPR(forge.PR{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", State: "OPEN", NodeID: "PR_1", HeadSHA: "abc",
		Author: agents, Title: "docs: fix the link", Body: "Fixes the dead link in the runbook. Fixes #7"})
	g.reconcile(t, "3buqdlot", 2)
	g.runs.set(rid(0), "Succeeded")
	g.log.end(rid(0), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseAwaitingCI || !tk.Status.Runs[0].Shadowed {
		t.Fatalf("%s shadowed %v", tk.Status.Phase, tk.Status.Runs[0].Shadowed)
	}
	if _, ok := shadowed(g)["pr https://github.com/Smana/cloud-native-ref/pull/12"]; !ok {
		t.Fatalf("records %v", shadowed(g))
	}
}

// B1: a push of more files than a compare lists is its own T4 finding, and the canary leaked in a
// comment beside it is still recorded: the run is shadowed in one pass.
func TestShadowPassUndiffablePush(t *testing.T) {
	g, tk, _ := shadowRig(t, "documentation")
	g.f.SetDiffError(fmt.Errorf("compare: %w", forge.ErrDiffTooLarge))
	g.f.SetComments(12, forge.Comment{ID: 501, Author: agents, Body: "token mkc-0123456789abcdef0123", At: now.Add(-5 * time.Minute)})
	g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
	got := shadowed(g)
	if r := got["push https://github.com/Smana/cloud-native-ref/commit/"+strings.Repeat("1", 40)]; r.Reason != "diff_unavailable" {
		t.Fatalf("the undiffable push: %+v", r)
	}
	if r := got["comment https://github.com/Smana/cloud-native-ref/pull/12#issuecomment-501"]; r.Reason != "canary_leaked" {
		t.Fatalf("the canary comment: %+v", r)
	}
	if !tk.Status.Runs[0].Shadowed || tk.Status.Runs[0].ShadowAttempts != 0 {
		t.Fatalf("%+v", tk.Status.Runs[0])
	}
}

// B1: a pass that keeps failing waits a doubling time between attempts, and at the cap the run is
// marked shadowed and reported incomplete, so it never loops on GitHub reads.
func TestShadowPassGivesUp(t *testing.T) {
	g, tk, buf := shadowRig(t, "documentation")
	m := &shadowCounter{fakeMetrics: g.metrics}
	g.r.Metrics = m
	g.log.failOutput = errors.New("broker down")
	finished := tk.Status.Runs[0].Finished.Time
	for attempt, wait := range []time.Duration{0, time.Minute, 3 * time.Minute, 7 * time.Minute, 15 * time.Minute} {
		g.r.Now = func() time.Time { return finished.Add(wait - time.Second) }
		calls := g.log.outputCalls
		g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
		if attempt > 0 && g.log.outputCalls != calls {
			t.Fatalf("attempt %d ran before its wait", attempt+1)
		}
		g.r.Now = func() time.Time { return finished.Add(wait) }
		g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
		if g.log.outputCalls == calls || int(tk.Status.Runs[0].ShadowAttempts) != attempt+1 {
			t.Fatalf("attempt %d: %d calls, %d attempts", attempt+1, g.log.outputCalls-calls, tk.Status.Runs[0].ShadowAttempts)
		}
	}
	if !tk.Status.Runs[0].Shadowed || !strings.Contains(buf.String(), "shadow pass incomplete") ||
		!slices.Contains(m.got, " incomplete  shadow_incomplete") {
		t.Fatalf("shadowed %v, metrics %q", tk.Status.Runs[0].Shadowed, m.got)
	}
}

// The broker refusing a record as bad (a run it does not know in this room) is permanent for that
// record: it is skipped, never retried.
func TestShadowPassBadMessageIsPermanent(t *testing.T) {
	g, tk, buf := shadowRig(t, "documentation")
	g.log.failOutput = fmt.Errorf("output: %w", rooms.ErrBadMessage)
	g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
	if !tk.Status.Runs[0].Shadowed || tk.Status.Runs[0].ShadowAttempts != 0 || !strings.Contains(buf.String(), "refused by the broker") {
		t.Fatalf("%+v\n%s", tk.Status.Runs[0], buf)
	}
}

// Nit 4: a run that started before audit was turned on is never backfilled.
func TestShadowPassSkipsRunsStartedBeforeAudit(t *testing.T) {
	g, tk, _ := shadowRig(t, "documentation")
	tk.Status.Runs[0].Audited = false
	g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
	if g.log.outputCalls != 0 || tk.Status.Runs[0].Shadowed {
		t.Fatalf("%d posts", g.log.outputCalls)
	}
}

// failingGuard is an agent-guard that is down, counting its calls.
type failingGuard struct{ calls int }

func (f *failingGuard) Injection(context.Context, []string) (bool, error) {
	f.calls++
	return false, errors.New("agent-guard: 503")
}

// Nit 1: after T5's first failure in a pass, the rest of the pass skips it.
func TestShadowPassBoundsT5(t *testing.T) {
	g, tk, _ := shadowRig(t, "documentation")
	g.r.Cfg.Current().Hardening.AgentGuard.URL = "http://agent-guard:8080"
	guard := &failingGuard{}
	g.r.Hardening.Checker.Classifier = guard
	g.r.shadowPending(t.Context(), g.r.Cfg.Current(), tk)
	if guard.calls != 1 {
		t.Fatalf("%d calls to a classifier that is down, want 1", guard.calls)
	}
	for k, r := range shadowed(g) {
		if len(r.Audit) != 1 || r.Audit[0].Reason != "classifier_unavailable" {
			t.Errorf("%s: %+v", k, r.Audit)
		}
	}
}

// Nit 3: a stop shadows the task's runs while their claims, and canaries, still exist.
func TestShadowPassBeforeStop(t *testing.T) {
	g, tk, _ := shadowRig(t, "documentation")
	tk.Status.Runs[0].Finished = nil
	g.runs.runs[rid(0)] = runs.Run{ID: rid(0), TaskID: tk.Name, Role: "implementer", Phase: "Running", Canary: "mkc-0123456789abcdef0123"}
	g.f.SetComments(12, forge.Comment{ID: 501, Author: agents, Body: "token mkc-0123456789abcdef0123", At: now.Add(-5 * time.Minute)})
	if err := g.c.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	if err := g.r.stop(t.Context(), g.r.Cfg.Current(), tk, "manual"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := g.runs.Get(t.Context(), rid(0)); found {
		t.Fatal("the claim survived the stop")
	}
	if r := shadowed(g)["comment https://github.com/Smana/cloud-native-ref/pull/12#issuecomment-501"]; r.Reason != "canary_leaked" {
		t.Fatalf("the canary was not read before the claim went: %+v", r)
	}
}
