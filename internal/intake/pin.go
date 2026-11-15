// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"github.com/Smana/maku/internal/factory/catalogue"
)

// CheckPin is intake's early check of a pinned workflow (P6), against the plan repository's
// catalogue, in the order triage checks it (triage.ResolvePin): who pins, the workflow, then
// whether intake can show the workflow's roles. Nil when the pin holds or there is none. Since the
// workflow left the class (WF4), every class may run every workflow: the class is only named.
//
// A workflow the catalogue does not list is unknown here: the catalogue lists every workflow a pin
// could name.
func CheckPin(c catalogue.Catalogue, class, profile string) *ProfileRefusal {
	if profile == "" {
		return nil
	}
	r := &ProfileRefusal{Class: class, Profile: profile}
	p, known := c.Profiles[profile]
	switch {
	case !c.Maintainer:
		r.Code = NotMaintainer
	case !known:
		r.Code = ProfileUnknown
	case !p.Offered:
		r.Code = ProfileNotOffered
	default:
		return nil
	}
	return r
}
