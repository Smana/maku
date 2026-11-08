// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/tracing"
)

func (m *fakeMetrics) CoordinationFamily(_ context.Context, repository, outcome string) {
	m.add("coord_family " + repository + " " + outcome)
}

func (m *fakeMetrics) CoordinationAdmissionRefused(_ context.Context, repository, reason string) {
	m.add("coord_refused " + repository + " " + reason)
}

func (m *fakeMetrics) CoordinationChild(_ context.Context, repository, class, outcome string) {
	m.add("coord_child " + repository + " " + class + " " + outcome)
}

func (m *fakeMetrics) CoordinationIntegrationRun(_ context.Context, repository, purpose, result string) {
	m.add("coord_integration " + repository + " " + purpose + " " + result)
}

func (m *fakeMetrics) CoordinationWait(_ context.Context, repository, reason string, d time.Duration) {
	m.add(fmt.Sprintf("coord_wait %s %s %s", repository, reason, d))
}

// coordinationSeries is what the rig recorded of the coordination series, in order.
func (g *rig) coordinationSeries() []string {
	g.metrics.mu.Lock()
	defer g.metrics.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(g.metrics.recorded), func(s string) bool { return !strings.HasPrefix(s, "coord_") })
}

// The reconciler's lineage refusals are metric reasons too, never other.
func TestCoordinationLineageReasonsAreMetricReasons(t *testing.T) {
	for _, err := range []error{errCrossRepositoryLineage, errCoordinationLineage, errCoordinationParent, errCoordinationScopeChanged,
		errCoordinationArtifacts, coordination.ErrIntegrationStale} {
		if !slices.Contains(fmetrics.CoordinationReasons(), err.Error()) {
			t.Errorf("%s is not a metric reason", err)
		}
	}
}

// v4 observability: each series is recorded at its real point, once per event however many passes
// replay it: a refused filing (on the primary), each child admitted (in its own repository, by its
// plan class), each child landed, an integration attempt's end with its wait, the family done, a
// recovery that stood until it was replaced, and a reverted child.
func TestCoordinationMetricsAreRecordedAtTheirPoints(t *testing.T) {
	t.Run("a refused filing counts once", func(t *testing.T) {
		p := coordinationPlan(t, true)
		g, fc := coordinationRig(t, config.CoordinationExecute, filedParent(p, coordination.ReasonConfirmed))
		fc.fileErr = coordination.ErrPrerequisites
		g.reconcile(t, p.ID, 4)
		if got := g.coordinationSeries(); !slices.Equal(got, []string{"coord_refused " + coordA + " coordination_prerequisites"}) {
			t.Fatalf("%q", got)
		}
	})

	t.Run("independent children admitted once each", func(t *testing.T) {
		p := coordinationPlan(t, true)
		g, _ := coordinationRig(t, config.CoordinationExecute, filedParent(p, coordination.ReasonFiled))
		g.reconcile(t, p.ID, 4)
		got := g.coordinationSeries()
		for _, c := range p.Children {
			if n := strings.Count(strings.Join(got, "|"), "coord_child "+c.Repository+" "+c.Class+" admitted"); n != 1 {
				t.Fatalf("%s admitted %d times: %q", c.ID, n, got)
			}
		}
	})

	t.Run("landed, integrated, done", func(t *testing.T) {
		parent, contract, consumer := landedFamily(t)
		contract.Status.Trace = &v1alpha1.TraceRef{TraceID: strings.Repeat("c", 32), SpanID: strings.Repeat("d", 16)}
		g, _ := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer)
		sink := &fakeSink{}
		g.r.Trace = sink
		p := g.reconcile(t, parent.Name, 3)
		a := p.Status.Coordination.IntegrationAttempts[0]
		g.r.Now = func() time.Time { return now.Add(time.Hour) }
		it, _ := g.coordTask(t, a.Task)
		it.CreationTimestamp = metav1.NewTime(now) // the fake API server stamps none
		if err := g.c.Update(t.Context(), it); err != nil {
			t.Fatal(err)
		}
		g.integrated(t, a.Task, coordination.ResultPassed, a.ManifestHash)
		if p = g.reconcile(t, parent.Name, 3); p.Status.Phase != v1alpha1.PhaseDone {
			t.Fatalf("parent %s/%s", p.Status.Phase, p.Status.Reason)
		}
		want := []string{"coord_child " + coordA + " scripts landed", "coord_child " + coordB + " scripts landed",
			"coord_integration " + coordA + " final passed", "coord_wait " + coordA + " integration 1h0m0s",
			"coord_family " + coordA + " done"}
		if got := g.coordinationSeries(); !slices.Equal(got, want) {
			t.Fatalf("%q\nwant %q", got, want)
		}
		// The parent's root span: its version and hash (cut to a prefix by tracing), and a link to
		// each family Task that has a root span; no plan text.
		if len(sink.got) != 1 || sink.got[0].TaskID != parent.Name || sink.got[0].Coordination == nil {
			t.Fatalf("spans %+v", sink.got)
		}
		c := sink.got[0].Coordination
		if c.PlanVersion != p.Status.Coordination.PlanVersion || c.PlanHash != p.Status.Coordination.PlanHash || len(c.Links) != 1 ||
			c.Links[0] != (tracing.Link{TraceID: strings.Repeat("c", 32), SpanID: strings.Repeat("d", 16)}) {
			t.Fatalf("coordination span %+v", c)
		}
	})

	t.Run("a recovery's wait ends when it is replaced", func(t *testing.T) {
		p := coordinationPlan(t, true)
		parent, kids := filedFamily(p)
		contract, consumer := kids[0], kids[1]
		merged := metav1.NewTime(now.Add(-time.Hour))
		contract.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, PullRequest: &v1alpha1.PullRequestRef{Number: 12,
			MergeCommitSHA: strings.Repeat("a", 40), MergedAt: &merged}}
		consumer.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseClosed, Reason: "pr_closed", PullRequest: &v1alpha1.PullRequestRef{Number: 13}}
		g, _ := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer)
		g.reconcile(t, p.ID, 2)
		c, _ := g.coordTask(t, contract.Name)
		c.Status.Phase, c.Status.Reason = v1alpha1.PhaseReverted, "revert_requested"
		if err := g.c.Status().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		g.r.Now = func() time.Time { return now.Add(2 * time.Hour) }
		g.reconcile(t, p.ID, 2)
		if got := g.coordinationSeries(); !slices.Contains(got, "coord_wait "+coordA+" recovery 2h0m0s") {
			t.Fatalf("%q", got)
		}
	})

	t.Run("a reverted child", func(t *testing.T) {
		p := coordinationPlan(t, true)
		parent, kids := filedFamily(p)
		g, _ := coordinationRig(t, config.CoordinationExecute, parent, kids[0])
		g.r.tellCoordinationParent(t.Context(), kids[0], 99)
		if got := g.coordinationSeries(); !slices.Equal(got, []string{"coord_child " + coordA + " scripts reverted"}) {
			t.Fatalf("%q", got)
		}
	})
}
