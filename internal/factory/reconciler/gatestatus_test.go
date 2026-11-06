// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
)

func TestStatusFor(t *testing.T) {
	for _, c := range []struct {
		d                    ArmDecision
		class, state, descrp string
	}{
		{ArmDecision{Verdict: "arm"}, "docs-links", "success", "docs-links: merging"},
		{ArmDecision{Verdict: "shadow", Reason: "shadow_would_arm"}, "docs-links", "success", "docs-links, shadow: would merge"},
		{ArmDecision{Verdict: "wait", Reason: "ci_pending"}, "docs-links", "pending", "docs-links: waiting, ci_pending"},
		{ArmDecision{Verdict: "human", Reason: "not_owned"}, "docs-links", "pending", "docs-links: a human decides, not_owned"},
		{ArmDecision{Verdict: "ci_red", Reason: "ci_red"}, "docs-links", "failure", "docs-links: ci_red"},
		{ArmDecision{Verdict: "ci_cancelled", Reason: "ci_cancelled"}, "docs-links", "failure", "docs-links: ci_cancelled"},
		{ArmDecision{Verdict: "escalate", Reason: "secret_scan_red"}, "docs-links", "failure", "docs-links: escalated, secret_scan_red"},
		{ArmDecision{Verdict: "arm"}, "", "success", "no class: merging"},
	} {
		state, d := statusFor(c.d, c.class)
		if state != c.state || d != c.descrp {
			t.Errorf("%+v: got %s %q, want %s %q", c.d, state, d, c.state, c.descrp)
		}
	}
}

func TestStatusForTruncatesToGitHubsLimit(t *testing.T) {
	_, d := statusFor(ArmDecision{Verdict: "human", Reason: strings.Repeat("é", 300)}, "docs-links")
	if n := len([]rune(d)); n != 140 {
		t.Fatalf("description is %d characters, want 140", n)
	}
}
