// SPDX-License-Identifier: Apache-2.0

package fmetrics

import (
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// D11: whether the default branch's rulesets require maku/merge-gate from the App that posts it,
// per repository, as the last fence pass read it.
func TestMergeGateRequiredGauge(t *testing.T) {
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Smana/cloud-native-ref")
	s.MergeGateRequired(t.Context(), "Smana/cloud-native-ref", true)
	s.MergeGateRequired(t.Context(), "Smana/unlisted", false)
	body := scrape()
	for _, want := range []string{
		`maku_factory_merge_gate_required{repository="Smana/cloud-native-ref"} 1`,
		`maku_factory_merge_gate_required{repository="other"} 0`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
	s.MergeGateRequired(t.Context(), "Smana/cloud-native-ref", false)
	if want := `maku_factory_merge_gate_required{repository="Smana/cloud-native-ref"} 0`; !strings.Contains(scrape(), want+"\n") {
		t.Errorf("missing %s", want)
	}
}

// PR12: a merge by the agents App is its own outcome, not folded into other.
func TestAgentMergedIsAnOutcome(t *testing.T) {
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.PROutcome(t.Context(), "docs-links", "agent_merged")
	if want := `maku_factory_pr_outcomes_total{class="docs-links",outcome="agent_merged",principal="other",repository="other"} 1`; !strings.Contains(scrape(), want+"\n") {
		t.Errorf("missing %s", want)
	}
}
