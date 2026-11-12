// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/intake"
)

// P12: each profile refusal renders the spec's one sentence, with the class and the profiles the
// reader may see in the catalogue and never a repository.
func TestProfileRefusalSentences(t *testing.T) {
	base := intake.ProfileRefusal{Class: "review", Profile: "review-frontier", Floor: "review-floor", Mode: "live",
		Allowed: []string{"review-frontier", "review-solo"}}
	want := map[string]string{
		intake.ProfileUnknown:       "`review-frontier` is not a profile of this factory. `classes_list` shows the profiles.",
		intake.ProfileNotAllowed:    "Class `review` runs under `review-floor` or `review-frontier`, `review-solo`; `review-frontier` is not one of them.",
		intake.ProfileNotOffered:    "`review-frontier` sets no template, so its roles are chosen at triage; it runs only as the factory's choice.",
		intake.ClassProfileFixed:    "Class `review` is live: its merge policy runs it under `review-floor` only.",
		intake.ClassNoProfiles:      "Class `review` declares no profiles: the factory runs it as configured.",
		intake.NotMaintainer:        "Only a maintainer of this repository can choose the profile; the factory will choose it.",
		intake.ProfileConflict:      "The issue names more than one profile; none was applied.",
		intake.ProfileInternal:      "`review-frontier` writes tests first, which runs on public tasks only.",
		intake.ProfileNoTestsPaths:  "`review-frontier` writes tests first, and this repository declares no tests paths.",
		intake.CatalogueUnavailable: "The factory's list of classes could not be read; try again in a minute.",
	}
	if len(want) != len(intake.ProfileRefusals) || len(intake.ProfileRefusals) != 10 {
		t.Fatalf("closed list = %v: want the spec's ten codes", intake.ProfileRefusals)
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

	t.Run("a class with only a floor names the floor alone", func(t *testing.T) {
		r := intake.ProfileRefusal{Code: intake.ProfileNotAllowed, Class: "docs", Profile: "x", Floor: "docs-solo"}
		if got := r.Sentence(); got != "Class `docs` runs under `docs-solo`; `x` is not one of them." {
			t.Fatalf("got %q", got)
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
