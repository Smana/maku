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
