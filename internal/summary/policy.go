// SPDX-License-Identifier: Apache-2.0

package summary

import (
	"fmt"

	"github.com/Smana/maku/internal/envelope"
)

// GatePathRule ends every PolicyHow: gate paths are the repository's policy-bot rules, which the
// factory does not read, so the rule is stated rather than the paths listed (merge policy view D7).
const GatePathRule = "A pull request that touches a gate path always needs a human merge."

// PolicyView is status.policy: the factory's policy fact, what it takes to merge under it, and the
// gate's decision on this task when there is one.
type PolicyView struct {
	envelope.PolicyFact
	How      string `json:"how"`
	Decision string `json:"decision,omitempty"`
}

// PolicyHow is what it takes to merge a pull request under p, the gate's rules in one sentence
// (merge policy view D5): the class's own conditions when live, a maintainer's approval through the
// merger App under approveToMerge, a maintainer's merge otherwise.
func PolicyHow(p envelope.PolicyFact) string {
	var s string
	switch {
	case p.GateOff:
		s = "A maintainer merges it: no merge class is live or in shadow, so the factory merges nothing."
	case p.Mode == envelope.PolicyLive && p.ApproveToMerge:
		s = "The factory merges it once the class's conditions hold, or when a maintainer approves the current commit: the merger App merges that commit."
	case p.Mode == envelope.PolicyLive:
		s = "The factory merges it once the class's conditions hold; otherwise a maintainer merges it."
	case p.Mode == envelope.PolicyShadow && p.ApproveToMerge:
		s = "The gate decides it as if live but never merges it (shadow): a maintainer's approval of the current commit merges it through the merger App."
	case p.Mode == envelope.PolicyShadow:
		s = "The gate decides it as if live but never merges it (shadow): a maintainer merges it."
	case p.ApproveToMerge:
		s = "A maintainer's approval of the current commit merges it through the merger App."
	default:
		s = "The factory never merges this class: a maintainer reviews and merges it."
	}
	return s + " " + GatePathRule
}

// PolicyDecision is the gate's word on the task (D6): how the merger merged it, else the reason of
// an AwaitingHuman hand-off (shadow_would_arm is the shadow decision), else "".
func PolicyDecision(f *envelope.TaskFacts) string {
	if f == nil || f.Policy == nil {
		return ""
	}
	switch f.Policy.MergedHow {
	case "class":
		return "merged by the merger App: the class's conditions held"
	case "approved":
		who := "a maintainer's"
		if f.PR != nil && envelope.ValidLogin(f.PR.ApprovedBy) {
			who = fmt.Sprintf("@%s's", f.PR.ApprovedBy)
		}
		return "merged by the merger App on " + who + " approval"
	}
	if f.Phase != "AwaitingHuman" {
		return ""
	}
	return reasons()[f.Reason]
}
