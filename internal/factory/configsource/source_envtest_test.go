// SPDX-License-Identifier: Apache-2.0

package configsource

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// A real API server and a manager cache: creating, updating and deleting a Repository and its
// MergePolicy reach the published config through Watch's kicks.
func TestEnvtestSourceFollowsTheObjects(t *testing.T) {
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
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{Scheme: s, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		t.Fatal(err)
	}
	c := mgr.GetClient()
	ctx := t.Context()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}

	src := &Source{Reader: mgr.GetAPIReader(), Namespace: ns, File: file(t), Resync: time.Hour}
	if err := src.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := src.Watch(ctx, mgr.GetCache(), Kinds()); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Add(src); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	eventually := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("within 10s: %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// With one entry RepositoryFor answers it for any name, so count the entries.
	served := func() bool { return len(src.Live.Current().Repos()) == 2 }

	repo := &v1alpha1.Repository{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: ns},
		Spec: v1alpha1.RepositorySpec{FactoryRef: "aws-0", Repository: "Smana/Other", Maintainers: []string{"Smana"}, TriggerLabel: "factory/ready"}}
	policy := policyObj(t, "other", "other", 0, nil)
	if err := c.Create(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, policy); err != nil {
		t.Fatal(err)
	}
	eventually("the repository is served", func() bool { return served() })
	if _, r, p := src.Live.Current().GenerationsFor("Smana/Other"); r != 1 || p != 1 {
		t.Fatalf("generations %d %d, want the API server's 1 1", r, p)
	}

	policy.Spec.Classes["extra"] = config.Class{Shadow: true}
	if err := c.Update(ctx, policy); err != nil {
		t.Fatal(err)
	}
	eventually("the new class appears", func() bool {
		_, ok := src.Live.Current().RepositoryFor("Smana/Other").Classes["extra"]
		return ok
	})

	if err := c.Delete(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, repo); err != nil {
		t.Fatal(err)
	}
	eventually("the repository is gone", func() bool { return len(src.Live.Current().Repos()) == 1 })
	var left v1alpha1.RepositoryList
	if err := c.List(ctx, &left, client.InNamespace(ns)); err != nil || len(left.Items) != 0 {
		t.Fatalf("left %v %v", left.Items, err)
	}
}
