// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/factory/sanitize"
)

// profileNameRE is a harness profile's name as config and the Task's CRD spell it: it lands in an
// issue body and a label, so nothing else is taken.
var profileNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// IsProfileRevision is whether candidate is base with only issues' profiles changed: the edit kind
// a pin is (P4, P5). Every other material field and every body is base's, byte for byte.
func IsProfileRevision(base, candidate Plan) bool {
	if candidate.APIVersion != base.APIVersion || candidate.Repository != base.Repository || candidate.Budget != base.Budget ||
		len(candidate.Issues) != len(base.Issues) {
		return false
	}
	changed := false
	for i := range base.Issues {
		b, c := base.Issues[i], candidate.Issues[i]
		changed = changed || c.Profile != b.Profile
		c.Profile, c.Suggestion = b.Profile, b.Suggestion
		if c.Index != b.Index || c.Title != b.Title || c.Body != b.Body || c.Class != b.Class ||
			!slices.Equal(c.Roles, b.Roles) || !slices.Equal(c.DependsOn, b.DependsOn) {
			return false
		}
	}
	return changed
}

// reviseProfile is a verified profile revision: base with candidate's profiles, and one Decision
// record per changed issue so the issue filed says what was chosen. The display fields are base's
// (a client's rounds and gaps are not trusted) and the hash is renewed. No model call: whether the
// pin holds is the catalogue's to say (CheckPin), at the door that has one.
func reviseProfile(base, candidate Plan) (Plan, error) {
	if len(base.Gaps) > 0 {
		return Plan{}, refuse(ReasonPlannerInvalid, "a profile is pinned once every gap is answered")
	}
	out := cloneRevision(base)
	next := lastRecord(base)
	for i, is := range candidate.Issues {
		if is.Profile == base.Issues[i].Profile {
			continue
		}
		text := "profile: factory's choice, chosen by the developer"
		if is.Profile != "" {
			if !profileNameRE.MatchString(is.Profile) {
				return Plan{}, refuse(ReasonPlannerInvalid, fmt.Sprintf("issue %d: profile is a profile's name", i+1))
			}
			text = "profile: " + is.Profile + ", pinned by the developer"
		}
		next++
		out.Issues[i].Profile = is.Profile
		body := out.Issues[i].Body
		if is.Profile != "" {
			body = dropFactorysChoice(body) // the pin replaces it: the body never says both
		}
		out.Issues[i].Body = appendRevisionRecord(body, fmt.Sprintf("D%d: %s", next, text))
	}
	out.Hash = Hash(out)
	return out, nil
}

// RecordProfiles writes the factory's choice down on a final plan (P4): an issue left unpinned whose
// suggestion named a profile gains `A<n>: profile: factory's choice; suggested <s>`, so the issue
// filed carries what was chosen against what was suggested. An issue whose Decisions already hold a
// profile record gains nothing, so the call is idempotent; the hash is renewed when a record is added.
func RecordProfiles(p Plan) Plan {
	next := lastRecord(p)
	out := cloneRevision(p)
	added := false
	for i, is := range p.Issues {
		if is.Profile != "" || is.Suggestion == nil || is.Suggestion.Profile == "" || hasProfileRecord(is.Body) {
			continue
		}
		next++
		out.Issues[i].Body = appendRevisionRecord(is.Body,
			fmt.Sprintf("A%d: profile: factory's choice; suggested %s", next, is.Suggestion.Profile))
		added = true
	}
	if !added {
		return p
	}
	out.Hash = Hash(out)
	return out
}

// factorysChoiceRE is the Assumption RecordProfiles writes, one whole line.
var factorysChoiceRE = regexp.MustCompile(`(?m)^A[0-9]+: profile: factory's choice; suggested [a-z0-9-]+\n?`)

// dropFactorysChoice removes the factory's-choice Assumption a pin replaces. Records keep their
// numbers: the grammar asks for ascending numbers, not contiguous ones.
func dropFactorysChoice(body string) string {
	return factorysChoiceRE.ReplaceAllString(body, "")
}

// PinRecorded is whether body's last profile record is the Decision that pinned profile: a pinned
// issue says so in the issue filed (CheckFinal).
func PinRecorded(body, profile string) bool {
	records, _ := sanitize.DecisionRecords(body)
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if r.Gap == "" && strings.HasPrefix(r.Text, "profile: ") {
			return r.Kind == "D" && r.Text == "profile: "+profile+", pinned by the developer"
		}
	}
	return false
}

// MaterialRoles writes each issue's material roles from the plan repository's catalogue classes
// (G3): the pinned profile's template roles, else the floor's (none for a floor without a template).
// A class from the config table is left as Compose wrote it. The hash is renewed when a role changed.
func MaterialRoles(p Plan, classes map[string]Class) Plan {
	out := cloneRevision(p)
	changed := false
	for i, is := range p.Issues {
		class, ok := classes[is.Class]
		if !ok || !class.fromCatalogue() {
			continue
		}
		roles := class.Roles
		if is.Profile != "" {
			pinned, offered := class.ProfileRoles[is.Profile]
			if !offered {
				continue // CheckPin refuses it; nothing to derive
			}
			roles = pinned
		}
		if !slices.Equal(roles, is.Roles) {
			out.Issues[i].Roles = slices.Clone(roles)
			changed = true
		}
	}
	if !changed {
		return p
	}
	out.Hash = Hash(out)
	return out
}

// lastRecord is the plan's highest D/A number: records form one sequence across its issues.
func lastRecord(p Plan) int {
	last := 0
	for _, is := range p.Issues {
		records, _ := sanitize.DecisionRecords(is.Body)
		for _, r := range records {
			last = max(last, r.Number)
		}
	}
	return last
}

func hasProfileRecord(body string) bool {
	records, _ := sanitize.DecisionRecords(body)
	for _, r := range records {
		if r.Gap == "" && strings.HasPrefix(r.Text, "profile: ") {
			return true
		}
	}
	return false
}
