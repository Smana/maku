// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"slices"
	"strings"
)

// Upgrade is a plan/v1 shown again as plan/v2 (P14, G2): deterministic, with no model call, so
// neither plan_check's contract nor the requester's daily plans are spent. The issues keep their
// title, body, class and dependsOn; each gains `profile: ""` (the factory's choice) and, when classes
// came from the catalogue, the suggestion of the factory's choice with an empty why. The roles and
// the ceiling are re-derived from classes, as Compose derives them; a channel ceiling the v1 plan
// was lowered to stays when it is still the lower. The new plan carries its new hash and is shown,
// never filed: the person confirms that one.
//
// classes is the plan repository's table (ClassesFor). A class it no longer holds is refused
// `invalid_class`, and a plan that is not plan/v1 is refused `planner_invalid`.
func Upgrade(v1 Plan, classes map[string]Class) (Plan, *Refusal) {
	if v1.APIVersion != APIVersionV1 {
		return Plan{}, refuse(ReasonPlannerInvalid, "apiVersion "+v1.APIVersion+", only "+APIVersionV1+" is upgraded")
	}
	p := v1
	p.APIVersion = APIVersion
	p.Issues = make([]Issue, 0, len(v1.Issues))
	chosen := make([]Class, 0, len(v1.Issues))
	var dropped []string
	for _, is := range v1.Issues {
		class, ok := classes[is.Class]
		if !ok {
			return Plan{}, invalidClass(is.Index, is.Class)
		}
		chosen = append(chosen, class)
		is.Profile = ""
		is.Roles = slices.Clone(class.Roles)
		is.DependsOn = slices.Clone(is.DependsOn)
		is.Suggestion = suggest(class, nil, is.Index, &dropped)
		p.Issues = append(p.Issues, is)
	}
	ceiling, source := sumCeilings(chosen)
	if strings.HasPrefix(v1.Budget.Source, "channel:") && v1.Budget.CeilingUSD > 0 && v1.Budget.CeilingUSD < ceiling {
		ceiling, source = v1.Budget.CeilingUSD, v1.Budget.Source
	}
	p.Budget = Budget{CeilingUSD: ceiling, Source: source}
	p.Hash = Hash(p)
	return p, nil
}
