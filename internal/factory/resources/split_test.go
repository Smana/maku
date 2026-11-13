// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

// anrLegacy is ai-native-ref's live objects in the per-repository shape and the residual config
// file they are served over.
func anrLegacy(t *testing.T) (residual []byte, objs []Object) {
	t.Helper()
	anr, err := os.ReadFile("testdata/anr.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, residual, err = Migrate(anr, "agent-system"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/anr-objects.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if objs, err = Decode(raw); err != nil {
		t.Fatal(err)
	}
	return residual, objs
}

func assembled(t *testing.T, residual []byte, objs []Object) *config.Config {
	t.Helper()
	r, err := Assemble(residual, objs)
	if err != nil || len(r.Refused) != 0 {
		t.Fatalf("%v %+v", err, r.Refused)
	}
	for _, b := range r.Bindings {
		if !b.Accepted {
			t.Fatalf("binding %+v", b)
		}
	}
	return r.Config
}

func object(objs []Object, kind Kind, name string) *Object {
	for i := range objs {
		if objs[i].Kind == kind && objs[i].Name == name {
			return &objs[i]
		}
	}
	return nil
}

func bindingOf(t *testing.T, objs []Object, repo, name string) config.Binding {
	t.Helper()
	r := object(objs, KindRepository, repo)
	var bs []config.Binding
	if err := decodeStrict(r.Spec["mergePolicies"], &bs); err != nil {
		t.Fatal(err)
	}
	for _, b := range bs {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("%s binds no %s", repo, name)
	return config.Binding{}
}

func policySpec(t *testing.T, objs []Object, name string) config.PolicySpec {
	t.Helper()
	var p config.PolicySpec
	if err := decodeStrict(object(objs, KindMergePolicy, name).Spec, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// B17, PR19: ai-native-ref's four repositories split into one policy per class and four bound
// Repositories, and every resolved class is what the legacy objects served.
func TestSplitANR(t *testing.T) {
	residual, objs := anrLegacy(t)
	out, err := Split(objs, "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	var policies []string
	repos := 0
	for _, o := range out {
		switch {
		case o.Kind == KindMergePolicy:
			policies = append(policies, o.Name)
			if legacyPolicy(o) {
				t.Fatalf("a legacy policy is left: %s", o.Name)
			}
		case o.Kind == KindRepository && o.Spec["merge"] != nil && o.Spec["mergePolicies"] != nil:
			repos++
		}
	}
	want := []string{"alert-rules", "dashboards", "dead-links", "deps-minor", "deps-patch", "docs", "docs-links", "kb-entries", "revert", "review", "scripts", "tests"}
	if !slices.Equal(policies, want) || repos != 4 || object(out, KindFactory, "aws-0") == nil {
		t.Fatalf("policies %v, %d bound repositories", policies, repos)
	}

	// The modes: cloud-native-ref's docs-links and revert live, the rest shadow or off.
	for _, tc := range []struct{ repo, name, mode string }{
		{"cloud-native-ref", "docs-links", config.ModeLive}, {"cloud-native-ref", "revert", config.ModeLive},
		{"ai-native-ref", "docs-links", config.ModeShadow}, {"cloud-native-ref", "deps-patch", config.ModeShadow},
		{"cloud-native-ref", "docs", config.ModeOff}, {"cloud-native-ref", "review", config.ModeOff}, {"runlore-kb", "kb-entries", config.ModeShadow},
	} {
		if b := bindingOf(t, out, tc.repo, tc.name); b.Mode != tc.mode {
			t.Errorf("%s %s: mode %q, want %q", tc.repo, tc.name, b.Mode, tc.mode)
		}
	}

	// docs-links: the holdouts differ by repository, so they are the bindings', and required.
	if p := policySpec(t, out, "docs-links"); !slices.Equal(p.Requires, []string{"holdoutChecks"}) || p.HoldoutChecks != nil {
		t.Fatalf("docs-links %+v", p)
	}
	if b := bindingOf(t, out, "runlore", "docs-links"); !slices.Equal(b.HoldoutChecks, []string{"build"}) {
		t.Fatalf("%+v", b)
	}

	// dead-links: both consumers set allowedFiles and the workflow, and share holdouts and denials.
	p := policySpec(t, out, "dead-links")
	if !slices.Equal(p.Requires, []string{"allowedFiles", "oracleWorkflow"}) || !slices.Equal(p.HoldoutChecks, []string{"External links 🔗"}) ||
		len(p.ProtectedFiles) != 2 || p.AllowedFiles != nil || p.Oracle == nil || p.Oracle.Workflow != "" || p.MaxPerDay != 1 || p.Live || p.Shadow {
		t.Fatalf("dead-links %+v", p)
	}
	if b := bindingOf(t, out, "ai-native-ref", "dead-links"); b.OracleWorkflow != "dead-links.yaml" || len(b.AllowedFiles) != 1 || b.HoldoutChecks != nil {
		t.Fatalf("%+v", b)
	}

	// deps-patch: the Flux source is the repository's.
	merge := object(out, KindRepository, "cloud-native-ref").Spec["merge"].(map[string]any)
	if merge["fluxSource"] != "flux-system/flux-system" {
		t.Fatalf("merge %+v", merge)
	}
	if p := policySpec(t, out, "deps-patch"); p.Monitor == nil || p.Monitor.Flux == nil || p.Monitor.Flux.Source != "" {
		t.Fatalf("deps-patch %+v", p.Monitor)
	}

	if d := Equal(assembled(t, residual, objs), assembled(t, residual, out)); len(d) != 0 {
		t.Fatalf("not equal:\n%s", strings.Join(d, "\n"))
	}
}

// Caps: the loosest consumer's on the policy, a lower one on the binding; no cap is the loosest.
func TestSplitCaps(t *testing.T) {
	cs := []consumer{
		{"a", config.Class{Shadow: true, MaxPerDay: 1, MaxModifiedLines: 20}},
		{"b", config.Class{Shadow: true, MaxPerDay: 3}},
	}
	p, bs, err := splitClass("c", cs)
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxPerDay != 3 || bs["a"].MaxPerDay != 1 || bs["b"].MaxPerDay != 0 {
		t.Fatalf("maxPerDay %+v %+v", p, bs)
	}
	if p.MaxModifiedLines != 0 || bs["a"].MaxModifiedLines != 20 {
		t.Fatalf("maxModifiedLines %+v %+v", p, bs)
	}
}

// Split refuses what it cannot convert without changing a meaning: two consumers declaring a class
// differently, and a repository watching two Flux sources (Q14).
func TestSplitRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(objs []Object)
		want string
	}{
		"two meanings of docs": {func(objs []Object) {
			object(objs, KindMergePolicy, "ai-native-ref").Spec["classes"].(map[string]any)["docs"] = map[string]any{}
		}, "class docs: Repository ai-native-ref and Repository cloud-native-ref declare it differently in checks"},
		"two Flux sources": {func(objs []Object) {
			c := object(objs, KindMergePolicy, "cloud-native-ref").Spec["classes"].(map[string]any)["deps-minor"].(map[string]any)
			c["monitor"].(map[string]any)["flux"].(map[string]any)["source"] = "flux-system/other"
		}, "Repository cloud-native-ref: its classes watch 2 Flux sources"},
	} {
		t.Run(name, func(t *testing.T) {
			_, objs := anrLegacy(t)
			tc.edit(objs)
			if _, err := Split(objs, "agent-system"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// PR10: guardrails cap every binding, where a legacy central class ran without them; Equal names
// each class they change.
func TestEqualNamesWhatTheGuardrailsChange(t *testing.T) {
	residual, objs := anrLegacy(t)
	object(objs, KindFactory, "aws-0").Spec["mergeGuardrails"] = map[string]any{"maxPerDay": 2}
	out, err := Split(objs, "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	before, err := Assemble(residual, objs)
	if err != nil || len(before.Refused) != 0 {
		t.Fatal(err, before.Refused)
	}
	after, err := Assemble(residual, out)
	if err != nil || len(after.Refused) != 0 {
		t.Fatal(err, after.Refused)
	}
	d := strings.Join(Equal(before.Config, after.Config), "\n")
	for _, want := range []string{
		"Smana/runlore-kb kb-entries: served before only",          // 5 a day, over the guardrail: refused
		"Smana/cloud-native-ref alert-rules: maxPerDay unset -> 2", // no cap: the guardrail's
	} {
		if !strings.Contains(d, want) {
			t.Errorf("missing %q in:\n%s", want, d)
		}
	}
}

// Split of what is already split is the same objects: a converted repository converts to itself.
func TestSplitOfTheBindingShapeIsANoOp(t *testing.T) {
	_, objs := anrLegacy(t)
	once, err := Split(objs, "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	twice, err := Split(once, "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := Encode(once)
	b, _ := Encode(twice)
	if string(a) != string(b) {
		t.Fatal("a second Split changed the objects")
	}
}

// A class policy already served under a class's name is kept when the converted one is the same
// definition, and refused when it is not: Split never replaces a shared policy.
func TestSplitRefusesACollidingPolicy(t *testing.T) {
	_, objs := anrLegacy(t)
	same := Object{Kind: KindMergePolicy, Name: "docs", Namespace: "agent-system", Spec: map[string]any{"checks": true}}
	out, err := Split(append(slices.Clone(objs), same), "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, o := range out {
		if o.Kind == KindMergePolicy && o.Name == "docs" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d docs policies", n)
	}
	other := Object{Kind: KindMergePolicy, Name: "docs", Namespace: "agent-system", Spec: map[string]any{"checks": true, "testFirst": true}}
	if _, err := Split(append(slices.Clone(objs), other), "agent-system"); err == nil || !strings.Contains(err.Error(), "MergePolicy docs exists with another definition") {
		t.Fatalf("got %v", err)
	}
}
