// SPDX-License-Identifier: Apache-2.0

package summary_test

import (
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

func TestPolicyHow(t *testing.T) {
	const rule = " " + summary.GatePathRule
	for _, tc := range []struct {
		name string
		p    envelope.PolicyFact
		want string
	}{
		{"gate off", envelope.PolicyFact{Mode: envelope.PolicyLive, GateOff: true, ApproveToMerge: true},
			"A maintainer merges it: no merge class is live or in shadow, so the factory merges nothing." + rule},
		{"live, approve on", envelope.PolicyFact{Mode: envelope.PolicyLive, ApproveToMerge: true},
			"The factory merges it once the class's conditions hold, or when a maintainer approves the current commit: the merger App merges that commit." + rule},
		{"live, approve off", envelope.PolicyFact{Mode: envelope.PolicyLive},
			"The factory merges it once the class's conditions hold; otherwise a maintainer merges it." + rule},
		{"shadow, approve on", envelope.PolicyFact{Mode: envelope.PolicyShadow, ApproveToMerge: true},
			"The gate decides it as if live but never merges it (shadow): a maintainer's approval of the current commit merges it through the merger App." + rule},
		{"shadow, approve off", envelope.PolicyFact{Mode: envelope.PolicyShadow},
			"The gate decides it as if live but never merges it (shadow): a maintainer merges it." + rule},
		{"unarmed, approve on", envelope.PolicyFact{Mode: envelope.PolicyUnarmed, ApproveToMerge: true},
			"A maintainer's approval of the current commit merges it through the merger App." + rule},
		{"unarmed, approve off", envelope.PolicyFact{Mode: envelope.PolicyUnarmed},
			"The factory never merges this class: a maintainer reviews and merges it." + rule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := summary.PolicyHow(tc.p); got != tc.want {
				t.Fatalf("PolicyHow() =\n %q\nwant\n %q", got, tc.want)
			}
		})
	}
}

func TestPolicyDecision(t *testing.T) {
	pr := func(by string) *envelope.PRFact {
		return &envelope.PRFact{Number: 3, URL: "https://github.com/o/r/pull/3", ApprovedBy: by}
	}
	for _, tc := range []struct {
		name string
		f    envelope.TaskFacts
		want string
	}{
		{"the shadow decision", envelope.TaskFacts{Phase: "AwaitingHuman", Reason: "shadow_would_arm", PR: pr(""), Policy: &envelope.PolicyFact{Mode: envelope.PolicyShadow}},
			"Waits for a human: it would auto-merge, and the merge gate is in shadow"},
		{"a gate path", envelope.TaskFacts{Phase: "AwaitingHuman", Reason: "gate_path", PR: pr(""), Policy: &envelope.PolicyFact{Mode: envelope.PolicyLive}},
			"Waits for a human: it touches a gate path, so an agent can never merge it"},
		{"merged on a named approval", envelope.TaskFacts{Phase: "Merged", PR: pr("alice"), Policy: &envelope.PolicyFact{Mode: envelope.PolicyLive, MergedHow: "approved"}},
			"merged by the merger App on @alice's approval"},
		{"merged on an approval with no login", envelope.TaskFacts{Phase: "Merged", PR: pr(""), Policy: &envelope.PolicyFact{Mode: envelope.PolicyLive, MergedHow: "approved"}},
			"merged by the merger App on a maintainer's approval"},
		{"a running task has none", envelope.TaskFacts{Phase: "Implementing", PR: pr(""), Policy: &envelope.PolicyFact{Mode: envelope.PolicyLive}}, ""},
		{"an unknown reason has none", envelope.TaskFacts{Phase: "AwaitingHuman", Reason: "no_such_reason", PR: pr(""), Policy: &envelope.PolicyFact{Mode: envelope.PolicyLive}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := summary.PolicyDecision(&tc.f); got != tc.want {
				t.Fatalf("PolicyDecision() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The decision names its policy (merge policies review): the policy the diff matched once the
// merger merged under it, the reason once a human holds it, and nothing before a pull request.
func TestPolicyDecisionNamesThePolicy(t *testing.T) {
	pr := &envelope.PRFact{Number: 3, URL: "https://github.com/o/r/pull/3"}
	p := func(how string) *envelope.PolicyFact {
		return &envelope.PolicyFact{Class: "review", Name: "docs-links", Mode: envelope.PolicyLive, MergedHow: how}
	}
	for _, tc := range []struct {
		name string
		f    envelope.TaskFacts
		want string
	}{
		{"merged", envelope.TaskFacts{Phase: "Merged", PR: pr, Policy: p("class")}, "Merged automatically by policy docs-links"},
		{"capped", envelope.TaskFacts{Phase: "AwaitingHuman", Reason: "auto_merge_cap", PR: pr, Policy: p("")},
			"Waits for a human: today's auto-merge cap is reached"},
		{"no pull request", envelope.TaskFacts{Phase: "AwaitingHuman", Reason: "auto_merge_cap", Policy: p("")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := summary.PolicyDecision(&tc.f); got != tc.want {
				t.Fatalf("PolicyDecision() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestViewCarriesThePolicy(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	p := &envelope.PolicyFact{Class: "docs-links", Mode: envelope.PolicyShadow,
		Classes: []envelope.PolicyClass{{Name: "docs-links", Mode: envelope.PolicyShadow}}}
	facts := &envelope.TaskFacts{Phase: "AwaitingHuman", Reason: "shadow_would_arm", Policy: p,
		PR: &envelope.PRFact{Number: 3, URL: "https://github.com/o/r/pull/3"}}
	for _, tc := range []struct {
		name   string
		sealed string
	}{{"an open room", ""}, {"a sealed room keeps the policy it ended under", "Sealed"}} {
		t.Run(tc.name, func(t *testing.T) {
			s := view(summary.State{Facts: facts, Sealed: tc.sealed}, "3kq7x2ma", "u", owner(), 0, now)
			got := s.Status.Policy
			if got == nil || got.Class != "docs-links" || got.How != summary.PolicyHow(*p) ||
				got.Decision != "Waits for a human: it would auto-merge, and the merge gate is in shadow" {
				t.Fatalf("status.policy = %+v", got)
			}
		})
	}
	if s := view(summary.State{Facts: &envelope.TaskFacts{Phase: "Implementing"}}, "3kq7x2ma", "u", owner(), 0, now); s.Status.Policy != nil {
		t.Fatalf("status.policy without a policy fact = %+v", s.Status.Policy)
	}
}
