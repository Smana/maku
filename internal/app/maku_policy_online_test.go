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
