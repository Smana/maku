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
	file, err := os.ReadFile(filepath.Join("..", "resources", "testdata", "anr.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	manifests, _, err := resources.Migrate(file, "default")
	if err != nil {
		t.Fatal(err)
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
			t.Errorf("create %s %s: %v", u.GetKind(), u.GetName(), err)
			continue
		}
		created++
	}
	// One Factory, and a Repository and a MergePolicy for each of the three repositories.
	if created != 7 && !t.Failed() {
		t.Fatalf("created %d objects, want 7", created)
	}
}
