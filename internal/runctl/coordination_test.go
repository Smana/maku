// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/runs"
)

const manifestJSON = `{"parentTask":"p4zt6wce","planVersion":1}`

func integrationRun(mut ...func(*v1alpha1.AgentRun)) *v1alpha1.AgentRun {
	return run("run-abcdefgh", append([]func(*v1alpha1.AgentRun){func(r *v1alpha1.AgentRun) {
		r.Spec.Role = "triager"
		r.Annotations = map[string]string{annCoordinationManifest: manifestJSON,
			annCoordinationManifestHash: fmt.Sprintf("%x", sha256.Sum256([]byte(manifestJSON)))}
	}}, mut...)...)
}

// The run controller reads the very annotations the factory writes at CREATE.
func TestCoordinationAnnotationsAreTheFactorys(t *testing.T) {
	if annCoordinationManifest != runs.AnnCoordinationManifest || annCoordinationManifestHash != runs.AnnCoordinationManifestHash {
		t.Fatalf("%q %q", annCoordinationManifest, annCoordinationManifestHash)
	}
}

// v4 MR10: an integration run's manifest and its hash reach the harness exactly; any other run gets
// neither variable.
func TestCoordinationManifestReachesTheHarness(t *testing.T) {
	envOf := func(r *v1alpha1.AgentRun) map[string]string {
		env := map[string]string{}
		for _, e := range Pod(r, testConfig(t), testModel, "abcdefgh").Spec.Containers[0].Env {
			env[e.Name] = e.Value
		}
		return env
	}
	r := integrationRun()
	env := envOf(r)
	if env[EnvCoordinationManifest] != manifestJSON || env[EnvCoordinationManifestHash] != r.Annotations[annCoordinationManifestHash] {
		t.Fatalf("env %v", env)
	}
	env = envOf(run("run-abcdefgh"))
	if _, ok := env[EnvCoordinationManifest]; ok {
		t.Fatal("a run without a manifest got one")
	}
	if _, ok := env[EnvCoordinationManifestHash]; ok {
		t.Fatal("a run without a manifest got a hash")
	}
}

// A manifest the claim cannot vouch for refuses the run before any pod: the harness never sees it.
func TestInvalidCoordinationManifest(t *testing.T) {
	c := testConfig(t)
	for name, tc := range map[string]struct {
		r    *v1alpha1.AgentRun
		want string
	}{
		"valid": {integrationRun(), ""},
		"wrong hash": {integrationRun(func(r *v1alpha1.AgentRun) {
			r.Annotations[annCoordinationManifestHash] = strings.Repeat("0", 64)
		}), "SHA-256"},
		"changed byte": {integrationRun(func(r *v1alpha1.AgentRun) {
			r.Annotations[annCoordinationManifest] = strings.Replace(manifestJSON, "1", "2", 1)
		}), "SHA-256"},
		"no hash":         {integrationRun(func(r *v1alpha1.AgentRun) { delete(r.Annotations, annCoordinationManifestHash) }), "SHA-256"},
		"hash only":       {integrationRun(func(r *v1alpha1.AgentRun) { delete(r.Annotations, annCoordinationManifest) }), "manifest"},
		"an implementer":  {integrationRun(func(r *v1alpha1.AgentRun) { r.Spec.Role = "implementer" }), "triager"},
		"a branch":        {integrationRun(func(r *v1alpha1.AgentRun) { r.Spec.Branch = "agent/x" }), "branch"},
		"not json":        {integrationRun(func(r *v1alpha1.AgentRun) { setManifest(r, "{") }), "JSON"},
		"over the bound":  {integrationRun(func(r *v1alpha1.AgentRun) { setManifest(r, `"`+strings.Repeat("a", 8192)+`"`) }), "JSON"},
		"no manifest yet": {run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.Role = "triager" }), ""},
	} {
		if got := c.Invalid(tc.r); (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: Invalid = %q, want it to mention %q", name, got, tc.want)
		}
	}
}

func setManifest(r *v1alpha1.AgentRun, raw string) {
	r.Annotations[annCoordinationManifest] = raw
	r.Annotations[annCoordinationManifestHash] = fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
}
