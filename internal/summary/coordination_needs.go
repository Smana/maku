// SPDX-License-Identifier: Apache-2.0

package summary

import (
	"fmt"
)

// CoordinationNeeds is what needs/v1 reads of a filtered family: the parent, its plan version and
// primary, the flow's nodes (each child's pending confirmation) and the one recovery's code.
type CoordinationNeeds struct {
	ParentTask   string
	PlanVersion  int32
	Primary      string
	Flow         *CoordinationFlow
	RecoveryCode string
}

// coordinationNeeds is a coordination parent's own items (v4 MR5, MR11), from the viewer's filtered
// view: the parent confirmation and each child authorization the plan still waits for, and the one
// recovery the core proposes. They are the existing plan_confirmation and recovery kinds, anchored on
// the parent room's #coordination section, where alone they are decided. A child's merge, review or
// check need stays its own room's: nothing is derived for a child here, so no item is listed twice.
// The standing is GitHub's: a writer of the primary for the parent and the recovery, a writer of the
// child's repository for its authorization. A sponsored room read gets the link, never a decision.
func coordinationNeeds(in NeedsIn) []Need {
	v := in.Coordination
	if v == nil || v.Flow == nil {
		return nil
	}
	anchor := in.URL + "#coordination"
	ver := v.PlanVersion
	may := func(write bool) bool { return write || in.Caller.RoomScoped }
	action := func(kind, label string) NeedAction {
		switch {
		case in.Caller.RoomScoped:
			label = "open the parent room page to " + label
		case !in.Caller.WebUI:
			label = "open the room page to " + label
		default:
			label += " in the parent room"
		}
		return NeedAction{Kind: kind, Label: label, URL: anchor}
	}
	var out []Need
	parent := false // the plan's one parent confirmation, however many children wait on it
	for _, n := range v.Flow.Nodes {
		switch {
		case n.RequiredConfirmation == ConfirmParent && may(in.GitHub.MayWrite):
			if !parent {
				parent = true
				out = append(out, stamp(Need{Kind: NeedPlanConfirmation, ID: fmt.Sprintf("%s/parent/v%d", v.ParentTask, ver),
					What:   clipWhat(fmt.Sprintf("a maintainer of %s confirms coordination plan v%d", v.Primary, ver)),
					Action: action("decide", "confirm")}, in, NeedSource{Kind: "room", Reason: "coordination_parent"}))
			}
		case n.RequiredConfirmation == ConfirmChild && may(in.CoordinationStanding[n.Repository].MayWrite):
			out = append(out, stamp(Need{Kind: NeedPlanConfirmation, ID: fmt.Sprintf("%s/%s/v%d", v.ParentTask, n.ChildID, ver),
				What:   clipWhat(fmt.Sprintf("a maintainer of %s authorizes child %s of plan v%d", n.Repository, n.ChildID, ver)),
				Action: action("decide", "authorize")}, in, NeedSource{Kind: "room", Reason: "coordination_child"}))
		}
	}
	if code := v.RecoveryCode; code != "" && may(in.GitHub.MayWrite) {
		out = append(out, stamp(Need{Kind: NeedRecovery, ID: fmt.Sprintf("%s/%s/v%d", v.ParentTask, code, ver),
			Why:    code,
			What:   clipWhat(fmt.Sprintf("a maintainer of %s decides the proposed recovery %s", v.Primary, code)),
			Action: action("retry", "decide")}, in, NeedSource{Kind: "room", Reason: code}))
	}
	return out
}
