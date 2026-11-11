// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/intake"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/runs"
)

type countingOverlapEvaluator struct {
	result intake.OverlapResult
	calls  int
}

func (e *countingOverlapEvaluator) retrieveOverlap(context.Context, *v1alpha1.Task) intake.OverlapResult {
	e.calls++
	return e.result
}

func overlapTask(name, source, repository, text string) *v1alpha1.Task {
	t := issueTask(name, 7, text)
	t.Spec.Source.Kind = source
	t.Spec.Repository = repository
	return t
}

func TestOverlapExactActiveRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, current, existing string
	}{
		{"issue finds API", "issue", "api"},
		{"API finds schedule", "api", "schedule"},
		{"schedule finds issue", "schedule", "issue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			goal := "FIX the dead-link.\nThe link resolves"
			existing := overlapTask("aaaaaaa2", tc.existing, "Smana/cloud-native-ref", "fix THE dead link\nThe link resolves")
			existing.Spec.Issue = 41
			existing.Status.Phase = v1alpha1.PhaseImplementing
			current := overlapTask("bbbbbbb3", tc.current, "Smana/cloud-native-ref", goal)
			current.Spec.Issue = 42
			g := newRig(t, current, existing)
			g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapEnforce
			g.f.SetIssue(forge.Issue{Number: 41, State: "OPEN", URL: "https://github.com/Smana/cloud-native-ref/issues/41"})

			got := g.reconcile(t, current.Name, 1)
			if got.Status.Phase != v1alpha1.PhaseRejected || got.Status.Reason != "overlap_active_duplicate" {
				t.Fatalf("phase %s reason %s", got.Status.Phase, got.Status.Reason)
			}
			o := got.Status.Overlap
			if o == nil || o.Classification != v1alpha1.OverlapActiveDuplicate || o.CheckedAt == nil || !o.CheckedAt.Time.Equal(now) {
				t.Fatalf("overlap %+v", o)
			}
			wantMatch := v1alpha1.OverlapMatch{Task: existing.Name, Issue: 41, Relation: "exact", Phase: v1alpha1.PhaseImplementing}
			if len(o.Matches) != 1 || o.Matches[0] != wantMatch {
				t.Fatalf("matches %+v, want %+v", o.Matches, wantMatch)
			}
			for _, source := range []string{"tasks", "runs", "github"} {
				if !slices.Contains(o.Sources, source) {
					t.Errorf("sources %q lack %q", o.Sources, source)
				}
			}
			if len(g.runs.specs) != 0 || len(got.Status.Runs) != 0 || got.Status.RoomRef != "" || got.Spec.Template != "" {
				t.Fatalf("refusal had success effects: runs %+v status %+v template %q", g.runs.specs, got.Status, got.Spec.Template)
			}
			reserved, err := ledger.ReservedByRun(t.Context(), g.c, "agent-system")
			if err != nil || len(reserved) != 0 {
				t.Fatalf("implementation budget reserved: %v %v", reserved, err)
			}
			if comments := eventCommentBodies(g.f, 42); len(comments) != 1 || !strings.Contains(comments[0], "same outcome") {
				t.Fatalf("refusal narration %q", comments)
			}
		})
	}
}

func TestOverlapOffDoesNoRetrieval(t *testing.T) {
	current := overlapTask("bbbbbbb3", "issue", "Smana/cloud-native-ref", "fix the link")
	g := newRig(t, current)
	e := &countingOverlapEvaluator{result: intake.OverlapResult{Classification: v1alpha1.OverlapNoMatch}}
	g.r.Overlap = e
	got := g.reconcile(t, current.Name, 3)
	if e.calls != 0 || got.Status.Overlap != nil || got.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("calls %d overlap %+v phase %s", e.calls, got.Status.Overlap, got.Status.Phase)
	}
}

func TestOverlapShadowRecordsButDoesNotRefuse(t *testing.T) {
	existing := overlapTask("aaaaaaa2", "issue", "Smana/cloud-native-ref", "Fix the link.")
	existing.Status.Phase = v1alpha1.PhaseImplementing
	current := overlapTask("bbbbbbb3", "schedule", "Smana/cloud-native-ref", "fix   THE link")
	g := newRig(t, current, existing)
	g.f.SetIssue(forge.Issue{Number: existing.Spec.Issue, State: "OPEN"})
	g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapShadow
	got := g.reconcile(t, current.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseReceived || got.Status.Overlap == nil || got.Status.Overlap.Classification != v1alpha1.OverlapActiveDuplicate {
		t.Fatalf("first pass %s %+v", got.Status.Phase, got.Status.Overlap)
	}
	if got.Spec.Template != "" || got.Status.Trace != nil || len(g.runs.specs) != 0 {
		t.Fatal("success effects preceded the evaluation's persistence")
	}
	first := got.Status.Overlap.DeepCopy()
	got = g.reconcile(t, current.Name, 3)
	if got.Status.Phase != v1alpha1.PhaseImplementing || len(g.runs.specs) != 1 {
		t.Fatalf("shadow blocked work: %s reason %s runs %d", got.Status.Phase, got.Status.Reason, len(g.runs.specs))
	}
	if got.Status.Overlap == nil || got.Status.Overlap.CheckedAt.Time != first.CheckedAt.Time || got.Status.Overlap.Fingerprint != first.Fingerprint {
		t.Fatalf("final identity changed on replay: before %+v after %+v", first, got.Status.Overlap)
	}
}

func TestOverlapPersistedFinalResultsAreNonblockingAndImmutable(t *testing.T) {
	for _, classification := range []string{
		v1alpha1.OverlapCompletedDuplicate,
		v1alpha1.OverlapPriorAttempt,
		v1alpha1.OverlapPartialOverlap,
		v1alpha1.OverlapUnverifiedOverlap,
		v1alpha1.OverlapNoMatch,
	} {
		t.Run(classification, func(t *testing.T) {
			current := overlapTask("bbbbbbb3", "schedule", "Smana/cloud-native-ref", "fix the link")
			checked := metav1.NewTime(now.Add(-time.Hour))
			current.Status.Overlap = &v1alpha1.OverlapStatus{Fingerprint: strings.Repeat("a", 64), Classification: classification,
				CheckedAt: &checked, Sources: []string{"tasks"}, Note: "persisted evaluation"}
			g := newRig(t, current)
			g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapEnforce
			e := &countingOverlapEvaluator{}
			g.r.Overlap = e
			got := g.reconcile(t, current.Name, 3)
			if got.Status.Phase != v1alpha1.PhaseImplementing || e.calls != 0 {
				t.Fatalf("phase %s retrieval calls %d", got.Status.Phase, e.calls)
			}
			o := got.Status.Overlap
			if o.Fingerprint != strings.Repeat("a", 64) || !o.CheckedAt.Time.Equal(checked.Time) || o.Classification != classification || o.Note != "persisted evaluation" {
				t.Fatalf("persisted final result changed: %+v", o)
			}
		})
	}
}

func TestOverlapPersistsBeforeSuccessAndReplays(t *testing.T) {
	current := overlapTask("bbbbbbb3", "issue", "Smana/cloud-native-ref", "fix the link")
	g := newRig(t, current)
	g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapShadow
	e := &countingOverlapEvaluator{result: intake.OverlapResult{Fingerprint: strings.Repeat("b", 64),
		Classification: v1alpha1.OverlapNoMatch, Sources: []string{"tasks", "runs"}, Note: "no duplicate found in checked sources"}}
	g.r.Overlap = e
	got := g.reconcile(t, current.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseReceived || got.Status.Overlap == nil || got.Status.Overlap.Classification != v1alpha1.OverlapNoMatch ||
		got.Spec.Template != "" || got.Status.Trace != nil || len(g.runs.specs) != 0 {
		t.Fatalf("first pass %s %+v spec %+v", got.Status.Phase, got.Status, got.Spec)
	}
	checked := got.Status.Overlap.CheckedAt.DeepCopy()
	got = g.reconcile(t, current.Name, 2)
	if got.Status.Phase != v1alpha1.PhaseQueued || e.calls != 1 || got.Status.Overlap.CheckedAt.Time != checked.Time {
		t.Fatalf("replay phase %s calls %d checked %v", got.Status.Phase, e.calls, got.Status.Overlap.CheckedAt)
	}
}

func TestOverlapPendingHasDeterministicEvaluationIdentity(t *testing.T) {
	current := overlapTask("bbbbbbb3", "api", "Smana/cloud-native-ref", "fix the link")
	g := newRig(t, current)
	g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapEnforce
	e := &countingOverlapEvaluator{result: intake.OverlapResult{Fingerprint: strings.Repeat("c", 64), Pending: true,
		Matches: []v1alpha1.OverlapMatch{{Task: "aaaaaaa2", Relation: "lexical", Phase: v1alpha1.PhaseDone}}, Sources: []string{"tasks"}}}
	g.r.Overlap = e
	got := g.reconcile(t, current.Name, 1)
	o := got.Status.Overlap
	if o == nil || o.Classification != "" || o.CheckedAt != nil || o.ScoperRun != runID(current.Name, 0) || len(g.runs.specs) != 0 {
		t.Fatalf("pending %+v runs %+v", o, g.runs.specs)
	}
	got = g.reconcile(t, current.Name, 1)
	if e.calls != 1 || got.Status.Overlap.ScoperRun != runID(current.Name, 0) || got.Status.Overlap.Classification != "" {
		t.Fatalf("pending replay calls %d overlap %+v", e.calls, got.Status.Overlap)
	}
}

func TestOverlapMergedIsActiveUntilDone(t *testing.T) {
	for _, tc := range []struct {
		phase, wantClassification string
	}{
		{v1alpha1.PhaseMerged, v1alpha1.OverlapActiveDuplicate},
		{v1alpha1.PhaseDone, ""},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			existing := overlapTask("aaaaaaa2", "issue", "Smana/cloud-native-ref", "Fix the link.")
			existing.Status.Phase = tc.phase
			current := overlapTask("bbbbbbb3", "issue", "Smana/cloud-native-ref", "fix THE link")
			g := newRig(t, current, existing)
			if tc.phase != "" {
				g.f.SetIssue(forge.Issue{Number: existing.Spec.Issue, State: "OPEN"})
			}
			g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapEnforce
			got := g.reconcile(t, current.Name, 1)
			if got.Status.Overlap == nil || got.Status.Overlap.Classification != tc.wantClassification {
				t.Fatalf("phase %s classification %+v", tc.phase, got.Status.Overlap)
			}
			if tc.phase == v1alpha1.PhaseMerged && got.Status.Phase != v1alpha1.PhaseRejected {
				t.Fatalf("merged did not refuse: %s", got.Status.Phase)
			}
			if tc.phase == v1alpha1.PhaseDone && (got.Status.Phase != v1alpha1.PhaseReceived || got.Status.Overlap.ScoperRun == "") {
				t.Fatalf("completed exact match blocked or skipped review: %s %+v", got.Status.Phase, got.Status.Overlap)
			}
		})
	}
}

func TestOverlapRunAndRoomEvidence(t *testing.T) {
	existing := overlapTask("aaaaaaa2", "issue", "Smana/cloud-native-ref", "Fix the link.")
	existing.Status.Phase = v1alpha1.PhaseDone // the Task is complete, but its active run is independent evidence
	current := overlapTask("bbbbbbb3", "schedule", "Smana/cloud-native-ref", "fix THE link")
	g := newRig(t, current, existing)
	g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapEnforce
	g.runs.runs["ccccccc4"] = runs.Run{ID: "ccccccc4", TaskID: existing.Name, Phase: "Running", RoomRef: existing.Name,
		PullRequest: "https://github.com/Smana/cloud-native-ref/pull/12", TaskText: "rendered AgentRun brief"}
	g.f.SetIssue(forge.Issue{Number: existing.Spec.Issue, State: "OPEN"})
	g.f.SetPR(forge.PR{Number: 12, State: "OPEN", URL: "https://github.com/Smana/cloud-native-ref/pull/12"})
	g.log.evs = []envelope.Event{{Seq: 1, RunID: "ccccccc4", Type: envelope.Message,
		Payload: envelope.Must(envelope.MessagePayload{Kind: envelope.KindChat, Text: "prior room evidence"})}}
	got := g.reconcile(t, current.Name, 1)
	o := got.Status.Overlap
	if got.Status.Phase != v1alpha1.PhaseRejected || o == nil || o.Classification != v1alpha1.OverlapActiveDuplicate {
		t.Fatalf("active run did not refuse: %s %+v", got.Status.Phase, o)
	}
	var cited *v1alpha1.OverlapMatch
	for i := range o.Matches {
		if o.Matches[i].Run == "ccccccc4" {
			cited = &o.Matches[i]
		}
	}
	if cited == nil || cited.PR != 12 || !slices.Contains(o.Sources, "rooms") {
		t.Fatalf("run/room evidence was lost: matches %+v sources %q", o.Matches, o.Sources)
	}
	if strings.Contains(o.Note, "rendered AgentRun brief") {
		t.Fatalf("the rendered brief became the goal: %+v", o)
	}
}

func TestOverlapOtherRepositoryIsIsolated(t *testing.T) {
	existing := overlapTask("aaaaaaa2", "issue", "Smana/other", "Fix the link.")
	existing.Status.Phase = v1alpha1.PhaseImplementing
	current := overlapTask("bbbbbbb3", "issue", "Smana/cloud-native-ref", "fix THE link")
	g := newRig(t, current, existing)
	g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapEnforce
	got := g.reconcile(t, current.Name, 1)
	if got.Status.Overlap == nil || got.Status.Overlap.Classification != v1alpha1.OverlapNoMatch || len(got.Status.Overlap.Matches) != 0 {
		t.Fatalf("other repository became evidence: %+v", got.Status.Overlap)
	}
}

type failingOverlapRuns struct{ *fakeRuns }

func (f failingOverlapRuns) List(context.Context) ([]runs.Run, error) {
	return nil, errors.New("run source unavailable")
}

func TestOverlapSourceOutagesAreUnverifiedAndNonblocking(t *testing.T) {
	for _, source := range []string{"tasks", "runs", "rooms", "github"} {
		t.Run(source, func(t *testing.T) {
			existing := overlapTask("aaaaaaa2", "issue", "Smana/cloud-native-ref", "Fix the link.")
			existing.Spec.Issue = 41
			existing.Status.Phase = v1alpha1.PhaseImplementing
			existing.Status.RoomRef = existing.Name
			current := overlapTask("bbbbbbb3", "issue", "Smana/cloud-native-ref", "fix THE link")
			g := newRig(t, current, existing)
			if source != "github" {
				g.f.SetIssue(forge.Issue{Number: existing.Spec.Issue, State: "OPEN"})
			}
			g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapEnforce
			switch source {
			case "tasks":
				reads := 0
				g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*v1alpha1.TaskList); ok {
						reads++
						if reads == 2 { // admission's daily-cap read succeeds; overlap's read fails
							return errors.New("task source unavailable")
						}
					}
					return cl.List(ctx, list, opts...)
				}})
			case "runs":
				g.r.Runs = failingOverlapRuns{g.runs}
			case "rooms":
				g.log.failEvents = errors.New("room source unavailable")
			case "github":
				// The matched candidate names issue 41, which the fake GitHub does not contain.
			}
			got := g.reconcile(t, current.Name, 1)
			o := got.Status.Overlap
			if o == nil || o.Classification != v1alpha1.OverlapUnverifiedOverlap || o.CheckedAt == nil || got.Status.Phase != v1alpha1.PhaseReceived {
				t.Fatalf("outage %s: phase %s overlap %+v", source, got.Status.Phase, o)
			}
			if strings.Contains(o.Note, "unavailable") == false || strings.Contains(o.Note, "source unavailable") {
				t.Fatalf("note does not qualify the outage without leaking its error: %q", o.Note)
			}
			got = g.reconcile(t, current.Name, 1)
			if got.Status.Phase != v1alpha1.PhaseTriaged || len(g.runs.specs) != 0 {
				t.Fatalf("unverified blocked or started a run: %s %+v", got.Status.Phase, g.runs.specs)
			}
		})
	}
}

func TestOverlapStatusWriteFailureRetriesWithoutProceeding(t *testing.T) {
	current := overlapTask("bbbbbbb3", "issue", "Smana/cloud-native-ref", "fix the link")
	g := newRig(t, current)
	g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapShadow
	e := &countingOverlapEvaluator{result: intake.OverlapResult{Fingerprint: strings.Repeat("d", 64), Classification: v1alpha1.OverlapNoMatch}}
	g.r.Overlap = e
	fail := true
	g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
		tk, ok := obj.(*v1alpha1.Task)
		if ok && fail && tk.Status.Overlap != nil {
			fail = false
			return errors.New("status store unavailable")
		}
		return cl.SubResource(sub).Update(ctx, obj, opts...)
	}})
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "agent-system", Name: current.Name}}
	if _, err := g.r.Reconcile(t.Context(), req); err == nil {
		t.Fatal("the failed overlap status write did not retry")
	}
	var stored v1alpha1.Task
	if err := g.c.Get(t.Context(), req.NamespacedName, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Overlap != nil || stored.Spec.Template != "" || stored.Status.Trace != nil || len(g.runs.specs) != 0 {
		t.Fatalf("failed write proceeded: %+v spec %+v runs %+v", stored.Status, stored.Spec, g.runs.specs)
	}
	if _, err := g.r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if err := g.c.Get(t.Context(), req.NamespacedName, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Overlap == nil || stored.Status.Overlap.Classification != v1alpha1.OverlapNoMatch || e.calls != 2 {
		t.Fatalf("retry overlap %+v calls %d", stored.Status.Overlap, e.calls)
	}
	if _, err := g.r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if e.calls != 2 {
		t.Fatalf("persisted retry retrieved again: %d", e.calls)
	}
}

func TestOverlapRetrievalExcludesSpecialTasksAndCurrentRuns(t *testing.T) {
	current := overlapTask("bbbbbbb3", "issue", "Smana/cloud-native-ref", "fix the link")
	objects := []client.Object{current}
	for i, source := range []string{"adopt", "runlore"} {
		tk := overlapTask([]string{"aaaaaaa2", "ccccccc4"}[i], source, "Smana/cloud-native-ref", "Fix THE link.")
		tk.Status.Phase = v1alpha1.PhaseImplementing
		objects = append(objects, tk)
	}
	split := overlapTask("ddddddd5", "issue", "Smana/cloud-native-ref", "Fix THE link.")
	split.Spec.Parent = &v1alpha1.Parent{Issue: 1, Index: 1, Of: 2}
	split.Status.Phase = v1alpha1.PhaseImplementing
	coordination := overlapTask("eeeeeee6", "issue", "Smana/cloud-native-ref", "Fix THE link.")
	coordination.Spec.CoordinationParent = &v1alpha1.CoordinationParent{Task: "aaaaaaa2", PlanVersion: 1, ChildID: "docs"}
	coordination.Status.Phase = v1alpha1.PhaseCoordinating
	objects = append(objects, split, coordination)
	g := newRig(t, objects...)
	g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapEnforce
	g.runs.runs["zzzzzzz7"] = runs.Run{ID: "zzzzzzz7", TaskID: current.Name, Phase: "Running", TaskText: "rendered brief"}
	got := g.reconcile(t, current.Name, 1)
	if got.Status.Overlap == nil || got.Status.Overlap.Classification != v1alpha1.OverlapNoMatch || len(got.Status.Overlap.Matches) != 0 {
		t.Fatalf("excluded work became a candidate: %+v", got.Status.Overlap)
	}
}

func TestOverlapRelabelProducesFreshEvidence(t *testing.T) {
	checked := metav1.NewTime(now.Add(-time.Hour))
	previous := overlapTask("aaaaaaa2", "issue", "Smana/cloud-native-ref", "Fix the link.")
	previous.Status.Phase = v1alpha1.PhaseRejected
	previous.Status.Reason = "overlap_active_duplicate"
	previous.Status.Overlap = &v1alpha1.OverlapStatus{Fingerprint: strings.Repeat("e", 64), Classification: v1alpha1.OverlapActiveDuplicate, CheckedAt: &checked}
	current := overlapTask("bbbbbbb3", "issue", "Smana/cloud-native-ref", "fix THE link")
	g := newRig(t, previous, current)
	g.f.SetIssue(forge.Issue{Number: previous.Spec.Issue, State: "OPEN"})
	g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapEnforce
	got := g.reconcile(t, current.Name, 1)
	o := got.Status.Overlap
	if o == nil || o.Classification != "" || o.CheckedAt != nil || len(o.Matches) != 1 || o.Matches[0].Task != previous.Name || o.ScoperRun != runID(current.Name, 0) {
		t.Fatalf("new generation did not evaluate fresh evidence: %+v", o)
	}
	if got.Status.Phase != v1alpha1.PhaseReceived || previous.Status.Overlap.CheckedAt.Time != checked.Time {
		t.Fatal("the new generation replayed or changed the old evaluation")
	}
}

func TestOverlapEvidenceBlockCitesIDsOnly(t *testing.T) {
	tk := overlapTask("bbbbbbb3", "issue", "Smana/cloud-native-ref", "secret goal text")
	checked := metav1.NewTime(now)
	tk.Status.Overlap = &v1alpha1.OverlapStatus{Fingerprint: strings.Repeat("f", 64), Classification: v1alpha1.OverlapCompletedDuplicate,
		CheckedAt: &checked, Matches: []v1alpha1.OverlapMatch{{Task: "aaaaaaa2", Run: "ccccccc4", Issue: 41, PR: 42, Relation: "exact", Phase: v1alpha1.PhaseDone}}}
	block := overlapEvidenceBlock(tk.Status.Overlap)
	if strings.Contains(block, "secret goal text") {
		t.Fatalf("evidence block leaked the goal: %s", block)
	}
	for name, brief := range map[string]string{
		"first": FirstBrief(tk, "n0nce234"),
		"revision": func() string {
			text, _ := ReviseBrief(tk, nil, nil, "n0nce234")
			return text
		}(),
	} {
		for _, want := range []string{"Outcome-overlap evidence", "aaaaaaa2", "ccccccc4", "#41", "#42", "completed_duplicate"} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s brief lacks %q:\n%s", name, want, brief)
			}
		}
	}
}

func TestOverlapEvaluationMetricsWaitForPersistence(t *testing.T) {
	current := overlapTask("bbbbbbb3", "issue", "Smana/cloud-native-ref", "fix the link")
	g := newRig(t, current)
	g.r.Cfg.Current().Intake.Overlap.Mode = config.OverlapShadow
	g.r.Overlap = &countingOverlapEvaluator{result: intake.OverlapResult{Classification: v1alpha1.OverlapNoMatch, Sources: []string{"tasks", "runs"}}}
	_ = g.reconcile(t, current.Name, 1)
	if got := slices.Contains(g.metrics.recorded, "overlap no_match tasks") && slices.Contains(g.metrics.recorded, "overlap no_match runs"); !got {
		t.Fatalf("evaluation metrics %q", g.metrics.recorded)
	}
}

func (m *fakeMetrics) OverlapEvaluation(_ context.Context, classification, source string) {
	m.add("overlap " + classification + " " + source)
}

func (m *fakeMetrics) OverlapEvaluationDuration(_ context.Context, _ time.Duration) {
	m.add("overlap_duration")
}

func (m *fakeMetrics) OverlapScoperRun(_ context.Context, outcome string) {
	m.add("overlap_scoper " + outcome)
}
