// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
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

// imagingForge is the fake forge reading an issue's images (v5 Q16 B3).
type imagingForge struct {
	*forgetest.Fake
	sum  string
	imgs []forge.IssueImage
}

func (f imagingForge) IssueImages(context.Context, int, int) (string, []forge.IssueImage, error) {
	return f.sum, f.imgs, nil
}

// imageLog is the fake room log storing request images.
type imageLog struct {
	*fakeLog
	put map[int][]byte
}

func (l *imageLog) PutRequestImage(_ context.Context, _ string, n int, b []byte) error {
	l.put[n] = b
	return nil
}

func TestAttachRequestImages(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "fix it")
	tk.Spec.Source.ContentSHA256 = "sum-as-labelled"
	tk.Status.RoomRef = "3buqdlot"
	imgs := []forge.IssueImage{{N: 1, Alt: "the bug", Bytes: []byte("a")}, {N: 3, Alt: "", Bytes: []byte("c")}}
	try := func(sum string, s runs.Spec) (runs.Spec, *imageLog) {
		log := &imageLog{fakeLog: &fakeLog{}, put: map[int][]byte{}}
		r := &Reconciler{Forge: imagingForge{Fake: forgetest.NewFake(), sum: sum, imgs: imgs}, Rooms: log, Log: slog.New(slog.DiscardHandler)}
		r.attachRequestImages(t.Context(), tk, &s)
		return s, log
	}
	s, log := try("sum-as-labelled", runs.Spec{Role: "implementer", Screenshots: 10, TaskText: "brief"})
	if len(log.put) != 2 || string(log.put[3]) != "c" {
		t.Fatalf("stored %v", log.put)
	}
	for _, want := range []string{"brief\n\nImages attached to the request", "- image 1: \"the bug\"\n", "- image 3\n", "room_image"} {
		if !strings.Contains(s.TaskText, want) {
			t.Errorf("brief lacks %q:\n%s", want, s.TaskText)
		}
	}
	if s, log := try("edited-since", runs.Spec{Role: "implementer", Screenshots: 10, TaskText: "brief"}); len(log.put) != 0 || s.TaskText != "brief" {
		t.Errorf("an issue edited since it was labelled attaches nothing: %v %q", log.put, s.TaskText)
	}
	if s, log := try("sum-as-labelled", runs.Spec{Role: "implementer", TaskText: "brief"}); len(log.put) != 0 || s.TaskText != "brief" {
		t.Errorf("a run without screenshots attaches nothing: %v %q", log.put, s.TaskText)
	}
}
