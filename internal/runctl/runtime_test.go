// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

const jvmImage = "ghcr.io/acme/maku-harness-jvm@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// Developer review gap 2: a repository's runtime image replaces the harness image, only under the
// platform's allowed prefixes, and never for OpenCode.
func TestARunTakesItsRuntimeImageWhenAllowed(t *testing.T) {
	c := testConfig(t)
	withImage := func(r *v1alpha1.AgentRun) { r.Spec.Image = jvmImage }
	if why := c.Invalid(run("run-aaaaaaaa", withImage)); !strings.Contains(why, "runtime.allowedImages") {
		t.Errorf("no allowed prefix: %q", why)
	}
	c.Runtime.AllowedImages = []string{"ghcr.io/acme/maku-harness-"}
	if why := c.Invalid(run("run-aaaaaaaa", withImage)); why != "" {
		t.Errorf("allowed: %q", why)
	}
	c.Images.Opencode = "ghcr.io/smana/maku-opencode:v0.16.0"
	if why := c.Invalid(run("run-aaaaaaaa", withImage, func(r *v1alpha1.AgentRun) { r.Spec.Harness = "opencode" })); !strings.Contains(why, "opencode") {
		t.Errorf("opencode: %q", why)
	}
	if p := Pod(run("run-aaaaaaaa", withImage), c, testModel, "aaaaaaaa"); p.Spec.Containers[0].Image != jvmImage {
		t.Errorf("harness image %q", p.Spec.Containers[0].Image)
	}
	if p := Pod(run("run-aaaaaaaa"), c, testModel, "aaaaaaaa"); p.Spec.Containers[0].Image != c.Images.Harness {
		t.Errorf("default image %q", p.Spec.Containers[0].Image)
	}
}
