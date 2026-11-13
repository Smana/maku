// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
)

const policyConfig = "testdata/policy-factory.yaml"

func policyRepo(t *testing.T) string {
	t.Helper()
	c, err := config.Load(policyConfig)
	if err != nil {
		t.Fatal(err)
	}
	return c.Repos()[0].Name
}

// boundManifests is cloud-native-ref served from its bindings: a config file without its
// repositories, and the manifests serving the repository.
func boundManifests(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../factory/resources/testdata/file.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := yaml.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	delete(base, "repositories")
	if raw, err = yaml.Marshal(base); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "factory.yaml")
	if err := os.WriteFile(cfg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"--config", cfg, "--manifests", "../factory/mergepolicy/testdata/bound-cloud-native-ref.yaml", "--repository", "Smana/cloud-native-ref"}
}

// B15, B16: export writes the bound repository's file in the binding shape, and check proves it
// equivalent to the bindings the factory serves.
func TestPolicyExportThenCheck(t *testing.T) {
	args := boundManifests(t)
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	if err := m.run(t.Context(), append([]string{"policy", "export"}, args...)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "mergePolicies:") || strings.Contains(out.String(), "classes:") {
		t.Fatalf("%s", out.String())
	}
	file := filepath.Join(t.TempDir(), "agent-factory.yaml")
	if err := os.WriteFile(file, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := m.run(t.Context(), append(append([]string{"policy", "check"}, args...), file)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "equivalent") {
		t.Fatalf("%s", out.String())
	}
}

// A repository served from the per-repository shape is migrated before its file binds policies.
func TestPolicyExportRefusesALegacyServedRepository(t *testing.T) {
	var out bytes.Buffer
	err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), []string{"policy", "export", "--config", policyConfig, "--repository", policyRepo(t)})
	if err == nil || !strings.Contains(err.Error(), "migrate the repository first: maku config migrate") {
		t.Fatalf("got %v", err)
	}
}

// check prints the bindings that do not resolve and the deprecations; the file stays valid, and a
// refused binding is a divergence.
func TestPolicyCheckPrintsTheBindingErrors(t *testing.T) {
	args := boundManifests(t)
	file := filepath.Join(t.TempDir(), "agent-factory.yaml")
	body := "factory: aws-0\nmergePolicies:\n" +
		"  - {name: docs-links, mode: live, holdoutChecks: ['Check the documentation links 🔗']}\n" +
		"  - {name: revert, mode: shadow, holdoutChecks: ['Check the documentation links 🔗']}\n" +
		"  - {name: foo, mode: shadow}\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), append(append([]string{"policy", "check"}, args...), file))
	if err == nil || !strings.Contains(err.Error(), "foo: refused, MissingRef") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(out.String(), "binding foo: MissingRef: the factory serves no MergePolicy foo") {
		t.Fatalf("%s", out.String())
	}
}

// PR4: check prints a legacy file's conditions.any as deprecated.
func TestPolicyCheckPrintsTheDeprecations(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agent-factory.yaml")
	body := "factory: aws-0\nclasses:\n" +
		"  docs-links: {mode: shadow, author: agents, harness: plain}\n  revert: {mode: shadow, author: agents, harness: plain}\n" +
		"  docs: {mode: shadow, author: agents, harness: plain, conditions: {any: [[{allowedFiles: ['^docs/.+$']}], [{allowedFiles: ['^README$']}]]}}\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_ = Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), []string{"policy", "check", "--config", policyConfig, "--repository", policyRepo(t), file})
	if !strings.Contains(out.String(), "deprecated: class docs: conditions.any is deprecated") {
		t.Fatalf("%s", out.String())
	}
}

func TestPolicyCheckNamesWhatIsWrong(t *testing.T) {
	repo := policyRepo(t)
	file := filepath.Join(t.TempDir(), "agent-factory.yaml")
	if err := os.WriteFile(file, []byte("factory: aws-0\nclasses: {docs-links: {mode: live, author: agents, harness: plain}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), []string{"policy", "check", "--config", policyConfig, "--repository", repo, file})
	if err == nil || !strings.Contains(err.Error(), "revert") {
		t.Fatalf("got %v", err)
	}
}

func TestPolicyRefusesAnUnlistedRepository(t *testing.T) {
	var out bytes.Buffer
	err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), []string{"policy", "export", "--config", policyConfig, "--repository", "o/nope"})
	if err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("got %v", err)
	}
}

// check is for a repository's own CI: a file naming another factory gives the repository away.
func TestPolicyCheckRefusesAnotherFactory(t *testing.T) {
	repo := policyRepo(t)
	file := filepath.Join(t.TempDir(), "agent-factory.yaml")
	if err := os.WriteFile(file, []byte("factory: gcp-0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), []string{"policy", "check", "--config", policyConfig, "--repository", repo, file})
	if err == nil || !strings.Contains(err.Error(), "gcp-0") || !strings.Contains(err.Error(), "aws-0") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(out.String(), "equivalent") {
		t.Fatalf("%s", out.String())
	}
}
