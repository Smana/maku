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

// Factory resources D3, D7, D10, D11: namespaced, a status subresource, the printer columns, the
// fixed references, and live and shadow never together on one class.
func TestFactoryResourceCRDsCarryTheDesignRules(t *testing.T) {
	for plural, wants := range map[string][]string{
		"factories": {"kind: Factory", "scope: Namespaced", "subresources:", "name: Accepted", "name: Ready", "name: Repos", "name: Stopped"},
		"repositories": {"kind: Repository", "scope: Namespaced", "subresources:", "name: Repo", "name: Factory", "name: Accepted", "name: Connected",
			"spec.factoryRef is fixed once set", "spec.repository is fixed once set"},
		"mergepolicies": {"kind: MergePolicy", "scope: Namespaced", "subresources:", "name: Repo", "name: Authority", "name: Classes", "name: Live", "name: Accepted",
			"spec.repositoryRef is fixed once set", "a class is live or shadow, never both"},
	} {
		raw, _ := readCRD(t, plural)
		for _, w := range wants {
			if !strings.Contains(raw, w) {
				t.Errorf("%s CRD lacks %q", plural, w)
			}
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
