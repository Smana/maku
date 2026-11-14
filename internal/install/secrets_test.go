// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// root is the repository's root, from this package's directory.
const root = "../.."

// The docs' table is the data: docs/install-secrets.md is generated from Seams, never edited apart.
func TestDocs(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(root, "docs/install-secrets.md"))
	if err != nil {
		t.Fatal(err)
	}
	start, end := bytes.Index(doc, []byte(TableStart)), bytes.Index(doc, []byte(TableEnd))
	if start < 0 || end < start {
		t.Fatalf("docs/install-secrets.md lacks the %s … %s markers", TableStart, TableEnd)
	}
	got := string(doc[start+len(TableStart) : end])
	if want := "\n" + Table() + "\n"; got != want {
		t.Fatalf("docs/install-secrets.md's table differs from Seams: run task chart:seams\n--- got\n%s\n--- want\n%s", got, want)
	}
}

// The chart's _seams.tpl is the data too: task chart:seams writes it.
func TestSeamsTemplate(t *testing.T) {
	for path, gen := range map[string]func() (string, error){
		"charts/agent-factory/templates/_seams.tpl": Template,
		"charts/maku/templates/_seams.tpl":          MakuTemplate,
	} {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		want, err := gen()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s differs from Seams: run task chart:seams", path)
		}
	}
}

// Every seam's value path exists in its chart's values.yaml, so the umbrella resolves a name the
// chart really reads.
func TestValuePathsExist(t *testing.T) {
	values := map[string]map[string]any{}
	for _, s := range Seams {
		if s.Value == "" { // a fixed name, the umbrella's own
			continue
		}
		v, ok := values[s.Chart]
		if !ok {
			raw, err := os.ReadFile(filepath.Join(root, "charts", s.Chart, "values.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			values[s.Chart] = v
		}
		var cur any = v
		for _, step := range strings.Split(s.Value, ".") {
			m, ok := cur.(map[string]any)
			if !ok {
				t.Fatalf("%s: %s is not in charts/%s/values.yaml", s.ID, s.Value, s.Chart)
			}
			if cur, ok = m[step]; !ok {
				t.Fatalf("%s: %s is not in charts/%s/values.yaml", s.ID, s.Value, s.Chart)
			}
		}
	}
}

// The registry's own rules: ids unique, every seam with a key and a reader, no two seams sharing a
// default name in one namespace, a file seam with its mount path, and a closed vocabulary.
func TestSeamsAreWellFormed(t *testing.T) {
	ids, names := map[string]bool{}, map[string]string{}
	for _, s := range Seams {
		switch {
		case s.ID == "" || ids[s.ID]:
			t.Errorf("seam id %q is empty or used twice", s.ID)
		case len(s.Keys) == 0:
			t.Errorf("%s: no key", s.ID)
		case !slices.Contains([]Namespace{Release, Runs}, s.Namespace):
			t.Errorf("%s: namespace %q", s.ID, s.Namespace)
		case !slices.Contains([]ReadAs{Env, File, API}, s.ReadAs):
			t.Errorf("%s: readAs %q", s.ID, s.ReadAs)
		case s.ReadAs == File && s.MountPath == "":
			t.Errorf("%s: a file seam needs its mount path", s.ID)
		case !slices.Contains([]Generated{"", GeneratedPostgres, GeneratedUmbrella, GeneratedDemoModel, GeneratedDemoApp, GeneratedFactory, GeneratedCertManager}, s.Generated):
			t.Errorf("%s: generated %q", s.ID, s.Generated)
		}
		ids[s.ID] = true
		if s.Default == "" {
			continue
		}
		key := string(s.Namespace) + "/" + s.Default
		if other, ok := names[key]; ok {
			t.Errorf("%s and %s share the default name %s", s.ID, other, key)
		}
		names[key] = s.ID
	}
}

// Every Secret a render names, by secretKeyRef or by a secret volume, is a registered seam's: the
// demo, and a production-shaped install with every optional Secret on.
func TestRenderedRefs(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not on PATH")
	}
	// The umbrella's subcharts are file:// dependencies a fresh checkout has not built (CI's test
	// shards): build them in a copy of charts/, never in the working tree.
	charts := t.TempDir()
	if err := os.CopyFS(charts, os.DirFS(filepath.Join(root, "charts"))); err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(t.Context(), "helm", "dependency", "build", filepath.Join(charts, "agent-factory"))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("helm dependency build: %v\n%s", err, out)
	}
	prod, err := filepath.Abs(filepath.Join(root, "internal/install/testdata/production-values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const d = "v0.0.0@sha256:0000"
	common := []string{"template", "af", filepath.Join(charts, "agent-factory"), "-n", "maku",
		"-f", filepath.Join(charts, "agent-factory/values-demo.yaml"),
		"--set", "maku.broker.image.tag=" + d, "--set", "maku.runController.image.tag=" + d,
		"--set", "maku.runController.images.harness.tag=" + d, "--set", "maku.runController.images.bridge.tag=" + d,
		"--set", "maku-factory.image.tag=" + d, "--set", "demo.model.apiKey=k", "--set", "demo.githubApp.id=1", "--set", "demo.githubApp.privateKey=pem",
		// the demo's model gateway (step 7 R4)
		"--set", "maku-gateway.identity.issuer=https://kubernetes.default.svc.cluster.local", "--set", "maku-gateway.providers.demo.host=m.example.test",
		"--set", "maku-gateway.aliases.public.agent-default.model=m", "--set", "maku-gateway.aliases.public.tier-light.model=m"}
	for name, extra := range map[string][]string{
		"demo":       nil,
		"production": {"-f", prod},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := exec.CommandContext(t.Context(), "helm", append(common, extra...)...).Output()
			if err != nil {
				var stderr string
				if ee, ok := err.(*exec.ExitError); ok { //nolint:errorlint // the stderr of this one command
					stderr = string(ee.Stderr)
				}
				t.Fatalf("helm template: %v\n%s", err, stderr)
			}
			vals := map[string]any{}
			files := []string{filepath.Join(charts, "agent-factory/values-demo.yaml")}
			for i := 0; i+1 < len(extra); i += 2 {
				files = append(files, extra[i+1])
			}
			for _, f := range files {
				raw, err := os.ReadFile(f) //nolint:gosec // the test's own values files
				if err != nil {
					t.Fatal(err)
				}
				var v map[string]any
				if err := yaml.Unmarshal(raw, &v); err != nil {
					t.Fatal(err)
				}
				merge(vals, v)
			}
			names := Names(vals)
			refs := secretRefs(t, out)
			if len(refs) == 0 {
				t.Fatal("the render names no Secret: the walk is broken")
			}
			for _, ref := range refs {
				if _, ok := names[ref]; !ok {
					t.Errorf("the render names Secret %q, which no seam registers", ref)
				}
			}
		})
	}
}

// secretRefs walks every rendered document for secretKeyRef names and secret volumes' secretName.
func secretRefs(t *testing.T, out []byte) []string {
	t.Helper()
	var refs []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if r, ok := x["secretKeyRef"].(map[string]any); ok {
				if n, ok := r["name"].(string); ok {
					refs = append(refs, n)
				}
			}
			if s, ok := x["secret"].(map[string]any); ok {
				if n, ok := s["secretName"].(string); ok {
					refs = append(refs, n)
				}
			}
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	for _, doc := range bytes.Split(out, []byte("\n---")) {
		var v any
		if err := yaml.Unmarshal(doc, &v); err != nil {
			t.Fatalf("a rendered document: %v", err)
		}
		walk(v)
	}
	slices.Sort(refs)
	return slices.Compact(refs)
}

// Lookup maps a file the components read to its seam and key, by the chart's mount path.
func TestLookup(t *testing.T) {
	s, k, ok := Lookup("/etc/room-broker/oidc/web-client-id")
	if !ok || s.ID != "brokerOIDC" || k != "web-client-id" {
		t.Fatalf("%+v %q %v", s, k, ok)
	}
	if _, _, ok := Lookup("/tmp/elsewhere/client-id"); ok {
		t.Fatal("a path under no mount resolved")
	}
	if _, _, ok := Lookup("/etc/room-broker/oidc"); ok {
		t.Fatal("the mount itself is no key")
	}
}

// CheckFiles names the seam, the Secret and the key of a missing or empty file, and passes a file
// outside every mount, which another check owns.
func TestCheckFiles(t *testing.T) {
	files := map[string]string{"/etc/room-broker/oidc/web-client-id": "rooms-web\n", "/etc/room-broker/oidc/cli-client-id": " \n"}
	read := func(p string) ([]byte, error) {
		if v, ok := files[p]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}
	if err := CheckFiles(read, "/etc/room-broker/oidc/web-client-id", "/tmp/not-a-seam", "/etc/agent-factory-intake/token"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/etc/room-broker/oidc/cli-client-id":   "brokerOIDC: Secret broker.oidc.existingSecret key cli-client-id is missing or empty (docs/install-secrets.md)",
		"/etc/room-broker/tls/tls.key":          "brokerTLS: Secret room-broker-tls key tls.key is missing or empty (docs/install-secrets.md)",
		"/etc/agent-factory-github/private_key": "factoryApp: Secret agent-factory-github key private_key is missing or empty (docs/install-secrets.md)",
	} {
		if err := CheckFiles(read, path); err == nil || err.Error() != want {
			t.Errorf("%s: got %v, want %q", path, err, want)
		}
	}
}

// merge lays src over dst, map by map, as helm's -f does.
func merge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				merge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}
