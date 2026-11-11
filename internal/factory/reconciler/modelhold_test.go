// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// refuse ends run id as the broker records a run the model provider refused (#450).
func (g *rig) refuse(id, reason string) {
	r := g.runs.runs[id]
	r.Phase = "Failed"
	g.runs.runs[id] = r
	g.log.end(id, "Failed", reason)
}

// #450: the provider refused the run (an empty wallet). The task holds in Queued with the reason
// and a backoff, the room and the issue say so, and nothing escalates to a human.
func TestAProviderRefusalHoldsTheTaskWithABackoff(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	g.reconcile(t, "3buqdlot", 3)
	g.refuse(rid(0), "model_unavailable_billing")
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Reason != "model_unavailable_billing" || tk.Status.ModelHolds != 1 ||
		tk.Status.ModelHoldUntil == nil || len(g.runs.specs) != 1 {
		t.Fatalf("%s %q holds=%d until=%v runs=%d", tk.Status.Phase, tk.Status.Reason, tk.Status.ModelHolds, tk.Status.ModelHoldUntil, len(g.runs.specs))
	}
	if !slices.Contains(g.metrics.recorded, "model_refused billing implementer") {
		t.Fatalf("metrics %v", g.metrics.recorded)
	}
	if c := strings.Join(g.f.Comments(7), "\n"); !strings.Contains(c, "the model provider refused: billing") || strings.Contains(c, "stopped on an error") {
		t.Fatalf("%q", c)
	}
	// Still inside the backoff: no run starts.
	g.reconcile(t, "3buqdlot", 2)
	if len(g.runs.specs) != 1 {
		t.Fatalf("a run started inside the backoff: %d", len(g.runs.specs))
	}
	// Past it, the same work runs again as a resume.
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	tk = g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(g.runs.specs) != 2 || tk.Status.Runs[1].Trigger != "resume" {
		t.Fatalf("%s runs=%d %+v", tk.Status.Phase, len(g.runs.specs), tk.Status.Runs)
	}
}

// The hold is bounded: past ModelHoldLimit consecutive refusals the task escalates, with the
// provider's cause as its reason, so it cannot loop forever.
func TestProviderRefusalHoldsAreBounded(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	g.reconcile(t, "3buqdlot", 3)
	at := now
	for i := range modelHoldLimit {
		g.refuse(rid(i), "model_unavailable_quota")
		tk := g.reconcile(t, "3buqdlot", 1)
		if tk.Status.Phase != v1alpha1.PhaseQueued || int(tk.Status.ModelHolds) != i+1 {
			t.Fatalf("hold %d: %s holds=%d", i+1, tk.Status.Phase, tk.Status.ModelHolds)
		}
		at = at.Add(time.Hour)
		g.r.Now = func() time.Time { return at }
		g.reconcile(t, "3buqdlot", 1) // the next run starts
	}
	g.refuse(rid(modelHoldLimit), "model_unavailable_quota")
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseEscalated || tk.Status.Reason != "model_unavailable_quota" {
		t.Fatalf("%s %q", tk.Status.Phase, tk.Status.Reason)
	}
}

// An ordinary run end clears the count: the bound is on consecutive refusals.
func TestAnotherEndResetsTheModelHolds(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	g.reconcile(t, "3buqdlot", 3)
	g.refuse(rid(0), "model_unavailable_auth")
	g.reconcile(t, "3buqdlot", 1)
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	g.reconcile(t, "3buqdlot", 1)
	g.runs.set(rid(1), "Failed")
	g.log.end(rid(1), "Failed", "agent_error")
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.ModelHolds != 0 || tk.Status.ModelHoldUntil != nil {
		t.Fatalf("holds=%d until=%v", tk.Status.ModelHolds, tk.Status.ModelHoldUntil)
	}
}

// #450 with #451: a scoper the provider refused holds like any run, and runs again.
func TestAProviderRefusalHoldsTheScoper(t *testing.T) {
	g := scoperRig(t)
	g.runs.set(rid(0), "Failed")
	g.log.end(rid(0), "Failed", "model_unavailable_billing")
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Scope != nil || tk.Status.ModelHolds != 1 || len(g.runs.specs) != 1 {
		t.Fatalf("%s scope=%+v holds=%d runs=%d", tk.Status.Phase, tk.Status.Scope, tk.Status.ModelHolds, len(g.runs.specs))
	}
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	tk = g.reconcile(t, "3buqdlot", 1)
	if s := g.runs.specs[rid(1)]; s.Role != "triager" || tk.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("%s %+v", tk.Status.Phase, s)
	}
}
