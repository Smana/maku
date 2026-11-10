// SPDX-License-Identifier: Apache-2.0

package configsource

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/resources"
)

const ns = "agent-system"

func file(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/file.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

func repoObj(name, repository string, gen int64) *v1alpha1.Repository {
	return &v1alpha1.Repository{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: gen},
		Spec:       v1alpha1.RepositorySpec{FactoryRef: "aws-0", Repository: repository, Maintainers: []string{"Smana"}, TriggerLabel: "factory/ready"},
	}
}

func policyObj(t *testing.T, name, ref string, gen int64, mutate func(*v1alpha1.MergePolicySpec)) *v1alpha1.MergePolicy {
	t.Helper()
	c, err := config.Parse(file(t))
	if err != nil {
		t.Fatal(err)
	}
	r := c.RepositoryFor("Smana/cloud-native-ref")
	p := &v1alpha1.MergePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: gen},
		Spec:       v1alpha1.MergePolicySpec{RepositoryRef: ref, Classes: r.Classes, Merge: r.Merge},
	}
	if mutate != nil {
		mutate(&p.Spec)
	}
	return p
}

func source(t *testing.T, objs ...client.Object) (*Source, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(objs...).Build()
	return &Source{Reader: c, Namespace: ns, File: file(t), Log: slog.New(slog.DiscardHandler)}, c
}

func outcome(t *testing.T, s *Source, k resources.Kind, name string) Outcome {
	t.Helper()
	for _, o := range outs(s) {
		if o.Kind == k && o.Name == name {
			return o
		}
	}
	t.Fatalf("no outcome for %s/%s in %+v", k, name, outs(s))
	return Outcome{}
}

func TestSyncServesTheFileAloneWithNoObjects(t *testing.T) {
	s, _ := source(t)
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg := s.Live.Current()
	if len(cfg.Repos()) != 1 {
		t.Fatalf("repos: %+v", cfg.Repos())
	}
	if cfg.Generations.Repos != nil || cfg.Generations.Factory != 0 {
		t.Fatalf("generations must be zero with the file alone: %+v", cfg.Generations)
	}
	if len(outs(s)) != 0 {
		t.Fatalf("outcomes: %+v", outs(s))
	}
}

func TestSyncAddsARepository(t *testing.T) {
	s, _ := source(t, repoObj("other", "Smana/Other", 3), policyObj(t, "other", "other", 5, nil))
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg := s.Live.Current()
	if len(cfg.Repos()) != 2 {
		t.Fatalf("repos: %+v", cfg.Repos())
	}
	if f, r, p := cfg.GenerationsFor("smana/other"); f != 0 || r != 3 || p != 5 {
		t.Fatalf("generations: %d %d %d", f, r, p)
	}
	if g := cfg.Generations.Repos["smana/other"]; g.RepositoryName != "other" || g.PolicyName != "other" {
		t.Fatalf("names: %+v", g)
	}
	if o := outcome(t, s, resources.KindRepository, "other"); !o.Accepted || o.ServedGeneration != 3 || o.ObservedGeneration != 3 {
		t.Fatalf("outcome: %+v", o)
	}
	all := outs(s)
	if all[0].Kind != resources.KindMergePolicy || all[1].Kind != resources.KindRepository {
		t.Fatalf("outcomes must sort by kind: %+v", all)
	}
}

func TestSyncKeepsTheLastGoodVersion(t *testing.T) {
	ctx := t.Context()
	s, c := source(t, repoObj("other", "Smana/Other", 1), policyObj(t, "other", "other", 1, nil))
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	var p v1alpha1.MergePolicy
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "other"}, &p); err != nil {
		t.Fatal(err)
	}
	p.Generation = 2
	p.Spec.Classes = map[string]config.Class{"docs-links": {Live: true, Shadow: true}, "revert": {Shadow: true}}
	if err := c.Update(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := s.Live.Current()
	if _, ok := cfg.RepositoryFor("Smana/Other").Classes["docs"]; !ok {
		t.Fatalf("generation 1's classes must still be served: %+v", cfg.RepositoryFor("Smana/Other").Classes)
	}
	if _, r, pg := cfg.GenerationsFor("Smana/Other"); r != 1 || pg != 1 {
		t.Fatalf("generations: %d %d", r, pg)
	}
	o := outcome(t, s, resources.KindMergePolicy, "other")
	if o.Accepted || o.ServedGeneration != 1 || o.ObservedGeneration != 2 || o.Reason != v1alpha1.ReasonInvalid || o.Message == "" {
		t.Fatalf("outcome: %+v", o)
	}
	if r := outcome(t, s, resources.KindRepository, "other"); !r.Accepted {
		t.Fatalf("the repository is still served: %+v", r)
	}
}

func TestSyncDropsADeletedObject(t *testing.T) {
	ctx := t.Context()
	s, c := source(t, repoObj("other", "Smana/Other", 1), policyObj(t, "other", "other", 1, nil))
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.Live.Current().Repos()) != 2 {
		t.Fatal("repository not served")
	}
	for _, o := range []client.Object{repoObj("other", "", 0), policyObj(t, "other", "other", 0, nil)} {
		if err := c.Delete(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Live.Current().Repos()); n != 1 {
		t.Fatalf("a deleted object is meant to go: %d repos", n)
	}
	if s.Live.Current().Generations.Repos != nil {
		t.Fatalf("generations: %+v", s.Live.Current().Generations)
	}
	if len(outs(s)) != 0 || len(s.lastGood) != 0 {
		t.Fatalf("outcomes %+v lastGood %+v", outs(s), s.lastGood)
	}
}

func TestSyncAtStartupWithNothingServableFails(t *testing.T) {
	manifests, residual, err := resources.Migrate(file(t), ns)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := resources.Decode(manifests)
	if err != nil {
		t.Fatal(err)
	}
	var cs []client.Object
	for _, o := range objs {
		switch o.Kind {
		case resources.KindFactory:
			o.Spec["caps"] = map[string]any{"activeTasks": -1}
			var f v1alpha1.Factory
			f.Name, f.Namespace, f.Generation = o.Name, ns, 1
			if err := specInto(o.Spec, &f.Spec); err != nil {
				t.Fatal(err)
			}
			cs = append(cs, &f)
		case resources.KindRepository:
			var r v1alpha1.Repository
			r.Name, r.Namespace, r.Generation = o.Name, ns, 1
			if err := specInto(o.Spec, &r.Spec); err != nil {
				t.Fatal(err)
			}
			cs = append(cs, &r)
		case resources.KindMergePolicy:
			var p v1alpha1.MergePolicy
			p.Name, p.Namespace, p.Generation = o.Name, ns, 1
			if err := specInto(o.Spec, &p.Spec); err != nil {
				t.Fatal(err)
			}
			cs = append(cs, &p)
		}
	}
	s, _ := source(t, cs...)
	s.File = residual
	if err := s.Sync(t.Context()); err == nil {
		t.Fatal("Sync must fail when nothing can be served at startup")
	}
	if s.Live != nil {
		t.Fatal("Live must stay nil")
	}
	var found bool
	for _, o := range outs(s) {
		if o.Kind == resources.KindFactory && !o.Accepted && o.Reason == v1alpha1.ReasonInvalid {
			found = true
		}
	}
	if !found {
		t.Fatalf("the refusals are still reported: %+v", outs(s))
	}
}

func TestSyncKeepsTheCurrentConfigWhenLaterNothingIsServable(t *testing.T) {
	ctx := t.Context()
	s, c := source(t)
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	prev := s.Live.Current()
	// A broken file makes Assemble fail while a config is already published.
	s.File = []byte("factory: [")
	if err := s.Sync(ctx); err != nil {
		t.Fatalf("after startup an error keeps the current config: %v", err)
	}
	if s.Live.Current() != prev {
		t.Fatal("the config must be kept")
	}
	_ = c
}

func specInto(spec map[string]any, into any) error {
	b, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

type noCRDs struct{ client.Reader }

func (noCRDs) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "maku.ogenki.io", Kind: "Factory"}}
}

func TestSyncWithoutCRDsRunsFromTheFile(t *testing.T) {
	var sb strings.Builder
	s := &Source{Reader: noCRDs{}, Namespace: ns, File: file(t), Log: slog.New(slog.NewTextHandler(&sb, nil))}
	for range 2 {
		if err := s.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.Live.Current().Repos()) != 1 {
		t.Fatal("the file alone must serve")
	}
	if n := strings.Count(sb.String(), "CRDs"); n != 1 {
		t.Fatalf("must log once, logged %d times:\n%s", n, sb.String())
	}
}

func TestKickCoalesces(t *testing.T) {
	s, _ := source(t)
	s.Kick()
	s.Kick()
	s.Kick()
	if len(s.kickCh()) != 1 {
		t.Fatal("kicks must coalesce")
	}
}

// A first-pass refusal whose last good version was not served in the end is not accepted, even
// at generation 0, where an unserved object's zero ServedGeneration equals its own.
func TestARefusedObjectNotServedIsNotAcceptedAtGenerationZero(t *testing.T) {
	listed := []resources.Object{{Kind: resources.KindRepository, Name: "a", Generation: 0}}
	first := map[key]resources.Refusal{{resources.KindRepository, "a"}: {Kind: resources.KindRepository, Name: "a",
		Reason: v1alpha1.ReasonInvalid, Message: "bad"}}
	out := outcomes(listed, resources.Result{}, first, nil)
	if len(out) != 1 || out[0].Accepted || out[0].Reason != v1alpha1.ReasonInvalid || out[0].Message != "bad" {
		t.Fatalf("outcome %+v, want refused with its first-pass reason", out)
	}
}

// A MergePolicy whose repositoryRef names no served Repository records no generations under "".
func TestGenerationsIgnoreAnOrphanPolicy(t *testing.T) {
	g := generations([]resources.Object{
		{Kind: resources.KindMergePolicy, Name: "p", Generation: 3, Spec: map[string]any{"repositoryRef": "missing"}},
	})
	if _, ok := g.Repos[""]; ok || len(g.Repos) != 0 {
		t.Fatalf("generations %+v, want none for an orphan policy", g.Repos)
	}
}

// outs are s's outcomes, without the Sync's completion.
func outs(s *Source) []Outcome {
	o, _ := s.Outcomes()
	return o
}
