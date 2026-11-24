// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// The spec's class-shape policies (reusable merge policies, "The model").
const (
	deadLinksPolicy = `apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata: {name: dead-links}
spec:
  description: Retargets dead external links a detector found, and nothing else.
  modifyOnly: true
  maxModifiedLines: 20
  diffShape: links-only
  protectedFiles: ['^docs/(superpowers|specs)/']
  oracle: {probe: dead-links, maxFindings: 10}
  monitor: {oracleRuns: 2, followUpWindow: 168h}
  maxPerDay: 1
`
	docsLinksPolicy = `apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata: {name: docs-links}
spec:
  description: Fixes a broken link in the docs, and nothing else.
`
	anrRepository = `apiVersion: maku.ogenki.io/v1alpha1
kind: Repository
metadata: {name: ai-native-ref}
spec:
  factoryRef: aws-0
  repository: Smana/ai-native-ref
  maintainers: [Smana]
  classAuthority: central
  merge:
    requiredChecks: [validate, lint]
    verifyChecks: [validate, lint]
    policyBotLogin: ogenki-merge-gate[bot]
    mergerLogin: ogenki-agent-merger[bot]
    maxPerDay: 2
    leakScanCheck: lint
    approveToMerge: true
    breaker: {window: 10, maxReverts: 1, maxFollowUps: 1, recoverAfter: 3}
  mergePolicies:
    - {name: docs-links, mode: shadow, holdoutChecks: [validate]}
    - {name: revert,     mode: shadow, holdoutChecks: [validate]}
    - {name: docs,       mode: disabled}
    - name: dead-links
      mode: shadow
      allowedFiles: ['^(docs|website/content)/.+\.md$']
      holdoutChecks: ['External links 🔗']
      oracleWorkflow: dead-links.yaml
      protectedFiles: ['^website/content/docs/decisions/']
`
)

// admission is an API server with the factory CRDs, and a create that reads one YAML object.
type admission struct {
	c  client.Client
	ns string
}

func startAdmission(t *testing.T) *admission {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	restCfg, err := env.Start() // a CEL rule over the cost budget fails here
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(restCfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return &admission{c: c, ns: "default"}
}

func (a *admission) object(t *testing.T, manifest string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(manifest), &u.Object); err != nil {
		t.Fatal(err)
	}
	u.SetNamespace(a.ns)
	return u
}

func (a *admission) create(t *testing.T, manifest string) error {
	t.Helper()
	return a.c.Create(t.Context(), a.object(t, manifest))
}

// edit sets one key path in a manifest: a dotted path into the spec, and its YAML value.
func edit(t *testing.T, manifest, name string, set map[string]string) string {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(manifest), &m); err != nil {
		t.Fatal(err)
	}
	if name != "" {
		m["metadata"].(map[string]any)["name"] = name
	}
	for path, value := range set {
		var v any
		if err := yaml.Unmarshal([]byte(value), &v); err != nil {
			t.Fatal(err)
		}
		at := m
		keys := strings.Split(path, ".")
		for _, k := range keys[:len(keys)-1] {
			next, ok := at[k].(map[string]any)
			if !ok {
				next = map[string]any{}
				at[k] = next
			}
			at = next
		}
		if v == nil {
			delete(at, keys[len(keys)-1])
		} else {
			at[keys[len(keys)-1]] = v
		}
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// B1, B7, MR9: the class shape admitted, each of its rules refusing.
func TestMergePolicyAdmission(t *testing.T) {
	a := startAdmission(t)
	for name, m := range map[string]string{"dead-links": deadLinksPolicy, "docs-links": docsLinksPolicy} {
		if err := a.create(t, m); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}

	for name, tc := range map[string]struct{ manifest, want string }{
		"a name that is no class":   {edit(t, docsLinksPolicy, "docs.links", nil), msgPolicyName},
		"a name too long":           {edit(t, docsLinksPolicy, strings.Repeat("d", 41), nil), msgPolicyName},
		"a workflow on a policy":    {edit(t, deadLinksPolicy, "workflow", map[string]string{"spec.oracle.workflow": "dead-links.yaml"}), msgWorkflow},
		"a source on a policy":      {edit(t, deadLinksPolicy, "source", map[string]string{"spec.monitor.flux": "{source: flux-system/flux-system, within: 30m}"}), msgFluxSource},
		"narrowedFiles on a policy": {edit(t, docsLinksPolicy, "narrowed", map[string]string{"spec.narrowedFiles": "['^docs/.+$']"}), msgNarrowed},
		"an evidence level unknown": {edit(t, docsLinksPolicy, "evidence", map[string]string{"spec.evidence": "high"}), `Unsupported value: "high"`},
		"review":                    {"apiVersion: maku.ogenki.io/v1alpha1\nkind: MergePolicy\nmetadata: {name: review}\nspec: {}\n", msgReviewPolicy},
	} {
		t.Run(name, func(t *testing.T) {
			err := a.create(t, tc.manifest)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// B8, plan PR20: the per-repository fields are gone from the schema. A legacy object created under
// release N's CRD reads back, after the upgrade, as an empty class policy: the API server prunes
// what the schema no longer declares, on read as on write.
func TestLegacyMergePolicyReadsBackEmptyAfterTheUpgrade(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("testdata", "mergepolicies-release-n.yaml")}, ErrorIfCRDPathMissing: true}
	restCfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(restCfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := &admission{c: c, ns: "default"}
	raw, err := os.ReadFile("testdata/anr-mergepolicies.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		u := a.object(t, doc)
		if err := c.Create(t.Context(), u); err != nil {
			t.Fatalf("release N refused ai-native-ref's legacy MergePolicy: %v", err)
		}
		names = append(names, u.GetName())
	}

	if _, err := envtest.InstallCRDs(restCfg, envtest.CRDInstallOptions{
		Paths: []string{filepath.Join("..", "..", "..", "config", "crd", "maku.ogenki.io_mergepolicies.yaml")}, ErrorIfPathMissing: true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion(GroupVersion.String())
		u.SetKind("MergePolicy")
		if err := c.Get(t.Context(), client.ObjectKey{Namespace: a.ns, Name: name}, u); err != nil {
			t.Fatal(err)
		}
		spec, _, _ := unstructured.NestedMap(u.Object, "spec")
		if len(spec) != 1 || spec["evidence"] != "standard" {
			t.Errorf("MergePolicy %s reads back with spec %v, want only the default evidence level", name, spec)
		}
	}
}

// B2, B3, MP15, MR9, Q8: bindings keyed by name, a closed mode (live, shadow, disabled), review
// refused, and a Factory's merge defaults.
func TestRepositoryAdmission(t *testing.T) {
	a := startAdmission(t)
	if err := a.create(t, anrRepository); err != nil {
		t.Fatalf("the spec's ai-native-ref Repository refused: %v", err)
	}
	factory := "apiVersion: maku.ogenki.io/v1alpha1\nkind: Factory\nmetadata: {name: aws-0}\nspec: {mergeDefaults: {maxPerDay: 2}}\n"
	if err := a.create(t, factory); err != nil {
		t.Fatalf("a Factory with mergeDefaults refused: %v", err)
	}

	// MR9: disabled is a mode for catalog policies.
	disabled := edit(t, anrRepository, "r-disabled", map[string]string{"spec.mergePolicies": `[{name: docs-links, mode: disabled}, {name: revert, mode: disabled}]`})
	if err := a.create(t, disabled); err != nil {
		t.Fatalf("mode disabled refused: %v", err)
	}

	var many []string
	for i := range 31 {
		many = append(many, fmt.Sprintf("{name: c%d, mode: disabled}", i))
	}
	for name, tc := range map[string]struct{ bindings, extra, want string }{
		"a name twice":     {bindings: `[{name: docs-links, mode: disabled}, {name: revert, mode: disabled}, {name: docs, mode: disabled}, {name: docs, mode: shadow}]`, want: "Duplicate value"},
		"mode on":          {bindings: `[{name: docs-links, mode: 'on'}, {name: revert, mode: disabled}]`, want: `Unsupported value: "on"`},
		"mode off":         {bindings: `[{name: docs-links, mode: "off"}, {name: revert, mode: disabled}]`, want: `Unsupported value: "off"`},
		"no mode":          {bindings: `[{name: docs-links}, {name: revert, mode: disabled}]`, want: "Required value"},
		"review shadow":    {bindings: `[{name: docs-links, mode: disabled}, {name: revert, mode: disabled}, {name: review, mode: shadow}]`, want: msgReviewBinding},
		"review disabled":  {bindings: `[{name: docs-links, mode: disabled}, {name: revert, mode: disabled}, {name: review, mode: disabled}]`, want: msgReviewBinding},
		"file authority":   {bindings: `[{name: docs-links, mode: disabled}, {name: revert, mode: disabled}]`, extra: "repository", want: msgFileBindings},
		"33 bindings":      {bindings: "[{name: docs-links, mode: disabled}, {name: revert, mode: disabled}, " + strings.Join(many, ", ") + "]", want: "must have at most 32 items"},
		"a negative cap":   {bindings: `[{name: docs-links, mode: disabled, maxPerDay: -1}, {name: revert, mode: disabled}]`, want: "should be greater than or equal to 0"},
		"unanchored files": {bindings: `[{name: docs-links, mode: disabled, allowedFiles: ['docs/.+']}, {name: revert, mode: disabled}]`, want: "should match"},
		"a bad name":       {bindings: `[{name: docs-links, mode: disabled}, {name: revert, mode: disabled}, {name: Docs, mode: disabled}]`, want: "should match"},
	} {
		t.Run(name, func(t *testing.T) {
			set := map[string]string{"spec.mergePolicies": tc.bindings}
			if tc.extra != "" {
				set["spec.classAuthority"] = tc.extra
			}
			m := edit(t, anrRepository, "r-"+strings.ReplaceAll(name, " ", "-"), set)
			err := a.create(t, m)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// v4 D3, step 7 plan IN9: privateCode.acknowledged is true or absent; an explicit false says
// nothing and is refused.
func TestRepositoryPrivateCodeAdmission(t *testing.T) {
	a := startAdmission(t)
	for name, tc := range map[string]struct{ privateCode, want string }{
		"acknowledged": {privateCode: "{acknowledged: true}"},
		"false":        {privateCode: "{acknowledged: false}", want: msgPrivateCode},
		"empty":        {privateCode: "{}", want: "Required value"},
	} {
		t.Run(name, func(t *testing.T) {
			m := edit(t, anrRepository, "pc-"+name, map[string]string{"spec.privateCode": tc.privateCode})
			err := a.create(t, m)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// WF3, WF7: a Repository names its default workflow and opts in to the tester; the name is bounded.
func TestRepositoryWorkflowAdmission(t *testing.T) {
	a := startAdmission(t)
	for name, tc := range map[string]struct{ workflow, want string }{
		"careful":         {workflow: "{default: careful, testFirst: true}"},
		"a name too long": {workflow: "{default: " + strings.Repeat("x", 300) + "}", want: "Too long"},
	} {
		t.Run(name, func(t *testing.T) {
			m := edit(t, anrRepository, "wf-"+strings.ReplaceAll(name, " ", "-"), map[string]string{"spec.workflow": tc.workflow})
			err := a.create(t, m)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// WF9, WF13: a Factory still spelling its workflows harnessProfiles is refused, strictly, as a field
// the CRD no longer declares.
func TestFactoryHarnessProfilesRefused(t *testing.T) {
	a := startAdmission(t)
	u := a.object(t, "apiVersion: maku.ogenki.io/v1alpha1\nkind: Factory\nmetadata: {name: hp, namespace: "+a.ns+"}\nspec: {harnessProfiles: {a: {template: pair}}}\n")
	if err := a.c.Create(t.Context(), u, client.FieldValidation("Strict")); err == nil || !strings.Contains(err.Error(), "harnessProfiles") {
		t.Fatalf("got %v", err)
	}
}

// MR9, MR13: every field release N deprecated on MergePolicy, Repository and Factory is gone from
// the CRD schema or refused by its enum/CEL rules.
func TestRemovedFieldsAreRefused(t *testing.T) {
	a := startAdmission(t)
	for _, field := range []struct{ key, val string }{
		{"harness", "standard"},
		{"harnessAllowed", "[careful]"},
		{"escalation", "{notConverged: careful}"},
		{"testFirst", "true"},
		{"checks", "true"},
		{"requires", "[allowedFiles]"},
		{"live", "true"},
		{"shadow", "true"},
	} {
		t.Run("MergePolicy."+field.key, func(t *testing.T) {
			u := a.object(t, fmt.Sprintf("apiVersion: maku.ogenki.io/v1alpha1\nkind: MergePolicy\nmetadata: {name: p-%s}\nspec: {%s: %s}\n",
				strings.ToLower(field.key), field.key, field.val))
			err := a.c.Create(t.Context(), u, client.FieldValidation("Strict"))
			if err == nil || !strings.Contains(err.Error(), field.key) || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("MergePolicy with %s: got %v, want strict schema refusal", field.key, err)
			}
		})
	}
	t.Run("MergePolicy.review", func(t *testing.T) {
		u := a.object(t, "apiVersion: maku.ogenki.io/v1alpha1\nkind: MergePolicy\nmetadata: {name: review}\nspec: {}\n")
		if err := a.c.Create(t.Context(), u, client.FieldValidation("Strict")); err == nil || !strings.Contains(err.Error(), "review is not a policy") {
			t.Fatalf("MergePolicy review: got %v", err)
		}
	})
	t.Run("Repository.mode.off", func(t *testing.T) {
		u := a.object(t, "apiVersion: maku.ogenki.io/v1alpha1\nkind: Repository\nmetadata: {name: r-off}\nspec:\n  factoryRef: aws-0\n  repository: Smana/x\n  mergePolicies: [{name: docs, mode: \"off\"}]\n")
		if err := a.c.Create(t.Context(), u, client.FieldValidation("Strict")); err == nil || !strings.Contains(err.Error(), `Unsupported value: "off"`) {
			t.Fatalf("Repository mode off: got %v", err)
		}
	})
	t.Run("Repository.autoMergesPerDay", func(t *testing.T) {
		u := a.object(t, "apiVersion: maku.ogenki.io/v1alpha1\nkind: Repository\nmetadata: {name: r-ampd}\nspec:\n  factoryRef: aws-0\n  repository: Smana/x\n  merge: {autoMergesPerDay: 2}\n")
		if err := a.c.Create(t.Context(), u, client.FieldValidation("Strict")); err == nil || !strings.Contains(err.Error(), "autoMergesPerDay") {
			t.Fatalf("Repository autoMergesPerDay: got %v", err)
		}
	})
	t.Run("Factory.minPromotionEvidence", func(t *testing.T) {
		u := a.object(t, "apiVersion: maku.ogenki.io/v1alpha1\nkind: Factory\nmetadata: {name: f-mpe}\nspec:\n  mergeGuardrails: {minPromotionEvidence: 10}\n")
		if err := a.c.Create(t.Context(), u, client.FieldValidation("Strict")); err == nil || !strings.Contains(err.Error(), "minPromotionEvidence") {
			t.Fatalf("Factory minPromotionEvidence: got %v", err)
		}
	})
}

// ai-native-ref's live objects (testdata/anr-live-objects.yaml, copied from its main) pass this
// CRD's admission strictly: the binding-shape Repositories with partial merge blocks over the
// Factory's mergeDefaults, durations as written, and the twelve class policies. Strict field
// validation means nothing the live objects say is pruned.
func TestTheLiveANRObjectsAreAdmitted(t *testing.T) {
	a := startAdmission(t)
	a.ns = "agent-system"
	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(a.ns)
	if err := a.c.Create(t.Context(), ns); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/anr-live-objects.yaml")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		u := a.object(t, doc)
		if u.GetKind() == "" {
			continue
		}
		if err := a.c.Create(t.Context(), u, client.FieldValidation("Strict")); err != nil {
			t.Errorf("%s %s refused: %v", u.GetKind(), u.GetName(), err)
		}
		kinds[u.GetKind()]++
	}
	if kinds["Factory"] != 1 || kinds["Repository"] != 4 || kinds["MergePolicy"] != 8 {
		t.Fatalf("objects %v, want 1 Factory, 4 Repositories, 8 MergePolicies", kinds)
	}
}

func TestMergePolicyFieldAdmission(t *testing.T) {
	a := startAdmission(t)
	for name, tc := range map[string]struct {
		set  map[string]string
		want string
	}{
		"diffShape nonsense": {set: map[string]string{"spec.diffShape": "nonsense"}, want: "Unsupported value"},
		"negative cap":       {set: map[string]string{"spec.maxPerDay": "-1"}, want: "should be greater than or equal to 0"},
		"unanchored files":   {set: map[string]string{"spec.allowedFiles": `["docs/.+"]`}, want: "should match"},
		"low without proof":  {set: map[string]string{"spec.evidence": "low"}, want: "evidence: low needs diffShape or oracle"},
	} {
		t.Run(name, func(t *testing.T) {
			m := edit(t, docsLinksPolicy, strings.ReplaceAll(name, " ", "-"), tc.set)
			if err := a.create(t, m); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
	if err := a.create(t, docsLinksPolicy); err != nil {
		t.Fatalf("a policy with no evidence level refused: %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetAPIVersion(GroupVersion.String())
	got.SetKind("MergePolicy")
	if err := a.c.Get(t.Context(), client.ObjectKey{Namespace: a.ns, Name: "docs-links"}, got); err != nil {
		t.Fatal(err)
	}
	evidence, _, _ := unstructured.NestedString(got.Object, "spec", "evidence")
	if evidence != "standard" {
		t.Fatalf("evidence = %q, want standard", evidence)
	}
}
