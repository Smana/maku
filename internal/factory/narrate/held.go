// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"fmt"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// The replies to human input that arrives while it cannot apply. Each is keyed by the comment or
// review it answers, so a comment is answered once, and none quotes what the person wrote: only
// the factory's own command spelling, a login and ids.

// CommandHeld answers a maintainer's command posted before the task reached the phase it applies
// in: it is held, and applied once when the task reaches it, if still valid.
func CommandHeld(t *v1alpha1.Task, id int64, command, when string) Event {
	return Event{Key: fmt.Sprintf("held-%d", id), Class: ClassChanged, Body: fmt.Sprintf(
		"Agent factory task `%s`: held `%s`: it applies when the task %s. It is applied once, if still valid then, "+
			"and expires when the task ends or after %s; a newer `%s` replaces it.", t.Name, command, when, HeldFor, command)}
}

// PlanCommandNoted answers a plan command posted while no plan is under review: a plan command
// answers the plan it was posted on, so it is never applied to a later one. The next plan's
// review comment mentions it.
func PlanCommandNoted(t *v1alpha1.Task, id int64, command string) Event {
	return Event{Key: fmt.Sprintf("held-%d", id), Class: ClassChanged, Body: fmt.Sprintf(
		"Agent factory task `%s`: no plan is under review yet, so `%s` was not applied. A plan command answers the plan "+
			"it was posted on: the next plan's review comment will mention this one, and you answer that plan there.",
		t.Name, command)}
}

// CommandApplied says a held command was taken up now that the task reached its phase.
func CommandApplied(t *v1alpha1.Task, id int64, command string) Event {
	return Event{Key: fmt.Sprintf("held-applied-%d", id), Class: ClassChanged, Body: fmt.Sprintf(
		"Agent factory task `%s`: the held `%s` was acted on now that the task reached the phase it applies in.", t.Name, command)}
}

// CommandExpired says a held command was dropped without being applied, and why.
func CommandExpired(t *v1alpha1.Task, id int64, command, author, why string) Event {
	return Event{Key: fmt.Sprintf("held-expired-%d", id), Class: ClassNeedsYou, Body: fmt.Sprintf(
		"Agent factory task `%s`: the held `%s` was not applied: %s. @%s, post it again if it still applies.",
		t.Name, command, why, author)}
}

// CommandNotApplicable answers a maintainer's command this task can never act on.
func CommandNotApplicable(t *v1alpha1.Task, id int64, command, why string) Event {
	return Event{Key: fmt.Sprintf("not-applicable-%d", id), Class: ClassChanged, Body: fmt.Sprintf(
		"Agent factory task `%s`: `%s` was not acted on: %s.", t.Name, command, why)}
}

// NotActedOn answers a command or a review from someone the factory does not take input from.
// It says what was not acted on and why, never what they wrote (what is the factory's own words).
func NotActedOn(t *v1alpha1.Task, id int64, what string) Event {
	return Event{Key: fmt.Sprintf("not-acted-%d", id), Class: ClassChanged, Body: fmt.Sprintf(
		"Agent factory task `%s`: %s was not acted on: only this repository's maintainers with write access steer "+
			"the factory. A maintainer can post it.", t.Name, what)}
}

// HeldFor is how long a held command waits for its phase before it expires.
const HeldFor = "24 hours"
