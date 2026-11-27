// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/intake"
	"github.com/Smana/maku/internal/factory/triage"
	"github.com/Smana/maku/internal/redact"
)

var (
	proposeRedactorOnce sync.Once
	proposeRedactorVal  *redact.Redactor
	proposeRedactorErr  error
)

func testProposeRedactor(t *testing.T) *redact.Redactor {
	t.Helper()
	proposeRedactorOnce.Do(func() { proposeRedactorVal, proposeRedactorErr = redact.New() })
	if proposeRedactorErr != nil {
		t.Fatal(proposeRedactorErr)
	}
	return proposeRedactorVal
}

func proposeRoutineTask(name, routine, dataClass string) *v1alpha1.Task {
	return &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "agent-system",
			CreationTimestamp: metav1.NewTime(now.Add(-time.Minute)),
			Labels:            map[string]string{v1alpha1.LabelRoutine: routine},
		},
		Spec: v1alpha1.TaskSpec{
			Repository:  "Smana/cloud-native-ref",
			DataClass:   dataClass,
			MaxMicroUSD: 1_500_000,
			Source: v1alpha1.Source{
				Kind:          v1alpha1.SourceKindRoutine,
				Ref:           routine,
				Key:           "routine:" + routine + ":1",
				RequestedBy:   "system:scheduler",
				Trust:         "trusted",
				ContentSHA256: strings.Repeat("b", 64),
			},
			Text: "Audit documentation against CLI flags.",
			Routine: &v1alpha1.TaskRoutine{
				Name:         routine,
				Mode:         config.RoutineModePropose,
				PromptSource: "routine",
				PromptSHA:    strings.Repeat("a", 64),
			},
		},
	}
}

func proposeRig(t *testing.T, tk *v1alpha1.Task, objs ...client.Object) *rig {
	t.Helper()
	g := newRig(t, append([]client.Object{tk}, objs...)...)
	g.r.Triage = triage.Classify{}
	g.r.Redact = testProposeRedactor(t)
	c := g.r.Cfg.Current()
	c.Templates["investigate"] = config.Template{Roles: []string{"triager"}}
	c.Repositories[0].ResolvedRoutines = map[string]config.ResolvedRoutine{
		tk.Spec.Routine.Name: {
			Name:       tk.Spec.Routine.Name,
			Mode:       config.RoutineModePropose,
			Cron:       "0 4 * * 1",
			DataClass:  tk.Spec.DataClass,
			RepoPrompt: true,
		},
	}
	return g
}

func TestProposeRoutineNoOpCreatesZeroIssues(t *testing.T) {
	tk := proposeRoutineTask("prop234a", "doc-drift", "public")
	tk.Spec.Routine.PromptSource = "repo"
	tk.Spec.Routine.PromptPath = ".github/agent-factory/upkeep/doc-drift.md"
	g := proposeRig(t, tk)
	g.f.SetFile(".github/agent-factory/upkeep/doc-drift.md", "## Scope\nCheck docs.\n\n## Verify\n- make test\n")

	got := g.reconcile(t, tk.Name, 3)
	if got.Status.Phase != v1alpha1.PhaseImplementing || got.Spec.Template != "investigate" || got.Spec.DataClass != "public" {
		t.Fatalf("got phase=%s template=%s dataClass=%s", got.Status.Phase, got.Spec.Template, got.Spec.DataClass)
	}
	spec := g.runs.specs[runID(tk.Name, 0)]
	if spec.Role != "triager" || spec.DataClass != "public" ||
		!strings.Contains(spec.TaskText, "ROUTINE-PROMPT-n0nce234") ||
		!strings.Contains(spec.TaskText, "end without a handoff") {
		t.Fatalf("unexpected triager spec: %+v", spec)
	}

	g.runs.set(runID(tk.Name, 0), "Succeeded")
	g.log.end(runID(tk.Name, 0), "Succeeded", "agent_finished")
	got = g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseNoOp || got.Status.Reason != "no_action" {
		t.Fatalf("got %s/%s, want PhaseNoOp/no_action", got.Status.Phase, got.Status.Reason)
	}
	if len(g.f.Created()) != 0 {
		t.Fatalf("expected zero GitHub issues on NoOp, got %+v", g.f.Created())
	}
}

func TestProposeRoutinePublicProposalAndIdempotency(t *testing.T) {
	tk := proposeRoutineTask("propose2", "doc-drift", "public")
	g := proposeRig(t, tk)
	got := g.reconcile(t, tk.Name, 3)
	if got.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("got %s/%s", got.Status.Phase, got.Status.Reason)
	}

	proposal := "docs(cli): update flags table\n" +
		"## What is wrong\nThe `--timeout` flag is missing from `docs/cli.md`.\n" +
		"## What it should be\nDocument `--timeout` with its 30s default.\n" +
		"## Acceptance check\n`docs/cli.md` lists `--timeout`.\n" +
		"## Files\n- `docs/cli.md`"
	rID := runID(tk.Name, 0)
	g.log.evs = append(g.log.evs, envelope.Event{
		Seq:     int64(len(g.log.evs) + 1),
		RunID:   rID,
		Type:    envelope.Handoff,
		Origin:  envelope.OriginClient,
		Actor:   envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + rID},
		Payload: envelope.Must(envelope.HandoffPayload{FromRole: "triager", ToRole: "implementer", Summary: proposal}),
	})
	g.runs.set(rID, "Succeeded")
	g.log.end(rID, "Succeeded", "agent_finished")

	// Inject one Status().Update conflict right after CreateIssue + Patch to prove crash-replay idempotency.
	failOnce := true
	baseClient := g.c.(client.WithWatch)
	g.r.Client = interceptor.NewClient(baseClient, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if subResourceName == "status" && failOnce {
				if tObj, ok := obj.(*v1alpha1.Task); ok && tObj.Status.Phase == v1alpha1.PhaseDone {
					failOnce = false
					return errors.New("simulated status conflict")
				}
			}
			return cl.Status().Update(ctx, obj, opts...)
		},
	})

	if _, err := g.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "agent-system", Name: tk.Name}}); err == nil {
		t.Fatal("expected simulated status conflict error")
	}
	if len(g.f.Created()) != 1 {
		t.Fatalf("expected 1 issue created before conflict, got %d", len(g.f.Created()))
	}

	// Second reconcile must reuse the existing issue via PublishedFromLabel without creating a duplicate.
	got = g.reconcile(t, tk.Name, 1)
	if len(g.f.Created()) != 1 {
		t.Fatalf("duplicate issue created on replay: %d", len(g.f.Created()))
	}
	if got.Status.Phase != v1alpha1.PhaseDone || got.Status.Reason != "proposal_filed" {
		t.Fatalf("got %s/%s, want Done/proposal_filed", got.Status.Phase, got.Status.Reason)
	}
	if got.Spec.Issue == 0 || got.Labels[v1alpha1.LabelIssue] != strconv.Itoa(got.Spec.Issue) {
		t.Fatalf("spec.issue=%d label=%q", got.Spec.Issue, got.Labels[v1alpha1.LabelIssue])
	}
	iss, err := g.f.Issue(t.Context(), got.Spec.Issue)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(iss.Body, "factory/ready") || !strings.Contains(iss.Body, "What is wrong") {
		t.Fatalf("unexpected public issue body:\n%s", iss.Body)
	}
	for _, wantLabel := range []string{"factory/proposed", "factory/routine:doc-drift", intake.PublishedFromLabel(tk.Name)} {
		if !containsString(iss.Labels, wantLabel) {
			t.Fatalf("issue labels %v missing %q", iss.Labels, wantLabel)
		}
	}

	// Advance past settleWindow so drain posts the settled comment on got.Spec.Issue;
	// it must instruct maintainers to review with factory/ready, never factory/publish.
	g.r.Now = func() time.Time { return now.Add(g.r.settleWindow(g.r.Cfg.Current())) }
	_ = g.reconcile(t, tk.Name, 1)
	comments, err := g.f.RecentComments(t.Context(), got.Spec.Issue)
	if err != nil {
		t.Fatal(err)
	}
	var endedBody string
	for _, c := range comments {
		if strings.Contains(c.Body, "factory/publish") {
			t.Fatalf("comment on public propose issue must not mention factory/publish:\n%s", c.Body)
		}
		if strings.Contains(c.Body, "event=end-done-") {
			endedBody = c.Body
		}
	}
	if endedBody == "" {
		t.Fatalf("expected settled end-done comment on #%d, got %+v", got.Spec.Issue, comments)
	}
	if !strings.Contains(endedBody, "factory/ready") {
		t.Fatalf("settled comment on public propose issue must mention factory/ready:\n%s", endedBody)
	}
}

func TestProposeRoutinePublicFailClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		taskID     string
		summary    string
		nilRedact  bool
		renderHTML string
		wantReason string
	}{
		"invalid draft": {
			taskID:     "prop234b",
			summary:    "missing sections",
			wantReason: "invalid_draft",
		},
		"secret redacted": {
			taskID:     "prop234c",
			summary:    "docs: oops\n## What is wrong\nFound ghp_1234567890abcdef1234567890abcdef1234\n## What it should be\nRemove it.\n## Acceptance check\nClean.",
			wantReason: "redacted",
		},
		"nil redactor fails closed": {
			taskID:     "prop234d",
			summary:    "docs(cli): ok\n## What is wrong\nBad.\n## What it should be\nGood.\n## Acceptance check\nCheck.",
			nilRedact:  true,
			wantReason: "redacted",
		},
		"live reference withheld": {
			taskID:     "prop234e",
			summary:    "docs(cli): ping user\n## What is wrong\nBad.\n## What it should be\nGood.\n## Acceptance check\nCheck.",
			renderHTML: `<p><a class="user-mention" href="https://github.com/alice">@alice</a></p>`,
			wantReason: "live_reference",
		},
	} {
		t.Run(name, func(t *testing.T) {
			tk := proposeRoutineTask(tc.taskID, "doc-drift", "public")
			g := proposeRig(t, tk)
			if tc.nilRedact {
				g.r.Redact = nil
			}
			if tc.renderHTML != "" {
				g.f.SetRender(func(string) (string, error) { return tc.renderHTML, nil })
			}
			_ = g.reconcile(t, tk.Name, 3)
			rID := runID(tk.Name, 0)
			g.log.evs = append(g.log.evs, envelope.Event{
				Seq:     int64(len(g.log.evs) + 1),
				RunID:   rID,
				Type:    envelope.Handoff,
				Origin:  envelope.OriginClient,
				Actor:   envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + rID},
				Payload: envelope.Must(envelope.HandoffPayload{FromRole: "triager", ToRole: "implementer", Summary: tc.summary}),
			})
			g.runs.set(rID, "Succeeded")
			g.log.end(rID, "Succeeded", "agent_finished")
			got := g.reconcile(t, tk.Name, 1)
			if got.Status.Phase != v1alpha1.PhaseEscalated || got.Status.Reason != tc.wantReason {
				t.Fatalf("got %s/%s, want Escalated/%s", got.Status.Phase, got.Status.Reason, tc.wantReason)
			}
			if len(g.f.Created()) != 0 {
				t.Fatalf("expected 0 GitHub issues on fail-closed %s, got %d", tc.wantReason, len(g.f.Created()))
			}
		})
	}
}

func TestProposeRoutineInternalStubAndPublishCompatibility(t *testing.T) {
	tk := proposeRoutineTask("proposei", "doc-drift", "internal")
	g := proposeRig(t, tk)
	got := g.reconcile(t, tk.Name, 3)
	if got.Spec.DataClass != "internal" {
		t.Fatalf("DataClass = %q, want internal", got.Spec.DataClass)
	}

	rID := runID(tk.Name, 0)
	proposal := "docs(internal): internal host cluster-secret.internal\n" +
		"## What is wrong\nInternal secret detail XYZ_PRIVATE.\n" +
		"## What it should be\nClean it.\n" +
		"## Acceptance check\nVerified."
	g.log.evs = append(g.log.evs, envelope.Event{
		Seq:     int64(len(g.log.evs) + 1),
		RunID:   rID,
		Type:    envelope.Handoff,
		Origin:  envelope.OriginClient,
		Actor:   envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + rID},
		Payload: envelope.Must(envelope.HandoffPayload{FromRole: "triager", ToRole: "implementer", Summary: proposal}),
	})
	g.runs.set(rID, "Succeeded")
	g.log.end(rID, "Succeeded", "agent_finished")

	got = g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseDone || got.Status.Reason != "proposal_ready" || got.Spec.Issue == 0 {
		t.Fatalf("got %+v", got)
	}
	stub, err := g.f.Issue(t.Context(), got.Spec.Issue)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stub.Title+stub.Body, "XYZ_PRIVATE") || strings.Contains(stub.Title+stub.Body, "cluster-secret") {
		t.Fatalf("internal prose leaked to stub issue: %s\n%s", stub.Title, stub.Body)
	}
	if !strings.Contains(stub.Body, "factory/publish") || !strings.Contains(stub.Body, "/r/"+tk.Name) {
		t.Fatalf("stub missing room link or factory/publish note:\n%s", stub.Body)
	}

	// Applying factory/publish on the stub issue must file a NEW public issue (not adopt the stub itself).
	g.f.SetLabeled(intake.LabelPublish, forge.Item{Number: stub.Number})
	g.f.SetEvents(stub.Number, forge.LabelEvent{Actor: "Smana", Label: intake.LabelPublish, At: now.Add(time.Minute)})
	poller := &intake.IssuePoller{
		Forge:     g.f,
		Client:    g.c,
		Namespace: "agent-system",
		Cfg:       g.r.Cfg,
		Owned:     func(string) bool { return true },
		Stopped:   func(context.Context) bool { return false },
		CanWrite:  func(context.Context, string) (bool, error) { return true, nil },
		Redact:    testProposeRedactor(t),
		Proposal:  g.r.Proposal,
		Now:       func() time.Time { return now.Add(2 * time.Minute) },
	}
	if err := poller.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(g.f.Created()) != 2 {
		t.Fatalf("expected factory/publish to file a second public issue, got %d created", len(g.f.Created()))
	}
	var updated v1alpha1.Task
	_ = g.c.Get(t.Context(), client.ObjectKey{Namespace: "agent-system", Name: tk.Name}, &updated)
	if updated.Status.Published == nil || updated.Status.Published.Issue == stub.Number {
		t.Fatalf("expected published issue != stub #%d, got %+v", stub.Number, updated.Status.Published)
	}
}

func containsString(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}
