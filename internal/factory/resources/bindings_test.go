// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// y reads a YAML snippet as a raw spec.
func y(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// classPolicy is a class-shape MergePolicy.
func classPolicy(t *testing.T, name string, gen int64, spec string) Object {
	t.Helper()
	return Object{Kind: KindMergePolicy, Name: name, Generation: gen, Spec: y(t, spec)}
}

// The spec's policies (with requires per PR3), and the two built-ins every bound repository binds.
func specPolicies(t *testing.T) []Object {
	return []Object{
		classPolicy(t, "dead-links", 3, `
description: Retargets dead external links a detector found, and nothing else.
requires: [allowedFiles, holdoutChecks, oracleWorkflow]
modifyOnly: true
maxModifiedLines: 20
diffShape: links-only
protectedFiles: ['^docs/(superpowers|specs)/']
oracle: {probe: dead-links, maxFindings: 10}
monitor: {oracleRuns: 2, followUpWindow: 168h}
maxPerDay: 1
`),
		classPolicy(t, "docs-links", 1, "description: Fixes a broken link in the docs, and nothing else.\nrequires: [holdoutChecks]\n"),
		classPolicy(t, "revert", 1, "requires: [holdoutChecks]\n"),
		classPolicy(t, "docs", 1, "checks: true\n"),
	}
}

// boundRepo is a Repository holding its merge and its bindings.
func boundRepo(t *testing.T, name, repository string, gen int64, bindings string) Object {
	t.Helper()
	o := repo(name, repository)
	o.Generation = gen
	o.Spec["merge"] = mergeBlock(t)
	o.Spec["mergePolicies"] = y(t, "list:\n"+bindings)["list"]
	return o
}

// anrBindings are the spec's ai-native-ref bindings.
const anrBindings = `
- {name: docs-links, mode: shadow, holdoutChecks: [validate]}
- {name: revert, mode: shadow, holdoutChecks: [validate]}
- {name: docs, mode: "off"}
- name: dead-links
  mode: shadow
  allowedFiles: ['^(docs|website/content)/.+\.md$']
  holdoutChecks: ['External links 🔗']
  oracleWorkflow: dead-links.yaml
  protectedFiles: ['^website/content/docs/decisions/']
`

func binding(r Result, repository, name string) *BindingOutcome {
	for i, b := range r.Bindings {
		if b.Repository == repository && b.Name == name {
			return &r.Bindings[i]
		}
	}
	return nil
}

// B4: the spec's policies and Repository serve the class cloud-native-ref declares today.
func TestBindingsResolve(t *testing.T) {
	objs := append(specPolicies(t), boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 2, anrBindings))
	r, err := Assemble(file(t), objs)
	if err != nil {
		t.Fatal(err, r.Refused)
	}
	if len(r.Refused) != 0 {
		t.Fatalf("refused: %+v", r.Refused)
	}
	var want config.Class
	if err := yaml.UnmarshalStrict([]byte(`
description: Retargets dead external links a detector found, and nothing else.
shadow: true
allowedFiles: ['^(docs|website/content)/.+\.md$']
protectedFiles: ['^docs/(superpowers|specs)/', '^website/content/docs/decisions/']
modifyOnly: true
maxModifiedLines: 20
diffShape: links-only
holdoutChecks: ["External links 🔗"]
oracle: {probe: dead-links, workflow: dead-links.yaml, maxFindings: 10}
monitor: {oracleRuns: 2, followUpWindow: 168h}
maxPerDay: 1
`), &want); err != nil {
		t.Fatal(err)
	}
	rc := r.Config.RepositoryFor("Smana/ai-native-ref")
	if rc == nil {
		t.Fatal("Smana/ai-native-ref not served")
	}
	if got := rc.Classes["dead-links"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("dead-links:\n got %+v\nwant %+v", got, want)
	}
	if len(rc.Classes) != 4 || !rc.Classes["docs-links"].Shadow || rc.Classes["docs"].Live || rc.Classes["docs"].Shadow || !rc.Classes["docs"].Checks {
		t.Fatalf("classes %+v", rc.Classes)
	}
	if strings.Join(rc.Bound, " ") != "docs-links revert docs dead-links" || len(rc.Bindings) != 4 || rc.Merge.MergerLogin == "" {
		t.Fatalf("bound %v, bindings %d, merge %+v", rc.Bound, len(rc.Bindings), rc.Merge)
	}
	b := binding(r, "ai-native-ref", "dead-links")
	if b == nil || !b.Accepted || !b.Served || b.PolicyGeneration != 3 || b.Mode != config.ModeShadow {
		t.Fatalf("outcome %+v", b)
	}
	if len(r.Bindings) != 4 || len(r.Accepted) != 5 {
		t.Fatalf("%d outcomes, %d accepted, want 4 and 5 (four policies, one Repository)", len(r.Bindings), len(r.Accepted))
	}
	if p, ok := r.Config.Policies["dead-links"]; !ok || p.Generation != 3 || len(r.Config.Policies) != 4 {
		t.Fatalf("policies %+v", r.Config.Policies)
	}
	if rc := r.Resolved[BindingKey{"ai-native-ref", "dead-links"}]; !reflect.DeepEqual(rc.Class, want) || rc.PolicyGeneration != 3 {
		t.Fatalf("resolved %+v", rc)
	}
}

// B9: dead-links at generation 4 watches Flux. ai-native-ref has no fluxSource, so its binding
// alone is dropped (or keeps its last resolved class); runlore's serves generation 4.
func TestBindingIsolation(t *testing.T) {
	objs := specPolicies(t)
	objs[0].Generation = 4
	objs[0].Spec["monitor"] = y(t, "{oracleRuns: 2, followUpWindow: 168h, flux: {within: 30m}}")
	anr := boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 2, anrBindings)
	runlore := boundRepo(t, "runlore", "Smana/runlore", 1, anrBindings)
	runlore.Spec["merge"].(map[string]any)["fluxSource"] = "flux-system/flux-system"
	objs = append(objs, anr, runlore)

	r, err := Assemble(file(t), objs)
	if err != nil {
		t.Fatal(err)
	}
	b := binding(r, "ai-native-ref", "dead-links")
	if b == nil || b.Accepted || b.Served || b.Reason != config.ReasonMissingParameter || !strings.Contains(b.Message, "fluxSource") {
		t.Fatalf("ai-native-ref's dead-links: %+v", b)
	}
	rc := r.Config.RepositoryFor("Smana/ai-native-ref")
	if _, ok := rc.Classes["dead-links"]; ok || len(rc.Classes) != 3 {
		t.Fatalf("the dropped class must be absent, the others served: %v", rc.Classes)
	}
	if refused(r, KindRepository, "ai-native-ref") != "" || refused(r, KindMergePolicy, "dead-links") != "" {
		t.Fatalf("one binding never refuses its Repository or the policy: %+v", r.Refused)
	}
	if b := binding(r, "runlore", "dead-links"); b == nil || !b.Accepted || b.PolicyGeneration != 4 {
		t.Fatalf("runlore's dead-links: %+v", b)
	}
	if c := r.Config.RepositoryFor("Smana/runlore").Classes["dead-links"]; c.Monitor == nil || c.Monitor.Flux == nil || c.Monitor.Flux.Source != "flux-system/flux-system" {
		t.Fatalf("runlore's dead-links monitor: %+v", c.Monitor)
	}

	// The last resolved class is put back while the factory runs (D5).
	last := config.Class{Shadow: true, Description: "generation 3"}
	r, err = Assemble(file(t), objs, Options{LastResolved: map[BindingKey]ResolvedClass{
		{"ai-native-ref", "dead-links"}: {Class: last, PolicyGeneration: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	b = binding(r, "ai-native-ref", "dead-links")
	if b == nil || b.Accepted || !b.Served || b.PolicyGeneration != 3 || b.Reason != config.ReasonMissingParameter {
		t.Fatalf("with a last resolved class: %+v", b)
	}
	if got := r.Config.RepositoryFor("Smana/ai-native-ref").Classes["dead-links"]; got.Description != "generation 3" {
		t.Fatalf("served %+v, want the last resolved class", got)
	}
	if _, ok := r.Resolved[BindingKey{"ai-native-ref", "dead-links"}]; ok {
		t.Fatal("a class put back is not newly resolved")
	}
}

func TestBindingMissingRef(t *testing.T) {
	bad := classPolicy(t, "docs", 2, "live: true\n") // refused alone: the mode is the binding's
	for name, objs := range map[string][]Object{
		"absent":  specPolicies(t)[:3],
		"refused": append(specPolicies(t)[:3], bad),
	} {
		t.Run(name, func(t *testing.T) {
			r, err := Assemble(file(t), append(objs, boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 1, anrBindings)))
			if err != nil {
				t.Fatal(err)
			}
			b := binding(r, "ai-native-ref", "docs")
			if b == nil || b.Accepted || b.Served || b.Reason != v1alpha1.ReasonMissingRef {
				t.Fatalf("docs: %+v", b)
			}
			if refused(r, KindRepository, "ai-native-ref") != "" || r.Config.RepositoryFor("Smana/ai-native-ref") == nil {
				t.Fatalf("the Repository stays accepted: %+v", r.Refused)
			}
			if name == "refused" && refused(r, KindMergePolicy, "docs") != v1alpha1.ReasonInvalid {
				t.Fatalf("the policy is refused alone: %+v", r.Refused)
			}
		})
	}
}

// PR9: two adopted policies of one shape whose logins and branch prefixes overlap: the later binding
// in list order is refused, naming both.
func TestBindingAmbiguousAdoption(t *testing.T) {
	adopt := func(name, shape, login, branch string) Object {
		return classPolicy(t, name, 1, `
diffShape: `+shape+`
modifyOnly: true
allowedFiles: ['^renovate\.json$']
author: {adopt: {logins: ["`+login+`"], branches: ['`+branch+`']}}
`)
	}
	base := specPolicies(t)[1:3]
	for _, tc := range []struct {
		name, order, shapeB, refused string
	}{
		{"same shape", "a b", "semver-patch", "deps-b"},
		{"same shape, swapped", "b a", "semver-patch", "deps-a"},
		{"different shapes", "a b", "semver-minor", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := adopt("deps-a", "semver-patch", "renovate[bot]", "^renovate/")
			b := adopt("deps-b", tc.shapeB, "Renovate[bot]", "^renovate/deps-")
			bindings := "- {name: docs-links, mode: shadow, holdoutChecks: [validate]}\n- {name: revert, mode: shadow, holdoutChecks: [validate]}\n"
			for _, n := range strings.Fields(tc.order) {
				bindings += "- {name: deps-" + n + `, mode: "off"}` + "\n"
			}
			r, err := Assemble(file(t), append(slicesClone(base), a, b, boundRepo(t, "maku", "Smana/maku", 1, bindings)))
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range []string{"deps-a", "deps-b"} {
				o := binding(r, "maku", n)
				switch {
				case n == tc.refused && (o.Reason != config.ReasonAmbiguousAdoption || o.Served ||
					!strings.Contains(o.Message, "deps-a") || !strings.Contains(o.Message, "deps-b")):
					t.Fatalf("%s: %+v", n, o)
				case n != tc.refused && !o.Accepted:
					t.Fatalf("%s: %+v", n, o)
				}
			}
		})
	}
}

func slicesClone(o []Object) []Object { return append([]Object(nil), o...) }

// PR8: a Repository that adds bindings while a per-repository MergePolicy names it is refused, and
// so is that MergePolicy; with the Repository's last good put back, the legacy pair serves.
func TestBindingDefinedTwice(t *testing.T) {
	bound := boundRepo(t, "maku", "Smana/maku", 2, anrBindings)
	objs := append(specPolicies(t), bound, policy(t, "maku-legacy", "maku"))
	r, err := Assemble(file(t), objs)
	if err != nil {
		t.Fatal(err)
	}
	if refused(r, KindRepository, "maku") != v1alpha1.ReasonDefinedTwice || refused(r, KindMergePolicy, "maku-legacy") != v1alpha1.ReasonDefinedTwice {
		t.Fatalf("both must be refused DefinedTwice: %+v", r.Refused)
	}
	objs[len(objs)-2] = repo("maku", "Smana/maku")
	r, err = Assemble(file(t), objs)
	if err != nil {
		t.Fatal(err)
	}
	if rc := r.Config.RepositoryFor("Smana/maku"); rc == nil || len(rc.Bound) != 0 || len(r.Refused) != 0 {
		t.Fatalf("the legacy pair must serve: %+v", r.Refused)
	}
}

// B14: the guardrails cap every binding.
func TestBindingGuardrails(t *testing.T) {
	f := strings.Replace(string(file(t)), "evaluation:", "mergeGuardrails: {maxPerDay: 1}\nevaluation:", 1)
	r, err := Assemble([]byte(f), append(specPolicies(t), boundRepo(t, "maku", "Smana/maku", 1, `
- {name: docs-links, mode: shadow, holdoutChecks: [validate], maxPerDay: 2}
- {name: revert, mode: shadow, holdoutChecks: [validate]}
`)))
	if err != nil {
		t.Fatal(err)
	}
	if b := binding(r, "maku", "docs-links"); b.Reason != config.ReasonGuardrailMismatch {
		t.Fatalf("docs-links: %+v", b)
	}
	if c := r.Config.RepositoryFor("Smana/maku").Classes["revert"]; c.MaxPerDay != 1 {
		t.Fatalf("an unset cap inherits the guardrail's: %d", c.MaxPerDay)
	}
}

// B17, B8: ai-native-ref's live objects, all in the per-repository shape, assemble to the config
// main served before bindings existed, each legacy MergePolicy with its warning.
func TestLegacyObjectsUnchanged(t *testing.T) {
	anr, err := os.ReadFile("testdata/anr.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, residual, err := Migrate(anr, "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/anr-objects.yaml")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Assemble(residual, objs)
	if err != nil || len(r.Refused) != 0 {
		t.Fatal(err, r.Refused)
	}
	got, err := json.MarshalIndent(struct {
		Hash   string
		Config any
	}{r.Config.Hash, r.Config}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/anr-legacy-config.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Fatal("the legacy objects no longer assemble to main's config: testdata/anr-legacy-config.json")
	}
	var n int
	for _, w := range r.Warnings {
		if strings.Contains(w, "is in the per-repository shape, removed in the next minor: maku config migrate") {
			n++
		}
	}
	if n != 4 || len(r.Bindings) != 0 {
		t.Fatalf("%d legacy warnings, want 4: %v", n, r.Warnings)
	}
}

// B9: a refused policy never refuses a Repository. Its last good generation, put back by the
// caller, keeps serving every consumer; without one, each binding is MissingRef.
func TestABadPolicyNeverRefusesARepository(t *testing.T) {
	objs := append(specPolicies(t), boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 1, anrBindings))
	good := objs[0]
	bad := classPolicy(t, "dead-links", 4, "live: true\noracle: {probe: dead-links, maxFindings: 10}\n")
	objs[0] = bad
	r, err := Assemble(file(t), objs)
	if err != nil {
		t.Fatal(err)
	}
	if refused(r, KindMergePolicy, "dead-links") != v1alpha1.ReasonInvalid || refused(r, KindRepository, "ai-native-ref") != "" {
		t.Fatalf("refused %+v", r.Refused)
	}
	if b := binding(r, "ai-native-ref", "dead-links"); b.Reason != v1alpha1.ReasonMissingRef {
		t.Fatalf("no last good: %+v", b)
	}
	objs[0] = good // the caller's D5
	r, err = Assemble(file(t), objs)
	if err != nil {
		t.Fatal(err)
	}
	if b := binding(r, "ai-native-ref", "dead-links"); !b.Accepted || b.PolicyGeneration != 3 {
		t.Fatalf("the last good generation serves: %+v", b)
	}
}

// B9, B14 (review of #522): a last resolved class put back is checked again under the current
// catalogue and merge. One that no longer holds is dropped, never handed to Parse, where it would
// refuse the whole Repository.
func TestPutBackClassIsRechecked(t *testing.T) {
	// docs is absent: its binding is MissingRef, so its last class is put back.
	for _, tc := range []struct {
		name   string
		file   func(string) string
		merge  func(map[string]any)
		last   config.Class
		served bool
		check  func(*testing.T, config.Class)
	}{
		{name: "a harness profile the Factory removed", last: config.Class{Harness: "removed"}},
		{name: "testFirst once contractCheck is gone", last: config.Class{Checks: true, TestFirst: true}},
		{name: "live without allowedFiles once policyBot is off", last: config.Class{Live: true, HoldoutChecks: []string{"validate"}},
			merge: func(m map[string]any) { m["policyBot"] = false; m["gateStatus"] = true }},
		{name: "a cap over a tightened guardrail", last: config.Class{Shadow: true, MaxPerDay: 2},
			file: func(f string) string {
				return strings.Replace(f, "evaluation:", "mergeGuardrails: {maxPerDay: 1}\nevaluation:", 1)
			}},
		{name: "an unset cap inherits the guardrail", last: config.Class{Shadow: true}, served: true,
			file: func(f string) string {
				return strings.Replace(f, "evaluation:", "mergeGuardrails: {maxPerDay: 1}\nevaluation:", 1)
			},
			check: func(t *testing.T, c config.Class) {
				if c.MaxPerDay != 1 {
					t.Fatalf("maxPerDay %d, want the guardrail's 1", c.MaxPerDay)
				}
			}},
		{name: "a class that still holds", last: config.Class{Shadow: true}, served: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := string(file(t))
			if tc.file != nil {
				f = tc.file(f)
			}
			r := boundRepo(t, "maku", "Smana/maku", 1, anrBindings)
			if tc.merge != nil {
				tc.merge(r.Spec["merge"].(map[string]any))
			}
			res, err := Assemble([]byte(f), append(specPolicies(t)[:3:3], r), Options{LastResolved: map[BindingKey]ResolvedClass{
				{"maku", "docs"}: {Class: tc.last, PolicyGeneration: 1}}})
			if err != nil {
				t.Fatal(err, res.Refused)
			}
			if refused(res, KindRepository, "maku") != "" {
				t.Fatalf("a stale last class refused the Repository: %+v", res.Refused)
			}
			b := binding(res, "maku", "docs")
			c, ok := res.Config.RepositoryFor("Smana/maku").Classes["docs"]
			if b.Served != tc.served || ok != tc.served || b.Reason != v1alpha1.ReasonMissingRef {
				t.Fatalf("served %v (class present %v), want %v: %+v", b.Served, ok, tc.served, b)
			}
			if !tc.served && !strings.Contains(b.Message, "last resolved class no longer holds") {
				t.Fatalf("message %q must say why the last class is not served", b.Message)
			}
			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}

// A refused policy's message does not claim nothing was served when a last class is put back.
func TestRefusedPolicyMessageWithAPutBack(t *testing.T) {
	bad := classPolicy(t, "docs", 2, "live: true\n")
	r, err := Assemble(file(t), append(specPolicies(t)[:3:3], bad, boundRepo(t, "maku", "Smana/maku", 1, anrBindings)),
		Options{LastResolved: map[BindingKey]ResolvedClass{{"maku", "docs"}: {Class: config.Class{Shadow: true}, PolicyGeneration: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if b := binding(r, "maku", "docs"); !b.Served || strings.Contains(b.Message, "none was served") {
		t.Fatalf("outcome %+v", b)
	}
}

// bound and bindings are Assemble's alone: the config file's own entries may not carry them.
func TestFileEntriesCannotCarryBindings(t *testing.T) {
	for _, k := range []string{"bound: [docs-links, revert]", "bindings: [{name: docs-links, mode: shadow}]"} {
		f := strings.Replace(string(file(t)), "    triggerLabel: factory/ready\n", "    triggerLabel: factory/ready\n    "+k+"\n", 1)
		if _, err := Assemble([]byte(f), nil); err == nil || !strings.Contains(err.Error(), "set by the factory from Repository objects") {
			t.Errorf("%s in the file: %v", k, err)
		}
	}
}

// TE2's floor reads the catalogue's templates: a policy whose variant is below its floor is
// refused alone (B9), never handed to Parse, where it would refuse each consumer Repository.
func TestAPolicyBelowItsFloorIsRefusedAlone(t *testing.T) {
	below := classPolicy(t, "docs", 2, "harness: rich\nharnessAllowed: [base]\n")
	r, err := Assemble(file(t), append(specPolicies(t)[:3:3], below, boundRepo(t, "maku", "Smana/maku", 1, anrBindings)))
	if err != nil {
		t.Fatal(err)
	}
	if x := refusal(r, KindMergePolicy, "docs"); x == nil || x.Reason != v1alpha1.ReasonInvalid || !strings.Contains(x.Message, "drops roles of the floor") {
		t.Fatalf("docs: %+v", x)
	}
	if refused(r, KindRepository, "maku") != "" || r.Config.RepositoryFor("Smana/maku") == nil {
		t.Fatalf("the consumer stays served: %+v", r.Refused)
	}
}

// The floor check reads the templates as Parse validates them: Parse drops the deprecated tester
// role only after Validate, so a floor whose template still lists tester is judged with it in both
// places. Stripping it before the policy check would accept a policy that Parse then refuses in
// every consumer Repository.
func TestFloorReadsTemplatesAsParseValidates(t *testing.T) {
	f := strings.Replace(string(file(t)), "  rich: {template: pair,", "  tested: {template: trio}\n  rich: {template: pair,", 1)
	// Parse, on a class of the file's own repository: trio's tester counts at validation.
	legacy := strings.Replace(f, "docs: {harness: base, harnessAllowed: [rich], escalation: {notConverged: rich}}", "docs: {harness: tested, harnessAllowed: [rich]}", 1)
	_, perr := config.Parse([]byte(legacy))
	if perr == nil || !strings.Contains(perr.Error(), `harnessAllowed "rich" drops roles of the floor`) {
		t.Fatalf("Parse: %v", perr)
	}
	// The policy check agrees.
	r, err := Assemble([]byte(f), append(specPolicies(t)[:3:3], classPolicy(t, "docs", 1, "harness: tested\nharnessAllowed: [rich]\n"),
		boundRepo(t, "maku", "Smana/maku", 1, anrBindings)))
	if err != nil {
		t.Fatal(err)
	}
	if x := refusal(r, KindMergePolicy, "docs"); x == nil || !strings.Contains(x.Message, `harnessAllowed "rich" drops roles of the floor`) {
		t.Fatalf("docs: %+v", x)
	}
	if refused(r, KindRepository, "maku") != "" {
		t.Fatalf("the consumer stays served: %+v", r.Refused)
	}
}
