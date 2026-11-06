// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// needsBody is GET /api/needs as the broker sends it: one merge, in one room, with the cursor to ask from
// next and the access value that says why a list may be short.
const needsBody = `{"apiVersion":"needs/v1","access":"ok","items":[{"kind":"merge","id":"2239","room":"3kq7x2ma",` +
	`"what":"a maintainer approves PR #2239","url":"https://github.com/Smana/a/pull/2239",` +
	`"action":{"kind":"merge","label":"open the pull request","url":"https://github.com/Smana/a/pull/2239"}}],` +
	`"cursor":"2026-11-04T19:10:00Z"}`

// needsBroker serves GET /api/needs alone: the status, body and access header it answers with, and the query
// and bearer it was asked with.
type needsBroker struct {
	srv    *httptest.Server
	status int
	body   string
	access string
	mu     sync.Mutex
	query  string
	bearer string
}

func newNeedsBroker(t *testing.T, status int, body string) *needsBroker {
	t.Helper()
	b := &needsBroker{status: status, body: body}
	b.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.query, b.bearer = r.URL.RawQuery, r.Header.Get("Authorization")
		status, body, access := b.status, b.body, b.access
		b.mu.Unlock()
		if r.URL.Path != "/api/needs" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if status == 0 {
			status = http.StatusOK
		}
		if access != "" {
			w.Header().Set("X-Rooms-Access", access)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// client asks b as the developer: a fixed token, and the hints in a buffer the caller can read.
func (b *needsBroker) client(errs *strings.Builder) Client {
	return Client{URL: b.srv.URL, Token: fixed, HC: b.srv.Client(), Stream: b.srv.Client(), Err: errs}
}

// CD28: `maku needs` reads GET /api/needs with the filters it was given, and the route's own envelope comes
// back as items, cursor and the rooms whose state could not be read.
func TestNeedsAsksTheRouteItsFilters(t *testing.T) {
	b := newNeedsBroker(t, http.StatusOK, needsBody)
	var errs strings.Builder
	page, err := b.client(&errs).Needs(t.Context(), NeedsFilter{Since: "2026-11-04T19:10:00Z", Room: "3kq7x2ma", Repo: "Smana/a"})
	if err != nil {
		t.Fatal(err)
	}
	if b.query != "repo=Smana%2Fa&room=3kq7x2ma&since=2026-11-04T19%3A10%3A00Z" || b.bearer != "Bearer tok" {
		t.Fatalf("query %q, bearer %q", b.query, b.bearer)
	}
	if len(page.Items) != 1 || page.Items[0].Kind != "merge" || page.Items[0].ID != "2239" || page.Items[0].Room != "3kq7x2ma" ||
		page.Items[0].Action.URL != "https://github.com/Smana/a/pull/2239" {
		t.Fatalf("the items: %+v", page.Items)
	}
	if page.Cursor != "2026-11-04T19:10:00Z" || len(page.Unread) != 0 {
		t.Fatalf("cursor %q, unread %v", page.Cursor, page.Unread)
	}
	if errs.String() != "" {
		t.Fatalf("an access value maku explains printed nothing unexpected: %q", errs.String())
	}
	// No filter is no query: an empty filter asks the whole list.
	if _, err := b.client(&errs).Needs(t.Context(), NeedsFilter{}); err != nil || b.query != "" {
		t.Fatalf("no filter: %q %v", b.query, err)
	}
}

// Review Focus 1: a room whose state could not be read is named in the page, so a view can say so.
func TestNeedsCarriesTheRoomsItCouldNotRead(t *testing.T) {
	b := newNeedsBroker(t, http.StatusOK, `{"apiVersion":"needs/v1","items":[],"cursor":"2026-11-04T19:10:00Z","unread":["5kq7x2mc"]}`)
	page, err := b.client(&strings.Builder{}).Needs(t.Context(), NeedsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || len(page.Unread) != 1 || page.Unread[0] != "5kq7x2mc" {
		t.Fatalf("%+v", page)
	}
}

// A refusal is the broker's, explained: a token to renew, a route the broker does not serve (the switch is
// off, or the broker predates it), and a `since` it would not read, which names its own format.
func TestNeedsRefusesWhatTheRouteRefuses(t *testing.T) {
	cases := []struct {
		name, body, want string
		status           int
	}{
		{"unauthenticated", "", "maku login", http.StatusUnauthorized},
		{"off", "", "human.needs.enabled", http.StatusNotFound},
		{"since", "since is an RFC 3339 instant, as this answer's cursor is, and never in the future\n", "never in the future", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newNeedsBroker(t, c.status, c.body)
			_, err := b.client(&strings.Builder{}).Needs(t.Context(), NeedsFilter{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%d: %v, want %q", c.status, err, c.want)
			}
		})
	}
}

// The room list's courtesy, for the same route family: when the access value says the list may be short,
// the hint names the cause, on the error stream so stdout stays the list.
func TestNeedsSaysWhyTheListMayBeShort(t *testing.T) {
	b := newNeedsBroker(t, http.StatusOK, needsBody)
	b.access = "unlinked"
	var errs strings.Builder
	if _, err := b.client(&errs).Needs(t.Context(), NeedsFilter{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs.String(), "no linked GitHub identity") {
		t.Fatalf("the hint: %q", errs.String())
	}
}

// --json prints what the broker sent: the page keeps the answer's bytes, apiVersion and access included,
// which a rebuilt envelope would lose.
func TestNeedsKeepsTheAnswerForJSON(t *testing.T) {
	b := newNeedsBroker(t, http.StatusOK, needsBody)
	page, err := b.client(&strings.Builder{}).Needs(t.Context(), NeedsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(page.Raw()); got != needsBody {
		t.Fatalf("raw %q", got)
	}
}

// A body the page cannot read is an error naming the route, never a quiet list.
func TestNeedsRefusesAGarbledAnswer(t *testing.T) {
	b := newNeedsBroker(t, http.StatusOK, `{"apiVersion":"needs/v1","items":[{"kind":"merge","deadline":"soon"}]}`)
	if _, err := b.client(&strings.Builder{}).Needs(context.Background(), NeedsFilter{}); err == nil || !strings.Contains(err.Error(), "needs:") {
		t.Fatalf("%v", err)
	}
}
