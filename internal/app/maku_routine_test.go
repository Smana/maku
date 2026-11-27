// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/Smana/maku/internal/factory/resources"
	"github.com/Smana/maku/internal/maku"
)

type routineTestCall struct {
	Method      string
	Path        string
	Query       string
	ContentType string
	Origin      string
	IdemKey     string
	Body        string
}

type fakeRoutineBroker struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []routineTestCall
	listJS string
	detail string
}

func newFakeRoutineBroker(t *testing.T) *fakeRoutineBroker {
	t.Helper()
	b := &fakeRoutineBroker{
		listJS: `{"routines":[{"repository":"Smana/cloud-native-ref","routine":"dead-links","description":"Fix dead links\u001b[31m","mode":"pr","workflow":"quick","probeKind":"dead-links","cron":"0 7 * * *","paused":false,"maxOpenPRs":1,"maxCostUSD":2,"openCount":1,"nextRunAt":"2026-11-27T07:00:00Z","sparkline":[{"date":"2026-11-26","merged":2,"closed":1,"noOp":0,"escalated":0}],"rollingMerged":6,"rollingClosed":4,"rollingNoOp":1,"mergeRate":0.6,"usdPerMerged":1.5,"promptEditURL":"https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory/upkeep/dead-links.md","promptSHA":"abcdef1234567890","breakerState":"ok","canWrite":true}]}`,
		detail: `{"repository":"Smana/cloud-native-ref","routine":"dead-links","description":"Fix dead links","mode":"pr","workflow":"quick","probeKind":"dead-links","cron":"0 7 * * *","paused":false,"maxOpenPRs":1,"maxCostUSD":2,"openCount":0,"nextRunAt":"2026-11-27T07:00:00Z","sparkline":[{"date":"2026-11-26","merged":2,"closed":0,"noOp":1,"escalated":0}],"rollingMerged":2,"rollingClosed":0,"rollingNoOp":1,"mergeRate":1,"usdPerMerged":0.75,"promptEditURL":"https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory/upkeep/dead-links.md","promptSHA":"abcdef1234567890","breakerState":"ok","canWrite":true,"defaultPrompt":"Fix dead links.","runs":[{"name":"task-1","createdAt":"2026-11-26T07:00:00Z","phase":"Done","outcome":"accepted","prURL":"https://github.com/Smana/cloud-native-ref/pull/12","prNumber":12,"roomRef":"3kq7x2ma","promptSHA":"abcdef1234567890","requestedBy":"system:scheduler","costUSD":0.75}],"skips":[{"slotKey":"2026-11-25T07:00","at":"2026-11-25T07:00:00Z","reason":"open_cap"}]}`,
	}
	b.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.calls = append(b.calls, routineTestCall{
			Method:      r.Method,
			Path:        r.URL.Path,
			Query:       r.URL.RawQuery,
			ContentType: r.Header.Get("Content-Type"),
			Origin:      r.Header.Get("Origin"),
			IdemKey:     r.Header.Get("Idempotency-Key"),
			Body:        string(raw),
		})
		listJS := b.listJS
		detail := b.detail
		b.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/routines":
			_, _ = w.Write([]byte(listJS))
		case r.Method == http.MethodGet && r.URL.Path == "/api/routines/Smana/cloud-native-ref/dead-links":
			_, _ = w.Write([]byte(detail))
		case r.Method == http.MethodPost && r.URL.Path == "/api/routines/Smana/cloud-native-ref/dead-links/runs":
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"bad_json"}`))
				return
			}
			var in struct {
				Mode           string `json:"mode"`
				IdempotencyKey string `json:"idempotencyKey"`
			}
			_ = json.Unmarshal(raw, &in)
			if in.IdempotencyKey == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"bad_idempotency_key"}`))
				return
			}
			if in.Mode == "propose" || in.Mode == "draft" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"mode_requires_phase2"}`))
				return
			}
			mode := in.Mode
			if mode == "" {
				mode = "pr"
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"task":"rt-manual-1","room":"3kq7x2ma","repository":"Smana/cloud-native-ref","routine":"dead-links","mode":"` + mode + `","requestedBy":"github:Smana"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/routines/Smana/cloud-native-ref/dead-links/pause":
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"bad_json"}`))
				return
			}
			var in struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(raw, &in)
			reason := in.Reason
			if reason == "" {
				reason = "manual"
			}
			_, _ = w.Write([]byte(`{"repository":"Smana/cloud-native-ref","routine":"dead-links","paused":true,"declarativePaused":false,"pausedReason":"` + reason + `","pausedBy":"github:smana"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/routines/Smana/cloud-native-ref/dead-links/resume":
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"bad_json"}`))
				return
			}
			_, _ = w.Write([]byte(`{"repository":"Smana/cloud-native-ref","routine":"dead-links","paused":false,"declarativePaused":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func routineApp(t *testing.T, b *fakeRoutineBroker) (Maku, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	if err := maku.SaveJSON(filepath.Join(dir, "config.json"), &maku.Config{URL: b.srv.URL, HumanMode: maku.HumanModeLocal}); err != nil {
		t.Fatal(err)
	}
	if err := maku.SaveJSON(filepath.Join(dir, "token.json"), &oauth2.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	return Maku{Dir: dir, HC: b.srv.Client(), Stream: b.srv.Client(), Out: &out, Err: &errOut}, &out, &errOut
}

func TestMakuRoutineListAndStatus(t *testing.T) {
	b := newFakeRoutineBroker(t)
	m, out, _ := routineApp(t, b)

	if err := m.Run(t.Context(), []string{"routine", "list", "--repo", "Smana/cloud-native-ref"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"REPO", "ROUTINE", "Smana/cloud-native-ref", "dead-links", "1/1", "60%", "$1.50", "2026-11-27T07:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("list missing %q in:\n%s", want, got)
		}
	}
	if b.calls[len(b.calls)-1].Query != "repo=Smana%2Fcloud-native-ref" {
		t.Fatalf("unexpected query: %q", b.calls[len(b.calls)-1].Query)
	}

	out.Reset()
	if err := m.Run(t.Context(), []string{"routine", "list", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"routines"`) {
		t.Fatalf("expected raw JSON, got:\n%s", out.String())
	}

	b.mu.Lock()
	b.listJS = `{"routines":[]}`
	b.mu.Unlock()
	out.Reset()
	if err := m.Run(t.Context(), []string{"routine", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no routines configured") {
		t.Fatalf("expected empty message, got:\n%s", out.String())
	}

	out.Reset()
	if err := m.Run(t.Context(), []string{"routine", "status", "Smana/cloud-native-ref", "dead-links"}); err != nil {
		t.Fatal(err)
	}
	got = out.String()
	for _, want := range []string{"dead-links", "Smana/cloud-native-ref", "quick", "0 7 * * *", "active", "100%", "$0.75", "abcdef123456"} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q in:\n%s", want, got)
		}
	}

	out.Reset()
	if err := m.Run(t.Context(), []string{"routine", "status", "Smana/cloud-native-ref", "dead-links", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"defaultPrompt"`) {
		t.Fatalf("status --json unexpected:\n%s", out.String())
	}
}

func TestMakuRoutineRunPauseResumeAndHistory(t *testing.T) {
	b := newFakeRoutineBroker(t)
	m, out, _ := routineApp(t, b)

	// Default run generates idempotency key and sends it in both header and body.
	if err := m.Run(t.Context(), []string{"routine", "run", "Smana/cloud-native-ref", "dead-links"}); err != nil {
		t.Fatal(err)
	}
	last := b.calls[len(b.calls)-1]
	if last.ContentType != "application/json" || last.Origin != "" || last.IdemKey == "" {
		t.Fatalf("unexpected POST headers: %+v", last)
	}
	if !strings.Contains(last.Body, `"idempotencyKey":"`+last.IdemKey+`"`) {
		t.Fatalf("expected idempotencyKey in body, got %s", last.Body)
	}
	if !strings.Contains(out.String(), "rt-manual-1") || !strings.Contains(out.String(), "3kq7x2ma") {
		t.Fatalf("run output missing task/room:\n%s", out.String())
	}

	// Explicit idempotency key and --json.
	out.Reset()
	if err := m.Run(t.Context(), []string{"routine", "run", "Smana/cloud-native-ref", "dead-links", "--mode", "pr", "--idempotency-key", "manual-123", "--json"}); err != nil {
		t.Fatal(err)
	}
	last = b.calls[len(b.calls)-1]
	if last.IdemKey != "manual-123" {
		t.Fatalf("expected Idempotency-Key manual-123, got %q", last.IdemKey)
	}
	if !strings.Contains(out.String(), `"task":"rt-manual-1"`) {
		t.Fatalf("run --json unexpected:\n%s", out.String())
	}

	// Phase 2 mode forwarded verbatim and surfaces mode_requires_phase2 (with digit '2' intact!).
	for _, mode := range []string{"propose", "draft"} {
		err := m.Run(t.Context(), []string{"routine", "run", "Smana/cloud-native-ref", "dead-links", "--mode", mode})
		if err == nil || !strings.Contains(err.Error(), "mode_requires_phase2") {
			t.Fatalf("--mode %s: expected mode_requires_phase2, got %v", mode, err)
		}
	}

	// Pause without reason sends valid {} JSON object.
	out.Reset()
	if err := m.Run(t.Context(), []string{"routine", "pause", "Smana/cloud-native-ref", "dead-links"}); err != nil {
		t.Fatal(err)
	}
	last = b.calls[len(b.calls)-1]
	if last.ContentType != "application/json" || last.Body != "{}" {
		t.Fatalf("pause without reason sent %q (%s)", last.Body, last.ContentType)
	}
	if !strings.Contains(out.String(), "paused") {
		t.Fatalf("pause output: %s", out.String())
	}

	// Pause with reason sends {"reason":...}.
	out.Reset()
	if err := m.Run(t.Context(), []string{"routine", "pause", "Smana/cloud-native-ref", "dead-links", "--reason", "noisy", "--json"}); err != nil {
		t.Fatal(err)
	}
	last = b.calls[len(b.calls)-1]
	if !strings.Contains(last.Body, `"reason":"noisy"`) {
		t.Fatalf("pause with reason body = %q", last.Body)
	}
	if !strings.Contains(out.String(), `"pausedReason":"noisy"`) {
		t.Fatalf("pause --json output = %s", out.String())
	}

	// Resume sends {} JSON object.
	out.Reset()
	if err := m.Run(t.Context(), []string{"routine", "resume", "Smana/cloud-native-ref", "dead-links"}); err != nil {
		t.Fatal(err)
	}
	last = b.calls[len(b.calls)-1]
	if last.ContentType != "application/json" || last.Body != "{}" {
		t.Fatalf("resume sent %q (%s)", last.Body, last.ContentType)
	}
	if !strings.Contains(out.String(), "resumed") {
		t.Fatalf("resume output: %s", out.String())
	}

	// History renders runs and skips.
	out.Reset()
	if err := m.Run(t.Context(), []string{"routine", "history", "Smana/cloud-native-ref", "dead-links"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"RUNS", "task-1", "accepted", "3kq7x2ma", "$0.75", "SKIPS", "open_cap"} {
		if !strings.Contains(got, want) {
			t.Errorf("history missing %q in:\n%s", want, got)
		}
	}

	// Empty runs and skips render cleanly.
	b.mu.Lock()
	b.detail = `{"repository":"Smana/cloud-native-ref","routine":"dead-links","mode":"pr","paused":false,"maxOpenPRs":1,"maxCostUSD":2,"openCount":0,"nextRunAt":null,"sparkline":[],"rollingMerged":0,"rollingClosed":0,"rollingNoOp":0,"mergeRate":null,"usdPerMerged":null,"promptEditURL":"https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory/upkeep/dead-links.md","breakerState":"ok","canWrite":true,"runs":[],"skips":[]}`
	b.mu.Unlock()
	out.Reset()
	if err := m.Run(t.Context(), []string{"routine", "history", "Smana/cloud-native-ref", "dead-links"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no runs yet") || !strings.Contains(out.String(), "no recent skips") {
		t.Fatalf("empty history output:\n%s", out.String())
	}
}

func TestMakuRoutineUsageAndValidationErrors(t *testing.T) {
	b := newFakeRoutineBroker(t)
	m, _, _ := routineApp(t, b)

	for _, args := range [][]string{
		{"routine"},
		{"routine", "unknown"},
		{"routine", "list", "extra"},
		{"routine", "list", "--repo", "not-a-repo"},
		{"routine", "status", "Smana/cloud-native-ref"},
		{"routine", "status", "badrepo", "dead-links"},
		{"routine", "status", "Smana/cloud-native-ref", "Bad_Routine"},
		{"routine", "run", "Smana/cloud-native-ref"},
		{"routine", "pause", "Smana/cloud-native-ref"},
		{"routine", "resume", "Smana/cloud-native-ref"},
		{"routine", "history", "Smana/cloud-native-ref"},
	} {
		if err := m.Run(t.Context(), args); err == nil {
			t.Errorf("%v: expected error, got nil", args)
		}
	}
}

func TestMakuConfigMigrateSchedules(t *testing.T) {
	baseRaw, err := os.ReadFile(configFixture)
	if err != nil {
		t.Fatal(err)
	}
	// Inject legacy schedules + dead-links class onto file.yaml's repository and add quick/standard workflows.
	withScheds := strings.Replace(string(baseRaw),
		"    classes: {docs-links: {shadow: true}, revert: {shadow: true}, docs: {}}\n",
		"    classes:\n      docs-links: {shadow: true}\n      revert: {shadow: true}\n      docs: {}\n      dead-links: {shadow: true, allowedFiles: ['^docs/.+\\.md$'], modifyOnly: true, diffShape: links-only, holdoutChecks: [validate], oracle: {probe: dead-links, workflow: dead-links.yaml, maxFindings: 10}}\n    schedules:\n      - name: dead-links\n        cron: \"0 7 * * *\"\n        class: dead-links\n        probe: dead-links\n        text: Fix dead links.\n      - name: renovate-red\n        cron: \"0 */6 * * *\"\n        probe: renovate-red\n        probeAuthor: \"mend[bot]\"\n        workflow: standard\n        dataClass: internal\n        text: Fix red renovate PRs.\n      - name: weekly-docs\n        cron: \"0 6 * * 1\"\n        text: Review docs.\n",
		1)
	withScheds = strings.Replace(withScheds,
		"workflows:\n",
		"workflows:\n  quick: {template: solo}\n  standard: {template: pair}\n",
		1)
	if !strings.Contains(withScheds, "schedules:") || !strings.Contains(withScheds, "quick:") {
		t.Fatal("failed to construct schedule fixture")
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(withScheds), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Stdout mode emits pure multi-document YAML including Routine CRs and Repository.spec.routines[] (no schedules),
	// and validates cleanly via maku config validate.
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	residual := filepath.Join(dir, "residual.yaml")
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", cfgPath, "--residual", residual}); err != nil {
		t.Fatalf("stdout migrate: %v\n%s", err, out.String())
	}
	yamlOut := out.String()
	if !strings.Contains(yamlOut, "kind: Routine") || !strings.Contains(yamlOut, "routines:") || strings.Contains(yamlOut, "schedules:") {
		t.Fatalf("unexpected migrated YAML:\n%s", yamlOut)
	}
	objs, err := resources.Decode(out.Bytes())
	if err != nil {
		t.Fatalf("stdout migrate did not produce pure YAML: %v\n%s", err, yamlOut)
	}
	var routineCount int
	for _, o := range objs {
		if o.Kind == resources.KindRoutine {
			routineCount++
		}
	}
	if routineCount != 3 {
		t.Fatalf("expected 3 Routine objects, got %d", routineCount)
	}

	manifests := filepath.Join(dir, "manifests.yaml")
	if err := os.WriteFile(manifests, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"config", "validate", "--config", residual, manifests}); err != nil {
		t.Fatalf("validate migrated manifests: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "ok: ") {
		t.Fatalf("validate output: %s", out.String())
	}

	// 2. --out DIR writes individual manifests (routine-*.yaml, repository-*.yaml, etc.) and prints equivalence summary.
	outDir := filepath.Join(dir, "manifests-dir")
	out.Reset()
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", cfgPath, "--out", outDir}); err != nil {
		t.Fatalf("--out migrate: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "equivalent") {
		t.Fatalf("expected equivalence confirmation in output, got:\n%s", out.String())
	}
	for _, fname := range []string{
		"factory-aws-0.yaml",
		"repository-cloud-native-ref.yaml",
		"routine-dead-links.yaml",
		"routine-renovate-red.yaml",
		"routine-weekly-docs.yaml",
	} {
		if _, err := os.Stat(filepath.Join(outDir, fname)); err != nil {
			t.Errorf("missing %s in --out dir: %v", fname, err)
		}
	}

	// 3. --out DIR refuses overwrite without --force, succeeds with --force.
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", cfgPath, "--out", outDir}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected --force error on existing --out file, got %v", err)
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", cfgPath, "--out", outDir, "--force"}); err != nil {
		t.Fatalf("--out --force: %v", err)
	}

	// 4. Conflicting shared schedule across repos fails and writes nothing.
	conflictCfg := filepath.Join(dir, "conflict.yaml")
	conflictRaw := strings.Replace(withScheds,
		"repositories:\n",
		"repositories:\n  - name: Smana/other-repo\n    maintainers: [Smana]\n    triggerLabel: factory/ready\n    classes: {docs: {}}\n    schedules:\n      - name: weekly-docs\n        cron: \"0 6 * * 1\"\n        text: Different prompt.\n",
		1)
	if err := os.WriteFile(conflictCfg, []byte(conflictRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	badOutDir := filepath.Join(dir, "bad-out")
	badResidual := filepath.Join(dir, "bad-residual.yaml")
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", conflictCfg, "--out", badOutDir, "--residual", badResidual}); err == nil {
		t.Fatal("expected conflicting schedules migration to fail")
	}
	if _, err := os.Stat(badResidual); !os.IsNotExist(err) {
		t.Fatalf("failed migration wrote residual: %v", err)
	}
	if _, err := os.Stat(badOutDir); !os.IsNotExist(err) {
		t.Fatalf("failed migration wrote --out dir: %v", err)
	}

	// 5. Pre-existing non-overlapping disabled routine alongside schedules is preserved and validated.
	withBothRaw := strings.Replace(withScheds,
		"    schedules:\n",
		"    routines:\n      - name: extra-routine\n        mode: disabled\n        cron: \"0 4 * * 1\"\n    schedules:\n",
		1)
	if !strings.Contains(withBothRaw, "extra-routine") {
		t.Fatal("failed to construct config with both schedules and routines")
	}
	bothCfg := filepath.Join(dir, "both.yaml")
	if err := os.WriteFile(bothCfg, []byte(withBothRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	bothOutDir := filepath.Join(dir, "both-out")
	bothResidual := filepath.Join(dir, "both-residual.yaml")
	out.Reset()
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", bothCfg, "--out", bothOutDir, "--residual", bothResidual}); err != nil {
		t.Fatalf("migrate with both schedules and disabled routine: %v\n%s", err, out.String())
	}
	repoManifest, err := os.ReadFile(filepath.Join(bothOutDir, "repository-cloud-native-ref.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"extra-routine", "dead-links", "renovate-red", "weekly-docs"} {
		if !strings.Contains(string(repoManifest), want) {
			t.Errorf("migrated Repository manifest missing %q:\n%s", want, repoManifest)
		}
	}
	if strings.Contains(string(repoManifest), "schedules:") {
		t.Errorf("migrated Repository manifest still contains schedules:\n%s", repoManifest)
	}

	// 6. Pre-existing active routine whose Routine CR is external must fail explicitly (MissingRef), never silently drop and exit 0.
	withActiveRaw := strings.Replace(withBothRaw, "mode: disabled", "mode: pr", 1)
	activeCfg := filepath.Join(dir, "active-routine.yaml")
	if err := os.WriteFile(activeCfg, []byte(withActiveRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	activeOutDir := filepath.Join(dir, "active-out")
	activeResidual := filepath.Join(dir, "active-residual.yaml")
	out.Reset()
	err = m.run(t.Context(), []string{"config", "migrate", "--from", activeCfg, "--out", activeOutDir, "--residual", activeResidual})
	if err == nil || !strings.Contains(out.String(), "MissingRef") {
		t.Fatalf("expected MissingRef failure when active pre-existing routine CR is external, got err=%v\n%s", err, out.String())
	}
	if _, statErr := os.Stat(activeResidual); !os.IsNotExist(statErr) {
		t.Fatalf("failed migration wrote residual: %v", statErr)
	}
	if _, statErr := os.Stat(activeOutDir); !os.IsNotExist(statErr) {
		t.Fatalf("failed migration wrote --out dir: %v", statErr)
	}

	// 7. When --out points to an existing regular file, migrate fails and does NOT write --residual.
	outFile := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(outFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	resOnBadOut := filepath.Join(dir, "res-on-bad-out.yaml")
	out.Reset()
	if err := m.run(t.Context(), []string{"config", "migrate", "--from", cfgPath, "--out", outFile, "--residual", resOnBadOut}); err == nil {
		t.Fatal("expected migrate to fail when --out is a regular file")
	}
	if _, statErr := os.Stat(resOnBadOut); !os.IsNotExist(statErr) {
		t.Fatalf("failed migrate wrote --residual %s: %v", resOnBadOut, statErr)
	}
	if gotFile, _ := os.ReadFile(outFile); string(gotFile) != "occupied" {
		t.Fatalf("--out regular file was modified: %q", string(gotFile))
	}
}
