// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/resources"
)

// F1: every document Migrate makes of a real config (ai-native-ref's) passes the CRDs'
// admission. The schema requires nothing Parse defaults, and a loop row's `off` stays a string.
func TestMigratedANRConfigPassesAdmission(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	restCfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	c, err := client.New(restCfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// anr.yaml is ai-native-ref's config; file.yaml also carries the keys ANR does not set yet
	// (the team evaluation's evaluation, a class's harness, harnessAllowed and escalation). The
	// apiserver makes kube-public itself, so the two sets, alike in names, never meet.
	for _, f := range []struct {
		file, ns string
		want     int
	}{{"anr.yaml", "default", 16}, {"file.yaml", "kube-public", 7}} {
		admit(t, c, f.file, f.ns, f.want)
	}
}

// admit creates every document Migrate makes of testdata/file in ns and wants that many.
func admit(t *testing.T, c client.Client, file, ns string, want int) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "resources", "testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	manifests, _, err := resources.Migrate(b, ns)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, doc := range strings.Split(string(manifests), "\n---\n") {
		var u unstructured.Unstructured
		if err := yaml.Unmarshal([]byte(doc), &u.Object); err != nil {
			t.Fatal(err)
		}
		if len(u.Object) == 0 {
			continue
		}
		if err := c.Create(t.Context(), &u); err != nil {
			t.Errorf("%s: create %s %s: %v", file, u.GetKind(), u.GetName(), err)
			continue
		}
		created++
	}
	// One Factory, a Repository for each repository and a MergePolicy for each class (B17).
	if created != want && !t.Failed() {
		t.Fatalf("%s: created %d objects, want %d", file, created, want)
	}
}

// ai-native-ref's objects in the binding shape (testdata/anr-bound-objects.yaml) pass admission, and
// a Source serves its four repositories from them, every object accepted.
func TestANRObjectsServeFourRepositories(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	restCfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	c, err := client.New(restCfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	const ns = "agent-system"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "resources", "testdata", "anr-bound-objects.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		var u unstructured.Unstructured
		if err := yaml.Unmarshal([]byte(doc), &u.Object); err != nil {
			t.Fatal(err)
		}
		if len(u.Object) == 0 || u.GetKind() == "" {
			continue
		}
		if err := c.Create(ctx, &u); err != nil {
			t.Errorf("create %s %s: %v", u.GetKind(), u.GetName(), err)
		}
	}
	anr, err := os.ReadFile(filepath.Join("..", "resources", "testdata", "anr.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, residual, err := resources.Migrate(anr, ns)
	if err != nil {
		t.Fatal(err)
	}
	src := &configsource.Source{Reader: c, Namespace: ns, File: residual}
	if err := src.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(src.Live.Current().Repos()); n != 4 {
		t.Fatalf("%d repositories served, want 4", n)
	}
	out, _ := src.Outcomes()
	if len(out) != 17 {
		t.Fatalf("%d outcomes, want 17 (one Factory, four Repositories, twelve MergePolicies)", len(out))
	}
	for _, o := range out {
		if !o.Accepted {
			t.Errorf("%s %s refused: %s %s", o.Kind, o.Name, o.Reason, o.Message)
		}
	}
}
