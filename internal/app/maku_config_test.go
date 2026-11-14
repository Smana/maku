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

// orphanPolicy is a Repository of another factory: refused NotThisFactory.
const orphanPolicy = `apiVersion: maku.ogenki.io/v1alpha1
kind: Repository
metadata:
  name: orphan
spec:
  factoryRef: gcp-0
  repository: Smana/orphan
`

const typoPolicy = `apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata:
  name: typo
spec:
  checks: true
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

// R7: the factory refuses the single-repository shape, and maku config migrate still reads it,
// from the last release's file, into objects the residual serves.
func TestMakuConfigMigrateReadsTheLegacyShape(t *testing.T) {
	dir := t.TempDir()
	residual := filepath.Join(dir, "residual.yaml")
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	legacy := "../factory/resources/testdata/legacy-single-repository.yaml"
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", legacy, "--residual", residual}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "kind: Repository") || !strings.Contains(out.String(), "repository: Smana/cloud-native-ref") {
		t.Fatalf("manifests: %s", out.String())
	}
	raw, err := os.ReadFile(residual)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\nrepository:") || strings.Contains(string(raw), "controlIssue") {
		t.Fatalf("the residual keeps the old shape:\n%s", raw)
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
	if !strings.Contains(out.String(), "Repository/orphan: NotThisFactory") {
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

// anrObjects are ai-native-ref's objects in the per-repository shape, as of release N; anrBound
// the same objects in the binding shape, as Migrate's converter writes them.
const (
	anrObjects = "../factory/resources/testdata/anr-objects.yaml"
	anrBound   = "../factory/resources/testdata/anr-bound-objects.yaml"
)

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

// B8, plan PR20: the per-repository MergePolicy is gone, so migrate takes no manifests and validate
// refuses them, each naming the way out.
func TestMakuConfigRefusesThePerRepositoryShape(t *testing.T) {
	residual := anrResidual(t)
	for verb, args := range map[string][]string{
		"migrate":  {"config", "migrate", "--from", residual, "--manifests", anrObjects},
		"validate": {"config", "validate", "--config", residual, anrObjects},
	} {
		var out bytes.Buffer
		err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), args)
		if err == nil || !strings.Contains(err.Error(), resources.ErrPerRepositoryRemoved) {
			t.Errorf("%s: got %v\n%s", verb, err, out.String())
		}
	}
}

// splitANR writes ai-native-ref's objects in the binding shape, each edited by edit first.
func splitANR(t *testing.T, edit func(objs []resources.Object)) string {
	t.Helper()
	raw, err := os.ReadFile(anrBound)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := resources.Decode(raw)
	if err != nil {
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
	// The refused consumer fails a plain validate (TestMakuConfigValidateRefusedBindings): the
	// blast radius is read here with it allowed.
	if err := m.run(t.Context(), []string{"config", "validate", "--allow-refused-bindings", "--config", residual, "--base-config", residual, "--base", base, next}); err != nil {
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
warning: binding: Repository/ai-native-ref dead-links: MissingParameter: monitor.flux needs Repository.spec.merge.fluxSource
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

// B17, ANR-1: the split objects resolve to the classes the legacy objects did (testdata/anr-resolved.json,
// frozen from them in release N), --resolved printing them as the snapshot ai-native-ref keeps, and
// a base equal to the change is unchanged.
func TestMakuConfigValidateResolvedAndUnchanged(t *testing.T) {
	residual := anrResidual(t)
	split := splitANR(t, nil)
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	if err := m.run(t.Context(), []string{"config", "validate", "--config", residual, "--base-config", residual, "--base", anrBound, "--expect-unchanged", split}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	want, err := os.ReadFile("testdata/anr-resolved.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, objs := range []string{split} {
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

// B9: a binding that does not resolve drops its class without refusing an object, so validate
// names each one with its reason and fails; --allow-refused-bindings keeps them as warnings.
func TestMakuConfigValidateRefusedBindings(t *testing.T) {
	residual := anrResidual(t)
	live := splitANR(t, func(objs []resources.Object) {
		for i := range objs {
			if objs[i].Kind != resources.KindRepository || objs[i].Name != "cloud-native-ref" {
				continue
			}
			for _, b := range objs[i].Spec["mergePolicies"].([]any) {
				if m := b.(map[string]any); m["name"] == "deps-minor" {
					m["mode"] = "live" // semver-minor is shadow only (M2)
				}
			}
		}
	})
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	err := m.run(t.Context(), []string{"config", "validate", "--config", residual, live})
	if err == nil || !strings.Contains(err.Error(), "1 binding refused") {
		t.Fatalf("got %v\n%s", err, out.String())
	}
	if want := "binding: Repository/cloud-native-ref deps-minor: Invalid: class deps-minor: semver-minor is shadow only (M2)\n"; !strings.Contains(out.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, out.String())
	}
	if strings.Contains(out.String(), "ok: ") {
		t.Fatalf("a refused binding is not ok:\n%s", out.String())
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"config", "validate", "--allow-refused-bindings", "--config", residual, live}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "warning: binding: Repository/cloud-native-ref deps-minor: Invalid: ") || !strings.Contains(out.String(), "ok: ") {
		t.Fatalf("%s", out.String())
	}
	// Every binding resolving, nothing is said.
	out.Reset()
	if err := m.run(t.Context(), []string{"config", "validate", "--config", residual, splitANR(t, nil)}); err != nil || strings.Contains(out.String(), "binding:") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}
