// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"os"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
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

// The CEL messages of reusable merge policies (B1–B3, B8, MP15, MR9).
const (
	msgPolicyName    = "a MergePolicy's name is its class name (B1)"
	msgReviewPolicy  = "review is not a policy"
	msgWorkflow      = "oracle.workflow is the repository's: set oracleWorkflow on the binding"
	msgFluxSource    = "the Flux source is the repository's: Repository.spec.merge.fluxSource (B2)"
	msgNarrowed      = "narrowedFiles is set by a binding's allowedFiles: set allowedFiles on the policy"
	msgReviewBinding = "review is not a policy"
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
			msgReviewBinding, msgFileBindings, msgPrivateCode, "privateCode:"},
		"mergepolicies": {"kind: MergePolicy", "scope: Namespaced", "subresources:", "name: Consumers", "name: Current", "name: Live", "name: Accepted",
			"servedGeneration:",
			msgPolicyName, msgReviewPolicy, msgWorkflow, msgFluxSource, msgNarrowed, "evidence:"},
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
	for _, f := range []string{"repositoryRef", "classes", "merge", "requires", "live", "shadow", "harness", "harnessAllowed", "escalation", "checks", "testFirst"} {
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
