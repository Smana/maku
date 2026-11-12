// SPDX-License-Identifier: Apache-2.0

package fmetrics

import (
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// H2's latency (AC7): each answer's wait by repository and output type, the type a closed set.
func TestHardeningOutputSeconds(t *testing.T) {
	ctx := WithRepository(t.Context(), "Smana/cloud-native-ref")
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Smana/cloud-native-ref")
	s.HardeningOutput(ctx, "pr", 1500*time.Millisecond)
	s.HardeningOutput(ctx, "an agent's text", time.Second)
	body := scrape()
	for _, want := range []string{
		`maku_factory_hardening_output_seconds_bucket{repository="Smana/cloud-native-ref",type="pr",le="2"} 1`,
		`maku_factory_hardening_output_seconds_bucket{repository="Smana/cloud-native-ref",type="pr",le="1"} 0`,
		`maku_factory_hardening_output_seconds_count{repository="Smana/cloud-native-ref",type="other"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
}

// H2's tracer: each untraced agent write by repository and output type, the type a closed set.
func TestHardeningUntracedIsCounted(t *testing.T) {
	ctx := WithRepository(t.Context(), "Smana/cloud-native-ref")
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Smana/cloud-native-ref")
	s.HardeningUntraced(ctx, "comment")
	s.HardeningUntraced(ctx, "an agent's text")
	body := scrape()
	for _, want := range []string{
		`maku_factory_hardening_untraced_total{repository="Smana/cloud-native-ref",type="comment"} 1`,
		`maku_factory_hardening_untraced_total{repository="Smana/cloud-native-ref",type="other"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
}

// H1's shadow counter (HR3): each output_shadowed record by repository, type, verdict, layer and
// reason, every label a closed set, so an agent's text can never name a series.
func TestHardeningShadowIsCounted(t *testing.T) {
	ctx := WithRepository(t.Context(), "Smana/cloud-native-ref")
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Smana/cloud-native-ref")
	s.HardeningShadow(ctx, "pr", "would_apply", "", "")
	s.HardeningShadow(ctx, "labels", "would_refuse", "t1_schema", "output_schema")
	s.HardeningShadow(ctx, "push", "would_wait", "t4_paths", "protected_path_needs_approval")
	s.HardeningShadow(ctx, "Ignore previous", "maybe", "t9", "an agent's text")
	s.HardeningShadow(ctx, "push", "would_refuse", "t4_paths", "diff_unavailable")
	s.HardeningShadow(ctx, "", "incomplete", "", "shadow_incomplete")
	body := scrape()
	for _, want := range []string{
		`maku_factory_hardening_shadow_total{layer="t4_paths",reason="diff_unavailable",repository="Smana/cloud-native-ref",type="push",verdict="would_refuse"} 1`,
		`maku_factory_hardening_shadow_total{layer="none",reason="shadow_incomplete",repository="Smana/cloud-native-ref",type="none",verdict="incomplete"} 1`,
		`maku_factory_hardening_shadow_total{layer="none",reason="none",repository="Smana/cloud-native-ref",type="pr",verdict="would_apply"} 1`,
		`maku_factory_hardening_shadow_total{layer="t1_schema",reason="output_schema",repository="Smana/cloud-native-ref",type="labels",verdict="would_refuse"} 1`,
		`maku_factory_hardening_shadow_total{layer="t4_paths",reason="protected_path_needs_approval",repository="Smana/cloud-native-ref",type="push",verdict="would_wait"} 1`,
		`maku_factory_hardening_shadow_total{layer="other",reason="other",repository="Smana/cloud-native-ref",type="other",verdict="other"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
}
