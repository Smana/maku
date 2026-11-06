// SPDX-License-Identifier: Apache-2.0

package summary

import (
	"strings"

	"github.com/Smana/maku/internal/envelope"
)

// The three failure causes a room's own log names, and the only three (fast-follow 6, CD12): a GitHub App
// missing a permission, a check the factory cannot read, and a CI log host outside the egress policy. They
// are codes, not sentences: Cause picks one from what the room already holds, and Fix is its one sentence,
// so a brief, a card and a hand-off comment cannot say the failure three ways.
const (
	// CauseAppPermission is the App's installation lacking the permission a call needs: gh answers 403.
	CauseAppPermission = "app_permission"
	// CauseCheckUnreadable is a check the factory waited on that never reported — a name `merge.checks`
	// carries that the App cannot read, or does not carry at all.
	CauseCheckUnreadable = "check_unreadable"
	// CauseCILogEgress is a CI log whose host the sandbox's egress policy does not allow.
	CauseCILogEgress = "ci_log_egress"
)

// The two signatures a note carries, as the platform writes them. Both are fixed phrases inside a longer
// sentence the agent's harness writes, never the whole note: the harness's LOG_UNREADABLE
// (images/maku-harness/agent_run.py) continues after its first clause, and gh's own 403 is the message of a
// failed call the agent quoted.
const (
	ghForbidden   = "Resource not accessible by integration"
	logUnreadable = "the CI log is not readable from the sandbox"
)

// causeCIWaitExpired is the factory's reason for a check it waited on that never reported (reconciler's
// merge.go): the one reason that is a configuration failure rather than a red check.
const causeCIWaitExpired = "ci_wait_expired"

// Cause is the one failure f and the room's notes name, and whether they name one (fast-follow 6): "" and
// false when the room holds nothing to act on, so a surface prints no fix rather than a guess. It reads what
// the caller already has — the facts, and the fold's notes — so it costs no read, and it never returns a
// note's text: only a code from the table, so an agent that writes one of the platform's sentences can at
// most make the room print the broker's own words.
//
// The precedence is the room's own: the newest evidence first, and a permission before anything else. The
// App's 403 explains a check the App could not see, so it outranks the facts' reason; a log the sandbox
// could not fetch does not, because the factory's merge path never reads a job log — a wait that expired for
// another reason is still that reason.
func Cause(f *envelope.TaskFacts, notes []Note) (string, bool) {
	newest := func(signature string) bool {
		for i := len(notes) - 1; i >= 0; i-- {
			if strings.Contains(notes[i].Text, signature) {
				return true
			}
		}
		return false
	}
	switch {
	case newest(ghForbidden):
		return CauseAppPermission, true
	case f != nil && f.Reason == causeCIWaitExpired:
		return CauseCheckUnreadable, true
	case newest(logUnreadable):
		return CauseCILogEgress, true
	}
	return "", false
}

// Fix is a cause's one sentence: what to change and where, naming the file or the host a person edits. A
// cause this package does not know has none — "" — so a surface prints no line rather than a guess.
func Fix(cause string) string { return fixes()[cause] }

// fixes is the table both the brief and the card read (fast-follow 6): each fix names the thing the person
// must change, and says what the alternative is where one exists.
func fixes() map[string]string {
	return map[string]string{
		CauseAppPermission:   "the factory App is missing a permission on this repository: read the 403's scope in the run's note and grant it",
		CauseCheckUnreadable: "the factory cannot read that check: `.github/agent-factory.yml`'s `merge.checks` must name a check the App may read",
		CauseCILogEgress:     "the CI log's host is outside the factory's egress policy: quote the failing check's annotations instead, or add the host",
	}
}
