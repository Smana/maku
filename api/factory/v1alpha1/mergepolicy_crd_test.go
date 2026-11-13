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

// The spec's class-shape policies (reusable merge policies, "The model"), with requires per plan PR3.
const (
	deadLinksPolicy = `apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata: {name: dead-links}
spec:
  description: Retargets dead external links a detector found, and nothing else.
  requires: [allowedFiles, holdoutChecks, oracleWorkflow]
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
  requires: [holdoutChecks]
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
    autoMergesPerDay: 2
    leakScanCheck: lint
    approveToMerge: true
    breaker: {window: 10, maxReverts: 1, maxFollowUps: 1, recoverAfter: 3}
  mergePolicies:
    - {name: docs-links, mode: shadow, holdoutChecks: [validate]}
    - {name: revert,     mode: shadow, holdoutChecks: [validate]}
    - {name: docs,       mode: "off"}
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

// B1, B4, B7, B8: both shapes admitted in release N, each class-shape rule refusing.
func TestMergePolicyAdmission(t *testing.T) {
	a := startAdmission(t)
	raw, err := os.ReadFile("testdata/anr-mergepolicies.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		if err := a.create(t, doc); err != nil {
			t.Errorf("ai-native-ref's legacy MergePolicy refused: %v", err)
		}
	}
	reviewOK := "apiVersion: maku.ogenki.io/v1alpha1\nkind: MergePolicy\nmetadata: {name: review}\nspec: {description: d, harness: solo, checks: true, testFirst: true}\n"
	for name, m := range map[string]string{"dead-links": deadLinksPolicy, "docs-links": docsLinksPolicy, "review": reviewOK} {
		if err := a.create(t, m); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}

	for name, tc := range map[string]struct{ manifest, want string }{
		"a name that is no class": {edit(t, docsLinksPolicy, "docs.links", nil), msgPolicyName},
		"a name too long":         {edit(t, docsLinksPolicy, strings.Repeat("d", 41), nil), msgPolicyName},
		"legacy and class fields": {edit(t, docsLinksPolicy, "mixed", map[string]string{"spec.repositoryRef": "cloud-native-ref", "spec.maxPerDay": "1"}), msgNeverTogether},
		"classes without a ref":   {edit(t, docsLinksPolicy, "classes", map[string]string{"spec.classes": "{docs: {}}"}), msgLegacyNeedsRef},
		"live on a policy":        {edit(t, docsLinksPolicy, "live", map[string]string{"spec.live": "true"}), msgModeOnBinding},
		"a workflow on a policy":  {edit(t, deadLinksPolicy, "workflow", map[string]string{"spec.oracle.workflow": "dead-links.yaml"}), msgWorkflow},
		"a source on a policy":    {edit(t, deadLinksPolicy, "source", map[string]string{"spec.monitor.flux": "{source: flux-system/flux-system, within: 30m}"}), msgFluxSource},
		"requires a cap":          {edit(t, docsLinksPolicy, "requires", map[string]string{"spec.requires": "[maxPerDay]"}), `Unsupported value: "maxPerDay"`},
		"review with files":       {edit(t, reviewOK, "", map[string]string{"metadata.name": "review", "spec.allowedFiles": "['^docs/.+$']"}), msgReviewPolicy},
		"review with requires":    {edit(t, reviewOK, "", map[string]string{"metadata.name": "review", "spec.requires": "[holdoutChecks]"}), msgReviewPolicy},
		"review with a label":     {edit(t, reviewOK, "", map[string]string{"metadata.name": "review", "spec.requiredLabel": "x"}), msgReviewPolicy},
		"review with modifyOnly":  {edit(t, reviewOK, "", map[string]string{"metadata.name": "review", "spec.modifyOnly": "true"}), msgReviewPolicy},
	} {
		t.Run(name, func(t *testing.T) {
			_ = a.c.Delete(t.Context(), a.object(t, tc.manifest)) // review was admitted above
			err := a.create(t, tc.manifest)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}

	t.Run("a class policy keeps its shape", func(t *testing.T) {
		u := a.object(t, edit(t, docsLinksPolicy, "shape", nil))
		if err := a.c.Create(t.Context(), u); err != nil {
			t.Fatal(err)
		}
		_ = unstructured.SetNestedField(u.Object, "cloud-native-ref", "spec", "repositoryRef")
		unstructured.RemoveNestedField(u.Object, "spec", "description")
		unstructured.RemoveNestedField(u.Object, "spec", "requires")
		if err := a.c.Update(t.Context(), u); err == nil || !strings.Contains(err.Error(), msgKeepsShape) {
			t.Fatalf("got %v, want %q", err, msgKeepsShape)
		}
	})
}

// B2, B3, OD-8, MP15, Q8: bindings keyed by name, a closed mode, review off, the entry classes,
// and a Factory's merge defaults.
func TestRepositoryAdmission(t *testing.T) {
	a := startAdmission(t)
	if err := a.create(t, anrRepository); err != nil {
		t.Fatalf("the spec's ai-native-ref Repository refused: %v", err)
	}
	factory := "apiVersion: maku.ogenki.io/v1alpha1\nkind: Factory\nmetadata: {name: aws-0}\nspec: {mergeDefaults: {autoMergesPerDay: 2}}\n"
	if err := a.create(t, factory); err != nil {
		t.Fatalf("a Factory with mergeDefaults refused: %v", err)
	}

	var many []string
	for i := range 31 {
		many = append(many, fmt.Sprintf("{name: c%d, mode: \"off\"}", i))
	}
	for name, tc := range map[string]struct{ bindings, extra, want string }{
		"a name twice": {bindings: `[{name: docs-links, mode: "off"}, {name: revert, mode: "off"}, {name: docs, mode: "off"}, {name: docs, mode: shadow}]`, want: "Duplicate value"},
		"mode on":      {bindings: `[{name: docs-links, mode: 'on'}, {name: revert, mode: "off"}]`, want: `Unsupported value: "on"`},
		"no mode":      {bindings: `[{name: docs-links}, {name: revert, mode: "off"}]`, want: "Required value"},
		"review shadow": {bindings: `[{name: docs-links, mode: "off"}, {name: revert, mode: "off"}, {name: review, mode: shadow}]`,
			want: msgReviewBinding},
		"review with a parameter": {bindings: `[{name: docs-links, mode: "off"}, {name: revert, mode: "off"}, {name: review, mode: "off", maxPerDay: 1}]`,
			want: msgReviewBinding},
		"no revert": {bindings: `[{name: docs-links, mode: shadow}]`, want: msgEntryClasses},
		"file authority": {bindings: `[{name: docs-links, mode: "off"}, {name: revert, mode: "off"}]`, extra: "repository",
			want: msgFileBindings},
		"33 bindings":    {bindings: "[{name: docs-links, mode: \"off\"}, {name: revert, mode: \"off\"}, " + strings.Join(many, ", ") + "]", want: "must have at most 32 items"},
		"a negative cap": {bindings: `[{name: docs-links, mode: "off", maxPerDay: -1}, {name: revert, mode: "off"}]`, want: "should be greater than or equal to 0"},
		"a bad name":     {bindings: `[{name: docs-links, mode: "off"}, {name: revert, mode: "off"}, {name: Docs, mode: "off"}]`, want: "should match"},
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
