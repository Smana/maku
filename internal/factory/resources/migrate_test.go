// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
)

// D8: migrating, then assembling the manifests with the residual file, gives the config the file
// gave: same repositories, classes, merge rules and catalogue.
func TestMigrateRoundTrips(t *testing.T) {
	want, err := config.Parse(file(t))
	if err != nil {
		t.Fatal(err)
	}
	manifests, residual, err := Migrate(file(t), "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Decode(manifests)
	if err != nil {
		t.Fatalf("Decode(Migrate(...)): %v\n%s", err, manifests)
	}
	got, err := Assemble(residual, objs)
	if err != nil || len(got.Refused) != 0 {
		t.Fatalf("%v %+v", err, got.Refused)
	}
	for _, k := range CatalogueKeys {
		if strings.Contains(string(residual), "\n"+k+":") {
			t.Errorf("the residual file still has %s", k)
		}
	}
	if !equalRepos(t, want.Repos(), got.Config.Repos()) || !equalJSON(t, want.Tiers, got.Config.Tiers) ||
		!equalJSON(t, want.Templates, got.Config.Templates) || !equalJSON(t, want.Caps, got.Config.Caps) ||
		!equalJSON(t, want.Budgets, got.Config.Budgets) || !equalJSON(t, want.Triage, got.Config.Triage) ||
		!equalJSON(t, want.Evaluation, got.Config.Evaluation) || !equalJSON(t, want.HarnessProfiles, got.Config.HarnessProfiles) {
		t.Fatalf("the round trip changed the config\nmanifests:\n%s\nresidual:\n%s", manifests, residual)
	}
}

// R7: the factory refuses the single-repository shape, but migrate still reads the last release's
// file and writes its objects: the repository, its kill switch and its classes survive the trip.
func TestMigrateReadsTheLegacyShape(t *testing.T) {
	legacy, err := os.ReadFile("testdata/legacy-single-repository.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Parse(legacy); err == nil || !strings.Contains(err.Error(), "single-repository shape") {
		t.Fatalf("the factory must refuse the fixture: %v", err)
	}
	manifests, residual, err := Migrate(legacy, "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := yaml.Unmarshal(residual, &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"repository", "maintainers", "triggerLabel", "controlIssue", "classes", "merge", "schedules", "repositories"} {
		if _, ok := top[k]; ok {
			t.Errorf("the residual file still has %s:\n%s", k, residual)
		}
	}
	if api, _ := top["api"].(map[string]any); api["repositories"] != nil {
		t.Errorf("the residual file keeps api.repositories, which the list derives")
	}
	objs, err := Decode(manifests)
	if err != nil {
		t.Fatalf("Decode: %v\n%s", err, manifests)
	}
	got, err := Assemble(residual, objs)
	if err != nil || len(got.Refused) != 0 {
		t.Fatalf("%v %+v", err, got.Refused)
	}
	rs := got.Config.Repos()
	if len(rs) != 1 || rs[0].Name != "Smana/cloud-native-ref" || !rs[0].IsMaintainer("Smana") || rs[0].TriggerLabel != "factory/ready" ||
		len(rs[0].Classes) != 2 || rs[0].Merge.MergerLogin != "ogenki-agent-merger[bot]" {
		t.Fatalf("repositories %+v", rs)
	}
	if c := got.Config.ControlFor(); c != (config.Control{Repository: "Smana/cloud-native-ref", Issue: 7}) {
		t.Fatalf("control %+v", c)
	}
}

func TestMigrateNamesAndSplitsTheMeter(t *testing.T) {
	var m map[string]any
	if err := yaml.Unmarshal(file(t), &m); err != nil {
		t.Fatal(err)
	}
	entry := m["repositories"].([]any)[0].(map[string]any)
	var repos []any
	for _, n := range []string{"A/x", "B/x", "C/zed"} {
		e := maps.Clone(entry)
		e["name"] = n
		repos = append(repos, e)
	}
	m["repositories"] = repos
	m["meter"].(map[string]any)["prices"] = map[string]any{"agent-default": map[string]any{"input": 1.40, "cacheRead": 0.26,
		"output": 4.40, "source": "https://docs.z.ai/guides/overview/pricing", "checkedAt": "2026-10-16"}}
	m["triage"] = map[string]any{"controlPercent": 5}
	in, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	manifests, residual, err := Migrate(in, "ns")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"name: a-x", "name: b-x", "name: zed"} {
		if !strings.Contains(string(manifests), n) {
			t.Errorf("missing %q in\n%s", n, manifests)
		}
	}
	if !strings.Contains(string(residual), "url: http://vmsingle") || strings.Contains(string(residual), "prices") ||
		strings.Contains(string(residual), "triage") {
		t.Errorf("residual:\n%s", residual)
	}
}

func equalRepos(t *testing.T, a, b []config.Repository) bool {
	t.Helper()
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || !equalJSON(t, a[i].Maintainers, b[i].Maintainers) ||
			a[i].TriggerLabel != b[i].TriggerLabel || !equalJSON(t, a[i].Workflow, b[i].Workflow) ||
			!equalJSON(t, classesUnderDefaults(a[i].Classes), classesUnderDefaults(b[i].Classes)) ||
			!equalJSON(t, a[i].Merge, b[i].Merge) || !equalJSON(t, a[i].Schedules, b[i].Schedules) ||
			!equalJSON(t, a[i].Checks, b[i].Checks) || a[i].ClassAuthority != b[i].ClassAuthority {
			return false
		}
	}
	return true
}

func equalJSON(t *testing.T, a, b any) bool {
	t.Helper()
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	return bytes.Equal(x, y)
}

// F1: ai-native-ref's real config migrates into objects Assemble serves whole, and a loop row's
// bare `off` (YAML 1.1's false) is written as the string "off".
func TestMigrateANRConfig(t *testing.T) {
	file, err := os.ReadFile("testdata/anr.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Parse(file); err != nil {
		t.Fatalf("the fixture must parse as a file: %v", err)
	}
	manifests, residual, err := Migrate(file, "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifests), "scope: \"off\"") {
		t.Errorf("loops.default.scope must be the string \"off\":\n%s", manifests)
	}
	objs, err := Decode(manifests)
	if err != nil {
		t.Fatalf("Decode(Migrate(anr)): %v", err)
	}
	r, err := Assemble(residual, objs)
	if err != nil || len(r.Refused) != 0 || len(r.Accepted) != 12 { // a Factory, three Repositories and eight class policies
		t.Fatalf("err %v, refused %+v, accepted %d", err, r.Refused, len(r.Accepted))
	}
}

func TestDecodeRefusesABooleanOff(t *testing.T) {
	for _, spec := range []string{"{loops: {default: {scope: off}}}", "{workflows: {plain: {loop: false}}}"} {
		_, err := Decode([]byte("apiVersion: maku.ogenki.io/v1alpha1\nkind: Factory\nmetadata: {name: f}\nspec: " + spec + "\n"))
		if err == nil || !strings.Contains(err.Error(), `not a boolean`) || !strings.Contains(err.Error(), "spec.") {
			t.Errorf("%s: %v", spec, err)
		}
	}
}

// F5: Migrate refuses what Parse refuses, an empty factory name or a nameless entry among them.
func TestMigrateParsesFirst(t *testing.T) {
	for _, in := range []string{
		strings.Replace(string(file(t)), "factory: aws-0", "factory: \"\"", 1),
		strings.Replace(string(file(t)), "- name: Smana/cloud-native-ref", "- name: \"\"", 1),
	} {
		if _, _, err := Migrate([]byte(in), "ns"); err == nil {
			t.Errorf("Migrate must refuse what Parse refuses:\n%.200s", in)
		}
	}
}

func TestNamePartIsADNSLabel(t *testing.T) {
	for in, want := range map[string]string{
		"Smana/cloud-native-ref": "cloud-native-ref",
		"Org/My_Repo.go":         "my-repo-go",
		"Org/.dotfiles_":         "dotfiles",
		"plain":                  "plain",
	} {
		if got := namePart(in); got != want {
			t.Errorf("namePart(%q) = %q, want %q", in, got, want)
		}
	}
}

// classesUnderDefaults is each evaluated class as the default guardrails leave it, as Equal
// compares them. Disabled classes no longer serve and do not affect a gate decision.
func classesUnderDefaults(cs map[string]config.Class) map[string]config.Class {
	out := make(map[string]config.Class, len(cs))
	for n, c := range cs {
		if c.Live || c.Shadow {
			out[n] = underDefaults(n, c)
		}
	}
	return out
}

// WF9, WF13: migrate rewrites the workflow settings release N warned about: harnessProfiles is
// written workflows, a workflow's trio is pair, and the dead keys are dropped.
func TestMigrateRewritesTheRemovedWorkflowSettings(t *testing.T) {
	old := strings.Replace(string(file(t)), "workflows:", "harnessProfiles:", 1)
	old = strings.Replace(old, "defaults: {", "defaults: {template: solo, tier: standard, ", 1)
	old = strings.Replace(old, "triage: {", `triage: {classifierURL: "http://c7", `, 1)
	old = strings.Replace(old, "templates:\n", "templates:\n  trio: {roles: [implementer, reviewer]}\n", 1)
	if _, err := config.Parse([]byte(old)); err == nil {
		t.Fatal("the old file parses: the edits did not apply")
	}
	manifests, _, err := Migrate([]byte(old), "ns")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"harnessProfiles", "trio", "classifierURL"} {
		if strings.Contains(string(manifests), gone) {
			t.Errorf("%s is written:\n%s", gone, manifests)
		}
	}
	objs, err := Decode(manifests)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := object(objs, KindFactory, "aws-0").Spec["defaults"].(map[string]any); d["template"] != nil || d["tier"] != nil {
		t.Errorf("defaults: %v", d)
	}
	if !strings.Contains(string(manifests), "workflows:") {
		t.Errorf("workflows is not written:\n%s", manifests)
	}
}

// MR9, MR13: the v0.47.0 ai-native-ref fixture and every field release N deprecated migrate to
// objects and residual config that Assemble accepts with no problem.
func TestMigrateRewritesEveryRemovedField(t *testing.T) {
	legacy, err := os.ReadFile("testdata/anr-legacy-config.json")
	if err != nil {
		t.Fatal(err)
	}
	manifests, residual, err := Migrate(legacy, "agent-system")
	if err != nil {
		t.Fatalf("Migrate(anr-legacy-config.json): %v", err)
	}
	for _, gone := range []string{"autoMergesPerDay", "minPromotionEvidence", "harnessAllowed", "requires:"} {
		if strings.Contains(string(manifests), gone) || strings.Contains(string(residual), gone) {
			t.Errorf("%s survived migration", gone)
		}
	}
	for _, gone := range []string{`mode: "off"`, "mode: off"} {
		if strings.Contains(string(manifests), gone) {
			t.Errorf("%s survived in manifests:\n%s", gone, manifests)
		}
	}
	objs, err := Decode(manifests)
	if err != nil {
		t.Fatalf("Decode(Migrate(anr-legacy-config.json)): %v\n%s", err, manifests)
	}
	got, err := Assemble(residual, objs)
	if err != nil || len(got.Refused) != 0 {
		t.Fatalf("Assemble: err=%v, refused=%+v", err, got.Refused)
	}

	// Also exercise every class-workflow move, policyManagement switch and guardrail removal on a single file.
	var synMap map[string]any
	if err := yaml.Unmarshal(file(t), &synMap); err != nil {
		t.Fatal(err)
	}
	synMap["policyManagement"] = map[string]any{"promotion": true, "read": true, "operators": []any{"Smana"}}
	synMap["mergeDefaults"] = map[string]any{"autoMergesPerDay": 3}
	synMap["mergeGuardrails"] = map[string]any{"minPromotionEvidence": 15}
	synMap["workflows"] = map[string]any{"plain": map[string]any{"template": "solo"}}
	repoEntry := synMap["repositories"].([]any)[0].(map[string]any)
	delete(repoEntry, "workflow")
	repoEntry["schedules"] = []any{map[string]any{"name": "weekly-links", "cron": "0 6 * * 1", "class": "docs-links", "text": "check links"}}
	mergeEntry := repoEntry["merge"].(map[string]any)
	delete(mergeEntry, "maxPerDay")
	mergeEntry["autoMergesPerDay"] = 4
	mergeEntry["contractCheck"] = "Test contract 🧪"
	repoEntry["classes"] = map[string]any{
		"docs-links": map[string]any{"live": true, "holdoutChecks": []any{"Check the documentation links 🔗"}, "harness": "plain", "harnessAllowed": []any{"careful"}, "escalation": map[string]any{"notConverged": "careful"}, "checks": true, "testFirst": true},
		"revert":     map[string]any{"live": true, "holdoutChecks": []any{"Check the documentation links 🔗"}},
		"docs":       map[string]any{},
		"scripts":    map[string]any{},
		"review":     map[string]any{"harness": "careful"},
	}
	synthetic, err := yaml.Marshal(synMap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Parse(synthetic); err == nil {
		t.Fatal("Parse accepted a file with removed fields")
	}
	m2, r2, err := Migrate(synthetic, "agent-system")
	if err != nil {
		t.Fatalf("Migrate(synthetic): %v", err)
	}
	var top map[string]any
	if err := yaml.Unmarshal(r2, &top); err != nil {
		t.Fatal(err)
	}
	if pm, _ := top["policyManagement"].(map[string]any); pm["stage"] != "manage" || pm["promotion"] != nil || pm["read"] != nil {
		t.Fatalf("policyManagement = %v", pm)
	}
	objs2, err := Decode(m2)
	if err != nil {
		t.Fatalf("Decode(synthetic): %v\n%s", err, m2)
	}
	got2, err := Assemble(r2, objs2)
	if err != nil || len(got2.Refused) != 0 {
		t.Fatalf("Assemble(synthetic): err=%v, refused=%+v", err, got2.Refused)
	}
	repo := got2.Config.Repos()[0]
	if repo.DefaultWorkflow() != "careful" || !repo.Workflow.TestFirst {
		t.Fatalf("Repository.Workflow = %+v, want default=careful testFirst=true", repo.Workflow)
	}
	if p := got2.Config.HarnessProfiles["plain"]; !p.Checks || p.Escalation == nil || p.Escalation.NotConverged != "careful" {
		t.Fatalf("workflows.plain = %+v, want checks=true and escalation.notConverged=careful", p)
	}
	if repo.Merge.MaxPerDay != 4 || got2.Config.MergeDefaults == nil || got2.Config.MergeDefaults.MaxPerDay != 3 {
		t.Fatalf("maxPerDay: repo=%d defaults=%+v", repo.Merge.MaxPerDay, got2.Config.MergeDefaults)
	}
	if len(repo.Schedules) != 1 || repo.Schedules[0].Class != "" || repo.Schedules[0].WorkflowOr() != "plain" {
		t.Fatalf("schedule = %+v, want class cleared and workflow=plain", repo.Schedules)
	}
	if _, ok := repo.Classes["scripts"]; ok {
		t.Fatalf("unarmed non-catalog class scripts kept: %+v", repo.Classes)
	}
}
