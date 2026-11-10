// SPDX-License-Identifier: Apache-2.0

package statuswriter

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/resources"
)

const envNS = "agent-system"

// A real API server: WriteOnce publishes the status, writes nothing when nothing changed, and
// follows the outcome when it turns to a refusal.
func TestEnvtestWriteOnce(t *testing.T) {
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
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envNS}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "configsource", "testdata", "file.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	const repository = "Smana/cloud-native-ref"
	classes := cfg.RepositoryFor(repository).Classes
	repo := &v1alpha1.Repository{ObjectMeta: metav1.ObjectMeta{Name: "cnref", Namespace: envNS},
		Spec: v1alpha1.RepositorySpec{FactoryRef: "aws-0", Repository: repository, Maintainers: []string{"Smana"}, TriggerLabel: "factory/ready"}}
	policy := &v1alpha1.MergePolicy{ObjectMeta: metav1.ObjectMeta{Name: "cnref", Namespace: envNS},
		Spec: v1alpha1.MergePolicySpec{RepositoryRef: "cnref", Classes: classes}}
	for _, o := range []client.Object{repo, policy} {
		if err := c.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}

	outcomes := []configsource.Outcome{
		{Kind: resources.KindRepository, Name: "cnref", Repository: repository, ObservedGeneration: 1, ServedGeneration: 1, Accepted: true},
		{Kind: resources.KindMergePolicy, Name: "cnref", Repository: repository, ObservedGeneration: 1, ServedGeneration: 1, Accepted: true},
	}
	w := &Writer{Client: c, Namespace: envNS, Log: slog.New(slog.DiscardHandler),
		Inputs: func(context.Context) Inputs { return Inputs{Outcomes: outcomes, Config: cfg} }}
	get := func() (*v1alpha1.Repository, *v1alpha1.MergePolicy) {
		t.Helper()
		var r v1alpha1.Repository
		var p v1alpha1.MergePolicy
		if err := c.Get(ctx, client.ObjectKeyFromObject(repo), &r); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, client.ObjectKeyFromObject(policy), &p); err != nil {
			t.Fatal(err)
		}
		return &r, &p
	}

	if err := w.WriteOnce(ctx); err != nil {
		t.Fatal(err)
	}
	r, p := get()
	if cnd := apimeta.FindStatusCondition(r.Status.Conditions, v1alpha1.ConditionAccepted); cnd == nil || cnd.Status != metav1.ConditionTrue || cnd.ObservedGeneration != r.Generation {
		t.Fatalf("repository Accepted %+v", cnd)
	}
	if r.Status.ObservedGeneration != r.Generation || r.Status.MergePolicy != "cnref" {
		t.Fatalf("repository status %+v", r.Status)
	}
	if len(p.Status.Classes) != len(classes) || p.Status.ClassCount != len(classes) || len(classes) == 0 {
		t.Fatalf("policy classes %+v, want %d", p.Status.Classes, len(classes))
	}

	if err := w.WriteOnce(ctx); err != nil {
		t.Fatal(err)
	}
	r2, p2 := get()
	if r2.ResourceVersion != r.ResourceVersion || p2.ResourceVersion != p.ResourceVersion {
		t.Fatalf("a second pass wrote: %s -> %s, %s -> %s", r.ResourceVersion, r2.ResourceVersion, p.ResourceVersion, p2.ResourceVersion)
	}

	was := apimeta.FindStatusCondition(r.Status.Conditions, v1alpha1.ConditionAccepted).LastTransitionTime
	outcomes[0] = configsource.Outcome{Kind: resources.KindRepository, Name: "cnref", Repository: repository, ObservedGeneration: 1,
		Reason: v1alpha1.ReasonInvalid, Message: "refused for the test"}
	if err := w.WriteOnce(ctx); err != nil {
		t.Fatal(err)
	}
	r3, _ := get()
	cnd := apimeta.FindStatusCondition(r3.Status.Conditions, v1alpha1.ConditionAccepted)
	if cnd == nil || cnd.Status != metav1.ConditionFalse || cnd.Reason != v1alpha1.ReasonInvalid || cnd.Message != "refused for the test" {
		t.Fatalf("refused Accepted %+v", cnd)
	}
	conn := apimeta.FindStatusCondition(r3.Status.Conditions, ConditionConnected)
	if conn == nil || conn.LastTransitionTime.Before(&was) {
		t.Fatalf("Connected %+v", conn)
	}
}
