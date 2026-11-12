// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/intake"
)

// docsCatalogue is a catalogue shaped like a docs repository's: docs-links live under a templated
// floor, review off with a no-template floor and one offered variant, and tests offered nowhere
// else. approve is the repository's approveToMerge.
func docsCatalogue(repository string, approve bool) catalogue.Catalogue {
	return catalogue.Catalogue{Repository: repository, ApproveToMerge: approve,
		Classes: []catalogue.Class{
			{Name: "docs-links", Description: "Dead links in docs", Mode: catalogue.ModeLive, CeilingUSD: 2, Floor: "links-solo"},
			{Name: "review", Description: "Anything else", Mode: catalogue.ModeOff, CeilingUSD: 8, Floor: "review-floor",
				Allowed: []string{"review-frontier"}},
		},
		Profiles: map[string]catalogue.Profile{
			"links-solo":      {Template: "solo", Roles: []string{"implementer"}, Tier: "light", Offered: true},
			"review-floor":    {NotOffered: catalogue.NoTemplate},
			"review-frontier": {Template: "trio", Roles: []string{"implementer", "tester", "reviewer"}, Tier: "frontier", Offered: true},
		}}
}

// offersIn composes against the catalogue of Smana/ai-native-ref, with the config table as the
// fallback for any other repository.
func offersIn(approve bool) intake.ComposeIn {
	in := composeIn()
	in.Offers = map[string]map[string]intake.Class{
		"smana/ai-native-ref": intake.ClassesFromCatalogue(docsCatalogue("Smana/ai-native-ref", approve)),
	}
	return in
}

func draftOf(class string, s *intake.DraftSuggestion) intake.Draft {
	return intake.Draft{Repository: "Smana/ai-native-ref",
		Issues: []intake.DraftIssue{{Title: "docs: x is wrong", Body: validBody, Class: class, Suggestion: s}}}
}

// O1, P15: a profile the class does not offer is the factory's choice, never the model's guess, and
// the operator's log names the field.
func TestSuggestionOutsideClassIsFactorysChoice(t *testing.T) {
	var logs bytes.Buffer
	in := offersIn(false)
	in.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	for _, name := range []string{"review-floor", "links-solo", "zzz"} {
		logs.Reset()
		p, err := intake.Compose(draftOf("review", &intake.DraftSuggestion{Profile: name, Why: "because"}), in)
		if err != nil {
			t.Fatal(err)
		}
		s := p.Issues[0].Suggestion
		if s == nil || s.Profile != "" || s.Roles != nil || s.Why != "" {
			t.Fatalf("%s: %+v", name, s)
		}
		if !strings.Contains(logs.String(), "suggestion.profile@1") || strings.Contains(logs.String(), name) {
			t.Fatalf("%s: log %s", name, logs.String())
		}
	}
	p, err := intake.Compose(draftOf("review", &intake.DraftSuggestion{Profile: "review-frontier", Why: "spans files"}), in)
	if err != nil {
		t.Fatal(err)
	}
	want := &intake.Suggestion{Profile: "review-frontier", Roles: []string{"implementer", "tester", "reviewer"},
		Merge: intake.MergeShown{Outcome: catalogue.OutcomeMaintainer}, Why: "spans files"}
	if s := p.Issues[0].Suggestion; s == nil || s.Profile != want.Profile || !slices.Equal(s.Roles, want.Roles) ||
		s.Merge != want.Merge || s.Why != want.Why {
		t.Fatalf("%+v", s)
	}
	if p.Issues[0].Profile != "" {
		t.Fatalf("accepting a suggestion pins nothing (P4): %q", p.Issues[0].Profile)
	}
}

// P3: the merge outcome is the broker's, from the class's mode and approveToMerge; a merge the model
// wrote is dropped and logged.
func TestMergeOutcomeIsTheBrokers(t *testing.T) {
	var reply intake.Draft
	raw := `{"repository":"Smana/ai-native-ref","issues":[{"title":"docs: x is wrong","body":` +
		jsonString(validBody) + `,"class":"review","suggestion":{"profile":"","why":"w","merge":"auto"}}]}`
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		t.Fatal(err)
	}
	for approve, want := range map[bool]string{false: catalogue.OutcomeMaintainer, true: catalogue.OutcomeApproval} {
		var logs bytes.Buffer
		in := offersIn(approve)
		in.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
		p, err := intake.Compose(reply, in)
		if err != nil {
			t.Fatal(err)
		}
		if s := p.Issues[0].Suggestion; s == nil || s.Merge != (intake.MergeShown{Outcome: want, ApproveToMerge: approve}) {
			t.Fatalf("approve %v: %+v", approve, s)
		}
		if !strings.Contains(logs.String(), "suggestion.merge@1") {
			t.Fatalf("log %s", logs.String())
		}
	}
	p, err := intake.Compose(draftOf("docs-links", nil), offersIn(false))
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Issues[0].Suggestion; s == nil || s.Merge.Outcome != catalogue.OutcomeAuto || s.Profile != "" {
		t.Fatalf("%+v", s)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// G3: the material roles are the floor's template (or none when the floor has no template: chosen
// at triage), whatever the suggestion says; the suggested profile's roles are display.
func TestRolesFollowTheFloorNotTheSuggestion(t *testing.T) {
	for class, want := range map[string][]string{"docs-links": {"implementer"}, "review": {}} {
		p, err := intake.Compose(draftOf(class, &intake.DraftSuggestion{Profile: "review-frontier", Why: "w"}), offersIn(false))
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Issues[0].Roles; !slices.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
			t.Fatalf("%s: roles %v, want %v", class, got, want)
		}
	}
}

// G4: classes are the plan repository's: a class another repository offers is refused here.
func TestPerRepositoryClasses(t *testing.T) {
	other := docsCatalogue("Smana/other", false)
	other.Classes = append(other.Classes, catalogue.Class{Name: "tests", Mode: catalogue.ModeOff, CeilingUSD: 4, Floor: "links-solo"})
	in := offersIn(false)
	in.Offers["smana/other"] = intake.ClassesFromCatalogue(other)
	if _, err := intake.Compose(draftOf("tests", nil), in); err == nil || !strings.Contains(err.Error(), intake.ReasonInvalidClass) {
		t.Fatalf("a class of another repository: %v", err)
	}
	p, err := intake.Compose(draftOf("review", nil), in)
	if err != nil {
		t.Fatal(err)
	}
	classes := intake.ClassesFor(in.Offers, in.Classes, "smana/other")
	if r := intake.Check(p, []string{"Smana/ai-native-ref"}, intake.ClassesFor(in.Offers, in.Classes, p.Repository)); r != nil {
		t.Fatal(r)
	}
	p.Issues[0].Class = "tests"
	p.Hash = intake.Hash(p)
	if r := intake.Check(p, []string{"Smana/ai-native-ref"}, intake.ClassesFor(in.Offers, in.Classes, p.Repository)); r == nil ||
		r.Reason != intake.ReasonInvalidClass {
		t.Fatalf("%+v", r)
	}
	if _, ok := classes["tests"]; !ok {
		t.Fatal("the other repository's own classes")
	}
}

// G4: the ceiling is the class's ceilingUSD from the catalogue, summed per issue as before.
func TestCeilingFromCatalogue(t *testing.T) {
	p, err := intake.Compose(draftOf("review", nil), offersIn(false))
	if err != nil {
		t.Fatal(err)
	}
	if p.Budget.CeilingUSD != 8 || p.Budget.Source != "catalogue:review.ceilingUSD" {
		t.Fatalf("%+v", p.Budget)
	}
}

// Until stage 5 a repository without a catalogue plans from the config table, with no suggestion.
func TestFallbackHasNoSuggestion(t *testing.T) {
	var logs bytes.Buffer
	in := composeIn()
	in.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	p, err := intake.Compose(draftOf("docs-links", &intake.DraftSuggestion{Profile: "links-solo", Why: "w"}), in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Issues[0].Suggestion != nil || p.Budget.Source != "class:docs-links.taskUSD" ||
		!slices.Equal(p.Issues[0].Roles, []string{"scoper", "implementer", "reviewer"}) {
		t.Fatalf("%+v %+v", p.Issues[0], p.Budget)
	}
	if !strings.Contains(logs.String(), "suggestion@1") {
		t.Fatalf("log %s", logs.String())
	}
}

// #460: the floor runs after composition and leaves the suggestion and the profile alone.
func TestFloorLeavesSuggestionAlone(t *testing.T) {
	p, err := intake.Compose(draftOf("review", &intake.DraftSuggestion{Profile: "review-frontier", Why: "w"}), offersIn(false))
	if err != nil {
		t.Fatal(err)
	}
	before := *p.Issues[0].Suggestion
	f := intake.ApplyFloor(p, "the docs are wrong")
	if s := f.Issues[0].Suggestion; s == nil || s.Profile != before.Profile || s.Why != before.Why || s.Merge != before.Merge ||
		f.Issues[0].Profile != "" {
		t.Fatalf("%+v", f.Issues[0])
	}
}

// P1: the prompt lists each candidate's classes with their profiles, and says profile, never workflow.
func TestPromptListsProfiles(t *testing.T) {
	f := fakeGateway(t, okReply)
	m := modelFor(f)
	r := req()
	r.Offers = offersIn(false).Offers
	if _, _, err := m.Draft(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	body := f.rawBody()
	for _, want := range []string{"Classes you may name in smana/ai-native-ref", "Dead links in docs", "links-solo (default)", "review-frontier", `\"suggestion\"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the prompt lacks %q: %s", want, body)
		}
	}
	if strings.Contains(strings.ToLower(body), "workflow") {
		t.Fatalf("the prompt says workflow: %s", body)
	}
	if strings.Contains(body, "review-floor") {
		t.Fatalf("a profile that is not offered reached the prompt: %s", body)
	}
}
