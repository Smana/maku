// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Comment is one comment of a pull request, as the timeline reads it.
type Comment struct {
	ID     int64
	Author string
	Body   string
	At     time.Time
}

// Step is one row of a decision's timeline: passed, refused (the row that decided), not_reached
// (after the row that decided), or recorded (a reading the record holds).
type Step struct {
	Row    string `json:"row"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Decision is one decision record rendered as a timeline (the spec's "Decision timeline").
type Decision struct {
	At      time.Time `json:"at"`
	Head    string    `json:"head"`
	Class   string    `json:"class"`
	Verdict string    `json:"verdict"`
	Reason  string    `json:"reason"`
	Steps   []Step    `json:"steps"`
	// Revisions are what the decision was bound to (MP12), as far as its record holds them.
	Revisions []Step `json:"revisions"`
	// CommentURL is the PR comment that holds the record; PolicyURL the policy file's history.
	CommentURL string `json:"commentURL"`
	PolicyURL  string `json:"policyURL"`
}

// Timeline is a pull request's decisions, oldest first.
type Timeline struct {
	Repository  string     `json:"repository"`
	PullRequest int        `json:"pullRequest"`
	Decisions   []Decision `json:"decisions"`
}

var (
	record = regexp.MustCompile(`agent-factory-decision head=([^ ]+) class=([^ ]+) decision=([a-z_]+):([a-z_]+) (.*?) -->`)
	field  = regexp.MustCompile(`([a-z]+)=([^ ]+)`)
	event  = regexp.MustCompile(`event=human-([a-z_]+)-[0-9]+`)
)

// The gate's stages, in order (the spec's resolution order), and the reasons each refuses with.
var (
	stages       = []string{"fixed_base", "author_mode", "class", "deny", "requires_approval", "grant", "breaker_and_caps"}
	stageReasons = map[string][]string{
		"fixed_base": {"ci_red", "secret_scan_red", "ci_cancelled", "changes_requested", "ci_pending", "ci_wait_expired",
			"gate_path", "steering_path", "not_owned", "policy_pending", "policy_absent", "no_approving_verdict", "not_mergeable",
			"head_unreported", "foreign_trailer", "human_commit_in_pr", "push_unrecorded", "closes_parent", "verdict_stale",
			"merge_gate_off", "head_moved", "approval_stale"},
		"author_mode": {"not_agent_authored", "fork_pull_request", "adoption_unread", "not_adopted_author", "branch_not_allowed",
			"label_not_adopted", "adopt_ref_invalid"},
		"class": {"class_mismatch", "class_unarmed", "file_not_allowed", "file_added_or_deleted", "patch_too_large", "holdout_red",
			"holdout_pending", "oracle_finding_open", "oracle_unrequested", "release_age_pending", "release_age_red", "label_missing"},
		"deny":              {"file_protected"},
		"requires_approval": {"approval_required", "shadow_would_require_approval"},
		"breaker_and_caps":  {"class_paused", "auto_merge_cap", "ledger_truncated"},
	}
)

// stageOf is the stage a reason refuses at, "" for none (an arm, a shadow, a maintainer's approval).
func stageOf(reason string) string {
	for st, rs := range stageReasons {
		if slices.Contains(rs, reason) {
			return st
		}
	}
	return ""
}

// Decisions reads every decision record the factory wrote on a pull request: only factory's own
// comments count, as for the tally. A hand-off's record is a hand-off whatever decision= says.
func Decisions(repository string, pr int, factory string, comments []Comment) Timeline {
	t := Timeline{Repository: repository, PullRequest: pr, Decisions: []Decision{}}
	mine := func(a string) bool {
		n := func(s string) string {
			return strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(s), "app/"), "[bot]")
		}
		return n(a) == n(factory)
	}
	sort.SliceStable(comments, func(i, j int) bool { return comments[i].At.Before(comments[j].At) })
	for _, c := range comments {
		m := record.FindStringSubmatch(c.Body)
		if m == nil || !mine(c.Author) {
			continue
		}
		d := Decision{At: c.At, Head: m[1], Class: m[2], Verdict: m[3], Reason: m[4],
			CommentURL: fmt.Sprintf("https://github.com/%s/pull/%d#issuecomment-%d", repository, pr, c.ID),
			PolicyURL:  "https://github.com/" + repository + "/commits/HEAD/.github/agent-factory.yaml"}
		if h := event.FindStringSubmatch(c.Body); h != nil {
			d.Verdict, d.Reason = "human", h[1]
		}
		rows := map[string]string{}
		for _, f := range field.FindAllStringSubmatch(m[5], -1) {
			rows[f[1]] = f[2]
		}
		d.Steps, d.Revisions = steps(d, rows), revisions(rows)
		t.Decisions = append(t.Decisions, d)
	}
	return t
}

// steps walks the stages in order: each before the deciding one passed, that one refused, each
// after it not reached; an arm or a shadow decision passed them all. The class's own readings
// are the record's.
func steps(d Decision, rows map[string]string) []Step {
	decided, refused := stageOf(d.Reason), d.Verdict == "human"
	at := len(stages)
	for i, s := range stages {
		if refused && s == decided {
			at = i
		}
	}
	var out []Step
	for i, s := range stages {
		st := Step{Row: s, Status: "passed"}
		switch {
		case i == at:
			st.Status, st.Detail = "refused", d.Reason
		case i > at:
			st.Status = "not_reached"
		}
		if s == "grant" && st.Status == "passed" { // stage 5: the grant a decision armed on, or none
			st.Status, st.Detail = "recorded", "no grant used"
			if id, version, ok := strings.Cut(rows["grant"], ":"); ok {
				st.Detail = "grant " + id + ", version " + version
			}
		}
		if s == "author_mode" {
			st.Detail = strings.TrimSpace(st.Detail + " " + "class " + d.Class)
		}
		if s == "breaker_and_caps" && st.Status != "not_reached" {
			st.Detail = strings.TrimSpace(st.Detail + fmt.Sprintf(" paused=%s capped=%s", dash(rows["paused"]), dash(rows["capped"])))
		}
		out = append(out, st)
		if s == "class" {
			for _, r := range []struct{ key, label string }{{"files", "files"}, {"lines", "modified lines"}, {"shape", "diff shape"},
				{"holdout", "holdout checks"}, {"oracle", "oracle"}, {"age", "release age"}, {"evidence", "evidence"}} {
				out = append(out, Step{Row: "condition: " + r.label, Status: "recorded", Detail: dash(rows[r.key])})
			}
		}
	}
	return append(out, Step{Row: "verdict", Status: d.Verdict, Detail: d.Reason})
}

// revisions are the inputs the decision is bound to, as its record holds them.
func revisions(rows map[string]string) []Step {
	policy := "not recorded: neither dual evaluation nor repository authority was on"
	switch p := rows["policy"]; {
	case p != "" && rows["dual"] != "":
		policy = "sha256 " + p + ", dual=" + rows["dual"]
	case p != "": // repository authority (stage 4): policy= alone is the policy that decided
		policy = "sha256 " + p + ", the policy that decided"
	}
	grant := rows["grant"]
	if grant == "" {
		grant = "none"
	}
	return []Step{
		{Row: "repository policy", Status: "recorded", Detail: policy},
		{Row: "guardrails and harness profiles", Status: "recorded", Detail: "the factory config the pod ran, bound by its config checksum; not in the record"},
		{Row: "reviewer", Status: "recorded", Detail: dash(rows["reviewer"])},
		{Row: "grant version", Status: "recorded", Detail: grant},
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
