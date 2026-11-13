// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"os"
	"os/exec"
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

// splitANR writes ai-native-ref's objects in the binding shape, each edited by edit first.
func splitANR(t *testing.T, edit func(objs []resources.Object)) string {
	t.Helper()
	raw, err := os.ReadFile(anrObjects)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := resources.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if objs, err = resources.Split(objs, "agent-system"); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(objs)
	}
	out, err := resources.Encode(objs)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "objects.yaml")
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func editPolicy(objs []resources.Object, name string, gen int64, set map[string]any) {
	for i := range objs {
		if objs[i].Kind == resources.KindMergePolicy && objs[i].Name == name {
			objs[i].Generation = gen
			for k, v := range set {
				objs[i].Spec[k] = v
			}
		}
	}
}

// B11, Q5: --base prints each changed policy's consumers, live ones first, a live relaxation
// marked LIVE, and a consumer the change no longer resolves for with its reason.
func TestMakuConfigValidateBase(t *testing.T) {
	residual := anrResidual(t)
	base := splitANR(t, func(objs []resources.Object) {
		editPolicy(objs, "dead-links", 3, nil)
		editPolicy(objs, "docs-links", 1, map[string]any{"maxModifiedLines": 10})
	})
	next := splitANR(t, func(objs []resources.Object) {
		editPolicy(objs, "dead-links", 4, map[string]any{"monitor": map[string]any{"oracleRuns": 2, "followUpWindow": "168h", "flux": map[string]any{"within": "30m"}}})
		editPolicy(objs, "docs-links", 2, map[string]any{"maxModifiedLines": 50})
	})
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	if err := m.run(t.Context(), []string{"config", "validate", "--config", residual, "--base-config", residual, "--base", base, next}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	want := `MergePolicy dead-links (generation 3 -> 4):
  ai-native-ref     shadow  -                                                     refused: MissingParameter: monitor.flux needs Repository.spec.merge.fluxSource
  cloud-native-ref  shadow  relaxation: monitor changed or removed: its evidence  resolves
MergePolicy docs-links (generation 1 -> 2):
  LIVE cloud-native-ref  live    relaxation: maxModifiedLines raised or removed  resolves
  ai-native-ref          shadow  relaxation: maxModifiedLines raised or removed  resolves
  runlore                shadow  relaxation: maxModifiedLines raised or removed  resolves
  runlore-kb             shadow  relaxation: maxModifiedLines raised or removed  resolves
ok: 17 accepted
`
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
	out.Reset()
	err := m.run(t.Context(), []string{"config", "validate", "--config", residual, "--base-config", residual, "--base", base, "--expect-unchanged", next})
	if err == nil || !strings.Contains(out.String(), "changed: Smana/ai-native-ref dead-links: served before only") {
		t.Fatalf("got %v\n%s", err, out.String())
	}
}

// B17, ANR-1: the legacy objects and their split resolve to the same classes, and --resolved prints
// them as the frozen snapshot ai-native-ref keeps.
func TestMakuConfigValidateResolvedAndUnchanged(t *testing.T) {
	residual := anrResidual(t)
	split := splitANR(t, nil)
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	if err := m.run(t.Context(), []string{"config", "validate", "--config", residual, "--base-config", residual, "--base", anrObjects, "--expect-unchanged", split}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	want, err := os.ReadFile("testdata/anr-resolved.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, objs := range []string{anrObjects, split} {
		var stdout, stderr bytes.Buffer
		if err := (Maku{Dir: t.TempDir(), Out: &stdout, Err: &stderr}).run(t.Context(), []string{"config", "validate", "--config", residual, "--resolved", objs}); err != nil {
			t.Fatalf("%v\n%s", err, stderr.String())
		}
		if got := stdout.String(); got != string(want) { // pure JSON: the warnings go to stderr
			t.Fatalf("%s: --resolved is not testdata/anr-resolved.json:\n%s", objs, got)
		}
	}
}

// Q10: a binding over a guardrail is one guardrail: line, and validate fails; within them, the
// count is printed.
func TestMakuConfigValidateGuardrails(t *testing.T) {
	residual := anrResidual(t)
	guarded := func(maxPerDay int) string {
		return splitANR(t, func(objs []resources.Object) {
			for i := range objs {
				if objs[i].Kind == resources.KindFactory {
					objs[i].Spec["mergeGuardrails"] = map[string]any{"maxPerDay": maxPerDay}
				}
			}
		})
	}
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	err := m.run(t.Context(), []string{"config", "validate", "--config", residual, guarded(1)})
	if err == nil || !strings.Contains(err.Error(), "2 bindings over a guardrail") {
		t.Fatalf("got %v\n%s", err, out.String())
	}
	for _, line := range []string{
		"guardrail: Repository/cloud-native-ref deps-patch: maxPerDay 2 is over the guardrail's 1\n",
		"guardrail: Repository/runlore-kb kb-entries: maxPerDay 5 is over the guardrail's 1\n",
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("missing %q in:\n%s", line, out.String())
		}
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"config", "validate", "--config", residual, guarded(5)}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "guardrails: 23 bindings within\n") {
		t.Fatalf("%s", out.String())
	}
}

// B17: a refused conversion writes nothing, the residual included; the residual never replaces
// the file it came from, nor an existing file without --force.
func TestMakuConfigMigrateWritesNothingUnlessProved(t *testing.T) {
	dir, file := writeConfigFixture(t)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	m := Maku{Dir: t.TempDir(), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", file, "--residual", file}); err == nil || !strings.Contains(err.Error(), "--residual is the --from file") {
		t.Fatalf("got %v", err)
	}
	// A change the proof refuses: a guardrail in the file now caps every binding.
	capped := filepath.Join(dir, "capped.yaml")
	if err := os.WriteFile(capped, append(raw, []byte("mergeGuardrails: {maxModifiedLines: 1}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	residual := filepath.Join(dir, "residual.yaml")
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", capped, "--residual", residual}); err == nil {
		t.Fatal("the guardrail changes the classes: refused")
	}
	if _, err := os.Stat(residual); !os.IsNotExist(err) {
		t.Fatalf("a refused conversion wrote its residual: %v", err)
	}
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", file, "--residual", residual}); err != nil {
		t.Fatal(err)
	}
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", file, "--residual", residual}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("an existing residual is kept without --force: %v", err)
	}
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", file, "--residual", residual, "--force"}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(file); string(after) != string(raw) {
		t.Fatal("the --from file changed")
	}
}

// B11 with --base-ref: the base is the ref's own config and manifests, read through git, so a
// catalogue change, here a guardrail, shows on every consumer it changes.
func TestMakuConfigValidateBaseRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	residual, err := os.ReadFile(anrResidual(t))
	if err != nil {
		t.Fatal(err)
	}
	split, err := os.ReadFile(splitANR(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"factory.yaml": residual, "objects.yaml": split} {
		if err := os.WriteFile(filepath.Join(repo, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	guarded := strings.Replace(string(split), "  harnessProfiles:", "  mergeGuardrails:\n    maxPerDay: 1\n  harnessProfiles:", 1)
	if guarded == string(split) {
		t.Fatal("no harnessProfiles key to anchor on")
	}
	if err := os.WriteFile(filepath.Join(repo, "objects.yaml"), []byte(guarded), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	m := Maku{Dir: repo, Out: &out, Err: &errOut}
	err = m.run(t.Context(), []string{"config", "validate", "--config", filepath.Join(repo, "factory.yaml"), "--base-ref", "HEAD", filepath.Join(repo, "objects.yaml")})
	if err == nil {
		t.Fatal("bindings over the guardrail fail validate")
	}
	for _, want := range []string{
		"MergePolicy deps-patch:\n  cloud-native-ref  shadow  -  refused: GuardrailMismatch: class deps-patch: maxPerDay 2 is over the guardrail's 1\n",
		"MergePolicy alert-rules:\n  cloud-native-ref  shadow  tightening  resolves\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

// Task 19: validate names every legacy object it still reads.
func TestMakuConfigValidateWarnsOfTheLegacyShape(t *testing.T) {
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	if err := m.run(t.Context(), []string{"config", "validate", "--config", anrResidual(t), anrObjects}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"cloud-native-ref", "ai-native-ref", "runlore-kb", "runlore"} {
		if !strings.Contains(out.String(), "warning: MergePolicy "+n+" is in the per-repository shape, removed in the next minor: maku config migrate\n") {
			t.Errorf("no warning for %s:\n%s", n, out.String())
		}
	}
}

// B17 end to end: the migrated objects, their repository file exported in the binding shape, decide
// every conformance fixture as the central config does.
func TestMigratedOutputSimulatesUnchanged(t *testing.T) {
	residual := anrResidual(t)
	raw, err := os.ReadFile(residual)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(residual, append(raw, []byte("policyManagement: {simulation: true}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	split := splitANR(t, nil)
	args := []string{"--config", residual, "--manifests", split, "--repository", "Smana/ai-native-ref"}
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	if err := m.run(t.Context(), append([]string{"policy", "export"}, args...)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	file := filepath.Join(t.TempDir(), "agent-factory.yaml")
	if err := os.WriteFile(file, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtures, err := filepath.Glob("../factory/reconciler/testdata/conformance/*.json")
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	out.Reset()
	if err := m.run(t.Context(), append(append(append([]string{"policy", "simulate"}, args...), file), fixtures...)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != len(fixtures)+1 {
		t.Fatalf("%d rows for %d fixtures:\n%s", len(lines)-1, len(fixtures), out.String())
	}
	for _, l := range lines[1:] {
		if !strings.Contains(l, " unchanged") {
			t.Errorf("not unchanged: %s", l)
		}
	}
}

// Migrating objects already in the binding shape changes nothing and succeeds.
func TestMakuConfigMigrateIsIdempotent(t *testing.T) {
	residual := anrResidual(t)
	split := splitANR(t, nil)
	var out bytes.Buffer
	if err := (Maku{Dir: t.TempDir(), Out: &out, Err: &out}).run(t.Context(), []string{"config", "migrate", "--config", residual, "--manifests", split}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	want, err := os.ReadFile(split)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Fatalf("a second migration changed the objects:\n%s", out.String())
	}
}
