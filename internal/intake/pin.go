// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"slices"

	"github.com/Smana/maku/internal/factory/catalogue"
)

// CheckPin is intake's early check of a pinned profile (P6), against the plan repository's
// catalogue, in the order triage checks it (triage.ResolvePin): who pins, the profile, the class's
// profiles, its mode, the floor and allowed list, then whether intake can show the profile's roles
// and whether its tests-first run fits the task's data. Nil when the pin holds or there is none.
// The tests-paths check is triage's alone: intake reads no repository.
//
// A profile the catalogue does not list is unknown here: the catalogue lists the profiles of the
// repository's offered classes, which is every profile a pin there could name.
func CheckPin(c catalogue.Catalogue, class, profile string, public bool) *ProfileRefusal {
	if profile == "" {
		return nil
	}
	cl, _ := c.Class(class)
	r := &ProfileRefusal{Class: class, Profile: profile, Floor: cl.Floor, Mode: cl.Mode, Allowed: slices.Clone(cl.Allowed)}
	p, known := c.Profiles[profile]
	switch {
	case !c.Maintainer:
		r.Code = NotMaintainer
	case !known:
		r.Code = ProfileUnknown
	case cl.Floor == "":
		r.Code = ClassNoProfiles
	case cl.Mode == catalogue.ModeLive || cl.Mode == catalogue.ModeShadow:
		r.Code = ClassProfileFixed
	case profile != cl.Floor && !slices.Contains(cl.Allowed, profile):
		r.Code = ProfileNotAllowed
	case !p.Offered:
		r.Code = ProfileNotOffered
	case p.TestFirst && !public:
		r.Code = ProfileInternal
	default:
		return nil
	}
	return r
}
