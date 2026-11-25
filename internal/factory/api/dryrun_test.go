// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/stages"
	"github.com/Smana/maku/internal/policy"
)

func postJSONAs(t *testing.T, h http.Handler, tok, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestADryRunStoresNothing proves that POST /v1/workflows/dry-run with a candidate YAML definition
// validates, compiles, and simulates in memory without storing the workflow or mutating config.
func TestADryRunStoresNothing(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC))
	tk := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot", Namespace: "agent-system"},
		Spec:       v1alpha1.TaskSpec{Repository: coordPrimary, PredictedClass: "docs", DataClass: "public"},
		Status: v1alpha1.TaskStatus{
			Phase:   v1alpha1.PhaseDone,
			EndedAt: &now,
			Outcome: "accepted",
			Profile: &v1alpha1.ProfileStatus{Name: "standard"},
			Usage:   v1alpha1.Usage{CostMicroUSD: 540_000},
			Stages: []v1alpha1.StageRecord{
				{Stage: "implement", Runs: 1, MicroUSD: 400_000},
				{Stage: "review", Runs: 1, MicroUSD: 140_000},
			},
		},
	}
	s := catalogueServer(false)
	s.Groups = policy.Groups{Member: "agents-member", Admin: "agents-admin"}
	member := coordHuman
	member.Groups = []string{"agents-member"}
	s.Auth = byToken{"member": member}
	sch := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(sch)
	s.Rooms = fake.NewClientBuilder().WithScheme(sch).WithObjects(tk).Build()
	s.Namespace = "agent-system"

	beforeWorkflows := len(s.Cfg.Current().HarnessProfiles)
	beforeRoles := len(s.Cfg.Current().Roles)
	h := s.Handler()

	candidateYAML := `roles:
  security-reviewer:
    capability: read
    brief: roles/security-reviewer.md
workflows:
  thorough:
    budget: frontier
    stages:
      - {role: implementer, stall: true}
      - {role: reviewer, maxRounds: 2}
      - {role: security-reviewer}
`
	w := postJSONAs(t, h, "member", "/v1/workflows/dry-run", stages.DryRunRequest{
		Workflow:   "thorough",
		Definition: candidateYAML,
		Repository: coordPrimary,
		Last:       20,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("dry-run status = %d body=%s", w.Code, w.Body.String())
	}
	var res stages.DryRunResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Workflow != "thorough" || res.Tasks != 1 || len(res.Rows) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if !res.Rows[0].Unknown {
		t.Fatalf("expected unknown estimate with 0 security-reviewer samples, got %+v", res.Rows[0])
	}

	// Config must be completely untouched: neither "thorough" nor "security-reviewer" was stored.
	if got := len(s.Cfg.Current().HarnessProfiles); got != beforeWorkflows {
		t.Fatalf("HarnessProfiles grew from %d to %d", beforeWorkflows, got)
	}
	if _, ok := s.Cfg.Current().Workflow("thorough"); ok {
		t.Fatal("candidate workflow thorough was stored in config")
	}
	if got := len(s.Cfg.Current().Roles); got != beforeRoles {
		t.Fatalf("Roles grew from %d to %d", beforeRoles, got)
	}

	// Calling dry-run again for "thorough" without a definition must return 404 unknown_workflow.
	w2 := postJSONAs(t, h, "member", "/v1/workflows/dry-run", stages.DryRunRequest{
		Workflow:   "thorough",
		Repository: coordPrimary,
	})
	if w2.Code != http.StatusNotFound {
		t.Fatalf("after dry-run without definition: status = %d, want 404 (body=%s)", w2.Code, w2.Body.String())
	}

	// Invalid candidate YAML is refused with 400 invalid_definition.
	w3 := postJSONAs(t, h, "member", "/v1/workflows/dry-run", stages.DryRunRequest{
		Workflow:   "broken",
		Definition: "stages: [{role: reviewer}, {role: implementer}]\n",
	})
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("invalid definition: status = %d, want 400 (body=%s)", w3.Code, w3.Body.String())
	}
}
