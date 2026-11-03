// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
)

// H-4, D10: the meter keeps one baseline per series of meter.query, so the query must keep each
// gateway replica's counter apart. A query that sums them loads, with a warning that names the fix.
func TestAMeterQuerySummingTheReplicasWarns(t *testing.T) {
	warns := func(raw string) bool {
		c, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.Contains(w, "meter.query sums the gateway replicas") })
	}
	if !warns(good) { // sum by (ar_agent): every replica in one series
		t.Fatal("a query summing the replicas loads without a warning")
	}
	for name, by := range map[string]string{"by pod": "sum by (ar_agent, pod)", "by instance": "sum by (ar_agent, instance)"} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(good, "sum by (ar_agent)", by, 1)
			if raw == good {
				t.Fatal("the edit did not apply")
			}
			if warns(raw) {
				t.Fatal("a query keeping the replicas apart warns")
			}
		})
	}
}

// A query keeps the replicas apart only through a by (...) clause naming pod or instance as a
// whole word.
func TestByReplicaReadsTheByClause(t *testing.T) {
	for name, c := range map[string]struct {
		query string
		want  bool
	}{
		"by pod":                          {`sum by (ar_agent, gen_ai_token_type, pod) (x)`, true},
		"by instance":                     {`sum by (ar_agent, instance) (x)`, true},
		"by pod alone":                    {`sum by(pod)(x)`, true},
		"summed":                          {`sum by (ar_agent) (x)`, false},
		"without pod":                     {`sum without (pod) (x)`, false},
		"a namespace name":                {`sum by (ar_agent) (x{namespace="agent-pods"})`, false},
		"a pod matcher":                   {`sum by (ar_agent) (x{pod="x"})`, false},
		"a nested clause":                 {`sum by (a, b) (max by (a, b, pod) (x))`, false},
		"a trailing by over a nested one": {`sum(max by (a, pod) (x)) by (a)`, false},
		"a by inside a matcher":           {`sum(x{l="by (pod)"}) by (a)`, false},
		"a trailing by pod":               {`sum(x) by (a, pod)`, true},
		"a longer label name":             {`sum by (ar_agent, podname) (x)`, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := (Meter{Query: c.query}).ByReplica(); got != c.want {
				t.Fatalf("ByReplica(%q) = %v, want %v", c.query, got, c.want)
			}
		})
	}
}
