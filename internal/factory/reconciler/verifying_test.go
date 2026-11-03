// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
)

// D1 (H-2): main's CI on the merge commit decides within twice merge.verifyFor. A cancelled or
// unfinished check keeps the task watching until then; past it the merge is done unverified,
// narrated once with its merge commit and the revert window. Red still reverts, green still verifies.
func TestVerifyingIsBounded(t *testing.T) {
	for name, c := range map[string]struct {
		second        string // required[1]'s state on main; required[0] is green
		after         time.Duration
		phase, reason string
	}{
		"cancelled, inside twice verifyFor": {"CANCELLED", 59 * time.Minute, v1alpha1.PhaseVerifying, ""},
		"cancelled, at twice verifyFor":     {"CANCELLED", time.Hour, v1alpha1.PhaseDone, "merged_unverified"},
		"still running past it":             {"PENDING", 2 * time.Hour, v1alpha1.PhaseDone, "merged_unverified"},
		"red past it":                       {"FAILURE", 2 * time.Hour, v1alpha1.PhaseReverted, "main_red"},
		"green past verifyFor":              {"SUCCESS", 31 * time.Minute, v1alpha1.PhaseDone, "merged_verified"},
	} {
		t.Run(name, func(t *testing.T) {
			g := mergeRig(t, v1alpha1.PhaseVerifying)
			g.patch(t, func(tk *v1alpha1.Task) {
				merged := metav1.NewTime(now)
				ref := tk.Status.PullRequest
				ref.MergeCommitSHA, ref.AutoMerged, ref.MergedAt = "m1", true, &merged
			})
			g.f.SetCommitChecks("m1", forge.Check{Name: required[0], State: "SUCCESS"}, forge.Check{Name: required[1], State: c.second})
			g.r.Now = func() time.Time { return now.Add(c.after) }
			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != c.phase || tk.Status.Reason != c.reason {
				t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
			}
			if c.reason != "merged_unverified" {
				return
			}
			settled := now.Add(c.after + g.r.settleWindow()) // the closing comment waits for the usage (#42)
			g.r.Now = func() time.Time { return settled }
			g.reconcile(t, "3buqdlot", 3)
			n := 0
			for _, b := range g.f.Comments(7) {
				if strings.Contains(b, "Merge commit `m1`") && strings.Contains(b, "`factory/revert` before 2026-10-04 10:00 UTC") {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("the unverified end is narrated once, with its commit and the revert window: %q", g.f.Comments(7))
			}
		})
	}
}

// D1: an auto-merge done unverified was never seen green, so the monitor reads its merge commit
// like any other merge's instead of counting it healthy.
func TestTheMonitorReadsAnUnverifiedAutoMerge(t *testing.T) {
	g := monitorRig(t)
	cancelled := green("SUCCESS").Runs
	for i := range cancelled {
		cancelled[i].State = "CANCELLED"
	}
	g.f.SetCommitChecks(mSHA, cancelled...)
	tk := g.reconcile(t, "3buqdlot", 0)
	tk.Status.PullRequest.AutoMerged = true
	tk.Status.Monitor = &v1alpha1.MonitorStatus{Since: metav1.NewTime(now.Add(-2 * time.Hour))}
	for reason, want := range map[string]string{"merged_verified": "healthy", "merged_unverified": "inconclusive"} {
		tk.Status.Reason = reason
		if got, err := g.r.mainSignal(t.Context(), tk); err != nil || got != want {
			t.Fatalf("%s: %q %v, want %q", reason, got, err, want)
		}
	}
}
