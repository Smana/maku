// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// S-7: every template triage can choose is defined, and investigate is a lone triager wherever
// its tasks can arrive: a RunLore finding, or an internal principal's api task, reads internal
// data, so nothing of it may write (R38). Each refusal names its key.
func TestTriageTemplatesAreDefined(t *testing.T) {
	const lore = "runlore:\n  listen: \":8080\"\n  tokenFile: /etc/agent-factory-intake/token\n  minConfidence: 0.75\n  dailyCap: 5\n"
	const investigate = "  investigate: {roles: [triager]}  # R38: proposes a public issue text, never writes\n"
	writes := func(raw string) string {
		return strings.Replace(raw, "investigate: {roles: [triager]}", "investigate: {roles: [implementer]}", 1)
	}
	internal := strings.Replace(withSponsored(runloreBlock), lore, "", 1) // an internal principal, no runlore block
	for name, c := range map[string]struct{ raw, want string }{
		"no solo": {strings.Replace(good, "  solo: {roles: [implementer]}\n", "", 1), "templates.solo is missing"},
		"no pair": {strings.Replace(good, "  pair: {roles: [implementer, reviewer], maxReviewRounds: 2}\n", "", 1), "templates.pair is missing"},
		"no trio": {strings.Replace(good, "  trio: {roles: [implementer, tester, reviewer], maxReviewRounds: 2}\n", "", 1), "templates.trio is missing"},
		"no investigate beside runlore": {strings.Replace(good, investigate, "", 1),
			"templates.investigate.roles must be [triager] while runlore or api.internalPrincipals is set"},
		"an implementer investigating for runlore": {writes(good),
			"templates.investigate.roles must be [triager] while runlore or api.internalPrincipals is set"},
		"an implementer investigating for an internal principal": {writes(internal),
			"templates.investigate.roles must be [triager] while runlore or api.internalPrincipals is set"},
	} {
		t.Run(name, func(t *testing.T) {
			if c.raw == good || c.raw == internal {
				t.Fatal("the edit did not apply")
			}
			if _, err := Parse([]byte(c.raw)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want it to name %q", err, c.want)
			}
		})
	}
	// Neither runlore nor an internal principal: no investigate task can arrive, so its row is free.
	for name, raw := range map[string]string{
		"no investigate":             strings.Replace(strings.Replace(good, lore, "", 1), investigate, "", 1),
		"an implementer investigate": writes(strings.Replace(good, lore, "", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(raw)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
