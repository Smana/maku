// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/resources"
)

// The test's generated values: none is a real credential.
const (
	testCookie  = "dGVzdC1jb29raWUtc2VjcmV0LTMyLWJ5dGVzLWxvbmc="
	testWebhook = "dGVzdC13ZWJob29rLWtleS0zMi1ieXRlcy1sb25nLXg="
	testPEM     = "-----BEGIN RSA PRIVATE KEY-----\ntest-not-a-key\n-----END RSA PRIVATE KEY-----\n"
	testOAuth   = "test-oauth-client-secret"
)

// inputs is an answers file's inputs; the ghes file creates its Apps (a fake flow's results).
func inputs(t *testing.T, name string) Inputs {
	t.Helper()
	a := resolved(t, name)
	in := Inputs{Answers: a, Pins: Pins(a), CookieSecret: testCookie, WebhookKey: testWebhook}
	if a.Bool("apps.create") {
		in.Factory = AppInfo{ID: 3003, Slug: "maku-platform-factory", HTMLURL: "https://ghe.example.com/github-apps/maku-platform-factory",
			ClientID: "Iv1.factory", PEM: testPEM, ClientSecret: testOAuth}
		in.Agents = AppInfo{ID: 3004, Slug: "maku-platform-agents", HTMLURL: "https://ghe.example.com/github-apps/maku-platform-agents",
			ClientID: "Iv1.agents", PEM: testPEM}
	}
	return in
}

var goldens = []string{"generic", "zitadel-eso", "ghes"}

// goldenName is a tree path as its golden file: secrets/.gitignore is stored as dot-gitignore, or git
// would ignore the golden Secrets beside it.
func goldenName(p string) string { return strings.ReplaceAll(p, ".gitignore", "dot-gitignore") }

// IN6: three answers files give their trees, byte for byte. UPDATE_GOLDEN=1 rewrites them.
func TestRenderGoldens(t *testing.T) {
	for _, name := range goldens {
		tree, err := Render(inputs(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		dir := filepath.Join("testdata", "golden", name)
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			for _, p := range tree.Paths() {
				full := filepath.Join(dir, filepath.FromSlash(goldenName(p)))
				if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, tree[p], 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		var want []string
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(dir, p)
				want = append(want, strings.ReplaceAll(filepath.ToSlash(rel), "dot-gitignore", ".gitignore"))
			}
			return nil
		})
		sort.Strings(want)
		if got := tree.Paths(); strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: the tree is %v, the golden %v", name, got, want)
			continue
		}
		for _, p := range tree.Paths() {
			g, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(goldenName(p))))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(g, tree[p]) {
				t.Errorf("%s: %s differs from its golden (UPDATE_GOLDEN=1 go test ./internal/install, then review)", name, p)
			}
		}
	}
}

// The written factory starts outside the demo: its file and its objects assemble into a config the
// factory serves (a meter, a repository; step 6 PR G's finding).
func TestFactoryStarts(t *testing.T) {
	for _, name := range goldens {
		in := inputs(t, name)
		file, manifests, err := FactoryObjects(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// The chart writes the folded merger's files back (apps.merger.mode factory).
		gh := file["github"].(map[string]any)
		gh["mergerAppIDFile"], gh["mergerKeyFile"] = gh["appIDFile"], gh["privateKeyFile"]
		raw, _ := yamlDoc("", file)
		objs, err := resources.Decode(manifests)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		res, err := resources.Assemble(raw, objs)
		if err != nil {
			t.Fatalf("%s: the written factory does not assemble: %v", name, err)
		}
		if res.Config.Meter.URL == "" || len(res.Config.Repositories) != len(in.Answers.List("repositories")) {
			t.Errorf("%s: meter %q, %d repositories", name, res.Config.Meter.URL, len(res.Config.Repositories))
		}
	}
}

// Every written values.yaml renders under profile production, and kubeconform accepts the render.
// Skipped without helm; task chart:check always runs it.
func TestValuesRender(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	chart, _ := filepath.Abs(filepath.Join("..", "..", "charts", "agent-factory"))
	if _, err := os.Stat(filepath.Join(chart, "charts")); err != nil {
		t.Skip("the umbrella's dependencies are not built (task chart:check builds them)")
	}
	kubeconform, kerr := exec.LookPath("kubeconform")
	for _, name := range goldens {
		in := inputs(t, name)
		tree, err := Render(in)
		if err != nil {
			t.Fatal(err)
		}
		values := filepath.Join(t.TempDir(), "values.yaml")
		if err := os.WriteFile(values, tree["values.yaml"], 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.CommandContext(t.Context(), helm, "template", in.Answers.String("release"), chart, "-n", in.ns(), "-f", values).CombinedOutput()
		if err != nil {
			t.Errorf("%s: helm template: %v\n%s", name, err, out)
			continue
		}
		if kerr != nil {
			continue
		}
		cmd := exec.CommandContext(t.Context(), kubeconform, "-strict", "-ignore-missing-schemas", "-summary", "-")
		cmd.Stdin = bytes.NewReader(out)
		if kout, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: kubeconform: %v\n%s", name, err, kout)
		}
	}
}

// The README's commands run in order: the CRDs, agentgateway's CRDs, the Secrets, the umbrella, the
// factory objects, the ruleset, the preflight.
func TestReadmeOrder(t *testing.T) {
	tree, err := Render(inputs(t, "generic"))
	if err != nil {
		t.Fatal(err)
	}
	r := string(tree["README.md"])
	last := -1
	for _, step := range []string{"helm install agent-factory-crds", "config/agentgateway", "kubectl apply -n maku -f secrets/", "helm install agent-factory oci://", "kubectl apply -n maku -f factory/", "apply-ruleset.sh", "helm test agent-factory"} {
		i := strings.Index(r, step)
		if i < 0 || i < last {
			t.Fatalf("the README does not run %q after the step before it:\n%s", step, r)
		}
		last = i
	}
}

// R3: no file outside secrets/ holds a PEM, a client secret or a generated key.
func TestNoKeyMaterialOutsideSecrets(t *testing.T) {
	for _, name := range goldens {
		tree, err := Render(inputs(t, name))
		if err != nil {
			t.Fatal(err)
		}
		inSecrets := false
		for _, p := range tree.Paths() {
			hold := holdsKeyMaterial(tree[p], testCookie, testWebhook, testOAuth)
			if strings.HasPrefix(p, "secrets/") {
				inSecrets = inSecrets || hold
				continue
			}
			if hold {
				t.Errorf("%s: %s holds key material", name, p)
			}
		}
		if !inSecrets {
			t.Errorf("%s: secrets/ holds none of the generated values", name)
		}
	}
}

// R3, IN6: secrets/ is 0700 with 0600 files; a non-empty --out needs --force; secrets/ is never
// overwritten.
func TestWritePermissions(t *testing.T) {
	tree, err := Render(inputs(t, "ghes"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "out")
	if err := Write(dir, tree, false); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filepath.Join(dir, "secrets"))
	if st.Mode().Perm() != 0o700 {
		t.Errorf("secrets/ is %v, want 0700", st.Mode().Perm())
	}
	_ = filepath.WalkDir(filepath.Join(dir, "secrets"), func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			if fi, _ := d.Info(); fi.Mode().Perm() != 0o600 {
				t.Errorf("%s is %v, want 0600", p, fi.Mode().Perm())
			}
		}
		return nil
	})
	if g, _ := os.ReadFile(filepath.Join(dir, "secrets", ".gitignore")); string(g) != "*\n" {
		t.Errorf("secrets/.gitignore is %q", g)
	}
	if err := Write(dir, tree, false); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("a non-empty --out without --force: %v", err)
	}
	if err := Write(dir, tree, true); err == nil || !strings.Contains(err.Error(), "never overwrites secrets/") {
		t.Errorf("--force over an existing secrets/: %v", err)
	}
}

// The values file names its images by digest, every one of them.
func TestValuesPinImages(t *testing.T) {
	in := inputs(t, "generic")
	delete(in.Pins, "maku-factory")
	if _, err := Values(in); err == nil || !regexp.MustCompile(`no digest for maku-factory`).MatchString(err.Error()) {
		t.Errorf("a missing digest: %v", err)
	}
}

// Review of #565: the factory's run API is TLS when a ClusterIssuer is named (the broker forwards each
// human's bearer there); plaintext only on an explicit factory.insecure answer; neither is refused.
func TestFactoryAPITLS(t *testing.T) {
	in := inputs(t, "generic") // tls.issuer answered
	v, err := Values(in)
	if err != nil {
		t.Fatal(err)
	}
	b := digMap(v, "maku.broker")
	if b["factoryURL"] != "https://agent-factory.maku.svc:8082" || b["factoryInsecure"] != nil {
		t.Errorf("with tls.issuer: factoryURL %v, factoryInsecure %v", b["factoryURL"], b["factoryInsecure"])
	}
	if digMap(v, "maku.broker.factoryCA")["secretName"] != secretFactoryCA {
		t.Errorf("with tls.issuer: no broker.factoryCA")
	}
	tls := digMap(v, "maku-factory.api.tls")
	if tls["secretName"] == nil || digMap(tls, "issuerRef")["name"] != "internal-ca" {
		t.Errorf("with tls.issuer: maku-factory.api.tls is %v", tls)
	}
	a := readAnswers(t, "generic")
	delete(a, "tls.issuer")
	if _, _, err := Resolve(a); err == nil || !strings.Contains(err.Error(), "factory.insecure") {
		t.Errorf("no TLS and no explicit insecure: %v", err)
	}
	a["factory.insecure"] = true
	r, _, err := Resolve(a)
	if err != nil {
		t.Fatal(err)
	}
	in.Answers = r
	if v, err = Values(in); err != nil {
		t.Fatal(err)
	}
	if b := digMap(v, "maku.broker"); b["factoryInsecure"] != true || b["factoryURL"] != "http://agent-factory.maku.svc.cluster.local:8082" {
		t.Errorf("factory.insecure: %v %v", b["factoryURL"], b["factoryInsecure"])
	}
}

// Review of #565: with cert-manager, the README still names the CA Secrets cert-manager does not write.
func TestReadmeNamesTheCASecrets(t *testing.T) {
	tree, err := Render(inputs(t, "generic"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"room-broker-ca", "openbao-ca", secretFactoryCA} {
		if !strings.Contains(string(tree["README.md"]), s) {
			t.Errorf("the README does not name %s", s)
		}
	}
}
