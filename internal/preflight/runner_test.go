// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/version"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The runner runs the pod's checks, collects each component's /preflight, prints the JSON lines
// and writes the summary to the termination file. An older component is component_too_old, an
// unreachable one undecided: neither passes.
func TestRun(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/preflight" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"component":"broker","version":"v1","checks":[{"check":"database","outcome":"pass"}]}`))
	}))
	defer broker.Close()
	old := httptest.NewServer(http.NotFoundHandler()) // an image from before /preflight
	defer old.Close()
	objs, cluster := healthy()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build()
	v := fixedVersion{v: version.Info{Major: "1", Minor: "31"}}
	term := filepath.Join(t.TempDir(), "termination-log")
	run := func(comps []ComponentRef, strict bool) (int, string) {
		var out bytes.Buffer
		code := Run(t.Context(), Deps{Client: c, Version: v, HTTP: http.DefaultClient}, //nolint:forbidigo // the test servers are plain http
			RunConfig{Cluster: cluster, Components: comps, Timeout: Duration{2 * time.Second}}, &out, term, strict)
		return code, out.String()
	}
	code, out := run([]ComponentRef{{Name: "broker", URL: broker.URL}}, false)
	if code != 0 || !strings.Contains(out, `"check":"database","component":"broker","outcome":"pass"`) {
		t.Fatalf("%d\n%s", code, out)
	}
	sum, err := os.ReadFile(term)
	if err != nil || !strings.HasPrefix(string(sum), "preflight: 0 fail") {
		t.Fatalf("termination message %q %v", sum, err)
	}
	code, out = run([]ComponentRef{{Name: "factory", URL: old.URL}}, false)
	if code != 1 || !strings.Contains(out, `"code":"component_too_old"`) || !strings.Contains(out, `"component":"factory"`) {
		t.Fatalf("an older component: %d\n%s", code, out)
	}
	code, out = run([]ComponentRef{{Name: "broker", URL: "http://127.0.0.1:1"}}, false)
	if code != 1 || !strings.Contains(out, `"code":"undecided"`) {
		t.Fatalf("an unreachable component: %d\n%s", code, out)
	}
	cluster.RuntimeClass, cluster.UnsafeAllowRunc = "", true
	if code, _ := run(nil, false); code != 0 {
		t.Fatalf("a warning passes without --strict: %d", code)
	}
	if code, _ := run(nil, true); code != 1 {
		t.Fatalf("a warning fails --strict: %d", code)
	}
}

// The config is the chart's, decoded strictly.
func TestLoadRunConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(p, []byte(`{"cluster":{"minKubernetes":"1.30","crds":[],"runtimeClass":"gvisor","unsafeAllowRunc":false,"secrets":[]},"components":[],"timeout":"30s"}`), 0o600)
	cfg, err := LoadRunConfig(p)
	if err != nil || cfg.Timeout.Duration != 30*time.Second || cfg.Cluster.RuntimeClass != "gvisor" {
		t.Fatalf("%+v %v", cfg, err)
	}
	_ = os.WriteFile(p, []byte(`{"cluster":{},"extra":1}`), 0o600)
	if _, err := LoadRunConfig(p); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}
