// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape: %d", rec.Code)
	}
	b, _ := io.ReadAll(rec.Body)
	return string(b)
}

// The alerts and the plan's evidence query these names byte for byte: the
// exporter must add no unit or counter suffix, and drop no _total.
func TestExposedNamesAreTheOnesTheAlertsQuery(t *testing.T) {
	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	now := time.Unix(1_700_000_000, 0)
	s, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	s.Appended(ctx, "message", "harness", []string{"jwt"})
	s.AppendTook(ctx, 3*time.Millisecond)
	s.AppendFailed(ctx)
	s.ObserveRoom("3kq7x2ma", "Active", true, 2, now.Add(-time.Minute))
	s.ObserveRoom("4kq7x2ma", "Closed", false, 0, now)
	s.Participants.Add(ctx, 1)
	s.Connections.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", "human")))
	s.FanoutLag.Record(ctx, 0.01)
	s.ApprovalsOldest.Record(ctx, 12)
	s.DecisionSeconds.Record(ctx, 5)
	s.DriverChanges.Add(ctx, 1)
	s.Rejected.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "not_permitted")))
	s.Dropped.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "slow")))
	s.VerdictPosts.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "posted")))
	s.MCPCalls.Add(ctx, 1, metric.WithAttributes(attribute.String("tool", "room_summary"), attribute.String("outcome", "ok")))
	if err := s.WatchJWKS(map[string]func() time.Time{"https://issuer": func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	s.WebhookDelivery(ctx, "slack-app", "delivered")
	s.WebhookDisabled(ctx, "slack-app", true)
	s.DBTLS("rooms_broker", false)
	s.SchemaUnverified(true)
	s.BridgeStalled(ctx, "cursor_lost")
	s.BridgeStubbed(ctx, "refused")

	body := scrape(t, exp.Handler())
	for _, want := range []string{
		`maku_rooms_build_info{version="v1.2.3"} 1`,
		`rooms{phase="Active"} 1`,
		`rooms{phase="Closed"} 1`,
		`maku_rooms_participants 1`,
		`maku_rooms_connections{kind="human"} 1`,
		`maku_rooms_events_appended_total{origin="harness",type="message"} 1`,
		`maku_rooms_append_seconds_bucket{le="0.005"} 1`,
		`maku_rooms_append_errors_total 1`,
		`maku_rooms_fanout_lag_seconds_bucket{le="0.01"} 1`,
		`maku_rooms_approvals_pending 2`,
		`maku_rooms_approvals_oldest_pending_seconds 12`,
		`maku_rooms_approval_decision_seconds_count 1`,
		`maku_rooms_driver_changes_total 1`,
		`maku_rooms_redactions_total{rule="jwt"} 1`,
		`maku_rooms_rejected_actions_total{reason="not_permitted"} 1`,
		`maku_rooms_connections_dropped_total{reason="slow"} 1`,
		`maku_rooms_verdict_posts_total{result="posted"} 1`,
		`maku_rooms_human_mcp_calls_total{outcome="ok",tool="room_summary"} 1`,
		`maku_rooms_last_event_timestamp_seconds{room="3kq7x2ma"} 1.69999994e+09`,
		`maku_rooms_authn_jwks_last_refresh_timestamp_seconds{issuer="https://issuer"} 1.7e+09`,
		`maku_rooms_bridge_harness_stalls_total{reason="cursor_lost"} 1`,
		`maku_rooms_bridge_items_stubbed_total{reason="refused"} 1`,
		`maku_rooms_webhook_deliveries_total{outcome="delivered",subscriber="slack-app"} 1`,
		`maku_rooms_webhook_disabled{subscriber="slack-app"} 1`,
		`maku_rooms_db_tls{role="rooms_broker"} 0`,
		`maku_rooms_schema_unverified 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s", want)
		}
	}
	for _, unwanted := range []string{`room="4kq7x2ma"`, "otel_scope", "target_info", "_total_total", "_seconds_seconds"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("exposes %s", unwanted)
		}
	}
	if t.Failed() {
		t.Log(body)
	}
}

// The fan-out listener's state, read at each scrape: 1 while LISTEN is in place.
func TestFanoutListenerUp(t *testing.T) {
	exp, err := NewExporter(BrokerBuildInfo, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(t.Context()) }()
	s, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	var up atomic.Bool
	if err := s.WatchFanout(up.Load); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"maku_rooms_fanout_listener_up 0\n", "maku_rooms_fanout_listener_up 1\n"} {
		if body := scrape(t, exp.Handler()); !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
		up.Store(true)
	}
}

func TestRoomGauges(t *testing.T) {
	ctx := t.Context()
	now := time.Unix(1_700_000_000, 0)
	for _, c := range []struct {
		name    string
		observe func(s *Set)
		want    []string
		absent  []string
	}{
		{"a room counts once, in its latest phase", func(s *Set) {
			s.ObserveRoom("3kq7x2ma", "Open", false, 0, now)
			s.ObserveRoom("3kq7x2ma", "Active", true, 0, now)
		}, []string{`rooms{phase="Active"} 1`}, []string{`rooms{phase="Open"}`}},
		{"a room without a Running run has no activity series", func(s *Set) {
			s.ObserveRoom("3kq7x2ma", "Active", true, 0, now)
			s.ObserveRoom("3kq7x2ma", "Idle", false, 0, now)
		}, []string{`rooms{phase="Idle"} 1`}, []string{"maku_rooms_last_event_timestamp_seconds{"}},
		// S1 review I-3: 30 silent minutes flip a running room to AwaitingHuman,
		// which is exactly when RoomStalled must still see its series.
		{"a stalled room keeps its activity series through the flip to AwaitingHuman", func(s *Set) {
			s.ObserveRoom("3kq7x2ma", "Active", true, 0, now.Add(-31*time.Minute))
			s.ObserveRoom("3kq7x2ma", "AwaitingHuman", true, 0, now.Add(-31*time.Minute))
		}, []string{`rooms{phase="AwaitingHuman"} 1`, `maku_rooms_last_event_timestamp_seconds{room="3kq7x2ma"} 1.69999814e+09`}, nil},
		{"pending approvals sum over rooms", func(s *Set) {
			s.ObserveRoom("3kq7x2ma", "AwaitingHuman", false, 2, now)
			s.ObserveRoom("4kq7x2ma", "AwaitingHuman", false, 3, now)
		}, []string{`maku_rooms_approvals_pending 5`, `rooms{phase="AwaitingHuman"} 2`}, []string{"maku_rooms_last_event_timestamp_seconds{"}},
		{"a room keeps its last values while it is not reconciled (a database outage)", func(s *Set) {
			s.ObserveRoom("3kq7x2ma", "Active", true, 1, now)
		}, []string{`rooms{phase="Active"} 1`, `maku_rooms_last_event_timestamp_seconds{room="3kq7x2ma"}`, `maku_rooms_approvals_pending 1`}, nil},
		{"a forgotten room (its CR is gone) drops out", func(s *Set) {
			s.ObserveRoom("3kq7x2ma", "Active", true, 1, now)
			s.ObserveRoom("4kq7x2ma", "Idle", false, 0, now)
			s.ForgetRoom("3kq7x2ma")
		}, []string{`maku_rooms_approvals_pending 0`, `rooms{phase="Idle"} 1`}, []string{`rooms{phase="Active"}`, `room="3kq7x2ma"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			exp, err := NewExporter(BrokerBuildInfo, "test")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = exp.Shutdown(ctx) }()
			s, err := New(exp.Meter())
			if err != nil {
				t.Fatal(err)
			}
			c.observe(s)
			body := scrape(t, exp.Handler())
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %s", w)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(body, a) {
					t.Errorf("exposes %s", a)
				}
			}
			if t.Failed() {
				t.Log(body)
			}
		})
	}
}

func TestEachBinaryHasItsOwnBuildInfo(t *testing.T) {
	exp, err := NewExporter(FactoryBuildInfo, "v0.7.0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(t.Context()) }()
	if body := scrape(t, exp.Handler()); !strings.Contains(body, "maku_factory_build_info{version=\"v0.7.0\"} 1\n") ||
		strings.Contains(body, "maku_rooms_build_info") {
		t.Fatal(body)
	}
	if _, err := NewExporter("build_info", "v0.7.0"); err == nil {
		t.Fatal("a gauge outside both prefixes is refused")
	}
}

func TestNoopMeterWorks(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Appended(t.Context(), "message", "broker", nil)
	s.AppendTook(t.Context(), time.Millisecond)
	s.AppendFailed(t.Context())
	s.ObserveRoom("3kq7x2ma", "Active", true, 0, time.Now())
	s.ForgetRoom("3kq7x2ma")
}

// maku_rooms_run_info{run, model, effort} = 1 for each live run whose harness reported an effort. The
// model is the claim's spec.model, read from the watch at collection, never the harness's; the
// effort is the harness's, an enum. A per-live-run series, its map capped.
func TestRunInfo(t *testing.T) {
	ctx := t.Context()
	series := func(run, model, effort string) string {
		return `maku_rooms_run_info{effort="` + effort + `",model="` + model + `",run="` + run + `"} 1`
	}
	for _, c := range []struct {
		name    string
		runs    map[string]watchedRun // what the watch says; absent means live with no model
		observe func(s *Set)
		want    []string
		absent  []string
		count   int // series of maku_rooms_run_info, when set
	}{
		{"the claim's model and the harness's effort", map[string]watchedRun{"aaaaaaaa": {"agent-default", true}}, func(s *Set) {
			s.RunInfo(ctx, "aaaaaaaa", "high")
		}, []string{series("aaaaaaaa", "agent-default", "high")}, nil, 1},
		{"the latest effort wins", map[string]watchedRun{"aaaaaaaa": {"agent-default", true}}, func(s *Set) {
			s.RunInfo(ctx, "aaaaaaaa", "high")
			s.RunInfo(ctx, "aaaaaaaa", "low")
		}, []string{series("aaaaaaaa", "agent-default", "low")}, []string{`effort="high"`}, 1},
		{"a claim without a model", nil, func(s *Set) {
			s.RunInfo(ctx, "aaaaaaaa", "max")
		}, []string{series("aaaaaaaa", "", "max")}, nil, 1},
		{"a run that is no longer live drops out", map[string]watchedRun{"aaaaaaaa": {"agent-default", false}}, func(s *Set) {
			s.RunInfo(ctx, "aaaaaaaa", "high")
			s.RunInfo(ctx, "bbbbbbbb", "low")
		}, []string{series("bbbbbbbb", "", "low")}, []string{`run="aaaaaaaa"`}, 1},
		{"at the cap, dead runs make room", deadRuns(maxRunInfo), func(s *Set) {
			for i := range maxRunInfo {
				s.RunInfo(ctx, fmt.Sprintf("r%07d", i), "high")
			}
			s.RunInfo(ctx, "newrun00", "low") // every held run has ended since: pruned, not dropped
		}, []string{series("newrun00", "", "low")}, []string{"maku_rooms_run_info_dropped_total "}, 1},
		{"at the cap with every run live, a new one is dropped and counted", nil, func(s *Set) {
			for i := range maxRunInfo + 5 {
				s.RunInfo(ctx, fmt.Sprintf("r%07d", i), "high")
			}
			s.RunInfo(ctx, "r0000000", "low") // a known run still updates
		}, []string{series("r0000000", "", "low"), "maku_rooms_run_info_dropped_total 5"},
			[]string{`run="r` + fmt.Sprintf("%07d", maxRunInfo) + `"`}, maxRunInfo},
	} {
		t.Run(c.name, func(t *testing.T) {
			exp, err := NewExporter(BrokerBuildInfo, "test")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = exp.Shutdown(ctx) }()
			s, err := New(exp.Meter())
			if err != nil {
				t.Fatal(err)
			}
			s.TrackRuns(func(run string) (string, bool) {
				w, ok := c.runs[run]
				if !ok {
					return "", true
				}
				return w.model, w.live
			})
			c.observe(s)
			body := scrape(t, exp.Handler())
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %s", w)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(body, a) {
					t.Errorf("exposes %s", a)
				}
			}
			if n := strings.Count(body, "\nmaku_rooms_run_info{"); c.count > 0 && n != c.count {
				t.Errorf("%d maku_rooms_run_info series, want %d", n, c.count)
			}
			if t.Failed() && len(body) < 4096 {
				t.Log(body)
			}
		})
	}
}

// watchedRun is what a test's watch says of a run.
type watchedRun struct {
	model string
	live  bool
}

// deadRuns is a watch in which the first n runs of TestRunInfo's naming have ended.
func deadRuns(n int) map[string]watchedRun {
	out := make(map[string]watchedRun, n)
	for i := range n {
		out[fmt.Sprintf("r%07d", i)] = watchedRun{}
	}
	return out
}

// series maps each sample line of a scrape, "name{labels}", to its value.
func series(body string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(body, "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		i := strings.LastIndexByte(l, ' ')
		out[l[:i]] = l[i+1:]
	}
	return out
}

func TestMakuName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"rooms", "maku_rooms"},
		{"rooms_events_appended_total", "maku_rooms_events_appended_total"},
		{"rooms_build_info", "maku_rooms_build_info"},
		{"agent_factory_tasks", "maku_factory_tasks"},
		{"agent_factory_build_info", "maku_factory_build_info"},
		{"factory_slack_build_info", ""},
		{"roomsx", ""},
		{"go_goroutines", ""},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := MakuName(tc.in); got != tc.want {
				t.Fatalf("MakuName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The spec's series are maku_needs_*, which MakuName can only produce from a needs_* instrument:
// rooms_needs_list_total would be served as maku_rooms_needs_list_total. This is the test that makes
// the dashboard's queries true rather than plausible.
func TestMakuNameCoversNeeds(t *testing.T) {
	for in, want := range map[string]string{
		"needs_list_total": "maku_needs_list_total",
		"needs_items":      "maku_needs_items",
		"needs":            "",
		"needsx":           "",
	} {
		if got := MakuName(in); got != want {
			t.Fatalf("MakuName(%q) = %q, want %q", in, got, want)
		}
	}
}

// CD22's counter is maku_revision_deferred_total, the name the design's table and the live proof
// query: MakuName can only produce it from a revision_* instrument, and an agent_factory_ one would
// be served as maku_factory_revision_deferred_total.
func TestMakuNameCoversRevisionDeferred(t *testing.T) {
	for in, want := range map[string]string{
		"revision_deferred_total": "maku_revision_deferred_total",
		"revision":                "",
		"revisions_total":         "",
	} {
		if got := MakuName(in); got != want {
			t.Fatalf("MakuName(%q) = %q, want %q", in, got, want)
		}
	}
}

// needs/v1's adoption is measured where the list is served: one count per call, labelled by surface,
// principal and outcome, and what the last list held by kind. Both keep the spec's names, so the
// dashboard's maku_needs_* queries match; a list that holds no item of a kind drops that kind.
func TestNeedsInstruments(t *testing.T) {
	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	s, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	s.NeedsList(ctx, "api", "human:dev", "ok")
	s.NeedsList(ctx, "api", "human:dev", "unread")
	s.Needs(map[string]int{"plan_confirmation": 2, "merge": 1})
	for _, want := range []string{
		`maku_needs_list_total{outcome="ok",principal="human:dev",surface="api"} 1`,
		`maku_needs_list_total{outcome="unread",principal="human:dev",surface="api"} 1`,
		`maku_needs_items{kind="merge"} 1`,
		`maku_needs_items{kind="plan_confirmation"} 2`,
	} {
		if body := scrape(t, exp.Handler()); !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s", want)
		}
	}
	// The copy replaces the last list wholesale: a kind the list no longer holds has no series.
	s.Needs(nil)
	if body := scrape(t, exp.Handler()); strings.Contains(body, "maku_needs_items{") {
		t.Errorf("a list with no items keeps %s", body)
	}
}

// v0.31.0 serves the maku_ names only (the rebrand): every rooms series as maku_rooms*, none under its
// old name, the build info included.
func TestOnlyMakuNamesAreServed(t *testing.T) {
	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "v0.31.0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	s, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	s.Appended(ctx, "message", "harness", []string{"jwt"})
	s.AppendTook(ctx, 3*time.Millisecond)
	s.ObserveRoom("3kq7x2ma", "Active", true, 2, time.Unix(1_700_000_000, 0))
	s.BridgeStalled(ctx, "cursor_lost")
	got := series(scrape(t, exp.Handler()))
	n := 0
	for k := range got {
		switch {
		case strings.HasPrefix(k, "rooms_"), strings.HasPrefix(k, "rooms{"):
			t.Errorf("%s served under its old name", k)
		case strings.HasPrefix(k, "maku_rooms"):
			n++
		}
	}
	if n < 6 {
		t.Fatalf("scraped %d maku_rooms series, want at least 6", n)
	}
	if v := got[`maku_rooms_build_info{version="v0.31.0"}`]; v != "1" {
		t.Errorf("maku_rooms_build_info = %q, want 1", v)
	}
}

// A mapped family is served under its maku_ name only; a real family of that name wins over the
// renamed one, so a scrape never holds two families of one name; an unmapped family keeps its own.
func TestMakuGathererRenames(t *testing.T) {
	fam := func(name string, v float64) *dto.MetricFamily {
		typ := dto.MetricType_GAUGE
		return &dto.MetricFamily{Name: &name, Type: &typ, Metric: []*dto.Metric{{Gauge: &dto.Gauge{Value: &v}}}}
	}
	g := makuGatherer{reg: prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		return []*dto.MetricFamily{fam("rooms_x", 1), fam("maku_rooms_x", 2), fam("rooms_y", 3), fam("factory_slack_z", 4)}, nil
	})}
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]float64{}
	for _, mf := range mfs {
		got[mf.GetName()] = append(got[mf.GetName()], mf.GetMetric()[0].GetGauge().GetValue())
	}
	want := map[string][]float64{"maku_rooms_x": {2}, "maku_rooms_y": {3}, "factory_slack_z": {4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("families %v, want %v", got, want)
	}
}

// The planner's series are the spec's maku_intake_* ones, and MakuName can only produce those from
// intake_* instruments: a rooms_intake_plan_total would be served as maku_rooms_intake_plan_total,
// which the dashboard would query in vain.
func TestIntakeSeriesAreTheSpecsNames(t *testing.T) {
	for in, want := range map[string]string{
		"intake_plan_total":   "maku_intake_plan_total",
		"intake_plan_seconds": "maku_intake_plan_seconds",
		"intake_plan_tokens":  "maku_intake_plan_tokens",
		"intake":              "",
		"rooms_intake_plan":   "maku_rooms_intake_plan",
	} {
		if got := MakuName(in); got != want {
			t.Errorf("MakuName(%q) = %q, want %q", in, got, want)
		}
	}

	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "v0.34.0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	s, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	s.IntakePlan(ctx, "session", "planned", "")
	s.IntakePlan(ctx, "slack", "refused", "repository_not_allowed")
	s.IntakePlanSeconds(ctx, "session", "planned", 250*time.Millisecond)
	s.IntakePlanTokens(ctx, "session", "prompt", 1200)
	s.IntakePlanTokens(ctx, "session", "completion", 0) // nothing spent is no series
	got := series(scrape(t, exp.Handler()))
	for key, want := range map[string]string{
		`maku_intake_plan_total{outcome="planned",surface="session"}`:                               "1",
		`maku_intake_plan_total{outcome="refused",reason="repository_not_allowed",surface="slack"}`: "1",
		`maku_intake_plan_seconds_sum{outcome="planned",surface="session"}`:                         "0.25",
		`maku_intake_plan_tokens{kind="prompt",surface="session"}`:                                  "1200",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q: %v", key, got[key], want, got)
		}
	}
	if _, ok := got[`maku_intake_plan_tokens{kind="completion",surface="session"}`]; ok {
		t.Error("a zero token count made a series")
	}
}

// The catalogue's reads are maku_intake_catalogue_reads_total{outcome}, a closed vocabulary.
func TestIntakeCatalogueReadsMetric(t *testing.T) {
	if got := MakuName("intake_catalogue_reads_total"); got != "maku_intake_catalogue_reads_total" {
		t.Fatalf("MakuName = %q", got)
	}
	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "v0.34.0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	s, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	s.IntakeCatalogueRead(ctx, CatalogueHit)
	s.IntakeCatalogueRead(ctx, CatalogueHit)
	s.IntakeCatalogueRead(ctx, CatalogueMiss)
	s.IntakeCatalogueRead(ctx, CatalogueError)
	s.IntakeCatalogueRead(ctx, "Smana/cloud-native-ref") // a repository never labels a series
	got := series(scrape(t, exp.Handler()))
	for key, want := range map[string]string{
		`maku_intake_catalogue_reads_total{outcome="hit"}`:   "2",
		`maku_intake_catalogue_reads_total{outcome="miss"}`:  "1",
		`maku_intake_catalogue_reads_total{outcome="error"}`: "2",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q: %v", key, got[key], want, got)
		}
	}
}

// Pins at intake: maku_intake_profile_refusals_total{surface,reason} and
// maku_intake_profile_choices_total{surface,class,choice}, the choice a closed vocabulary.
func TestIntakeProfileMetrics(t *testing.T) {
	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "v0.34.0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	s, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	s.IntakeProfileRefusal(ctx, "session", "class_profile_fixed")
	s.IntakeProfileChoice(ctx, "session", "review", ChoiceFactory)
	s.IntakeProfileChoice(ctx, "slack", "review", ChoiceSuggested)
	s.IntakeProfileChoice(ctx, "slack", "review", "review-frontier") // a profile never labels a series
	got := series(scrape(t, exp.Handler()))
	for key, want := range map[string]string{
		`maku_intake_profile_refusals_total{reason="class_profile_fixed",surface="session"}`:   "1",
		`maku_intake_profile_choices_total{choice="factory",class="review",surface="session"}`: "1",
		`maku_intake_profile_choices_total{choice="suggested",class="review",surface="slack"}`: "1",
		`maku_intake_profile_choices_total{choice="other",class="review",surface="slack"}`:     "1",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q: %v", key, got[key], want, got)
		}
	}
}

// CD7 and CD25's evidence: the App's two families are served under maku_slack_*, so the names the
// dashboard and the acceptance queries hold are the ones a scrape carries — the plural
// maku_slack_thread_answers_total, since the label is a set of questions, and the proposal counter's
// six actions. A factory_slack_* family keeps its own name: MakuName must not catch it.
func TestProposalCounterName(t *testing.T) {
	for in, want := range map[string]string{
		"slack_proposal_updates_total": "maku_slack_proposal_updates_total",
		"slack_thread_answers_total":   "maku_slack_thread_answers_total",
		"factory_slack_requests_total": "",
		"factory_slack_build_info":     "",
	} {
		if got := MakuName(in); got != want {
			t.Errorf("MakuName(%q) = %q, want %q", in, got, want)
		}
	}

	ctx := t.Context()
	exp, err := NewExporter(SlackBuildInfo, "v0.35.0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	// The App's own meter, as internal/app's slackRecorder builds it: NewSlackProposals, not a Set.
	p, err := NewSlackProposals(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	actions := []string{"mention", "reply", "confirm", "cancel", "repost", "unrewritten"}
	for _, action := range actions {
		p.ProposalUpdate(ctx, action)
	}
	questions := []string{"status", "needs", "changes", "pr", "stop", "unknown"}
	for _, q := range questions {
		p.ThreadAnswer(ctx, q)
	}
	got := series(scrape(t, exp.Handler()))
	for _, action := range actions {
		served := `maku_slack_proposal_updates_total{action="` + action + `"}`
		if got[served] != "1" {
			t.Errorf("%s = %q, want 1: %v", served, got[served], got)
		}
		if _, ok := got[strings.TrimPrefix(served, "maku_")]; ok {
			t.Errorf("%s is also served under its instrument name", served)
		}
	}
	for _, q := range questions {
		served := `maku_slack_thread_answers_total{question="` + q + `"}`
		if got[served] != "1" {
			t.Errorf("%s = %q, want 1: %v", served, got[served], got)
		}
	}
	// The design once spelled the answer counter singular; the served name is the plural one, and a
	// query for the singular would read nothing.
	if _, ok := got[`maku_slack_thread_answer_total{question="status"}`]; ok {
		t.Error("the answer counter is served singular")
	} else if _, ok := got[`maku_slack_thread_answers_total{question="status"}`]; !ok {
		t.Errorf("the answer counter is not served: %v", got)
	}
	// The App's scrape carries the App's series alone: its meter never holds the broker's set, which
	// would serve maku_rooms_approvals_pending 0 with no room reconciled at all.
	for k := range got {
		if strings.HasPrefix(k, "maku_rooms") || strings.HasPrefix(k, "maku_intake") || strings.HasPrefix(k, "rooms") {
			t.Errorf("%s is served from an instrument this binary never recorded", k)
		}
	}
	// The broker's Set carries the same two families, so the name and the label vocabulary have one
	// spelling whoever records on them.
	set, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	set.ProposalUpdate(ctx, "mention")
	set.ThreadAnswer(ctx, "status")
}

// Natural-language intents: the classifier's two series are served as maku_intent_classify_*, and the
// App's reading counter as maku_slack_intents_total, each with its closed labels only.
func TestIntentInstruments(t *testing.T) {
	for in, want := range map[string]string{
		"intent_classify_total":            "maku_intent_classify_total",
		"intent_classify_duration_seconds": "maku_intent_classify_duration_seconds",
		"slack_intents_total":              "maku_slack_intents_total",
	} {
		if got := MakuName(in); got != want {
			t.Errorf("MakuName(%q) = %q, want %q", in, got, want)
		}
	}
	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	set, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	set.IntentClassify(ctx, "ok")
	set.IntentClassify(ctx, "capped")
	set.IntentClassifySeconds(ctx, 250*time.Millisecond)
	p, err := NewSlackProposals(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	p.SlackIntent(ctx, "1", "stop", "shadowed")
	got := series(scrape(t, exp.Handler()))
	for key, want := range map[string]string{
		`maku_intent_classify_total{outcome="ok"}`:                            "1",
		`maku_intent_classify_total{outcome="capped"}`:                        "1",
		`maku_intent_classify_duration_seconds_count`:                         "1",
		`maku_slack_intents_total{intent="stop",outcome="shadowed",tier="1"}`: "1",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q: %v", key, got[key], want, got)
		}
	}
}

// v4 coordination: what a parent's views and checkout documents became, by a closed outcome only;
// no repository, viewer, family or plan text is a label.
func TestCoordinationInstruments(t *testing.T) {
	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	s, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	s.CoordinationView(ctx, "restricted")
	s.CoordinationView(ctx, "repository:acme/secret") // not a closed outcome: counted as other
	s.CoordinationCheckout(ctx, "served")
	body := scrape(t, exp.Handler())
	for _, want := range []string{
		`maku_rooms_coordination_views_total{outcome="restricted"} 1`,
		`maku_rooms_coordination_views_total{outcome="other"} 1`,
		`maku_rooms_coordination_checkout_manifests_total{outcome="served"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, "acme/secret") {
		t.Fatal("an unbounded value reached a label")
	}
}

// EX11: a knowledge call is counted by tool and outcome, both closed sets.
func TestKnowledgeCallsCounter(t *testing.T) {
	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	s, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	s.KnowledgeCall(ctx, "kb_search", "ok")
	s.KnowledgeCall(ctx, "kb_get", "timeout")
	s.KnowledgeCall(ctx, "kb_get", "invalid_arguments")
	s.KnowledgeCall(ctx, "kb_delete", "no entry at incidents/secret.md")
	body := scrape(t, exp.Handler())
	for _, want := range []string{
		`maku_rooms_knowledge_calls_total{outcome="ok",tool="kb_search"} 1`,
		`maku_rooms_knowledge_calls_total{outcome="timeout",tool="kb_get"} 1`,
		`maku_rooms_knowledge_calls_total{outcome="invalid_arguments",tool="kb_get"} 1`,
		`maku_rooms_knowledge_calls_total{outcome="other",tool="other"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "kb_delete") {
		t.Fatal("an unbounded value reached a label")
	}
}

// v4: a coordination delivery is counted by what its channel was told, a closed outcome only.
func TestSlackCoordinationProjectionCounter(t *testing.T) {
	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	p, err := NewSlackProposals(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	p.CoordinationProjection(ctx, "coarse")
	p.CoordinationProjection(ctx, "C03/acme-secret")
	body := scrape(t, exp.Handler())
	for _, want := range []string{`maku_slack_coordination_projection_total{outcome="coarse"} 1`, `maku_slack_coordination_projection_total{outcome="other"} 1`} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, "acme-secret") {
		t.Fatal("an unbounded value reached a label")
	}
}

// Stage 6: a promotion record counts by repository, class and a closed change; ids never reach a label.
func TestPolicyChangeIsBounded(t *testing.T) {
	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	s, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	s.PolicyChange(ctx, "Smana/cloud-native-ref", "docs-links", "promotion_requested")
	s.PolicyChange(ctx, "Smana/cloud-native-ref", "docs-links", "promotion:aaaaaaaa")
	body := scrape(t, exp.Handler())
	if !strings.Contains(body, `maku_rooms_policy_changes_total{change="promotion_requested",class="docs-links",repository="Smana/cloud-native-ref"} 1`+"\n") ||
		strings.Contains(body, "aaaaaaaa") {
		t.Fatalf("%s", body)
	}
}

// v4 D8: a roles claim that gave no group is counted by its closed reason only.
func TestHumanRolesClaimRefused(t *testing.T) {
	ctx := t.Context()
	exp, err := NewExporter(BrokerBuildInfo, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()
	s, err := New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	s.HumanRolesClaimRefused(ctx, "missing")
	s.HumanRolesClaimRefused(ctx, "missing")
	s.HumanRolesClaimRefused(ctx, "bounds")
	got := series(scrape(t, exp.Handler()))
	for key, want := range map[string]string{
		`maku_rooms_human_roles_claim_refused_total{reason="missing"}`: "2",
		`maku_rooms_human_roles_claim_refused_total{reason="bounds"}`:  "1",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
}
