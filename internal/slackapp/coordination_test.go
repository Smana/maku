// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"reflect"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/factory/coordination"
)

// A plain owner/name counts only when the factory serves it somewhere (so "and/or" is a word), and
// a GitHub URL always counts: an unserved repository named by URL still refuses before any model call.
func TestExplicitRepositoriesSelectCoordination(t *testing.T) {
	for text, want := range map[string][]string{
		"change Smana/public-contracts then Smana/public-consumer": {"Smana/public-contracts", "Smana/public-consumer"},
		"fix Smana/a": {"Smana/a"},
		"fix Smana/a and again Smana/a, then https://github.com/Smana/b": {"Smana/a", "Smana/b"},
		"no repository here, just a/b-ish words like and/or":             {},
		"three: o/a o/b o/c": {"o/a", "o/b", "o/c"},
	} {
		known := []string{"Smana/public-contracts", "Smana/public-consumer", "Smana/a", "o/a", "o/b", "o/c"}
		if got := explicitRepositories(text, known); (len(got) > 0 || len(want) > 0) && !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
}

func projectionView(restricted int) coordinationview.View {
	v := coordination.CoordinationView{ParentTask: "pppppppp", Phase: "Coordinating", Reason: "children_running", PlanHash: "0123456789abcdef",
		Plan: v1alpha1.CoordinationPlan{ID: "pppppppp", Version: 2, Primary: v1alpha1.CoordinationPrimary{Repository: "o/a"},
			Children: []v1alpha1.CoordinationChild{{ID: "contract", Repository: "o/a"}, {ID: "consumer", Repository: "o/b"}}},
		Children: []coordination.CoordinationChildView{
			{ID: "contract", Repository: "o/a", Room: "aaaabbbb", Phase: "Merged", PRURL: "https://github.com/o/a/pull/2"},
			{ID: "consumer", Repository: "o/b", Room: "ccccdddd", Phase: "Running", Reason: "BSECRET"},
		},
		Recovery: &v1alpha1.RecoveryProposal{Code: "retry_child", ChildID: "consumer"}}
	if restricted > 0 {
		v.Plan.Children, v.Children, v.Recovery = v.Plan.Children[:1], v.Children[:1], nil
	}
	return coordinationview.View{CoordinationView: v, RestrictedChildren: restricted}
}

func TestSlackProjectionFullServesEveryChild(t *testing.T) {
	p, ok := projectCoordination(projectionView(0), Channel{Repositories: []string{"o/a", "o/b"}}, "https://rooms.example")
	if !ok || p.Outcome != projectionFull || len(p.Lines) < 3 {
		t.Fatalf("%+v", p)
	}
	text := strings.Join(p.Lines, "\n")
	for _, want := range []string{"contract (o/a): Merged", "consumer (o/b): Running", "retry_child", "https://rooms.example/r/pppppppp"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}

func TestSlackProjectionPartialIsCoarseOnly(t *testing.T) {
	for name, c := range map[string]struct {
		v  coordinationview.View
		ch Channel
	}{
		"a channel serving the primary only": {projectionView(0), Channel{Repositories: []string{"o/a"}}},
		"a child withheld from the App":      {projectionView(1), Channel{Repositories: []string{"o/a", "o/b"}}},
	} {
		t.Run(name, func(t *testing.T) {
			p, ok := projectCoordination(c.v, c.ch, "https://rooms.example")
			text := strings.Join(p.Lines, "\n")
			if !ok || p.Outcome != projectionCoarse || len(p.Lines) != 1 || strings.Contains(text, "consumer") || strings.Contains(text, "BSECRET") ||
				strings.Contains(text, "o/b") || !strings.Contains(text, "https://rooms.example/r/pppppppp") {
				t.Fatalf("%+v", p)
			}
		})
	}
}

func TestSlackProjectionUnservedPrimaryDrops(t *testing.T) {
	if p, ok := projectCoordination(projectionView(0), Channel{Repositories: []string{"o/b"}}, "https://rooms.example"); ok || p.Outcome != projectionDropped {
		t.Fatalf("%+v", p)
	}
}
