// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/intake"
)

// The plan server: one fake gateway, one class table and one writable public repository, which is
// what every test below reaches intake_plan through.

// intakeClasses is the class table an operator's config would carry for a docs repository: the
// roles and the ceiling are the config's, never the model's (CD17).
func intakeClasses() map[string]intake.Class {
	return map[string]intake.Class{"docs-links": {
		Roles: []string{"scoper", "implementer", "reviewer"}, CeilingUSD: 2, Source: "class:docs-links.taskUSD"}}
}

// intakeReply is a valid reply: one repository, one issue of the docs-links class, no question. It
// is the envelope a gateway answers with, and its answer is a JSON document inside a string.
var intakeReply = func() []byte {
	draft := `{"repository":"smana/ai-native-ref","evidence":[{"kind":"path","text":"the install link in the README"}],` +
		`"issues":[{"title":"docs: the install link is dead","body":"## What is wrong\nThe install link 404s.\n` +
		`## What it should be\nA link that resolves.\n## Acceptance check\nThe link resolves.","class":"docs-links"}]}`
	content, err := json.Marshal(draft)
	if err != nil {
		panic(err)
	}
	return []byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1762347600,"model":"glm-5.3",` +
		`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":` + string(content) + `}}],` +
		`"usage":{"prompt_tokens":120,"completion_tokens":340,"total_tokens":460}}`)
}()

// fakeIntake is the one planner endpoint these tests reach. It counts every call — the assertion a
// refusal is held to — records the user turn the model was sent, and holds the clock `Intake.Now`
// reads, so a test steps the day instead of waiting for it.
type fakeIntake struct {
	t       *testing.T
	server  *httptest.Server
	keyPath string
	logs    *syncBuffer
	// links is the store's slack_links in these tests: a Slack user linked to an account. The ids are
	// Slack's own shape (the closed on_behalf_of pattern, as run_create's, wants two characters after
	// the T and the U; the plan's shorthand `slack:T1/U2` is a shape that pattern refuses).
	links slackLinkTable
	// surface is the surface the test expects this server's plans on, so the test asserts the one
	// the tool derived from the principal rather than the one it was told.
	surface string

	mu      sync.Mutex
	n       int // requests answered
	status  int
	now     time.Time
	reply   []byte
	prompts []string
	// coordReply answers a call made with coordination_plan's system prompt, so one gateway serves
	// both tools, as the one governed route does; delay makes every answer late.
	coordReply []byte
	delay      time.Duration
}

func newFakeIntake(t *testing.T, surface string, at time.Time) *fakeIntake {
	t.Helper()
	f := &fakeIntake{t: t, surface: surface, status: http.StatusOK, now: at, logs: &syncBuffer{},
		links: slackLinkTable{"T01ABC/U02DEF": "u2", "T01ABC/U03GHI": "u3"}, reply: intakeReply}
	f.keyPath = filepath.Join(t.TempDir(), "planner-key")
	if err := os.WriteFile(f.keyPath, []byte("k-planner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.n++
		f.prompts = append(f.prompts, userTurn(raw))
		status, reply, delay := f.status, f.reply, f.delay
		if f.coordReply != nil && strings.Contains(string(raw), "You plan one coherent change") {
			reply = f.coordReply
		}
		f.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(reply)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// model is the planner's client over the fake endpoint.
func (f *fakeIntake) model() *intake.Model {
	return &intake.Model{URL: f.server.URL + "/v1", APIKeyFile: f.keyPath, Alias: "tier-frontier",
		HC: f.server.Client(), Deadline: 5 * time.Second}
}

// calls is how many requests the endpoint has answered.
func (f *fakeIntake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// advance moves the clock the planner reads.
func (f *fakeIntake) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// advanceTo sets the clock the planner reads.
func (f *fakeIntake) advanceTo(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = at
}

// clock is the planner's Now.
func (f *fakeIntake) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// replyWith makes the gateway answer body instead of the valid reply above.
func (f *fakeIntake) replyWith(body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply = body
}

// fail makes the gateway answer status instead of 200.
func (f *fakeIntake) fail(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

// sent is the last user turn the model was sent.
func (f *fakeIntake) sent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.prompts) == 0 {
		return ""
	}
	return f.prompts[len(f.prompts)-1]
}

// logged is what the server's one audit line wrote.
func (f *fakeIntake) logged() string { return f.logs.String() }

// userTurn is the user message of a chat/completions body, "" when the body is not one.
func userTurn(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	for _, m := range req.Messages {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

// syncBuffer is a log sink the handler writes and the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// slackLinkTable is the store's slack_links: a Slack user, linked to the account of that name.
type slackLinkTable map[string]string

func (l slackLinkTable) SlackLink(_ context.Context, team, user string) (string, bool, error) {
	sub, ok := l[team+"/"+user]
	return sub, ok, nil
}

// publicRepos is the visibility read in these tests: the repositories listed are public, every other
// one is not — a refusal the guard reads as "not public", never as "cannot say".
type publicRepos map[string]bool

func (p publicRepos) Public(_ context.Context, repository string) (bool, error) {
	return p[strings.ToLower(repository)], nil
}

// intakeOption runs set over the plan server the setup builds, building it on the first call: the
// tests below add their own twist (a cap, a clock, a fallback) without restating the rest.
func intakeOption(t *testing.T, set func(*fakeIntake, *Intake)) option {
	t.Helper()
	return func(s *Server, _ *fanout.Hub, v *hubView) {
		if v.intake == nil {
			v.intake = newFakeIntake(t, string(intake.SurfaceSession), time.Now())
			// The human of these tests writes one public repository; the two Slack users the link
			// table knows write it too, which is what makes the daily cap's key the only thing
			// separating them.
			w := world(nil)
			w.set(func() { w.writes = map[string][]string{"smana/ai-native-ref": {"dev", "u2", "u3"}} })
			w.gate(s)
			s.Intake = &Intake{
				Classes:  intakeClasses(),
				Allow:    map[string]bool{"Smana/ai-native-ref": true},
				Model:    v.intake.model(),
				Public:   publicRepos{"smana/ai-native-ref": true},
				Links:    v.intake.links,
				Daily:    50,
				Fallback: "off",
				Now:      v.intake.clock,
			}
			s.Logger = slog.New(slog.NewJSONHandler(v.intake.logs, nil))
		}
		set(v.intake, s.Intake)
	}
}

// withIntake turns the planner on: the fake gateway, the docs-links class table, one writable public
// repository. surface is the one the test expects its plans on.
func withIntake(t *testing.T, surface string) option {
	t.Helper()
	if surface != string(intake.SurfaceSession) && surface != string(intake.SurfaceSlack) {
		t.Fatalf("withIntake(%q): not a surface", surface)
	}
	return intakeOption(t, func(f *fakeIntake, _ *Intake) { f.surface = surface })
}

// withIntakeDaily turns the planner on with n plans a day.
func withIntakeDaily(t *testing.T, n int) option {
	t.Helper()
	return intakeOption(t, func(_ *fakeIntake, in *Intake) { in.Daily = n })
}

// withIntakeOnHour turns the planner on with its clock fixed at at.
func withIntakeOnHour(t *testing.T, at time.Time) option {
	t.Helper()
	return intakeOption(t, func(f *fakeIntake, _ *Intake) { f.advanceTo(at) })
}

// withIntakeFallback turns the planner on with the config's fallback.
func withIntakeFallback(t *testing.T, fallback string) option {
	t.Helper()
	return intakeOption(t, func(_ *fakeIntake, in *Intake) { in.Fallback = fallback })
}

// planArgs is the one-argument call: a sentence, and no repository — the allowlist's one repository
// is the candidate set.
func planArgs() map[string]any {
	return map[string]any{"text": "the install link in the README is dead"}
}

// planFixture is a golden plan/v1, decoded through encoding/json from the planner package's own
// testdata: the human API's tests and the plan package's goldens cannot drift, so a rule `Check`
// gains is a rule plan_check is held to by the same bytes. It is the one explicit relative path in
// this package's tests.
func planFixture(t *testing.T, name string) intake.Plan {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "intake", "testdata", "plan", name+".json"))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var p intake.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return p
}

// metricsBody is /metrics as these tests scrape it: the series a refused plan is counted under.
func metricsBody(t *testing.T, e env) string {
	t.Helper()
	rec := httptest.NewRecorder()
	e.exp.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics: %d", rec.Code)
	}
	return rec.Body.String()
}

// CD18: one boundary, two channels, one validator. A session plans; the plan's hash is the
// broker's; roles and ceiling are the config's.
func TestIntakePlanFromASession(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session")) // a fake gateway: one repository, no question
	got := call(t, e, viaRoomctl("dev"), "intake_plan", map[string]any{"text": "the install link in the README is dead"})
	if got.isError {
		t.Fatalf("%s", got.text)
	}
	var d struct {
		Plan     intake.Plan
		Hash     string
		Degraded bool
	}
	_ = json.Unmarshal([]byte(got.structured), &d)
	if d.Hash != intake.Hash(d.Plan) || !strings.EqualFold(d.Plan.Repository, "Smana/ai-native-ref") || d.Degraded {
		t.Fatalf("%+v", d)
	}
	if !slices.Equal(d.Plan.Issues[0].Roles, []string{"scoper", "implementer", "reviewer"}) || d.Plan.Budget.CeilingUSD != 2 {
		t.Fatalf("%+v", d.Plan.Issues[0])
	}
	// The plan says which surface produced it, and it is the caller's, not a value the request named.
	if d.Plan.Planner.Surface != e.intake.surface || d.Plan.Planner.Model != "tier-frontier" || d.Plan.Rev != 1 {
		t.Fatalf("%+v", d.Plan.Planner)
	}
	// A session cannot claim a user or widen its ceiling: both are a sponsored principal's alone.
	for _, bad := range []map[string]any{{"text": "x", "ceiling": 99}, {"text": "x", "on_behalf_of": "slack:T01ABC/U02DEF"}} {
		if got := call(t, e, viaRoomctl("dev"), "intake_plan", bad); !got.isError || !strings.Contains(got.text, "invalid_arguments") {
			t.Errorf("%v: %s", bad, got.text)
		}
	}
	// The one audit line: the requester, the surface, the repository, the hash, the model, the
	// tokens, the outcome and the duration — and never the text that was planned.
	logs := e.intake.logged()
	for _, want := range []string{`"requester":"dev"`, `"surface":"session"`, `"repository":"smana/ai-native-ref"`,
		`"hash":"sha256:`, `"model":"tier-frontier"`, `"outcome":"planned"`, `"duration":`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the audit line does not carry %s: %s", want, logs)
		}
	}
	if strings.Contains(logs, "the install link in the README") {
		t.Errorf("the audit line carries the request's text: %s", logs)
	}
}

// CD18: the App reaches the same planner with on_behalf_of; the effective requester is the linked
// login, and a Slack user with no link is refused before any call.
func TestIntakePlanFromSlackResolvesTheLinkedLogin(t *testing.T) {
	e := setup(t, mcpOn, withSponsored(sponsoredP), withIntake(t, "slack"))
	got := call(t, e, bearer("slack-app"), "intake_plan", map[string]any{
		"text": "the install guide is wrong", "on_behalf_of": "slack:T01ABC/U02DEF",
		"candidates": []string{"Smana/ai-native-ref"}, "ceiling": 1.5})
	if got.isError || !strings.Contains(got.text, "$1.50") { // the channel's ceiling binds below the class's
		t.Fatalf("%s", got.text)
	}
	var d struct {
		Plan intake.Plan
	}
	_ = json.Unmarshal([]byte(got.structured), &d)
	if d.Plan.Planner.Surface != e.intake.surface || d.Plan.Budget.Source != "channel:maxCostUSD" {
		t.Fatalf("%+v", d.Plan)
	}
	// A user the link tables do not know: not_linked, zero calls to the model.
	before := e.intake.calls()
	got = call(t, e, bearer("slack-app"), "intake_plan", map[string]any{"text": "x", "on_behalf_of": "slack:T01ABC/U09XYZ"})
	if !got.isError || !strings.Contains(got.text, "not_linked") || e.intake.calls() != before {
		t.Fatalf("%s (%d calls)", got.text, e.intake.calls()-before)
	}
}

// Review Focus 5: the cap is exact at the boundary, and the refusal sends no request.
func TestDailyPlanCapRefusesWithoutACall(t *testing.T) {
	e := setup(t, mcpOn, withIntakeDaily(t, 2), withIntakeOnHour(t, time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)))
	for i := 0; i < 2; i++ {
		if got := call(t, e, viaRoomctl("dev"), "intake_plan", planArgs()); got.isError {
			t.Fatalf("call %d: %s", i+1, got.text)
		}
	}
	if got := call(t, e, viaRoomctl("dev"), "intake_plan", planArgs()); !got.isError ||
		!strings.Contains(got.text, "daily_plans_reached") || e.intake.calls() != 2 {
		t.Fatalf("%s (%d)", got.text, e.intake.calls())
	}
	// The day rolls over at 00:00 UTC: the clock is the server's Now, so the test steps it.
	e.intake.advance(24 * time.Hour)
	if got := call(t, e, viaRoomctl("dev"), "intake_plan", planArgs()); got.isError {
		t.Fatalf("after midnight: %s", got.text)
	}
}

// D: the counter is the effective requester's, not the App's. Two linked Slack users each get
// their own cap; one person's plans never spend another person's allowance.
func TestTheDailyCapIsPerRequesterNotPerApp(t *testing.T) {
	e := setup(t, mcpOn, withSponsored(sponsoredP), withIntakeDaily(t, 1),
		withIntakeOnHour(t, time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)))
	first := map[string]any{"text": "x", "on_behalf_of": "slack:T01ABC/U02DEF", "candidates": []string{"Smana/ai-native-ref"}}
	second := map[string]any{"text": "y", "on_behalf_of": "slack:T01ABC/U03GHI", "candidates": []string{"Smana/ai-native-ref"}}
	if got := call(t, e, bearer("slack-app"), "intake_plan", first); got.isError {
		t.Fatalf("U2: %s", got.text)
	}
	if got := call(t, e, bearer("slack-app"), "intake_plan", first); !got.isError || !strings.Contains(got.text, "daily_plans_reached") {
		t.Fatalf("U2 twice: %s", got.text)
	}
	if got := call(t, e, bearer("slack-app"), "intake_plan", second); got.isError {
		t.Fatalf("U3 spent U2's allowance: %s", got.text)
	}
}

// Off by default: with human.intake.plan.enabled false, neither tool exists, on either list.
func TestNoIntakeToolsWithoutTheSwitch(t *testing.T) {
	e := setup(t, mcpOn, withSponsored(sponsoredP))
	for _, h := range []http.Header{viaRoomctl("dev"), bearer("slack-app")} {
		if got := toolNames(t, e, h); strings.Contains(got, "intake_plan") || strings.Contains(got, "plan_check") {
			t.Fatalf("%s", got)
		}
	}
	// And the switch that turns it on puts it on both lists.
	on := setup(t, mcpOn, withSponsored(sponsoredP), withIntake(t, "session"))
	if got := toolNames(t, on, viaRoomctl("dev")); !strings.Contains(got, "intake_plan") {
		t.Fatalf("a human's list: %s", got)
	}
	if got := toolNames(t, on, bearer("slack-app")); !strings.Contains(got, "intake_plan") {
		t.Fatalf("the App's list: %s", got)
	}
}

// The model is told the class table the config defines, so a reply names one of its classes rather
// than a slug of its own (invalid_class is a refusal, not the expected answer).
func TestIntakePlanTellsTheModelTheClassTable(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"))
	if got := call(t, e, viaRoomctl("dev"), "intake_plan", planArgs()); got.isError {
		t.Fatalf("%s", got.text)
	}
	if sent := e.intake.sent(); !strings.Contains(sent, "docs-links") {
		t.Fatalf("the model was not told the classes it may name: %s", sent)
	}
}

// The design's failure contract: a request no repository of the allowlist answers names the
// repositories this requester may actually file into, so the refusal is a way forward and not a wall.
func TestARefusedRepositoryNamesTheOnesYouMayWrite(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"))
	got := call(t, e, viaRoomctl("dev"), "intake_plan", map[string]any{"text": "x", "repository": "Smana/somewhere-else"})
	if !got.isError || !strings.Contains(got.text, "repository_not_allowed") {
		t.Fatalf("%s", got.text)
	}
	if !strings.Contains(got.text, "smana/ai-native-ref") {
		t.Fatalf("the refusal does not name the repository the caller may write: %s", got.text)
	}
	if e.intake.calls() != 0 {
		t.Fatalf("a refused request called the model %d times", e.intake.calls())
	}
}

// The plan is checked before it is shown: a reply that names a repository outside the candidate set
// is refused whole, and the outsider's name is nowhere in the answer the person reads.
func TestThePlanIsCheckedBeforeItIsShown(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"))
	outsider := func() []byte {
		draft := `{"repository":"smana/ai-native-ref","alternatives":[{"repository":"smana/private-one","why":"it fits"}],` +
			`"issues":[{"title":"docs: the install link is dead","body":"## What is wrong\nThe link 404s.\n## What it should be\nA link.\n## Acceptance check\nIt resolves.","class":"docs-links"}]}`
		content, _ := json.Marshal(draft)
		return []byte(`{"choices":[{"message":{"content":` + string(content) + `}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}()
	e.intake.replyWith(outsider)
	got := call(t, e, viaRoomctl("dev"), "intake_plan", planArgs())
	if !got.isError || !strings.Contains(got.text, "repository_not_in_candidates") {
		t.Fatalf("%s", got.text)
	}
	for _, out := range []string{got.text, string(got.structured), e.intake.logged()} {
		if strings.Contains(out, "private-one") {
			t.Fatalf("the outsider is echoed: %s", out)
		}
	}
}

// CD18: the degraded composer is reachable only when the config names it, and the plan it serves says
// so — a template draft is never mistaken for the planner's.
func TestTheComposerIsReachedOnlyWhenTheFallbackNamesIt(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), withIntakeFallback(t, "composer"))
	e.intake.fail(http.StatusServiceUnavailable)
	got := call(t, e, viaRoomctl("dev"), "intake_plan", planArgs())
	if got.isError || !strings.Contains(got.text, intake.TemplateLabel) {
		t.Fatalf("%s", got.text)
	}
	var d struct {
		Plan     intake.Plan
		Degraded bool
	}
	_ = json.Unmarshal([]byte(got.structured), &d)
	if !d.Degraded || d.Plan.Planner.Surface != e.intake.surface {
		t.Fatalf("%+v", d)
	}

	// Off, the same failure is a refusal: the composer is not a fallback anyone gets by accident.
	off := setup(t, mcpOn, withIntake(t, "session"))
	off.intake.fail(http.StatusServiceUnavailable)
	if got := call(t, off, viaRoomctl("dev"), "intake_plan", planArgs()); !got.isError ||
		!strings.Contains(got.text, "planner_unavailable") {
		t.Fatalf("%s", got.text)
	}
}

// plan_check is the door every consumer re-validates through, so these tests hold it to the plan
// package's own goldens: a plan the broker answered is accepted as it is, and a plan that is not the
// one it answered is refused before anything is filed.

// Offline proof 4, first half: a plan changed after the render is refused; an unchanged one is
// accepted and re-validated with the same validator. The refusal is the one metric plan_check writes.
func TestPlanCheckRefusesAChangedPlan(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}))
	shown := planFixture(t, "single")
	hash := intake.Hash(shown)
	if got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": shown, "hash": hash}); got.isError {
		t.Fatalf("%s", got.text)
	}
	changed := shown
	changed.Issues = slices.Clone(shown.Issues)
	changed.Issues[0].Body = strings.Replace(shown.Issues[0].Body, "## What is wrong", "## What is wrong\n- sneaked in", 1)
	if got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": changed, "hash": hash}); !got.isError ||
		!strings.Contains(got.text, "plan_changed") {
		t.Fatalf("%s", got.text)
	}
	if body := metricsBody(t, e); !strings.Contains(body, `maku_intake_plan_total{outcome="refused",reason="plan_changed",surface="session"} 1`) {
		t.Fatalf("the refusal is not counted under the closed list's reason: %s", body)
	}
	if e.intake.calls() != 0 {
		t.Fatalf("a plan_check called the model %d times", e.intake.calls())
	}
}

// The answer is the plan that was given, its hash recomputed, and whether this caller may start the
// factory on it: a caller files what it confirmed, not what a second composition produced.
func TestPlanCheckAnswersWithThePlanItWasGiven(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}))
	shown := planFixture(t, "single")
	got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": shown, "hash": intake.Hash(shown)})
	if got.isError {
		t.Fatalf("%s", got.text)
	}
	var d struct {
		Plan        intake.Plan
		Hash        string
		CanStart    bool
		CanStartWhy string
	}
	if err := json.Unmarshal(got.structured, &d); err != nil {
		t.Fatalf("%v: %s", err, got.structured)
	}
	if d.Hash != intake.Hash(d.Plan) || d.Plan.Hash != d.Hash {
		t.Fatalf("the answer's hash is not the plan's: %+v", d)
	}
	// The start field is issue_draft's: this session names no sessionStart repository, so it is off.
	if d.CanStart || d.CanStartWhy != startOff {
		t.Fatalf("a plan that may not start: %+v", d)
	}
	if !strings.Contains(got.text, "Plan") || !strings.Contains(got.text, d.Hash) {
		t.Fatalf("the text is not the plan's view: %s", got.text)
	}
}

// A plan the redactor refuses is refused here too: the one validator, whichever door. The redactor in
// these tests refuses the word SECRET, so a body carrying it is the second of the two checks.
func TestPlanCheckRunsCheckDraftAndTheRedactor(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}))
	for name, mutate := range map[string]func(*intake.Plan){
		"no acceptance section": func(p *intake.Plan) { p.Issues[0].Body = "## What is wrong\nx" },
		"a secret":              func(p *intake.Plan) { p.Issues[0].Body += "\nSECRET" },
	} {
		p := planFixture(t, "single")
		mutate(&p)
		if got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": p, "hash": intake.Hash(p)}); !got.isError ||
			!strings.Contains(got.text, "invalid_draft") {
			t.Errorf("%s: %s", name, got.text)
		}
	}
}

// The plan's numbering is the contract's, and plan_check is where a read-back plan is held to it: a
// duplicate, a zero, a gap and a dependency on later work are four ways one issue is filed twice or
// out of order. Each hash here is the plan's own, so only the numbering can refuse it.
func TestPlanCheckRefusesANumberingTheContractDoesNotHave(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}))
	for name, change := range map[string]func(*intake.Plan){
		"a duplicate index":        func(p *intake.Plan) { p.Issues = append(p.Issues, p.Issues[0]) },
		"a zero index":             func(p *intake.Plan) { p.Issues[0].Index = 0 },
		"an index out of sequence": func(p *intake.Plan) { p.Issues[0].Index = 2 },
		"a forward dependency":     func(p *intake.Plan) { p.Issues[0].DependsOn = []int{2} },
	} {
		p := planFixture(t, "single")
		change(&p)
		if got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": p, "hash": intake.Hash(p)}); !got.isError ||
			!strings.Contains(got.text, "planner_invalid") {
			t.Errorf("%s: %s", name, got.text)
		}
	}
}

// The hash is the plan's, never the caller's word for it: a value that is not one, a hash of another
// plan and a field outside the schema are three argument errors, and none of them is a plan_changed.
func TestPlanCheckRefusesAHashThatIsNotThePlans(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}))
	shown := planFixture(t, "single")
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"not a sha256": {map[string]any{"plan": shown, "hash": "sha256:nothex"}, "invalid_arguments"},
		"another plan's hash": {
			map[string]any{"plan": shown, "hash": intake.Hash(planFixture(t, "three"))}, "plan_changed"},
		"no hash": {map[string]any{"plan": shown}, "invalid_arguments"},
		"a field outside the schema": {map[string]any{"plan": shown, "hash": intake.Hash(shown),
			"candidates": []string{"Smana/ai-native-ref"}}, "invalid_arguments"},
		"a field inside the plan outside plan/v1": {map[string]any{"hash": intake.Hash(shown),
			"plan": map[string]any{"apiVersion": "plan/v1", "colour": "red"}}, "invalid_arguments"},
	} {
		if got := call(t, e, viaRoomctl("dev"), "plan_check", tc.args); !got.isError || !strings.Contains(got.text, tc.want) {
			t.Errorf("%s: %s", name, got.text)
		}
	}
}

// The candidate set is the plan's own repository and its alternatives, held to the guard's allowlist,
// write and public checks: a repository this broker no longer serves is not re-validated into a
// filing, and an outsider named as an alternative refuses the plan whole without being echoed.
func TestPlanCheckHoldsThePlansRepositoriesToTheGuard(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}))
	for name, tc := range map[string]struct {
		change func(*intake.Plan)
		want   string
	}{
		"a repository the allowlist does not serve": {
			func(p *intake.Plan) { p.Repository = "Smana/somewhere-else" }, "repository_not_allowed"},
		"an outsider alternative": {
			func(p *intake.Plan) {
				p.Alternatives = []intake.Alternative{{Repository: "Smana/private-one", Why: "it fits"}}
			}, "repository_not_in_candidates"},
	} {
		p := planFixture(t, "single")
		tc.change(&p)
		got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": p, "hash": intake.Hash(p)})
		if !got.isError || !strings.Contains(got.text, tc.want) {
			t.Errorf("%s: %s", name, got.text)
			continue
		}
		for _, named := range []string{"somewhere-else", "private-one"} {
			if strings.Contains(got.text, named) {
				t.Errorf("%s: the refusal names %s: %s", name, named, got.text)
			}
		}
	}
}

// plan_check is a read that costs the requester nothing and asks nobody anything: two checks spend no
// plan — the day's one plan is still there afterwards — and no check reached the model.
func TestPlanCheckSpendsNoPlanAndCallsNoModel(t *testing.T) {
	e := setup(t, mcpOn, withIntakeDaily(t, 1), actorOn(&actLog{}))
	p := planFixture(t, "single")
	for i := range 2 {
		if got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": p, "hash": intake.Hash(p)}); got.isError {
			t.Fatalf("check %d: %s", i+1, got.text)
		}
	}
	if got := call(t, e, viaRoomctl("dev"), "intake_plan", planArgs()); got.isError {
		t.Fatalf("a check spent the day's plan: %s", got.text)
	}
	if e.intake.calls() != 1 {
		t.Fatalf("the model was called %d times for one plan and two checks", e.intake.calls())
	}
}

// The caller is resolved by the same requester() intake_plan uses: a sponsored plan_check asks GitHub
// about the linked login the plan was made for, never about the App, which has no write access to
// name. A session cannot check a plan on someone else's behalf, which is the same rule.
func TestPlanCheckResolvesTheRequesterAsIntakePlanDoes(t *testing.T) {
	e := setup(t, mcpOn, withSponsored(sponsoredP), withIntake(t, "slack"), actorOn(&actLog{}))
	p := planFixture(t, "single")
	args := func(onBehalfOf string) map[string]any {
		m := map[string]any{"plan": p, "hash": intake.Hash(p)}
		if onBehalfOf != "" {
			m["on_behalf_of"] = onBehalfOf
		}
		return m
	}
	// u2 writes the repository and is linked; the App resolves no login of its own, so an answer that
	// holds is the link being read.
	got := call(t, e, bearer("slack-app"), "plan_check", args("slack:T01ABC/U02DEF"))
	if got.isError {
		t.Fatalf("%s", got.text)
	}
	var d struct {
		CanStart    bool
		CanStartWhy string
	}
	_ = json.Unmarshal(got.structured, &d)
	if d.CanStart || d.CanStartWhy != startSponsored {
		t.Fatalf("a sponsored plan_check offered a start: %+v", d)
	}
	for name, tc := range map[string]struct {
		h    http.Header
		args map[string]any
		want string
	}{
		"an unlinked user":       {bearer("slack-app"), args("slack:T01ABC/U09XYZ"), "not_linked"},
		"no linked user named":   {bearer("slack-app"), args(""), "invalid_arguments"},
		"a session for a person": {viaRoomctl("dev"), args("slack:T01ABC/U02DEF"), "invalid_arguments"},
	} {
		if got := call(t, e, tc.h, "plan_check", tc.args); !got.isError || !strings.Contains(got.text, tc.want) {
			t.Errorf("%s: %s", name, got.text)
		}
	}
	if e.intake.calls() != 0 {
		t.Fatalf("plan_check called the model %d times", e.intake.calls())
	}
}

// The schema is the client's copy of the check, so it must not disagree with it: each bound is the
// broker's own constant, and a ceiling of zero is "no channel ceiling" — a value the broker accepts,
// so a schema refusing it would refuse a request the broker answers.
func TestTheIntakeSchemasAgreeWithTheBroker(t *testing.T) {
	type property struct {
		MaxLength        *int     `json:"maxLength"`
		MaxItems         *int     `json:"maxItems"`
		Minimum          *float64 `json:"minimum"`
		ExclusiveMinimum *float64 `json:"exclusiveMinimum"`
		Pattern          string   `json:"pattern"`
		Items            *property
	}
	var plan struct {
		AdditionalProperties *bool               `json:"additionalProperties"`
		Required             []string            `json:"required"`
		Properties           map[string]property `json:"properties"`
	}
	if err := json.Unmarshal(intakePlanSchema, &plan); err != nil {
		t.Fatalf("%v: %s", err, intakePlanSchema)
	}
	if plan.AdditionalProperties == nil || *plan.AdditionalProperties {
		t.Error("intake_plan's schema is not closed")
	}
	for name, want := range map[string]struct {
		got *int
		n   int
	}{
		"text":            {plan.Properties["text"].MaxLength, maxPlanText},
		"context":         {plan.Properties["context"].MaxLength, maxPlanContext},
		"candidates":      {plan.Properties["candidates"].MaxItems, maxPlanCandidates},
		"evidence":        {plan.Properties["evidence"].MaxItems, maxPlanEvidence},
		"evidence[].text": {plan.Properties["evidence"].Items.MaxLength, maxPlanEvidenceRunes},
	} {
		if want.got == nil || *want.got != want.n {
			t.Errorf("%s: the schema holds %v, the broker %d", name, want.got, want.n)
		}
	}
	if c := plan.Properties["ceiling"]; c.Minimum == nil || *c.Minimum != 0 || c.ExclusiveMinimum != nil {
		t.Errorf("ceiling: the broker takes 0 as absent and refuses below it; the schema says %+v", c)
	}

	var check struct {
		AdditionalProperties *bool               `json:"additionalProperties"`
		Required             []string            `json:"required"`
		Properties           map[string]property `json:"properties"`
	}
	if err := json.Unmarshal(planCheckSchema, &check); err != nil {
		t.Fatalf("%v: %s", err, planCheckSchema)
	}
	if check.AdditionalProperties == nil || *check.AdditionalProperties {
		t.Error("plan_check's schema is not closed")
	}
	if !slices.Equal(check.Required, []string{"plan", "hash"}) {
		t.Errorf("plan_check requires %v", check.Required)
	}
	if got := check.Properties["hash"].Pattern; got != hashRE.String() {
		t.Errorf("hash pattern %q, the broker's is %q", got, hashRE.String())
	}
	if got := check.Properties["on_behalf_of"].Pattern; got != onBehalfRE.String() {
		t.Errorf("on_behalf_of pattern %q, the broker's is %q", got, onBehalfRE.String())
	}
}

// The answer's shape is a contract, not a habit: plan_check declares it, and declares it closed, so a
// client validating structuredContent sees the four fields and nothing else.
func TestPlanCheckDeclaresAClosedOutputSchema(t *testing.T) {
	e := setup(t, mcpOn, withSponsored(sponsoredP), withIntake(t, "session"))
	for name, h := range map[string]http.Header{"a human's": viaRoomctl("dev"), "the App's": bearer("slack-app")} {
		_, out := rpc(t, e, h, "tools/list", nil)
		b, _ := json.Marshal(out["result"])
		var r struct {
			Tools []struct {
				Name         string
				OutputSchema map[string]any
			}
		}
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("%s: %s", name, string(b))
		}
		i := slices.IndexFunc(r.Tools, func(tool struct {
			Name         string
			OutputSchema map[string]any
		}) bool {
			return tool.Name == "plan_check"
		})
		if i < 0 {
			t.Fatalf("%s list does not carry plan_check", name)
		}
		schema := r.Tools[i].OutputSchema
		if schema == nil || schema["additionalProperties"] != false {
			t.Fatalf("%s: plan_check's outputSchema is not closed: %v", name, schema)
		}
		props, _ := schema["properties"].(map[string]any)
		for _, field := range []string{"plan", "hash", "canStart", "canStartWhy"} {
			if _, ok := props[field]; !ok {
				t.Errorf("%s: the outputSchema does not name %s: %v", name, field, schema)
			}
		}
		required, _ := schema["required"].([]any)
		for _, field := range []string{"plan", "hash", "canStart"} {
			if !slices.Contains(required, any(field)) {
				t.Errorf("%s: the outputSchema does not require %s: %v", name, field, schema)
			}
		}
	}
}

// A broker with no redactor wired refuses rather than accepting a plan nobody scanned: checkDraft's
// own fail-closed rule, which is what makes every other test in this block wire an actor.
func TestPlanCheckWithoutARedactorRefuses(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"))
	p := planFixture(t, "single")
	got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": p, "hash": intake.Hash(p)})
	if !got.isError || !strings.Contains(got.text, "unavailable") {
		t.Fatalf("%s", got.text)
	}
}
