// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"slices"
	"testing"

	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/intake"
)

// P14, G2: the upgrade keeps every issue's words, class and order, pins nothing, and is a new hash.
func TestUpgradeKeepsIssues(t *testing.T) {
	v1 := planFixture(t, "v1-three")
	v1.Hash = intake.Hash(v1)
	for name, table := range map[string]map[string]intake.Class{
		"config":    classes(),
		"catalogue": intake.ClassesFromCatalogue(docsCatalogue("Smana/ai-native-ref", false)),
	} {
		v2, ref := intake.Upgrade(v1, table)
		if ref != nil {
			t.Fatalf("%s: %+v", name, ref)
		}
		if v2.APIVersion != intake.APIVersion || v2.Hash == v1.Hash || v2.Hash != intake.Hash(v2) || len(v2.Issues) != len(v1.Issues) {
			t.Fatalf("%s: %+v", name, v2)
		}
		for i, is := range v2.Issues {
			was := v1.Issues[i]
			if is.Title != was.Title || is.Body != was.Body || is.Class != was.Class || !slices.Equal(is.DependsOn, was.DependsOn) ||
				is.Profile != "" {
				t.Fatalf("%s: issue %d %+v", name, i+1, is)
			}
			if (is.Suggestion != nil) != (name == "catalogue") {
				t.Fatalf("%s: suggestion %+v", name, is.Suggestion)
			}
			if s := is.Suggestion; s != nil && (s.Profile != "" || s.Why != "" || s.Merge.Outcome != catalogue.OutcomeAuto) {
				t.Fatalf("%s: suggestion %+v", name, s)
			}
		}
		if r := intake.Check(v2, []string{"Smana/ai-native-ref", "Smana/cloud-native-ref"}, table); r != nil {
			t.Fatalf("%s: the upgraded plan does not hold: %+v", name, r)
		}
	}
	if _, ref := intake.Upgrade(planFixture(t, "single"), classes()); ref == nil || ref.Reason != intake.ReasonPlannerInvalid {
		t.Fatalf("a plan/v2 upgraded: %+v", ref)
	}
}

// A class the repository no longer offers is refused, never guessed.
func TestUpgradeClassGone(t *testing.T) {
	_, ref := intake.Upgrade(planFixture(t, "v1-single"), map[string]intake.Class{"review": {CeilingUSD: 8, Source: "x"}})
	if ref == nil || ref.Reason != intake.ReasonInvalidClass {
		t.Fatalf("%+v", ref)
	}
}
