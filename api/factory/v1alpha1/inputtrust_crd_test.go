// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"os"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// status.inputTrust (hardening UH7, HR8) is bounded: a level is one of the five names, sources and
// withheld counts are capped, and a run's shadow flag is a plain boolean on its record.
func TestInputTrustCRD(t *testing.T) {
	raw, err := os.ReadFile("../../../config/crd/maku.ogenki.io_tasks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.UnmarshalStrict(raw, &crd); err != nil {
		t.Fatal(err)
	}
	status := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"]
	it, ok := status.Properties["inputTrust"]
	if !ok || it.Type != "object" {
		t.Fatalf("status.inputTrust = %+v", it)
	}
	levels := []string{"blocked", "none", "unapproved", "approved", "merged"}
	enum := func(field string, s apiextensionsv1.JSONSchemaProps, want []string) {
		t.Helper()
		var got []string
		for _, e := range s.Enum {
			var v string
			if err := yaml.Unmarshal(e.Raw, &v); err != nil {
				t.Fatal(err)
			}
			got = append(got, v)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s enum = %q, want %q", field, got, want)
		}
	}
	enum("min", it.Properties["min"], levels)
	enum("mode", it.Properties["mode"], []string{"audit", "enforce"})
	sources := it.Properties["sources"]
	if sources.MaxItems == nil || *sources.MaxItems != 16 {
		t.Errorf("sources.maxItems = %v, want 16", sources.MaxItems)
	}
	enum("sources.level", sources.Items.Schema.Properties["level"], levels)
	enum("sources.kind", sources.Items.Schema.Properties["kind"], []string{"default_branch", "steering", "factory",
		"issue", "comment", "own_ci", "foreign_ci", "room", "parent", "runlore", "tool_result"})
	withheld := it.Properties["withheld"]
	if withheld.MaxProperties == nil || *withheld.MaxProperties != 16 {
		t.Errorf("withheld.maxProperties = %v, want 16", withheld.MaxProperties)
	}
	if v := withheld.AdditionalProperties; v == nil || v.Schema == nil || v.Schema.Minimum == nil || *v.Schema.Minimum != 0 {
		t.Errorf("withheld values = %+v, want non-negative counts", v)
	}
	run := status.Properties["runs"].Items.Schema
	if s := run.Properties["shadowed"]; s.Type != "boolean" {
		t.Errorf("runs.shadowed = %+v", s)
	}
	if s := run.Properties["audited"]; s.Type != "boolean" {
		t.Errorf("runs.audited = %+v", s)
	}
	if s := run.Properties["shadowAttempts"]; s.Maximum == nil || *s.Maximum != 16 || s.Minimum == nil || *s.Minimum != 0 {
		t.Errorf("runs.shadowAttempts = %+v", s)
	}
}
