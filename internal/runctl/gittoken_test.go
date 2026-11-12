// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/runs"
)

// gitVolume is the Secret behind the pod's github volume.
func gitVolume(t *testing.T, p *corev1.Pod) string {
	t.Helper()
	for _, v := range p.Spec.Volumes {
		if v.Name == "github" && v.Secret != nil {
			return v.Secret.SecretName
		}
	}
	t.Fatal("no github volume")
	return ""
}

func role(name string) func(*v1alpha1.AgentRun) {
	return func(r *v1alpha1.AgentRun) { r.Spec.Role = name }
}

// PR2: the run-controller picks the git Secret, so it enforces the role: reviewer and triager get
// the read-only token, implementer and tester the write one.
func TestGitSecretByRole(t *testing.T) {
	c := testConfig(t)
	for r, want := range map[string]string{"implementer": "agent-git", "tester": "agent-git", "reviewer": "agent-git-read", "triager": "agent-git-read"} {
		if got := gitVolume(t, Pod(run("run-abcdefgh", role(r)), c, testModel, "abcdefgh")); got != want {
			t.Errorf("%s mounts %q, want %q", r, got, want)
		}
	}
}

func TestPodReviewerGolden(t *testing.T) {
	golden(t, "pod-reviewer.golden.yaml", Pod(goRun(role("reviewer")), testConfig(t), testModel, "abcdefgh"))
}

func TestLoadConfigGitReadSecret(t *testing.T) {
	c, err := cfgFrom(t, strings.Replace(goodConfig, "gitReadSecret: agent-git-read\n", "", 1)+"unsafeSharedGitToken: true\n")
	if err != nil || c.GitReadSecret != "" || !c.UnsafeSharedGitToken {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := cfgFrom(t, goodConfig+"unsafeSharedGitToken: true\n"); err == nil || !strings.Contains(err.Error(), "choose one") {
		t.Fatalf("both: want a refusal, got %v", err)
	}
}

func TestInvalidReadOnlyRoles(t *testing.T) {
	c := testConfig(t)
	c.GitReadSecret = ""
	const want = "role triager needs a read-only token: set runController.gitReadSecret (or unsafeSharedGitToken on a laptop)"
	if got := c.Invalid(run("run-abcdefgh", role("triager"))); got != want {
		t.Fatalf("triager, no read token: %q", got)
	}
	if got := c.Invalid(run("run-abcdefgh", role("reviewer"))); !strings.Contains(got, "role reviewer needs a read-only token") {
		t.Fatalf("reviewer, no read token: %q", got)
	}
	if got := c.Invalid(run("run-abcdefgh")); got != "" {
		t.Fatalf("an implementer needs no read token: %q", got)
	}
	c.UnsafeSharedGitToken = true
	tr := run("run-abcdefgh", role("triager"))
	if got := c.Invalid(tr); got != "" {
		t.Fatalf("unsafeSharedGitToken: %q", got)
	}
	if got := gitVolume(t, Pod(tr, c, testModel, "abcdefgh")); got != "agent-git" {
		t.Fatalf("unsafeSharedGitToken: the triager mounts %q, want the shared agent-git", got)
	}
}

func scoper(r *v1alpha1.AgentRun) {
	r.Spec.Role, r.Spec.Task.Text = "triager", runs.ScoperMarkerPrefix+"nudgeAt=40 maxToolCalls=60 -->\nscope it\n"
}

// PR1 gap 2: a scoper ends with room_handoff, a room tool, which this runtime serves only through
// roomToolsURL.
func TestInvalidScoperNeedsRoomTools(t *testing.T) {
	const want = "a scoper hands off with room_handoff: set broker.roomTools.auth tokenreview"
	if got := testConfig(t).Invalid(run("run-abcdefgh", scoper)); got != want {
		t.Fatalf("scoper without room tools: %q", got)
	}
	if got := withRoomTools(t).Invalid(run("run-abcdefgh", scoper)); got != "" {
		t.Fatalf("scoper with room tools: %q", got)
	}
	if got := testConfig(t).Invalid(run("run-abcdefgh", role("triager"))); got != "" {
		t.Fatalf("a plain triager needs no room tools: %q", got)
	}
	// The harness gets MCP only for a run with a room: a roomless scoper could not hand off either.
	roomless := run("run-abcdefgh", scoper, func(r *v1alpha1.AgentRun) { r.Spec.RoomRef = "" })
	if got := withRoomTools(t).Invalid(roomless); got != "a scoper hands off with room_handoff: it needs a room (spec.roomRef)" {
		t.Fatalf("roomless scoper: %q", got)
	}
}

// GitSecretFor fails closed on its own: a read-only role with neither a read Secret nor the unsafe
// flag gets no Secret name, so a path that skipped Invalid would build an unmountable pod, never
// one holding the write token.
func TestGitSecretForFailsClosed(t *testing.T) {
	c := testConfig(t)
	c.GitReadSecret = ""
	for _, r := range []string{"reviewer", "triager"} {
		if got := c.GitSecretFor(r); got != "" {
			t.Errorf("%s: %q, want none", r, got)
		}
	}
	if got := c.GitSecretFor("implementer"); got != "agent-git" {
		t.Errorf("implementer: %q", got)
	}
	c.UnsafeSharedGitToken = true
	if got := c.GitSecretFor("reviewer"); got != "agent-git" {
		t.Errorf("unsafe reviewer: %q", got)
	}
}

// A reviewer run waits for its read-only Secret, as for the others.
func TestEnvtestReviewerWaitsForTheReadSecret(t *testing.T) {
	e := startEnv(t)
	e.modelSecret(t)
	if err := e.c.Delete(e.ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "agent-git-read", Namespace: "agents"}}); err != nil {
		t.Fatal(err)
	}
	create(t, e, run("run-rrrrrrrr", role("reviewer"), func(r *v1alpha1.AgentRun) {
		r.Spec.Task = v1alpha1.AgentRunTask{URL: "https://github.com/o/r/pull/12"}
	}))
	if res := e.reconcile(t, "run-rrrrrrrr"); res.RequeueAfter == 0 {
		t.Fatal("want a requeue while the Secret is missing")
	}
	got := e.get(t, "run-rrrrrrrr")
	c := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if got.Status.Phase != "Pending" || c == nil || c.Reason != "SecretMissing" || !strings.Contains(c.Message, "agent-git-read") {
		t.Fatalf("status %+v", got.Status)
	}
	if err := e.c.Get(e.ctx, key("run-rrrrrrrr"), &corev1.Pod{}); err == nil {
		t.Fatal("no pod before the read Secret exists")
	}
	// An implementer does not wait for it.
	create(t, e, run("run-iiiiiiii"))
	e.reconcile(t, "run-iiiiiiii")
	if err := e.c.Get(e.ctx, key("run-iiiiiiii"), &corev1.Pod{}); err != nil {
		t.Fatalf("implementer pod: %v", err)
	}
}
