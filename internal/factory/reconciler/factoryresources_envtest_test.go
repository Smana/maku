// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// The three factory-resource CRDs install: their CEL compiles within the API server's cost budget,
// and a MergePolicy that sets a mode is refused (B4).
func TestFactoryResourceCRDsInstall(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	restCfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	s := scheme()
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(restCfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, name := range []string{"factories.maku.ogenki.io", "repositories.maku.ogenki.io", "mergepolicies.maku.ogenki.io"} {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := c.Get(ctx, types.NamespacedName{Name: name}, &crd); err != nil {
			t.Errorf("CRD %s: %v", name, err)
		}
	}
	mp := &v1alpha1.MergePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "deps", Namespace: "default"},
		Spec:       v1alpha1.MergePolicySpec{PolicySpec: config.PolicySpec{Class: config.Class{Live: true}}},
	}
	if err := c.Create(ctx, mp); err == nil || !strings.Contains(err.Error(), "mode is the binding's") {
		t.Fatalf("got %v, want the mode-is-the-binding's rule", err)
	}
}
