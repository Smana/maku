// SPDX-License-Identifier: Apache-2.0

package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func drillReport(id string, at time.Time, pass bool) DrillReport {
	return DrillReport{ID: id, Version: "v0.45.0", ConfigHash: strings.Repeat("c", 64), At: at, Pass: pass,
		Report: json.RawMessage(`{"pass":` + map[bool]string{true: "true", false: "false"}[pass] + `}`)}
}

// HR38: a drill report is stored once per id, and LastPass answers the newest passing report of
// one version and configuration hash; the broker's role can neither rewrite nor delete one.
func TestStoreDrillReport(t *testing.T) {
	ctx := t.Context()
	s, broker, _, super := open(t)
	day := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	for _, r := range []DrillReport{
		drillReport("drill-1", day, true),
		drillReport("drill-2", day.Add(48*time.Hour), true),
		drillReport("drill-3", day.Add(72*time.Hour), false), // newer, but failed
	} {
		if err := s.PutDrillReport(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutDrillReport(ctx, drillReport("drill-1", day, false)); !errors.Is(err, ErrDrillExists) {
		t.Fatalf("a second report under one id: %v", err)
	}
	if got := query(t, super, `SELECT count(*)::text FROM drill_reports`); got != "3" {
		t.Fatalf("%s rows, want 3", got)
	}
	id, at, ok, err := s.LastPass(ctx, "v0.45.0", strings.Repeat("c", 64))
	if err != nil || !ok || id != "drill-2" || !at.Equal(day.Add(48*time.Hour)) {
		t.Fatalf("last pass %q %v %v %v", id, at, ok, err)
	}
	for name, key := range map[string][2]string{
		"another version": {"v0.46.0", strings.Repeat("c", 64)},
		"another hash":    {"v0.45.0", strings.Repeat("d", 64)},
	} {
		if _, _, ok, err := s.LastPass(ctx, key[0], key[1]); err != nil || ok {
			t.Errorf("%s: found %v, %v", name, ok, err)
		}
	}
	for name, r := range map[string]DrillReport{
		"a bad id":           drillReport("Drill 1", day, true),
		"a bad hash":         {ID: "drill-9", Version: "v1", ConfigHash: "x", At: day, Report: json.RawMessage(`{}`)},
		"a bad version":      {ID: "drill-9", Version: "v 1", ConfigHash: strings.Repeat("c", 64), At: day, Report: json.RawMessage(`{}`)},
		"no time":            {ID: "drill-9", Version: "v1", ConfigHash: strings.Repeat("c", 64), Report: json.RawMessage(`{}`)},
		"a report not JSON":  {ID: "drill-9", Version: "v1", ConfigHash: strings.Repeat("c", 64), At: day, Report: json.RawMessage(`{`)},
		"a report over 256K": {ID: "drill-9", Version: "v1", ConfigHash: strings.Repeat("c", 64), At: day, Report: json.RawMessage(`"` + strings.Repeat("a", 256<<10) + `"`)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := s.PutDrillReport(ctx, r); !errors.Is(err, ErrBadDrillReport) {
				t.Fatalf("err = %v, want ErrBadDrillReport", err)
			}
		})
	}
	for name, sql := range map[string]string{
		"an UPDATE": `UPDATE drill_reports SET pass = true`,
		"a DELETE":  `DELETE FROM drill_reports`,
	} {
		if err := run(ctx, broker, sql); err == nil {
			t.Errorf("the broker's role ran %s", name)
		}
	}
}
