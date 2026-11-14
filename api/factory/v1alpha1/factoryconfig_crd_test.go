// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
)

func readCRD(t *testing.T, plural string) (string, *apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	raw, err := os.ReadFile("../../../config/crd/maku.ogenki.io_" + plural + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.UnmarshalStrict(raw, &crd); err != nil {
		t.Fatal(err)
	}
	return string(raw), &crd
}

// The CEL messages of reusable merge policies (B1–B4, B8, OD-8, MP15).
const (
	msgPolicyName    = "a MergePolicy's name is its class name (B1)"
	msgReviewPolicy  = "review takes only description, harness, harnessAllowed, escalation, checks and testFirst"
	msgModeOnBinding = "mode is the binding's: live and shadow are refused on a MergePolicy (B4)"
	msgWorkflow      = "oracle.workflow is the repository's: set oracleWorkflow on the binding"
	msgFluxSource    = "the Flux source is the repository's: Repository.spec.merge.fluxSource (B2)"
	msgReviewBinding = "review is bound with mode off and no parameter"
	msgEntryClasses  = "bind docs-links and revert when any policy is bound (OD-8)"
	msgFileBindings  = "classAuthority repository: the repository's file holds the bindings (MP15)"
	msgPrivateCode   = "privateCode.acknowledged is true or absent: false says nothing (D3)"
)

// Factory resources D3, D7, D10, D11: namespaced, a status subresource, the printer columns, the
// fixed references. Reusable merge policies: the class shape, and the bindings.
func TestFactoryResourceCRDsCarryTheDesignRules(t *testing.T) {
	for plural, wants := range map[string][]string{
		"factories": {"kind: Factory", "scope: Namespaced", "subresources:", "name: Accepted", "name: Ready", "name: Repos", "name: Stopped", "mergeDefaults:"},
		"repositories": {"kind: Repository", "scope: Namespaced", "subresources:", "name: Repo", "name: Factory", "name: Accepted", "name: Connected",
			"spec.factoryRef is fixed once set", "spec.repository is fixed once set", "mergePolicies:", "x-kubernetes-list-type: map",
			"name: Classes", "name: Live", "policyGeneration:", "mergeDefaulted:",
			msgReviewBinding, msgEntryClasses, msgFileBindings, msgPrivateCode, "privateCode:"},
		"mergepolicies": {"kind: MergePolicy", "scope: Namespaced", "subresources:", "name: Consumers", "name: Current", "name: Live", "name: Accepted",
			"servedGeneration:", "requires:",
			msgPolicyName, msgReviewPolicy, msgModeOnBinding, msgWorkflow, msgFluxSource},
	} {
		raw, crd := readCRD(t, plural)
		msgs := messages(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, map[string]bool{}) // YAML folds a long one
		for _, w := range wants {
			if !strings.Contains(raw, w) && !msgs[w] {
				t.Errorf("%s CRD lacks %q", plural, w)
			}
		}
	}
	// B10, plan PR14: Authority and Classes move to the Repository. B8, PR20: the per-repository
	// shape is gone, its column, fields and rules with it.
	raw, crd := readCRD(t, "mergepolicies")
	for _, gone := range []string{"name: Authority", "name: Classes", "name: Repo", "spec.repositoryRef is fixed once set",
		"never together (B8)", "need repositoryRef (B8)", "keeps its shape", "a class is live or shadow, never both"} {
		if strings.Contains(raw, gone) {
			t.Errorf("mergepolicies CRD still has %q", gone)
		}
	}
	v := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	spec, status := v.Properties["spec"], v.Properties["status"]
	for _, f := range []string{"repositoryRef", "classes", "merge"} {
		if _, ok := spec.Properties[f]; ok {
			t.Errorf("mergepolicies spec still declares %s", f)
		}
	}
	for _, f := range []string{"authority", "classCount", "classes"} {
		if _, ok := status.Properties[f]; ok {
			t.Errorf("mergepolicies status still declares %s", f)
		}
	}
	_, rcrd := readCRD(t, "repositories")
	if _, ok := rcrd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"].Properties["mergePolicy"]; ok {
		t.Error("repositories status still declares mergePolicy")
	}
}

// policyFields are the JSON names of a class-shape MergePolicy's spec: config.PolicySpec, Class
// inlined.
func policyFields(t *testing.T) []string {
	t.Helper()
	var out []string
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for f := range typ.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.Anonymous && name == "" {
				walk(f.Type)
				continue
			}
			out = append(out, name)
		}
	}
	walk(reflect.TypeFor[config.PolicySpec]())
	if !slices.Contains(out, "requires") || !slices.Contains(out, "allowedFiles") {
		t.Fatalf("policy fields %q", out)
	}
	return out
}

// rule is the CEL rule whose message is msg, wherever it sits in the schema.
func rule(t *testing.T, s *apiextensionsv1.JSONSchemaProps, msg string) string {
	t.Helper()
	var found string
	var walk func(*apiextensionsv1.JSONSchemaProps)
	walk = func(s *apiextensionsv1.JSONSchemaProps) {
		for _, v := range s.XValidations {
			if v.Message == msg {
				found = v.Rule
			}
		}
		for _, p := range s.Properties {
			walk(&p)
		}
		if s.Items != nil && s.Items.Schema != nil {
			walk(s.Items.Schema)
		}
	}
	walk(s)
	if found == "" {
		t.Fatalf("no rule with message %q", msg)
	}
	return found
}

// The review rule lists fields one by one: a field added to config.Class or PolicySpec later must
// join it, or review could declare it.
func TestMergePolicyCELNamesEveryClassField(t *testing.T) {
	_, crd := readCRD(t, "mergepolicies")
	root := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	review := rule(t, root, msgReviewPolicy)
	for _, f := range policyFields(t) {
		if slices.Contains([]string{"description", "harness", "harnessAllowed", "escalation", "checks", "testFirst"}, f) {
			if strings.Contains(review, "self.spec."+f+")") {
				t.Errorf("the review rule refuses %s, which review takes", f)
			}
			continue
		}
		if !strings.Contains(review, "has(self.spec."+f+")") {
			t.Errorf("the review rule does not name %s", f)
		}
	}
}

// Review I3: CEL costs every rule against the declared bounds.
func TestFactoryResourceCRDsBoundEveryStringListAndMap(t *testing.T) {
	for _, plural := range []string{"factories", "repositories", "mergepolicies"} {
		_, crd := readCRD(t, plural)
		v := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
		spec, status := v.Properties["spec"], v.Properties["status"]
		bounded(t, plural+".spec", &spec)
		bounded(t, plural+".status", &status)
	}
}
