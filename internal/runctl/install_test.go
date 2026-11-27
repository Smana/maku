// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

var releaseHosts = []string{"release-assets.githubusercontent.com", "objects.githubusercontent.com"}

func checksRun(mut ...func(*v1alpha1.AgentRun)) *v1alpha1.AgentRun {
	return run("run-abcdefgh", append([]func(*v1alpha1.AgentRun){func(r *v1alpha1.AgentRun) { r.Spec.Loop.Checks = true }}, mut...)...)
}

// The always-on github profile is git and the API; GitHub's release hosts are the install set, and its
// raw and tarball hosts are in no set.
func TestDefaultGithubProfileIsGitAndTheAPI(t *testing.T) {
	c := testConfig(t)
	if got := c.EgressProfiles["github"]; !slices.Equal(got, []string{"github.com", "api.github.com"}) {
		t.Fatalf("github profile %v", got)
	}
	if !slices.Equal(c.InstallEgress.Hosts, releaseHosts) {
		t.Fatalf("install hosts %v", c.InstallEgress.Hosts)
	}
	if !slices.Equal(c.InstallEgress.HookProfiles, []string{"pypi", "npm", "crates"}) || c.InstallEgress.WindowSeconds != 1200 {
		t.Fatalf("install defaults %+v", c.InstallEgress)
	}
	if w := c.Warnings(); len(w) != 0 {
		t.Fatalf("the defaults warn of nothing: %v", w)
	}
	for _, h := range append(slices.Clone(releaseHosts), "codeload.github.com", "raw.githubusercontent.com") {
		if slices.Contains(c.FQDNs(checksRun()), h) {
			t.Errorf("%s is not in a run's set", h)
		}
	}
	for _, h := range []string{"codeload.github.com", "raw.githubusercontent.com"} {
		if slices.Contains(c.InstallFQDNs(checksRun()), h) {
			t.Errorf("%s is not in the install set", h)
		}
	}
}

func TestInstallFQDNs(t *testing.T) {
	c := testConfig(t)
	if got := c.InstallFQDNs(run("run-abcdefgh")); got != nil {
		t.Fatalf("a run without checks installs nothing, so it has no install set: %v", got)
	}
	got := c.InstallFQDNs(checksRun())
	want := append(slices.Clone(releaseHosts), "pypi.org", "files.pythonhosted.org", "registry.npmjs.org", "index.crates.io", "static.crates.io")
	if !slices.Equal(got, want) {
		t.Fatalf("a check run's install set: %v, want %v", got, want)
	}
	// A profile the run requests is its for the whole run (the agent adds dependencies), not the install's.
	withPypi := c.InstallFQDNs(checksRun(func(r *v1alpha1.AgentRun) { r.Spec.Egress.Profiles = []v1alpha1.AgentRunEgressProfile{"pypi"} }))
	if slices.Contains(withPypi, "pypi.org") || !slices.Contains(withPypi, "registry.npmjs.org") {
		t.Fatalf("pypi requested: install set %v", withPypi)
	}
}

func TestInstallEgressOverrides(t *testing.T) {
	c, err := cfgFrom(t, goodConfig+"installEgress:\n  hosts: [release-assets.githubusercontent.com]\n  hookProfiles: []\n  windowSeconds: 600\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.InstallFQDNs(checksRun()); !slices.Equal(got, []string{"release-assets.githubusercontent.com"}) || c.InstallEgress.WindowSeconds != 600 {
		t.Fatalf("overrides: %v %+v", got, c.InstallEgress)
	}
	for name, body := range map[string]string{
		"unknown hook profile": "installEgress:\n  hookProfiles: [docs]\n",
		"negative window":      "installEgress:\n  windowSeconds: -1\n",
		"a port on a host":     "installEgress:\n  hosts: [release-assets.githubusercontent.com:443]\n",
	} {
		if _, err := cfgFrom(t, goodConfig+body); err == nil || !strings.Contains(err.Error(), "installEgress") {
			t.Errorf("%s: want an installEgress error, got %v", name, err)
		}
	}
}

// An install that still lists the release hosts in its github profile keeps them for the whole run,
// and is told so.
func TestOldGithubProfileWarns(t *testing.T) {
	c, err := cfgFrom(t, goodConfig+`egressProfiles:
  github: [github.com, api.github.com, codeload.github.com, objects.githubusercontent.com, raw.githubusercontent.com, release-assets.githubusercontent.com]
`)
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(c.Warnings(), "\n")
	for _, h := range releaseHosts {
		if !strings.Contains(w, h) {
			t.Errorf("the warning names %s: %s", h, w)
		}
	}
	if !strings.Contains(w, "github-egress-tightening") {
		t.Errorf("the warning names the spec: %s", w)
	}
	if got := c.InstallFQDNs(checksRun()); slices.ContainsFunc(got, func(h string) bool { return slices.Contains(releaseHosts, h) }) {
		t.Fatalf("a host on the run set is not also on the install set: %v", got)
	}
}

func ctrEnv(ctr corev1.Container) map[string]corev1.EnvVar {
	out := map[string]corev1.EnvVar{}
	for _, e := range ctr.Env {
		out[e.Name] = e
	}
	return out
}

func TestKubernetesPodCarriesTheInstallSet(t *testing.T) {
	c := kubeConfig(t)
	p := Pod(checksRun(), c, testModel, "abcdefgh")
	if got := p.Annotations[EgressInstallAnnotation]; !strings.HasPrefix(got, "release-assets.githubusercontent.com:443,objects.githubusercontent.com:443,pypi.org:443") {
		t.Fatalf("install annotation %q", got)
	}
	for _, h := range releaseHosts {
		if strings.Contains(p.Annotations[EgressAnnotation], h) {
			t.Fatalf("the run set leaves out %s: %q", h, p.Annotations[EgressAnnotation])
		}
	}
	if _, ok := p.Annotations[EgressInstallUntilAnnotation]; ok {
		t.Fatal("the deadline is stamped at create, not by Pod")
	}
	e := ctrEnv(p.Spec.Containers[0])
	if e["EGRESS_INSTALL_CLOSE_URL"].Value != "http://egress-proxy.agent-platform.svc.cluster.local:3128/v1/close-install" {
		t.Fatalf("close URL %+v", e["EGRESS_INSTALL_CLOSE_URL"])
	}
	if f := e["POD_UID"].ValueFrom; f == nil || f.FieldRef == nil || f.FieldRef.FieldPath != "metadata.uid" {
		t.Fatalf("POD_UID from the downward API: %+v", e["POD_UID"])
	}
	for _, ctr := range append(p.Spec.InitContainers, p.Spec.Containers[1:]...) {
		if _, ok := ctrEnv(ctr)["EGRESS_INSTALL_CLOSE_URL"]; ok {
			t.Errorf("%s gets no close URL: the harness alone closes", ctr.Name)
		}
	}

	plain := Pod(run("run-abcdefgh"), c, testModel, "abcdefgh")
	if _, ok := plain.Annotations[EgressInstallAnnotation]; ok {
		t.Fatal("a run without checks has no install set")
	}
	if _, ok := ctrEnv(plain.Spec.Containers[0])["EGRESS_INSTALL_CLOSE_URL"]; ok {
		t.Fatal("a run without checks has nothing to close")
	}
}

// The browser sidecar shares the pod's IP, so it gets no variable of the close: it cannot be driven
// before the agent starts, which is after the close.
func TestBrowserSidecarGetsNoInstallWindowOfItsOwn(t *testing.T) {
	c := kubeConfig(t)
	c.Images.Browser = "ghcr.io/smana/maku-browser:v0.16.0"
	p := Pod(checksRun(func(r *v1alpha1.AgentRun) { r.Spec.Screenshots = &v1alpha1.AgentRunScreenshots{Enabled: true} }), c, testModel, "abcdefgh")
	found := false
	for _, ctr := range p.Spec.InitContainers {
		if ctr.Name != "browser" {
			continue
		}
		found = true
		for _, k := range []string{"EGRESS_INSTALL_CLOSE_URL", "POD_UID", "HTTPS_PROXY"} {
			if _, ok := ctrEnv(ctr)[k]; ok {
				t.Errorf("the browser gets no %s", k)
			}
		}
	}
	if !found {
		t.Fatal("no browser sidecar rendered")
	}
}

func TestStampInstallDeadline(t *testing.T) {
	c := kubeConfig(t)
	at := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	p := Pod(checksRun(), c, testModel, "abcdefgh")
	StampInstallDeadline(p, at, c)
	if got := p.Annotations[EgressInstallUntilAnnotation]; got != "2026-11-26T12:20:00Z" {
		t.Fatalf("deadline %q, want create + 20 minutes", got)
	}
	plain := Pod(run("run-abcdefgh"), c, testModel, "abcdefgh")
	StampInstallDeadline(plain, at, c)
	if _, ok := plain.Annotations[EgressInstallUntilAnnotation]; ok {
		t.Fatal("no install set, no deadline")
	}
}

func fqdnRules(u *unstructured.Unstructured) []string {
	var out []string
	rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
	for _, r := range rules {
		names, _ := r.(map[string]any)["toFQDNs"].([]any)
		for _, n := range names {
			out = append(out, n.(map[string]any)["matchName"].(string))
		}
	}
	return out
}

// cilium mode cannot close a set on the pod's signal: a check run's CNP holds its install set for the
// whole run, as before; a run without checks no longer reaches the release hosts.
func TestCiliumModeKeepsTheInstallSetOpen(t *testing.T) {
	c := testConfig(t)
	got := fqdnRules(NetworkPolicy(checksRun(), c, testModel, "abcdefgh"))
	for _, h := range releaseHosts {
		if !slices.Contains(got, h) {
			t.Errorf("a check run's CNP opens %s: %v", h, got)
		}
	}
	plain := fqdnRules(NetworkPolicy(run("run-abcdefgh"), c, testModel, "abcdefgh"))
	for _, h := range releaseHosts {
		if slices.Contains(plain, h) {
			t.Errorf("a run without checks reaches no %s: %v", h, plain)
		}
	}
	if e := ctrEnv(Pod(checksRun(), c, testModel, "abcdefgh").Spec.Containers[0]); e["EGRESS_INSTALL_CLOSE_URL"].Value != "" {
		t.Fatal("cilium mode has no close endpoint")
	}
}

func TestInstallPodGolden(t *testing.T) {
	c := kubeConfig(t)
	p := Pod(checksRun(), c, testModel, "abcdefgh")
	StampInstallDeadline(p, time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC), c)
	golden(t, "pod-install.golden.yaml", p)
}
