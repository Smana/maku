// SPDX-License-Identifier: Apache-2.0

package webhook

import "slices"

// notify is CD11's table: the transitions a person is told about, and the ones that are only log.
// Naming a type false is a decision, not an omission — a subscriber that declared it would still
// receive it, but nothing renders a line for it: room.run_started is a run beginning (the
// interesting moment is its end, or the red check that ends it), room.task_received and
// room.run_ended are bookkeeping, and room.monitor_verdict repeats the verdict the task's own
// transition already carries. The nine trues are what this package's vocabulary holds, so Notify is
// safe to copy into a subscriber's events: — room.approval_closed included, which a channel with
// approvals: true must declare or its card stays live past the decision.
var notify = map[string]bool{
	"room.task_triaged":      true,
	"room.run_started":       false,
	"room.waiting_for_human": true,
	"room.pr_opened":         true,
	"room.verdict_posted":    true,
	"room.merged":            true,
	"room.reverted":          true,
	"room.budget_reached":    true,
	"room.ended":             true,
	"room.task_received":     false,
	"room.run_ended":         false,
	"room.monitor_verdict":   false,
	"room.approval_closed":   true,
}

// Meaningful reports whether a person is told about a transition (CD11: notifications are
// meaningful transitions only). An unlisted transition is not news: the answer is false, so a type
// added to the vocabulary without a line here stays silent rather than reaching someone.
func Meaningful(t string) bool { return notify[t] }

// Notify lists, sorted, the transitions a person is told about: what a renderer asks before posting
// a line, and the list a subscriber's events: is copied from. Every name in it is in Types, so a
// declaration built from it is accepted by the broker.
func Notify() []string {
	out := make([]string, 0, len(notify))
	for t, meaningful := range notify {
		if meaningful {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}
