// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/intake"
)

// P12: each profile refusal renders the spec's one sentence, with the class and the profiles the
// reader may see in the catalogue and never a repository.
func TestProfileRefusalSentences(t *testing.T) {
	base := intake.ProfileRefusal{Class: "review", Profile: "review-frontier"}
	want := map[string]string{
		intake.ProfileUnknown:       "`review-frontier` is not a workflow of this factory.",
		intake.ProfileNotOffered:    "`review-frontier` names no roles, so they are chosen at triage; it runs only as the factory's choice.",
		intake.NotMaintainer:        "Only a maintainer of this repository can choose the workflow; the factory will choose it.",
		intake.ProfileConflict:      "The issue names more than one workflow; none was applied.",
		intake.ProfileNoTestsPaths:  "`review-frontier` writes tests first, and this repository declares no tests paths.",
		intake.CatalogueUnavailable: "The factory's list of classes could not be read; try again in a minute.",
	}
	// WF4, WF7: the class refusals and profile_internal went with the workflow's leaving the class.
	if len(want) != len(intake.ProfileRefusals) || len(intake.ProfileRefusals) != 6 {
		t.Fatalf("closed list = %v: want six codes", intake.ProfileRefusals)
	}
	for _, code := range intake.ProfileRefusals {
		r := base
		r.Code = code
		got := r.Sentence()
		if got != want[code] {
			t.Errorf("%s: %q, want %q", code, got, want[code])
		}
		if strings.Contains(got, "/") {
			t.Errorf("%s: %q names a repository", code, got)
		}
		if ref := r.Refusal(); ref.Reason != code || ref.Sentence != got {
			t.Errorf("%s: refusal = %+v", code, ref)
		}
	}

	t.Run("the task facts' validator holds the list, and the codes of facts written before WF4", func(t *testing.T) {
		if !slices.Equal(envelope.ProfileRefusalCodes[:len(intake.ProfileRefusals)], intake.ProfileRefusals) {
			t.Fatalf("envelope %v, intake %v", envelope.ProfileRefusalCodes, intake.ProfileRefusals)
		}
	})
	t.Run("an unknown code panics through the closed switch", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("no panic for a code outside the list")
			}
		}()
		_ = intake.ProfileRefusal{Code: "invented"}.Sentence()
	})
}

func TestRefusalCodesAreShownAsWorkflow(t *testing.T) {
	for api, shown := range map[string]string{
		"profile_unknown":        "workflow_unknown",
		"profile_conflict":       "workflow_conflict",
		"profile_no_tests_paths": "workflow_no_tests_paths",
		"profile_not_offered":    "workflow_not_offered",
		"not_maintainer":         "not_maintainer",
	} {
		if intake.Shown(api) != shown || intake.Accepted(shown) != api {
			t.Errorf("%s: shown %q, accepted %q", api, intake.Shown(api), intake.Accepted(shown))
		}
	}
}
