// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// IN5: results sorted by seam (in the spec's seam order), then by check id, then by component.
func TestReportSorts(t *testing.T) {
	var r Report
	for _, res := range []Result{
		{Check: "secrets", Outcome: Pass},
		{Check: "oidc", Component: "factory", Outcome: Pass},
		{Check: "kubernetes", Outcome: Pass},
		{Check: "oidc", Component: "broker", Outcome: Pass},
		{Check: "crds", Outcome: Pass},
	} {
		if err := r.Add(res); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, res := range r.Results() {
		got = append(got, res.Check+"/"+res.Component)
	}
	if want := "crds/ kubernetes/ secrets/ oidc/broker oidc/factory"; strings.Join(got, " ") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestExit(t *testing.T) {
	for name, c := range map[string]struct {
		outcomes      []Outcome
		plain, strict int
	}{
		"all pass":        {[]Outcome{Pass, Skip}, 0, 0},
		"a warn":          {[]Outcome{Pass, Warn}, 0, 1},
		"a fail":          {[]Outcome{Pass, Fail}, 1, 1},
		"nothing checked": {nil, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			var r Report
			for _, o := range c.outcomes {
				res := Result{Check: "runtimeclass", Outcome: o}
				switch o {
				case Warn:
					res.Code = "unsafe_allow_runc"
				case Fail:
					res.Code = "runtimeclass_missing"
				}
				if err := r.Add(res); err != nil {
					t.Fatal(err)
				}
			}
			if got := r.Exit(false); got != c.plain {
				t.Errorf("exit %d, want %d", got, c.plain)
			}
			if got := r.Exit(true); got != c.strict {
				t.Errorf("--strict exit %d, want %d", got, c.strict)
			}
		})
	}
}

// No credential in a report: a message that carries one is refused, and never stored.
func TestAddRefusesACredential(t *testing.T) {
	for _, msg := range []string{"dial postgres://rooms_broker:pw@db:5432/rooms", "-----BEGIN RSA PRIVATE KEY-----",
		"token ghs_abc123", "token ghp_abc123", "Authorization: Bearer abc", "postgresql://u:p@h/db"} {
		var r Report
		err := r.Add(Result{Check: "database", Component: "broker", Outcome: Fail, Code: "db_unreachable", Message: msg})
		if !errors.Is(err, ErrCredential) || len(r.Results()) != 0 {
			t.Errorf("%q: %v, %d results", msg, err, len(r.Results()))
		}
		if err != nil && strings.Contains(err.Error(), msg) {
			t.Errorf("the refusal echoes the message: %v", err)
		}
	}
	var r Report
	if err := r.Add(Result{Check: "secrets", Outcome: Fail, Code: "secret_key_missing", Message: "Secret rooms-broker-dsn has no key uri"}); err != nil {
		t.Fatal(err)
	}
}

// The JSON lines carry IN5's six fields, one result per line.
func TestJSONLines(t *testing.T) {
	var r Report
	_ = r.Add(Result{Check: "kubernetes", Outcome: Fail, Code: "kubernetes_too_old", Message: "v1.29 is below 1.30", Seconds: 0.25})
	var b bytes.Buffer
	if err := r.WriteJSONLines(&b); err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	if err := json.Unmarshal(b.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"check", "component", "outcome", "code", "message", "seconds"} {
		if _, ok := line[k]; !ok {
			t.Errorf("no %q in %s", k, b.String())
		}
	}
}

// The termination message fits Kubernetes' 4096 bytes even with every check failing at length,
// and the text summary groups by seam.
func TestSummaryFits(t *testing.T) {
	var r Report
	for _, c := range Catalog {
		for _, comp := range c.RunsIn {
			_ = r.Add(Result{Check: c.ID, Component: string(comp), Outcome: Fail, Code: c.Fail[0], Message: strings.Repeat("x", 500)})
		}
	}
	s := r.Summary()
	if len(s) > 4096 || !strings.Contains(s, "fail") {
		t.Fatalf("%d bytes: %s", len(s), s)
	}
	var b bytes.Buffer
	if err := r.WriteText(&b); err != nil {
		t.Fatal(err)
	}
	if i, j := strings.Index(b.String(), "cluster"), strings.Index(b.String(), "postgres"); i < 0 || j < 0 || i > j {
		t.Fatalf("the text is not grouped in seam order:\n%s", b.String())
	}
}
