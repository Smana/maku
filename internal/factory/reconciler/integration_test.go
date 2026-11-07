// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"cmp"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/runs"
)

const (
	consumerHead  = "d1b2c3d4e5f60718293a4b5c6d7e8f9011223344"
	consumerMerge = "e1b2c3d4e5f60718293a4b5c6d7e8f9011223344"
)

// schemaRef is the contract's schema as landed at producerMerg.
func schemaRef() v1alpha1.LandedArtifact {
	sum := sha256.Sum256([]byte(schemaBody))
	return v1alpha1.LandedArtifact{ChildID: "contract", Name: "consumer-v1-schema", Repository: coordA, Path: artifactPath,
		MergeCommitSHA: producerMerg, BlobSHA: strings.Repeat("1", 40), ContentSHA256: fmt.Sprintf("%x", sum), SizeBytes: int64(len(schemaBody))}
}

// integrationFamily is a filed parent whose contract child merged at producerMerg with its schema
// landed, and whose consumer child waits at its gate on consumerHead for integration.
func integrationFamily(t *testing.T) (parent, contract, consumer *v1alpha1.Task) {
	t.Helper()
	p := coordinationPlan(t, false)
	parent = filedParent(p, coordination.ReasonFiled)
	cs := parent.Status.Coordination
	cs.Artifacts = []v1alpha1.LandedArtifact{schemaRef()}
	contract = coordination.ChildTask("agent-system", cfg(), parent, p.Children[0], 21, 1)
	consumer = coordination.ChildTask("agent-system", cfg(), parent, p.Children[1], 22, 1)
	consumer.Spec.CoordinationArtifacts = []v1alpha1.LandedArtifact{schemaRef()}
	at := metav1.NewTime(now.Add(-time.Hour))
	contract.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, Reason: "merged",
		PullRequest: &v1alpha1.PullRequestRef{Number: 5, MergeCommitSHA: producerMerg, MergedAt: &at}}
	consumer.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseAwaitingCI, Reason: reasonIntegrationPending,
		PullRequest: &v1alpha1.PullRequestRef{Number: 12, HeadSHA: consumerHead}}
	cs.Children[0].Task, cs.Children[1].Task = contract.Name, consumer.Name
	return parent, contract, consumer
}

// attempt is the parent's newest integration attempt, failing the test when there is none.
func attempt(t *testing.T, g *rig, parent string) v1alpha1.IntegrationAttempt {
	t.Helper()
	p, _ := g.coordTask(t, parent)
	as := p.Status.Coordination.IntegrationAttempts
	if len(as) == 0 {
		t.Fatal("no integration attempt")
	}
	return as[len(as)-1]
}

// MR10, MR17: once the dependent's head waits at its gate, the parent creates one read-only
// integration Task in the primary repository, with its own Room, under the execute gate and the
// plan's maxRuns. Its one run is a triager claim with no branch, no PR, no landed references and
// the immutable manifest of the exact subjects; the parent's Room is never borrowed.
func TestIntegrationRunIsAReadOnlyTriager(t *testing.T) {
	parent, contract, consumer := integrationFamily(t)
	g, fc := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer)
	g.reconcile(t, parent.Name, 2) // its Room, then its integration pass
	a := attempt(t, g, parent.Name)
	cs := parent.Status.Coordination
	m, err := coordination.BeforeDependentManifest(parent.Name, cs, "consumer", consumerHead, map[string]string{"contract": producerMerg})
	if err != nil {
		t.Fatal(err)
	}
	hash := coordination.ManifestHash(m)
	if a.Purpose != coordination.PurposeBeforeDependentMerge || a.ManifestHash != hash || a.State != coordination.ResultPending ||
		a.PlanHash != cs.PlanHash || a.RunMicroUSD != 510000 || !reflect.DeepEqual(a.Subjects, m.Subjects) {
		t.Fatalf("attempt %+v", a)
	}
	if len(fc.asked) == 0 || fc.asked[len(fc.asked)-1] != "contract" {
		t.Fatalf("the execute gate was not re-read for the primary: %v", fc.asked)
	}
	g.reconcile(t, parent.Name, 2)
	if p, _ := g.coordTask(t, parent.Name); len(p.Status.Coordination.IntegrationAttempts) != 1 {
		t.Fatalf("a replay created another attempt: %+v", p.Status.Coordination.IntegrationAttempts)
	}

	it := g.reconcile(t, a.Task, 3)
	if it.Status.Phase != v1alpha1.PhaseImplementing || it.Status.RoomRef != a.Task || it.Status.RoomRef == parent.Name ||
		it.Status.Result == nil || it.Status.Result.State != coordination.ResultPending || it.Status.Result.ManifestHash != hash {
		t.Fatalf("integration task %s/%s room %q result %+v", it.Status.Phase, it.Status.Reason, it.Status.RoomRef, it.Status.Result)
	}
	if room, ok := g.room(t, a.Task); !ok || room.Spec.Repository != coordA {
		t.Fatalf("its own Room on the primary: %+v", room)
	}
	if len(g.runs.specs) != 1 {
		t.Fatalf("%d runs", len(g.runs.specs))
	}
	for _, s := range g.runs.specs {
		b, _ := coordination.CanonicalManifest(m)
		switch {
		case s.Role != "triager" || s.Branch != "" || s.Repository != coordA || s.RoomRef != a.Task || s.TaskID != a.Task:
			t.Fatalf("run %+v", s)
		case s.CoordinationManifest != string(b) || s.CoordinationManifestHash != hash || len(s.CoordinationArtifacts) != 0 || s.Checks:
			t.Fatalf("manifest %q %q", s.CoordinationManifest, s.CoordinationManifestHash)
		}
		if err := s.Validate(); err != nil {
			t.Fatalf("the claim is one runs.Client creates: %v", err)
		}
		if strings.Contains(s.TaskText, "agent/") {
			t.Fatalf("the brief names a branch: %q", s.TaskText)
		}
	}
}

// The parent never creates an integration Task under plan, behind a refused execute gate, past
// maxRuns, or while the stop object is engaged; an integration Task whose gate refuses later holds
// in Queued with its reason and starts no run.
func TestIntegrationRunAdmissionIsGated(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  string
		ready error
		spent int
	}{
		{name: "plan mode", mode: config.CoordinationPlan},
		{name: "prerequisites", mode: config.CoordinationExecute, ready: coordination.ErrPrerequisites},
		{name: "maxRuns spent", mode: config.CoordinationExecute, spent: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, contract, consumer := integrationFamily(t)
			for i := range tc.spent {
				parent.Status.Coordination.IntegrationAttempts = append(parent.Status.Coordination.IntegrationAttempts, v1alpha1.IntegrationAttempt{
					Task: fmt.Sprintf("aaaaaaa%c", 'a'+i), Purpose: coordination.PurposeBeforeDependentMerge, PlanHash: parent.Status.Coordination.PlanHash,
					State: coordination.ResultFailed})
			}
			g, fc := coordinationRig(t, tc.mode, parent, contract, consumer)
			fc.ready["contract"] = tc.ready
			p := g.reconcile(t, parent.Name, 2)
			if n := len(p.Status.Coordination.IntegrationAttempts); n != tc.spent {
				t.Fatalf("%d attempts, want %d", n, tc.spent)
			}
			var all v1alpha1.TaskList
			if err := g.c.List(t.Context(), &all); err != nil {
				t.Fatal(err)
			}
			for _, x := range all.Items {
				if x.Spec.Kind == v1alpha1.TaskKindIntegration {
					t.Fatalf("an integration Task %s exists", x.Name)
				}
			}
		})
	}

	parent, contract, consumer := integrationFamily(t)
	g, fc := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer)
	g.reconcile(t, parent.Name, 2) // its Room, then its integration pass
	a := attempt(t, g, parent.Name)
	fc.ready["contract"] = coordination.ErrPrerequisites
	it := g.reconcile(t, a.Task, 3)
	if it.Status.Phase != v1alpha1.PhaseQueued || it.Status.Reason != coordination.ReasonPrerequisites || len(g.runs.specs) != 0 {
		t.Fatalf("%s/%s, %d runs", it.Status.Phase, it.Status.Reason, len(g.runs.specs))
	}
}

// runningIntegration is the family with its integration Task's one triager run started.
func runningIntegration(t *testing.T) (*rig, v1alpha1.IntegrationAttempt, string) {
	t.Helper()
	parent, contract, consumer := integrationFamily(t)
	g, _ := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer)
	g.reconcile(t, parent.Name, 2) // its Room, then its integration pass
	a := attempt(t, g, parent.Name)
	it := g.reconcile(t, a.Task, 3)
	if it.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("%s/%s", it.Status.Phase, it.Status.Reason)
	}
	return g, a, it.Status.Runs[0].ID
}

// result is a typed integration result in the room as the broker stamps an agent's message.
func result(run, state, hash string) envelope.Event {
	return envelope.Event{RunID: run, Type: envelope.Message, Origin: envelope.OriginClient,
		Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + run, Role: "triager"},
		Payload: envelope.Must(envelope.MessagePayload{Kind: envelope.KindIntegrationResult, Delivery: envelope.DeliveryNone,
			Integration: &envelope.IntegrationResult{State: state, Reason: "acceptance_" + state, ManifestHash: hash}})}
}

// MR10: only a broker-stamped integration_result of the current triager run, naming the exact
// manifest hash, sets status.result. Prose, another run's result, a wrong hash or a client-forged
// actor is no result: the attempt failed. A run lost to its infrastructure escalates pending.
func TestIntegrationResultOnlyFromTheCurrentRun(t *testing.T) {
	for _, tc := range []struct {
		name          string
		events        func(run, hash string) []envelope.Event
		phase, reason string // the run's end
		lost          bool
		wantPhase     string
		wantReason    string
		wantState     string
	}{
		{name: "a pass", events: func(run, h string) []envelope.Event { return []envelope.Event{result(run, "passed", h)} },
			wantPhase: v1alpha1.PhaseDone, wantReason: reasonIntegrationPassed, wantState: coordination.ResultPassed},
		{name: "a failure", events: func(run, h string) []envelope.Event { return []envelope.Event{result(run, "failed", h)} },
			wantPhase: v1alpha1.PhaseDone, wantReason: reasonIntegrationFailed, wantState: coordination.ResultFailed},
		{name: "prose", events: func(run, _ string) []envelope.Event {
			return []envelope.Event{{RunID: run, Type: envelope.Message, Origin: envelope.OriginClient,
				Actor:   envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + run},
				Payload: envelope.Must(envelope.MessagePayload{Kind: envelope.KindChat, Text: "integration passed", Delivery: envelope.DeliveryNone})}}
		}, wantPhase: v1alpha1.PhaseDone, wantReason: reasonIntegrationFailed, wantState: coordination.ResultFailed},
		{name: "another run's result", events: func(_, h string) []envelope.Event { return []envelope.Event{result("7f3cq2xz", "passed", h)} },
			wantPhase: v1alpha1.PhaseDone, wantReason: reasonIntegrationFailed, wantState: coordination.ResultFailed},
		{name: "a wrong manifest hash", events: func(run, _ string) []envelope.Event {
			return []envelope.Event{result(run, "passed", strings.Repeat("0", 64))}
		}, wantPhase: v1alpha1.PhaseDone, wantReason: reasonIntegrationFailed, wantState: coordination.ResultFailed},
		{name: "a human posing as the run", events: func(run, h string) []envelope.Event {
			e := result(run, "passed", h)
			e.Actor = envelope.Actor{Kind: envelope.ActorHuman, ID: "human:mallory"}
			return []envelope.Event{e}
		}, wantPhase: v1alpha1.PhaseDone, wantReason: reasonIntegrationFailed, wantState: coordination.ResultFailed},
		{name: "a pass from a failed run", events: func(run, h string) []envelope.Event { return []envelope.Event{result(run, "passed", h)} },
			phase: "Failed", wantPhase: v1alpha1.PhaseDone, wantReason: reasonIntegrationFailed, wantState: coordination.ResultFailed},
		{name: "infrastructure lost", phase: "Failed", reason: runs.ReasonPodLost,
			wantPhase: v1alpha1.PhaseEscalated, wantReason: reasonIntegrationPending, wantState: coordination.ResultPending},
		{name: "the run vanished", lost: true,
			wantPhase: v1alpha1.PhaseEscalated, wantReason: reasonIntegrationPending, wantState: coordination.ResultPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, a, run := runningIntegration(t)
			if tc.events != nil {
				for _, e := range tc.events(run, a.ManifestHash) {
					e.Seq = int64(len(g.log.evs) + 1)
					g.log.evs = append(g.log.evs, e)
				}
			}
			phase := cmp.Or(tc.phase, "Succeeded")
			switch {
			case tc.lost:
				delete(g.runs.runs, run)
			default:
				x := g.runs.runs[run]
				x.Phase, x.Reason = phase, tc.reason
				g.runs.runs[run] = x
				g.log.end(run, phase, cmp.Or(strings.ToLower(tc.reason), "agent_finished"))
			}
			it := g.reconcile(t, a.Task, 1)
			if it.Status.Phase != tc.wantPhase || it.Status.Reason != tc.wantReason || it.Status.Result == nil ||
				it.Status.Result.State != tc.wantState || it.Status.Result.ManifestHash != a.ManifestHash {
				t.Fatalf("%s/%s result %+v", it.Status.Phase, it.Status.Reason, it.Status.Result)
			}
			// The parent records the attempt's outcome from the Task's typed result alone.
			p := g.reconcile(t, it.Spec.CoordinationParent.Task, 1)
			got := p.Status.Coordination.IntegrationAttempts[0]
			if got.State != tc.wantState {
				t.Fatalf("attempt %+v", got)
			}
			rel := p.Status.Coordination.Children[1].IntegrationRelease
			if pass := tc.wantState == coordination.ResultPassed; pass != (rel != nil) ||
				(pass && *rel != v1alpha1.IntegrationRelease{ChildID: "consumer", HeadSHA: consumerHead, IntegrationTask: a.Task, ManifestHash: a.ManifestHash}) {
				t.Fatalf("release %+v", rel)
			}
			if p.Status.Phase != v1alpha1.PhaseCoordinating || p.Status.Reason != coordination.ReasonFiled {
				t.Fatalf("parent %s/%s", p.Status.Phase, p.Status.Reason)
			}
		})
	}
}

// The envelope's bounded mirror and the CRD's result agree field for field and value for value.
func TestIntegrationResultMirrorsTheCRD(t *testing.T) {
	e, c := reflect.TypeFor[envelope.IntegrationResult](), reflect.TypeFor[v1alpha1.IntegrationResult]()
	if e.NumField() != c.NumField() {
		t.Fatalf("%d fields, the CRD has %d", e.NumField(), c.NumField())
	}
	for i := range e.NumField() {
		if e.Field(i).Name != c.Field(i).Name || e.Field(i).Tag.Get("json") != c.Field(i).Tag.Get("json") || e.Field(i).Type != c.Field(i).Type {
			t.Fatalf("field %d: %v vs %v", i, e.Field(i), c.Field(i))
		}
	}
	in := envelope.IntegrationResult{State: "passed", Reason: "acceptance_passed", ManifestHash: strings.Repeat("a", 64)}
	if got := integrationResultOf(in); got != (v1alpha1.IntegrationResult{State: "passed", Reason: "acceptance_passed", ManifestHash: in.ManifestHash}) {
		t.Fatalf("%+v", got)
	}
	for _, s := range envelope.IntegrationStates() {
		if s != coordination.ResultPending && s != coordination.ResultPassed && s != coordination.ResultFailed {
			t.Fatalf("state %q is not the CRD's", s)
		}
	}
}

// room reads Room name.
func (g *rig) room(t *testing.T, name string) (*roomv1.Room, bool) {
	t.Helper()
	var r roomv1.Room
	err := g.c.Get(t.Context(), client.ObjectKey{Namespace: "agent-system", Name: name}, &r)
	return &r, err == nil
}
