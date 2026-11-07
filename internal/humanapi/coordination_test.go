// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/runrequest"
)

// coordDraft is the model's two-repository reading of "add a v1 schema and make the consumer read
// it": the reply's content, before the chat envelope.
const coordDraft = `{"title":"Add and consume the v1 consumer schema","primaryRationale":"The integration fixture lives here.",
"children":[
 {"id":"contract","repository":"smana/public-contracts","rationale":"Publish the schema.","class":"scripts",
  "scope":{"task":"Add the consumer v1 JSON schema.","touches":["schemas/consumer-v1.json"],"plan":["Define the schema"],
   "acceptance":[{"check":"task test","proves":"The schema validates"}]},
  "produces":[{"name":"consumer-v1-schema","path":"schemas/consumer-v1.json","maxBytes":65536}]},
 {"id":"consumer","repository":"smana/public-consumer","rationale":"Read the schema.","class":"scripts",
  "scope":{"task":"Validate the consumer against the landed schema.","touches":["internal/consumer"],"plan":["Add the check"],
   "acceptance":[{"check":"go test ./internal/consumer","proves":"The consumer accepts v1"}]},
  "dependsOn":["contract"],"consumes":[{"from":"contract","artifact":"consumer-v1-schema","required":true}]}],
"mergeOrder":["contract","consumer"],
"integration":{"acceptance":[{"check":"tests/integration/consumer","proves":"The two heads work together"}]}}`

// coordReply is coordDraft in the gateway's envelope.
var coordReply = func() []byte {
	content, err := json.Marshal(coordDraft)
	if err != nil {
		panic(err)
	}
	return []byte(`{"choices":[{"message":{"content":` + string(content) + `}}],"usage":{"prompt_tokens":900,"completion_tokens":700}}`)
}()

// tripwires are every writer port the server holds: a factory task, a run request, a log append and
// a Room write. coordination_plan must touch none of them.
type tripwires struct {
	tasks, runs, roomWrites atomic.Int64
}

func (w *tripwires) CreateTask(context.Context, runrequest.TaskRequest) (runrequest.TaskResult, error) {
	w.tasks.Add(1)
	return runrequest.TaskResult{}, nil
}

type tripRequester struct{ w *tripwires }

func (r tripRequester) Request(context.Context, runrequest.Request) (runrequest.Result, error) {
	r.w.runs.Add(1)
	return runrequest.Result{}, nil
}

// tripLog is an ActLog that panics on any call: the nil interface it embeds has no method to run.
type tripLog struct{ ActLog }

func (w *tripwires) touched() int64 { return w.tasks.Load() + w.runs.Load() + w.roomWrites.Load() }

// withCoordination is the planner with coordination_plan on in mode: two public repositories the
// allowlist serves (dev and the Slack users u2 and dev-via-Slack write the first, nobody needs to
// write the second), a private one it also serves, the scripts class, and every writer a tripwire.
func withCoordination(t *testing.T, mode string, trips *tripwires) option {
	t.Helper()
	set := intakeOption(t, func(f *fakeIntake, in *Intake) {
		f.coordReply = coordReply
		f.links["T01ABC/U04DEV"] = "dev"
		in.Allow = map[string]bool{"Smana/ai-native-ref": true, "Smana/public-contracts": true, "Smana/public-consumer": true,
			"Smana/private-x": true}
		in.Public = publicRepos{"smana/ai-native-ref": true, "smana/public-contracts": true, "smana/public-consumer": true}
		in.Classes["scripts"] = intake.Class{Roles: []string{"scoper", "implementer", "reviewer"}, CeilingUSD: 5.1, Source: "class:scripts.taskUSD"}
	})
	return func(s *Server, h *fanout.Hub, v *hubView) {
		set(s, h, v)
		w := world(nil)
		w.set(func() {
			w.writes = map[string][]string{"smana/ai-native-ref": {"dev", "u2"}, "smana/public-contracts": {"dev", "u2"}}
		})
		w.gate(s)
		s.Coordination = &CoordinationPlanner{Mode: mode, Resolve: ClassPolicy(s.Intake.Classes),
			NewID: func() string { return "p4zt6wce" }}
		s.Tasks = trips
		rooms := roomClient(t)
		count := func() error { trips.roomWrites.Add(1); return nil }
		s.Actor = &Actor{Log: tripLog{}, Requester: tripRequester{trips}, Rooms: interceptorClient(rooms, count)}
	}
}

// interceptorClient counts every write on c.
func interceptorClient(c client.WithWatch, count func() error) client.Client {
	return interceptor.NewClient(c, interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error { return count() },
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error { return count() },
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			return count()
		},
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error { return count() },
	})
}

func coordArgs(extra ...string) map[string]any {
	a := map[string]any{"text": "add a v1 schema and make the consumer read it", "id": "p4zt6wce",
		"candidates": []string{"Smana/public-contracts", "Smana/public-consumer"}, "primary": "Smana/public-contracts"}
	for i := 0; i+1 < len(extra); i += 2 {
		a[extra[i]] = extra[i+1]
	}
	return a
}

// coordAnswer is coordination_plan's structuredContent.
type coordAnswer struct {
	Plan        json.RawMessage           `json:"plan"`
	Hash        string                    `json:"hash"`
	Diagnostics []coordination.Diagnostic `json:"diagnostics"`
	Planning    struct {
		AttemptID          string `json:"attemptID"`
		EffectiveRequester string `json:"effectiveRequester"`
	} `json:"planning"`
}

func decodeCoord(t *testing.T, got toolCall) coordAnswer {
	t.Helper()
	if got.isError {
		t.Fatalf("%s", got.text)
	}
	var a coordAnswer
	if err := json.Unmarshal(got.structured, &a); err != nil {
		t.Fatalf("%v: %s", err, got.structured)
	}
	return a
}

// MR4, O12: a session and the Slack App, on behalf of the same linked person, reach one governed
// route and get one typed plan: the same plan, hash and diagnostics, one call each, with each
// attempt recorded under its own effective requester.
func TestCoordinationPlanSessionAndSponsoredParity(t *testing.T) {
	trips := &tripwires{}
	e := setup(t, mcpOn, withSponsored(sponsoredP), withCoordination(t, "plan", trips))
	session := decodeCoord(t, call(t, e, viaRoomctl("dev"), "coordination_plan", coordArgs()))
	if e.intake.calls() != 1 {
		t.Fatalf("%d calls", e.intake.calls())
	}
	slack := decodeCoord(t, call(t, e, bearer("slack-app"), "coordination_plan", coordArgs("on_behalf_of", "slack:T01ABC/U04DEV")))
	if e.intake.calls() != 2 {
		t.Fatalf("%d calls", e.intake.calls())
	}
	if session.Hash != slack.Hash || string(session.Plan) != string(slack.Plan) || len(session.Diagnostics) != len(slack.Diagnostics) {
		t.Fatalf("session and Slack disagree:\n%s\n%s", session.Plan, slack.Plan)
	}
	if session.Planning.EffectiveRequester != "dev" || slack.Planning.EffectiveRequester != "slack:T01ABC/U04DEV" ||
		session.Planning.AttemptID == "" {
		t.Fatalf("%+v %+v", session.Planning, slack.Planning)
	}
	// The plan is the coordination contract, never plan/v1, and its hash is the canonical one.
	var plan struct {
		APIVersion, Kind string
		Children         []struct {
			Repository, Maintainer string
			Roles                  []string
			BudgetMicroUSD         int64
		}
		Budget struct{ TotalMicroUSD int64 }
	}
	if err := json.Unmarshal(session.Plan, &plan); err != nil || plan.Kind != "CoordinationPlan" || len(plan.Children) != 2 {
		t.Fatalf("%v %s", err, session.Plan)
	}
	// Roles from the class table without its scoper (a child's scope is the plan's), money from its
	// ceiling: 510000 planning + 2 × 5100000 + 4 × 510000 integration.
	if c := plan.Children[0]; c.Maintainer != "github:dev" || strings.Join(c.Roles, ",") != "implementer,reviewer" ||
		c.BudgetMicroUSD != 5100000 || plan.Budget.TotalMicroUSD != 12750000 {
		t.Fatalf("%+v %+v", c, plan.Budget)
	}
	if len(session.Hash) != 64 {
		t.Fatalf("hash %q", session.Hash)
	}
	// The sponsored principal plans for a linked user only, and a session cannot claim one.
	if got := call(t, e, bearer("slack-app"), "coordination_plan", coordArgs()); !got.isError || !strings.Contains(got.text, "on_behalf_of") {
		t.Fatalf("%s", got.text)
	}
	if got := call(t, e, viaRoomctl("dev"), "coordination_plan", coordArgs("on_behalf_of", "slack:T01ABC/U04DEV")); !got.isError {
		t.Fatalf("%s", got.text)
	}
	if trips.touched() != 0 {
		t.Fatalf("a writer was touched: %d", trips.touched())
	}
}

// CD18, design §budget: coordination_plan spends intake_plan's daily counter, exactly at N and N+1;
// the refusal makes no call.
func TestCoordinationPlanUsesExistingDailyCounter(t *testing.T) {
	e := setup(t, mcpOn, withCoordination(t, "plan", &tripwires{}), withIntakeDaily(t, 2),
		withIntakeOnHour(t, time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)))
	if got := call(t, e, viaRoomctl("dev"), "intake_plan", planArgs()); got.isError {
		t.Fatalf("intake_plan: %s", got.text)
	}
	decodeCoord(t, call(t, e, viaRoomctl("dev"), "coordination_plan", coordArgs()))
	for _, tool := range []string{"coordination_plan", "intake_plan"} {
		args := planArgs()
		if tool == "coordination_plan" {
			args = coordArgs()
		}
		if got := call(t, e, viaRoomctl("dev"), tool, args); !got.isError || !strings.Contains(got.text, "daily_plans_reached") {
			t.Fatalf("%s at N+1: %s", tool, got.text)
		}
	}
	if e.intake.calls() != 2 {
		t.Fatalf("%d calls", e.intake.calls())
	}
}

// Side effects: none. Planning files no issue, creates no Task or run, appends to no room log and
// writes no Room; and the audit line carries no request or plan text.
func TestCoordinationPlanHasNoSideEffects(t *testing.T) {
	trips := &tripwires{}
	e := setup(t, mcpOn, withCoordination(t, "plan", trips))
	events := func() int { e.log.mu.Lock(); defer e.log.mu.Unlock(); return len(e.log.evs) }
	before := events()
	decodeCoord(t, call(t, e, viaRoomctl("dev"), "coordination_plan", coordArgs()))
	if trips.touched() != 0 || events() != before {
		t.Fatalf("tasks %d, runs %d, room writes %d, log %d→%d", trips.tasks.Load(), trips.runs.Load(), trips.roomWrites.Load(),
			before, events())
	}
	logs := e.intake.logged()
	if !strings.Contains(logs, `"msg":"coordination plan"`) || !strings.Contains(logs, `"outcome":"planned"`) {
		t.Fatalf("no audit line: %s", logs)
	}
	for _, leak := range []string{"consumer read it", "Publish the schema", "consumer-v1"} {
		if strings.Contains(logs, leak) {
			t.Fatalf("the audit line carries text: %s", logs)
		}
	}
}

// MR16: off lists the tool on neither list and a call is an unknown tool; plan lists it on both;
// execute plans as plan does (the tool never files, so a missing execute prerequisite cannot matter
// here). intake_plan's and plan_check's tools/list entries are byte-identical either way.
func TestCoordinationPlanOffAndPlanModes(t *testing.T) {
	entries := func(e env, h http.Header) string {
		var out []string
		for _, tool := range tools(t, e, h) {
			if n := tool["name"]; n == "intake_plan" || n == "plan_check" {
				b, _ := json.Marshal(tool)
				out = append(out, string(b))
			}
		}
		return strings.Join(out, "\n")
	}
	off := setup(t, mcpOn, withSponsored(sponsoredP), withCoordination(t, "off", &tripwires{}))
	for _, h := range []http.Header{viaRoomctl("dev"), bearer("slack-app")} {
		if got := toolNames(t, off, h); strings.Contains(got, "coordination_plan") {
			t.Fatalf("off lists it: %s", got)
		}
	}
	if _, out := rpc(t, off, viaRoomctl("dev"), "tools/call", map[string]any{"name": "coordination_plan", "arguments": coordArgs()}); rpcCode(out) != -32602 || off.intake.calls() != 0 {
		t.Fatalf("off answered: %v", out)
	}
	for _, mode := range []string{"plan", "execute"} {
		trips := &tripwires{}
		on := setup(t, mcpOn, withSponsored(sponsoredP), withCoordination(t, mode, trips))
		for _, h := range []http.Header{viaRoomctl("dev"), bearer("slack-app")} {
			if got := toolNames(t, on, h); !strings.Contains(got, "coordination_plan") {
				t.Fatalf("%s does not list it: %s", mode, got)
			}
			if entries(on, h) != entries(off, h) {
				t.Fatalf("%s changed plan/v1's tools:\n%s\n%s", mode, entries(on, h), entries(off, h))
			}
		}
		decodeCoord(t, call(t, on, viaRoomctl("dev"), "coordination_plan", coordArgs()))
		if trips.touched() != 0 {
			t.Fatalf("%s touched a writer", mode)
		}
	}
	// Without the planner there is no coordination tool either, whatever the mode says.
	none := setup(t, mcpOn, func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.Coordination = &CoordinationPlanner{Mode: "plan", Resolve: ClassPolicy(nil)}
	})
	if got := toolNames(t, none, viaRoomctl("dev")); strings.Contains(got, "coordination_plan") {
		t.Fatalf("%s", got)
	}
}

// Design §seam: before any model call, every candidate is served and public and the requester
// writes the primary; a refusal names its reason and makes no call.
func TestCoordinationPlanGuardsBeforeTheCall(t *testing.T) {
	e := setup(t, mcpOn, withSponsored(sponsoredP), withCoordination(t, "plan", &tripwires{}))
	cases := []struct {
		name string
		h    http.Header
		args map[string]any
		want string
	}{
		{"a private candidate", viaRoomctl("dev"), func() map[string]any {
			a := coordArgs()
			a["candidates"] = []string{"Smana/public-contracts", "Smana/private-x"}
			return a
		}(), "private_repository_unsupported"},
		{"a candidate nobody serves", viaRoomctl("dev"), func() map[string]any {
			a := coordArgs()
			a["candidates"] = []string{"Smana/public-contracts", "Smana/elsewhere"}
			return a
		}(), "repository_not_allowed"},
		{"a primary the requester cannot write", viaRoomctl("dev"), coordArgs("primary", "Smana/public-consumer"), "no_write"},
		{"an unlinked Slack user", bearer("slack-app"), coordArgs("on_behalf_of", "slack:T01ABC/U09XYZ"), "not_linked"},
		{"a primary outside the candidates", viaRoomctl("dev"), coordArgs("primary", "Smana/ai-native-ref"), "invalid_arguments"},
		{"one candidate", viaRoomctl("dev"), func() map[string]any {
			a := coordArgs()
			a["candidates"] = []string{"Smana/public-contracts"}
			return a
		}(), "invalid_arguments"},
		{"a budget argument", viaRoomctl("dev"), func() map[string]any {
			a := coordArgs()
			a["budget"] = 99
			return a
		}(), "invalid_arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := e.intake.calls()
			got := call(t, e, tc.h, "coordination_plan", tc.args)
			if !got.isError || !strings.Contains(got.text, tc.want) || e.intake.calls() != before {
				t.Fatalf("%s (%d calls)", got.text, e.intake.calls()-before)
			}
			if strings.Contains(got.text, "private-x") || strings.Contains(got.text, "elsewhere") {
				t.Fatalf("the refusal names a repository: %s", got.text)
			}
		})
	}
}

// The route's deadline ends the call with a typed refusal, never a hang: on aws-0 the gateway's
// 20 s bound is the one a slow upstream meets.
func TestCoordinationPlanTimesOutTyped(t *testing.T) {
	e := setup(t, mcpOn, withCoordination(t, "plan", &tripwires{}), intakeOption(t, func(f *fakeIntake, in *Intake) {
		f.delay = 5 * time.Second
		in.Model.Deadline = 100 * time.Millisecond
	}))
	start := time.Now()
	got := call(t, e, viaRoomctl("dev"), "coordination_plan", coordArgs())
	if !got.isError || !strings.HasPrefix(got.text, "planner_timeout") || time.Since(start) > 3*time.Second {
		t.Fatalf("%s after %s", got.text, time.Since(start))
	}
}

// A model reply that breaks the plan contract is refused with its coordination reason.
func TestCoordinationPlanRefusesAnInvalidComposition(t *testing.T) {
	e := setup(t, mcpOn, withCoordination(t, "plan", &tripwires{}), intakeOption(t, func(f *fakeIntake, _ *Intake) {
		bad := strings.Replace(coordDraft, `"class":"scripts",
  "scope":{"task":"Validate`, `"class":"infra-admin",
  "scope":{"task":"Validate`, 1)
		content, _ := json.Marshal(bad)
		f.coordReply = []byte(`{"choices":[{"message":{"content":` + string(content) + `}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	got := call(t, e, viaRoomctl("dev"), "coordination_plan", coordArgs())
	if !got.isError || !strings.HasPrefix(got.text, "policy_unresolved") || strings.Contains(got.text, "infra-admin") {
		t.Fatalf("%s", got.text)
	}
}
