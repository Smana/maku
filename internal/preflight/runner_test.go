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
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"component":"broker","version":"v1","checks":[]}`))
	}))
	defer empty.Close()
	code, out = run([]ComponentRef{{Name: "broker", URL: empty.URL}}, false)
	if code != 1 || !strings.Contains(out, `"component":"broker","outcome":"fail","code":"undecided"`) {
		t.Fatalf("a component with no check passes silently: %d\n%s", code, out)
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

// A component that hangs leaves the checks already done on stdout: lines stream as they complete,
// and the run stops before the Job's own deadline.
func TestRunStreamsBeforeAHang(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer hang.Close()
	objs, cluster := healthy()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build()
	var out bytes.Buffer
	start := time.Now()
	code := Run(t.Context(), Deps{Client: c, Version: fixedVersion{v: version.Info{Major: "1", Minor: "31"}}, HTTP: &http.Client{}},
		RunConfig{Cluster: cluster, Components: []ComponentRef{{Name: "broker", URL: hang.URL}}, Timeout: Duration{2 * time.Second}},
		&out, "", false)
	if took := time.Since(start); took >= 2*time.Second {
		t.Fatalf("the run outlived the Job's deadline: %s", took)
	}
	if code != 1 || !strings.Contains(out.String(), `"check":"kubernetes"`) || !strings.Contains(out.String(), `"code":"undecided"`) {
		t.Fatalf("%d\n%s", code, out.String())
	}
}

const goodConfig = `{"cluster":{"minKubernetes":"1.30","crds":[{"name":"rooms.maku.ogenki.io","version":"v1alpha1"}],"runtimeClass":"gvisor",` +
	`"unsafeAllowRunc":false,"secrets":[{"seam":"brokerTLS","namespace":"maku","name":"room-broker-tls","keys":["tls.crt"]}]},"components":[],"timeout":"30s"}`

// The config is the chart's, decoded strictly, its required fields checked: an empty list would
// read as "0 pass".
func TestLoadRunConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(p, []byte(goodConfig), 0o600)
	cfg, err := LoadRunConfig(p)
	if err != nil || cfg.Timeout.Duration != 30*time.Second || cfg.Cluster.RuntimeClass != "gvisor" {
		t.Fatalf("%+v %v", cfg, err)
	}
	for name, c := range map[string]struct{ old, new, want string }{
		"an unknown key":          {`"components"`, `"extra":1,"components"`, "unknown field"},
		"no minKubernetes":        {`"minKubernetes":"1.30"`, `"minKubernetes":""`, "minKubernetes"},
		"a garbage minKubernetes": {`"minKubernetes":"1.30"`, `"minKubernetes":"latest"`, "minKubernetes"},
		"no CRD":                  {`"crds":[{"name":"rooms.maku.ogenki.io","version":"v1alpha1"}]`, `"crds":[]`, "crds"},
		"no Secret":               {`"secrets":[{"seam":"brokerTLS","namespace":"maku","name":"room-broker-tls","keys":["tls.crt"]}]`, `"secrets":[]`, "secrets"},
		"no timeout":              {`"timeout":"30s"`, `"timeout":"0s"`, "timeout"},
	} {
		t.Run(name, func(t *testing.T) {
			_ = os.WriteFile(p, []byte(strings.Replace(goodConfig, c.old, c.new, 1)), 0o600)
			if _, err := LoadRunConfig(p); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error naming %s", err, c.want)
			}
		})
	}
}
