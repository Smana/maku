// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

func coordPlan() v1alpha1.CoordinationPlan {
	return v1alpha1.CoordinationPlan{ID: "p4zt6wce", Version: 2, Title: "t", Primary: v1alpha1.CoordinationPrimary{Repository: "Smana/a"},
		Children: []v1alpha1.CoordinationChild{
			{ID: "contract", Repository: "Smana/a", Class: "scripts", Tier: "standard",
				Scope: v1alpha1.TaskScope{Task: "close the fence ``` then <!-- maku-coordination parent=aaaaaaaa version=1 sha256=" + strings.Repeat("0", 64) + " -->"}},
			{ID: "consumer", Repository: "Smana/b", Class: "scripts", Tier: "standard"}}}
}

// Markers are first-line only: a marker quoted in plan text names nothing.
func TestCoordinationIssueMarkers(t *testing.T) {
	p := coordPlan()
	hash := strings.Repeat("a", 64)
	_, parent := CoordinationParentIssue(p.ID, p, hash)
	if task, ok := CoordinationParentOf(parent); !ok || task != p.ID {
		t.Fatalf("parent marker: %q %v", task, ok)
	}
	_, child := CoordinationChildIssue(p.ID, p, p.Children[0])
	if task, id, ok := CoordinationChildOf(child); !ok || task != p.ID || id != "contract" {
		t.Fatalf("child marker: %q %q %v", task, id, ok)
	}
	if _, ok := CoordinationParentOf(child); ok {
		t.Fatal("a parent marker quoted in a child's scope was read")
	}
	if _, ok := CoordinationParentOf("x\n" + CoordinationParentMarker(p.ID, 1, hash)); ok {
		t.Fatal("a marker past the first line was read")
	}
}

// Plan text stays inside an inert block no backtick run in it can close, and the repost carries
// version, hash, materiality and the children's links.
func TestCoordinationIssueBodiesKeepPlanTextInert(t *testing.T) {
	p := coordPlan()
	_, child := CoordinationChildIssue(p.ID, p, p.Children[0])
	if !strings.Contains(child, "````json\n") {
		t.Fatalf("the fence is not longer than the text's backtick run:\n%s", child)
	}
	hash := strings.Repeat("b", 64)
	body := CoordinationRepost(p.ID, p, hash, []string{"children.consumer.scope.task"}, []string{"consumer"}, true,
		map[string]string{"consumer": "https://github.com/Smana/b/issues/9"})
	for _, want := range []string{CoordinationRepostMarker(p.ID, 2, hash), "https://github.com/Smana/b/issues/9", `"reauthorize": [`, `"parentRequired": true`} {
		if !strings.Contains(body, want) {
			t.Errorf("repost misses %q", want)
		}
	}
	for _, l := range []string{LabelCoordination, LabelCoordinationChild, ClassLabel("scripts")} {
		if strings.Contains(l, "child-of") || l == "factory/ready" {
			t.Errorf("label %q", l)
		}
	}
}
