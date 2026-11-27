// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const validRoutineManifest = `apiVersion: maku.ogenki.io/v1alpha1
kind: Routine
metadata: {name: dead-code}
spec:
  description: Remove unreachable code reported by CI.
  workflow: quick
  dataClass: public
  defaultPrompt: Remove one unreachable helper and verify checks pass.
  probe:
    kind: actions-findings
    requireConsecutive: 2
    maxFindings: 1
  requires: [probeWorkflow]
  maxOpenPRs: 1
  maxCostUSD: 2.0
  cooldown: 24h
  breaker:
    window: 10
    minSamples: 5
    minMergeRate: 0.25
    maxConsecutiveClosed: 3
    maxConsecutiveFailures: 3
  protectedPaths: ['^\.github/agent-factory/']
`

func TestAddToSchemeRegistersRoutineKinds(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Routine", "RoutineList"} {
		if !s.Recognizes(GroupVersion.WithKind(kind)) {
			t.Errorf("%s is not registered", kind)
		}
	}
	if LabelRoutine != "agents.ogenki.io/routine" {
		t.Errorf("LabelRoutine = %q", LabelRoutine)
	}
	if ConditionRoutinesValid != "RoutinesValid" {
		t.Errorf("ConditionRoutinesValid = %q", ConditionRoutinesValid)
	}
	if SourceKindRoutine != "routine" {
		t.Errorf("SourceKindRoutine = %q", SourceKindRoutine)
	}
}

func TestRoutineCRDBoundsEveryStringListAndMap(t *testing.T) {
	_, crd := readCRD(t, "routines")
	v := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	spec, status := v.Properties["spec"], v.Properties["status"]
	bounded(t, "routines.spec", &spec)
	bounded(t, "routines.status", &status)
}

func TestRoutineAdmission(t *testing.T) {
	a := startAdmission(t)
	if err := a.create(t, validRoutineManifest); err != nil {
		t.Fatalf("valid Routine refused: %v", err)
	}

	for name, tc := range map[string]struct{ manifest, want string }{
		"uppercase name":      {edit(t, validRoutineManifest, "DeadCode", nil), "Routine's name must match"},
		"name over 40 chars":  {edit(t, validRoutineManifest, strings.Repeat("a", 41), nil), "Routine's name must match"},
		"maxOpenPRs zero":     {edit(t, validRoutineManifest, "r-zero", map[string]string{"spec.maxOpenPRs": "0"}), "greater than or equal to 1"},
		"maxOpenPRs six":      {edit(t, validRoutineManifest, "r-six", map[string]string{"spec.maxOpenPRs": "6"}), "less than or equal to 5"},
		"maxFindings over 16": {edit(t, validRoutineManifest, "r-mf", map[string]string{"spec.probe.maxFindings": "17"}), "less than or equal to 16"},
		"unknown probe kind":  {edit(t, validRoutineManifest, "r-probe", map[string]string{"spec.probe.kind": "custom"}), `Unsupported value: "custom"`},
	} {
		t.Run(name, func(t *testing.T) {
			err := a.create(t, tc.manifest)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRepositoryRoutinesAdmission(t *testing.T) {
	a := startAdmission(t)
	validRepo := edit(t, anrRepository, "repo-routines", map[string]string{
		"spec.routines": `[{name: dead-links, mode: pr, cron: "0 5 * * *", oracleClass: dead-links}, {name: doc-drift, mode: propose, cron: "0 4 * * 1", repoPrompt: true, skill: docs-audit}]`,
	})
	if err := a.create(t, validRepo); err != nil {
		t.Fatalf("Repository with routines refused: %v", err)
	}

	var many []string
	for i := range 17 {
		many = append(many, fmt.Sprintf("{name: r%d, mode: disabled}", i))
	}
	for name, tc := range map[string]struct{ routines, want string }{
		"duplicate binding name": {routines: `[{name: dead-links, mode: pr}, {name: dead-links, mode: disabled}]`, want: "Duplicate value"},
		"unknown mode":           {routines: `[{name: dead-links, mode: auto}]`, want: `Unsupported value: "auto"`},
		"missing mode":           {routines: `[{name: dead-links}]`, want: "Required value"},
		"17 routines":            {routines: "[" + strings.Join(many, ", ") + "]", want: "must have at most 16 items"},
	} {
		t.Run(name, func(t *testing.T) {
			m := edit(t, anrRepository, "repo-rt-"+strings.ReplaceAll(name, " ", "-"), map[string]string{"spec.routines": tc.routines})
			err := a.create(t, m)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTaskRoutineAdmission(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	restCfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(restCfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	ns := "default"
	sum := strings.Repeat("a", 64)
	gitSHA := strings.Repeat("b", 40)

	makeTask := func(name, kind string, rt *TaskRoutine, maxMicroUSD int64) *Task {
		return &Task{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: TaskSpec{
				Source: Source{
					Kind:          kind,
					Ref:           "dead-code",
					Key:           "routine:Smana/maku:dead-code:2026-11-26T03:00Z",
					RequestedBy:   "system:scheduler",
					Trust:         "trusted",
					ContentSHA256: sum,
				},
				Repository:  "Smana/maku",
				Text:        "Run dead-code routine.",
				DataClass:   "public",
				MaxMicroUSD: maxMicroUSD,
				Routine:     rt,
			},
		}
	}

	validRT := &TaskRoutine{
		Name:              "dead-code",
		Mode:              "pr",
		RoutineGeneration: 1,
		PromptSource:      "routine",
		PromptSHA:         sum,
		PromptPath:        ".github/agent-factory/upkeep/dead-code.md",
		Skill:             "code-audit",
		SkillSHA:          gitSHA,
		FindingIDs:        []string{"deadcode:a.go:Unused"},
	}
	tk := makeTask("rtaaaaaa", SourceKindRoutine, validRT, 2_000_000)
	if err := c.Create(t.Context(), tk); err != nil {
		t.Fatalf("valid routine Task refused: %v", err)
	}

	// spec.routine is immutable after creation.
	mutated := tk.DeepCopy()
	mutated.Spec.Routine.Mode = "draft"
	if err := c.Update(t.Context(), mutated); err == nil || !strings.Contains(err.Error(), "spec.routine is fixed at creation") {
		t.Fatalf("mutating spec.routine got %v", err)
	}

	// budget exceeding maxMicroUSD on routine task is refused.
	overBudget := tk.DeepCopy()
	overBudget.Spec.Budget.TaskMicroUSD = 2_000_001
	if err := c.Update(t.Context(), overBudget); err == nil || !strings.Contains(err.Error(), "a routine task's budget stays under its ceiling") {
		t.Fatalf("over-budget routine task got %v", err)
	}

	for name, tc := range map[string]struct {
		task *Task
		want string
	}{
		"routine task without maxMicroUSD": {
			task: makeTask("rtaaaaab", SourceKindRoutine, validRT, 0),
			want: "a routine task carries maxMicroUSD",
		},
		"routine task without spec.routine": {
			task: makeTask("rtaaaaac", SourceKindRoutine, nil, 2_000_000),
			want: "a routine task, and only one, has spec.routine",
		},
		"issue task with spec.routine": {
			task: makeTask("rtaaaaad", "issue", validRT, 0),
			want: "a routine task, and only one, has spec.routine",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := c.Create(t.Context(), tc.task)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
