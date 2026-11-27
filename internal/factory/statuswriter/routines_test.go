// SPDX-License-Identifier: Apache-2.0

package statuswriter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	"github.com/Smana/maku/internal/factory/resources"
)

func routineObj(name string, gen int64) v1alpha1.Routine {
	return v1alpha1.Routine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: gen},
		Spec: v1alpha1.RoutineSpec{RoutineSpec: config.RoutineSpec{
			DefaultPrompt: "Fix issue.",
			Probe:         config.ProbeSpec{Kind: config.ProbeNone},
		}},
	}
}

func TestDesiredRoutineStatusAndConsumers(t *testing.T) {
	consumers := []resources.RoutineOutcome{
		{Repository: "zeta", Name: "doc-drift", Mode: config.RoutineModePR, RoutineGeneration: 3, Accepted: true, Served: true},
		{Repository: "beta", Name: "doc-drift", Mode: config.RoutineModePR, Paused: true, RoutineGeneration: 3, Accepted: true, Served: true},
		{Repository: "alpha", Name: "doc-drift", Mode: config.RoutineModeDisabled, Accepted: true, Served: false},
		{Repository: "gamma", Name: "doc-drift", Mode: config.RoutineModePR, RoutineGeneration: 2, Accepted: false, Served: true, Reason: config.ReasonInvalid, Message: "bad workflow"},
		{Repository: "delta", Name: "doc-drift", Mode: config.RoutineModePR, Accepted: false, Served: false, Reason: v1alpha1.ReasonMissingRef, Message: "missing"},
	}
	in := Inputs{
		Config: cfg(config.Repository{Name: repo}),
		Outcomes: []configsource.Outcome{
			{
				Kind:               resources.KindRoutine,
				Name:               "doc-drift",
				ObservedGeneration: 4,
				ServedGeneration:   3,
				Accepted:           false,
				Reason:             v1alpha1.ReasonInvalid,
				Message:            "defaultPrompt must not be empty",
				RoutineConsumers:   consumers,
			},
		},
	}
	out := Desired(in, nil, nil, nil, []v1alpha1.Routine{routineObj("doc-drift", 4)}, now)
	st := out.Routines["doc-drift"]
	if st.ObservedGeneration != 4 {
		t.Fatalf("ObservedGeneration = %d, want 4", st.ObservedGeneration)
	}
	c := want(t, st.Conditions, v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonInvalid)
	if !strings.HasPrefix(c.Message, "serving generation 3; generation 4 refused") {
		t.Fatalf("Accepted message = %q", c.Message)
	}
	// ActiveCount counts served, non-disabled, non-paused consumers (zeta + gamma = 2);
	// PausedCount counts Paused (beta = 1); ConsumerCount is all 5.
	if st.ConsumerCount != 5 || st.ActiveCount != 2 || st.PausedCount != 1 {
		t.Fatalf("counts = (consumers=%d active=%d paused=%d), want (5, 2, 1)", st.ConsumerCount, st.ActiveCount, st.PausedCount)
	}
	if len(st.Consumers) != 5 || st.Consumers[0].Repository != "alpha" || st.Consumers[1].Repository != "beta" || !st.Consumers[1].Paused || st.Consumers[4].Repository != "zeta" {
		t.Fatalf("sorted consumers = %+v", st.Consumers)
	}
	if st.Consumers[2].ServedGeneration != 0 || st.Consumers[3].ServedGeneration != 2 {
		t.Fatalf("served generations = %+v", st.Consumers)
	}
}

func TestDesiredRoutineConsumersBounded(t *testing.T) {
	var consumers []resources.RoutineOutcome
	for i := range 65 {
		consumers = append(consumers, resources.RoutineOutcome{
			Repository:        fmt.Sprintf("r%02d", i),
			Name:              "doc-drift",
			Mode:              config.RoutineModePR,
			RoutineGeneration: 1,
			Accepted:          true,
			Served:            true,
		})
	}
	in := Inputs{
		Config: cfg(),
		Outcomes: []configsource.Outcome{
			{Kind: resources.KindRoutine, Name: "doc-drift", ObservedGeneration: 1, ServedGeneration: 1, Accepted: true, RoutineConsumers: consumers},
		},
	}
	st := Desired(in, nil, nil, nil, []v1alpha1.Routine{routineObj("doc-drift", 1)}, now).Routines["doc-drift"]
	if len(st.Consumers) != maxConsumers || st.ConsumerCount != 65 || st.ActiveCount != 65 {
		t.Fatalf("len=%d count=%d active=%d", len(st.Consumers), st.ConsumerCount, st.ActiveCount)
	}
}

func TestDesiredRepositoryRoutinesAndDeprecated(t *testing.T) {
	t.Run("RoutinesValid, Ready and bounded RoutineBindingStatus message", func(t *testing.T) {
		in := repoIn(cfg(config.Repository{Name: repo}))
		in.Outcomes[0].RoutineBindings = []resources.RoutineOutcome{
			{Repository: "r", Name: "doc-drift", Mode: config.RoutineModePR, RoutineGeneration: 2, Accepted: true, Served: true},
			{Repository: "r", Name: "dead-links", Mode: config.RoutineModePR, RoutineGeneration: 1, Accepted: false, Served: true, Reason: config.ReasonOverRoutine, Message: strings.Repeat("x", 1500)},
		}
		got := Desired(in, nil, []v1alpha1.Repository{repoObj("r", repo, 4)}, nil, nil, now).Repositories["r"]
		if len(got.Routines) != 2 || got.Routines[0].RoutineGeneration != 2 || len(got.Routines[1].Message) != 1024 {
			t.Fatalf("routines status = %+v", got.Routines)
		}
		c := want(t, got.Conditions, v1alpha1.ConditionRoutinesValid, metav1.ConditionFalse, "RoutinesRefused")
		if !strings.Contains(c.Message, "dead-links (OverRoutine)") {
			t.Fatalf("RoutinesValid message = %q", c.Message)
		}
		want(t, got.Conditions, ConditionReady, metav1.ConditionFalse, "RoutinesRefused")
	})

	t.Run("Deprecated combines revert binding and schedules without overwriting", func(t *testing.T) {
		in := repoIn(cfg(config.Repository{
			Name:      repo,
			Schedules: []config.Schedule{{Name: "weekly", Cron: "0 6 * * 1", Text: "check"}},
		}))
		in.Outcomes[0].Bindings = append(in.Outcomes[0].Bindings, resources.BindingOutcome{Repository: "r", Name: "revert", Mode: config.ModeShadow, Accepted: true, Served: true})
		r := repoObj("r", repo, 4)
		r.Spec.Schedules = []config.Schedule{{Name: "weekly", Cron: "0 6 * * 1", Text: "check"}}
		got := Desired(in, nil, []v1alpha1.Repository{r}, nil, nil, now).Repositories["r"]
		dep := want(t, got.Conditions, ConditionDeprecated, metav1.ConditionTrue, "Deprecated")
		if !strings.Contains(dep.Message, "revert") || !strings.Contains(dep.Message, "spec.schedules") {
			t.Fatalf("expected combined deprecation message, got %q", dep.Message)
		}
		want(t, got.Conditions, v1alpha1.ConditionRoutinesValid, metav1.ConditionTrue, "RoutinesValid")
	})

	t.Run("Deprecated set for schedules alone", func(t *testing.T) {
		in := repoIn(cfg(config.Repository{
			Name:      repo,
			Schedules: []config.Schedule{{Name: "weekly", Cron: "0 6 * * 1", Text: "check"}},
		}))
		got := Desired(in, nil, []v1alpha1.Repository{repoObj("r", repo, 4)}, nil, nil, now).Repositories["r"]
		dep := want(t, got.Conditions, ConditionDeprecated, metav1.ConditionTrue, "Deprecated")
		if !strings.Contains(dep.Message, "maku config migrate") {
			t.Fatalf("schedules deprecation message = %q", dep.Message)
		}
	})
}

func TestEnvtestWriteRoutineStatus(t *testing.T) {
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
	parsedCfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	repoCR := &v1alpha1.Repository{
		ObjectMeta: metav1.ObjectMeta{Name: "cnref", Namespace: envNS},
		Spec: v1alpha1.RepositorySpec{
			FactoryRef:   "aws-0",
			Repository:   "Smana/cloud-native-ref",
			Maintainers:  []string{"Smana"},
			TriggerLabel: "factory/ready",
			Routines:     []config.RoutineBinding{{Name: "doc-drift", Mode: config.RoutineModePR, Cron: "0 4 * * 1"}},
		},
	}
	rtCR := &v1alpha1.Routine{
		ObjectMeta: metav1.ObjectMeta{Name: "doc-drift", Namespace: envNS},
		Spec: v1alpha1.RoutineSpec{RoutineSpec: config.RoutineSpec{
			Workflow:      config.WorkflowStandard,
			DefaultPrompt: "Fix doc drift.",
			Probe:         config.ProbeSpec{Kind: config.ProbeNone},
		}},
	}
	for _, o := range []client.Object{repoCR, rtCR} {
		if err := c.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}

	rb := []resources.RoutineOutcome{
		{Repository: "cnref", Name: "doc-drift", Mode: config.RoutineModePR, RoutineGeneration: 1, Accepted: true, Served: true},
	}
	outcomes := []configsource.Outcome{
		{Kind: resources.KindRepository, Name: "cnref", Repository: "Smana/cloud-native-ref", ObservedGeneration: 1, ServedGeneration: 1, Accepted: true, RoutineBindings: rb},
		{Kind: resources.KindRoutine, Name: "doc-drift", ObservedGeneration: 1, ServedGeneration: 1, Accepted: true, RoutineConsumers: rb},
	}
	w := &Writer{Client: c, Reader: c, Namespace: envNS, Log: slog.New(slog.DiscardHandler),
		Inputs: func(context.Context) Inputs { return Inputs{Outcomes: outcomes, Config: parsedCfg, GitHubUp: true} }}
	if err := w.WriteOnce(ctx); err != nil {
		t.Fatal(err)
	}

	var gotRT v1alpha1.Routine
	if err := c.Get(ctx, client.ObjectKeyFromObject(rtCR), &gotRT); err != nil {
		t.Fatal(err)
	}
	if gotRT.Status.ConsumerCount != 1 || gotRT.Status.ActiveCount != 1 || gotRT.Status.PausedCount != 0 {
		t.Fatalf("routine status = %+v", gotRT.Status)
	}
	var gotRepo v1alpha1.Repository
	if err := c.Get(ctx, client.ObjectKeyFromObject(repoCR), &gotRepo); err != nil {
		t.Fatal(err)
	}
	if len(gotRepo.Status.Routines) != 1 || !gotRepo.Status.Routines[0].Accepted {
		t.Fatalf("repo routine status = %+v", gotRepo.Status.Routines)
	}
	if cnd := apimeta.FindStatusCondition(gotRepo.Status.Conditions, v1alpha1.ConditionRoutinesValid); cnd == nil || cnd.Status != metav1.ConditionTrue {
		t.Fatalf("RoutinesValid = %+v", cnd)
	}

	// Verify Routine print columns via Table request.
	kc, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatal(err)
	}
	body, err := kc.Discovery().RESTClient().Get().
		AbsPath("/apis/maku.ogenki.io/v1alpha1/namespaces", envNS, "routines", "doc-drift").
		SetHeader("Accept", "application/json;as=Table;g=meta.k8s.io;v=v1").DoRaw(ctx)
	if err != nil {
		t.Fatalf("routines table: %v: %s", err, body)
	}
	var tbl metav1.Table
	if err := json.Unmarshal(body, &tbl); err != nil || len(tbl.Rows) != 1 {
		t.Fatalf("table unmarshal: %v: %s", err, body)
	}
	cells := map[string]any{}
	for i, col := range tbl.ColumnDefinitions {
		cells[col.Name] = tbl.Rows[0].Cells[i]
	}
	if cells["Consumers"] != 1.0 || cells["Active"] != 1.0 || cells["Paused"] != 0.0 || cells["Accepted"] != "True" {
		t.Fatalf("routine printer cells = %v", cells)
	}

	// Second pass is a no-op (ResourceVersion unchanged).
	before := gotRT.ResourceVersion
	if err := w.WriteOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(rtCR), &gotRT); err != nil || gotRT.ResourceVersion != before {
		t.Fatalf("second pass wrote Routine: %s -> %s (%v)", before, gotRT.ResourceVersion, err)
	}
}
