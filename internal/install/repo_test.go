// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/fence"
	"github.com/Smana/maku/internal/factory/resources"
)

func publicInputs() RepoInputs {
	return RepoInputs{Repository: "acme/my-service", Factory: "agent-factory", Namespace: "maku",
		Maintainers: []string{"alice"}, RequiredChecks: []string{"ci"}, LeakScanCheck: "ci", FactoryAppID: "123",
		FactoryLogin:      "acme-factory[bot]",
		FactoryInstallURL: "https://github.com/apps/acme-factory/installations/new",
		AgentsInstallURL:  "https://github.com/apps/acme-agents/installations/new"}
}

// repoGolden compares tree with dir, file by file; UPDATE_GOLDEN=1 rewrites dir.
func repoGolden(t *testing.T, tree Tree, dir string) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		for _, p := range tree.Paths() {
			full := filepath.Join(dir, filepath.FromSlash(p))
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
			want = append(want, filepath.ToSlash(rel))
		}
		return nil
	})
	if got := tree.Paths(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the tree is %v, the golden %v", got, want)
	}
	for _, p := range tree.Paths() {
		g, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(g, tree[p]) {
			t.Errorf("%s differs from its golden (UPDATE_GOLDEN=1 go test ./internal/install, then review):\n%s", p, tree[p])
		}
	}
}

// WF11: the Repository binds nothing (the catalog is evaluated in shadow without a binding, merge
// policies MR2), and the fence is one line. By default the Repository leaves the merge settings every
// repository shares to the Factory's spec.mergeDefaults, which maku install --print writes (Q15), and
// is the spec's target: under 15 lines, the per-repository ones only (TestRepoTreeIsAccepted).
func TestRepoTreeForAPublicRepository(t *testing.T) {
	tree, err := RepoTree(publicInputs())
	if err != nil {
		t.Fatal(err)
	}
	repoGolden(t, tree, filepath.Join("testdata", "golden", "repo-add", "public"))
	repo := tree["factory/repository-my-service.yaml"]
	want := `apiVersion: maku.ogenki.io/v1alpha1
kind: Repository
metadata: {name: my-service, namespace: maku}
spec:
  factoryRef: agent-factory
  repository: acme/my-service
  maintainers: [alice]
  triggerLabel: factory/ready
  merge:
    requiredChecks: [ci]
    verifyChecks: [ci]
    leakScanCheck: ci
`
	if string(repo) != want {
		t.Fatalf("the Repository:\n%s", repo)
	}
	if n := bytes.Count(repo, []byte("\n")); n >= 15 {
		t.Fatalf("the Repository is %d lines, the target is under 15", n)
	}
	if bytes.Contains(repo, []byte("mergePolicies")) {
		t.Fatal("a binding was written")
	}
	if got := string(tree["repo/.github/agent-factory.yaml"]); got != "factory: agent-factory\n" {
		t.Fatalf("the fence is %q", got)
	}
	if name, err := fence.Parse(tree["repo/"+fence.Path]); err != nil || name != "agent-factory" {
		t.Fatalf("the factory does not read the fence it is given: %q, %v", name, err)
	}
	if !bytes.Contains(tree["github/apply-ruleset.sh"], []byte("'repos/acme/my-service/rulesets'")) {
		t.Fatalf("the script does not target the repository:\n%s", tree["github/apply-ruleset.sh"])
	}
}

// With --merge-defaults=false, for a Factory with no spec.mergeDefaults, the Repository carries
// every merge setting the factory requires and no default fills.
func TestRepoTreeWithoutMergeDefaultsCarriesTheSharedSettings(t *testing.T) {
	in := publicInputs()
	in.NoMergeDefaults = true
	tree, err := RepoTree(in)
	if err != nil {
		t.Fatal(err)
	}
	repo := tree["factory/repository-my-service.yaml"]
	want := `apiVersion: maku.ogenki.io/v1alpha1
kind: Repository
metadata: {name: my-service, namespace: maku}
spec:
  factoryRef: agent-factory
  repository: acme/my-service
  maintainers: [alice]
  triggerLabel: factory/ready
  merge:
    requiredChecks: [ci]
    verifyChecks: [ci]
    leakScanCheck: ci
    mergerLogin: acme-factory[bot]
    verifyFor: 30m
    revertWindow: 168h
    breaker: {window: 10, maxReverts: 1}
`
	if string(repo) != want {
		t.Fatalf("the Repository:\n%s", repo)
	}
}

func TestRepoTreePrivateNeedsTheAcknowledgement(t *testing.T) {
	in := publicInputs()
	in.Repository, in.Private = "acme/secret", true
	if _, err := RepoTree(in); err == nil || !strings.Contains(err.Error(), "--acknowledge-private-code") {
		t.Fatalf("got %v", err)
	}
	in.Acknowledged = true
	tree, err := RepoTree(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(tree["factory/repository-secret.yaml"], []byte("  privateCode: {acknowledged: true}\n")) {
		t.Fatalf("the acknowledgement is not written:\n%s", tree["factory/repository-secret.yaml"])
	}
}

// Review Focus 5: a Repository never names no required check, nor a value the CRD would refuse.
func TestRepoTreeRefusesWhatTheFactoryWould(t *testing.T) {
	for name, edit := range map[string]func(*RepoInputs){
		"no required check":      func(in *RepoInputs) { in.RequiredChecks = nil },
		"a repository, no owner": func(in *RepoInputs) { in.Repository = "my-service" },
		"no maintainer":          func(in *RepoInputs) { in.Maintainers = nil },
		"a fence name refused":   func(in *RepoInputs) { in.Factory = "Agent Factory" },
		"no factory App id":      func(in *RepoInputs) { in.FactoryAppID = "" },
		"no factory login":       func(in *RepoInputs) { in.FactoryLogin = "acme-factory" },
		"the leak scan not required": func(in *RepoInputs) {
			in.LeakScanCheck = "gitleaks"
		},
	} {
		in := publicInputs()
		edit(&in)
		if _, err := RepoTree(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A value that needs quoting in YAML is quoted, and reads back as written.
func TestRepoTreeQuotesWhatYAMLWould(t *testing.T) {
	in := publicInputs()
	in.RequiredChecks, in.LeakScanCheck = []string{"ci / build: lint", "yes"}, "yes"
	tree, err := RepoTree(in)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := resources.Decode(tree["factory/repository-my-service.yaml"])
	if err != nil {
		t.Fatal(err)
	}
	merge, _ := objs[0].Spec["merge"].(map[string]any)
	if got := merge["requiredChecks"]; !reflect.DeepEqual(got, []any{"ci / build: lint", "yes"}) {
		t.Fatalf("requiredChecks read back as %#v", got)
	}
}

// maku install --print and maku repo add cannot drift: every field repo add writes is the one
// install writes for the same repository, under the same object name.
func TestRepoTreeAgreesWithInstall(t *testing.T) {
	in := inputs(t, "generic")
	full, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := resources.Decode(full["factory/repository-app.yaml"])
	if err != nil {
		t.Fatal(err)
	}
	installed := objs[0]
	tree, err := RepoTree(RepoInputs{Repository: "example/app", Factory: in.Answers.String("release"), Namespace: in.ns(),
		Maintainers: in.Answers.List("maintainers"), RequiredChecks: []string{"ci"}, LeakScanCheck: "ci", FactoryAppID: in.FactoryAppID(),
		FactoryLogin: in.factorySlug() + "[bot]"})
	if err != nil {
		t.Fatal(err)
	}
	added, err := resources.Decode(tree["factory/repository-app.yaml"])
	if err != nil {
		t.Fatal(err)
	}
	a := added[0]
	if a.Kind != installed.Kind || a.Name != installed.Name || a.Namespace != installed.Namespace {
		t.Fatalf("repo add writes %s %s/%s, install %s %s/%s", a.Kind, a.Namespace, a.Name, installed.Kind, installed.Namespace, installed.Name)
	}
	var walk func(path string, got, want map[string]any)
	walk = func(path string, got, want map[string]any) {
		for k, v := range got {
			if sub, ok := v.(map[string]any); ok {
				w, _ := want[k].(map[string]any)
				walk(path+k+".", sub, w)
				continue
			}
			if !reflect.DeepEqual(v, want[k]) {
				t.Errorf("spec.%s%s: repo add writes %v, install %v", path, k, v, want[k])
			}
		}
	}
	walk("", a.Spec, installed.Spec)
	// The long form's shared settings are install's Factory's mergeDefaults, key for key (Q15).
	factory, err := resources.Decode(full["factory/factory.yaml"])
	if err != nil {
		t.Fatal(err)
	}
	defaults, _ := factory[0].Spec["mergeDefaults"].(map[string]any)
	long, err := RepoTree(RepoInputs{Repository: "example/app", Factory: in.Answers.String("release"), Namespace: in.ns(),
		Maintainers: in.Answers.List("maintainers"), RequiredChecks: []string{"ci"}, LeakScanCheck: "ci", FactoryAppID: in.FactoryAppID(),
		FactoryLogin: in.factorySlug() + "[bot]", NoMergeDefaults: true})
	if err != nil {
		t.Fatal(err)
	}
	longObjs, err := resources.Decode(long["factory/repository-app.yaml"])
	if err != nil {
		t.Fatal(err)
	}
	longMerge, _ := longObjs[0].Spec["merge"].(map[string]any)
	for _, k := range []string{"mergerLogin", "verifyFor", "revertWindow", "breaker"} {
		if longMerge[k] == nil || !reflect.DeepEqual(longMerge[k], defaults[k]) {
			t.Errorf("merge.%s: repo add --merge-defaults=false writes %v, install's mergeDefaults %v", k, longMerge[k], defaults[k])
		}
	}
	if !bytes.Equal(tree["github/ruleset-merge-gate.json"], full["github/ruleset-merge-gate.json"]) {
		t.Error("the ruleset differs from install's")
	}
}

// The Repository repo add writes is one the factory serves: assembled with install's file, its
// Factory and the chart's catalog, it is accepted and evaluates the catalog in shadow. By default
// against install's Factory as written, whose mergeDefaults carries the shared settings (Q15); with
// --merge-defaults=false, against a Factory with none.
func TestRepoTreeIsAccepted(t *testing.T) {
	in := inputs(t, "generic")
	file, manifests, err := FactoryObjects(in)
	if err != nil {
		t.Fatal(err)
	}
	gh := file["github"].(map[string]any)
	gh["mergerAppIDFile"], gh["mergerKeyFile"] = gh["appIDFile"], gh["privateKeyFile"]
	raw, _ := yamlDoc("", file)
	all, err := resources.Decode(manifests)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := os.ReadFile(filepath.Join("..", "..", "charts", "agent-factory", "tests", "golden", "catalog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := resources.Decode(cat)
	if err != nil {
		t.Fatal(err)
	}
	for i := range catalog {
		catalog[i].Generation, catalog[i].Catalog = 1, true
	}
	for _, withDefaults := range []bool{true, false} {
		var objs []resources.Object
		for _, o := range all {
			if o.Kind == resources.KindFactory {
				if _, ok := o.Spec["mergeDefaults"]; !ok {
					t.Fatal("install's Factory has no spec.mergeDefaults (Q15)")
				}
				if !withDefaults {
					delete(o.Spec, "mergeDefaults")
				}
				objs = append(objs, o)
			}
		}
		tree, err := RepoTree(RepoInputs{Repository: "acme/my-service", Factory: in.Answers.String("release"), Namespace: in.ns(),
			Maintainers: []string{"alice"}, RequiredChecks: []string{"ci"}, LeakScanCheck: "ci", FactoryAppID: "123",
			FactoryLogin: in.factorySlug() + "[bot]", NoMergeDefaults: !withDefaults})
		if err != nil {
			t.Fatal(err)
		}
		repo, err := resources.Decode(tree["factory/repository-my-service.yaml"])
		if err != nil {
			t.Fatal(err)
		}
		res, err := resources.Assemble(raw, slices.Concat(objs, catalog, repo))
		if err != nil {
			t.Fatalf("mergeDefaults %v: %v\nrefused: %+v", withDefaults, err, res.Refused)
		}
		if len(res.Refused) > 0 {
			t.Fatalf("mergeDefaults %v: refused: %+v", withDefaults, res.Refused)
		}
		rc := res.Config.RepositoryFor("acme/my-service")
		if rc == nil || !rc.Classes["docs-links"].Shadow {
			t.Fatalf("mergeDefaults %v: served %+v", withDefaults, rc)
		}
	}
}
