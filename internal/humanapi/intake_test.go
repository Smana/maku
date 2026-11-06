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
		status, reply := f.status, f.reply
		f.mu.Unlock()
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
