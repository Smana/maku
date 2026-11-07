// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/intake"
)

// exampleDraft is what the model may say about the design's example: the coherent two-repository
// reading and nothing a config or a person owns (no id, version, maintainer, tier, roles or money).
const exampleDraft = `{
  "title": "Add and consume the v1 consumer schema",
  "primaryRationale": "The integration fixture and the consumer contract live here.",
  "children": [
    {"id": "contract", "repository": "Smana/public-contracts", "rationale": "Publish the schema the consumer will read.",
     "class": "scripts",
     "scope": {"task": "Add the consumer v1 JSON schema and nothing else.", "touches": ["schemas/consumer-v1.json"],
       "leave": [".github", "docs/superpowers"], "plan": ["Define the schema", "validate its fixtures"], "assumptions": [],
       "acceptance": [{"check": "task test", "proves": "The schema and fixtures validate"}]},
     "produces": [{"name": "consumer-v1-schema", "path": "schemas/consumer-v1.json", "mediaType": "application/json", "maxBytes": 65536}]},
    {"id": "consumer", "repository": "smana/public-consumer", "rationale": "Read the published schema instead of duplicating it.",
     "class": "scripts",
     "scope": {"task": "Validate the consumer against the landed v1 schema.", "touches": ["internal/consumer"],
       "leave": [".github"], "plan": ["Fetch the recorded schema commit", "add the compatibility check"], "assumptions": [],
       "acceptance": [{"check": "go test ./internal/consumer", "proves": "The consumer accepts the v1 schema"}]},
     "dependsOn": ["contract"],
     "consumes": [{"from": "contract", "artifact": "consumer-v1-schema", "required": true}]}
  ],
  "mergeOrder": ["contract", "consumer"],
  "integration": {"beforeDependents": true, "acceptance": [{"check": "tests/integration/consumer \"$MAKU_COORDINATION_MANIFEST\"",
    "proves": "The exact proposed heads work together, then the exact merged heads still do"}]}
}`

// gateway is the one governed endpoint: it counts calls and answers draft in the chat envelope.
type gateway struct {
	server *httptest.Server
	key    string
	calls  atomic.Int64
	sent   atomic.Value // the last request body
}

func newGateway(t *testing.T, draft string) *gateway {
	t.Helper()
	content, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	reply := []byte(`{"choices":[{"message":{"content":` + string(content) + `}}],"usage":{"prompt_tokens":900,"completion_tokens":700}}`)
	g := &gateway{key: filepath.Join(t.TempDir(), "planner-key")}
	if err := os.WriteFile(g.key, []byte("k-planner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		g.sent.Store(string(b) + "\nAuthorization: " + r.Header.Get("Authorization"))
		_, _ = w.Write(reply)
	}))
	t.Cleanup(g.server.Close)
	return g
}

func (g *gateway) model() *intake.Model {
	return &intake.Model{URL: g.server.URL + "/v1", APIKeyFile: g.key, Alias: "tier-frontier", HC: httpx.New(3*time.Second, nil)}
}

// candidates are the guard's answer for the example: both public repositories, nothing else.
var candidates = []string{"Smana/public-contracts", "Smana/public-consumer"}

// plan is one attempt as the broker makes it: the one governed call with the coordination prompt,
// then the deterministic composition.
func plan(t *testing.T, g *gateway, pol *fakePolicies, attempt PlanningReconciliation) (PlanAttempt, error) {
	t.Helper()
	var d CoordinationDraft
	if _, err := g.model().Generate(t.Context(), intake.GovernedRequest{Text: "add a v1 schema and make the consumer read it",
		Candidates: candidates, Classes: []string{"scripts"}, SystemPrompt: SystemPrompt}, &d); err != nil {
		return PlanAttempt{}, err
	}
	return ComposeCoordination(t.Context(), d, ComposeInput{ID: "p4zt6wce", Version: 3, Primary: "smana/public-contracts",
		Maintainer: "github:Smana", Tier: "standard", Candidates: candidates, Resolve: pol.resolve, Attempt: attempt})
}

// MR4: one governed call yields a CoordinationPlan, not plan/v1. The model proposes the reading; the
// id, version, primary, maintainer, tier, roles and every amount are deterministic, so the result is
// the design's example (with the requester proposed as the second maintainer) to the hash.
func TestCoordinationPlannerComposesTheTypedPlan(t *testing.T) {
	g := newGateway(t, exampleDraft)
	pol := &fakePolicies{}
	got, err := plan(t, g, pol, PlanningReconciliation{AttemptID: "a1", EffectiveRequester: "Smana"})
	if err != nil {
		t.Fatal(err)
	}
	want := validPlan(t)
	want.Children[1].Maintainer = "github:Smana"
	if got.Hash != PlanHash(want) || got.Hash != PlanHash(got.Plan) {
		a, _ := Canonical(got.Plan)
		b, _ := Canonical(want)
		t.Fatalf("composed plan differs from the example:\n got %s\nwant %s", a, b)
	}
	if got.Plan.Budget.TotalMicroUSD != 12750000 || !slices.Equal(got.Plan.Children[0].Roles, []string{"implementer", "reviewer"}) {
		t.Fatalf("%+v", got.Plan)
	}
	if got.Reconciliation.AttemptID != "a1" || got.Reconciliation.EffectiveRequester != "Smana" {
		t.Fatalf("%+v", got.Reconciliation)
	}
	// The consumer's maintainer is a proposal its own maintainer confirms; the plan says so.
	if !slices.ContainsFunc(got.Diagnostics, func(d Diagnostic) bool {
		return d.Code == DiagnosticMaintainerProposed && d.Field == "children.consumer.maintainer"
	}) {
		t.Fatalf("%+v", got.Diagnostics)
	}
	// One governed call: the planner key, the pinned alias, temperature 0, the 4000-token cap.
	sent, _ := g.sent.Load().(string)
	for _, want := range []string{`"model":"tier-frontier"`, `"temperature":0`, `"max_tokens":4000`, "Authorization: Bearer k-planner",
		"Smana/public-consumer"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the call did not carry %s", want)
		}
	}
	if g.calls.Load() != 1 {
		t.Fatalf("%d calls", g.calls.Load())
	}
	// The resolver, never the model, is where class, tier and roles come from: per child repository.
	if !slices.Contains(pol.calls, "Smana/public-consumer|scripts|standard") {
		t.Fatalf("%v", pol.calls)
	}
}

// A plan-mode draft may hold one child: it composes, and says execute needs the second.
func TestCoordinationPlannerKeepsAnIncompleteDraft(t *testing.T) {
	var d map[string]any
	if err := json.Unmarshal([]byte(exampleDraft), &d); err != nil {
		t.Fatal(err)
	}
	d["children"] = d["children"].([]any)[:1]
	d["mergeOrder"] = []string{"contract"}
	b, _ := json.Marshal(d)
	got, err := plan(t, newGateway(t, string(b)), &fakePolicies{}, PlanningReconciliation{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Plan.Children) != 1 || !slices.ContainsFunc(got.Diagnostics, func(d Diagnostic) bool { return d.Code == ErrIncompletePlan.Error() }) {
		t.Fatalf("%+v", got)
	}
	// 510000 planning + 5100000 + 4 × 510000 integration runs.
	if got.Plan.Budget.TotalMicroUSD != 7650000 {
		t.Fatalf("total %d", got.Plan.Budget.TotalMicroUSD)
	}
}

// MR4, CD17: a reply that names authority (roles, money, a tier), a class the repository does not
// allow, a third repository, a repository outside the candidates, or more than the reply bound is
// refused whole after its one call; the outsider is never echoed.
func TestCoordinationPlannerRefusals(t *testing.T) {
	mutate := func(f func(d map[string]any)) string {
		var d map[string]any
		if err := json.Unmarshal([]byte(exampleDraft), &d); err != nil {
			t.Fatal(err)
		}
		f(d)
		b, _ := json.Marshal(d)
		return string(b)
	}
	childAt := func(d map[string]any, i int) map[string]any { return d["children"].([]any)[i].(map[string]any) }
	cases := []struct {
		name, draft string
		want        error
	}{
		{"model-named admin roles", mutate(func(d map[string]any) { childAt(d, 0)["roles"] = []string{"admin"} }), intake.ErrInvalid},
		{"model-named budget", mutate(func(d map[string]any) { childAt(d, 0)["budgetMicroUSD"] = 999 }), intake.ErrInvalid},
		{"model-named tier", mutate(func(d map[string]any) { childAt(d, 1)["tier"] = "frontier" }), intake.ErrInvalid},
		{"model-named maintainer", mutate(func(d map[string]any) { childAt(d, 1)["maintainer"] = "github:mallory" }), intake.ErrInvalid},
		{"unknown class", mutate(func(d map[string]any) { childAt(d, 1)["class"] = "infra-admin" }), ErrUnknownClass},
		{"third repository", mutate(func(d map[string]any) {
			third := map[string]any{}
			for k, v := range childAt(d, 1) {
				third[k] = v
			}
			third["id"], third["repository"] = "third", "Smana/public-third"
			d["children"] = append(d["children"].([]any), third)
		}), ErrInvalidPlan},
		{"private repository outside the candidates", mutate(func(d map[string]any) { childAt(d, 1)["repository"] = "Smana/private-secrets" }), ErrInvalidPlan},
		{"over-bound reply", mutate(func(d map[string]any) { d["title"] = strings.Repeat("x", 70<<10) }), intake.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGateway(t, tc.draft)
			_, err := plan(t, g, &fakePolicies{}, PlanningReconciliation{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "private-secrets") || strings.Contains(err.Error(), "infra-admin") {
				t.Fatalf("the refusal echoes the model's outsider: %v", err)
			}
			if g.calls.Load() != 1 {
				t.Fatalf("%d calls: no retry, no fallback", g.calls.Load())
			}
		})
	}
}

// Design invariant 7: the shared route's reservation and settlement ride along when supplied; an
// identifier without its amount is a cost nobody can reconcile.
func TestCoordinationPlannerPlanningCost(t *testing.T) {
	g := newGateway(t, exampleDraft)
	got, err := plan(t, g, &fakePolicies{}, PlanningReconciliation{AttemptID: "a1", ReservationID: "r1", ReservedMicroUSD: 40000,
		SettlementID: "s1", SettledMicroUSD: 31000})
	if err != nil || got.Reconciliation.ReservedMicroUSD != 40000 || got.Reconciliation.SettlementID != "s1" {
		t.Fatalf("%+v %v", got.Reconciliation, err)
	}
	for _, bad := range []PlanningReconciliation{{ReservationID: "r1"}, {SettlementID: "s1"}} {
		if _, err := plan(t, g, &fakePolicies{}, bad); !errors.Is(err, ErrPlanningCostUnverified) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}

// The composer keeps the version and id it is given and refuses a primary outside the candidates.
func TestCoordinationPlannerIdentity(t *testing.T) {
	var d CoordinationDraft
	if err := json.Unmarshal([]byte(exampleDraft), &d); err != nil {
		t.Fatal(err)
	}
	in := ComposeInput{ID: "abcdefgh", Version: 1, Primary: "Smana/public-consumer", Maintainer: "github:Smana", Tier: "standard",
		Candidates: candidates, Resolve: (&fakePolicies{}).resolve}
	got, err := ComposeCoordination(t.Context(), d, in)
	if err != nil || got.Plan.ID != "abcdefgh" || got.Plan.Version != 1 || got.Plan.Primary.Repository != "Smana/public-consumer" {
		t.Fatalf("%+v %v", got.Plan, err)
	}
	in.Primary = "Smana/elsewhere"
	if _, err := ComposeCoordination(t.Context(), d, in); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("%v", err)
	}
}
