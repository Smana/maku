// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

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
	}{{"anr.yaml", "default", 7}, {"file.yaml", "kube-public", 3}} {
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
	// One Factory, and a Repository and a MergePolicy for each repository.
	if created != want && !t.Failed() {
		t.Fatalf("%s: created %d objects, want %d", file, created, want)
	}
}
