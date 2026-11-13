// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/resources"
)

// boundRig is two repositories bound to the same three policies: docs (fenced to the docs, at most
// 20 lines), docs-links and revert.
func boundRig(t *testing.T) *config.Config {
	t.Helper()
	hold := config.PolicySpec{Requires: []string{"holdoutChecks"}}
	cfg := &config.Config{Factory: "aws-0", Policies: map[string]config.ServedPolicy{
		"docs":       {Spec: config.PolicySpec{Class: config.Class{AllowedFiles: []string{`^docs/.+\.md$`}, MaxModifiedLines: 20}}, Generation: 3},
		"docs-links": {Spec: hold, Generation: 1}, "revert": {Spec: hold, Generation: 1}}}
	cfg.Generations.Repos = map[string]config.RepoGenerations{}
	for _, r := range []struct{ name, object string }{{"Smana/cloud-native-ref", "cloud-native-ref"}, {"Smana/ai-native-ref", "ai-native-ref"}} {
		rc := config.Repository{Name: r.name, Maintainers: []string{"Smana"}, TriggerLabel: "factory/ready", Classes: map[string]config.Class{},
			Merge: config.Merge{RequiredChecks: []string{"lint"}, VerifyChecks: []string{"lint"}, LeakScanCheck: "lint"},
			Bindings: []config.Binding{{Name: "docs-links", Mode: "shadow", HoldoutChecks: []string{"lint"}},
				{Name: "revert", Mode: "shadow", HoldoutChecks: []string{"lint"}}, {Name: "docs", Mode: "shadow"}}}
		for _, b := range rc.Bindings {
			cl, refused := config.Resolve(b.Name, cfg.Policies[b.Name].Spec, b, config.PolicyContext{}, rc.Merge)
			if refused != nil {
				t.Fatalf("%s: %+v", b.Name, refused)
			}
			rc.Classes[b.Name] = cl
			rc.Bound = append(rc.Bound, b.Name)
		}
		cfg.Repositories = append(cfg.Repositories, rc)
		cfg.Generations.Repos[strings.ToLower(r.name)] = config.RepoGenerations{Repository: 2, RepositoryName: r.object}
	}
	return cfg
}

// B11, B16: a policy edit lists every consumer's structural change; raising a cap relaxes both.
func TestPolicyPreviewListsItsConsumers(t *testing.T) {
	cfg := boundRig(t)
	p := cfg.Policies["docs"]
	spec := *p.Spec.DeepCopy()
	spec.MaxModifiedLines = 40
	pv := CheckPolicy(cfg, "docs", spec, "factory", func(string) bool { return true })
	if !pv.Valid || len(pv.Consumers) != 2 || pv.ManifestPath != "mergepolicy-docs.yaml" {
		t.Fatalf("%+v", pv)
	}
	for _, c := range pv.Consumers {
		if c.Change != "relaxation" || !c.Resolves {
			t.Fatalf("%+v", c)
		}
	}
	if objs, err := resources.Decode([]byte(pv.Manifest)); err != nil || len(objs) != 1 || objs[0].Spec["maxModifiedLines"] != float64(40) {
		t.Fatalf("%v %+v\n%s", err, objs, pv.Manifest)
	}
	// A consumer the caller cannot read is left out.
	if pv := CheckPolicy(cfg, "docs", spec, "factory", func(r string) bool { return r == "Smana/ai-native-ref" }); len(pv.Consumers) != 1 {
		t.Fatalf("%+v", pv.Consumers)
	}
	spec.Live = true
	if pv := CheckPolicy(cfg, "docs", spec, "factory", func(string) bool { return true }); pv.Valid || !strings.Contains(strings.Join(pv.Errors, " "), "mode is the binding's") {
		t.Fatalf("a policy refused alone: %+v", pv)
	}
}

// B16: a binding edit gives the Repository manifest and each binding's resolution; dropping
// revert is refused (OD-8).
func TestBindingPreview(t *testing.T) {
	cfg := boundRig(t)
	rc := &cfg.Repositories[0]
	m, err := resources.RepositoryManifest("cloud-native-ref", "factory", map[string]any{"factoryRef": "aws-0", "repository": rc.Name,
		"maintainers": rc.Maintainers, "triggerLabel": rc.TriggerLabel, "mergePolicies": rc.Bindings})
	if err != nil {
		t.Fatal(err)
	}
	rep := evidence.Report{RepositoryObject: "cloud-native-ref", RepositoryManifest: string(m), RepositoryManifestPath: "repository-cloud-native-ref.yaml"}

	live := []config.Binding{rc.Bindings[0], rc.Bindings[1], {Name: "docs", Mode: "live"}}
	pv := CheckBindings(cfg, rc, rep, live)
	if !pv.Valid || pv.ManifestPath != "repository-cloud-native-ref.yaml" || len(pv.Bindings) != 3 {
		t.Fatalf("%+v", pv)
	}
	objs, err := resources.Decode([]byte(pv.Manifest))
	if err != nil || len(objs) != 1 || !strings.Contains(pv.Manifest, "mode: live") || objs[0].Spec["repository"] != rc.Name {
		t.Fatalf("%v\n%s", err, pv.Manifest)
	}
	if b := pv.Bindings[2]; b.Name != "docs" || !b.Resolves || b.Change != "relaxation" {
		t.Fatalf("docs promoted: %+v", b)
	}

	dropped := []config.Binding{rc.Bindings[0], rc.Bindings[2]}
	if pv := CheckBindings(cfg, rc, rep, dropped); pv.Valid || !strings.Contains(strings.Join(pv.Errors, " "), "OD-8") {
		t.Fatalf("revert dropped: %+v", pv)
	}
	unknown := slices.Concat(rc.Bindings, []config.Binding{{Name: "nope", Mode: "off"}})
	if pv := CheckBindings(cfg, rc, rep, unknown); !pv.Valid || pv.Bindings[3].Resolves || pv.Bindings[3].Reason != "MissingRef" {
		t.Fatalf("a binding to no policy resolves to nothing, without refusing the edit (B9): %+v", pv.Bindings)
	}
	if pv := CheckBindings(cfg, rc, evidence.Report{}, live); pv.Valid {
		t.Fatalf("a repository served from a per-repository MergePolicy has no Repository manifest to edit: %+v", pv)
	}
}
