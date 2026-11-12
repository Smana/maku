// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// P13: a resolved task of a class that offers a choice counts the planner's suggestion against the
// scoper's recommendation once; a pinned task is offered none and counts nothing.
func TestProfileAgreement(t *testing.T) {
	for name, c := range map[string]struct {
		suggested, recommended string
		want                   string
	}{
		"agree":             {"review-search", "review-search", "agreement review agree"},
		"differ":            {"review-search", "review-tested", "agreement review differ"},
		"no suggestion":     {"", "review-search", "agreement review no_suggestion"},
		"no recommendation": {"review-search", "", "agreement review no_recommendation"},
	} {
		t.Run(name, func(t *testing.T) {
			g, tk := triagedRig(t, resolveCfg("observe", 0), "", "")
			if c.suggested != "" {
				tk.Spec.Intake = &v1alpha1.TaskIntake{Suggested: c.suggested}
			}
			sc := readyScope()
			sc.Profile = c.recommended
			if err := g.r.resolveProfile(t.Context(), g.r.Cfg.Current(), tk, &sc); err != nil {
				t.Fatal(err)
			}
			got := slices.DeleteFunc(slices.Clone(g.metrics.recorded), func(m string) bool { return !strings.HasPrefix(m, "agreement ") })
			if !slices.Equal(got, []string{c.want}) {
				t.Fatalf("metrics %q, want %q", got, c.want)
			}
		})
	}
	t.Run("a pinned task counts nothing", func(t *testing.T) {
		g := pinRig(t, resolveCfg("observe", 0), issueTask("3buqdlot", 7, "fix it"), "Smana", "profile:review-search")
		tk := g.reconcile(t, "3buqdlot", 1)
		tk.Spec.Intake = &v1alpha1.TaskIntake{Suggested: "review-search"}
		if err := g.r.resolveProfile(t.Context(), g.r.Cfg.Current(), tk, nil); err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(g.metrics.recorded, func(m string) bool { return strings.HasPrefix(m, "agreement ") }) {
			t.Fatalf("metrics %q", g.metrics.recorded)
		}
	})
}

// P13: the planner and the scoper stay independent: spec.intake.suggested reaches no brief.
func TestScoperBriefNeverReadsSuggestion(t *testing.T) {
	const canary = "canary-suggestion-zq9"
	tk := issueTask("3buqdlot", 7, "fix the dead link")
	tk.Spec.PredictedClass, tk.Spec.Template, tk.Spec.DataClass = "review", "pair", "public"
	tk.Spec.Intake = &v1alpha1.TaskIntake{Class: "review", Suggested: canary, Login: "Smana"}
	choices := []ProfileChoice{{Name: "review-floor", Description: "the floor", Floor: true}, {Name: "review-search", Description: "search first"}}
	sc := &Scoped{}
	revise, _ := ReviseBrief(tk, nil, nil, "n0nce")
	for name, brief := range map[string]string{
		"first":    FirstBrief(tk, "n0nce"),
		"triager":  TriagerBrief(tk, "n0nce"),
		"scoper":   ScoperBrief(tk, "n0nce", config.Scoper{}.WithDefaults(), choices),
		"tester":   TesterBrief(tk, "n0nce", sc),
		"reviewer": ReviewerBrief(tk, "n0nce", sc),
		"revise":   revise,
	} {
		if brief == "" || strings.Contains(brief, canary) {
			t.Errorf("%s brief carries the planner's suggestion (or is empty)", name)
		}
	}
}
