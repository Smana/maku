// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	if !strings.Contains(res.Text, "Handoff "+res.ID+"\n") {
		t.Fatalf("rendered plan does not show the handoff id: %q", res.Text)
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

	cand, err := answerFirstGap(storedPlan(t, st, e, r1.ID), "https://example.test/install")
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
	// One question per handoff (#625): g2 was assumed when g1 was asked, so the answer readies it.
	if !r2.Ready || r2.Question != nil {
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
	if !strings.Contains(afterPlan.Issues[0].Body, "D2 [g1]: https://example.test/install") ||
		!strings.Contains(afterPlan.Issues[0].Body, "A1 [g2]: assumed") {
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
		!strings.Contains(finalPlan.Issues[0].Body, "A1 [g2]: assumed") ||
		!strings.Contains(finalPlan.Issues[0].Body, "A2 [g1]: assumed") {
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

type fakeIssueStarter struct {
	mu   sync.Mutex
	reqs []StartIssueRequest
}

type fakeHandoffStarts struct {
	mu   sync.Mutex
	recs []store.HandoffStart
	err  error
}

func (f *fakeHandoffStarts) RecordHandoffStart(_ context.Context, h store.HandoffStart) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.recs = append(f.recs, h)
	return nil
}

func (f *fakeIssueStarter) StartIssue(_ context.Context, req StartIssueRequest) (StartIssueResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := req
	cp.Labels = slices.Clone(req.Labels)
	f.reqs = append(f.reqs, cp)
	n := 100 + len(f.reqs)
	return StartIssueResult{
		Number: n,
		URL:    "https://github.com/" + req.Repository + "/issues/101",
	}, nil
}

func TestTheHandoffRoutesNeedAMember(t *testing.T) {
	st := newMemHandoffStore()
	starter := &fakeIssueStarter{}
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withHandoffs(st), func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.StartDefault = true
		s.IssueStarter = starter
		s.HandoffStarts = &fakeHandoffStarts{}
	})
	post := func(path string, h http.Header, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		if h != nil {
			req.Header = h.Clone()
		}
		rec := httptest.NewRecorder()
		e.srv.Routes().ServeHTTP(rec, req)
		return rec
	}

	// First create a handoff as a member so check and start have a real id to test against.
	created := post("/api/handoffs", header("dev"), `{"text":"the install link in README.md is dead"}`)
	if created.Code != http.StatusOK {
		t.Fatalf("member create = %d %s", created.Code, created.Body.String())
	}
	var cRes struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &cRes); err != nil || cRes.ID == "" {
		t.Fatalf("decode create: %v (%s)", err, created.Body.String())
	}

	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{"create", "/api/handoffs", `{"text":"the install link in README.md is dead"}`},
		{"check", "/api/handoffs/" + cRes.ID + "/check", `{}`},
		{"start", "/api/handoffs/" + cRes.ID + "/start", `{}`},
	} {
		t.Run(tc.name+" unauthenticated", func(t *testing.T) {
			rec := post(tc.path, nil, tc.body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s without auth = %d %s, want 401", tc.name, rec.Code, rec.Body.String())
			}
		})
		t.Run(tc.name+" non-member", func(t *testing.T) {
			rec := post(tc.path, header("stranger"), tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s as stranger = %d %s, want 403", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
	if len(starter.reqs) != 0 {
		t.Fatalf("unauthenticated/non-member calls filed %d issues", len(starter.reqs))
	}
}

func TestStartFromTheWebFilesAsTheViewer(t *testing.T) {
	st := newMemHandoffStore()
	starter := &fakeIssueStarter{}
	starts := &fakeHandoffStarts{}
	w := world(nil, "unlinked")
	w.set(func() { w.writes = map[string][]string{"smana/ai-native-ref": {"dev", "own", "u2", "u3"}} })
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withCatalogue(carefulCatalogue{}), withHandoffs(st),
		w.option(),
		func(s *Server, _ *fanout.Hub, _ *hubView) {
			s.StartDefault = true
			s.IssueStarter = starter
			s.HandoffStarts = starts
		})
	post := func(path string, h http.Header, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		req.Header = h.Clone()
		rec := httptest.NewRecorder()
		e.srv.Routes().ServeHTTP(rec, req)
		return rec
	}

	// 1. Plan over POST /api/handoffs as member dev (linked to GitHub "dev").
	cRec := post("/api/handoffs", header("dev"), `{"text":"the install link in README.md is dead"}`)
	if cRec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", cRec.Code, cRec.Body.String())
	}
	var created handoffCreateResponse
	if err := json.Unmarshal(cRec.Body.Bytes(), &created); err != nil || !created.Ready || created.ID == "" {
		t.Fatalf("created = %+v (err=%v)", created, err)
	}

	// 2. Check with workflow pin + plan-first outcome over POST /api/handoffs/{id}/check.
	chkRec := post("/api/handoffs/"+created.ID+"/check", header("dev"), `{"workflow":"careful","outcome":"plan-first"}`)
	if chkRec.Code != http.StatusOK {
		t.Fatalf("check = %d %s", chkRec.Code, chkRec.Body.String())
	}
	var checked handoffCheckResponse
	if err := json.Unmarshal(chkRec.Body.Bytes(), &checked); err != nil {
		t.Fatalf("decode check: %v", err)
	}
	if !checked.CanStart || !checked.Linked || checked.Workflow != "careful" {
		t.Fatalf("checked = %+v", checked)
	}
	if len(checked.Plan.Issues) == 0 || checked.Plan.Issues[0].Outcome != intake.OutcomePlanFirst {
		t.Fatalf("checked outcome = %+v, want plan-first", checked.Plan.Issues)
	}

	// 3. Start over POST /api/handoffs/{id}/start -> files as viewer "dev" with labels.
	stRec := post("/api/handoffs/"+created.ID+"/start", header("dev"), `{"hash":"`+checked.Hash+`"}`)
	if stRec.Code != http.StatusCreated {
		t.Fatalf("start = %d %s", stRec.Code, stRec.Body.String())
	}
	var started handoffStartResponse
	if err := json.Unmarshal(stRec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if started.Viewer != "dev" || started.Issue != 101 || !strings.Contains(started.URL, "/issues/101") {
		t.Fatalf("started = %+v", started)
	}
	if len(starter.reqs) != 1 {
		t.Fatalf("filed %d issues, want 1", len(starter.reqs))
	}
	got := starter.reqs[0]
	if got.Viewer != "dev" || got.Principal != "human:dev" || !strings.EqualFold(got.Repository, "smana/ai-native-ref") {
		t.Fatalf("StartIssue req = %+v", got)
	}
	wantLabels := []string{"workflow:careful", "plan-first", "factory/ready"}
	if !slices.Equal(got.Labels, wantLabels) {
		t.Fatalf("labels = %v, want %v", got.Labels, wantLabels)
	}
	// The start is recorded for the factory's intake (broker-confirmed handoff): the filed issue, the
	// handoff, the signed-in principal and its sub, the linked login and the labels the App applied.
	if len(starts.recs) != 1 {
		t.Fatalf("recorded %d starts, want 1", len(starts.recs))
	}
	if r := starts.recs[0]; r.Issue != 101 || r.Handoff != created.ID || r.Principal != "human:dev" || r.Sub != "dev" ||
		r.Login != "dev" || !strings.EqualFold(r.Repository, "smana/ai-native-ref") || !slices.Equal(r.Labels, wantLabels) ||
		r.StartedAt.IsZero() || !r.ExpiresAt.Equal(r.StartedAt.Add(store.HandoffStartTTL)) {
		t.Fatalf("recorded start = %+v", r)
	}

	// 4. An unlinked viewer is refused cleanly and files nothing.
	unlinkedRow := store.Handoff{
		ID:        "unlinkedhandoffid123456789",
		Principal: "human:unlinked",
		Args:      []byte(`{"text":"the install link in README.md is dead","repository":"smana/ai-native-ref"}`),
		Plan:      mustPlanJSON(t, checked.Plan),
		Hash:      checked.Hash,
		CreatedAt: e.intake.clock(),
		ExpiresAt: e.intake.clock().Add(handoffTTL),
	}
	if err := st.PutHandoff(t.Context(), unlinkedRow); err != nil {
		t.Fatal(err)
	}
	unRec := post("/api/handoffs/"+unlinkedRow.ID+"/start", header("unlinked"), `{}`)
	if unRec.Code != http.StatusForbidden {
		t.Fatalf("unlinked start = %d %s, want 403", unRec.Code, unRec.Body.String())
	}
	var refBody struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(unRec.Body.Bytes(), &refBody) != nil || refBody.Error != startNotLinked {
		t.Fatalf("unlinked error body = %s, want error=%q", unRec.Body.String(), startNotLinked)
	}
	if len(starter.reqs) != 1 {
		t.Fatalf("unlinked start filed an issue: %d", len(starter.reqs))
	}

	// 5. A start the broker cannot record is answered as such, naming the filed issue: the factory
	// would not start it.
	starts.err = errors.New("database down")
	again := store.Handoff{ID: "unrecordedhandoffid1234567", Principal: "human:dev", Args: unlinkedRow.Args, Plan: unlinkedRow.Plan,
		Hash: checked.Hash, CreatedAt: e.intake.clock(), ExpiresAt: e.intake.clock().Add(handoffTTL)}
	if err := st.PutHandoff(t.Context(), again); err != nil {
		t.Fatal(err)
	}
	unRec = post("/api/handoffs/"+again.ID+"/start", header("dev"), `{}`)
	if unRec.Code != http.StatusServiceUnavailable || json.Unmarshal(unRec.Body.Bytes(), &refBody) != nil ||
		refBody.Error != "start_unrecorded" || !strings.Contains(unRec.Body.String(), "/issues/") {
		t.Fatalf("unrecorded start = %d %s", unRec.Code, unRec.Body.String())
	}
}

// Without a start store the broker files nothing: the factory could not confirm the App's label.
func TestStartWithoutAStartRecordFilesNothing(t *testing.T) {
	st := newMemHandoffStore()
	starter := &fakeIssueStarter{}
	w := world(nil)
	w.set(func() { w.writes = map[string][]string{"smana/ai-native-ref": {"dev"}} })
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withCatalogue(carefulCatalogue{}), withHandoffs(st),
		w.option(), func(s *Server, _ *fanout.Hub, _ *hubView) {
			s.StartDefault = true
			s.IssueStarter = starter
		})
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		req.Header = header("dev").Clone()
		rec := httptest.NewRecorder()
		e.srv.Routes().ServeHTTP(rec, req)
		return rec
	}
	cRec := post("/api/handoffs", `{"text":"the install link in README.md is dead"}`)
	var created handoffCreateResponse
	if cRec.Code != http.StatusOK || json.Unmarshal(cRec.Body.Bytes(), &created) != nil || created.ID == "" {
		t.Fatalf("create = %d %s", cRec.Code, cRec.Body.String())
	}
	rec := post("/api/handoffs/"+created.ID+"/start", `{}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "start_unavailable") || len(starter.reqs) != 0 {
		t.Fatalf("start = %d %s, filed %d", rec.Code, rec.Body.String(), len(starter.reqs))
	}
}

func mustPlanJSON(t *testing.T, p intake.Plan) []byte {
	t.Helper()
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type strangerCatalogue struct{ carefulCatalogue }

func (strangerCatalogue) Catalogue(_ context.Context, _, _, _ string) (catalogue.Catalogue, error) {
	return aiNativeRef(false), nil
}

// A writer the catalogue names as no maintainer is offered no Start, and Start files nothing: the
// factory would publicly refuse the App's factory/ready on that issue (broker-confirmed handoff).
func TestANonMaintainerIsOfferedNoStart(t *testing.T) {
	st := newMemHandoffStore()
	starter := &fakeIssueStarter{}
	starts := &fakeHandoffStarts{}
	w := world(nil)
	w.set(func() { w.writes = map[string][]string{"smana/ai-native-ref": {"dev"}} })
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withCatalogue(strangerCatalogue{}), withHandoffs(st),
		w.option(), func(s *Server, _ *fanout.Hub, _ *hubView) {
			s.StartDefault = true
			s.IssueStarter = starter
			s.HandoffStarts = starts
		})
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		req.Header = header("dev").Clone()
		rec := httptest.NewRecorder()
		e.srv.Routes().ServeHTTP(rec, req)
		return rec
	}
	cRec := post("/api/handoffs", `{"text":"the install link in README.md is dead"}`)
	var created handoffCreateResponse
	if cRec.Code != http.StatusOK || json.Unmarshal(cRec.Body.Bytes(), &created) != nil || created.ID == "" {
		t.Fatalf("create = %d %s", cRec.Code, cRec.Body.String())
	}
	var checked handoffCheckResponse
	chk := post("/api/handoffs/"+created.ID+"/check", `{}`)
	if chk.Code != http.StatusOK || json.Unmarshal(chk.Body.Bytes(), &checked) != nil {
		t.Fatalf("check = %d %s", chk.Code, chk.Body.String())
	}
	if checked.CanStart || checked.CanStartWhy != startNotMaintainer || !checked.Linked {
		t.Fatalf("checked canStart=%v why=%q linked=%v", checked.CanStart, checked.CanStartWhy, checked.Linked)
	}
	rec := post("/api/handoffs/"+created.ID+"/start", `{}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"not_maintainer"`) ||
		!strings.Contains(rec.Body.String(), "ask a maintainer") || len(starter.reqs) != 0 || len(starts.recs) != 0 {
		t.Fatalf("start = %d %s, filed %d, recorded %d", rec.Code, rec.Body.String(), len(starter.reqs), len(starts.recs))
	}
}

// POST /api/handoffs/{id}/check answers what handoff_check answers: under wholePlans, the switch and
// every issue of the plan, in order (one newHandoffChecked for both surfaces).
func TestHandoffCheckRouteCarriesTheWholePlan(t *testing.T) {
	h := WholePlanBroker(t)
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	cRec := post("/api/handoffs", `{"text":"fix the three stale docs pages"}`)
	var created handoffCreateResponse
	if cRec.Code != http.StatusOK || json.Unmarshal(cRec.Body.Bytes(), &created) != nil || created.ID == "" {
		t.Fatalf("create = %d %s", cRec.Code, cRec.Body.String())
	}
	if !created.Ready { // the template's open gaps, accepted as maku start accepts them on Enter
		if rec := post("/api/handoffs", `{"id":"`+created.ID+`","acceptRest":true}`); rec.Code != http.StatusOK {
			t.Fatalf("accept = %d %s", rec.Code, rec.Body.String())
		}
	}
	chk := post("/api/handoffs/"+created.ID+"/check", `{}`)
	var checked handoffCheckResponse
	if chk.Code != http.StatusOK || json.Unmarshal(chk.Body.Bytes(), &checked) != nil {
		t.Fatalf("check = %d %s", chk.Code, chk.Body.String())
	}
	if !checked.WholePlans || len(checked.Issues) != 3 || checked.Issues[2].Index != 3 || len(checked.Issues[2].DependsOn) != 2 {
		t.Fatalf("wholePlans=%v issues=%+v", checked.WholePlans, checked.Issues)
	}
}
