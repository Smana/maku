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
		// v4 step 8: unset, the defaults are maku/merge-gate without policy-bot.
		"the defaults": {liveDeadLinks, "", ""},
		"no gate status": {liveDeadLinks, "      policyBot: false\n      gateStatus: false\n",
			"merge.policyBot: false needs merge.gateStatus: true: without policy-bot, maku/merge-gate is the status branch protection requires"},
		// MR6: docs-links's built-in shape bounds it; docs has neither paths nor a shape.
		"a live class without allowedFiles": {"docs-links: {live: true, holdoutChecks: [x]}\n  revert: {shadow: true}\n  docs: {live: true}", "      policyBot: false\n      gateStatus: true\n",
			"class docs: an evaluated policy needs allowedFiles or a diffShape"},
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
			if m := cfg.Repos()[0].Merge; m.UsesPolicyBot() || !m.PostsGateStatus() {
				t.Fatalf("merge %+v: want policyBot off and gateStatus on", m)
			}
		})
	}
}
