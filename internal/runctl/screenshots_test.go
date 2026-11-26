// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

const browserImage = "ghcr.io/smana/maku-browser:v0.60.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func withScreenshots(r *v1alpha1.AgentRun) {
	r.Spec.Screenshots = &v1alpha1.AgentRunScreenshots{Enabled: true, MaxScreenshots: 10}
}

// v5 Q16, VS5: the controller refuses a browser on any role but implementer and tester, and a browser
// it has no image for.
func TestScreenshotsRefusals(t *testing.T) {
	c := testConfig(t)
	if why := c.Invalid(run("run-aaaaaaaa", withScreenshots)); !strings.Contains(why, "images.browser") {
		t.Errorf("no browser image: %q", why)
	}
	c.Images.Browser = browserImage
	for name, tc := range map[string]struct {
		mut  func(*v1alpha1.AgentRun)
		want string
	}{
		"an implementer is accepted":      {mut: func(*v1alpha1.AgentRun) {}},
		"a tester is accepted":            {mut: role("tester")},
		"a reviewer is refused":           {mut: role("reviewer"), want: "implementer or tester"},
		"a triager is refused":            {mut: role("triager"), want: "implementer or tester"},
		"a disabled opt-in is no browser": {mut: func(r *v1alpha1.AgentRun) { r.Spec.Role, r.Spec.Screenshots.Enabled = "reviewer", false }},
	} {
		t.Run(name, func(t *testing.T) {
			r := run("run-aaaaaaaa", withScreenshots, tc.mut)
			if r.Spec.Role == "reviewer" {
				r.Spec.Task = v1alpha1.AgentRunTask{URL: "https://github.com/o/r/pull/1"}
			}
			why := c.Invalid(r)
			if tc.want == "" && why != "" || tc.want != "" && !strings.Contains(why, tc.want) {
				t.Errorf("got %q, want %q", why, tc.want)
			}
		})
	}
}

// The browser's resources default to the spec's §4.1 values; a chart value overrides one key only.
func TestBrowserResourcesDefault(t *testing.T) {
	c := testConfig(t)
	r := c.Browser.Resources
	for name, tc := range map[string]struct{ got, want resource.Quantity }{
		"requests.cpu":    {r.Requests.Cpu().DeepCopy(), resource.MustParse("250m")},
		"requests.memory": {r.Requests.Memory().DeepCopy(), resource.MustParse("512Mi")},
		"limits.cpu":      {r.Limits.Cpu().DeepCopy(), resource.MustParse("2")},
		"limits.memory":   {r.Limits.Memory().DeepCopy(), resource.MustParse("2Gi")},
	} {
		if tc.got.Cmp(tc.want) != 0 {
			t.Errorf("%s %s, want %s", name, tc.got.String(), tc.want.String())
		}
	}
	c, err := cfgFrom(t, goodConfig+"browser:\n  resources:\n    limits: {memory: 3Gi}\n")
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Browser.Resources.Limits.Memory(); m.Cmp(resource.MustParse("3Gi")) != 0 {
		t.Errorf("limits.memory %s, want 3Gi", m)
	}
	if cpu := c.Browser.Resources.Limits.Cpu(); cpu.Cmp(resource.MustParse("2")) != 0 {
		t.Errorf("limits.cpu %s, want the default 2", cpu)
	}
	if _, err := cfgFrom(t, goodConfig+"browser:\n  resources:\n    requests: {memory: 4Gi}\n"); err == nil || !strings.Contains(err.Error(), "browser.resources") {
		t.Errorf("a request above its limit was accepted: %v", err)
	}
}

// C4: the vision list sits beside the aliases; it decides only whether images reach the model.
func TestVisionFor(t *testing.T) {
	c := gatewayTestConfig(t)
	c.Model.Gateway.Vision = GatewayAliases{Public: []string{"tier-standard"}, Internal: []string{"tier-light"}}
	pub := run("run-aaaaaaaa")
	if !c.VisionFor(pub, "tier-standard") || c.VisionFor(pub, "agent-default") || c.VisionFor(pub, "tier-light") {
		t.Error("public list")
	}
	in := run("run-aaaaaaaa", func(r *v1alpha1.AgentRun) { r.Spec.DataClass = "internal" })
	if !c.VisionFor(in, "tier-light") || c.VisionFor(in, "tier-standard") {
		t.Error("internal list")
	}
	if testConfig(t).VisionFor(pub, "tier-standard") {
		t.Error("model.mode secret: no alias is known to see images")
	}
}

func TestVisionAliasMustBeServed(t *testing.T) {
	c, err := cfgFrom(t, gatewayConfig+"    vision: {public: [tier-standard], internal: [tier-light]}\n")
	if err != nil {
		t.Fatalf("served vision aliases: %v", err)
	}
	if len(c.Model.Gateway.Vision.Public) != 1 {
		t.Fatalf("vision %+v", c.Model.Gateway.Vision)
	}
	for name, vision := range map[string]string{
		"public":   "    vision: {public: [not-served]}\n",
		"internal": "    vision: {internal: [kimi-frontier]}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := cfgFrom(t, gatewayConfig+vision)
			if err == nil || !strings.Contains(err.Error(), "model.gateway.vision."+name) {
				t.Fatalf("a vision alias the listener does not serve is refused at load: %v", err)
			}
		})
	}
}
