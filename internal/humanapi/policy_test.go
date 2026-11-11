// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Smana/maku/internal/factory/catalogue"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/policyview"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/runrequest"
	"github.com/Smana/maku/internal/store"
)

const policyRepo = "Smana/maku"

// fakePolicyFactory is the factory's evidence: docs-links at clean, eligible as given; revert live.
type fakePolicyFactory struct {
	clean  int
	bad    int
	drill  string
	tokens []string
}

func (f *fakePolicyFactory) PolicyEvidence(_ context.Context, token, repository string) (evidence.Report, error) {
	f.tokens = append(f.tokens, token)
	if !strings.EqualFold(repository, policyRepo) {
		return evidence.Report{}, runrequest.ErrRefused{Status: http.StatusNotFound, Reason: "not_found"}
	}
	// No drill is evidence still being read.
	var ev *evidence.Evidence
	var at *time.Time
	if f.drill != "" {
		ev = &evidence.Evidence{Clean: f.clean, Bad: f.bad, Drill: f.drill, Minimum: 20}
		ev.Eligible = f.clean >= 20 && f.bad == 0 && f.drill == evidence.DrillPassed
		read := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
		at = &read
	}
	return evidence.Report{Repository: policyRepo, Minimum: 20, ReadAt: time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), EvidenceAt: at, Classes: []evidence.ClassReport{
		{Class: "docs-links", Source: "repository", Mode: "shadow", Evidence: ev,
			Proposal: &evidence.Proposal{Path: evidence.PolicyPath, Line: 6, File: "factory: aws-0\n", EditURL: "https://github.com/Smana/maku/edit/main/.github/agent-factory.yaml"}},
		{Class: "revert", Source: "repository", Mode: "live", Unproposed: "not_shadow"},
	}}, nil
}

func (f *fakePolicyFactory) PolicyPreview(_ context.Context, token, repository, file string) (policyview.Preview, error) {
	f.tokens = append(f.tokens, token)
	if !strings.EqualFold(repository, policyRepo) {
		return policyview.Preview{}, runrequest.ErrRefused{Status: http.StatusNotFound, Reason: "not_found"}
	}
	return policyview.Preview{Valid: true, Proposal: &evidence.Proposal{Path: evidence.PolicyPath, File: file}}, nil
}

func (f *fakePolicyFactory) PolicyDecisions(_ context.Context, token, repository string, pr int) (policyview.Timeline, error) {
	f.tokens = append(f.tokens, token)
	if !strings.EqualFold(repository, policyRepo) {
		return policyview.Timeline{}, runrequest.ErrRefused{Status: http.StatusNotFound, Reason: "not_found"}
	}
	return policyview.Timeline{Repository: policyRepo, PullRequest: pr, Decisions: []policyview.Decision{}}, nil
}

func (f *fakePolicyFactory) PolicyRepositories(_ context.Context, token string) ([]string, error) {
	f.tokens = append(f.tokens, token)
	return []string{policyRepo}, nil
}

// memRecords is the store's promotion workflow, in memory, with its refusals.
type memRecords struct {
	mu         sync.Mutex
	promotions map[string]store.Promotion
	exceptions map[string]store.Exception
}

func (m *memRecords) RecordPromotion(_ context.Context, p store.Promotion) (store.Promotion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.promotions {
		if o.Repository == p.Repository && o.Class == p.Class && (o.State == store.PromotionRequested || o.State == store.PromotionPROpened) {
			return store.Promotion{}, store.ErrPromotionOpen
		}
	}
	p.State, p.RequestedAt = store.PromotionRequested, time.Now()
	m.promotions[p.ID] = p
	return p, nil
}

func (m *memRecords) MovePromotion(_ context.Context, id, state string, pr int, by string) (store.Promotion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.promotions[id]
	if !ok {
		return p, store.ErrNoPolicyRecord
	}
	if p.State != store.PromotionRequested && p.State != store.PromotionPROpened {
		return p, store.ErrPolicyMoved
	}
	p.State, p.ClosedBy = state, by
	if pr > 0 {
		p.PullRequest = pr
	}
	m.promotions[id] = p
	return p, nil
}

func (m *memRecords) Promotion(_ context.Context, id string) (store.Promotion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.promotions[id]
	if !ok {
		return p, store.ErrNoPolicyRecord
	}
	return p, nil
}

func (m *memRecords) RequestException(_ context.Context, e store.Exception) (store.Exception, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.State = store.ExceptionRequested
	m.exceptions[e.ID] = e
	return e, nil
}

func (m *memRecords) DecideException(_ context.Context, id, state, by, note string, expires time.Time) (store.Exception, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.exceptions[id]
	if !ok {
		return e, store.ErrNoPolicyRecord
	}
	if e.State != store.ExceptionRequested {
		return e, store.ErrPolicyMoved
	}
	e.State, e.DecidedBy, e.Note = state, by, note
	if state == store.ExceptionApproved {
		e.ExpiresAt = &expires
	}
	m.exceptions[id] = e
	return e, nil
}

func (m *memRecords) Exception(_ context.Context, id string) (store.Exception, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.exceptions[id]
	if !ok {
		return e, store.ErrNoPolicyRecord
	}
	return e, nil
}

func (m *memRecords) PolicyAudit(_ context.Context, repository string) ([]store.Promotion, []store.Exception, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps, es := []store.Promotion{}, []store.Exception{}
	for _, p := range m.promotions {
		if p.Repository == repository {
			ps = append(ps, p)
		}
	}
	for _, e := range m.exceptions {
		if e.Repository == repository {
			es = append(es, e)
		}
	}
	return ps, es, nil
}

var policyIndex = fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><html>maku</html>")}}

// policyOn wires the workflow: "maint" writes the repository, "ops" is the operator, everyone else
// reads it (w0's world).
func policyOn(f *fakePolicyFactory, m *memRecords) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) {
		w := w0()
		w.writes = map[string][]string{policyRepo: {"maint"}}
		w.gate(s)
		s.Humans, s.UI = tokenAuth{}, policyIndex
		n := 0
		s.Policy = &Policy{Factory: f, Records: m, Operator: func(l string) bool { return l == "ops" },
			NewID: func() string { n++; return strings.Repeat(string(rune('a'+n)), 8) }}
	}
}

func newPolicy(clean int) (*fakePolicyFactory, *memRecords) {
	return &fakePolicyFactory{clean: clean, drill: evidence.DrillPassed},
		&memRecords{promotions: map[string]store.Promotion{}, exceptions: map[string]store.Exception{}}
}

func getPolicy(t *testing.T, e env, path string, h http.Header) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.ts.URL+path, nil)
	req.Header = h.Clone()
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var policyPaths = []struct{ method, path string }{
	{http.MethodGet, "/settings"}, {http.MethodGet, "/settings/merge-policies"},
	{http.MethodGet, "/settings/merge-policies/Smana/maku"}, {http.MethodGet, "/settings/evidence"},
	{http.MethodGet, "/settings/merge-policies/Smana/maku/decisions/12"},
	{http.MethodPost, "/api/policy/repositories/Smana/maku/preview"}, {http.MethodGet, "/api/policy/repositories/Smana/maku/decisions/12"},
	{http.MethodGet, "/api/policy/repositories"}, {http.MethodGet, "/api/policy/repositories/Smana/maku"},
	{http.MethodPost, "/api/policy/promotions"}, {http.MethodPost, "/api/policy/promotions/bbbbbbbb"},
	{http.MethodPost, "/api/policy/exceptions"}, {http.MethodPost, "/api/policy/exceptions/bbbbbbbb"},
}

// Off (policyManagement.promotion false), every stage 6 path answers exactly as a path that never
// existed: the route table is the one before stage 6, and nothing reads the factory or the store.
func TestPolicyOffIsByteIdentical(t *testing.T) {
	e := setup(t, func(s *Server, _ *fanout.Hub, _ *hubView) { s.Humans, s.UI = tokenAuth{}, policyIndex })
	wantCode, want := sendJSON(t, e, http.MethodGet, "/never-a-route", header("maint"), "")
	for _, p := range policyPaths {
		if code, body := sendJSON(t, e, p.method, p.path, header("maint"), "{}"); code != wantCode || body != want {
			t.Errorf("%s %s: %d %q, want %d %q", p.method, p.path, code, body, wantCode, want)
		}
	}
}

// On, the settings pages are the SPA on a direct load.
func TestSettingsPagesServeTheApp(t *testing.T) {
	f, m := newPolicy(20)
	e := setup(t, policyOn(f, m))
	for _, p := range policyPaths[:5] {
		if code, body := getPolicy(t, e, p.path, header("anyone")); code != http.StatusOK || !strings.Contains(body, "maku</html>") {
			t.Errorf("%s: %d %q", p.path, code, body)
		}
	}
}

// A reader reads the evidence, the records and what they may do; the factory reads with their own
// token and refuses a repository they cannot read.
func TestPolicyRepositoryReadsForAReader(t *testing.T) {
	f, m := newPolicy(20)
	e := setup(t, policyOn(f, m))
	code, body := getPolicy(t, e, "/api/policy/repositories/Smana/maku", header("anyone"))
	var got struct {
		Report evidence.Report `json:"report"`
		You    roles           `json:"you"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got.Report.Repository != policyRepo ||
		got.You != (roles{Login: "anyone"}) || f.tokens[0] != "tok-anyone" {
		t.Fatalf("%d %s %q", code, body, f.tokens)
	}
	if code, _ := getPolicy(t, e, "/api/policy/repositories/Smana/elsewhere", header("anyone")); code != http.StatusNotFound {
		t.Fatalf("%d", code)
	}
	if code, _ := getPolicy(t, e, "/api/policy/repositories", header("anyone", "X-Test-Kind", "system")); code != http.StatusForbidden {
		t.Fatalf("a machine reads: %d", code)
	}
}

func promote(t *testing.T, e env, user string, body any) (int, string) {
	t.Helper()
	return sendJSON(t, e, http.MethodPost, "/api/policy/promotions", header(user), body)
}

// MP7 server side: a maintainer's request is recorded on the evidence read again here; anyone
// else, a short count, or a class with no proposal is refused, and nothing is recorded.
func TestPromotionRequest(t *testing.T) {
	req := map[string]string{"repository": policyRepo, "class": "docs-links"}
	for name, tc := range map[string]struct {
		user   string
		clean  int
		bad    int
		drill  string
		body   any
		code   int
		reason string
	}{
		"a maintainer, eligible": {"maint", 20, 0, evidence.DrillPassed, req, http.StatusCreated, ""},
		"a reader":               {"anyone", 20, 0, evidence.DrillPassed, req, http.StatusForbidden, "not_a_maintainer"},
		"nineteen":               {"maint", 19, 0, evidence.DrillPassed, req, http.StatusConflict, "not_eligible"},
		"a bad outcome":          {"maint", 30, 1, evidence.DrillPassed, req, http.StatusConflict, "not_eligible"},
		"no drill":               {"maint", 30, 0, evidence.DrillOpen, req, http.StatusConflict, "not_eligible"},
		"evidence being read":    {"maint", 30, 0, "", req, http.StatusConflict, "evidence_pending"},
		"a live class":           {"maint", 30, 0, evidence.DrillPassed, map[string]string{"repository": policyRepo, "class": "revert"}, http.StatusConflict, "not_promotable"},
		"no such class":          {"maint", 30, 0, evidence.DrillPassed, map[string]string{"repository": policyRepo, "class": "nope"}, http.StatusNotFound, "no_such_class"},
		"another repository":     {"maint", 30, 0, evidence.DrillPassed, map[string]string{"repository": "Smana/other", "class": "docs-links"}, http.StatusNotFound, "not_found"},
		"an unknown field":       {"maint", 30, 0, evidence.DrillPassed, `{"repository":"Smana/maku","class":"docs-links","mode":"live"}`, http.StatusBadRequest, "bad_json"},
		"an unbounded body":      {"maint", 30, 0, evidence.DrillPassed, `{"repository":"` + strings.Repeat("a", 9<<10) + `"}`, http.StatusBadRequest, "bad_json"},
	} {
		t.Run(name, func(t *testing.T) {
			f, m := newPolicy(tc.clean)
			f.bad, f.drill = tc.bad, tc.drill
			e := setup(t, policyOn(f, m))
			code, body := promote(t, e, tc.user, tc.body)
			if code != tc.code || (tc.reason != "" && !strings.Contains(body, `"`+tc.reason+`"`)) {
				t.Fatalf("%d %s", code, body)
			}
			if (code == http.StatusCreated) != (len(m.promotions) == 1) {
				t.Fatalf("recorded %d", len(m.promotions))
			}
		})
	}
}

// The recorded request carries its evidence, threshold and requester, and the answer is the
// proposal the maintainer submits; a second open request of the class is refused; the maintainer
// then names their pull request. The counter has one series per change.
func TestPromotionRecordsTheEvidenceAndMoves(t *testing.T) {
	f, m := newPolicy(20)
	e := setup(t, policyOn(f, m))
	code, body := promote(t, e, "maint", map[string]string{"repository": policyRepo, "class": "docs-links"})
	var out struct {
		Promotion store.Promotion   `json:"promotion"`
		Proposal  evidence.Proposal `json:"proposal"`
	}
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &out) != nil || out.Promotion.RequestedBy != "maint" ||
		out.Promotion.Threshold != 20 || !strings.Contains(string(out.Promotion.Evidence), `"clean":20`) || out.Proposal.Line != 6 {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := promote(t, e, "maint", map[string]string{"repository": policyRepo, "class": "docs-links"}); code != http.StatusConflict ||
		!strings.Contains(body, "promotion_open") {
		t.Fatalf("%d %s", code, body)
	}
	path := "/api/policy/promotions/" + out.Promotion.ID
	if code, _ := sendJSON(t, e, http.MethodPost, path, header("anyone"), map[string]any{"state": "pr_opened", "pullRequest": 12}); code != http.StatusForbidden {
		t.Fatalf("a reader moved it: %d", code)
	}
	if code, _ := sendJSON(t, e, http.MethodPost, path, header("maint"), map[string]any{"state": "requested"}); code != http.StatusBadRequest {
		t.Fatalf("back to requested: %d", code)
	}
	if code, body := sendJSON(t, e, http.MethodPost, path, header("maint"), map[string]any{"state": "pr_opened", "pullRequest": 12}); code != http.StatusOK ||
		!strings.Contains(body, `"pullRequest":12`) {
		t.Fatalf("%d %s", code, body)
	}
	scraped := scrape(t, e)
	for _, change := range []string{"promotion_requested", "promotion_pr_opened"} {
		if !strings.Contains(scraped, `maku_rooms_policy_changes_total{change="`+change+`",class="docs-links",repository="Smana/maku"} 1`) {
			t.Errorf("%s uncounted", change)
		}
	}
}

// MP7's exception: a maintainer asks below the minimum with a justification, an operator alone
// decides it, once, and the approved exception carries a promotion at its own minimum.
func TestExceptionIsAnOperatorsDecision(t *testing.T) {
	f, m := newPolicy(12)
	e := setup(t, policyOn(f, m))
	ask := map[string]any{"repository": policyRepo, "class": "docs-links", "minimum": 10, "justification": "a narrow class: ten clean decisions bound it"}
	if code, body := sendJSON(t, e, http.MethodPost, "/api/policy/exceptions", header("maint"),
		map[string]any{"repository": policyRepo, "class": "docs-links", "minimum": 20, "justification": ask["justification"]}); code != http.StatusBadRequest ||
		!strings.Contains(body, "minimum_not_below") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := sendJSON(t, e, http.MethodPost, "/api/policy/exceptions", header("anyone"), ask); code != http.StatusForbidden {
		t.Fatalf("a reader asked: %d", code)
	}
	code, body := sendJSON(t, e, http.MethodPost, "/api/policy/exceptions", header("maint"), ask)
	var exc store.Exception
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &exc) != nil || exc.RequestedBy != "maint" {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := promote(t, e, "maint", map[string]string{"repository": policyRepo, "class": "docs-links", "exceptionId": exc.ID}); code != http.StatusConflict ||
		!strings.Contains(body, "exception_unusable") {
		t.Fatalf("a requested exception carried it: %d %s", code, body)
	}
	path := "/api/policy/exceptions/" + exc.ID
	if code, _ := sendJSON(t, e, http.MethodPost, path, header("maint"), map[string]string{"decision": "approved"}); code != http.StatusForbidden {
		t.Fatalf("a maintainer approved: %d", code)
	}
	if code, body := sendJSON(t, e, http.MethodPost, path, header("ops"), map[string]string{"decision": "approved", "note": "ok for the canary"}); code != http.StatusOK ||
		!strings.Contains(body, `"decidedBy":"ops"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := sendJSON(t, e, http.MethodPost, path, header("ops"), map[string]string{"decision": "refused"}); code != http.StatusConflict {
		t.Fatalf("decided twice: %d", code)
	}
	code, body = promote(t, e, "maint", map[string]string{"repository": policyRepo, "class": "docs-links", "exceptionId": exc.ID})
	if code != http.StatusCreated || !strings.Contains(body, `"threshold":10`) || !strings.Contains(body, `"exceptionId":"`+exc.ID+`"`) {
		t.Fatalf("%d %s", code, body)
	}
}

// MP17, one row per role per handler: anyone who reads the repository views; a maintainer
// (write) previews, gets the edit link, requests and moves promotions and asks for exceptions;
// an operator decides exceptions; a machine and an anonymous caller do nothing.
func TestPolicyHandlersByRole(t *testing.T) {
	type route struct{ method, path, body string }
	routes := map[string]route{
		"repositories":     {http.MethodGet, "/api/policy/repositories", ""},
		"repository":       {http.MethodGet, "/api/policy/repositories/Smana/maku", ""},
		"decisions":        {http.MethodGet, "/api/policy/repositories/Smana/maku/decisions/12", ""},
		"settings":         {http.MethodGet, "/settings/merge-policies/Smana/maku", ""},
		"preview":          {http.MethodPost, "/api/policy/repositories/Smana/maku/preview", `{"file":"factory: aws-0\n"}`},
		"promote":          {http.MethodPost, "/api/policy/promotions", `{"repository":"Smana/maku","class":"docs-links"}`},
		"move":             {http.MethodPost, "/api/policy/promotions/pppppppp", `{"state":"pr_opened","pullRequest":12}`},
		"ask exception":    {http.MethodPost, "/api/policy/exceptions", `{"repository":"Smana/maku","class":"docs-links","minimum":10,"justification":"a narrow class: ten clean decisions bound it"}`},
		"decide exception": {http.MethodPost, "/api/policy/exceptions/xxxxxxxx", `{"decision":"approved"}`},
	}
	ok := func(code int) map[string]int {
		return map[string]int{"repositories": 200, "repository": 200, "decisions": 200, "settings": 200, "preview": code,
			"promote": code, "move": code, "ask exception": code, "decide exception": 403}
	}
	maint := ok(200)
	maint["promote"], maint["ask exception"] = 201, 201
	ops := ok(403)
	ops["decide exception"] = 200
	refused := func(code int) map[string]int {
		m := map[string]int{}
		for n := range routes {
			m[n] = code
		}
		m["settings"] = 200 // the page loads; every call it makes is refused
		return m
	}
	for role, want := range map[string]struct {
		h     http.Header
		codes map[string]int
	}{
		"reader":     {header("anyone"), ok(403)},
		"maintainer": {header("maint"), maint},
		"operator":   {header("ops"), ops},
		"machine":    {header("bot", "X-Test-Kind", "system"), refused(403)},
		"anonymous":  {http.Header{}, refused(401)},
	} {
		for name, rt := range routes {
			t.Run(role+" "+name, func(t *testing.T) {
				f, m := newPolicy(20)
				m.promotions["pppppppp"] = store.Promotion{ID: "pppppppp", Repository: policyRepo, Class: "revert", State: store.PromotionRequested}
				m.exceptions["xxxxxxxx"] = store.Exception{ID: "xxxxxxxx", Repository: policyRepo, Class: "docs-links", Minimum: 10, State: store.ExceptionRequested}
				e := setup(t, policyOn(f, m))
				var body any = ""
				if rt.body != "" {
					body = rt.body
				}
				code, out := sendJSON(t, e, rt.method, rt.path, want.h, body)
				if code != want.codes[name] {
					t.Fatalf("%d %s, want %d", code, out, want.codes[name])
				}
			})
		}
	}
}

// Profile choice at intake, stage 1: the repository page carries what the factory's catalogue says
// the repository offers, read with the caller's own token, so each class card can show its profiles;
// a catalogue that cannot be read leaves the page whole, with catalogue null.
func TestPolicyRepositoryCarriesCatalogue(t *testing.T) {
	f, m := newPolicy(20)
	src := &fakeFactoryCatalogue{}
	e := setup(t, policyOn(f, m), withCatalogue(src))
	code, body := getPolicy(t, e, "/api/policy/repositories/Smana/maku", header("anyone"))
	var got struct {
		Report    evidence.Report      `json:"report"`
		Catalogue *catalogue.Catalogue `json:"catalogue"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got.Catalogue == nil ||
		len(got.Catalogue.Classes) != 2 || got.Catalogue.Evaluation.Mode != "observe" || !got.Catalogue.Profiles["review-frontier"].Offered {
		t.Fatalf("%d %s", code, body)
	}
	if len(src.asked) != 1 || src.asked[0] != "Smana/maku|" {
		t.Fatalf("asked %q: the catalogue is read for the caller, with no onBehalfOf", src.asked)
	}

	t.Run("an unreadable catalogue leaves the page whole", func(t *testing.T) {
		down := &fakeFactoryCatalogue{byRepo: map[string]error{"Smana/maku": errors.New("factory down")}}
		e := setup(t, policyOn(f, m), withCatalogue(down))
		code, body := getPolicy(t, e, "/api/policy/repositories/Smana/maku", header("anyone"))
		var got struct {
			Report    evidence.Report `json:"report"`
			Catalogue json.RawMessage `json:"catalogue"`
		}
		if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got.Report.Repository != policyRepo || string(got.Catalogue) != "null" {
			t.Fatalf("%d %s", code, body)
		}
	})
	t.Run("no reader wired is the same", func(t *testing.T) {
		e := setup(t, policyOn(f, m))
		code, body := getPolicy(t, e, "/api/policy/repositories/Smana/maku", header("anyone"))
		if code != http.StatusOK || !strings.Contains(body, `"catalogue":null`) {
			t.Fatalf("%d %s", code, body)
		}
	})
}
