// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/upkeep"
	"github.com/Smana/maku/internal/policy"
)

type fakeBranchReader struct {
	files map[string]struct {
		text  string
		sha   string
		found bool
		err   error
	}
}

func (f fakeBranchReader) DefaultBranchFile(_ context.Context, path string) (text, sha string, found bool, err error) {
	v, ok := f.files[path]
	if !ok {
		return "", "", false, nil
	}
	return v.text, v.sha, v.found, v.err
}

func routineWriteTestServer(t *testing.T, uiWrite bool, now time.Time) (*Server, *upkeep.BindingsStore, *bytes.Buffer, *metric.ManualReader) {
	t.Helper()
	const coordEmpty = "Smana/empty-repo"
	repoCR := &factoryv1.Repository{
		ObjectMeta: metav1.ObjectMeta{Name: "public-contracts", Namespace: "agent-system"},
		Spec: factoryv1.RepositorySpec{
			FactoryRef:   "prod",
			Repository:   coordPrimary,
			Maintainers:  []string{"Alice", "Bob"},
			TriggerLabel: "factory/ready",
			Checks:       config.Checks{Egress: []string{"pypi"}},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(routineScheme()).WithObjects(repoCR).Build()
	st := &upkeep.StateStore{Client: cl, Reader: cl, Namespace: "agent-system", Now: func() time.Time { return now }}
	bs := &upkeep.BindingsStore{Client: cl, Reader: cl, Namespace: "agent-system", Now: func() time.Time { return now }}

	rd := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(rd))
	m, err := fmetrics.New(mp.Meter("test"), nil, "", func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	m.ListRepositories(coordPrimary, coordEmpty, coordOther)

	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, nil))

	upkeepBudget := 7.50
	s := &Server{
		Auth: byToken{
			"maint":    authn.Principal{Kind: envelope.ActorHuman, ID: "human:1", Sub: "1", Groups: []string{"agents-member"}},
			"nonmaint": authn.Principal{Kind: envelope.ActorHuman, ID: "human:2", Sub: "2", Groups: []string{"agents-member"}},
			"nowrite":  authn.Principal{Kind: envelope.ActorHuman, ID: "human:4", Sub: "4", Groups: []string{"agents-member"}},
			"nogroup":  authn.Principal{Kind: envelope.ActorHuman, ID: "human:5", Sub: "5"},
			"outsider": authn.Principal{Kind: envelope.ActorHuman, ID: "human:6", Sub: "6", Groups: []string{"agents-member"}},
			"machine":  authn.Principal{Kind: envelope.ActorSystem, ID: "system:broker", Sub: "sa"},
		},
		Groups: policy.Groups{Member: "agents-member", Admin: "agents-admin"},
		Cfg: &config.Config{
			Routines: config.RoutinesConfig{UIWrite: uiWrite},
			Budgets:  config.Budgets{FactoryDailyUSD: 40, UpkeepDailyUSD: &upkeepBudget},
			HarnessProfiles: map[string]config.HarnessProfile{
				"quick":    {},
				"standard": {},
				"docs":     {},
			},
			Generations: config.Generations{
				Repos: map[string]config.RepoGenerations{
					strings.ToLower(coordPrimary): {RepositoryName: "public-contracts", Repository: 2},
				},
			},
			ServedRoutines: map[string]config.ServedRoutine{
				"dead-links": {
					Generation: 3,
					Catalog:    true,
					Spec: config.RoutineSpec{
						Description:   "Fix broken markdown links",
						Workflow:      "docs",
						DataClass:     "public",
						DefaultPrompt: "Fix dead links.\n\n## Verify\n- check links\n",
						Probe:         config.ProbeSpec{Kind: config.ProbeDeadLinks, RequireConsecutive: 2, MaxFindings: 2},
						Requires:      []string{"oracleClass"},
						MaxOpenPRs:    2,
						MaxCostUSD:    3.5,
					},
				},
				"doc-drift": {
					Generation: 2,
					Catalog:    true,
					Spec: config.RoutineSpec{
						Description:   "Keep docs aligned with code",
						Workflow:      "standard",
						DataClass:     "public",
						DefaultPrompt: "Check docs drift.\n\n## Verify\n- run docs check\n",
						Probe:         config.ProbeSpec{Kind: config.ProbeNone},
						MaxOpenPRs:    1,
						MaxCostUSD:    2.0,
						Egress:        []string{"pypi"},
					},
				},
			},
			Repositories: []config.Repository{
				{
					Name:         coordPrimary,
					Maintainers:  []string{"Alice", "Bob"},
					TriggerLabel: "factory/ready",
					Checks:       config.Checks{Egress: []string{"pypi"}},
					Schedules: []config.Schedule{
						{Name: "legacy-sched", Cron: "0 1 * * 1", Text: "legacy"},
					},
					Routines: []config.RoutineBinding{
						{Name: "dead-links", Mode: config.RoutineModePR, Cron: "0 3 * * *", OracleClass: "dead-links"},
						{Name: "ui-bound", Mode: config.RoutineModePropose, Cron: "0 4 * * 1"},
					},
					UIRoutines: map[string]bool{"ui-bound": true},
					ResolvedRoutines: map[string]config.ResolvedRoutine{
						"dead-links": {
							Name:          "dead-links",
							Description:   "Fix broken markdown links",
							Mode:          config.RoutineModePR,
							Cron:          "0 3 * * *",
							Workflow:      "docs",
							DataClass:     "public",
							DefaultPrompt: "Fix dead links.",
							Probe:         config.ProbeSpec{Kind: config.ProbeDeadLinks, RequireConsecutive: 2, MaxFindings: 2},
							OracleClass:   "dead-links",
							Egress:        []string{"pypi"},
							MaxOpenPRs:    2,
							MaxCostUSD:    3.5,
							Cooldown:      24 * time.Hour,
							Breaker:       config.ResolvedBreaker{Window: 10, MinSamples: 5, MinMergeRate: 0.25, MaxConsecutiveClosed: 3, MaxConsecutiveFailures: 3},
							RepoPrompt:    true,
						},
						"ui-bound": {
							Name:          "ui-bound",
							Mode:          config.RoutineModePropose,
							Cron:          "0 4 * * 1",
							Workflow:      "standard",
							DataClass:     "public",
							DefaultPrompt: "UI bound.",
							Probe:         config.ProbeSpec{Kind: config.ProbeNone, RequireConsecutive: 1, MaxFindings: 1},
							MaxOpenPRs:    1,
							MaxCostUSD:    2.0,
							Breaker:       config.ResolvedBreaker{Window: 10, MinSamples: 5, MinMergeRate: 0.25, MaxConsecutiveClosed: 3, MaxConsecutiveFailures: 3},
							RepoPrompt:    true,
						},
					},
				},
				{
					Name:        coordEmpty,
					Maintainers: []string{"Alice"},
					Checks:      config.Checks{Egress: []string{"golang"}},
				},
				{
					Name:        coordOther,
					Maintainers: []string{"other"},
					ResolvedRoutines: map[string]config.ResolvedRoutine{
						"doc-drift": {
							Name:       "doc-drift",
							Mode:       config.RoutineModeDraft,
							Cron:       "0 5 * * *",
							MaxOpenPRs: 1,
							MaxCostUSD: 2.0,
							Breaker:    config.ResolvedBreaker{Window: 10},
						},
					},
				},
			},
		},
		Access: routineReadWriteAccess{
			reads: map[string]bool{
				"human:1|" + coordPrimary: true,
				"human:1|" + coordEmpty:   true,
				"human:2|" + coordPrimary: true,
				"human:4|" + coordPrimary: true,
				"human:5|" + coordPrimary: true,
				"human:1|" + coordOther:   false,
				"human:6|" + coordOther:   true,
			},
			writes: map[string]bool{
				"alice|" + coordPrimary: true,
				"alice|" + coordEmpty:   true,
				"bob|" + coordPrimary:   false,
			},
		},
		Logins: coordLogins{
			"1": "Alice",
			"2": "charlie",
			"4": "Bob",
			"6": "other",
		},
		Rooms:           cl,
		APIReader:       cl,
		Tasks:           cl,
		Namespace:       "agent-system",
		RoutineState:    st,
		RoutineBindings: bs,
		Metrics:         m,
		Log:             log,
		Now:             func() time.Time { return now },
	}
	return s, bs, &logBuf, rd
}

func TestRoutineCatalogAndDetailEnrichment(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, _, _, _ := routineWriteTestServer(t, true, now)
	s.RoutinePrompts = func(repo string) upkeep.DefaultBranchReader {
		if repo == coordPrimary {
			return fakeBranchReader{
				files: map[string]struct {
					text  string
					sha   string
					found bool
					err   error
				}{
					".github/agent-factory/upkeep/dead-links.md": {
						text:  "## Goal\nFix links.\n\n## Verify\n- check links\n",
						sha:   "abc",
						found: true,
					},
					".github/agent-factory/upkeep/ui-bound.md": {
						err: errors.New("github timeout"),
					},
				},
			}
		}
		return nil
	}
	h := s.Handler()

	// Auth checks on /v1/routines/catalog.
	if w := doRoutineReq(h, http.MethodGet, "", "/v1/routines/catalog", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth catalog = %d", w.Code)
	}
	if w := doRoutineReq(h, http.MethodGet, "machine", "/v1/routines/catalog", ""); w.Code != http.StatusForbidden {
		t.Fatalf("machine catalog = %d", w.Code)
	}
	if w := doRoutineReq(h, http.MethodGet, "nogroup", "/v1/routines/catalog", ""); w.Code != http.StatusForbidden {
		t.Fatalf("nogroup catalog = %d", w.Code)
	}

	// Maintainer sees coordPrimary + coordEmpty (0 bound routines!) and NEVER coordOther.
	w := doRoutineReq(h, http.MethodGet, "maint", "/v1/routines/catalog", "")
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", w.Code, w.Body.String())
	}
	var cat RoutineCatalogResponse
	if err := json.Unmarshal(w.Body.Bytes(), &cat); err != nil {
		t.Fatal(err)
	}
	if !cat.UIWrite || cat.UpkeepDailyUSD != 7.5 {
		t.Fatalf("catalog flags = %+v", cat)
	}
	if len(cat.Repositories) != 2 || cat.Repositories[0] != "Smana/empty-repo" || cat.Repositories[1] != coordPrimary {
		t.Fatalf("repositories = %v", cat.Repositories)
	}
	if len(cat.Routines) != 2 || cat.Routines[0].Name != "dead-links" || cat.Routines[1].Name != "doc-drift" {
		t.Fatalf("catalog routines = %+v", cat.Routines)
	}
	// doc-drift is bound only on coordOther which maint cannot read -> consumerCount=0.
	if cat.Routines[1].ConsumerCount != 0 || len(cat.Routines[1].Consumers) != 0 {
		t.Fatalf("doc-drift leaked unreadable consumer: %+v", cat.Routines[1])
	}
	// dead-links is bound on coordPrimary -> consumerCount=1.
	if cat.Routines[0].ConsumerCount != 1 || cat.Routines[0].Consumers[0].Repository != coordPrimary {
		t.Fatalf("dead-links consumers = %+v", cat.Routines[0])
	}
	if cat.Routines[0].Breaker.Window != 10 || cat.Routines[0].Breaker.MinMergeRate != 0.25 {
		t.Fatalf("breaker defaults = %+v", cat.Routines[0].Breaker)
	}

	// Enriched GET /v1/routines/{owner}/{name}/{routine} includes editor metadata.
	w = doRoutineReq(h, http.MethodGet, "maint", "/v1/routines/"+coordPrimary+"/dead-links", "")
	if w.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", w.Code, w.Body.String())
	}
	var det RoutineDetail
	if err := json.Unmarshal(w.Body.Bytes(), &det); err != nil {
		t.Fatal(err)
	}
	if !det.UIWrite || det.DefinedIn != "gitops" || det.Cooldown != "24h0m0s" || det.PromptPath != ".github/agent-factory/upkeep/dead-links.md" ||
		det.PromptExists == nil || !*det.PromptExists || !strings.Contains(det.RepoPromptText, "## Verify") ||
		det.Spec == nil || det.Spec.MaxOpenPRs != 2 || len(det.RepoEgress) != 1 || det.UpkeepDailyUSD != 7.5 {
		t.Fatalf("enriched detail mismatch: %+v", det)
	}

	// Best-effort prompt fetch error leaves PromptExists nil and returns 200.
	w = doRoutineReq(h, http.MethodGet, "maint", "/v1/routines/"+coordPrimary+"/ui-bound", "")
	if w.Code != http.StatusOK {
		t.Fatalf("ui-bound detail status=%d body=%s", w.Code, w.Body.String())
	}
	var detUI RoutineDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detUI); err != nil {
		t.Fatal(err)
	}
	if detUI.DefinedIn != "ui" || detUI.PromptExists != nil {
		t.Fatalf("expected definedIn=ui and nil PromptExists on error, got %+v", detUI)
	}

	// Unbound routine on GET detail still returns 404.
	if w := doRoutineReq(h, http.MethodGet, "maint", "/v1/routines/"+coordPrimary+"/doc-drift", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unbound routine GET = %d, want 404", w.Code)
	}
}

func TestPreviewRoutine(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, _, _, _ := routineWriteTestServer(t, false, now) // works even when uiWrite=false!
	h := s.Handler()
	previewURL := "/v1/routines/" + coordPrimary + "/preview"

	if w := doRoutineReq(h, http.MethodPost, "nonmaint", previewURL, `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("nonmaint preview = %d", w.Code)
	}
	if w := doRoutineReq(h, http.MethodPost, "nowrite", previewURL, `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("nowrite preview = %d", w.Code)
	}
	if w := doRoutineReq(h, http.MethodPost, "maint", previewURL, `{"binding":{"name":"doc-drift","mode":"propose","cron":"0 4 * * 1"},"extra":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown json field = %d", w.Code)
	}

	// Valid unbound routine + valid promptText -> valid=true, promptValid=true, Repository YAML returned.
	body := `{"binding":{"name":"doc-drift","mode":"propose","cron":"0 4 * * 1"},"promptText":"## Goal\nKeep docs fresh.\n\n## Verify\n- run docs\n"}`
	w := doRoutineReq(h, http.MethodPost, "maint", previewURL, body)
	if w.Code != http.StatusOK {
		t.Fatalf("valid preview = %d %s", w.Code, w.Body.String())
	}
	var pv RoutinePreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &pv); err != nil {
		t.Fatal(err)
	}
	if !pv.Valid || pv.PromptValid == nil || !*pv.PromptValid || pv.PromptError != "" {
		t.Fatalf("unexpected valid preview = %+v", pv)
	}
	if !strings.Contains(pv.YAML, "doc-drift") || !strings.Contains(pv.Manifest, "kind: Repository") || pv.ManifestPath != "repository-public-contracts.yaml" {
		t.Fatalf("unexpected preview YAML/manifest: %+v", pv)
	}
	var parsedRepo factoryv1.Repository
	if err := yaml.UnmarshalStrict([]byte(pv.Manifest), &parsedRepo); err != nil {
		t.Fatalf("rendered manifest invalid Repository YAML: %v\n%s", err, pv.Manifest)
	}

	// Missing ## Verify in promptText -> promptValid=false.
	badPrompt := `{"binding":{"name":"doc-drift","mode":"propose","cron":"0 4 * * 1"},"promptText":"## Goal\nNo verify section.\n"}`
	w = doRoutineReq(h, http.MethodPost, "maint", previewURL, badPrompt)
	if w.Code != http.StatusOK {
		t.Fatalf("bad prompt preview = %d %s", w.Code, w.Body.String())
	}
	pv = RoutinePreviewResponse{}
	_ = json.Unmarshal(w.Body.Bytes(), &pv)
	if !pv.Valid || pv.PromptValid == nil || *pv.PromptValid || !strings.Contains(pv.PromptError, "## Verify") {
		t.Fatalf("expected promptValid=false naming ## Verify, got %+v", pv)
	}

	// Widening past Routine ceiling -> valid=false, reason=OverRoutine.
	overBody := `{"binding":{"name":"doc-drift","mode":"propose","cron":"0 4 * * 1","maxOpenPRs":3}}`
	w = doRoutineReq(h, http.MethodPost, "maint", previewURL, overBody)
	if w.Code != http.StatusOK {
		t.Fatalf("over ceiling preview = %d %s", w.Code, w.Body.String())
	}
	pv = RoutinePreviewResponse{}
	_ = json.Unmarshal(w.Body.Bytes(), &pv)
	if pv.Valid || pv.Reason != config.ReasonOverRoutine || len(pv.Errors) == 0 {
		t.Fatalf("expected valid=false OverRoutine, got %+v", pv)
	}
}

func TestPutRoutineOptInAndValidation(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	t.Run("returns 404 when routines.uiWrite is false", func(t *testing.T) {
		s, _, _, _ := routineWriteTestServer(t, false, now)
		h := s.Handler()
		w := doRoutineReq(h, http.MethodPut, "maint", "/v1/routines/"+coordPrimary+"/doc-drift", `{"name":"doc-drift","mode":"propose","cron":"0 4 * * 1"}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("uiWrite=false PUT = %d %s, want 404", w.Code, w.Body.String())
		}
	})

	t.Run("enforces auth, DefinedTwice, ResolveRoutine, persists ConfigMap, kicks, logs and meters", func(t *testing.T) {
		s, bs, logBuf, rd := routineWriteTestServer(t, true, now)
		kicks := 0
		s.ConfigKick = func() { kicks++ }
		h := s.Handler()

		// Non-maintainer and no-write refused 403.
		if w := doRoutineReq(h, http.MethodPut, "nonmaint", "/v1/routines/"+coordPrimary+"/doc-drift", `{"mode":"propose","cron":"0 4 * * 1"}`); w.Code != http.StatusForbidden {
			t.Fatalf("nonmaint PUT = %d", w.Code)
		}
		if w := doRoutineReq(h, http.MethodPut, "nowrite", "/v1/routines/"+coordPrimary+"/doc-drift", `{"mode":"propose","cron":"0 4 * * 1"}`); w.Code != http.StatusForbidden {
			t.Fatalf("nowrite PUT = %d", w.Code)
		}

		// GitOps routine collision -> 409 defined_twice.
		w := doRoutineReq(h, http.MethodPut, "maint", "/v1/routines/"+coordPrimary+"/dead-links", `{"mode":"propose","cron":"0 4 * * 1","oracleClass":"dead-links"}`)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"defined_twice"`) {
			t.Fatalf("GitOps routine collision = %d %s, want 409 defined_twice", w.Code, w.Body.String())
		}
		// Schedule collision -> 409 defined_twice.
		w = doRoutineReq(h, http.MethodPut, "maint", "/v1/routines/"+coordPrimary+"/legacy-sched", `{"mode":"propose","cron":"0 4 * * 1"}`)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"defined_twice"`) {
			t.Fatalf("Schedule collision = %d %s, want 409 defined_twice", w.Code, w.Body.String())
		}

		// OverRoutine widening -> 400 over_routine.
		w = doRoutineReq(h, http.MethodPut, "maint", "/v1/routines/"+coordPrimary+"/doc-drift", `{"mode":"propose","cron":"0 4 * * 1","maxOpenPRs":4}`)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"over_routine"`) {
			t.Fatalf("OverRoutine PUT = %d %s, want 400 over_routine", w.Code, w.Body.String())
		}

		// Valid new unbound routine -> 200 OK, persists in BindingsStore, kicks configsource, logs, meters.
		w = doRoutineReq(h, http.MethodPut, "maint", "/v1/routines/"+coordPrimary+"/doc-drift", `{"mode":"propose","cron":"0 4 * * 1"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("valid PUT = %d %s", w.Code, w.Body.String())
		}
		if kicks != 1 {
			t.Fatalf("ConfigKick count = %d, want 1", kicks)
		}
		stored, err := bs.Get(t.Context(), coordPrimary)
		if err != nil || len(stored) != 1 || stored[0].Name != "doc-drift" || stored[0].Mode != config.RoutineModePropose {
			t.Fatalf("stored bindings = %+v err=%v", stored, err)
		}
		// Existing UI-managed routine (ui-bound) can also be updated.
		w = doRoutineReq(h, http.MethodPut, "maint", "/v1/routines/"+coordPrimary+"/ui-bound", `{"mode":"disabled"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("ui-bound update PUT = %d %s", w.Code, w.Body.String())
		}

		logOut := logBuf.String()
		if !strings.Contains(logOut, `"msg":"routine.ui_write"`) || !strings.Contains(logOut, `"actor":"Alice"`) || strings.Contains(logOut, "Check docs drift") {
			t.Fatalf("audit log = %s", logOut)
		}

		var rm metricdata.ResourceMetrics
		if err := rd.Collect(t.Context(), &rm); err != nil {
			t.Fatal(err)
		}
		var foundCounter bool
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "agent_factory_routine_config_writes_total" {
					foundCounter = true
				}
			}
		}
		if !foundCounter {
			t.Fatal("missing agent_factory_routine_config_writes_total metric")
		}
	})
}
