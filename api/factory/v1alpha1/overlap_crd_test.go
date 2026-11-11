// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

func TestOverlapCRDSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../config/crd/maku.ogenki.io_tasks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.UnmarshalStrict(raw, &crd); err != nil {
		t.Fatal(err)
	}
	status := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"]
	overlap := status.Properties["overlap"]
	if overlap.Type != "object" || !slices.Equal(overlap.Required, []string{"fingerprint"}) {
		t.Fatalf("status.overlap = %+v", overlap)
	}

	fingerprint := overlap.Properties["fingerprint"]
	if fingerprint.Pattern != `^[a-f0-9]{64}$` || fingerprint.MaxLength == nil || *fingerprint.MaxLength != 64 {
		t.Errorf("fingerprint = %+v, want exactly 64 lowercase hexadecimal characters", fingerprint)
	}
	requireOverlapEnum(t, "classification", overlap.Properties["classification"], []string{
		"active_duplicate", "completed_duplicate", "prior_attempt", "partial_overlap", "unverified_overlap", "no_match",
	})
	matches := overlap.Properties["matches"]
	if matches.MaxItems == nil || *matches.MaxItems != 20 {
		t.Errorf("matches.maxItems = %v, want 20", matches.MaxItems)
	}
	match := matches.Items.Schema
	requireOverlapEnum(t, "relation", match.Properties["relation"], []string{"exact", "lexical"})
	for _, field := range []string{"task", "run"} {
		p := match.Properties[field]
		if p.Pattern != `^[a-z2-7]{8}$` || p.MaxLength == nil || *p.MaxLength != 8 {
			t.Errorf("matches.%s = %+v, want a C2 id", field, p)
		}
	}
	for _, field := range []string{"issue", "pr"} {
		p := match.Properties[field]
		if p.Minimum == nil || *p.Minimum != 1 {
			t.Errorf("matches.%s.minimum = %v, want 1", field, p.Minimum)
		}
	}
	if note := overlap.Properties["note"]; note.MaxLength == nil || *note.MaxLength != 512 {
		t.Errorf("note.maxLength = %v, want 512", note.MaxLength)
	}
	if scoperRun := overlap.Properties["scoperRun"]; scoperRun.Pattern != `^[a-z2-7]{8}$` || scoperRun.MaxLength == nil || *scoperRun.MaxLength != 8 {
		t.Errorf("scoperRun = %+v, want the evaluation run's C2 id", scoperRun)
	}
	sources := overlap.Properties["sources"]
	if sources.MaxItems == nil || *sources.MaxItems != 4 {
		t.Errorf("sources.maxItems = %v, want 4", sources.MaxItems)
	}
	requireOverlapEnum(t, "sources", *sources.Items.Schema, []string{"tasks", "runs", "rooms", "github"})
	if len(overlap.XValidations) != 3 {
		t.Errorf("overlap transition rules = %v, want pending pairing, final immutability and pending-only scoperRun advancement", overlap.XValidations)
	}
}

func requireOverlapEnum(t *testing.T, field string, schema apiextensionsv1.JSONSchemaProps, want []string) {
	t.Helper()
	got := make([]string, len(schema.Enum))
	for i, value := range schema.Enum {
		if err := json.Unmarshal(value.Raw, &got[i]); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s enum = %v, want %v", field, got, want)
	}
}

func TestOverlapDeepCopyDoesNotAlias(t *testing.T) {
	checkedAt := metav1.NewTime(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	original := &Task{Status: TaskStatus{Overlap: &OverlapStatus{
		Fingerprint:    strings.Repeat("a", 64),
		Classification: "completed_duplicate",
		Matches:        []OverlapMatch{{Task: "task2345", Run: "runr2345", Issue: 12, PR: 34, Relation: "exact", Phase: PhaseDone}},
		Sources:        []string{"tasks", "runs"},
		ScoperRun:      "eval2345",
		CheckedAt:      &checkedAt,
		Note:           "original",
	}}}
	copied := original.DeepCopy()
	copied.Status.Overlap.Matches[0].Task = "othr2345"
	copied.Status.Overlap.Sources[0] = "github"
	copied.Status.Overlap.CheckedAt.Time = checkedAt.Add(time.Hour)
	if got := original.Status.Overlap.Matches[0].Task; got != "task2345" {
		t.Errorf("matches alias the original: %q", got)
	}
	if got := original.Status.Overlap.Sources[0]; got != "tasks" {
		t.Errorf("sources alias the original: %q", got)
	}
	if got := original.Status.Overlap.CheckedAt.Time; !got.Equal(checkedAt.Time) {
		t.Errorf("checkedAt aliases the original: %v", got)
	}
}

func TestOverlapPhase(t *testing.T) {
	for _, tc := range []struct {
		phase          string
		classification string
		active         bool
	}{
		{phase: PhaseReceived, classification: "active_duplicate", active: true},
		{phase: PhaseTriaged, classification: "active_duplicate", active: true},
		{phase: PhaseQueued, classification: "active_duplicate", active: true},
		{phase: PhaseImplementing, classification: "active_duplicate", active: true},
		{phase: PhaseReviewing, classification: "active_duplicate", active: true},
		{phase: PhaseAwaitingCI, classification: "active_duplicate", active: true},
		{phase: PhaseAutoMerging, classification: "active_duplicate", active: true},
		{phase: PhaseVerifying, classification: "active_duplicate", active: true},
		{phase: PhaseCoordinating, classification: "active_duplicate", active: true},
		{phase: PhaseAwaitingHuman, classification: "active_duplicate", active: true},
		{phase: PhaseMerged, classification: "active_duplicate", active: true},
		{phase: PhaseDone, classification: "completed_duplicate"},
		{phase: PhaseNoOp, classification: "completed_duplicate"},
		{phase: PhaseRejected, classification: "prior_attempt"},
		{phase: PhaseReverted, classification: "prior_attempt"},
		{phase: PhaseEscalated, classification: "prior_attempt"},
		{phase: PhaseClosed, classification: "prior_attempt"},
		{phase: PhaseStopped, classification: "prior_attempt"},
		{phase: PhaseSplit, classification: "partial_overlap"},
		{phase: "", classification: "unverified_overlap"},
		{phase: "FuturePhase", classification: "unverified_overlap"},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			classification, active := OverlapPhase(tc.phase)
			if classification != tc.classification || active != tc.active {
				t.Errorf("OverlapPhase(%q) = %q, %v; want %q, %v", tc.phase, classification, active, tc.classification, tc.active)
			}
		})
	}
}

func TestEnvtestOverlapTransitions(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	restCfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	create := func(t *testing.T, name string, overlap *OverlapStatus) *Task {
		t.Helper()
		task := &Task{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: TaskSpec{
				Source:     Source{Kind: "issue", Ref: "Smana/maku#1", Key: "key", RequestedBy: "github:Smana", Trust: "untrusted", ContentSHA256: strings.Repeat("b", 64)},
				Repository: "Smana/maku", Text: "overlap transition fixture", DataClass: "public",
			},
			Status: TaskStatus{Phase: PhaseReceived, Overlap: overlap},
		}
		if err := c.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		// The status subresource ignores status on create.
		task.Status = TaskStatus{Phase: PhaseReceived, Overlap: overlap}
		if err := c.Status().Update(ctx, task); err != nil {
			t.Fatalf("initial overlap status refused: %v", err)
		}
		return task
	}

	t.Run("pending pairs classification and checkedAt", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			class string
			at    *metav1.Time
		}{
			{name: "ovlpair2", class: "no_match"},
			{name: "ovlpair3", at: overlapCheckedAt()},
		} {
			task := create(t, tc.name, &OverlapStatus{Fingerprint: strings.Repeat("a", 64)})
			task.Status.Overlap.Classification = tc.class
			task.Status.Overlap.CheckedAt = tc.at
			if err := c.Status().Update(ctx, task); err == nil {
				t.Error("classification and checkedAt were accepted separately")
			}
		}
	})

	t.Run("scoperRun advances while pending then freezes", func(t *testing.T) {
		task := create(t, "ovlpend2", &OverlapStatus{Fingerprint: strings.Repeat("a", 64)})
		task.Status.Overlap.ScoperRun = "eval2345"
		if err := c.Status().Update(ctx, task); err != nil {
			t.Fatalf("pending scoperRun refused: %v", err)
		}
		if task.Status.Overlap.ScoperRun != "eval2345" {
			t.Fatalf("scoperRun = %q, want the evaluation run's C2 id", task.Status.Overlap.ScoperRun)
		}
		task.Status.Overlap.Classification = "no_match"
		task.Status.Overlap.CheckedAt = overlapCheckedAt()
		if err := c.Status().Update(ctx, task); err != nil {
			t.Fatalf("final classification refused: %v", err)
		}
		task.Status.Overlap.ScoperRun = "eval6789"
		if err := c.Status().Update(ctx, task); err == nil {
			t.Error("scoperRun advanced after classification became final")
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*OverlapStatus)
	}{
		{name: "ovlclas2", mutate: func(o *OverlapStatus) { o.Classification = "prior_attempt" }},
		{name: "ovlmtch2", mutate: func(o *OverlapStatus) { o.Matches[0].Task = "othr2345" }},
		{name: "ovlsrcs2", mutate: func(o *OverlapStatus) { o.Sources[0] = "github" }},
		{name: "ovlnote2", mutate: func(o *OverlapStatus) { o.Note = "changed" }},
		{name: "ovlscpr2", mutate: func(o *OverlapStatus) { o.ScoperRun = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := create(t, tc.name, finalOverlapStatus())
			tc.mutate(task.Status.Overlap)
			if err := c.Status().Update(ctx, task); err == nil {
				t.Error("a final overlap record changed")
			}
		})
	}

	t.Run("unset optional old fields do not reject later status updates", func(t *testing.T) {
		task := create(t, "ovlmini2", &OverlapStatus{
			Fingerprint: strings.Repeat("a", 64), Classification: "no_match", CheckedAt: overlapCheckedAt(),
		})
		task.Status.Reason = "evaluation recorded"
		if err := c.Status().Update(ctx, task); err != nil {
			t.Fatalf("an update with optional oldSelf fields unset was refused: %v", err)
		}
		task.Status.Overlap = nil
		if err := c.Status().Update(ctx, task); err == nil {
			t.Error("status.overlap was removed")
		}
	})
}

func overlapCheckedAt() *metav1.Time {
	at := metav1.NewTime(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	return &at
}

func finalOverlapStatus() *OverlapStatus {
	return &OverlapStatus{
		Fingerprint:    strings.Repeat("a", 64),
		Classification: "completed_duplicate",
		Matches:        []OverlapMatch{{Task: "task2345", Run: "runr2345", Issue: 12, PR: 34, Relation: "exact", Phase: PhaseDone}},
		Sources:        []string{"tasks", "runs"},
		ScoperRun:      "eval2345",
		CheckedAt:      overlapCheckedAt(),
		Note:           "original",
	}
}
