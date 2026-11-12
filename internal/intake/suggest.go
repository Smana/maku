// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/factory/catalogue"
)

// ClassesFromCatalogue is a repository's offered classes as the planner reads them (P2, G4): each
// class's ceiling, its offered profiles and their roles, its mode and the repository's
// approveToMerge, all from the factory. The material roles are the floor's template, or none when
// the floor sets no template: triage chooses them (G3, P15).
func ClassesFromCatalogue(c catalogue.Catalogue) map[string]Class {
	out := make(map[string]Class, len(c.Classes))
	for _, cl := range c.Classes {
		offered := c.OfferedProfiles(cl.Name)
		roles := make(map[string][]string, len(offered))
		for _, name := range offered {
			roles[name] = slices.Clone(c.Profiles[name].Roles)
		}
		out[cl.Name] = Class{
			Roles:          slices.Clone(roles[cl.Floor]),
			CeilingUSD:     cl.CeilingUSD,
			Source:         "catalogue:" + cl.Name + ".ceilingUSD",
			Description:    cl.Description,
			Mode:           cl.Mode,
			Floor:          cl.Floor,
			Offered:        offered,
			ProfileRoles:   roles,
			ApproveToMerge: c.ApproveToMerge,
		}
	}
	return out
}

// ClassesFor is the class table a plan for repository is held to: its catalogue's classes when
// the broker read them, else the config's table (until stage 5). The key is the lowercase name.
func ClassesFor(offers map[string]map[string]Class, fallback map[string]Class, repository string) map[string]Class {
	if c, ok := offers[strings.ToLower(repository)]; ok {
		return c
	}
	return fallback
}

// fromCatalogue is whether the class came from the factory's catalogue: only such a class carries a
// suggestion, since a config row knows no profiles and no merge mode.
func (c Class) fromCatalogue() bool { return c.Mode != "" }

// suggest is issue index's suggestion under class, from the reply's ds: the profile when the class
// offers it, else the factory's choice, the merge outcome the broker computes, and the why that
// explains the profile kept. Every value the reply named that the broker owns is added to dropped.
func suggest(class Class, ds *DraftSuggestion, index int, dropped *[]string) *Suggestion {
	if !class.fromCatalogue() {
		if ds != nil {
			*dropped = append(*dropped, fmt.Sprintf("suggestion@%d", index))
		}
		return nil
	}
	s := &Suggestion{Merge: MergeShown{Outcome: catalogue.Outcome(class.Mode, class.ApproveToMerge), ApproveToMerge: class.ApproveToMerge}}
	if ds == nil {
		return s
	}
	if len(ds.Merge) > 0 {
		*dropped = append(*dropped, fmt.Sprintf("suggestion.merge@%d", index))
	}
	switch {
	case ds.Profile == "":
	case slices.Contains(class.Offered, ds.Profile):
		s.Profile, s.Roles, s.Why = ds.Profile, slices.Clone(class.ProfileRoles[ds.Profile]), strings.TrimSpace(ds.Why)
	default:
		// A why for a profile the class does not offer explains nothing the person will see.
		*dropped = append(*dropped, fmt.Sprintf("suggestion.profile@%d", index))
	}
	return s
}

// Resuggest is p with every issue's suggestion rebuilt from classes, the plan repository's table
// (ClassesFor): a suggestion read back from a client is the client's, so a consumer never shows,
// stores or files one the broker did not derive. An offered profile and its why are kept, the roles
// and the merge outcome recomputed; anything else is the factory's choice, and a class from the
// config table carries none. The suggestion is display: the hash is unchanged.
func Resuggest(p Plan, classes map[string]Class) Plan {
	issues := make([]Issue, len(p.Issues))
	var dropped []string
	for i, is := range p.Issues {
		var ds *DraftSuggestion
		if s := is.Suggestion; s != nil {
			ds = &DraftSuggestion{Profile: s.Profile, Why: s.Why}
		}
		class, ok := classes[is.Class]
		is.Suggestion = nil
		if ok {
			is.Suggestion = suggest(class, ds, is.Index, &dropped)
		}
		issues[i] = is
	}
	p.Issues = issues
	return p
}
