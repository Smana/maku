// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/runrequest"
)

// fakeTaskFactory answers CreateTask as the factory would, and keeps every request.
type fakeTaskFactory struct {
	mu     sync.Mutex
	answer runrequest.TaskResult
	err    error
	got    []runrequest.TaskRequest
}

func (f *fakeTaskFactory) CreateTask(_ context.Context, r runrequest.TaskRequest) (runrequest.TaskResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, r)
	return f.answer, f.err
}

func withTaskFactory(f TaskCreator) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) { s.Tasks = f }
}

// setTaskPhase appends the factory's task_state with phase, as the factory narrates one.
func setTaskPhase(e env, phase string) {
	e.log.mu.Lock()
	defer e.log.mu.Unlock()
	e.log.evs = append(e.log.evs, logEvent(int64(len(e.log.evs)+1), envelope.StateChanged,
		envelope.Actor{Kind: envelope.ActorSystem, ID: "system:factory"}, envelope.TaskStatePayload(envelope.TaskFacts{Phase: phase})))
}

// getTask is tasks/get's answer for id, as h, and the JSON-RPC error's code (0 on success).
func getTask(t *testing.T, e env, h http.Header, id string) (taskView, int) {
	t.Helper()
	_, out := rpc(t, e, h, "tasks/get", map[string]any{"taskId": id})
	var v taskView
	b, _ := json.Marshal(out["result"])
	_ = json.Unmarshal(b, &v)
	return v, rpcCode(out)
}

// startRunTask calls run_create as a task and returns the CreateTaskResult's task.
func startRunTask(t *testing.T, e env, h http.Header, args map[string]any) taskView {
	t.Helper()
	_, out := rpc(t, e, h, "tools/call", map[string]any{"name": "run_create", "arguments": args, "task": map[string]any{"ttl": 86400000}})
	var res struct{ Task taskView }
	b, _ := json.Marshal(out["result"])
	if json.Unmarshal(b, &res) != nil || res.Task.TaskID == "" {
		t.Fatalf("not a task: %v", out)
	}
	return res.Task
}

const runDraft = "## What is wrong\nx\n## What it should be\ny\n## Acceptance check\nz"

// F4, F5, R14: run_create is refused without max_cost and with a secret in its draft, before the
// factory is asked; with both right, the answer is a task whose status follows the room's coarse
// state, bound to its caller.
func TestRunCreate(t *testing.T) {
	f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomID, Room: roomID, Issue: 42}}
	ci := authn.Principal{Kind: envelope.ActorSystem, ID: "system:ci", Sponsored: true, AccessToken: "u", Expiry: sponsoredP.Expiry}
	e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP, ci), withTaskFactory(f), sponsoredRooms(t, world(nil)))
	h := bearer("slack-app")
	if got := call(t, e, h, "run_create", map[string]any{"repo": "Smana/ai-native-ref", "text": "x", "dataClass": "public"}); !got.isError ||
		!strings.Contains(got.text, "max_cost") {
		t.Fatalf("no max_cost: %+v", got)
	}
	if got := call(t, e, h, "run_create", map[string]any{"repo": "Smana/ai-native-ref", "title": "docs(x): y",
		"text": runDraft + "\ntoken SECRET", "dataClass": "public", "max_cost": 2}); !got.isError || !strings.Contains(got.text, "looks like a secret") {
		t.Fatalf("a secret in a titled draft: %+v", got)
	}
	if len(f.got) != 0 {
		t.Fatal("asked the factory")
	}
	task := startRunTask(t, e, h, map[string]any{"repo": "Smana/ai-native-ref", "title": "docs(x): y", "text": runDraft,
		"dataClass": "public", "max_cost": 2, "on_behalf_of": "slack:T01ABC/U02DEF"})
	if !strings.HasPrefix(task.TaskID, "run.") || task.Status != "working" {
		t.Fatalf("%+v", task)
	}
	if got := f.got[0]; got.AccessToken != "t" || got.MaxCostUSD != 2 || got.OnBehalfOf != "slack:T01ABC/U02DEF" ||
		!strings.HasPrefix(got.IdempotencyKey, "system:slack-app:") {
		t.Fatalf("%+v", got)
	}
	for _, c := range []struct{ phase, want string }{
		{"AwaitingHuman", "input_required"}, {"Done", "completed"}, {"Rejected", "failed"}, {"Stopped", "cancelled"},
	} {
		setTaskPhase(e, c.phase)
		if got, _ := getTask(t, e, h, task.TaskID); got.Status != c.want {
			t.Errorf("%s: %s, want %s", c.phase, got.Status, c.want)
		}
	}
	setTaskPhase(e, "AwaitingHuman")
	if got, _ := getTask(t, e, h, task.TaskID); !strings.Contains(got.StatusMessage, "/r/"+roomID) {
		t.Fatalf("a wait names the room page, never a command: %q", got.StatusMessage)
	}
	if _, code := getTask(t, e, bearer("ci"), task.TaskID); code != -32602 {
		t.Fatalf("another principal's task: %d", code)
	}
}

// C19: the room exists once triage makes it; until then, and for two minutes, the task is working.
func TestRunTaskBeforeItsRoom(t *testing.T) {
	base := time.Now()
	var later atomic.Int64 // nanoseconds past base: the handler reads the clock on its own goroutine
	f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomB, Room: roomB}}
	e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withTaskFactory(f),
		func(s *Server, _ *fanout.Hub, _ *hubView) {
			s.Rooms = roomClient(t)
			s.Now = func() time.Time { return base.Add(time.Duration(later.Load())) }
		})
	task := startRunTask(t, e, bearer("slack-app"), map[string]any{"repo": "Smana/ai-native-ref", "text": "x", "dataClass": "public", "max_cost": 1})
	if task.Status != "working" || !strings.Contains(task.StatusMessage, "not created yet") {
		t.Fatalf("%+v", task)
	}
	later.Store(int64(3 * time.Minute))
	if got, _ := getTask(t, e, bearer("slack-app"), task.TaskID); got.Status != "failed" {
		t.Fatalf("past the grace: %+v", got)
	}
}

// The factory's refusal code reaches the caller unchanged; a human never sees run_create; a long
// idempotency_key and a non-task call are refused.
func TestRunCreateRefusalsAndGate(t *testing.T) {
	f := &fakeTaskFactory{err: runrequest.ErrRefused{Status: 403, Reason: "not_linked"}}
	e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withTaskFactory(f), sponsoredRooms(t, world(nil)))
	args := map[string]any{"repo": "Smana/ai-native-ref", "text": "x", "dataClass": "public", "max_cost": 1}
	if got := call(t, e, bearer("slack-app"), "run_create", args); !got.isError || !strings.Contains(got.text, "not_linked") {
		t.Fatalf("%+v", got)
	}
	_, out := rpc(t, e, bearer("slack-app"), "tools/call", map[string]any{"name": "run_create", "arguments": args, "task": map[string]any{}})
	if !strings.Contains(strings.ToLower(jsonOf(out)), "not_linked") {
		t.Fatalf("as a task: %v", out)
	}
	long := map[string]any{"repo": "Smana/ai-native-ref", "text": "x", "dataClass": "public", "max_cost": 1,
		"idempotency_key": strings.Repeat("k", 129)}
	if got := call(t, e, bearer("slack-app"), "run_create", long); !got.isError {
		t.Fatalf("a 129-char key: %+v", got)
	}
	_, hout := rpc(t, e, viaRoomctl("dev"), "tools/call", map[string]any{"name": "run_create", "arguments": args})
	if rpcCode(hout) != -32602 || !strings.Contains(jsonOf(hout), "unknown tool: run_create") {
		t.Fatalf("a human: %v", hout)
	}
	if strings.Contains(toolNames(t, e, viaRoomctl("dev")), "run_create") {
		t.Fatal("a human lists run_create")
	}
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

// M3: the advertised schema is enforced by the server: a bad ref or on_behalf_of never reaches the factory.
func TestRunCreateChecksRefAndSponsor(t *testing.T) {
	f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomID, Room: roomID}}
	e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withTaskFactory(f), sponsoredRooms(t, world(nil)))
	base := func(k, v string) map[string]any {
		return map[string]any{"repo": "Smana/ai-native-ref", "text": "x", "dataClass": "public", "max_cost": 1, k: v}
	}
	for k, vs := range map[string][]string{
		"ref":          {"-x", "a b", "a..b", "a\nb", strings.Repeat("a", 257), "x;rm"},
		"on_behalf_of": {"slack:T01ABC", "U02DEF", "slack:t01abc/u02def", "slack:T01ABC/U02DEF\nx"},
	} {
		for _, v := range vs {
			if got := call(t, e, bearer("slack-app"), "run_create", base(k, v)); !got.isError {
				t.Errorf("%s=%q: %+v", k, v, got)
			}
		}
	}
	if len(f.got) != 0 {
		t.Fatal("asked the factory")
	}
	if got := call(t, e, bearer("slack-app"), "run_create", base("ref", "refs/heads/main")); got.isError {
		t.Fatalf("a good ref: %+v", got)
	}
}

// M5: redaction of untitled text, fail closed, and the task ids' bindings.
func TestRunCreateUntitledAndIDs(t *testing.T) {
	f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomID, Room: roomID}}
	args := map[string]any{"repo": "Smana/ai-native-ref", "text": "token SECRET", "dataClass": "public", "max_cost": 1}
	e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withTaskFactory(f), sponsoredRooms(t, world(nil)))
	if got := call(t, e, bearer("slack-app"), "run_create", args); !got.isError || !strings.Contains(got.text, "looks like a secret") {
		t.Fatalf("untitled secret: %+v", got)
	}
	nored := setup(t, mcpOn, withSponsored(sponsoredP), withTaskFactory(f), sponsoredRooms(t, world(nil)))
	args["text"] = "fine"
	if got := call(t, nored, bearer("slack-app"), "run_create", args); !got.isError || !strings.Contains(got.text, "unavailable") {
		t.Fatalf("no redactor: %+v", got)
	}
	if len(f.got) != 0 {
		t.Fatal("asked the factory")
	}
	// A principal with no token gets unavailable, never a forwarded empty bearer.
	notoken := sponsoredP
	notoken.AccessToken = ""
	inner := &fakeTaskFactory{}
	et := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(notoken), withTaskFactory(tokenCheck{inner}), sponsoredRooms(t, world(nil)))
	if got := call(t, et, bearer("slack-app"), "run_create", args); !got.isError || !strings.Contains(got.text, "unavailable") {
		t.Fatalf("no access token: %+v", got)
	}
	// Task ids: a run. id is a sponsored principal's only; the prefixes never cross.
	ok := startRunTask(t, e, bearer("slack-app"), map[string]any{"repo": "Smana/ai-native-ref", "text": "fine", "dataClass": "public", "max_cost": 1})
	payload := strings.TrimPrefix(ok.TaskID, "run.")
	human := runTask{V: 1, By: "human:dev", Room: roomID, Created: time.Now().UnixMilli(), TTL: time.Hour.Milliseconds()}
	for _, id := range []string{human.id(), "tail." + payload, "run." + strings.TrimPrefix(
		tailTask{V: 1, By: "system:slack-app", Room: roomID, Created: time.Now().UnixMilli(), TTL: time.Minute.Milliseconds()}.id(), "tail.")} {
		if _, code := getTask(t, e, bearer("slack-app"), id); code != -32602 {
			t.Errorf("%.20s: %d", id, code)
		}
	}
	if _, code := getTask(t, e, viaRoomctl("dev"), ok.TaskID); code != -32602 {
		t.Fatalf("a human with a run id: %d", code)
	}
}

// tokenCheck refuses as runrequest.Factory does without a token.
type tokenCheck struct{ f *fakeTaskFactory }

func (c tokenCheck) CreateTask(ctx context.Context, r runrequest.TaskRequest) (runrequest.TaskResult, error) {
	if r.AccessToken == "" {
		return runrequest.TaskResult{}, runrequest.ErrForbidden
	}
	return c.f.CreateTask(ctx, r)
}

// CD19, CD27: the plan is the promise, and the four fields the App still sends are checked against it
// before the factory is asked. Any disagreement between the call and the plan it carries files nothing,
// and a call that agrees is filed as the plan's own issue.
func TestRunCreateHoldsTheTaskToItsPlan(t *testing.T) {
	f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomID, Room: roomID, Issue: 42}}
	e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withIntake(t, "slack"), withTaskFactory(f))
	plan := planFixture(t, "slack-one-issue") // one issue, $1.50 from the channel
	ok := map[string]any{"repo": plan.Repository, "title": plan.Issues[0].Title, "text": plan.Issues[0].Body,
		"dataClass": "public", "max_cost": 1.5, "on_behalf_of": "slack:T01ABC/U02DEF",
		"plan": plan, "plan_hash": intake.Hash(plan)}
	if got := call(t, e, bearer("slack-app"), "run_create", ok); got.isError {
		t.Fatalf("%s", got.text)
	}
	// What was filed is the plan's own issue: a call that agrees adds nothing to it.
	if len(f.got) != 1 {
		t.Fatalf("asked the factory %d times", len(f.got))
	}
	if got := f.got[0]; got.Repository != plan.Repository || got.Title != plan.Issues[0].Title ||
		got.Text != plan.Issues[0].Body || got.MaxCostUSD != plan.Budget.CeilingUSD || got.Class != plan.Issues[0].Class || got.Suggested != "" {
		t.Fatalf("%+v", got)
	}
	for _, c := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"a hash that moved", func(m map[string]any) { m["plan_hash"] = "sha256:" + strings.Repeat("0", 64) }, "plan_changed"},
		{"another repository", func(m map[string]any) { m["repo"] = "Smana/cloud-native-ref" }, "plan_changed"},
		{"a body changed", func(m map[string]any) { m["text"] = plan.Issues[0].Body + "\nand more" }, "plan_changed"},
		{"a title changed", func(m map[string]any) { m["title"] = "docs: another title" }, "plan_changed"},
		{"a ceiling above it", func(m map[string]any) { m["max_cost"] = 9.0 }, "invalid_arguments"},
		{"more than one issue", func(m map[string]any) { m["plan"] = planFixture(t, "three") }, "plan_changed"},
		{"no hash with the plan", func(m map[string]any) { delete(m, "plan_hash") }, "invalid_arguments"},
		{"a hash that is not one", func(m map[string]any) { m["plan_hash"] = "deadbeef" }, "invalid_arguments"},
	} {
		args := maps.Clone(ok)
		c.mutate(args)
		if got := call(t, e, bearer("slack-app"), "run_create", args); !got.isError || !strings.Contains(got.text, c.want) {
			t.Errorf("%s: %s", c.name, got.text)
		}
	}
	// One filing, from the agreeing call alone: every disagreement above stopped before the factory.
	if len(f.got) != 1 {
		t.Fatalf("asked the factory %d times", len(f.got))
	}
}

// CD3, CD5: the plan is re-validated through the same checks plan_check runs — the requester its guard
// resolves, the allowlist, the class table — so a plan this broker cannot hold is refused here rather
// than filed and left to the factory to refuse.
func TestRunCreateRevalidatesThePlanThroughItsGuard(t *testing.T) {
	f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomID, Room: roomID, Issue: 42}}
	e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withIntake(t, "slack"), withTaskFactory(f))
	plan := planFixture(t, "slack-one-issue")
	ok := map[string]any{"repo": plan.Repository, "title": plan.Issues[0].Title, "text": plan.Issues[0].Body,
		"dataClass": "public", "max_cost": 1.5, "on_behalf_of": "slack:T01ABC/U02DEF",
		"plan": plan, "plan_hash": intake.Hash(plan)}
	// A repository the allowlist does not serve, carried by a plan whose hash and fields all agree:
	// the hash proves the plan is the one the broker made, and the guard is what says this requester
	// may not file into it.
	other := plan
	other.Repository = "Smana/cloud-native-ref"
	away := maps.Clone(ok)
	away["repo"], away["plan"], away["plan_hash"] = other.Repository, other, intake.Hash(other)
	if got := call(t, e, bearer("slack-app"), "run_create", away); !got.isError || !strings.Contains(got.text, "repository_not_allowed") {
		t.Fatalf("a repository the allowlist does not serve: %s", got.text)
	}
	// A plan is re-validated for the Slack user it was planned for: one the link table does not know
	// names no requester, so nothing about it can be checked.
	unlinked := maps.Clone(ok)
	unlinked["on_behalf_of"] = "slack:T09ZZZ/U09ZZZ"
	if got := call(t, e, bearer("slack-app"), "run_create", unlinked); !got.isError || !strings.Contains(got.text, "not_linked") {
		t.Fatalf("an unlinked user: %s", got.text)
	}
	// The plan is decoded as strictly as plan_check decodes it: a field plan/v1 does not carry is not
	// an object this broker answered with, and nothing guesses at what it was for.
	var loose map[string]any
	b, _ := json.Marshal(plan)
	if err := json.Unmarshal(b, &loose); err != nil {
		t.Fatal(err)
	}
	loose["approvedBy"] = "someone"
	strict := maps.Clone(ok)
	strict["plan"] = loose
	if got := call(t, e, bearer("slack-app"), "run_create", strict); !got.isError || !strings.Contains(got.text, "invalid_arguments") {
		t.Fatalf("a plan with a field of its own: %s", got.text)
	}
	if len(f.got) != 0 {
		t.Fatalf("asked the factory: %+v", f.got)
	}
	// A broker whose planner is off made no plan, so it cannot re-validate one: the call is refused
	// rather than filed on a plan nothing checked. Its switch-off route carries no plan at all.
	off := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withTaskFactory(f))
	if got := call(t, off, bearer("slack-app"), "run_create", ok); !got.isError || !strings.Contains(got.text, "planner_unavailable") {
		t.Fatalf("a broker with no planner: %s", got.text)
	}
	if len(f.got) != 0 {
		t.Fatalf("asked the factory: %+v", f.got)
	}
}

// A hash with no plan names nothing to check, and the tool files nothing on half a pair: the plan is
// the promise, so the call that carries its hash carries the plan. A call carrying neither is the tool
// it always was — an App that has not been updated sends neither field and keeps working.
func TestRunCreateRefusesHalfAPlanPair(t *testing.T) {
	f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomID, Room: roomID, Issue: 42}}
	e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withIntake(t, "slack"), withTaskFactory(f))
	plan := planFixture(t, "slack-one-issue")
	fields := map[string]any{"repo": plan.Repository, "title": plan.Issues[0].Title, "text": plan.Issues[0].Body,
		"dataClass": "public", "max_cost": 1.5, "on_behalf_of": "slack:T01ABC/U02DEF"}
	// Half the pair: a hash with no plan. It is refused, and refused as the arguments' error rather
	// than as a plan's — there is no plan to say anything about.
	half := maps.Clone(fields)
	half["plan_hash"] = intake.Hash(plan)
	if got := call(t, e, bearer("slack-app"), "run_create", half); !got.isError || !strings.Contains(got.text, "invalid_arguments") {
		t.Fatalf("a hash with no plan: %s", got.text)
	}
	if len(f.got) != 0 {
		t.Fatalf("a hash with no plan filed: %+v", f.got)
	}
	// Neither field: today's call, filed as it always was.
	if got := call(t, e, bearer("slack-app"), "run_create", fields); got.isError {
		t.Fatalf("a call with no plan and no hash: %s", got.text)
	}
	if len(f.got) != 1 || f.got[0].Repository != plan.Repository {
		t.Fatalf("%+v", f.got)
	}
}

// A call carrying no plan is the tool it always was: an older App keeps working, and the broker keeps
// no opinion it cannot check.
func TestRunCreateWithoutAPlanIsUnchanged(t *testing.T) {
	f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomID, Room: roomID, Issue: 42}}
	e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withIntake(t, "slack"), withTaskFactory(f))
	got := call(t, e, bearer("slack-app"), "run_create", map[string]any{"repo": "Smana/ai-native-ref", "title": "docs(x): y",
		"text": runDraft, "dataClass": "public", "max_cost": 2, "on_behalf_of": "slack:T01ABC/U02DEF"})
	if got.isError {
		t.Fatalf("%s", got.text)
	}
	if len(f.got) != 1 || f.got[0].Title != "docs(x): y" {
		t.Fatalf("%+v", f.got)
	}
}

// run_create's answer names the sponsor the factory chose, so the Slack App can say who files.
func TestRunCreateNamesTheSponsor(t *testing.T) {
	f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomID, Room: roomID, Issue: 42, Sponsor: "smana"}}
	e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withTaskFactory(f), sponsoredRooms(t, world(nil)))
	got := call(t, e, bearer("slack-app"), "run_create", map[string]any{"repo": "Smana/ai-native-ref", "title": "docs(x): y",
		"text": runDraft, "dataClass": "public", "max_cost": 2, "on_behalf_of": "slack:T01ABC/U02DEF"})
	if got.isError || !strings.Contains(string(got.structured), `"sponsor":"smana"`) {
		t.Fatalf("%+v", got)
	}
}

// The live case on gcp-0: the planner lowercases the repository, the Slack App files into the channel's
// spelling. The call is about the plan's task, so the two compare case-insensitively (the hash still
// covers the plan's own form), and the factory is asked with the call's spelling.
func TestRunCreateRepositoryCase(t *testing.T) {
	for name, tc := range map[string]struct {
		repo   string
		filed  bool
		reason string
	}{
		"the channel's spelling":       {"Smana/ai-native-ref", true, ""},
		"the planner's lowercase form": {"smana/ai-native-ref", true, ""},
		"another repository":           {"Smana/cloud-native-ref", false, "plan_changed"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomID, Room: roomID, Issue: 42}}
			e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withIntake(t, "slack"), withTaskFactory(f))
			plan := planFixture(t, "slack-one-issue")
			plan.Repository = "smana/ai-native-ref"
			args := map[string]any{"repo": tc.repo, "title": plan.Issues[0].Title, "text": plan.Issues[0].Body,
				"dataClass": "public", "max_cost": 1.5, "on_behalf_of": "slack:T01ABC/U02DEF",
				"plan": plan, "plan_hash": intake.Hash(plan)}
			got := call(t, e, bearer("slack-app"), "run_create", args)
			if !tc.filed {
				if !got.isError || !strings.Contains(got.text, tc.reason) || len(f.got) != 0 {
					t.Fatalf("want %s, got %s (%d asks)", tc.reason, got.text, len(f.got))
				}
				return
			}
			if got.isError || len(f.got) != 1 || f.got[0].Repository != tc.repo {
				t.Fatalf("%s; the factory was asked %+v", got.text, f.got)
			}
		})
	}
}

// P7, review B2: a planned task carries the plan's class, bound by its hash, and the suggested
// profile only as the catalogue re-derives it: an offered one is kept, one the class does not offer
// or one on the config fallback is never stored.
func TestRunCreateClassBoundByPlan(t *testing.T) {
	for name, c := range map[string]struct {
		catalogue bool
		profile   string
		want      string
	}{
		"offered":             {true, "links-solo", "links-solo"},
		"not offered":         {true, "review-frontier", ""},
		"the config fallback": {false, "links-solo", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeTaskFactory{answer: runrequest.TaskResult{Task: roomID, Room: roomID, Issue: 42}}
			opts := []option{mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withIntake(t, "slack"), withTaskFactory(f)}
			if c.catalogue {
				opts = append(opts, withCatalogue(&fakeFactoryCatalogue{}))
			}
			e := setup(t, opts...)
			plan := planFixture(t, "slack-one-issue")
			plan.Issues[0].Suggestion = &intake.Suggestion{Profile: c.profile, Merge: intake.MergeShown{Outcome: "auto"}}
			args := map[string]any{"repo": plan.Repository, "title": plan.Issues[0].Title, "text": plan.Issues[0].Body,
				"dataClass": "public", "max_cost": 1.5, "on_behalf_of": "slack:T01ABC/U02DEF", "plan": plan, "plan_hash": intake.Hash(plan)}
			if got := call(t, e, bearer("slack-app"), "run_create", args); got.isError {
				t.Fatal(got.text)
			}
			if len(f.got) != 1 || f.got[0].Class != "docs-links" || f.got[0].Suggested != c.want {
				t.Fatalf("%+v", f.got)
			}
			// A class in the arguments is not run_create's: the schema is closed.
			args["class"] = "review"
			if got := call(t, e, bearer("slack-app"), "run_create", args); !got.isError || len(f.got) != 1 {
				t.Fatalf("a class beside the plan was taken: %s", got.text)
			}
		})
	}
}
