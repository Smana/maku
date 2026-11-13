// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/resources"
)

const orphanPolicy = `apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata:
  name: orphan
spec:
  repositoryRef: nothing
`

const typoPolicy = `apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata:
  name: typo
spec:
  repositoryRef: nothing
  classes:
    docs:
      typo: true
`

const configFixture = "../factory/resources/testdata/file.yaml"

func writeConfigFixture(t *testing.T) (dir, file string) {
	t.Helper()
	raw, err := os.ReadFile(configFixture)
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	file = filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, file
}

// TestMakuConfigMigrateThenValidate: the migrate output, with its residual, validates.
func TestMakuConfigMigrateThenValidate(t *testing.T) {
	dir, file := writeConfigFixture(t)
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	residual := filepath.Join(dir, "residual.yaml")
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", file, "--residual", residual}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	manifests := filepath.Join(dir, "manifests.yaml")
	if err := os.WriteFile(manifests, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"config", "validate", "--config", residual, manifests}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "ok: 7 accepted") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestMakuConfigValidateReportsRefusals(t *testing.T) {
	dir, file := writeConfigFixture(t)
	orphan := filepath.Join(dir, "orphan.yaml")
	if err := os.WriteFile(orphan, []byte(orphanPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	err := m.run(t.Context(), []string{"config", "validate", "--config", file, orphan})
	if err == nil || err.Error() != "config validate: 1 refused" {
		t.Fatalf("error: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "MergePolicy/orphan: MissingRef") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestMakuConfigValidateRefusesUnknownFields(t *testing.T) {
	dir, file := writeConfigFixture(t)
	typo := filepath.Join(dir, "typo.yaml")
	if err := os.WriteFile(typo, []byte(typoPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	err := m.run(t.Context(), []string{"config", "validate", "--config", file, typo})
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("error: %v\n%s", err, out.String())
	}
}

// F2: when no config can be served, validate still prints the refusals before its error.
func TestMakuConfigValidateReportsRefusalsWithoutAConfig(t *testing.T) {
	dir, _ := writeConfigFixture(t)
	residual := filepath.Join(dir, "residual.yaml")
	if err := os.WriteFile(residual, []byte("factory: aws-0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "factory.yaml")
	if err := os.WriteFile(f, []byte("apiVersion: maku.ogenki.io/v1alpha1\nkind: Factory\nmetadata: {name: aws-0}\nspec: {caps: {activeTasks: -1}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	err := m.run(t.Context(), []string{"config", "validate", "--config", residual, f})
	if err == nil || !strings.Contains(out.String(), "Factory/aws-0: Invalid") {
		t.Fatalf("error: %v\n%s", err, out.String())
	}
}

// A switch block written twice in the file is refused by validate, as the factory refuses it.
func TestMakuConfigValidateRefusesADuplicatedSwitchBlock(t *testing.T) {
	dir, file := writeConfigFixture(t)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(raw, "hardening: {}\nhardening: {}\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "orphan.yaml")
	if err := os.WriteFile(orphan, []byte(orphanPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	err = m.run(t.Context(), []string{"config", "validate", "--config", file, orphan})
	if err == nil || !strings.Contains(err.Error(), "hardening is written twice") {
		t.Fatalf("error: %v\n%s", err, out.String())
	}
}

const anrObjects = "../factory/resources/testdata/anr-objects.yaml"

// anrResidual is ai-native-ref's config file once its catalogue and repositories are objects.
func anrResidual(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../factory/resources/testdata/anr.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, residual, err := resources.Migrate(raw, "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "residual.yaml")
	if err := os.WriteFile(p, residual, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// B17: ai-native-ref's live objects convert to policies and bindings that validate, and the
// conversion proved no resolved class changes.
func TestMakuConfigMigrateManifests(t *testing.T) {
	residual := anrResidual(t)
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	if err := m.run(t.Context(), []string{"config", "migrate", "--config", residual, "--manifests", anrObjects}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	objs, err := resources.Decode(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 17 { // the Factory, four Repositories, twelve class policies
		t.Fatalf("%d objects:\n%s", len(objs), out.String())
	}
	manifests := filepath.Join(t.TempDir(), "manifests.yaml")
	if err := os.WriteFile(manifests, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"config", "validate", "--config", residual, manifests}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "ok: 17 accepted") || strings.Contains(out.String(), "per-repository shape") {
		t.Fatalf("%s", out.String())
	}
}

// PR10: a conversion that would change a resolved class (here, guardrails now capping every
// binding) is refused, each change named.
func TestMakuConfigMigrateRefusesAChange(t *testing.T) {
	residual := anrResidual(t)
	raw, err := os.ReadFile(anrObjects)
	if err != nil {
		t.Fatal(err)
	}
	capped := filepath.Join(t.TempDir(), "objects.yaml")
	if err := os.WriteFile(capped, []byte(strings.Replace(string(raw), "  harnessProfiles:", "  mergeGuardrails: {maxPerDay: 2}\n  harnessProfiles:", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), []string{"config", "migrate", "--config", residual, "--manifests", capped})
	if err == nil || !strings.Contains(err.Error(), "would change") {
		t.Fatalf("got %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "changed: Smana/runlore-kb kb-entries: served before only") || strings.Contains(out.String(), "apiVersion") {
		t.Fatalf("%s", out.String())
	}
}
