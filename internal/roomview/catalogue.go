// SPDX-License-Identifier: Apache-2.0

package roomview

import "github.com/Smana/maku/internal/factory/catalogue"

// MergeWords is how a task of the class merges, in the broker's words.
func MergeWords(outcome string) string {
	switch outcome {
	case catalogue.OutcomeAuto:
		return "merges on its own when the gate's conditions hold"
	case catalogue.OutcomeShadow:
		return "a maintainer merges; the factory records whether it would have"
	case catalogue.OutcomeApproval:
		return "a maintainer's approval merges it"
	}
	return "a maintainer merges it"
}
