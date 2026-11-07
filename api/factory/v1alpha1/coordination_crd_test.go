// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"bytes"
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
	"sigs.k8s.io/yaml"
)

// coordinationFixtures is the one fixture set both this package's assertions and the reconciler's
// envtest read (MR2): one file per case, never a Go literal or a private copy.
const coordinationFixtures = "../../../internal/factory/reconciler/testdata/coordination"

// coordinationCase is one fixture file: a Task to create, and optionally the same Task edited.
type coordinationCase struct {
	Description string `json:"description"`
	// accept or refuse.
	Want string `json:"want"`
	// crd: the API server refuses it; admission: the CRD cannot see it (CEL reads only
	// metadata.name), so the reconciler's admission must.
	Layer   string `json:"layer"`
	Message string `json:"message,omitempty"`
	Task    Task   `json:"task"`
	Update  *Task  `json:"update,omitempty"`
}

func taskCRD(t *testing.T) *apiextensionsv1.JSONSchemaProps {
	t.Helper()
	raw, err := os.ReadFile("../../../config/crd/maku.ogenki.io_tasks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.UnmarshalStrict(raw, &crd); err != nil {
		t.Fatal(err)
	}
	return crd.Spec.Versions[0].Schema.OpenAPIV3Schema
}

func rules(s apiextensionsv1.JSONSchemaProps) map[string]string {
	m := map[string]string{}
	for _, v := range s.XValidations {
		m[v.Message] = v.Rule
	}
	return m
}

// MR2, MR16: the coordination kinds and their records are additive fields on Task, bounded, and
// the API server holds the cross-field invariants.
func TestCoordinationCRD(t *testing.T) {
	root := taskCRD(t)
	spec, status := root.Properties["spec"], root.Properties["status"]
	if k := spec.Properties["kind"]; !slices.Equal(enum(k), []string{"coordination", "integration"}) {
		t.Errorf("spec.kind enum = %v", enum(k))
	}
	if !slices.Contains(enum(spec.Properties["source"].Properties["kind"]), SourceKindCoordination) {
		t.Error("source.kind lacks coordination")
	}
	if !slices.Contains(enum(status.Properties["phase"]), PhaseCoordinating) {
		t.Error("status.phase lacks Coordinating")
	}
	for _, f := range []string{"coordination", "coordinationParent", "integration", "coordinationArtifacts"} {
		if _, ok := spec.Properties[f]; !ok {
			t.Errorf("spec.%s missing", f)
		}
	}
	for _, f := range []string{"coordination", "result"} {
		if _, ok := status.Properties[f]; !ok {
			t.Errorf("status.%s missing", f)
		}
	}
	// The bounds the plan pins (MR1, MR7, MR12).
	plan := status.Properties["coordination"].Properties["plan"]
	integ := plan.Properties["integration"]
	if p := integ.Properties["maxRuns"]; p.Minimum == nil || *p.Minimum != 1 || p.Maximum == nil || *p.Maximum != 16 {
		t.Errorf("integration.maxRuns bounds = %v..%v", p.Minimum, p.Maximum)
	}
	if !slices.Equal(enum(integ.Properties["role"]), []string{"triager"}) {
		t.Errorf("integration.role enum = %v", enum(integ.Properties["role"]))
	}
	if c := plan.Properties["children"]; c.MaxItems == nil || *c.MaxItems != 2 {
		t.Errorf("plan.children maxItems = %v", c.MaxItems)
	}
	art := plan.Properties["children"].Items.Schema.Properties["produces"].Items.Schema
	if !slices.Equal(enum(art.Properties["kind"]), []string{"file"}) || art.Properties["maxBytes"].Maximum == nil || *art.Properties["maxBytes"].Maximum != 65536 {
		t.Errorf("artifact kind/maxBytes = %v %v", enum(art.Properties["kind"]), art.Properties["maxBytes"].Maximum)
	}
	if v := plan.Properties["apiVersion"]; !slices.Equal(enum(v), []string{"maku.ogenki.io/v1alpha1"}) {
		t.Errorf("plan.apiVersion enum = %v", enum(v))
	}
	if v := plan.Properties["kind"]; !slices.Equal(enum(v), []string{"CoordinationPlan"}) {
		t.Errorf("plan.kind enum = %v", enum(v))
	}
	if v := plan.Properties["id"]; v.Pattern != `^[a-z2-7]{8}$` {
		t.Errorf("plan.id pattern = %q", v.Pattern)
	}
	if v := plan.Properties["version"]; v.Minimum == nil || *v.Minimum != 1 {
		t.Errorf("plan.version minimum = %v", v.Minimum)
	}
	landed := spec.Properties["coordinationArtifacts"].Items.Schema.Properties
	for field, want := range map[string]string{"mergeCommitSHA": `^[0-9a-f]{40}$`, "blobSHA": `^[0-9a-f]{40}$`, "contentSHA256": `^[0-9a-f]{64}$`} {
		if landed[field].Pattern != want {
			t.Errorf("landed %s pattern = %q", field, landed[field].Pattern)
		}
	}
	if s := spec.Properties["integration"].Properties["subjects"]; s.MaxItems == nil || *s.MaxItems != 2 {
		t.Errorf("integration.subjects maxItems = %v", s.MaxItems)
	}
	if !slices.Equal(enum(spec.Properties["integration"].Properties["purpose"]), []string{"beforeDependentMerge", "final"}) {
		t.Error("integration.purpose enum")
	}
	if !slices.Equal(enum(status.Properties["result"].Properties["state"]), []string{"pending", "passed", "failed"}) {
		t.Error("result.state enum")
	}
	// Bounded histories (the plan's retention caps).
	coord := status.Properties["coordination"].Properties
	for list, want := range map[string]int64{"history": 16, "confirmations": 16, "authorizations": 16, "integrationAttempts": 16,
		"incidents": 4, "pendingGenerations": 2} {
		if got := coord[list].MaxItems; got == nil || *got != want {
			t.Errorf("status.coordination.%s maxItems = %v, want %d", list, got, want)
		}
	}
	// Transition and cross-field rules, by message.
	specRules := rules(spec)
	for _, m := range []string{
		"spec.kind is fixed at creation",
		"spec.coordinationParent is fixed at creation",
		"spec.coordination is fixed at creation",
		"spec.integration is fixed at creation",
		"spec.coordinationArtifacts are fixed at creation",
		"a coordination or integration task, and only one, has source kind coordination (MR2)",
		"a coordination parent has no template, tier or implementation budget (MR2)",
		"a coordination child is never a splitter part: no spec.parent, no spec.split (MR2)",
		"a coordination child names its childID",
		"a coordination child is an issue task",
		"an integration task names its parent and no childID (MR10)",
		"a coordination family is public only (MR16)",
	} {
		if _, ok := specRules[m]; !ok {
			t.Errorf("spec lacks the rule %q", m)
		}
	}
	rootRules := rules(*root)
	for _, m := range []string{
		"a coordination parent has no run, pull request, next role or next trigger (MR2)",
		"an integration task has no implementation pull request (MR10)",
		"an integration task runs only triager (MR10, MR17)",
		"only a coordination parent is Coordinating",
		"status.coordination is a parent's record, or a child's lineage under its own childID",
		"status.result is an integration task's",
	} {
		if _, ok := rootRules[m]; !ok {
			t.Errorf("Task lacks the rule %q", m)
		}
	}
	// An omitempty field read in a transition rule needs has() on both objects (CEL).
	if r := specRules["spec.coordinationParent is fixed at creation"]; !strings.Contains(r, "has(oldSelf.coordinationParent)") {
		t.Errorf("coordinationParent rule %q", r)
	}
	all := messages(root, map[string]bool{})
	for _, s := range []string{"maxChildren is 2 in this release (MR1)", "publicOnly is true in this release (MR16)",
		"an integration subject names exactly one of headSHA or mergeCommitSHA", "beforeDependents: false needs a rationale",
		"primary.repository is one child repository (MR3)", "a child id is unique", "a repository holds one child (MR1)"} {
		if !all[s] {
			t.Errorf("CRD lacks the rule %q", s)
		}
	}
}

// messages is every CEL rule message anywhere under s.
func messages(s *apiextensionsv1.JSONSchemaProps, into map[string]bool) map[string]bool {
	for _, v := range s.XValidations {
		into[v.Message] = true
	}
	for _, p := range s.Properties {
		messages(&p, into)
	}
	if s.Items != nil && s.Items.Schema != nil {
		messages(s.Items.Schema, into)
	}
	return into
}

func enum(s apiextensionsv1.JSONSchemaProps) []string {
	var out []string
	for _, e := range s.Enum {
		var v string
		if err := json.Unmarshal(e.Raw, &v); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// The shared fixtures decode strictly into the types, carry a C2 name each, cover every case the
// plan names, and every CRD-layer refusal names a rule the generated CRD carries.
func TestCoordinationFixtures(t *testing.T) {
	root := taskCRD(t)
	all := messages(root, map[string]bool{})
	files, err := filepath.Glob(filepath.Join(coordinationFixtures, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	seen := map[string]string{}
	got := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		var c coordinationCase
		if err := dec.Decode(&c); err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		got[name] = true
		if prev, dup := seen[c.Task.Name]; dup {
			t.Errorf("%s and %s share the Task name %s", prev, name, c.Task.Name)
		}
		seen[c.Task.Name] = name
		switch {
		case c.Want == "accept" && (c.Layer != "crd" || c.Message != ""):
			t.Errorf("%s: an accepted case names no refusal", name)
		case c.Want == "refuse" && c.Message == "":
			t.Errorf("%s: a refusal names its message", name)
		case c.Want == "refuse" && c.Layer == "crd" && !all[c.Message]:
			t.Errorf("%s: the CRD carries no rule %q", name, c.Message)
		case c.Want != "accept" && c.Want != "refuse", c.Layer != "crd" && c.Layer != "admission":
			t.Errorf("%s: want %q layer %q", name, c.Want, c.Layer)
		}
		if c.Update != nil && c.Update.Name != c.Task.Name {
			t.Errorf("%s: an update edits the same Task", name)
		}
	}
	for _, want := range []string{"valid-legacy", "valid-parent", "valid-child", "valid-child-dependent", "valid-integration",
		"parent-template", "parent-tier", "parent-implementation-budget", "parent-next-role", "parent-next-trigger",
		"parent-pull-request", "parent-runs", "parent-source-api", "parent-max-children-3", "parent-public-only-false",
		"child-spec-parent", "child-spec-split", "child-splitter-label", "child-no-child-id", "child-non-issue-source",
		"integration-child-id", "integration-pull-request", "integration-subject-both-shas", "integration-subject-no-sha",
		"integration-implementer-run", "integration-mutable-parent", "integration-mutable-subjects"} {
		if !got[want] {
			t.Errorf("fixture %s missing", want)
		}
	}
}

// MR2: a coordination plan is data on a Task, not a resource: no new Kind registers.
func TestCoordinationPlanIsNoKind(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for gvk := range s.AllKnownTypes() {
		if gvk.Group == GroupVersion.Group && strings.HasPrefix(gvk.Kind, "Coordination") {
			t.Errorf("%s registers as a Kind", gvk.Kind)
		}
	}
	// The plan's own apiVersion/kind are data fields, decoded as such.
	p := CoordinationPlan{APIVersion: CoordinationPlanAPIVersion, Kind: CoordinationPlanKind}
	if p.APIVersion != "maku.ogenki.io/v1alpha1" || p.Kind != "CoordinationPlan" {
		t.Errorf("%+v", p)
	}
}

// A nil BeforeDependents is true: the dependent merge waits for integration by default (MR10).
func TestIntegrationPlanBeforeDependentsDefaultsTrue(t *testing.T) {
	f := false
	for _, c := range []struct {
		p    IntegrationPlan
		want bool
	}{{IntegrationPlan{}, true}, {IntegrationPlan{BeforeDependents: new(true)}, true}, {IntegrationPlan{BeforeDependents: &f}, false}} {
		if got := c.p.EffectiveBeforeDependents(); got != c.want {
			t.Errorf("%v: %v", c.p.BeforeDependents, got)
		}
	}
}

// The bounded histories keep the newest records, oldest dropped first, deterministically.
func TestAppendNewest(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC))
	var h []CoordinationIncident
	for i := range 6 {
		h = AppendNewest(h, CoordinationIncident{ChildID: string(rune('a' + i)), Reason: "reverted", At: at}, MaxCoordinationIncidents)
	}
	if len(h) != 4 || h[0].ChildID != "c" || h[3].ChildID != "f" {
		t.Errorf("%+v", h)
	}
	if got := AppendNewest([]int{1}, 2, 0); len(got) != 0 {
		t.Errorf("a zero cap keeps nothing: %v", got)
	}
}
