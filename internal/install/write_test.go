// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
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
		// What install writes loads without a warning: no dead setting, nothing a later release refuses.
		if w := slices.Concat(res.Warnings, res.Config.Warnings); len(w) != 0 {
			t.Errorf("%s: the written factory warns: %q", name, w)
		}
	}
}

// Every written values.yaml renders under the production defaults, and kubeconform accepts the render.
// Skipped without helm only.
func TestValuesRender(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	// The umbrella's subcharts are file:// dependencies a fresh checkout has not built (CI's test
	// shards): build them in a copy of charts/, never in the working tree, so this always runs.
	charts := t.TempDir()
	if err := os.CopyFS(charts, os.DirFS(filepath.Join("..", "..", "charts"))); err != nil {
		t.Fatal(err)
	}
	chart := filepath.Join(charts, "agent-factory")
	if out, err := exec.CommandContext(t.Context(), helm, "dependency", "build", chart).CombinedOutput(); err != nil {
		t.Fatalf("helm dependency build: %v\n%s", err, out)
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

// An in-cluster issuer (kind, k3s, kubeadm): its JWKS is the API server's, read with no credential, so
// the gateway reads it as source apiserver with the discovery binding, and the README says what the
// API server must admit (k3s: anonymous-auth). A cloud issuer keeps source url and no such line.
func TestInClusterIssuer(t *testing.T) {
	for name, want := range map[string]bool{"generic": true, "ghes": false, "zitadel-eso": false} {
		tree, err := Render(inputs(t, name))
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			Gateway struct {
				Identity struct {
					JWKS struct {
						Source    string `json:"source"`
						Anonymous bool   `json:"allowAnonymousDiscovery"`
					} `json:"jwks"`
				} `json:"identity"`
			} `json:"maku-gateway"`
		}
		if err := yaml.Unmarshal(tree["values.yaml"], &v); err != nil {
			t.Fatal(err)
		}
		j := v.Gateway.Identity.JWKS
		if want && (j.Source != "apiserver" || !j.Anonymous) {
			t.Errorf("%s: an in-cluster issuer's JWKS is source %q, allowAnonymousDiscovery %v", name, j.Source, j.Anonymous)
		}
		if !want && (j.Source == "apiserver" || j.Anonymous) {
			t.Errorf("%s: a cloud issuer's JWKS is source %q, allowAnonymousDiscovery %v", name, j.Source, j.Anonymous)
		}
		if got := strings.Contains(string(tree["README.md"]), "--kube-apiserver-arg=anonymous-auth=true"); got != want {
			t.Errorf("%s: the README names k3s's anonymous-auth: %v, want %v", name, got, want)
		}
	}
}

// renderedFactory is the Factory install writes for an answers file, decoded.
func renderedFactory(t *testing.T, name string) v1alpha1.Factory {
	t.Helper()
	tree, err := Render(inputs(t, name))
	if err != nil {
		t.Fatal(err)
	}
	var f v1alpha1.Factory
	if err := yaml.UnmarshalStrict(tree["factory/factory.yaml"], &f); err != nil {
		t.Fatalf("factory/factory.yaml: %v", err)
	}
	return f
}

// WR11: install writes the three workflows in the readable list form and none of the legacy
// bundles: no templates block, no loops block, no defaults.template or defaults.tier.
func TestInstalledFactoryCarriesTheWorkflows(t *testing.T) {
	for _, name := range goldens {
		f := renderedFactory(t, name)
		in := inputs(t, name)
		profiles, _, _ := config.FillShippedWorkflows(nil, nil, nil, len(in.Answers.List("models.prices")) > 0)
		for n, want := range profiles {
			if got, ok := f.Spec.Workflows[n]; !ok || !reflect.DeepEqual(got, want) {
				t.Errorf("%s: workflow %s = %+v, want %+v", name, n, got, want)
			}
		}
		if len(f.Spec.Workflows) != len(profiles) {
			t.Errorf("%s: workflows %v", name, slices.Collect(maps.Keys(f.Spec.Workflows)))
		}
		if len(f.Spec.Templates) != 0 {
			t.Errorf("%s: templates should be omitted in the list form, got %v", name, slices.Collect(maps.Keys(f.Spec.Templates)))
		}
		if len(f.Spec.Loops) != 0 {
			t.Errorf("%s: loops should be omitted in the list form, got %v", name, slices.Collect(maps.Keys(f.Spec.Loops)))
		}
	}
}

// TestTheInstallerWritesTheListForm verifies maku install --print emits quick, standard and
// careful using budget + stages and omits legacy templates and loops blocks.
func TestTheInstallerWritesTheListForm(t *testing.T) {
	for _, name := range goldens {
		f := renderedFactory(t, name)
		for _, wf := range []string{config.WorkflowQuick, config.WorkflowStandard, config.WorkflowCareful} {
			p, ok := f.Spec.Workflows[wf]
			if !ok {
				t.Fatalf("%s: missing workflow %s", name, wf)
			}
			if len(p.Stages) == 0 || p.Budget == "" {
				t.Errorf("%s: workflow %s not in list form: %+v", name, wf, p)
			}
			if p.Template != "" || p.Tier != "" || p.Loop != "" {
				t.Errorf("%s: workflow %s still carries legacy keys: %+v", name, wf, p)
			}
		}
		if len(f.Spec.Templates) != 0 || len(f.Spec.Loops) != 0 {
			t.Errorf("%s: expected no templates or loops, got templates=%v loops=%v", name, f.Spec.Templates, f.Spec.Loops)
		}
	}
}

// v5 DB5: database.mode supports bundled (Tier-1 Pilot), cnpg (CloudNativePG manifest) and
// external (default); every mode's values.yaml renders cleanly through `helm template`.
func TestDatabaseModes(t *testing.T) {
	helm, helmErr := exec.LookPath("helm")
	var chart string
	if helmErr == nil {
		charts := t.TempDir()
		if err := os.CopyFS(charts, os.DirFS(filepath.Join("..", "..", "charts"))); err != nil {
			t.Fatal(err)
		}
		chart = filepath.Join(charts, "agent-factory")
		if out, err := exec.CommandContext(t.Context(), helm, "dependency", "build", chart).CombinedOutput(); err != nil {
			t.Fatalf("helm dependency build: %v\n%s", err, out)
		}
	}

	renderHelm := func(t *testing.T, name string, tree Tree, rel, ns string) {
		t.Helper()
		if helmErr != nil {
			return
		}
		values := filepath.Join(t.TempDir(), "values.yaml")
		if err := os.WriteFile(values, tree["values.yaml"], 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.CommandContext(t.Context(), helm, "template", rel, chart, "-n", ns, "-f", values).CombinedOutput(); err != nil {
			t.Errorf("%s: helm template failed: %v\n%s", name, err, out)
		}
	}

	t.Run("bundled-cert-manager", func(t *testing.T) {
		a := readAnswers(t, "generic")
		delete(a, "database.caSecret")
		a["database.mode"] = "bundled"
		r, _, err := Resolve(a)
		if err != nil {
			t.Fatalf("Resolve bundled: %v", err)
		}
		tree, err := Render(Inputs{Answers: r, Pins: Pins(r), CookieSecret: testCookie, WebhookKey: testWebhook})
		if err != nil {
			t.Fatalf("Render bundled: %v", err)
		}
		if _, ok := tree["secrets/templates/database.yaml"]; ok {
			t.Errorf("bundled mode emitted secrets/templates/database.yaml")
		}
		if _, ok := tree["database/cnpg-cluster.yaml"]; ok {
			t.Errorf("bundled mode emitted database/cnpg-cluster.yaml")
		}
		if !strings.Contains(string(tree["values.yaml"]), "acknowledgeSingleReplica: true") || !strings.Contains(string(tree["values.yaml"]), "mode: cert-manager") {
			t.Errorf("bundled values.yaml missing Tier-1 Pilot settings:\n%s", tree["values.yaml"])
		}
		if !strings.Contains(string(tree["README.md"]), "rooms-db-ca") {
			t.Errorf("bundled README with tls.issuer does not name rooms-db-ca")
		}
		renderHelm(t, "bundled-cert-manager", tree, r.String("release"), r.String("namespace"))
	})

	t.Run("bundled-helm-tls-and-eso", func(t *testing.T) {
		a := readAnswers(t, "generic")
		delete(a, "database.caSecret")
		delete(a, "tls.issuer")
		a["factory.insecure"] = true
		a["database.mode"] = "bundled"
		a["secrets.mode"] = "eso"
		a["secrets.store"] = "cluster-secrets"
		r, _, err := Resolve(a)
		if err != nil {
			t.Fatalf("Resolve bundled helm+eso: %v", err)
		}
		tree, err := Render(Inputs{Answers: r, Pins: Pins(r), CookieSecret: testCookie, WebhookKey: testWebhook})
		if err != nil {
			t.Fatalf("Render bundled helm+eso: %v", err)
		}
		vals := string(tree["values.yaml"])
		if !strings.Contains(vals, "mode: helm") {
			t.Errorf("expected postgres.tls.mode helm:\n%s", vals)
		}
		for _, forbidden := range []string{"databaseBroker:", "databaseOwner:", "databaseRetention:", "databaseCA:"} {
			if strings.Contains(vals, forbidden) {
				t.Errorf("bundled helm+eso emitted conflicting ExternalSecret %s:\n%s", forbidden, vals)
			}
		}
		renderHelm(t, "bundled-helm-tls-and-eso", tree, r.String("release"), r.String("namespace"))
	})

	t.Run("bundled-refuses-external-db-keys", func(t *testing.T) {
		a := readAnswers(t, "generic")
		a["database.mode"] = "bundled"
		if _, _, err := Resolve(a); err == nil || !strings.Contains(err.Error(), "database.caSecret: answered, but the answers before it leave it out") {
			t.Fatalf("bundled with database.caSecret = %v, want refused", err)
		}
	})

	t.Run("cnpg", func(t *testing.T) {
		a := readAnswers(t, "generic")
		delete(a, "database.caSecret")
		a["database.mode"] = "cnpg"
		r, _, err := Resolve(a)
		if err != nil {
			t.Fatalf("Resolve cnpg: %v", err)
		}
		if r.String("database.caSecret") != cnpgCASecret {
			t.Errorf("cnpg default caSecret = %q, want %q", r.String("database.caSecret"), cnpgCASecret)
		}
		if r.String("database.sslmode") != "verify-full" {
			t.Errorf("cnpg default sslmode = %q, want verify-full", r.String("database.sslmode"))
		}
		tree, err := Render(Inputs{Answers: r, Pins: Pins(r), CookieSecret: testCookie, WebhookKey: testWebhook})
		if err != nil {
			t.Fatalf("Render cnpg: %v", err)
		}
		cnpgRaw, ok := tree["database/cnpg-cluster.yaml"]
		if !ok {
			t.Fatalf("cnpg mode did not emit database/cnpg-cluster.yaml")
		}
		docs := strings.Split(string(cnpgRaw), "---\n")
		if len(docs) != 2 {
			t.Fatalf("expected 2 YAML docs in cnpg-cluster.yaml, got %d", len(docs))
		}
		var cluster, db map[string]any
		if err := yaml.Unmarshal([]byte(docs[0]), &cluster); err != nil {
			t.Fatalf("parse CNPG Cluster: %v", err)
		}
		if err := yaml.Unmarshal([]byte(docs[1]), &db); err != nil {
			t.Fatalf("parse CNPG Database: %v", err)
		}
		if cluster["kind"] != "Cluster" || db["kind"] != "Database" {
			t.Fatalf("unexpected kinds: %v, %v", cluster["kind"], db["kind"])
		}
		if !strings.Contains(string(cnpgRaw), "rooms_owner") || !strings.Contains(string(cnpgRaw), "rooms_broker") || !strings.Contains(string(cnpgRaw), "rooms_retention") {
			t.Errorf("cnpg-cluster.yaml missing Maku roles:\n%s", cnpgRaw)
		}
		if !strings.Contains(string(tree["values.yaml"]), "cnpg.io/cluster: rooms-cnpg") {
			t.Errorf("cnpg values.yaml missing podLabels:\n%s", tree["values.yaml"])
		}
		if strings.Contains(string(tree["secrets/templates/database.yaml"]), "name: rooms-cnpg-ca") {
			t.Errorf("cnpg secrets/templates/database.yaml should not duplicate CNPG-managed CA Secret")
		}
		readmeStr := string(tree["README.md"])
		iCNPG := strings.Index(readmeStr, "database/cnpg-cluster.yaml")
		iSecrets := strings.Index(readmeStr, "kubectl apply -n maku -f secrets/")
		if iCNPG < 0 || iSecrets < 0 || iCNPG > iSecrets {
			t.Errorf("cnpg README does not apply database/cnpg-cluster.yaml before secrets/:\n%s", readmeStr)
		}
		renderHelm(t, "cnpg", tree, r.String("release"), r.String("namespace"))
	})
}
