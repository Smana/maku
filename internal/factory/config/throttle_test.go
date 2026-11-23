// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// GW10: the gateway's 429s come from a PromQL query on meter.url (meter.throttle.promQuery), or from
// VictoriaLogs (meter.logsURL with meter.throttleQuery). Outside the demo one is required, and both
// set is refused.
func TestMeterThrottleSource(t *testing.T) {
	const (
		logsURL  = "  logsURL: http://victoria-logs-victoria-logs-single-server.observability.svc:9428\n"
		promLine = "  throttle: {promQuery: 'sum by (ar_agent) (increase(agentgateway_requests_total{status=\"429\", reason=\"RateLimit\"}[2m])) > 0'}\n"
	)
	_, rest, ok := strings.Cut(good, "  throttleQuery:")
	if !ok {
		t.Fatal("good has no throttleQuery")
	}
	line, _, _ := strings.Cut(rest, "\n")
	throttleQuery := "  throttleQuery:" + line + "\n"
	noLogs := strings.Replace(strings.Replace(good, logsURL, "", 1), throttleQuery, "", 1)
	for name, c := range map[string]struct {
		raw, err string
	}{
		"VictoriaLogs, as before":      {good, ""},
		"PromQL alone":                 {strings.Replace(noLogs, "  url: http://vmsingle", promLine+"  url: http://vmsingle", 1), ""},
		"neither":                      {noLogs, "meter.throttle.promQuery, or meter.logsURL with meter.throttleQuery, is required"},
		"both":                         {strings.Replace(good, logsURL, promLine+logsURL, 1), "meter.throttle.promQuery and meter.logsURL with meter.throttleQuery: choose one"},
		"half the logs pair":           {strings.Replace(good, throttleQuery, "", 1), "meter.throttleQuery is required"},
		"an unknown throttle key":      {strings.Replace(good, logsURL, "  throttle: {logsQL: x}\n"+logsURL, 1), `unknown field "logsQL"`},
		"demo: PromQL with the meter":  {strings.Replace(demoGood, "runs: {group", "meter:\n  url: http://vm.example:8428\n  query: q\n"+promLine+"runs: {group", 1), ""},
		"demo: PromQL without a meter": {strings.Replace(demoGood, "runs: {group", "meter:\n"+promLine+"runs: {group", 1), "meter.url is required"},
	} {
		t.Run(name, func(t *testing.T) {
			if name != "VictoriaLogs, as before" && (c.raw == good || c.raw == demoGood) {
				t.Fatal("the edit changed nothing")
			}
			cfg, err := Parse([]byte(c.raw))
			if c.err == "" {
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(c.raw, "promQuery") && cfg.Meter.Throttle.PromQuery == "" {
					t.Fatal("promQuery not read")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("got %v, want %q", err, c.err)
			}
		})
	}
}

// REL-M12: meter.logsURL must be a valid http:// or https:// URL with a non-empty host and no credentials.
func TestMeterLogsURLValidation(t *testing.T) {
	const orig = "logsURL: http://victoria-logs-victoria-logs-single-server.observability.svc:9428"
	for name, tc := range map[string]struct {
		url  string
		want string
	}{
		"https":       {"https://logs.example:9428", ""},
		"http":        {"http://logs.example:9428", ""},
		"bad scheme":  {"ftp://logs.example:9428", "meter.logsURL"},
		"no scheme":   {"logs.example:9428", "meter.logsURL"},
		"empty host":  {"https:///select", "meter.logsURL"},
		"credentials": {"https://user:pass@logs.example:9428", "meter.logsURL"},
		"malformed":   {"http://[::1", "meter.logsURL"},
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(good, orig, "logsURL: "+tc.url, 1)
			if raw == good {
				t.Fatal("the edit changed nothing")
			}
			_, err := Parse([]byte(raw))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
