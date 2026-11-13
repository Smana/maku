// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/runwatch"
)

// HR36: a run annotated forge-api: deny gets the github profile without api.github.com, in its
// FQDNs, its CNP and its egress proxy allow-list, keeps git's hosts, and gets FORGE_READ=1.
func TestFQDNsForgeAPIDeny(t *testing.T) {
	denied := goRun(func(r *v1alpha1.AgentRun) {
		r.Annotations = map[string]string{runwatch.ForgeAPIAnnotation: runwatch.ForgeAPIDeny}
	})
	c := testConfig(t)
	got := c.FQDNs(denied)
	if slices.Contains(got, ForgeAPIHost) {
		t.Fatalf("api.github.com kept: %v", got)
	}
	for _, h := range []string{"github.com", "codeload.github.com", "objects.githubusercontent.com"} {
		if !slices.Contains(got, h) {
			t.Fatalf("%s dropped: %v", h, got)
		}
	}
	if plain := c.FQDNs(goRun()); !slices.Contains(plain, ForgeAPIHost) || len(plain) != len(got)+1 {
		t.Fatalf("without the annotation the list changes: %v", plain)
	}
	cnp, _ := json.Marshal(NetworkPolicy(denied, c, testModel, "abcdefgh").Object)
	if strings.Contains(string(cnp), ForgeAPIHost) {
		t.Fatalf("the CNP opens api.github.com: %s", cnp)
	}
	kube := kubeConfig(t)
	p := Pod(denied, kube, testModel, "abcdefgh")
	if strings.Contains(p.Annotations[EgressAnnotation], ForgeAPIHost) {
		t.Fatalf("the egress proxy allows api.github.com: %s", p.Annotations[EgressAnnotation])
	}
	if env := harnessEnv(p); env["FORGE_READ"] != "1" {
		t.Fatalf("FORGE_READ %q", env["FORGE_READ"])
	}
	if env := harnessEnv(Pod(goRun(), kube, testModel, "abcdefgh")); env["FORGE_READ"] != "" {
		t.Fatal("FORGE_READ without the annotation")
	}
}

// HR37: under forge-api: deny the reviewer's rules name forge_read and git diff for the pull
// request's diff; without it the rules are as before.
func TestRulesReviewerDiff(t *testing.T) {
	reviewer := func(r *v1alpha1.AgentRun) { r.Spec.Role = "reviewer" }
	denied := run("run-abcdefgh", reviewer, func(r *v1alpha1.AgentRun) {
		r.Annotations = map[string]string{runwatch.ForgeAPIAnnotation: runwatch.ForgeAPIDeny}
	})
	got := Rules(denied, "abcdefgh")
	if !strings.Contains(got, "git diff origin/"+denied.Spec.BaseRef+"...HEAD") || !strings.Contains(got, "forge_read tool (target issue, pr or checks)") {
		t.Fatalf("reviewer rules:\n%s", got)
	}
	if plain := Rules(run("run-abcdefgh", reviewer), "abcdefgh"); strings.Contains(plain, "forge_read") {
		t.Fatalf("rules without the annotation:\n%s", plain)
	}
}
