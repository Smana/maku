// SPDX-License-Identifier: Apache-2.0

package statuswriter

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/decision"
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
	const repository, className = "Smana/cloud-native-ref", "docs-links"
	repo := &v1alpha1.Repository{ObjectMeta: metav1.ObjectMeta{Name: "cnref", Namespace: envNS},
		Spec: v1alpha1.RepositorySpec{FactoryRef: "aws-0", Repository: repository, Maintainers: []string{"Smana"}, TriggerLabel: "factory/ready",
			MergePolicies: []config.Binding{{Name: "docs-links", Mode: config.ModeShadow}, {Name: "revert", Mode: config.ModeShadow}}}}
	policy := &v1alpha1.MergePolicy{ObjectMeta: metav1.ObjectMeta{Name: className, Namespace: envNS},
		Spec: v1alpha1.MergePolicySpec{PolicySpec: config.PolicySpec{Class: config.Class{Description: "a class definition"}}}}
	for _, o := range []client.Object{repo, policy} {
		if err := c.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}

	bindings := []resources.BindingOutcome{
		{Repository: "cnref", Name: "docs-links", Mode: config.ModeShadow, PolicyGeneration: 1, Accepted: true, Served: true},
		{Repository: "cnref", Name: "revert", Mode: config.ModeShadow, PolicyGeneration: 1, Accepted: true, Served: true},
	}
	outcomes := []configsource.Outcome{
		{Kind: resources.KindRepository, Name: "cnref", Repository: repository, ObservedGeneration: 1, ServedGeneration: 1, Accepted: true, Bindings: bindings},
		{Kind: resources.KindMergePolicy, Name: className, ObservedGeneration: 1, ServedGeneration: 1, Accepted: true, Consumers: bindings[:1]},
	}
	// A nanosecond-precision time: the API server keeps seconds, and a second pass must still be quiet.
	decidedAt := time.Date(2026, 11, 10, 10, 0, 0, 123456789, time.UTC)
	w := &Writer{Client: c, Reader: c, Namespace: envNS, Log: slog.New(slog.DiscardHandler),
		Inputs: func(context.Context) Inputs {
			return Inputs{Outcomes: outcomes, Config: cfg, Decisions: func(string) map[string]decision.Last {
				return map[string]decision.Last{className: {Verdict: "merge", Reason: "green", At: decidedAt}}
			}}
		}}
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
	if r.Status.ObservedGeneration != r.Generation || len(r.Status.Classes) != 2 || r.Status.ClassCount != 2 {
		t.Fatalf("repository status %+v", r.Status)
	}
	if p.Status.ConsumerCount != 1 || p.Status.CurrentCount != 1 {
		t.Fatalf("policy status %+v", p.Status)
	}

	if err := w.WriteOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if at := r.Status.Classes; len(at) == 0 || !hasDecision(at, decidedAt.Truncate(time.Second)) {
		t.Fatalf("no decision time in %+v", at)
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
	if conn == nil || !conn.LastTransitionTime.Equal(&was) {
		t.Fatalf("Connected %+v", conn)
	}

	// Still refused, for another reason: the message follows, the transition time does not.
	refusedAt := cnd.LastTransitionTime
	outcomes[0].Reason, outcomes[0].Message = v1alpha1.ReasonDefinedTwice, "defined twice for the test"
	time.Sleep(1100 * time.Millisecond) // the API server keeps seconds: a new time would show
	if err := w.WriteOnce(ctx); err != nil {
		t.Fatal(err)
	}
	r4, _ := get()
	cnd = apimeta.FindStatusCondition(r4.Status.Conditions, v1alpha1.ConditionAccepted)
	if cnd == nil || cnd.Reason != v1alpha1.ReasonDefinedTwice || cnd.Message != "defined twice for the test" || !cnd.LastTransitionTime.Equal(&refusedAt) {
		t.Fatalf("Accepted %+v, want the time %v kept", cnd, refusedAt)
	}
}

func hasDecision(cs []v1alpha1.BindingStatus, at time.Time) bool {
	for _, c := range cs {
		if c.LastDecisionAt != nil && c.LastDecisionAt.Time.Equal(at) {
			return true
		}
	}
	return false
}

// With none of the CRDs installed a pass is not an error and logs nothing at Warn.
func TestEnvtestWriteOnceWithoutCRDs(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	env := &envtest.Environment{}
	restCfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	c, err := client.New(restCfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	w := &Writer{Client: c, Reader: c, Namespace: envNS, Every: 20 * time.Millisecond,
		Log:    slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Inputs: func(context.Context) Inputs { return Inputs{} }}
	if err := w.WriteOnce(t.Context()); err != nil {
		t.Fatalf("WriteOnce: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("logged at Warn: %s", logs.String())
	}
}

// B10: the binding status of Task 11's objects, written to a real API server, and the printer
// cells kubectl reads from it (a meta.k8s.io Table).
func TestWriterBindings(t *testing.T) {
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
	in, repos, policies := bindingsWorld()
	for i := range repos {
		r := repos[i].DeepCopy()
		r.Namespace, r.Generation = envNS, 0
		r.Spec.FactoryRef, r.Spec.Maintainers, r.Spec.TriggerLabel = "aws-0", []string{"Smana"}, "factory/ready"
		if r.Spec.MergePolicies != nil { // OD-8 in CEL: revert is bound beside docs-links
			r.Spec.MergePolicies = append(r.Spec.MergePolicies, config.Binding{Name: "revert", Mode: "shadow"})
		}
		if err := c.Create(ctx, r); err != nil {
			t.Fatalf("create Repository %s: %v", r.Name, err)
		}
	}
	for i := range policies {
		p := policies[i].DeepCopy()
		p.Namespace, p.Generation = envNS, 0
		p.Spec.Description = "a class definition"
		if err := c.Create(ctx, p); err != nil {
			t.Fatalf("create MergePolicy %s: %v", p.Name, err)
		}
	}
	w := &Writer{Client: c, Reader: c, Namespace: envNS, Log: slog.New(slog.DiscardHandler),
		Inputs: func(context.Context) Inputs { return in }}
	if err := w.WriteOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var r v1alpha1.Repository
	if err := c.Get(ctx, client.ObjectKey{Namespace: envNS, Name: "ai-native-ref"}, &r); err != nil {
		t.Fatal(err)
	}
	if b := apimeta.FindStatusCondition(r.Status.Conditions, v1alpha1.ConditionBindingsValid); b == nil || b.Status != metav1.ConditionFalse || len(r.Status.Classes) != 2 ||
		r.Status.Merge == nil || r.Status.Merge.AutoMergesPerDay != 2 {
		t.Fatalf("ai-native-ref status %+v", r.Status)
	}

	kc, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatal(err)
	}
	cells := func(resource, name string) map[string]any {
		t.Helper()
		body, err := kc.Discovery().RESTClient().Get().
			AbsPath("/apis/maku.ogenki.io/v1alpha1/namespaces", envNS, resource, name).
			SetHeader("Accept", "application/json;as=Table;g=meta.k8s.io;v=v1").DoRaw(ctx)
		if err != nil {
			t.Fatalf("%s/%s: %v: %s", resource, name, err, body)
		}
		var tbl metav1.Table
		if err := json.Unmarshal(body, &tbl); err != nil || len(tbl.Rows) != 1 {
			t.Fatalf("%s/%s table: %v: %s", resource, name, err, body)
		}
		out := map[string]any{}
		for i, col := range tbl.ColumnDefinitions {
			out[col.Name] = tbl.Rows[0].Cells[i]
		}
		return out
	}
	// JSON numbers decode as float64.
	if got := cells("mergepolicies", "dead-links"); got["Consumers"] != 2.0 || got["Current"] != 1.0 || got["Live"] != 0.0 || got["Accepted"] != "True" {
		t.Fatalf("mergepolicy/dead-links %v, want Consumers 2, Current 1, Live 0, Accepted True", got)
	}
	if got := cells("repositories", "cloud-native-ref"); got["Classes"] != 2.0 || got["Live"] != 1.0 {
		t.Fatalf("repository/cloud-native-ref %v, want Classes 2, Live 1", got)
	}

	// A second pass writes nothing: the stored status round-trips, on both kinds.
	var p v1alpha1.MergePolicy
	if err := c.Get(ctx, client.ObjectKey{Namespace: envNS, Name: "dead-links"}, &p); err != nil {
		t.Fatal(err)
	}
	before, beforeP := r.ResourceVersion, p.ResourceVersion
	if err := w.WriteOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: envNS, Name: "ai-native-ref"}, &r); err != nil || r.ResourceVersion != before {
		t.Fatalf("a second pass wrote the Repository: %s -> %s (%v)", before, r.ResourceVersion, err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: envNS, Name: "dead-links"}, &p); err != nil || p.ResourceVersion != beforeP {
		t.Fatalf("a second pass wrote the MergePolicy: %s -> %s (%v)", beforeP, p.ResourceVersion, err)
	}
}
