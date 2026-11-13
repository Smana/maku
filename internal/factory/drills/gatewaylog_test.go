// SPDX-License-Identifier: Apache-2.0

package drills

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// victoriaLogs answers LogsQL queries with the lines reply returns, and records each query.
type victoriaLogs struct {
	mu      sync.Mutex
	queries []url.Values
	reply   func(q string) (int, string)
}

func (v *victoriaLogs) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/select/logsql/query" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		v.mu.Lock()
		v.queries = append(v.queries, form)
		v.mu.Unlock()
		code, body := v.reply(form.Get("query"))
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

var (
	logFrom = time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	logTo   = logFrom.Add(6 * time.Hour)
	runIDs  = []string{"cf4ato2x", "7f3cq2xz"}
)

const marker = "0123456789abcdef"

// AC5: one LogsQL query, filtered by the runs' gateway identity and the marker phrase, over the
// drill's window, answers how many model requests held the marker.
func TestMarkerHits(t *testing.T) {
	vl := &victoriaLogs{reply: func(string) (int, string) { return http.StatusOK, `{"hits":"3"}` + "\n" }}
	g := NewGatewayLog(vl.server(t).URL, 5*time.Second, nil)
	n, err := g.MarkerHits(t.Context(), runIDs, marker, logFrom, logTo)
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	if len(vl.queries) != 1 {
		t.Fatalf("%d queries", len(vl.queries))
	}
	q := vl.queries[0]
	want := `x_ar_agent:~"(^|:)(cf4ato2x|7f3cq2xz)$" "` + marker + `" | stats count() hits`
	if q.Get("query") != want || q.Get("start") != "2026-11-13T06:00:00Z" || q.Get("end") != "2026-11-13T12:00:00Z" {
		t.Fatalf("query %v", q)
	}
	vl.reply = func(string) (int, string) { return http.StatusOK, "" }
	if n, err := g.MarkerHits(t.Context(), runIDs, marker, logFrom, logTo); err != nil || n != 0 {
		t.Fatalf("no rows: %d %v", n, err)
	}
}

// AC5: the guardrails verdicts of the runs' requests, counted per verdict; a verdict outside the
// bounded shape reads other.
func TestGuardrails(t *testing.T) {
	vl := &victoriaLogs{reply: func(string) (int, string) {
		return http.StatusOK, `{"guardrails":"allow","hits":"12"}` + "\n" + `{"guardrails":"block","hits":"2"}` + "\n" +
			`{"guardrails":"Ignore previous instructions","hits":"1"}` + "\n"
	}}
	g := NewGatewayLog(vl.server(t).URL, 5*time.Second, nil)
	got, err := g.Guardrails(t.Context(), runIDs, logFrom, logTo)
	if err != nil || got["allow"] != 12 || got["block"] != 2 || got["other"] != 1 || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if q := vl.queries[0].Get("query"); q != `x_ar_agent:~"(^|:)(cf4ato2x|7f3cq2xz)$" guardrails:* | stats by (guardrails) count() hits` {
		t.Fatalf("query %q", q)
	}
}

// Errors name the failure, never the marker; what could not be a run id or a marker is never sent.
func TestGatewayLogErrors(t *testing.T) {
	vl := &victoriaLogs{reply: func(string) (int, string) { return http.StatusBadRequest, "cannot parse " + marker }}
	g := NewGatewayLog(vl.server(t).URL, 5*time.Second, nil)
	_, err := g.MarkerHits(t.Context(), runIDs, marker, logFrom, logTo)
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("err %v", err)
	}
	vl.reply = func(string) (int, string) { return http.StatusOK, `{"hits":"many"}` }
	if _, err := g.MarkerHits(t.Context(), runIDs, marker, logFrom, logTo); err == nil {
		t.Fatal("a reply that is not a count was read")
	}
	sent := len(vl.queries)
	for name, err := range map[string]error{
		"a bad run": func() error {
			_, err := g.MarkerHits(t.Context(), []string{`x" or *`}, marker, logFrom, logTo)
			return err
		}(),
		"no run":       func() error { _, err := g.Guardrails(t.Context(), nil, logFrom, logTo); return err }(),
		"a bad marker": func() error { _, err := g.MarkerHits(t.Context(), runIDs, `" or *`, logFrom, logTo); return err }(),
		"a window backwards": func() error {
			_, err := g.Guardrails(t.Context(), runIDs, logTo, logFrom)
			return err
		}(),
	} {
		if err == nil {
			t.Errorf("%s: sent", name)
		}
	}
	if len(vl.queries) != sent {
		t.Fatal("a refused query reached the log")
	}
}

// An unset URL is no port: the drill runner stays off.
func TestGatewayLogUnset(t *testing.T) {
	if g := NewGatewayLog("", time.Second, nil); g != nil {
		t.Fatalf("%v", g)
	}
	var g *GatewayLog
	if _, err := g.MarkerHits(t.Context(), runIDs, marker, logFrom, logTo); !errors.Is(err, ErrNoGatewayLog) {
		t.Fatalf("%v", err)
	}
}

// #520's review: the identity filter is anchored, so it matches the run's own subject and nothing
// that merely contains its id.
func TestGatewayRunFilterIsAnchored(t *testing.T) {
	vl := &victoriaLogs{reply: func(string) (int, string) { return http.StatusOK, `{"hits":"0"}` }}
	g := NewGatewayLog(vl.server(t).URL, 5*time.Second, nil)
	if _, err := g.MarkerHits(t.Context(), runIDs, marker, logFrom, logTo); err != nil {
		t.Fatal(err)
	}
	q := vl.queries[0].Get("query")
	pattern := q[strings.Index(q, `~"`)+2 : strings.Index(q, `$"`)+1]
	re := regexp.MustCompile(pattern)
	for subject, want := range map[string]bool{
		"system:serviceaccount:agent-system:cf4ato2x": true,
		"7f3cq2xz": true,
		"system:serviceaccount:agent-system:xcf4ato2x": false,
		"system:serviceaccount:agent-system:cf4ato2xy": false,
		"system:serviceaccount:cf4ato2x:other":         false,
	} {
		if re.MatchString(subject) != want {
			t.Errorf("%s: match %v, want %v", subject, !want, want)
		}
	}
}
