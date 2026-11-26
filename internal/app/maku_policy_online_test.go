// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/meaning"
	"github.com/Smana/maku/internal/factory/policyview"
	makucli "github.com/Smana/maku/internal/maku"
)

func policyOnlineApp(t *testing.T) (Maku, *strings.Builder, *strings.Builder) {
	t.Helper()
	var out, errOut strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/policy/repositories/Smana/a/decisions":
			_, _ = w.Write([]byte(`{"repository":"Smana/a","since":"2026-11-01","decisions":[{"at":"2026-11-24T12:00:00Z","pullRequest":42,"head":"abc","outcome":"human","policy":"docs","reason":"patch_too_large","rows":{"counted":true,"files":2,"maxFiles":5,"lines":131,"maxLines":100},"commentURL":"https://github.com/Smana/a/pull/42"}]}`))
		case "/api/policy/repositories/Smana/a/decisions/42":
			_, _ = w.Write([]byte(`{"repository":"Smana/a","pullRequest":42,"decisions":[{"at":"2026-11-24T12:00:00Z","head":"abc","class":"docs","verdict":"human","reason":"patch_too_large","steps":[{"row":"condition: modified lines","status":"refused","detail":"131/100"}],"commentURL":"https://github.com/Smana/a/pull/42"}]}`))
		case "/api/policy/repositories/Smana/a":
			_, _ = w.Write(policyReportJSON(t))
		case "/api/policy/repositories/Smana/b/decisions/7":
			_, _ = w.Write(timelineJSON(t))
		case "/api/policy/repositories/Smana/a/backtest":
			_, _ = w.Write([]byte(`{"valid":true,"repository":"Smana/a","since":"2026-10-25","counts":{"considered":1,"changed":1,"unchanged":0,"unknown":0,"matched":0},"rows":[{"pullRequest":42,"url":"https://github.com/Smana/a/pull/42","policy":"docs","before":{"verdict":"shadow","reason":"shadow_would_arm"},"after":{"verdict":"human","reason":"patch_too_large"},"change":"changed"}]}`))
		case "/api/policy/promotions":
			_, _ = w.Write([]byte(`{"proposal":{"path":"repository-a.yaml","line":6,"file":"kind: Repository\n"},"command":"gh pr create --repo Smana/a","pullRequest":91,"pullRequestURL":"https://github.com/Smana/a/pull/91","branch":"factory/promote-Smana-a-docs"}`))
		case "/api/policy/repositories/Smana/a/drill":
			_, _ = w.Write([]byte(`{"number":42,"url":"https://github.com/Smana/a/pull/42"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	if err := makucli.SaveJSON(filepath.Join(dir, "config.json"), &makucli.Config{URL: srv.URL, HumanMode: makucli.HumanModeLocal}); err != nil {
		t.Fatal(err)
	}
	if err := makucli.SaveJSON(filepath.Join(dir, "token.json"), &oauth2.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return Maku{Dir: dir, HC: srv.Client(), Stream: srv.Client(), Out: &out, Err: &errOut}, &out, &errOut
}

func TestPolicyListAndExplain(t *testing.T) {
	m, out, _ := policyOnlineApp(t)
	if err := m.run(t.Context(), []string{"policy", "list", "--repository", "Smana/a", "--since", "2026-11-01"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#42", "human:patch_too_large", "docs", "2/5 files, 131/100 lines"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"policy", "explain", "Smana/a#42"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"abc", "docs", "human:patch_too_large", "condition: modified lines", "131/100"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
}

func TestPolicyListAndExplainJSON(t *testing.T) {
	m, out, _ := policyOnlineApp(t)
	if err := m.run(t.Context(), []string{"policy", "list", "--repository", "Smana/a", "--output", "json"}); err != nil {
		t.Fatal(err)
	}
	var recent policyview.Recent
	if err := json.Unmarshal([]byte(out.String()), &recent); err != nil || len(recent.Decisions) != 1 || recent.Decisions[0].Rows.Lines != 131 {
		t.Fatalf("%s %v", out.String(), err)
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"policy", "explain", "Smana/a#42", "--output", "json"}); err != nil {
		t.Fatal(err)
	}
	var timeline policyview.Timeline
	if err := json.Unmarshal([]byte(out.String()), &timeline); err != nil || timeline.PullRequest != 42 || len(timeline.Decisions) != 1 {
		t.Fatalf("%s %v", out.String(), err)
	}
}

func TestPolicyValidateJSONAndMigrateAliases(t *testing.T) {
	args := boundManifests(t)
	var out, errOut bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &errOut}
	if err := m.run(t.Context(), append([]string{"policy", "export"}, args...)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "maku config migrate export") {
		t.Fatalf("alias hint %q", errOut.String())
	}
	file := filepath.Join(t.TempDir(), "agent-factory.yaml")
	if err := os.WriteFile(file, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := m.run(t.Context(), append(append([]string{"policy", "validate", "--output", "json"}, args...), file)); err != nil {
		t.Fatal(err)
	}
	var report struct{ Valid bool }
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || !report.Valid {
		t.Fatalf("%s %v", out.String(), err)
	}
	out.Reset()
	if err := m.run(t.Context(), append([]string{"config", "migrate", "check"}, append(args, file)...)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "equivalent") {
		t.Fatalf("%s", out.String())
	}
}

func TestPolicyPRValidation(t *testing.T) {
	for _, value := range []string{"bad", "owner/name#0", "owner/name#x", "owner#1"} {
		if _, _, err := policyPR(value); err == nil {
			t.Errorf("%q accepted", value)
		}
	}
	if repo, pr, err := policyPR("owner/name#42"); err != nil || repo != "owner/name" || pr != 42 {
		t.Fatalf("%s %d %v", repo, pr, err)
	}
}

func TestPolicyBacktestPromoteAndDrill(t *testing.T) {
	m, out, _ := policyOnlineApp(t)
	file := filepath.Join(t.TempDir(), "agent-factory.yaml")
	if err := os.WriteFile(file, []byte("factory: aws-0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.run(t.Context(), []string{"policy", "backtest", file, "--repository", "Smana/a", "--since", "30d"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#42", "shadow:shadow_would_arm", "human:patch_too_large", "1 considered: 1 changed, 0 unchanged, 0 unknown", "matched none"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("backtest lacks %q in %s", want, out.String())
		}
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"policy", "promote", "docs", "--repository", "Smana/a"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://github.com/Smana/a/pull/91") {
		t.Fatalf("promotion: %s", out.String())
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"policy", "drill", "Smana/a#41"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://github.com/Smana/a/pull/42") {
		t.Fatalf("drill: %s", out.String())
	}
}

// The fixture policies of Smana/a: docs in shadow (prose), alert-rules in shadow (unchecked), docs-links live.
func fixtureMeanings() map[string]meaning.Sentence {
	in := func(name, mode string, cl config.Class) meaning.Sentence {
		return meaning.Build(meaning.Input{Name: name, Mode: mode, Class: cl})
	}
	return map[string]meaning.Sentence{
		"docs":        in("docs", "shadow", config.Class{AllowedFiles: []string{`^.+\.md$`}, MaxPatchFiles: 5}),
		"alert-rules": in("alert-rules", "shadow", config.Class{AllowedFiles: []string{`^observability/.+\.ya?ml$`}}),
		"docs-links":  in("docs-links", "live", config.Class{}),
	}
}

func policyReportJSON(t *testing.T) []byte {
	t.Helper()
	m := fixtureMeanings()
	ptr := func(s meaning.Sentence) *meaning.Sentence { return &s }
	rep := evidence.Report{Repository: "Smana/a", Classes: []evidence.ClassReport{
		{Class: "docs", Mode: "shadow", Meaning: ptr(m["docs"]), Evidence: &evidence.Evidence{Clean: 7, Minimum: 20, Drill: "none",
			Shadow: evidence.ShadowTally{Changed: 1}, Blockers: []string{"below_minimum", "no_drill"}}},
		{Class: "alert-rules", Mode: "shadow", Meaning: ptr(m["alert-rules"]), Evidence: &evidence.Evidence{Clean: 8, Minimum: 20}},
		{Class: "docs-links", Mode: "live", Meaning: ptr(m["docs-links"]), Evidence: &evidence.Evidence{Clean: 5, Minimum: 5,
			Outcomes: evidence.OutcomeTally{Merges: 34, Conclusive: 34}}},
	}}
	raw, err := json.Marshal(map[string]any{"report": rep})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func timelineJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(policyview.Timeline{Repository: "Smana/b", PullRequest: 7, Meanings: fixtureMeanings(), Decisions: []policyview.Decision{
		{Head: "abc", Class: "alert-rules", Verdict: "shadow", Reason: "shadow_would_arm", Signal: "none"},
		{Head: "abc", Class: "docs", Verdict: "human", Reason: "patch_too_large"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Correctness signals W8: explain says whether each policy held, what checked the meaning and, with
// no signal, how to add one; the rules of the next plan are not promised.
func TestPolicyExplainSaysWhatCheckedTheMeaning(t *testing.T) {
	m, out, _ := policyOnlineApp(t)
	if err := m.run(t.Context(), []string{"policy", "explain", "Smana/b#7"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"alert-rules", "holds", "meaning checked by: nothing", "To have its meaning checked: require a plan and tests.",
		"recorded as: would have merged. Merge it unchanged if you agree", "does not hold: ", "meaning checked by: nothing: this is prose"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Never merges without you") || strings.Contains(out.String(), "factory/wrong") {
		t.Errorf("a rule the gate does not apply is promised:\n%s", out.String())
	}
}

// W8, W1: a policy of a repository prints the sentence the factory built, its progress with the
// judge of every number, and each blocker with its next step.
func TestPolicyExplainOfAPolicy(t *testing.T) {
	m, out, _ := policyOnlineApp(t)
	if err := m.run(t.Context(), []string{"policy", "explain", "Smana/a", "docs"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{fixtureMeanings()["docs"].Plain(), "7 of 20 agreed: you merged 7 unchanged, changed 1. 0 wrong calls. Rollback drill: not done.",
		"Blocks promotion: Needs 13 more decisions you agree with.", "Blocks promotion: No rollback drill on this repository yet.",
		"docs/merge-policies.md#holds-agreed-signal-checked"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	if err := m.run(t.Context(), []string{"policy", "explain", "Smana/a", "nope"}); err == nil {
		t.Fatal("an unknown policy explained")
	}
}

// CS7: list leads with each policy, what checks it and its progress, then the decisions.
func TestPolicyListLeadsWithThePolicies(t *testing.T) {
	m, out, _ := policyOnlineApp(t)
	if err := m.run(t.Context(), []string{"policy", "list", "--repository", "Smana/a"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"CHECKED BY", "WRONG CALLS", "nothing (prose)", "7 of 20 agreed", "links-only", "34 merged alone, 34 quiet"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "POLICY") || strings.Index(got, "PR ") < strings.Index(got, "WRONG CALLS") {
		t.Errorf("the policies come first:\n%s", got)
	}
	if strings.Contains(strings.ToLower(got), "clean") || strings.Contains(strings.ToLower(got), "healthy") {
		t.Errorf("an old word:\n%s", got)
	}
}
