// SPDX-License-Identifier: Apache-2.0

package hardeningreport

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/hardening"
)

type readiness struct {
	reports map[string]hardening.Report
	err     error
	calls   int
}

func (r *readiness) Hardened(_ context.Context, repo string) (hardening.Report, error) {
	r.calls++
	return r.reports[repo], r.err
}

type recorder struct{ got []string }

func (m *recorder) HardeningReady(_ context.Context, repo string, ready bool, missing []string) {
	m.got = append(m.got, repo+" "+map[bool]string{true: "ready", false: "not"}[ready]+" "+strings.Join(missing, ","))
}

// HR41: every report computes readiness afresh for each repository, sets the gauge, and logs a
// repository only when its answer changed; an evaluation error reports not ready, missing error.
func TestReadinessReport(t *testing.T) {
	rd := &readiness{reports: map[string]hardening.Report{
		"o/a": {Hardened: true, Drill: "drill-1"},
		"o/b": {Missing: []hardening.Missing{hardening.MissingEnforce, hardening.MissingDrill}},
	}}
	m := &recorder{}
	var logs bytes.Buffer
	r := &Reporter{Readiness: rd, Repositories: func() []string { return []string{"o/a", "o/b"} }, Metrics: m,
		Log: slog.New(slog.NewTextHandler(&logs, nil))}
	r.Report(t.Context())
	r.Report(t.Context())
	if rd.calls != 4 {
		t.Fatalf("%d evaluations, want one per repository per report", rd.calls)
	}
	want := []string{"o/a ready ", "o/b not mode_not_enforce,drill_stale_or_failed", "o/a ready ", "o/b not mode_not_enforce,drill_stale_or_failed"}
	if strings.Join(m.got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", m.got)
	}
	if n := strings.Count(logs.String(), "hardening readiness changed"); n != 2 {
		t.Fatalf("%d change lines, want 2:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "drill=drill-1") {
		t.Fatalf("%s", logs.String())
	}
	rd.err = errors.New("read publisher policy: 502")
	r.Report(t.Context())
	if got := m.got[len(m.got)-1]; got != "o/b not error" || m.got[len(m.got)-2] != "o/a not error" {
		t.Fatalf("%q", m.got)
	}
	if n := strings.Count(logs.String(), "hardening readiness changed"); n != 4 {
		t.Fatalf("%d change lines after the error", n)
	}
	if !(&Reporter{}).NeedLeaderElection() {
		t.Fatal("every replica would report")
	}
}
