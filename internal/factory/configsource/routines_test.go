// SPDX-License-Identifier: Apache-2.0

package configsource

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
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
	"github.com/Smana/maku/internal/factory/resources"
)

func routineCRObj(name string, gen int64, catalog bool, spec config.RoutineSpec) *v1alpha1.Routine {
	var labels map[string]string
	if catalog {
		labels = map[string]string{v1alpha1.LabelCatalog: "true"}
	}
	return &v1alpha1.Routine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: gen, Labels: labels},
		Spec:       v1alpha1.RoutineSpec{RoutineSpec: spec},
	}
}

func routineOutcomeOf(t *testing.T, o Outcome, name string) resources.RoutineOutcome {
	t.Helper()
	for _, b := range o.RoutineBindings {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("%s/%s has no routine binding %s: %+v", o.Kind, o.Name, name, o.RoutineBindings)
	return resources.RoutineOutcome{}
}

func TestKindsIncludesRoutine(t *testing.T) {
	var hasRoutine bool
	for _, k := range Kinds() {
		if _, ok := k.(*v1alpha1.Routine); ok {
			hasRoutine = true
		}
	}
	if !hasRoutine {
		t.Fatal("Kinds() must include &v1alpha1.Routine{}")
	}
}

func TestSyncRoutinesLastGoodAndIsolation(t *testing.T) {
	ctx := t.Context()
	objs := servedObjs(t, "other", "Smana/Other", 1, 1)
	repo := objs[0].(*v1alpha1.Repository)
	repo.Spec.Routines = []config.RoutineBinding{
		{Name: "doc-drift", Mode: config.RoutineModePR, Cron: "0 4 * * 1"},
	}
	drift := routineCRObj("doc-drift", 1, false, config.RoutineSpec{
		Workflow:      config.WorkflowStandard,
		DefaultPrompt: "Fix doc drift v1.",
		Probe:         config.ProbeSpec{Kind: config.ProbeNone},
	})
	catalogOnly := routineCRObj("flaky-tests", 1, true, config.RoutineSpec{
		Workflow:      config.WorkflowStandard,
		DefaultPrompt: "Fix flaky tests.",
		Probe:         config.ProbeSpec{Kind: config.ProbeNone},
	})
	objs = append(objs, drift, catalogOnly)

	s, c := source(t, objs...)
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	cfg := s.Live.Current()
	rc := cfg.RepositoryFor("Smana/Other")
	if rc == nil || len(rc.ResolvedRoutines) != 1 || rc.ResolvedRoutines["doc-drift"].DefaultPrompt != "Fix doc drift v1." {
		t.Fatalf("resolved routines = %+v", rc)
	}
	if _, ok := rc.ResolvedRoutines["flaky-tests"]; ok {
		t.Fatal("unbound catalog Routine must stay inert")
	}
	if rg, repog, bound := cfg.RoutineGenerations("Smana/Other", "doc-drift"); rg != 1 || repog != 1 || !bound {
		t.Fatalf("RoutineGenerations = (%d, %d, %v), want (1, 1, true)", rg, repog, bound)
	}
	ro := outcome(t, s, resources.KindRoutine, "doc-drift")
	if !ro.Accepted || ro.ServedGeneration != 1 || len(ro.RoutineConsumers) != 1 || ro.RoutineConsumers[0].Repository != "other" {
		t.Fatalf("routine outcome = %+v", ro)
	}

	// Update Routine to an invalid spec (empty defaultPrompt): lastGood keeps generation 1 serving.
	var cur v1alpha1.Routine
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "doc-drift"}, &cur); err != nil {
		t.Fatal(err)
	}
	cur.Generation = 2
	cur.Spec.DefaultPrompt = ""
	if err := c.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	ro = outcome(t, s, resources.KindRoutine, "doc-drift")
	if ro.Accepted || ro.ObservedGeneration != 2 || ro.ServedGeneration != 1 || ro.Reason != v1alpha1.ReasonInvalid {
		t.Fatalf("refused routine outcome = %+v", ro)
	}
	if got := s.Live.Current().RepositoryFor("Smana/Other").ResolvedRoutines["doc-drift"].DefaultPrompt; got != "Fix doc drift v1." {
		t.Fatalf("expected lastGood prompt, got %q", got)
	}

	// Fix Routine at gen 3, then break Repository's binding (unknown workflow) at gen 2.
	cur.Generation = 3
	cur.Spec.DefaultPrompt = "Fix doc drift v3."
	if err := c.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.Live.Current().RepositoryFor("Smana/Other").ResolvedRoutines["doc-drift"].DefaultPrompt; got != "Fix doc drift v3." {
		t.Fatalf("gen 3 prompt = %q", got)
	}

	var curRepo v1alpha1.Repository
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "other"}, &curRepo); err != nil {
		t.Fatal(err)
	}
	curRepo.Generation = 2
	curRepo.Spec.Routines[0].Workflow = "no-such-workflow"
	if err := c.Update(ctx, &curRepo); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	s.Log = slog.New(slog.NewTextHandler(&logs, nil))
	for range 2 {
		if err := s.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	repoOut := outcome(t, s, resources.KindRepository, "other")
	if !repoOut.Accepted {
		t.Fatalf("Repository should stay Accepted when binding fails: %+v", repoOut)
	}
	if rb := routineOutcomeOf(t, repoOut, "doc-drift"); rb.Accepted || !rb.Served || rb.RoutineGeneration != 3 || rb.Reason != config.ReasonInvalid {
		t.Fatalf("refused routine binding outcome = %+v", rb)
	}
	if n := strings.Count(logs.String(), "routine binding refused"); n != 1 {
		t.Fatalf("expected 1 routine binding refusal log, got %d:\n%s", n, logs.String())
	}

	// Restart (fresh Source) has no lastResolvedRoutines fallback.
	fresh := &Source{Reader: c, Namespace: ns, File: file(t), Log: slog.New(slog.DiscardHandler)}
	if err := fresh.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if rb := routineOutcomeOf(t, outcome(t, fresh, resources.KindRepository, "other"), "doc-drift"); rb.Accepted || rb.Served {
		t.Fatalf("after restart broken binding must not serve: %+v", rb)
	}
}

func TestEnvtestSourceWatchRoutine(t *testing.T) {
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
	var publishes atomic.Int32
	src.Live.Subscribe(func(_, _ *config.Config) { publishes.Add(1) })
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

	objs := servedObjs(t, "other", "Smana/Other", 0, 0)
	repo := objs[0].(*v1alpha1.Repository)
	repo.Spec.Routines = []config.RoutineBinding{{Name: "doc-drift", Mode: config.RoutineModePR, Cron: "0 4 * * 1"}}
	rt := routineCRObj("doc-drift", 0, false, config.RoutineSpec{
		Workflow:      config.WorkflowStandard,
		DefaultPrompt: "Initial prompt.",
		Probe:         config.ProbeSpec{Kind: config.ProbeNone},
	})
	for _, o := range append(slices.Clone(objs), rt) {
		if err := c.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	eventually("initial routine is served", func() bool {
		rc := src.Live.Current().RepositoryFor("Smana/Other")
		return rc != nil && rc.ResolvedRoutines["doc-drift"].DefaultPrompt == "Initial prompt."
	})
	if rg, _, bound := src.Live.Current().RoutineGenerations("Smana/Other", "doc-drift"); rg != 1 || !bound {
		t.Fatalf("RoutineGenerations = (%d, %v), want (1, true)", rg, bound)
	}
	beforePublishes := publishes.Load()

	// Edit ONLY the Routine CR (Repository unchanged, Hash unchanged): RepoGenerations.Routines must trigger Publish.
	var liveRT v1alpha1.Routine
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "doc-drift"}, &liveRT); err != nil {
		t.Fatal(err)
	}
	liveRT.Spec.DefaultPrompt = "Updated prompt."
	if err := c.Update(ctx, &liveRT); err != nil {
		t.Fatal(err)
	}
	eventually("updated routine prompt is published", func() bool {
		rc := src.Live.Current().RepositoryFor("Smana/Other")
		return rc != nil && rc.ResolvedRoutines["doc-drift"].DefaultPrompt == "Updated prompt."
	})
	if publishes.Load() <= beforePublishes {
		t.Fatal("expected Live.Publish to fire on Routine-only update")
	}
	if rg, _, _ := src.Live.Current().RoutineGenerations("Smana/Other", "doc-drift"); rg != 2 {
		t.Fatalf("updated RoutineGeneration = %d, want 2", rg)
	}
}
