// SPDX-License-Identifier: Apache-2.0

package configsource

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/resources"
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

// PR5: the kinds are listed as the API server stores them. A typed list made ToUnstructured write
// every config.Merge field without omitempty as present, so an omitted fixRuns read as a set 0 and
// every merge key looked set, defeating mergeDefaults' key-by-key override (Q8).
func TestListKeepsOmittedKeysOmitted(t *testing.T) {
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
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	for _, doc := range []string{`
apiVersion: maku.ogenki.io/v1alpha1
kind: Repository
metadata: {name: other, namespace: agent-system}
spec:
  factoryRef: aws-0
  repository: Smana/Other
  maintainers: [Smana]
  triggerLabel: factory/ready
  merge: {autoMergesPerDay: 2}
`, `
apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata: {name: legacy, namespace: agent-system}
spec:
  repositoryRef: legacy
  classes:
    docs-links: {shadow: true}
    revert: {shadow: true}
  merge:
    requiredChecks: [lint]
    verifyChecks: [lint]
    leakScanCheck: lint
    policyBotLogin: policy-bot[bot]
    mergerLogin: merger[bot]
    autoMergesPerDay: 1
    verifyFor: 30m
    revertWindow: 168h
    breaker: {window: 10, maxReverts: 1}
`} {
		var u unstructured.Unstructured
		if err := yaml.Unmarshal([]byte(doc), &u.Object); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(ctx, &u); err != nil {
			t.Fatalf("create %s: %v", u.GetName(), err)
		}
	}

	src := &Source{Reader: c, Namespace: ns}
	objs, err := src.list(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		merge, _ := o.Spec["merge"].(map[string]any)
		switch o.Kind {
		case resources.KindRepository:
			if len(merge) != 1 || merge["autoMergesPerDay"] == nil {
				t.Errorf("Repository merge %v, want exactly {autoMergesPerDay: 2}", merge)
			}
			for _, k := range []string{"checks", "classAuthority", "mergePolicies"} {
				if _, ok := o.Spec[k]; ok {
					t.Errorf("Repository spec.%s is present, the author omitted it", k)
				}
			}
		case resources.KindMergePolicy:
			for _, k := range []string{"fixRuns", "checkReruns"} {
				if _, ok := merge[k]; ok {
					t.Errorf("MergePolicy merge.%s is present, the author omitted it", k)
				}
			}
		}
	}
	if len(objs) != 2 {
		t.Fatalf("listed %d objects, want 2", len(objs))
	}
}
