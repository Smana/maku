// SPDX-License-Identifier: Apache-2.0

package summary_test

import (
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

// Fast-follow 6: the three failures the platform meets most, from what the room already holds.
func TestCause(t *testing.T) {
	for _, c := range []struct {
		name  string
		facts *envelope.TaskFacts
		notes []summary.Note
		want  string
	}{
		{"a check that never reported", &envelope.TaskFacts{Phase: "AwaitingCI", Reason: "ci_wait_expired"}, nil, "check_unreadable"},
		{"a log outside the egress policy", &envelope.TaskFacts{Phase: "AwaitingHuman"},
			[]summary.Note{{Text: "the CI log is not readable from the sandbox: do not try to fetch it"}}, "ci_log_egress"},
		{"an App missing a permission", &envelope.TaskFacts{Phase: "Escalated", Reason: "agent_error"},
			[]summary.Note{{Text: "gh: Resource not accessible by integration (HTTP 403)"}}, "app_permission"},
		{"nothing to say", &envelope.TaskFacts{Phase: "AwaitingHuman"}, nil, ""},
	} {
		got, _ := summary.Cause(c.facts, c.notes)
		if got != c.want {
			t.Errorf("%s: %q", c.name, got)
		}
	}
}

// Each cause has one fix, and it names the file or the host the person must change.
func TestFix(t *testing.T) {
	for cause, want := range map[string]string{
		"app_permission":   "the factory App is missing a permission on this repository: read the 403's scope in the run's note and grant it",
		"check_unreadable": "the factory cannot read that check: `.github/agent-factory.yml`'s `merge.checks` must name a check the App may read",
		"ci_log_egress":    "the CI log's host is outside the factory's egress policy: quote the failing check's annotations instead, or add the host",
	} {
		if got := summary.Fix(cause); got != want {
			t.Errorf("%s: %q", cause, got)
		}
	}
}

// The precedence the three rules are read in: the App's own 403 explains a check the App could not see, so it
// outranks the facts' reason; a log the sandbox could not fetch does not, because the factory's merge path
// never reads a job log — a wait that expired for another reason is still that reason.
func TestCausePrefersTheAppPermission(t *testing.T) {
	expired := &envelope.TaskFacts{Phase: "AwaitingHuman", Reason: "ci_wait_expired"}
	for _, c := range []struct {
		name  string
		facts *envelope.TaskFacts
		notes []summary.Note
		want  string
	}{
		{"the 403 explains the check", expired,
			[]summary.Note{{Text: "gh: Resource not accessible by integration (HTTP 403)"}}, summary.CauseAppPermission},
		{"the log host explains nothing about the wait", expired,
			[]summary.Note{{Text: "the CI log is not readable from the sandbox: do not try to fetch it"}}, summary.CauseCheckUnreadable},
		{"the note is not one of the three", expired,
			[]summary.Note{{Text: "the implementer pushed a fix; CI is running"}}, summary.CauseCheckUnreadable},
		{"no facts, no note", nil, nil, ""},
	} {
		got, ok := summary.Cause(c.facts, c.notes)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: %q %t", c.name, got, ok)
		}
	}
}

// A cause the table does not know has no sentence: a brief prints no Fix line rather than a guess.
func TestFixOfAnUnknownCause(t *testing.T) {
	for _, cause := range []string{"", "no_such_cause"} {
		if got := summary.Fix(cause); got != "" {
			t.Errorf("%q: %q", cause, got)
		}
	}
}
