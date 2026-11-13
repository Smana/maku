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

// H2 (R2 as evidence): each granted pair of each sandbox role and the publisher is 1; a pair a
// later pass no longer reads is 0, and a role whose policy was not read keeps its pairs.
func TestSandboxPermissionGauge(t *testing.T) {
	ctx := t.Context()
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Smana/ai-native-ref")
	s.SandboxPermissions(ctx, "Smana/ai-native-ref", map[string]map[string]string{
		"implementer": {"contents": "write", "pull_requests": "write"},
		"reviewer":    {"contents": "read"},
		"publisher":   {"pull_requests": "write", "issues": "write", "contents": "read"},
		"tester":      {"an agent's text": "WRITE"},
	})
	series := func(role, perm, access string, v int) string {
		return `maku_factory_hardening_sandbox_permission{access="` + access + `",permission="` + perm +
			`",repository="Smana/ai-native-ref",role="` + role + `"} ` + string(rune('0'+v))
	}
	body := scrape()
	for _, want := range []string{
		series("implementer", "pull_requests", "write", 1), series("implementer", "contents", "write", 1),
		series("reviewer", "contents", "read", 1), series("publisher", "pull_requests", "write", 1),
		series("publisher", "issues", "write", 1), series("tester", "other", "other", 1),
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
	// The implementer drops pull_requests: write; the reviewer's policy is not read this pass.
	s.SandboxPermissions(ctx, "Smana/ai-native-ref", map[string]map[string]string{
		"implementer": {"contents": "write", "pull_requests": "read"},
		"publisher":   {"pull_requests": "write", "issues": "write", "contents": "read"},
		"tester":      {},
	})
	body = scrape()
	for _, want := range []string{
		series("implementer", "pull_requests", "write", 0), series("implementer", "pull_requests", "read", 1),
		series("reviewer", "contents", "read", 1), series("tester", "other", "other", 0),
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

// H3 (HR27): how long a protected-path push waited for its approval, by repository and how the
// approval closed, the decision a closed set.
func TestHardeningApprovalWait(t *testing.T) {
	ctx := WithRepository(t.Context(), "Smana/cloud-native-ref")
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Smana/cloud-native-ref")
	s.HardeningApprovalWait(ctx, "approved", 90*time.Second)
	s.HardeningApprovalWait(ctx, "whatever the broker said", time.Hour)
	body := scrape()
	for _, want := range []string{
		`maku_factory_hardening_approval_wait_seconds_bucket{decision="approved",repository="Smana/cloud-native-ref",le="300"} 1`,
		`maku_factory_hardening_approval_wait_seconds_bucket{decision="approved",repository="Smana/cloud-native-ref",le="60"} 0`,
		`maku_factory_hardening_approval_wait_seconds_count{decision="other",repository="Smana/cloud-native-ref"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
}

// HR38: the last scored drill's time, by result.
func TestHardeningDrillLast(t *testing.T) {
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	at := time.Unix(1_794_000_000, 0)
	s.HardeningDrillScored(t.Context(), true, at)
	s.HardeningDrillScored(t.Context(), false, at.Add(time.Hour))
	body := scrape()
	for _, want := range []string{
		`maku_factory_hardening_drill_last{result="pass"} 1.794e+09`,
		`maku_factory_hardening_drill_last{result="fail"} 1.7940036e+09`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
}

// HR39: every T5 call counted by type, result and drill, each a closed set.
func TestT5Counter(t *testing.T) {
	ctx := WithRepository(t.Context(), "Smana/cloud-native-ref")
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Smana/cloud-native-ref")
	s.HardeningT5(ctx, "pr", "hit", true)
	s.HardeningT5(ctx, "comment", "pass", false)
	s.HardeningT5(ctx, "an agent's text", "maybe", false)
	body := scrape()
	for _, want := range []string{
		`maku_factory_hardening_t5_total{drill="true",repository="Smana/cloud-native-ref",result="hit",type="pr"} 1`,
		`maku_factory_hardening_t5_total{drill="false",repository="Smana/cloud-native-ref",result="pass",type="comment"} 1`,
		`maku_factory_hardening_t5_total{drill="false",repository="Smana/cloud-native-ref",result="other",type="other"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
}

// H5 (HR41): each repository's readiness, 1 or 0, and each missing fact at 1; a fact that holds
// again goes to 0, and anything outside the closed set reads other.
func TestHardeningReadyGauge(t *testing.T) {
	ctx := t.Context()
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Smana/ai-native-ref")
	s.HardeningReady(ctx, "Smana/ai-native-ref", false, []string{"mode_not_enforce", "drill_stale_or_failed", "an agent's text"})
	body := scrape()
	for _, want := range []string{
		`maku_factory_hardening_ready{repository="Smana/ai-native-ref"} 0`,
		`maku_factory_hardening_missing{missing="mode_not_enforce",repository="Smana/ai-native-ref"} 1`,
		`maku_factory_hardening_missing{missing="drill_stale_or_failed",repository="Smana/ai-native-ref"} 1`,
		`maku_factory_hardening_missing{missing="other",repository="Smana/ai-native-ref"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
	s.HardeningReady(ctx, "Smana/ai-native-ref", true, nil)
	body = scrape()
	for _, want := range []string{
		`maku_factory_hardening_ready{repository="Smana/ai-native-ref"} 1`,
		`maku_factory_hardening_missing{missing="mode_not_enforce",repository="Smana/ai-native-ref"} 0`,
		`maku_factory_hardening_missing{missing="other",repository="Smana/ai-native-ref"} 0`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
}
