// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/runs"
)

// v5 Q16: triage freezes the Repository's screenshots opt-in on the task, and the implementer run
// carries it, read from the task, not from today's config.
func TestScreenshotsFrozenAtTriageAndStampedOnTheImplementer(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "fix it"))
	c := g.r.Cfg.Current()
	if c.HarnessProfiles == nil {
		c.HarnessProfiles = map[string]config.HarnessProfile{}
	}
	c.HarnessProfiles[config.WorkflowStandard] = config.HarnessProfile{Template: "solo"} // a served workflow: triage records a profile
	c.Repositories[0].Screenshots = &config.Screenshots{Enabled: true, MaxScreenshots: 7}
	tk := g.reconcile(t, "3buqdlot", 1) // Received → Triaged
	if p := tk.Status.Profile; p == nil || p.Screenshots == nil || !p.Screenshots.Enabled || p.Screenshots.MaxScreenshots != 7 {
		t.Fatalf("triage freezes the repository's screenshots: %+v", tk.Status.Profile)
	}
	c.Repositories[0].Screenshots.Enabled = false // a later config change
	tk = g.reconcile(t, "3buqdlot", 2)            // → Queued → Implementing
	s, ok := g.runs.specs[rid(0)]
	if !ok || s.Role != "implementer" || s.Screenshots != 7 {
		t.Fatalf("the implementer reads the task's frozen value: %+v", s)
	}
	if !tk.Status.Runs[0].Provenance.Screenshots {
		t.Fatalf("provenance: %+v", tk.Status.Runs[0].Provenance)
	}
}

// A repository without the opt-in stamps no run.
func TestNoScreenshotsWithoutTheOptIn(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "fix it"))
	tk := g.reconcile(t, "3buqdlot", 3)
	if s := g.runs.specs[rid(0)]; s.Role != "implementer" || s.Screenshots != 0 || tk.Status.Runs[0].Provenance.Screenshots {
		t.Fatalf("no opt-in, no browser: %+v", s)
	}
}

func TestStampScreenshotsRoles(t *testing.T) {
	task := func(kind string) *v1alpha1.Task {
		return &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Kind: kind},
			Status: v1alpha1.TaskStatus{Profile: &v1alpha1.ProfileStatus{Screenshots: &config.Screenshots{Enabled: true}}}}
	}
	for name, tc := range map[string]struct {
		kind, role string
		want       int32
	}{
		"an implementer gets the default cap": {role: "implementer", want: config.DefaultMaxScreenshots},
		"a tester gets the default cap":       {role: "tester", want: config.DefaultMaxScreenshots},
		"a reviewer never gets the browser":   {role: "reviewer"},
		"a triager never gets the browser":    {role: "triager"},
		"an integration task never does":      {kind: "integration", role: "implementer"},
	} {
		t.Run(name, func(t *testing.T) {
			s := runs.Spec{Role: tc.role, Screenshots: 99}
			stampScreenshots(task(tc.kind), &s)
			if s.Screenshots != tc.want {
				t.Errorf("got %d, want %d", s.Screenshots, tc.want)
			}
		})
	}
	s := runs.Spec{Role: "implementer"}
	stampScreenshots(&v1alpha1.Task{}, &s) // a task triaged before the field: no profile
	if s.Screenshots != 0 {
		t.Errorf("no profile: %d", s.Screenshots)
	}
}

// A cluster whose AgentRun schema lacks spec.screenshots (an older CRD, or the host's XRD) refuses the
// claim under Strict validation: the task escalates run_refused once, never retried forever.
func TestARefusedScreenshotsClaimEscalates(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "fix it"))
	c := g.r.Cfg.Current()
	if c.HarnessProfiles == nil {
		c.HarnessProfiles = map[string]config.HarnessProfile{}
	}
	c.HarnessProfiles[config.WorkflowStandard] = config.HarnessProfile{Template: "solo"}
	c.Repositories[0].Screenshots = &config.Screenshots{Enabled: true}
	creates := 0
	g.runs.refuse = func(s runs.Spec) error {
		if s.Role != "implementer" {
			return nil
		}
		creates++
		return apierrors.NewBadRequest(`strict decoding error: unknown field "spec.screenshots"`)
	}
	tk := g.reconcile(t, "3buqdlot", 4)
	if tk.Status.Phase != v1alpha1.PhaseEscalated || tk.Status.Reason != "run_refused" || creates != 1 {
		t.Fatalf("%s %s after %d creates, want Escalated run_refused after 1", tk.Status.Phase, tk.Status.Reason, creates)
	}
}
