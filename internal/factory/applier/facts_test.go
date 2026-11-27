// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/hardening"
)

// Developer review gap 6: the factory appends how to try the change, above the provenance footer.
func TestPRBodyCarriesTheFactorysFacts(t *testing.T) {
	tk := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"}, Status: v1alpha1.TaskStatus{RoomRef: "3buqdlot"}}
	got := prBody(tk, "## What changed\nx\n", hardening.Provenance{Output: "0123456789abcdef"}, "https://rooms.example/")
	for _, want := range []string{"## What changed\nx\n\n## Try it", "`maku checkout 3buqdlot`", "https://rooms.example/r/3buqdlot",
		"Agent-Output: 0123456789abcdef"} {
		if !strings.Contains(got, want) {
			t.Errorf("body lacks %q:\n%s", want, got)
		}
	}
	if _, id := hardening.SplitFooter(got); id != "0123456789abcdef" {
		t.Errorf("the footer must stay last, got %q", id)
	}
	if got := prBody(tk, "x", hardening.Provenance{Output: "0123456789abcdef"}, ""); strings.Contains(got, "/r/") {
		t.Errorf("no rooms URL, no room link:\n%s", got)
	}
}
