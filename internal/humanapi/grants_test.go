// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/grant"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/store"
)

// memGrants is the store's grants in memory, with the store's transition rules for the moves
// the human API makes.
type memGrants struct {
	mu     sync.Mutex
	grants map[string]store.Grant
	events map[string][]store.GrantEvent
}

func (m *memGrants) CreateGrant(_ context.Context, n store.NewGrant) (store.Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	g := store.Grant{ID: n.ID, Version: 1, Repository: n.Repository, Class: n.Class, Files: n.Files, DiffShape: n.DiffShape,
		PolicyHash: n.PolicyHash, OriginPR: n.OriginPR, CreatedBy: n.CreatedBy, CreatedAt: now, RenewedAt: now,
		ExpiresAt: now.Add(n.Lifetime), Status: "active", StatusAt: now}
	m.grants[g.ID] = g
	m.events[g.ID] = append(m.events[g.ID], store.GrantEvent{GrantID: g.ID, Version: 1, Event: "created", Actor: n.CreatedBy})
	return g, nil
}

func (m *memGrants) Grant(_ context.Context, id string) (store.Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	if !ok {
		return g, store.ErrNoGrant
	}
	return g, nil
}

func (m *memGrants) Grants(_ context.Context, f store.GrantFilter) ([]store.Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Grant
	for _, g := range m.grants {
		if (f.Repository == "" || g.Repository == f.Repository) && (f.Status == "" || g.Status == f.Status) {
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(a, b store.Grant) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (m *memGrants) GrantEvents(_ context.Context, id string) ([]store.GrantEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.events[id]), nil
}

func (m *memGrants) MoveGrant(_ context.Context, id string, mv store.GrantMove) (store.Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.grants[id]
	from := map[string][]string{"revoked": {"active", "suspended", "expired"}, "renewed": {"active", "expired"}, "reconfirmed": {"suspended"}}
	if !slices.Contains(from[mv.Event], g.Status) {
		return g, store.ErrGrantState
	}
	g.Status = "active"
	switch mv.Event {
	case "revoked":
		g.Status = "revoked"
	case "renewed":
		g.Version++
		g.ExpiresAt = time.Now().Add(mv.Lifetime)
	case "reconfirmed":
		g.Version++
		g.PolicyHash = mv.PolicyHash
	}
	m.grants[id] = g
	m.events[id] = append(m.events[id], store.GrantEvent{GrantID: id, Version: g.Version, Event: mv.Event, Actor: mv.Actor, PolicyHash: mv.PolicyHash})
	return g, nil
}

const (
	grantRepo  = "smana/maku"
	otherRepo  = "smana/secret"
	publicURL  = "https://rooms.test"
	grantA     = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	grantB     = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	grantC     = "cccccccccccccccccccccccccc"
	grantHash  = "0123456789ab"
	suspendRef = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

// grantsOn serves approval memory over memGrants holding one grant on smana/maku (A, suspended
// under suspendRef) and one on smana/secret (B). bob writes smana/maku, alice reads it, carol is
// unlinked, and nobody but an admin reads smana/secret.
func grantsOn(m *memGrants) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) {
		w := world(map[string][]string{grantRepo: {"alice", "bob"}})
		w.writes = map[string][]string{grantRepo: {"bob"}}
		w.unlinked["carol"] = true
		w.gate(s)
		s.Grants, s.PublicURL, s.UI = m, publicURL, assetsFS()
		now := time.Now()
		m.grants = map[string]store.Grant{
			grantA: {ID: grantA, Version: 1, Repository: grantRepo, Class: "docs-broad", Files: []string{`docs/security/a\.md`},
				PolicyHash: grantHash, OriginPR: 4, CreatedBy: "bob", ExpiresAt: now.Add(time.Hour), Status: "suspended"},
			grantC: {ID: grantC, Version: 1, Repository: grantRepo, Class: "docs-broad", Files: []string{"y"},
				PolicyHash: grantHash, OriginPR: 6, CreatedBy: "bob", ExpiresAt: now.Add(time.Hour), Status: "active"},
			grantB: {ID: grantB, Version: 1, Repository: otherRepo, Class: "docs-broad", Files: []string{"x"},
				PolicyHash: grantHash, OriginPR: 5, CreatedBy: "dana", ExpiresAt: now.Add(time.Hour), Status: "active"},
		}
		m.events = map[string][]store.GrantEvent{grantA: {{Event: "created", Version: 1}, {Event: "suspended", Version: 1, PolicyHash: suspendRef}}}
	}
}

func web(user string) http.Header {
	return http.Header{"X-Test-User": {user}, "Origin": {publicURL}, "Content-Type": {"application/json"}}
}

// Off, approval memory serves nothing: neither the page nor the API.
func TestApprovalMemoryOffServesNothing(t *testing.T) {
	e := setup(t, func(s *Server, _ *fanout.Hub, _ *hubView) { s.UI = assetsFS() })
	for _, path := range []string{grantsPage, "/api/approval-memory/grants", "/api/approval-memory/export"} {
		if code, _ := get(t, e, path, as("bob", false)); code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, code)
		}
	}
}

// A reader lists, reads and exports the grants of the repositories they may read, and only
// those; the page loads directly as the SPA.
func TestGrantsAreListedToTheirRepositorysReaders(t *testing.T) {
	e := setup(t, grantsOn(&memGrants{}))
	if code, body := get(t, e, grantsPage, as("alice", false)); code != http.StatusOK || !strings.Contains(body, "<html") {
		t.Fatalf("the page's direct load: %d", code)
	}
	ids := func(user string, admin bool, query string) []string {
		code, body := get(t, e, "/api/approval-memory/grants"+query, as(user, admin))
		var out struct{ Grants []grantView }
		if code != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil {
			t.Fatalf("%d %s", code, body)
		}
		var got []string
		for _, g := range out.Grants {
			got = append(got, g.ID)
		}
		return got
	}
	for name, tc := range map[string]struct {
		user  string
		admin bool
		query string
		want  []string
	}{
		"a reader":                 {"alice", false, "", []string{grantA, grantC}},
		"an admin":                 {"zed", true, "", []string{grantA, grantB, grantC}},
		"an unlinked member":       {"carol", false, "", nil},
		"a search":                 {"zed", true, "?q=SECURITY", []string{grantA}},
		"by status":                {"zed", true, "?status=active", []string{grantB, grantC}},
		"another repository asked": {"alice", false, "?repository=Smana/Secret", nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ids(tc.user, tc.admin, tc.query); !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if code, body := get(t, e, "/api/approval-memory/grants/"+grantA, as("alice", false)); code != http.StatusOK ||
		!strings.Contains(body, `"event":"suspended"`) || !strings.Contains(body, "the breaker and the daily caps") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := get(t, e, "/api/approval-memory/grants/"+grantB, as("alice", false)); code != http.StatusNotFound {
		t.Fatalf("a grant of an unreadable repository: %d", code)
	}
	code, body := get(t, e, "/api/approval-memory/export", as("alice", false))
	if code != http.StatusOK || !strings.Contains(body, "kind: ApprovalGrantExport") || !strings.Contains(body, "id: "+grantA) ||
		strings.Contains(body, grantB) {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := get(t, e, "/api/approval-memory/grants?status=gone", as("alice", false)); code != http.StatusBadRequest {
		t.Fatalf("an unknown status: %d", code)
	}
}

// "Merge and create rule": a maintainer of the repository saves an exact predicate, from the web
// UI's session and this broker's origin; the answer shows it, what stays outside it, its expiry
// and its revocation path. Every other caller is refused by the server.
func TestAMaintainerCreatesAGrantFromTheWebUI(t *testing.T) {
	m := &memGrants{}
	e := setup(t, grantsOn(m))
	body := map[string]any{"repository": grantRepo, "class": "docs-broad", "files": []string{`docs/security/b\.md`},
		"policyHash": grantHash, "pullRequest": 9}
	code, out := sendJSON(t, e, http.MethodPost, "/api/approval-memory/grants", web("bob"), body)
	var created struct {
		Grant     grantView
		Predicate grant.Predicate
		Outside   []string
		ExpiresAt time.Time
		Revoke    string
	}
	if code != http.StatusCreated || json.Unmarshal([]byte(out), &created) != nil {
		t.Fatalf("%d %s", code, out)
	}
	if created.Grant.CreatedBy != "bob" || created.Grant.OriginPR != 9 || created.Predicate.Files[0] != `docs/security/b\.md` ||
		len(created.Outside) == 0 || created.Revoke != "POST /api/approval-memory/grants/"+created.Grant.ID+"/revoke" ||
		time.Until(created.ExpiresAt) < 89*24*time.Hour || !grant.IDRE.MatchString(created.Grant.ID) {
		t.Fatalf("%+v", created)
	}
	maku := web("bob")
	maku.Set("X-Test-Client", "maku")
	foreign := web("bob")
	foreign.Set("Origin", "https://evil.test")
	noOrigin := web("bob")
	noOrigin.Del("Origin")
	bad := func(f func(map[string]any)) map[string]any {
		b := map[string]any{}
		for k, v := range body {
			b[k] = v
		}
		f(b)
		return b
	}
	for name, tc := range map[string]struct {
		h      http.Header
		body   any
		code   int
		reason string
	}{
		"a reader":            {web("alice"), body, http.StatusForbidden, "repository_permission"},
		"another repository":  {web("bob"), bad(func(b map[string]any) { b["repository"] = otherRepo }), http.StatusForbidden, "repository_permission"},
		"maku's bearer":       {maku, body, http.StatusForbidden, "web_only"},
		"another site's page": {foreign, body, http.StatusForbidden, "cross_origin"},
		"no Origin":           {noOrigin, body, http.StatusForbidden, "cross_origin"},
		"a broken pattern":    {web("bob"), bad(func(b map[string]any) { b["files"] = []string{"("} }), http.StatusBadRequest, "bad_predicate"},
		"no pull request":     {web("bob"), bad(func(b map[string]any) { delete(b, "pullRequest") }), http.StatusBadRequest, "bad_predicate"},
		"two years":           {web("bob"), bad(func(b map[string]any) { b["lifetimeDays"] = 730 }), http.StatusBadRequest, "bad_predicate"},
		"an unknown field":    {web("bob"), bad(func(b map[string]any) { b["status"] = "active" }), http.StatusBadRequest, "bad_json"},
		"an oversize body":    {web("bob"), bad(func(b map[string]any) { b["class"] = strings.Repeat("a", maxGrantWrite) }), http.StatusBadRequest, "bad_json"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out := sendJSON(t, e, http.MethodPost, "/api/approval-memory/grants", tc.h, tc.body)
			if code != tc.code || !strings.Contains(out, `"`+tc.reason+`"`) {
				t.Fatalf("%d %s", code, out)
			}
		})
	}
	if len(m.grants) != 4 {
		t.Fatalf("%d grants: a refused create stored one", len(m.grants))
	}
}

// Any maintainer revokes, as does an admin; renewing and reconfirming take a maintainer. A
// reconfirmation binds the grant to the policy its suspension was found under.
func TestGrantMovesAreTheMaintainers(t *testing.T) {
	m := &memGrants{}
	e := setup(t, grantsOn(m))
	post := func(h http.Header, id, action string, body any) (int, string) {
		return sendJSON(t, e, http.MethodPost, "/api/approval-memory/grants/"+id+"/"+action, h, body)
	}
	if code, out := post(web("alice"), grantA, "reconfirm", map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("a reader reconfirmed: %d %s", code, out)
	}
	if code, out := post(web("bob"), grantA, "reconfirm", map[string]any{}); code != http.StatusOK || m.grants[grantA].PolicyHash != suspendRef ||
		m.grants[grantA].Version != 2 {
		t.Fatalf("%d %s %+v", code, out, m.grants[grantA])
	}
	if code, _ := post(web("bob"), grantA, "reconfirm", map[string]any{}); code != http.StatusConflict {
		t.Fatalf("an active grant reconfirmed again: %d", code)
	}
	if code, _ := post(web("bob"), grantA, "renew", map[string]any{"lifetimeDays": 30}); code != http.StatusOK || m.grants[grantA].Version != 3 {
		t.Fatalf("renew: %d", code)
	}
	if code, _ := post(web("bob"), grantB, "revoke", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("a grant of an unreadable repository revoked: %d", code)
	}
	admin := web("zed")
	admin.Set("X-Test-Admin", "1")
	if code, _ := post(admin, grantB, "renew", map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("an admin renewed: %d", code)
	}
	if code, out := post(admin, grantB, "revoke", map[string]any{"reason": "a defect"}); code != http.StatusOK || m.grants[grantB].Status != "revoked" {
		t.Fatalf("an admin's revoke: %d %s", code, out)
	}
	if code, _ := post(web("bob"), grantA, "promote", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("an unknown action: %d", code)
	}
}

// MP17, per handler and per role, each against a fresh store: an anonymous caller is 401, a member
// of no agents group 403; members read the grants of the repositories they read (an unlinked one
// reads none, and a grant it may not read is a 404); only a maintainer (GitHub write) previews,
// creates, renews and reconfirms; any maintainer, or an admin, revokes; and maku's bearer, a
// maintainer's own, writes nothing.
func TestEveryGrantHandlerChecksItsCallerServerSide(t *testing.T) {
	rule := map[string]any{"repository": grantRepo, "class": "docs-broad", "caughtFiles": []string{"docs/security/c.md"},
		"policyHash": grantHash, "pullRequest": 11}
	type call struct {
		method, path string
		body         any
	}
	handlers := map[string]call{
		"page":      {http.MethodGet, grantsPage, nil},
		"list":      {http.MethodGet, "/api/approval-memory/grants", nil},
		"detail":    {http.MethodGet, "/api/approval-memory/grants/" + grantA, nil},
		"export":    {http.MethodGet, "/api/approval-memory/export", nil},
		"preview":   {http.MethodPost, "/api/approval-memory/preview", rule},
		"create":    {http.MethodPost, "/api/approval-memory/grants", rule},
		"revoke":    {http.MethodPost, "/api/approval-memory/grants/" + grantC + "/revoke", map[string]any{}},
		"renew":     {http.MethodPost, "/api/approval-memory/grants/" + grantC + "/renew", map[string]any{}},
		"reconfirm": {http.MethodPost, "/api/approval-memory/grants/" + grantA + "/reconfirm", map[string]any{}},
	}
	anonymous := http.Header{"Origin": {publicURL}, "Content-Type": {"application/json"}}
	stranger, admin, maku := web("stranger"), web("zed"), web("bob")
	admin.Set("X-Test-Admin", "1")
	maku.Set("X-Test-Client", "maku")
	roles := map[string]http.Header{"anonymous": anonymous, "stranger": stranger, "unlinked": web("carol"), "reader": web("alice"),
		"maintainer": web("bob"), "admin": admin, "maku bearer": maku}
	// want[handler][role]: the status each caller gets.
	want := map[string]map[string]int{
		"page":      {"anonymous": 200, "stranger": 200, "unlinked": 200, "reader": 200, "maintainer": 200, "admin": 200, "maku bearer": 200},
		"list":      {"anonymous": 401, "stranger": 403, "unlinked": 200, "reader": 200, "maintainer": 200, "admin": 200, "maku bearer": 200},
		"detail":    {"anonymous": 401, "stranger": 403, "unlinked": 404, "reader": 200, "maintainer": 200, "admin": 200, "maku bearer": 200},
		"export":    {"anonymous": 401, "stranger": 403, "unlinked": 200, "reader": 200, "maintainer": 200, "admin": 200, "maku bearer": 200},
		"preview":   {"anonymous": 401, "stranger": 403, "unlinked": 403, "reader": 403, "maintainer": 200, "admin": 403, "maku bearer": 403},
		"create":    {"anonymous": 401, "stranger": 403, "unlinked": 403, "reader": 403, "maintainer": 201, "admin": 403, "maku bearer": 403},
		"revoke":    {"anonymous": 401, "stranger": 403, "unlinked": 404, "reader": 403, "maintainer": 200, "admin": 200, "maku bearer": 403},
		"renew":     {"anonymous": 401, "stranger": 403, "unlinked": 404, "reader": 403, "maintainer": 200, "admin": 403, "maku bearer": 403},
		"reconfirm": {"anonymous": 401, "stranger": 403, "unlinked": 404, "reader": 403, "maintainer": 200, "admin": 403, "maku bearer": 403},
	}
	for handler, c := range handlers {
		for role, h := range roles {
			t.Run(handler+"/"+role, func(t *testing.T) {
				m := &memGrants{}
				e := setup(t, grantsOn(m))
				var code int
				var body string
				if c.method == http.MethodGet {
					code, body = get(t, e, c.path, h)
				} else {
					code, body = sendJSON(t, e, c.method, c.path, h, c.body)
				}
				if code != want[handler][role] {
					t.Fatalf("%d, want %d: %s", code, want[handler][role], body)
				}
				wrote := len(m.grants) != 3 || m.grants[grantC].Status != "active" || m.grants[grantC].Version != 1 || m.grants[grantA].Status != "suspended"
				if wrote != (code == 201 || (code == 200 && c.method == http.MethodPost && handler != "preview")) {
					t.Fatalf("the store moved on a %d: %+v", code, m.grants)
				}
			})
		}
	}
}

// The preview is the exact rule a create would save, and saves nothing: each caught file becomes
// an exact pattern, the future pull requests it would match are said in words, and the answer
// names the conditions outside it, its expiry, its revocation path and what merge once does.
func TestThePreviewShowsTheExactRuleAndSavesNothing(t *testing.T) {
	m := &memGrants{}
	e := setup(t, grantsOn(m))
	code, out := sendJSON(t, e, http.MethodPost, "/api/approval-memory/preview", web("bob"), map[string]any{
		"repository": "Smana/Maku", "class": "docs-broad", "caughtFiles": []string{"docs/security/a.md", "docs/security/b+c.md"},
		"policyHash": grantHash, "pullRequest": 11, "lifetimeDays": 30})
	var p ruleProposal
	if code != http.StatusOK || json.Unmarshal([]byte(out), &p) != nil {
		t.Fatalf("%d %s", code, out)
	}
	if !slices.Equal(p.Predicate.Files, []string{`docs/security/a\.md`, `docs/security/b\+c\.md`}) || p.Predicate.Repository != grantRepo ||
		!strings.Contains(p.Matches, "requiresApproval subset matches one of") || len(p.Outside) == 0 || p.Grant != nil ||
		!strings.Contains(p.Revoke, "/revoke") || !strings.Contains(p.MergeOnce, "that head only") ||
		time.Until(p.ExpiresAt) < 29*24*time.Hour || time.Until(p.ExpiresAt) > 31*24*time.Hour {
		t.Fatalf("%+v", p)
	}
	if len(m.grants) != 3 {
		t.Fatalf("a preview saved a grant: %d", len(m.grants))
	}
	if code, _ := sendJSON(t, e, http.MethodPost, "/api/approval-memory/preview", web("bob"), map[string]any{
		"repository": grantRepo, "class": "docs-broad", "caughtFiles": []string{"a"}, "files": []string{"a"},
		"policyHash": grantHash, "pullRequest": 11}); code != http.StatusBadRequest {
		t.Fatalf("both files and caughtFiles: %d", code)
	}
}
