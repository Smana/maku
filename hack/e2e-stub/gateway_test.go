// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/intent"
)

// The gateway proof (plan v4 step 4, Task 12): the stub stands behind the model gateway as a provider,
// so the k3d test reads what the provider received, by role, and the stub answers the classifier.

func TestModelRecordsWhatTheProviderReceived(t *testing.T) {
	_, ts := newStubServer(t)
	keyFile := filepath.Join(t.TempDir(), "planner-key")
	if err := os.WriteFile(keyFile, []byte("the-gateway-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	planner := intake.Model{URL: ts.URL + "/v1", APIKeyFile: keyFile, Alias: "tier-frontier", ReasoningEffort: "low", HC: ts.Client(), Deadline: time.Second}
	if _, _, err := planner.Draft(t.Context(), intake.DraftRequest{Text: "Document the verification command.", Candidates: []string{"owner/repo"}, Classes: []string{"docs"}}); err != nil {
		t.Fatalf("planner request failed: %v", err)
	}
	status, raw := request(t, ts, http.MethodGet, "/__received", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /__received status = %d, body = %s", status, raw)
	}
	got := decodeResponse[[]map[string]any](t, raw)
	if len(got) != 1 {
		t.Fatalf("received = %#v, want one planner request", got)
	}
	sum := sha256.Sum256([]byte("Bearer the-gateway-key"))
	want := map[string]any{
		"role": "planner", "path": "/v1/chat/completions", "model": "tier-frontier",
		"temperature": float64(0), "top_p": absent, "reasoning_effort": "low",
		"authorization": "sha256:" + hex.EncodeToString(sum[:]),
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Errorf("received[0].%s = %#v, want %#v", k, got[0][k], v)
		}
	}
	if strings.Contains(string(raw), "the-gateway-key") {
		t.Fatal("GET /__received printed the bearer itself")
	}
}

func TestModelAnswersTheClassifier(t *testing.T) {
	stub, ts := newStubServer(t)
	keyFile := filepath.Join(t.TempDir(), "classifier-key")
	if err := os.WriteFile(keyFile, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	classifier := intent.Model{URL: ts.URL + "/v1", Alias: "tier-light", KeyFile: keyFile, HC: ts.Client(), Deadline: time.Second}
	answer, _, err := classifier.Classify(t.Context(), "where is it at?", "running")
	if err != nil {
		t.Fatalf("classifier request failed: %v", err)
	}
	result, outcome := intent.Parse(answer, "where is it at?", 0.5)
	if outcome != intent.OutcomeOK || result.Intent != intent.Status {
		t.Fatalf("classification = %+v, %s; want status", result, outcome)
	}
	if stub.ClassifierCalls() != 1 || stub.PlannerCalls() != 0 || stub.ModelCalls() != 1 {
		t.Fatalf("calls classifier/planner/total = %d/%d/%d", stub.ClassifierCalls(), stub.PlannerCalls(), stub.ModelCalls())
	}
	_, raw := request(t, ts, http.MethodGet, "/__counts", nil)
	if counts := decodeResponse[map[string]int](t, raw); counts["classifier"] != 1 {
		t.Fatalf("GET /__counts = %#v, want classifier 1", counts)
	}
}

func TestPlainHTTPServesTheSameStub(t *testing.T) {
	t.Setenv("E2E_STUB_HTTP_ADDR", "")
	if srv := newPlainServer(New().Handler()); srv != nil {
		t.Fatalf("newPlainServer with no E2E_STUB_HTTP_ADDR = %v, want none", srv.Addr)
	}
	t.Setenv("E2E_STUB_HTTP_ADDR", ":8080")
	stub := New()
	srv := newPlainServer(stub.Handler())
	if srv == nil || srv.Addr != ":8080" || srv.ReadHeaderTimeout != 5*time.Second || srv.WriteTimeout != 0 {
		t.Fatalf("newPlainServer = %+v", srv)
	}
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/__counts", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /__counts over http = %d", resp.StatusCode)
	}
}

// intakeRequest is the request intake.sh plans (its text=): it names the file it touches.
const intakeRequest = "Document the verification command in README.md."

// The broker's deterministic gap floor (internal/intake.ApplyFloor) adds a gap for each element a
// draft leaves out; the stub's draft must hold all three, so the scenario sees only the model's g1.
func TestModelPlannerDraftPassesTheGapFloor(t *testing.T) {
	var draft intake.Plan
	if err := json.Unmarshal([]byte(plannerDraft()), &draft); err != nil {
		t.Fatal(err)
	}
	draft.Gaps = nil // the floor ranks after the model's own gaps; count only its additions
	if got := intake.ApplyFloor(draft, intakeRequest); len(got.Gaps) != 0 {
		t.Fatalf("the floor added %d gaps to the stub's draft: %+v", len(got.Gaps), got.Gaps)
	}
}
