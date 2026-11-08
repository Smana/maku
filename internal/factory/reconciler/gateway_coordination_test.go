// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
)

// v4 "No new authority": a whole family, from its landed children to Done, runs only integration
// claims, each a triager on the primary with no branch, through the existing primary
// octo-sts/<primary>/<triager> path; the parent runs nothing, opens no pull request and makes no
// merger call. The AgentRun role set is still the four roles, no coordination source names a
// gateway or STS identity, and a cross-repository integration Task or Room fails the existing
// lineage validation before any run.
func TestNoCoordinationGatewayOrParentAuthority(t *testing.T) {
	parent, contract, consumer := landedFamily(t)
	g, _ := coordinationRig(t, config.CoordinationExecute, parent, contract, consumer)
	a := g.reconcile(t, parent.Name, 3).Status.Coordination.IntegrationAttempts[0]
	g.reconcile(t, a.Task, 3) // its Room, Queued, its one run
	g.integrated(t, a.Task, coordination.ResultPassed, a.ManifestHash)
	p := g.reconcile(t, parent.Name, 3)
	if p.Status.Phase != v1alpha1.PhaseDone {
		t.Fatalf("parent %s/%s", p.Status.Phase, p.Status.Reason)
	}
	if len(g.runs.specs) != 1 {
		t.Fatalf("%d runs, want the one integration run", len(g.runs.specs))
	}
	for _, s := range g.runs.specs {
		if s.Role != "triager" || s.Repository != parent.Spec.Repository || s.Branch != "" || s.TaskID != a.Task || s.RoomRef != a.Task ||
			s.TaskID == parent.Name || len(s.CoordinationArtifacts) != 0 {
			t.Fatalf("an integration claim that is not a branchless primary triager: %+v", s)
		}
		if err := s.Validate(); err != nil {
			t.Fatalf("the claim is one the existing runs.Client validates: %v", err)
		}
	}
	if len(p.Status.Runs) != 0 || p.Status.PullRequest != nil || len(g.f.Armed()) != 0 || len(g.f.Reverts()) != 0 || len(g.f.Disarmed()) != 0 {
		t.Fatalf("the parent acted: runs %d pr %+v armed %v reverts %v", len(p.Status.Runs), p.Status.PullRequest, g.f.Armed(), g.f.Reverts())
	}

	// The AgentRun role enum is unchanged: no coordinator or integrator role exists.
	crd, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "crd", "maku.ogenki.io_agentruns.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^ +role:\n +enum:\n((?: +- [a-z]+\n)+)`).FindSubmatch(crd)
	if m == nil {
		t.Fatal("no role enum in the AgentRun CRD")
	}
	var roles []string
	for _, l := range strings.Split(strings.TrimSpace(string(m[1])), "\n") {
		roles = append(roles, strings.TrimPrefix(strings.TrimSpace(l), "- "))
	}
	if !slices.Equal(roles, []string{"implementer", "reviewer", "tester", "triager"}) {
		t.Fatalf("AgentRun roles %v", roles)
	}

	// No coordination source names a gateway, an audience or an STS identity of its own.
	authority := regexp.MustCompile(`(?i)octo-sts|audience|gateway|sts\.amazonaws|AssumeRole`)
	srcs, _ := filepath.Glob(filepath.Join("..", "coordination", "*.go"))
	srcs = append(srcs, "coordination.go", "coordination_child.go", "coordination_watchers.go", "integration.go")
	for _, f := range srcs {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := authority.FindIndex(b); loc != nil {
			t.Errorf("%s names new authority: %q", f, b[loc[0]:loc[1]])
		}
	}

	// A cross-repository integration Task, or one whose Room is another repository's, is refused
	// by the existing lineage check before any run.
	for name, edit := range map[string]func(*v1alpha1.Task) []client.Object{
		"another repository": func(it *v1alpha1.Task) []client.Object { it.Spec.Repository = coordB; return nil },
		"a Room on another repository": func(it *v1alpha1.Task) []client.Object {
			return []client.Object{&roomv1.Room{ObjectMeta: metav1.ObjectMeta{Name: it.Name, Namespace: "agent-system"},
				Spec: roomv1.RoomSpec{Owner: "system:factory", Driver: "system:factory", DataClass: "public", Repository: coordB}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			parent, contract, consumer := landedFamily(t)
			cs := parent.Status.Coordination
			m, err := coordination.FinalManifest(parent.Name, cs, map[string]string{"contract": producerMerg, "consumer": consumerMerge})
			if err != nil {
				t.Fatal(err)
			}
			it := coordination.IntegrationTask("agent-system", cfg(), parent, m, 1)
			it.CreationTimestamp = metav1.NewTime(now.Add(-time.Minute))
			objs := append([]client.Object{parent, contract, consumer, it}, edit(it)...)
			g, _ := coordinationRig(t, config.CoordinationExecute, objs...)
			got := g.reconcile(t, it.Name, 3)
			if got.Status.Phase != v1alpha1.PhaseRejected || got.Status.Reason != "cross_repository_lineage" || len(g.runs.specs) != 0 {
				t.Fatalf("%s/%s, %d runs, want Rejected/cross_repository_lineage", got.Status.Phase, got.Status.Reason, len(g.runs.specs))
			}
		})
	}
}
