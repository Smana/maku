// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/store"
)

type memHandoffStore struct {
	mu   sync.Mutex
	rows map[string]store.Handoff
}

func newMemHandoffStore() *memHandoffStore {
	return &memHandoffStore{rows: map[string]store.Handoff{}}
}

func (m *memHandoffStore) PutHandoff(_ context.Context, h store.Handoff) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := h
	cp.Args = slices.Clone(h.Args)
	cp.Plan = slices.Clone(h.Plan)
	m.rows[h.ID] = cp
	return nil
}

func (m *memHandoffStore) GetHandoff(_ context.Context, id string, now time.Time) (store.Handoff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.rows[id]
	if !ok || !h.ExpiresAt.After(now) {
		return store.Handoff{}, store.ErrNoHandoff
	}
	cp := h
	cp.Args = slices.Clone(h.Args)
	cp.Plan = slices.Clone(h.Plan)
	return cp, nil
}

func (m *memHandoffStore) all() []store.Handoff {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.Handoff, 0, len(m.rows))
	for _, h := range m.rows {
		out = append(out, h)
	}
	return out
}

func withHandoffs(st HandoffStore) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) { s.Handoffs = st }
}

func twoGapBase(t *testing.T) intake.Plan {
	t.Helper()
	base := planFixture(t, "single")
	base.Rev, base.Round, base.RoundsLeft = 1, 1, 2
	base.Issues[0].Body = "## What is wrong\nThe install link 404s.\n" +
		"[NEEDS CLARIFICATION: g1 Which install URL?]\n" +
		"[NEEDS CLARIFICATION: g2 Who owns the page?]\n\n" +
		"## What it should be\nThe install link MUST resolve.\n\n" +
		"## Acceptance check\nOn the link, the page MUST return 200."
	base.Gaps = []intake.Gap{
		{ID: "g1", Section: "what-is-wrong", Text: "Which install URL?", Recommended: intake.Answer{
			Answer: "https://example.test/install", Why: "it is the documented location"}},
		{ID: "g2", Section: "what-is-wrong", Text: "Who owns the page?", Recommended: intake.Answer{
			Answer: "The docs team owns the page.", Why: "they maintain the guide"}},
	}
	base.Question = &intake.Question{Text: base.Gaps[0].Text, Recommended: base.Gaps[0].Recommended}
	base.Hash = intake.Hash(base)
	return base
}

func twoGapModelReply(t *testing.T) []byte {
	t.Helper()
	base := twoGapBase(t)
	draft, err := json.Marshal(map[string]any{
		"repository": base.Repository,
		"issues": []any{map[string]any{
			"title": base.Issues[0].Title, "body": base.Issues[0].Body, "class": base.Issues[0].Class,
		}},
		"gaps": base.Gaps,
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(string(draft))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(`{"choices":[{"message":{"content":` + string(content) + `}}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":20}}`)
}

func TestHandoffBeginStoresPlan(t *testing.T) {
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withIntakeOnHour(t, at), withHandoffs(st))

	got := call(t, e, viaRoomctl("dev"), "handoff", map[string]any{"text": "the install link in README.md is dead"})
	if got.isError {
		t.Fatalf("handoff: %s", got.text)
	}
	var res struct {
		APIVersion string  `json:"apiVersion"`
		ID         string  `json:"id"`
		Text       string  `json:"text"`
		Question   any     `json:"question"`
		Ready      bool    `json:"ready"`
		Workflow   string  `json:"workflow"`
		Budget     float64 `json:"budget"`
	}
	if err := json.Unmarshal(got.structured, &res); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, got.structured)
	}
	if res.APIVersion != "handoff/v1" {
		t.Fatalf("apiVersion = %q, want handoff/v1", res.APIVersion)
	}
	if !regexp.MustCompile(`^[a-z2-7]{26}$`).MatchString(res.ID) {
		t.Fatalf("id = %q, does not match ^[a-z2-7]{26}$", res.ID)
	}
	if res.Text == "" || res.Text != got.text {
		t.Fatalf("text mismatch: structured=%q tool=%q", res.Text, got.text)
	}
	if !res.Ready || res.Question != nil {
		t.Fatalf("ready=%v question=%v", res.Ready, res.Question)
	}
	if res.Budget != 2 || res.Workflow == "" {
		t.Fatalf("budget=%v workflow=%q", res.Budget, res.Workflow)
	}

	rows := st.all()
	if len(rows) != 1 {
		t.Fatalf("stored %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.ID != res.ID || row.Principal != "human:dev" {
		t.Fatalf("stored row = %+v", row)
	}
	if want := at.Add(24 * time.Hour); !row.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", row.ExpiresAt, want)
	}
	var storedPlan intake.Plan
	if err := json.Unmarshal(row.Plan, &storedPlan); err != nil {
		t.Fatalf("stored plan: %v", err)
	}
	if intake.Hash(storedPlan) != row.Hash {
		t.Fatalf("stored hash %q != computed %q", row.Hash, intake.Hash(storedPlan))
	}
}

func TestHandoffAnswerRevisesStoredPlan(t *testing.T) {
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withHandoffs(st),
		intakeOption(t, func(_ *fakeIntake, in *Intake) { in.MaxRounds = 3 }))

	e.intake.replyWith(twoGapModelReply(t))
	first := call(t, e, viaRoomctl("dev"), "handoff", map[string]any{"text": "the install link in README.md is dead"})
	if first.isError {
		t.Fatalf("begin: %s", first.text)
	}
	var r1 struct {
		ID       string `json:"id"`
		Ready    bool   `json:"ready"`
		Question *struct {
			Text        string `json:"text"`
			Recommended string `json:"recommended"`
		} `json:"question"`
	}
	if err := json.Unmarshal(first.structured, &r1); err != nil {
		t.Fatal(err)
	}
	if r1.Ready || r1.Question == nil || r1.Question.Text != "Which install URL?" {
		t.Fatalf("begin question = %+v, ready = %v", r1.Question, r1.Ready)
	}
	beforeRow, err := st.GetHandoff(t.Context(), r1.ID, e.intake.clock())
	if err != nil {
		t.Fatal(err)
	}

	cand, err := answerFirstGap(twoGapBase(t), "https://example.test/install")
	if err != nil {
		t.Fatal(err)
	}
	e.intake.replyWith(revisionModelReply(t, cand))
	next := call(t, e, viaRoomctl("dev"), "handoff", map[string]any{
		"id":     r1.ID,
		"answer": "https://example.test/install",
	})
	if next.isError {
		t.Fatalf("revise: %s", next.text)
	}
	var r2 struct {
		ID       string `json:"id"`
		Ready    bool   `json:"ready"`
		Question *struct {
			Text        string `json:"text"`
			Recommended string `json:"recommended"`
		} `json:"question"`
	}
	if err := json.Unmarshal(next.structured, &r2); err != nil {
		t.Fatal(err)
	}
	if r2.ID != r1.ID {
		t.Fatalf("id changed: %q -> %q", r1.ID, r2.ID)
	}
	if r2.Ready || r2.Question == nil || r2.Question.Text != "Who owns the page?" {
		t.Fatalf("second question = %+v, ready = %v", r2.Question, r2.Ready)
	}
	if e.intake.calls() != 2 {
		t.Fatalf("calls = %d, want 2", e.intake.calls())
	}
	afterRow, err := st.GetHandoff(t.Context(), r1.ID, e.intake.clock())
	if err != nil {
		t.Fatal(err)
	}
	if afterRow.Hash == beforeRow.Hash {
		t.Fatalf("stored hash did not change: %q", afterRow.Hash)
	}
	var afterPlan intake.Plan
	if err := json.Unmarshal(afterRow.Plan, &afterPlan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(afterPlan.Issues[0].Body, "D1 [g1]: https://example.test/install") {
		t.Fatalf("revised body lacks decision record:\n%s", afterPlan.Issues[0].Body)
	}
}

func TestHandoffAcceptRest(t *testing.T) {
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withHandoffs(st))

	e.intake.replyWith(twoGapModelReply(t))
	first := call(t, e, viaRoomctl("dev"), "handoff", map[string]any{"text": "the install link in README.md is dead"})
	if first.isError {
		t.Fatalf("begin: %s", first.text)
	}
	var r1 struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(first.structured, &r1); err != nil {
		t.Fatal(err)
	}
	callsBefore := e.intake.calls()

	done := call(t, e, viaRoomctl("dev"), "handoff", map[string]any{
		"id":         r1.ID,
		"acceptRest": true,
	})
	if done.isError {
		t.Fatalf("acceptRest: %s", done.text)
	}
	if gotCalls := e.intake.calls() - callsBefore; gotCalls != 0 {
		t.Fatalf("acceptRest made %d model calls, want 0", gotCalls)
	}
	var r2 struct {
		ID       string `json:"id"`
		Ready    bool   `json:"ready"`
		Question any    `json:"question"`
	}
	if err := json.Unmarshal(done.structured, &r2); err != nil {
		t.Fatal(err)
	}
	if r2.ID != r1.ID || !r2.Ready || r2.Question != nil {
		t.Fatalf("acceptRest result = %+v", r2)
	}
	row, err := st.GetHandoff(t.Context(), r1.ID, e.intake.clock())
	if err != nil {
		t.Fatal(err)
	}
	var finalPlan intake.Plan
	if err := json.Unmarshal(row.Plan, &finalPlan); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(finalPlan.Issues[0].Body, "NEEDS CLARIFICATION") ||
		!strings.Contains(finalPlan.Issues[0].Body, "A1 [g1]: assumed") ||
		!strings.Contains(finalPlan.Issues[0].Body, "A2 [g2]: assumed") {
		t.Fatalf("final body = %s", finalPlan.Issues[0].Body)
	}
}

func TestHandoffForeignOrExpiredID(t *testing.T) {
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withIntakeOnHour(t, at), actorOn(&actLog{}), withHandoffs(st))

	first := call(t, e, viaRoomctl("dev"), "handoff", map[string]any{"text": "the install link in the README is dead"})
	if first.isError {
		t.Fatalf("begin: %s", first.text)
	}
	var r1 struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(first.structured, &r1); err != nil {
		t.Fatal(err)
	}

	foreign := call(t, e, viaRoomctl("other"), "handoff", map[string]any{"id": r1.ID, "acceptRest": true})
	if !foreign.isError || foreign.text != "handoff_expired: start a new handoff" {
		t.Fatalf("foreign = %+v", foreign)
	}
	if strings.Contains(foreign.text, "README") || len(foreign.structured) != 0 {
		t.Fatalf("foreign leaked plan content: %+v", foreign)
	}

	e.intake.advance(24*time.Hour + time.Second)
	expired := call(t, e, viaRoomctl("dev"), "handoff", map[string]any{"id": r1.ID, "acceptRest": true})
	if !expired.isError || expired.text != "handoff_expired: start a new handoff" {
		t.Fatalf("expired = %+v", expired)
	}
	if strings.Contains(expired.text, "README") || len(expired.structured) != 0 {
		t.Fatalf("expired leaked plan content: %+v", expired)
	}
}

func TestHandoffPlanningOff(t *testing.T) {
	e := setup(t, mcpOn, withSponsored(sponsoredP))
	for _, h := range []map[string][]string{viaRoomctl("dev"), bearer("slack-app")} {
		if got := toolNames(t, e, h); !strings.Contains(","+got+",", ",handoff,") {
			t.Fatalf("tools/list lacks handoff when s.Intake == nil: %s", got)
		}
	}
	humanGot := call(t, e, viaRoomctl("dev"), "handoff", map[string]any{"text": "the link is dead"})
	if !humanGot.isError || humanGot.text != "Planning is not available here; file on GitHub." {
		t.Fatalf("human planning off = %+v", humanGot)
	}
	slackGot := call(t, e, bearer("slack-app"), "handoff", map[string]any{
		"text":         "the link is dead",
		"on_behalf_of": "slack:T01ABC/U02DEF",
	})
	if !slackGot.isError || slackGot.text != "Planning is not available here; file on GitHub." {
		t.Fatalf("sponsored planning off = %+v", slackGot)
	}
}

func TestHandoffNamesNoPolicy(t *testing.T) {
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withIntake(t, "session"), withCatalogue(&fakeFactoryCatalogue{}), withHandoffs(st))
	got := call(t, e, viaRoomctl("dev"), "handoff", map[string]any{"text": "the install link in the README is dead"})
	if got.isError {
		t.Fatalf("handoff: %s", got.text)
	}
	for _, line := range strings.Split(got.text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Merge") || strings.HasPrefix(strings.ToLower(trimmed), "merge ") {
			t.Fatalf("plan text has Merge line %q:\n%s", line, got.text)
		}
		if strings.HasPrefix(trimmed, "Ceiling") {
			t.Fatalf("plan text uses Ceiling instead of Budget:\n%s", got.text)
		}
	}
	if !strings.Contains(got.text, "Workflow ") || !strings.Contains(got.text, "Budget ") {
		t.Fatalf("plan text lacks Workflow or Budget:\n%s", got.text)
	}
}

type carefulCatalogue struct{}

func (carefulCatalogue) Catalogue(_ context.Context, _, _, _ string) (catalogue.Catalogue, error) {
	cat := aiNativeRef(true)
	cat.Profiles["careful"] = catalogue.Profile{
		Template:    "trio",
		Roles:       []string{"implementer", "tester", "reviewer"},
		Tier:        "frontier",
		Checks:      true,
		TestFirst:   true,
		Offered:     true,
		Description: "Careful multi-file workflow",
	}
	return cat, nil
}

func (carefulCatalogue) Repositories(_ context.Context, _ string) ([]string, error) {
	return []string{"Smana/ai-native-ref"}, nil
}

func TestHandoffCheckCoversWorkflow(t *testing.T) {
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withCatalogue(carefulCatalogue{}), withHandoffs(st))

	began := call(t, e, viaRoomctl("dev"), "handoff", map[string]any{"text": "the install link in README.md is dead"})
	if began.isError {
		t.Fatalf("begin: %s", began.text)
	}
	var r1 struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(began.structured, &r1); err != nil {
		t.Fatal(err)
	}

	defGot := call(t, e, viaRoomctl("dev"), "handoff_check", map[string]any{"id": r1.ID})
	if defGot.isError {
		t.Fatalf("handoff_check default: %s", defGot.text)
	}
	var defChecked struct {
		Hash       string   `json:"hash"`
		Workflows  []string `json:"workflows"`
		Title      string   `json:"title"`
		Body       string   `json:"body"`
		Repository string   `json:"repository"`
	}
	if err := json.Unmarshal(defGot.structured, &defChecked); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(defChecked.Workflows, "careful") {
		t.Fatalf("workflows = %v, lacks careful", defChecked.Workflows)
	}
	if defChecked.Title == "" || defChecked.Body == "" || defChecked.Repository == "" {
		t.Fatalf("draft fields missing: %+v", defChecked)
	}

	carefulGot := call(t, e, viaRoomctl("dev"), "handoff_check", map[string]any{"id": r1.ID, "workflow": "careful"})
	if carefulGot.isError {
		t.Fatalf("handoff_check careful: %s", carefulGot.text)
	}
	var carefulChecked struct {
		Plan intake.Plan `json:"plan"`
		Hash string      `json:"hash"`
	}
	if err := json.Unmarshal(carefulGot.structured, &carefulChecked); err != nil {
		t.Fatal(err)
	}
	if carefulChecked.Hash == defChecked.Hash {
		t.Fatalf("careful hash %q equals default hash", carefulChecked.Hash)
	}
	if carefulChecked.Plan.Issues[0].Profile != "careful" {
		t.Fatalf("pinned profile = %q, want careful", carefulChecked.Plan.Issues[0].Profile)
	}
	row, err := st.GetHandoff(t.Context(), r1.ID, e.intake.clock())
	if err != nil {
		t.Fatal(err)
	}
	if row.Hash != carefulChecked.Hash {
		t.Fatalf("stored hash = %q, want %q", row.Hash, carefulChecked.Hash)
	}
}

func TestHandoffCheckUnlisted(t *testing.T) {
	st := newMemHandoffStore()
	e := setup(t, mcpOn, withSponsored(sponsoredP), withIntake(t, "session"), actorOn(&actLog{}), withHandoffs(st))
	for _, h := range []map[string][]string{viaRoomctl("dev"), bearer("slack-app")} {
		if got := toolNames(t, e, h); strings.Contains(got, "handoff_check") {
			t.Fatalf("handoff_check listed in %s", got)
		}
	}

	began := call(t, e, viaRoomctl("dev"), "handoff", map[string]any{"text": "the install link in README.md is dead"})
	if began.isError {
		t.Fatalf("begin: %s", began.text)
	}
	var r1 struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(began.structured, &r1); err != nil {
		t.Fatal(err)
	}
	checked := call(t, e, viaRoomctl("dev"), "handoff_check", map[string]any{"id": r1.ID})
	if checked.isError {
		t.Fatalf("handoff_check call failed: %s", checked.text)
	}
}
