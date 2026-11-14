// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

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

// A laptop's one PAT (gitSecret, demo-only in the chart since v4 step 8): every role mounts it.
func TestGitSecretEveryRoleOnALaptop(t *testing.T) {
	c := testConfig(t)
	for _, r := range []string{"implementer", "tester", "reviewer", "triager"} {
		if got := gitVolume(t, Pod(run("run-abcdefgh", role(r)), c, testModel, "abcdefgh")); got != "agent-git" {
			t.Errorf("%s mounts %q, want agent-git", r, got)
		}
		if got := c.Invalid(run("run-abcdefgh", role(r))); got != "" {
			t.Errorf("%s: %q", r, got)
		}
	}
}

func TestPodReviewerGolden(t *testing.T) {
	golden(t, "pod-reviewer.golden.yaml", Pod(goRun(role("reviewer")), testConfig(t), testModel, "abcdefgh"))
}

// v4 step 8: the shared-token keys are removed, each refused by name with what replaced it.
func TestLoadConfigRefusesTheRemovedKeys(t *testing.T) {
	for key, want := range map[string]string{
		"gitTokens: perRun\n":             "gitTokens was removed in v4 step 8",
		"gitReadSecret: agent-git-read\n": "gitReadSecret was removed in v4 step 8",
		"unsafeSharedGitToken: true\n":    "unsafeSharedGitToken was removed in v4 step 8: a laptop's one PAT is gitSecret alone",
	} {
		if _, err := cfgFrom(t, goodConfig+key); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", strings.TrimSpace(key), err, want)
		}
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
