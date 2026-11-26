// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/meaning"
)

// Correctness signals W10: every merged, shadow and adopted decision message carries one line
// naming what checked the meaning, or that nothing did, and links the words; the monitor's
// healthy reads quiet, its key keeps the wire value.
func TestDecisionMessagesCarryTheSignal(t *testing.T) {
	tk := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "t1"}, Spec: v1alpha1.TaskSpec{PredictedClass: "docs-links"}}
	links := meaning.Build(meaning.Input{Name: "docs-links", Mode: "live"})
	prose := meaning.Build(meaning.Input{Name: "docs", Mode: "live", Class: config.Class{AllowedFiles: []string{`^.+\.md$`}}})
	alert := meaning.Build(meaning.Input{Name: "alert-rules", Mode: "shadow", Class: config.Class{AllowedFiles: []string{`^o/.+\.yaml$`}}})
	contract := meaning.Build(meaning.Input{Name: "alert-rules", Mode: "shadow", Class: config.Class{AllowedFiles: []string{`^o/.+\.yaml$`}, Requires: []string{"plan", "tests"}}})
	patch := meaning.Build(meaning.Input{Name: "deps-patch", Mode: "shadow", Class: config.Class{DiffShape: "semver-patch"}})
	const head = "Agent factory task `t1`: "
	for name, c := range map[string]struct{ got, want string }{
		"merged, signal-checked": {Armed(tk, links, "docs-links").Body,
			head + "Merged automatically by policy `docs-links`, at the decided head: GitHub re-checks that head at merge time and refuses a moved one. main's CI is watched after the merge.\n\nMeaning checked by the `links-only` shape. " + DocsLink},
		"merged, live prose": {Armed(tk, prose, "docs").Body,
			head + "Merged automatically by policy `docs`, at the decided head: GitHub re-checks that head at merge time and refuses a moved one. main's CI is watched after the merge.\n\nMeaning not checked: this is prose. " + DocsLink},
		"shadow, with a signal": {WouldArm(tk, contract, "alert-rules", "abc", false, false, 7, 20).Body,
			head + "Policy `alert-rules` would have merged this. It is in shadow here. Merge it unchanged if you agree: that makes 7 of 20 agreed decisions toward going live.\n\nMeaning checked by the test contract. " + DocsLink},
		"shadow, unchecked": {WouldArm(tk, alert, "alert-rules", "abc", false, false).Body,
			head + "Policy `alert-rules` would have merged this. It is in shadow here. Merge it unchanged if you agree.\n\nMeaning not checked. " + DocsLink},
		"adopted": {WouldArmAdopted(tk, config.Class{DiffShape: "semver-patch"}, patch, "deps-patch", "abc", false, false).Body,
			"Agent factory: would auto-merge: `deps-patch`, checks green, the diff a semver bump. The class is in shadow: nothing is merged by the factory.\n\nMeaning checked by the `semver-patch` shape. " + DocsLink},
		"monitor quiet": {MonitorVerdict(tk, 12, "healthy", []string{"main CI: green"}, false, false).Body,
			"Agent factory monitor of #12 (`docs-links`): **quiet**. main CI: green"},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", name, c.got, c.want)
		}
	}
	if k := MonitorVerdict(tk, 12, "healthy", nil, false, false).Key; k != "monitor-12 verdict=healthy" {
		t.Errorf("the key keeps the wire value: %q", k)
	}
}
