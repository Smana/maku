// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/intake"
)

// notMaintainerCatalogue is ai-native-ref's catalogue as a caller who maintains nothing reads it.
type notMaintainerCatalogue struct{}

func (notMaintainerCatalogue) Catalogue(context.Context, string, string, string) (catalogue.Catalogue, error) {
	return aiNativeRef(false), nil
}

// pinned is the single-issue fixture in class, pinned to profile, with its hash renewed.
func pinned(t *testing.T, class, profile string) intake.Plan {
	t.Helper()
	p := planFixture(t, "single")
	p.Issues[0].Class, p.Issues[0].Profile = class, profile
	p.Hash = intake.Hash(p)
	return p
}

// P6: plan_check refuses a pin the catalogue refuses, with the closed list's code and sentence, and
// finalises nothing; a pin that holds is answered.
func TestPlanCheckRefusesPin(t *testing.T) {
	for name, tc := range map[string]struct {
		class, profile string
		src            CatalogueSource
		want           string
	}{
		"live class":            {"docs-links", "links-solo", &fakeFactoryCatalogue{}, intake.ClassProfileFixed},
		"unknown":               {"review", "ghost", &fakeFactoryCatalogue{}, intake.ProfileUnknown},
		"not allowed":           {"review", "links-solo", &fakeFactoryCatalogue{}, intake.ProfileNotAllowed},
		"no template":           {"review", "review-floor", &fakeFactoryCatalogue{}, intake.ProfileNotOffered},
		"not a maintainer":      {"review", "review-frontier", notMaintainerCatalogue{}, intake.NotMaintainer},
		"catalogue unavailable": {"docs-links", "links-solo", &fakeFactoryCatalogue{byRepo: map[string]error{"Smana/ai-native-ref": errors.New("down")}}, intake.CatalogueUnavailable},
		"honoured":              {"review", "review-frontier", &fakeFactoryCatalogue{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withCatalogue(tc.src))
			plan := pinned(t, tc.class, tc.profile)
			got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": plan, "hash": plan.Hash})
			if tc.want == "" {
				if got.isError {
					t.Fatal(got.text)
				}
				return
			}
			if !got.isError || !strings.Contains(got.text, tc.want) {
				t.Fatalf("want %s: %s", tc.want, got.text)
			}
			if !strings.Contains(metricsBody(t, e), `maku_intake_profile_refusals_total{reason="`+tc.want+`",surface="session"} 1`) {
				t.Fatal("the refusal was not counted")
			}
			if e.intake.calls() != 0 {
				t.Fatal("a model call on plan_check")
			}
		})
	}
}

// P5: a pin is a revision through plan_check: only the profile changes, one Decision record is
// appended, the hash is renewed, and no model is called. The choice is counted.
func TestPlanCheckPinRevision(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withCatalogue(&fakeFactoryCatalogue{}))
	base := pinned(t, "review", "")
	base.Issues[0].Suggestion = &intake.Suggestion{Profile: "review-frontier", Why: "several pages"}
	candidate := base
	candidate.Issues = []intake.Issue{base.Issues[0]}
	candidate.Issues[0].Profile = "review-frontier"
	got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": base, "hash": base.Hash, "candidate": candidate})
	if got.isError {
		t.Fatal(got.text)
	}
	var out planChecked
	if err := json.Unmarshal(got.structured, &out); err != nil {
		t.Fatal(err)
	}
	is := out.Plan.Issues[0]
	if is.Profile != "review-frontier" || !strings.HasSuffix(is.Body, "D1: profile: review-frontier, pinned by the developer") ||
		out.Hash == base.Hash || out.Hash != intake.Hash(out.Plan) {
		t.Fatalf("%+v %s", is, out.Hash)
	}
	if e.intake.calls() != 0 {
		t.Fatal("a model call on a profile revision")
	}
	if !strings.Contains(metricsBody(t, e), `maku_intake_profile_choices_total{choice="suggested",class="review",surface="session"} 1`) {
		t.Fatal(metricsBody(t, e))
	}
}

// P4: a kept factory's choice is written down when the suggestion named a profile, and counted.
func TestPlanCheckRecordsTheFactorysChoice(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withCatalogue(&fakeFactoryCatalogue{}))
	plan := pinned(t, "review", "")
	plan.Issues[0].Suggestion = &intake.Suggestion{Profile: "review-frontier"}
	got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": plan, "hash": plan.Hash})
	if got.isError {
		t.Fatal(got.text)
	}
	var out planChecked
	if err := json.Unmarshal(got.structured, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.Plan.Issues[0].Body, "A1: profile: factory's choice; suggested review-frontier") || out.Hash != intake.Hash(out.Plan) {
		t.Fatalf("%q", out.Plan.Issues[0].Body)
	}
	// The answer is a plan plan_check holds again, unchanged.
	again := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": out.Plan, "hash": out.Hash})
	var second planChecked
	if again.isError || json.Unmarshal(again.structured, &second) != nil || second.Hash != out.Hash {
		t.Fatalf("not idempotent: %s", again.text)
	}
	if !strings.Contains(metricsBody(t, e), `maku_intake_profile_choices_total{choice="factory",class="review",surface="session"} 2`) {
		t.Fatal(metricsBody(t, e))
	}
}

// A pin through intake_plan is a revision with no model call and no daily plan, held to the same
// pin check.
func TestIntakePlanPinRevision(t *testing.T) {
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withCatalogue(&fakeFactoryCatalogue{}))
	base := pinned(t, "review", "")
	for profile, want := range map[string]string{"review-frontier": "", "links-solo": intake.ProfileNotAllowed} {
		candidate := base
		candidate.Issues = []intake.Issue{base.Issues[0]}
		candidate.Issues[0].Profile = profile
		got := call(t, e, viaRoomctl("dev"), "intake_plan", revisionArgs(base, candidate))
		if want != "" {
			if !got.isError || !strings.Contains(got.text, want) {
				t.Fatalf("%s: %s", profile, got.text)
			}
			continue
		}
		if got.isError {
			t.Fatal(got.text)
		}
		var out struct {
			Plan intake.Plan `json:"plan"`
		}
		if err := json.Unmarshal(got.structured, &out); err != nil || out.Plan.Issues[0].Profile != profile {
			t.Fatalf("%v %+v", err, out.Plan.Issues)
		}
	}
	if e.intake.calls() != 0 {
		t.Fatal("a model call on a profile revision")
	}
}
