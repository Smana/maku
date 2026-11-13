// SPDX-License-Identifier: Apache-2.0

package configsource

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/resources"
)

// classPolicyObj is a class-shape MergePolicy.
func classPolicyObj(name string, gen int64, spec config.PolicySpec) *v1alpha1.MergePolicy {
	return &v1alpha1.MergePolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: gen},
		Spec: v1alpha1.MergePolicySpec{PolicySpec: spec}}
}

// deadLinks is the spec's dead-links policy; with flux, generation 4 also watches Flux.
func deadLinks(gen int64, flux bool) *v1alpha1.MergePolicy {
	spec := config.PolicySpec{Class: config.Class{ModifyOnly: true, MaxModifiedLines: 20, DiffShape: "links-only", MaxPerDay: 1,
		Oracle:  &config.Oracle{Probe: "dead-links", MaxFindings: 10},
		Monitor: &config.Monitor{OracleRuns: 2, FollowUpWindow: config.Duration{Duration: 168 * time.Hour}}},
		Requires: []string{"allowedFiles", "holdoutChecks", "oracleWorkflow"}}
	if flux {
		spec.Monitor.Flux = &config.FluxWatch{Within: config.Duration{Duration: 30 * time.Minute}}
	}
	return classPolicyObj("dead-links", gen, spec)
}

// boundRepoObj binds docs-links, revert and dead-links, with the file's merge settings.
func boundRepoObj(t *testing.T, name, repository, fluxSource string) *v1alpha1.Repository {
	t.Helper()
	merge := *policyObj(t, "x", "x", 0, nil).Spec.Merge
	merge.FluxSource = fluxSource
	r := repoObj(name, repository, 1)
	r.Spec.Merge = &merge
	r.Spec.MergePolicies = []config.Binding{
		{Name: "docs-links", Mode: config.ModeShadow, HoldoutChecks: []string{"validate"}},
		{Name: "revert", Mode: config.ModeShadow, HoldoutChecks: []string{"validate"}},
		{Name: "dead-links", Mode: config.ModeShadow, AllowedFiles: []string{`^docs/.+\.md$`}, HoldoutChecks: []string{"External links 🔗"}, OracleWorkflow: "dead-links.yaml"},
	}
	return r
}

func bindingObjs(t *testing.T) []client.Object {
	holdout := config.PolicySpec{Requires: []string{"holdoutChecks"}}
	return []client.Object{
		classPolicyObj("docs-links", 1, holdout), classPolicyObj("revert", 1, holdout), deadLinks(3, false),
		boundRepoObj(t, "ai-native-ref", "Smana/ai-native-ref", ""),
		boundRepoObj(t, "runlore", "Smana/runlore", "flux-system/flux-system"),
	}
}

func bindingOutcome(t *testing.T, o Outcome, name string) resources.BindingOutcome {
	t.Helper()
	for _, b := range o.Bindings {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("%s/%s has no binding %s: %+v", o.Kind, o.Name, name, o.Bindings)
	return resources.BindingOutcome{}
}

// B9, D5, D14: a binding generation 4 of its policy breaks keeps generation 3's class while the
// factory runs; the other consumer serves 4; after a restart the broken binding serves nothing.
func TestSyncKeepsABindingsLastGood(t *testing.T) {
	ctx := t.Context()
	s, c := source(t, bindingObjs(t)...)
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if b := bindingOutcome(t, outcome(t, s, resources.KindRepository, "ai-native-ref"), "dead-links"); !b.Accepted || b.PolicyGeneration != 3 {
		t.Fatalf("sync 1: %+v", b)
	}
	if err := c.Update(ctx, deadLinksAt(t, c, 4)); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	s.Log = slog.New(slog.NewTextHandler(&logs, nil))
	for range 2 {
		if err := s.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	anr := outcome(t, s, resources.KindRepository, "ai-native-ref")
	if b := bindingOutcome(t, anr, "dead-links"); b.Accepted || !b.Served || b.PolicyGeneration != 3 || b.Reason != config.ReasonMissingParameter {
		t.Fatalf("sync 2, ai-native-ref: %+v", b)
	}
	if !anr.Accepted {
		t.Fatalf("the Repository stays accepted: %+v", anr)
	}
	if b := bindingOutcome(t, outcome(t, s, resources.KindRepository, "runlore"), "dead-links"); !b.Accepted || b.PolicyGeneration != 4 {
		t.Fatalf("sync 2, runlore: %+v", b)
	}
	cfg := s.Live.Current()
	if _, ok := cfg.RepositoryFor("Smana/ai-native-ref").Classes["dead-links"]; !ok {
		t.Fatal("generation 3's class must still be served")
	}
	if p, _, bound := cfg.ClassGenerations("Smana/ai-native-ref", "dead-links"); p != 3 || !bound {
		t.Fatalf("ai-native-ref's dead-links decided under %d (bound %v), want 3", p, bound)
	}
	if p, _, _ := cfg.ClassGenerations("Smana/runlore", "dead-links"); p != 4 {
		t.Fatalf("runlore's dead-links decided under %d, want 4", p)
	}
	pol := outcome(t, s, resources.KindMergePolicy, "dead-links")
	if !pol.Accepted || len(pol.Consumers) != 2 {
		t.Fatalf("the policy is accepted alone, with its two consumers: %+v", pol)
	}
	if n := strings.Count(logs.String(), "binding refused"); n != 1 || !strings.Contains(logs.String(), "fluxSource") {
		t.Fatalf("%d binding refusal lines, want 1:\n%s", n, logs.String())
	}

	// A restart: no last resolved class (D14).
	restarted := &Source{Reader: c, Namespace: ns, File: file(t), Log: slog.New(slog.DiscardHandler)}
	if err := restarted.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if b := bindingOutcome(t, outcome(t, restarted, resources.KindRepository, "ai-native-ref"), "dead-links"); b.Served {
		t.Fatalf("after a restart: %+v", b)
	}
	if _, ok := restarted.Live.Current().RepositoryFor("Smana/ai-native-ref").Classes["dead-links"]; ok {
		t.Fatal("after a restart the dropped class is absent")
	}
}

// deadLinksAt is the stored dead-links at generation gen, watching Flux.
func deadLinksAt(t *testing.T, c client.Client, gen int64) *v1alpha1.MergePolicy {
	t.Helper()
	var p v1alpha1.MergePolicy
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: "dead-links"}, &p); err != nil {
		t.Fatal(err)
	}
	p.Generation, p.Spec = gen, deadLinks(gen, true).Spec
	return &p
}

// B12: each bound class records its own policy's generation, and the accepted policies are served.
func TestSyncGenerationsPerClass(t *testing.T) {
	s, _ := source(t, bindingObjs(t)...)
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg := s.Live.Current()
	g := cfg.Generations.Repos["smana/ai-native-ref"]
	if g.Classes["dead-links"] != (config.ClassGeneration{Policy: 3, PolicyName: "dead-links"}) || g.Repository != 1 || g.RepositoryName != "ai-native-ref" {
		t.Fatalf("generations %+v", g)
	}
	if p, ok := cfg.Policies["dead-links"]; !ok || p.Generation != 3 || len(cfg.Policies) != 3 {
		t.Fatalf("policies %+v", cfg.Policies)
	}
}
