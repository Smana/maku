// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"context"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/hardening"
)

func TestDraftRoutineForcesDraftPRAndCrashReplays(t *testing.T) {
	r := newRig(t, 0)
	var tk v1alpha1.Task
	if err := r.c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: taskID}, &tk); err != nil {
		t.Fatal(err)
	}
	tk.Spec.Source = v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-code"}
	tk.Spec.Routine = &v1alpha1.TaskRoutine{Name: "dead-code", Mode: config.RoutineModeDraft}
	if err := r.c.Update(t.Context(), &tk); err != nil {
		t.Fatal(err)
	}

	// Agent requested Draft: false, but routine mode is draft so applier must force draft=true.
	id := r.request(t, runID, hardening.PR, hardening.PRPayload{Title: "chore: remove dead helper", Body: "Removes unused helper.", Draft: false})
	r.tick(t)
	got := r.only(t, id)
	if got.kind != envelope.KindOutputApplied {
		t.Fatalf("answer = %+v", got)
	}
	n, _ := r.pub.PullRequestForBranch(t.Context(), "agent/"+taskID)
	pr, _ := r.pub.PullRequest(t.Context(), n)
	if !pr.Draft {
		t.Fatalf("expected PR #%d to be draft, got Draft=%v", pr.Number, pr.Draft)
	}
	if calls := strings.Join(r.pub.Calls(), ";"); !strings.Contains(calls, "create-pr main agent/"+taskID+" draft=true") {
		t.Fatalf("expected create-pr draft=true, got calls %s", calls)
	}

	// Crash replay over the same log recovers the existing PR without creating a duplicate.
	restarted := newRig(t, 0)
	var rtk v1alpha1.Task
	if err := restarted.c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: taskID}, &rtk); err != nil {
		t.Fatal(err)
	}
	rtk.Spec = tk.Spec
	if err := restarted.c.Update(t.Context(), &rtk); err != nil {
		t.Fatal(err)
	}
	restarted.pub = r.pub
	restarted.a.Publisher = func(context.Context, string) (Publisher, error) { return r.pub, nil }
	restarted.request(t, runID, hardening.PR, hardening.PRPayload{Title: "chore: remove dead helper", Body: "Removes unused helper.", Draft: false})
	restarted.rooms.mu.Lock()
	restarted.rooms.events[len(restarted.rooms.events)-1].Payload = r.rooms.events[0].Payload
	restarted.rooms.mu.Unlock()
	restarted.tick(t)
	if strings.Count(strings.Join(r.pub.Calls(), ";"), "create-pr") != 1 {
		t.Fatalf("replay created duplicate PR: %v", r.pub.Calls())
	}
	if b := restarted.only(t, id); b.kind != envelope.KindOutputApplied || b.rec.Object != got.rec.Object {
		t.Fatalf("replay answer = %+v, want object %q", b, got.rec.Object)
	}
}
