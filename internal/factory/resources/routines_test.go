// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"os"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

func routineObj(t *testing.T, name string, gen int64, catalog bool, spec string) Object {
	t.Helper()
	return Object{Kind: KindRoutine, Name: name, Generation: gen, Catalog: catalog, Spec: y(t, spec)}
}

func routineOutcome(r Result, repository, name string) *RoutineOutcome {
	for i, b := range r.RoutineBindings {
		if b.Repository == repository && b.Name == name {
			return &r.RoutineBindings[i]
		}
	}
	return nil
}

func TestDecodeRoutine(t *testing.T) {
	objs, err := Decode([]byte(`apiVersion: maku.ogenki.io/v1alpha1
kind: Routine
metadata:
  name: dead-links
  namespace: agent-system
  generation: 2
  labels:
    maku.ogenki.io/catalog: "true"
spec:
  description: Fix dead links
  workflow: quick
  defaultPrompt: Fix the dead external links listed below.
  probe:
    kind: dead-links
    requireConsecutive: 2
    maxFindings: 10
  requires: [oracleClass]
  maxOpenPRs: 1
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].Kind != KindRoutine || !objs[0].Catalog || objs[0].Generation != 2 {
		t.Fatalf("unexpected decoded Routine: %+v", objs)
	}

	_, err = Decode([]byte(`apiVersion: maku.ogenki.io/v1alpha1
kind: Routine
metadata:
  name: dead-links
spec:
  defaultPrompt: Fix links.
  unknownField: true
`))
	if err == nil || !strings.Contains(err.Error(), "unknownField") {
		t.Fatalf("unknown field must be refused: %v", err)
	}
}

func TestAssembleRoutineIsolationAndCatalogInert(t *testing.T) {
	validRT := routineObj(t, "dead-links", 3, false, `
workflow: quick
defaultPrompt: Fix dead links.
probe: {kind: dead-links, requireConsecutive: 2, maxFindings: 10}
requires: [oracleClass]
maxOpenPRs: 2
maxCostUSD: 3
`)
	catalogRT := routineObj(t, "doc-drift", 1, true, `
workflow: standard
defaultPrompt: Check docs drift.
probe: {kind: none}
maxOpenPRs: 1
`)
	invalidRT := routineObj(t, "bad-routine", 4, false, `
defaultPrompt: ""
probe: {kind: bogus}
`)

	t.Run("standalone invalid Routine is refused without failing valid Repository", func(t *testing.T) {
		repoObj := boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 2, anrBindings)
		repoObj.Spec["routines"] = []any{
			map[string]any{"name": "dead-links", "mode": "pr", "cron": "0 7 * * *", "oracleClass": "dead-links"},
			map[string]any{"name": "bad-routine", "mode": "pr", "cron": "0 8 * * *"},
		}
		objs := append(specPolicies(t), validRT, catalogRT, invalidRT, repoObj)
		r, err := Assemble(file(t), objs)
		if err != nil {
			t.Fatal(err)
		}
		if got := refused(r, KindRoutine, "bad-routine"); got != v1alpha1.ReasonInvalid {
			t.Fatalf("bad-routine refusal = %q, want %s (%+v)", got, v1alpha1.ReasonInvalid, r.Refused)
		}
		if got := refused(r, KindRepository, "ai-native-ref"); got != "" {
			t.Fatalf("Repository should not be refused: %s", got)
		}
		if ro := routineOutcome(r, "ai-native-ref", "dead-links"); ro == nil || !ro.Accepted || !ro.Served || ro.RoutineGeneration != 3 {
			t.Fatalf("dead-links outcome = %+v", ro)
		}
		if ro := routineOutcome(r, "ai-native-ref", "bad-routine"); ro == nil || ro.Accepted || ro.Served || ro.Reason != v1alpha1.ReasonMissingRef {
			t.Fatalf("bad-routine binding outcome = %+v", ro)
		}
		rc := r.Config.RepositoryFor("Smana/ai-native-ref")
		if rc == nil || len(rc.ResolvedRoutines) != 1 {
			t.Fatalf("expected exactly 1 resolved routine, got %+v", rc)
		}
		// UP2: unbound catalog Routine produces zero resolved routines.
		if _, ok := rc.ResolvedRoutines["doc-drift"]; ok {
			t.Fatal("unbound catalog Routine must never be resolved onto Repository")
		}
	})

	t.Run("invalid binding isolated and LastResolvedRoutines restored", func(t *testing.T) {
		repoObj := boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 2, anrBindings)
		repoObj.Spec["checks"] = map[string]any{"egress": []any{"pypi"}}
		repoObj.Spec["routines"] = []any{
			map[string]any{"name": "dead-links", "mode": "pr", "cron": "0 7 * * *", "oracleClass": "dead-links"},
			map[string]any{"name": "doc-drift", "mode": "pr", "cron": "0 4 * * 1"},
			map[string]any{"name": "disabled-one", "mode": "disabled"},
		}
		objs := append(specPolicies(t), validRT, catalogRT, repoObj)
		first, err := Assemble(file(t), objs)
		if err != nil || len(first.Refused) != 0 {
			t.Fatalf("first assemble: %v %+v", err, first.Refused)
		}
		if ro := routineOutcome(first, "ai-native-ref", "disabled-one"); ro == nil || !ro.Accepted || ro.Served {
			t.Fatalf("disabled binding without CR should be Accepted & !Served: %+v", ro)
		}

		// Now break doc-drift binding (bad workflow) and dead-links binding (OverRoutine maxOpenPRs),
		// plus add a bad egress binding without a last-good fallback.
		brokenRepo := boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 3, anrBindings)
		brokenRepo.Spec["checks"] = map[string]any{"egress": []any{"pypi"}}
		brokenRepo.Spec["routines"] = []any{
			map[string]any{"name": "dead-links", "mode": "pr", "cron": "0 7 * * *", "oracleClass": "dead-links", "maxOpenPRs": 4, "paused": true},
			map[string]any{"name": "doc-drift", "mode": "pr", "cron": "0 4 * * 1", "workflow": "nonexistent-wf"},
			map[string]any{"name": "extra-egress", "mode": "pr", "cron": "0 4 * * 1", "egress": []any{"golang"}},
		}
		extraRT := routineObj(t, "extra-egress", 1, false, `
defaultPrompt: Extra.
probe: {kind: none}
`)
		objs2 := append(specPolicies(t), validRT, catalogRT, extraRT, brokenRepo)
		second, err := Assemble(file(t), objs2, Options{LastResolvedRoutines: first.ResolvedRoutines})
		if err != nil {
			t.Fatalf("Repository should stay served when individual routine bindings fail: %v (%+v)", err, second.Refused)
		}
		if got := refused(second, KindRepository, "ai-native-ref"); got != "" {
			t.Fatalf("Repository refused: %s", got)
		}
		// dead-links falls back to LastResolvedRoutines with current Paused=true preserved.
		dl := routineOutcome(second, "ai-native-ref", "dead-links")
		if dl == nil || dl.Accepted || !dl.Served || dl.RoutineGeneration != 3 || dl.Reason != config.ReasonOverRoutine || !dl.Paused {
			t.Fatalf("dead-links fallback outcome = %+v", dl)
		}
		// doc-drift falls back to LastResolvedRoutines (workflow was bad in new edit, last resolved had standard).
		dd := routineOutcome(second, "ai-native-ref", "doc-drift")
		if dd == nil || dd.Accepted || !dd.Served || dd.RoutineGeneration != 1 || dd.Reason != config.ReasonInvalid {
			t.Fatalf("doc-drift fallback outcome = %+v", dd)
		}
		// extra-egress has no LastResolvedRoutines -> dropped cleanly without failing Repository.
		ee := routineOutcome(second, "ai-native-ref", "extra-egress")
		if ee == nil || ee.Accepted || ee.Served || ee.Reason != config.ReasonInvalid {
			t.Fatalf("extra-egress outcome = %+v", ee)
		}
		rc := second.Config.RepositoryFor("Smana/ai-native-ref")
		if rc == nil || len(rc.ResolvedRoutines) != 2 || !rc.ResolvedRoutines["dead-links"].Paused {
			t.Fatalf("resolved routines after fallback = %+v", rc.ResolvedRoutines)
		}

		// Unknown mode (bogus) must never widen by falling back to pr, while draft mode resolves cleanly.
		badModeRepo := boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 4, anrBindings)
		badModeRepo.Spec["routines"] = []any{
			map[string]any{"name": "doc-drift", "mode": "bogus", "cron": "0 4 * * 1"},
		}
		third, err := Assemble(file(t), append(specPolicies(t), catalogRT, badModeRepo), Options{LastResolvedRoutines: first.ResolvedRoutines})
		if err != nil {
			t.Fatalf("unknown mode refusal should isolate binding without failing Repository: %v", err)
		}
		if ro := routineOutcome(third, "ai-native-ref", "doc-drift"); ro == nil || ro.Accepted || ro.Served || ro.Reason != config.ReasonInvalid {
			t.Fatalf("unknown mode outcome = %+v", ro)
		}

		draftRepo := boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 5, anrBindings)
		draftRepo.Spec["routines"] = []any{
			map[string]any{"name": "doc-drift", "mode": "draft", "cron": "0 4 * * 1"},
		}
		fourth, err := Assemble(file(t), append(specPolicies(t), catalogRT, draftRepo), Options{LastResolvedRoutines: first.ResolvedRoutines})
		if err != nil {
			t.Fatalf("draft mode should resolve cleanly: %v", err)
		}
		if ro := routineOutcome(fourth, "ai-native-ref", "doc-drift"); ro == nil || !ro.Accepted || !ro.Served {
			t.Fatalf("draft mode outcome = %+v", ro)
		}
		if rc := fourth.Config.RepositoryFor("Smana/ai-native-ref"); rc == nil || rc.ResolvedRoutines["doc-drift"].Mode != config.RoutineModeDraft {
			t.Fatalf("resolved doc-drift mode = %+v", rc)
		}
	})

	t.Run("duplicate across schedules and routines refuses only the routine binding with DefinedTwice", func(t *testing.T) {
		repoObj := boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 2, anrBindings)
		repoObj.Spec["schedules"] = []any{
			map[string]any{"name": "dead-links", "cron": "0 7 * * *", "class": "dead-links", "probe": "dead-links", "text": "Fix dead links."},
		}
		repoObj.Spec["routines"] = []any{
			map[string]any{"name": "dead-links", "mode": "pr", "cron": "0 7 * * *", "oracleClass": "dead-links"},
			map[string]any{"name": "doc-drift", "mode": "pr", "cron": "0 4 * * 1"},
			map[string]any{"name": "doc-drift", "mode": "pr", "cron": "0 5 * * 1"},
		}
		objs := append(specPolicies(t), validRT, catalogRT, repoObj)
		prev := map[BindingKey]config.ResolvedRoutine{
			{Repository: "ai-native-ref", Name: "dead-links"}: {Name: "dead-links", Mode: config.RoutineModePR, Cron: "0 7 * * *", RoutineGeneration: 3},
		}
		r, err := Assemble(file(t), objs, Options{LastResolvedRoutines: prev})
		if err != nil {
			t.Fatalf("Repository should remain served: %v (%+v)", err, r.Refused)
		}
		if got := refused(r, KindRepository, "ai-native-ref"); got != "" {
			t.Fatalf("Repository refused: %s", got)
		}
		// Conflicting dead-links routine binding is refused DefinedTwice with NO fallback.
		dl := routineOutcome(r, "ai-native-ref", "dead-links")
		if dl == nil || dl.Accepted || dl.Served || dl.Reason != config.ReasonDefinedTwice {
			t.Fatalf("dead-links conflict outcome = %+v", dl)
		}
		// First doc-drift succeeds; second doc-drift is refused DefinedTwice.
		var ddOutcomes []RoutineOutcome
		for _, ro := range r.RoutineBindings {
			if ro.Name == "doc-drift" {
				ddOutcomes = append(ddOutcomes, ro)
			}
		}
		if len(ddOutcomes) != 2 || !ddOutcomes[0].Accepted || ddOutcomes[1].Accepted || ddOutcomes[1].Served || ddOutcomes[1].Reason != config.ReasonDefinedTwice {
			t.Fatalf("duplicate doc-drift outcomes = %+v", ddOutcomes)
		}
		rc := r.Config.RepositoryFor("Smana/ai-native-ref")
		if len(rc.Schedules) != 1 || len(rc.ResolvedRoutines) != 1 || rc.ResolvedRoutines["doc-drift"].Cron != "0 4 * * 1" {
			t.Fatalf("schedules=%+v resolvedRoutines=%+v", rc.Schedules, rc.ResolvedRoutines)
		}
	})
}

func TestMigrateSchedulesAndEqual(t *testing.T) {
	repos := []config.Repository{
		{
			Name: "Smana/cloud-native-ref",
			Classes: map[string]config.Class{
				"dead-links": {Oracle: &config.Oracle{Probe: config.ProbeDeadLinks, MaxFindings: 10}},
			},
			Schedules: []config.Schedule{
				{Name: "dead-links", Cron: "0 7 * * *", Class: "dead-links", Probe: config.ProbeDeadLinks, ProbeAuthor: "renovate[bot]", Text: "Fix dead links."},
				{Name: "renovate-red", Cron: "0 */6 * * *", Probe: config.ProbeRenovateRed, ProbeAuthor: "mend[bot]", Workflow: "standard", DataClass: "internal", Text: "Fix red renovate PRs."},
				{Name: "weekly-docs", Cron: "0 6 * * 1", Text: "Review docs."},
			},
		},
		{
			Name: "Smana/ai-native-ref",
			Classes: map[string]config.Class{
				"dead-links": {Oracle: &config.Oracle{Probe: config.ProbeDeadLinks, MaxFindings: 10}},
			},
			Schedules: []config.Schedule{
				{Name: "dead-links", Cron: "15 7 * * *", Class: "dead-links", Probe: config.ProbeDeadLinks, ProbeAuthor: "renovate[bot]", Text: "Fix dead links."},
			},
		},
	}

	routines, bindings, err := MigrateSchedules(repos)
	if err != nil {
		t.Fatal(err)
	}
	if len(routines) != 3 {
		t.Fatalf("got %d routines (expected shared dead-links deduped), want 3: %+v", len(routines), routines)
	}
	var names []string
	byName := map[string]v1alpha1.Routine{}
	for _, rt := range routines {
		names = append(names, rt.Name)
		byName[rt.Name] = rt
	}
	if !slices.Equal(names, []string{"dead-links", "renovate-red", "weekly-docs"}) {
		t.Fatalf("sorted routine names = %v", names)
	}
	dl := byName["dead-links"].Spec
	if dl.Workflow != config.WorkflowQuick || dl.Probe.Kind != config.ProbeDeadLinks || dl.Probe.RequireConsecutive != 2 ||
		dl.Probe.MaxFindings != 10 || !slices.Equal(dl.Requires, []string{"oracleClass"}) || dl.MaxOpenPRs != 1 {
		t.Fatalf("dead-links RoutineSpec = %+v", dl)
	}
	rr := byName["renovate-red"].Spec
	if rr.Workflow != config.WorkflowStandard || rr.DataClass != "internal" || rr.Probe.Kind != config.ProbeRenovateRed || rr.MaxOpenPRs != 1 {
		t.Fatalf("renovate-red RoutineSpec = %+v", rr)
	}
	wd := byName["weekly-docs"].Spec
	if wd.Workflow != config.WorkflowQuick || wd.Probe.Kind != config.ProbeNone || wd.MaxOpenPRs != 1 {
		t.Fatalf("weekly-docs RoutineSpec = %+v", wd)
	}
	if b := bindings["Smana/cloud-native-ref"]; len(b) != 3 || b[0].OracleClass != "dead-links" || b[1].ProbeAuthor != "mend[bot]" || b[2].Cron != "0 6 * * 1" {
		t.Fatalf("cloud-native-ref bindings = %+v", b)
	}
	if b := bindings["Smana/ai-native-ref"]; len(b) != 1 || b[0].Cron != "15 7 * * *" || b[0].OracleClass != "dead-links" {
		t.Fatalf("ai-native-ref bindings = %+v", b)
	}

	// Verify end-to-end equivalence via Assemble + Equal.
	basePolicies := specPolicies(t)
	cnr := boundRepo(t, "cnr", "Smana/cloud-native-ref", 1, `
- {name: docs-links, mode: shadow, holdoutChecks: [validate]}
- {name: revert, mode: shadow, holdoutChecks: [validate]}
- {name: dead-links, mode: shadow, oracleWorkflow: dead-links.yaml, allowedFiles: ['^docs/.+\.md$'], holdoutChecks: [validate]}
`)
	cnrLegacy := cnr
	cnrLegacy.Spec = map[string]any{}
	for k, v := range cnr.Spec {
		cnrLegacy.Spec[k] = v
	}
	cnrLegacy.Spec["schedules"] = []any{
		map[string]any{"name": "dead-links", "cron": "0 7 * * *", "class": "dead-links", "probe": "dead-links", "text": "Fix dead links."},
		map[string]any{"name": "renovate-red", "cron": "0 */6 * * *", "probe": "renovate-red", "probeAuthor": "mend[bot]", "workflow": "standard", "dataClass": "internal", "text": "Fix red renovate PRs."},
		map[string]any{"name": "weekly-docs", "cron": "0 6 * * 1", "text": "Review docs."},
	}

	// Build a minimal config file without repositories for Assemble.
	var fileMap map[string]any
	if err := yaml.Unmarshal(file(t), &fileMap); err != nil {
		t.Fatal(err)
	}
	delete(fileMap, "repositories")
	baseFile, err := yaml.Marshal(fileMap)
	if err != nil {
		t.Fatal(err)
	}
	beforeRes, err := Assemble(baseFile, append(slices.Clone(basePolicies), cnrLegacy))
	if err != nil || len(beforeRes.Refused) != 0 {
		t.Fatalf("before assemble: %v %+v", err, beforeRes.Refused)
	}

	migratedRoutines, migratedBindings, err := MigrateSchedules(beforeRes.Config.Repos())
	if err != nil {
		t.Fatal(err)
	}
	var afterObjs []Object
	afterObjs = append(afterObjs, basePolicies...)
	for _, rt := range migratedRoutines {
		m, err := toMap(rt.Spec)
		if err != nil {
			t.Fatal(err)
		}
		afterObjs = append(afterObjs, Object{Kind: KindRoutine, Name: rt.Name, Generation: 1, Spec: m})
	}
	cnrMigrated := boundRepo(t, "cnr", "Smana/cloud-native-ref", 1, `
- {name: docs-links, mode: shadow, holdoutChecks: [validate]}
- {name: revert, mode: shadow, holdoutChecks: [validate]}
- {name: dead-links, mode: shadow, oracleWorkflow: dead-links.yaml, allowedFiles: ['^docs/.+\.md$'], holdoutChecks: [validate]}
`)
	var rawBindings []any
	for _, b := range migratedBindings["Smana/cloud-native-ref"] {
		m, err := toMap(b)
		if err != nil {
			t.Fatal(err)
		}
		rawBindings = append(rawBindings, m)
	}
	cnrMigrated.Spec["routines"] = rawBindings
	afterObjs = append(afterObjs, cnrMigrated)

	afterRes, err := Assemble(baseFile, afterObjs)
	if err != nil || len(afterRes.Refused) != 0 {
		t.Fatalf("after assemble: %v %+v", err, afterRes.Refused)
	}
	if diffs := Equal(beforeRes.Config, afterRes.Config); len(diffs) != 0 {
		t.Fatalf("Equal found diffs after MigrateSchedules: %v", diffs)
	}

	// Mutating a migrated routine's cron must be detected by Equal.
	drifted := *afterRes.Config
	drifted.Repositories = slices.Clone(afterRes.Config.Repositories)
	rrCopy := mapsClone(drifted.Repositories[0].ResolvedRoutines)
	entry := rrCopy["weekly-docs"]
	entry.Cron = "0 0 * * *"
	rrCopy["weekly-docs"] = entry
	drifted.Repositories[0].ResolvedRoutines = rrCopy
	if diffs := Equal(beforeRes.Config, &drifted); len(diffs) == 0 {
		t.Fatal("Equal failed to detect migrated routine cron drift")
	}

	// Conflicting shared schedule across repos returns an error.
	repos[1].Schedules[0].Text = "Different prompt."
	if _, _, err := MigrateSchedules(repos); err == nil {
		t.Fatal("expected error when two repos define same schedule name differently")
	}
}

func TestMigratePreservesRoutinesAndEqualDetectsDroppedRoutines(t *testing.T) {
	raw := strings.Replace(string(file(t)),
		"    classes: {docs-links: {shadow: true}, revert: {shadow: true}, docs: {}}\n",
		"    classes: {docs-links: {shadow: true}, revert: {shadow: true}, docs: {}}\n    routines:\n      - name: extra-routine\n        mode: disabled\n        cron: \"0 4 * * 1\"\n",
		1)
	if !strings.Contains(raw, "extra-routine") {
		t.Fatal("failed to construct config with pre-existing routines")
	}
	before, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	manifests, residual, err := Migrate([]byte(raw), "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Decode(manifests)
	if err != nil {
		t.Fatal(err)
	}
	repoObj := object(objs, KindRepository, "cloud-native-ref")
	if repoObj == nil {
		t.Fatal("missing Repository object")
	}
	if _, ok := repoObj.Spec["routines"]; !ok {
		t.Fatalf("Migrate dropped pre-existing routines[] from Repository spec: %+v", repoObj.Spec)
	}

	after, err := Assemble(residual, objs)
	if err != nil || len(after.Refused) != 0 {
		t.Fatalf("Assemble failed: %v %+v", err, after.Refused)
	}
	if diffs := Equal(before, after.Config); len(diffs) != 0 {
		t.Fatalf("Equal reported unexpected diffs when routines[] preserved: %v", diffs)
	}

	// Dropping routines[] from after must be detected by Equal even when before came from config.Parse.
	dropped := *after.Config
	dropped.Repositories = slices.Clone(after.Config.Repositories)
	dropped.Repositories[0].Routines = nil
	if diffs := Equal(before, &dropped); len(diffs) == 0 {
		t.Fatal("Equal failed to detect dropped pre-existing routines[] when before came from config.Parse")
	}

	// Also verify when before has BOTH schedules and pre-existing routines, Equal detects dropped routine binding.
	beforeWithSched := *before
	beforeWithSched.Repositories = slices.Clone(before.Repositories)
	beforeWithSched.Repositories[0].Schedules = []config.Schedule{
		{Name: "weekly-docs", Cron: "0 6 * * 1", Text: "Review docs."},
	}
	afterWithSched := *after.Config
	afterWithSched.Repositories = slices.Clone(after.Config.Repositories)
	afterWithSched.Repositories[0].ResolvedRoutines = map[string]config.ResolvedRoutine{
		"weekly-docs": {
			Name:          "weekly-docs",
			Mode:          config.RoutineModePR,
			Cron:          "0 6 * * 1",
			DefaultPrompt: "Review docs.",
			Workflow:      config.WorkflowQuick,
			DataClass:     before.Defaults.DataClass,
			Probe:         config.ProbeSpec{Kind: config.ProbeNone},
			MaxOpenPRs:    1,
		},
	}
	afterWithSched.Repositories[0].Routines = append(slices.Clone(afterWithSched.Repositories[0].Routines), config.RoutineBinding{
		Name: "weekly-docs",
		Mode: config.RoutineModePR,
		Cron: "0 6 * * 1",
	})
	if diffs := Equal(&beforeWithSched, &afterWithSched); len(diffs) != 0 {
		t.Fatalf("Equal reported unexpected diffs when both schedule and pre-existing routine preserved: %v", diffs)
	}
	afterMissingPreExisting := afterWithSched
	afterMissingPreExisting.Repositories = slices.Clone(afterWithSched.Repositories)
	afterMissingPreExisting.Repositories[0].Routines = []config.RoutineBinding{
		{Name: "weekly-docs", Mode: config.RoutineModePR, Cron: "0 6 * * 1"},
	}
	if diffs := Equal(&beforeWithSched, &afterMissingPreExisting); len(diffs) == 0 {
		t.Fatal("Equal failed to detect dropped pre-existing routine binding alongside migrated schedules")
	}
}

func mapsClone[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func TestChartCatalogRoutinesAssembleAndInertWhenUnbound(t *testing.T) {
	raw, err := os.ReadFile("../../../charts/agent-factory/tests/golden/routines-catalog.yaml")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode routines-catalog.yaml: %v", err)
	}
	if len(objs) != 6 {
		t.Fatalf("got %d catalog routines, want 6", len(objs))
	}
	var names []string
	for i := range objs {
		if objs[i].Kind != KindRoutine || !objs[i].Catalog {
			t.Fatalf("catalog routine %d = %+v, want KindRoutine with Catalog=true", i, objs[i])
		}
		objs[i].Generation = 1
		names = append(names, objs[i].Name)
	}
	slices.Sort(names)
	wantNames := []string{"dead-code", "dead-links", "doc-drift", "dup-unifier", "flaky-tests", "renovate-red"}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("catalog routine names = %v, want %v", names, wantNames)
	}

	// Unbound: all 6 catalog Routine objects are accepted by Assemble and remain completely inert.
	unboundRepo := boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 1, anrBindings)
	res, err := Assemble(file(t), append(slices.Concat(specPolicies(t), objs), unboundRepo))
	if err != nil {
		t.Fatalf("assemble unbound catalog routines: %v (refused: %+v)", err, res.Refused)
	}
	if len(res.Refused) != 0 {
		t.Fatalf("unexpected refusals for catalog routines: %+v", res.Refused)
	}
	rc := res.Config.RepositoryFor("Smana/ai-native-ref")
	if rc == nil || len(rc.ResolvedRoutines) != 0 {
		t.Fatalf("unbound catalog routines must stay inert, got %+v", rc)
	}

	// Explicitly binding two catalog routines resolves only those two.
	bound := boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 2, anrBindings)
	bound.Spec["routines"] = []any{
		map[string]any{"name": "dead-code", "mode": "pr", "cron": "0 3 * * 1", "probeWorkflow": "deadcode.yaml"},
		map[string]any{"name": "doc-drift", "mode": "pr", "cron": "0 4 * * 1"},
	}
	res2, err := Assemble(file(t), append(slices.Concat(specPolicies(t), objs), bound))
	if err != nil {
		t.Fatalf("assemble bound catalog routines: %v (refused: %+v)", err, res2.Refused)
	}
	if len(res2.Refused) != 0 {
		t.Fatalf("unexpected refusals with bound catalog routines: %+v", res2.Refused)
	}
	rc2 := res2.Config.RepositoryFor("Smana/ai-native-ref")
	if rc2 == nil || len(rc2.ResolvedRoutines) != 2 || rc2.ResolvedRoutines["dead-code"].ProbeWorkflow != "deadcode.yaml" {
		t.Fatalf("unexpected resolved routines: %+v", rc2)
	}
}
