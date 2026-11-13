// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// D11: outside the demo a repository may gate on maku/merge-gate without policy-bot, but only
// when the factory posts that status; the allowedFiles rule for live classes stays.
func TestPolicyBotOffInProduction(t *testing.T) {
	const login = "      policyBotLogin: ogenki-merge-gate[bot]\n"
	liveDeadLinks := "docs-links: {shadow: true}\n  revert: {shadow: true}\n  " + strings.Replace(deadLinks, "dead-links: {", "dead-links: {live: true, ", 1)
	for name, c := range map[string]struct {
		classes, merge, err string
	}{
		"gate status, allowedFiles, no login": {liveDeadLinks, "      policyBot: false\n      gateStatus: true\n", ""},
		"gate status and a login":             {liveDeadLinks, login + "      policyBot: false\n      gateStatus: true\n", ""},
		"no gate status": {liveDeadLinks, "      policyBot: false\n",
			"merge.policyBot: false needs merge.gateStatus: true: without policy-bot, maku/merge-gate is the status branch protection requires"},
		"a live class without allowedFiles": {"docs-links: {live: true, holdoutChecks: [x]}\n  revert: {shadow: true}", "      policyBot: false\n      gateStatus: true\n",
			"class docs-links: merge.policyBot: false needs allowedFiles on every live class"},
		"a malformed login": {liveDeadLinks, "      policyBotLogin: notabot\n      policyBot: false\n      gateStatus: true\n", "merge.policyBotLogin"},
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(withClasses(t, c.classes), login, c.merge, 1)
			cfg, err := Parse([]byte(raw))
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("got %v, want %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m := cfg.Repos()[0].Merge; m.UsesPolicyBot() || !m.GateStatus {
				t.Fatalf("merge %+v: want policyBot off and gateStatus on", m)
			}
		})
	}
}
