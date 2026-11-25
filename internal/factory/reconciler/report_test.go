// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/hardening"
)

func reportRig(t *testing.T) (*rig, *v1alpha1.Task) {
	t.Helper()
	tk := issueTask("3buqdlot", 7, "# Investigate cache eviction spike\n\nWhy does eviction spike every 10 minutes?")
	tk.Spec.IssueAuthor = "alice"
	tk.Spec.DataClass = "public"
	tk.Spec.Outcome = config.OutcomeReport
	g := shippedRig(t, tk)
	g.r.Cfg.Current().Outcomes = []string{config.OutcomeChange, config.OutcomeReport}
	return g, tk
}

func finishTriagerWithFindings(g *rig, t *testing.T, tk *v1alpha1.Task, findings string) *v1alpha1.Task {
	t.Helper()
	g.log.evs = append(g.log.evs, envelope.Event{
		Seq:   int64(len(g.log.evs) + 1),
		RunID: rid(0),
		Type:  envelope.Handoff,
		Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + rid(0), Role: "triager"},
		Payload: envelope.Must(envelope.HandoffPayload{
			FromRole: "triager",
			ToRole:   "implementer",
			Summary:  findings,
		}),
	})
	g.runs.set(rid(0), "Succeeded")
	g.log.end(rid(0), "Succeeded", "agent_finished")
	return g.reconcile(t, tk.Name, 1)
}

func TestAReportTaskStartsNoImplementer(t *testing.T) {
	g, tk := reportRig(t)
	// Received -> Triaged -> Queued -> Implementing (triager only)
	tk = g.reconcile(t, tk.Name, 3)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(g.runs.specs) != 1 {
		t.Fatalf("expected triager running: phase=%s runs=%d", tk.Status.Phase, len(g.runs.specs))
	}
	spec := g.runs.specs[rid(0)]
	if spec.Role != "triager" {
		t.Fatalf("expected triager role, got %q", spec.Role)
	}
	if spec.DataClass != "public" {
		t.Fatalf("public report must keep dataClass=public, got %q", spec.DataClass)
	}
	perms, err := minter.Roles.For(spec.Role, hardening.Types())
	if err != nil {
		t.Fatal(err)
	}
	for scope, level := range perms {
		if level == "write" {
			t.Fatalf("report triager run must be read-only, got %s=%s", scope, level)
		}
	}
	if !strings.Contains(spec.TaskText, "read-only") || strings.Contains(spec.TaskText, "from an alert and its logs") {
		t.Fatalf("expected public report brief, got:\n%s", spec.TaskText)
	}

	tk = finishTriagerWithFindings(g, t, tk, "internal/cache/ttl.go resets TTL every 10m via ticker.")
	if tk.Status.Phase != v1alpha1.PhaseDone || tk.Status.Reason != ReasonFindingsPosted {
		t.Fatalf("expected Done/findings_posted, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
	if len(g.runs.specs) != 1 {
		t.Fatalf("expected no implementer run started, got %d runs", len(g.runs.specs))
	}
}

func TestTheFindingsOfferAFix(t *testing.T) {
	g, tk := reportRig(t)
	tk = g.reconcile(t, tk.Name, 3)
	const findings = "internal/cache/ttl.go flushes all keys when the sweep ticker fires."
	tk = finishTriagerWithFindings(g, t, tk, findings)

	if tk.Status.Phase != v1alpha1.PhaseDone || tk.Status.Reason != ReasonFindingsPosted {
		t.Fatalf("expected Done/findings_posted, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
	comments := eventCommentBodies(g.f, 7)
	var findingsComments []string
	for _, body := range comments {
		if strings.Contains(body, "Agent factory findings") {
			findingsComments = append(findingsComments, body)
		}
	}
	if len(findingsComments) != 1 {
		t.Fatalf("expected 1 findings comment on issue #7, got %d: %v", len(findingsComments), comments)
	}
	body := findingsComments[0]
	if !strings.Contains(body, findings) {
		t.Fatalf("findings comment missing findings:\n%s", body)
	}
	if !strings.Contains(body, "To have this fixed, a maintainer comments `/factory fix`") {
		t.Fatalf("findings comment missing /factory fix offer:\n%s", body)
	}
}

func TestFactoryFixFilesAChangeWithTheFindings(t *testing.T) {
	g, tk := reportRig(t)
	tk = g.reconcile(t, tk.Name, 3)
	const findings = "internal/cache/ttl.go clears active keys during sweep; only expired keys should be evicted."
	tk = finishTriagerWithFindings(g, t, tk, findings)

	// Non-maintainer /factory fix is ignored.
	appendComment(g, 7, forge.Comment{ID: 201, Author: "stranger", Body: "/factory fix", At: now.Add(2 * time.Minute)})
	tk = g.reconcile(t, tk.Name, 1)
	var list v1alpha1.TaskList
	if err := g.c.List(t.Context(), &list, client.InNamespace("agent-system")); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("non-maintainer /factory fix must not create a task, got %d", len(list.Items))
	}

	// Maintainer /factory fix files a change task on the same issue with the findings as its scope.
	appendComment(g, 7, forge.Comment{ID: 202, Author: "Smana", Body: "/factory fix", At: now.Add(3 * time.Minute)})
	tk = g.reconcile(t, tk.Name, 1)
	list = v1alpha1.TaskList{}
	if err := g.c.List(t.Context(), &list, client.InNamespace("agent-system")); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 tasks (report + change), got %d", len(list.Items))
	}
	var fix *v1alpha1.Task
	for i := range list.Items {
		if list.Items[i].Name != tk.Name {
			fix = &list.Items[i]
		}
	}
	if fix == nil {
		t.Fatal("fix task not found")
	}
	if fix.Spec.Outcome != config.OutcomeChange {
		t.Fatalf("expected outcome=change, got %q", fix.Spec.Outcome)
	}
	if fix.Spec.Issue != 7 || fix.Spec.Repository != "Smana/cloud-native-ref" || fix.Spec.DataClass != "public" {
		t.Fatalf("unexpected fix task metadata: %+v", fix.Spec)
	}
	if !strings.Contains(fix.Spec.Text, findings) {
		t.Fatalf("fix task text missing findings:\n%s", fix.Spec.Text)
	}
	if fix.Spec.Source.RequestedBy != "github:Smana" {
		t.Fatalf("expected RequestedBy github:Smana, got %q", fix.Spec.Source.RequestedBy)
	}
}
