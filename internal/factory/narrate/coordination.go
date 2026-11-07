// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// The coordination issue labels (v4 Issues and confirmations): output only. A parent issue never
// carries factory/ready, a class label or a split label; a child's never carries factory/child-of.
const (
	LabelCoordination      = "factory/coordination"
	LabelCoordinationChild = "factory/coordination-child"
)

// ClassLabel is the class label a child issue carries for people: class:<class>.
func ClassLabel(class string) string { return "class:" + class }

// CoordinationParentMarker is a parent issue's first line: what replay adopts it by, beside its
// factory author.
func CoordinationParentMarker(task string, version int32, hash string) string {
	return fmt.Sprintf("<!-- maku-coordination parent=%s version=%d sha256=%s -->", task, version, hash)
}

// CoordinationChildMarker is a child issue's first line.
func CoordinationChildMarker(task, child string, version int32) string {
	return fmt.Sprintf("<!-- maku-coordination-child parent=%s child=%s version=%d -->", task, child, version)
}

// CoordinationRepostMarker is a plan repost comment's first line: one comment per (parent,
// version, hash).
func CoordinationRepostMarker(task string, version int32, hash string) string {
	return fmt.Sprintf("<!-- maku-coordination-repost parent=%s version=%d sha256=%s -->", task, version, hash)
}

var (
	parentMarkerRE = regexp.MustCompile(`^<!-- maku-coordination parent=([a-z2-7]{8}) version=[0-9]+ sha256=[0-9a-f]{64} -->`)
	childMarkerRE  = regexp.MustCompile(`^<!-- maku-coordination-child parent=([a-z2-7]{8}) child=([a-z][a-z0-9-]{0,31}) version=[0-9]+ -->`)
)

// CoordinationParentOf is the parent task a body's first-line parent marker names. A marker
// anywhere else, as plan text quoted inside the body, names nothing; the caller still checks the
// issue's author and the coordination Task.
func CoordinationParentOf(body string) (string, bool) {
	m := parentMarkerRE.FindStringSubmatch(body)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// CoordinationChildOf is the parent task and child id a body's first-line child marker names.
func CoordinationChildOf(body string) (task, child string, ok bool) {
	m := childMarkerRE.FindStringSubmatch(body)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// inert fences text as a code block no backtick run inside it can close.
func inert(lang, text string) string {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	return fence + lang + "\n" + text + "\n" + fence
}

// CoordinationParentIssue is the parent issue the factory files in the primary repository: its
// marker, its children by id and repository, and the full plan as an inert JSON block. Plan text
// is only ever data in that block.
func CoordinationParentIssue(task string, p v1alpha1.CoordinationPlan, hash string) (title, body string) {
	var b strings.Builder
	b.WriteString(CoordinationParentMarker(task, p.Version, hash) + "\n")
	fmt.Fprintf(&b, "The agent factory coordinates one change across %d repositories as task `%s`, plan version %d (sha256 `%s`).\n\n", len(p.Children), task, p.Version, hash)
	b.WriteString("Each child is its own issue, run and pull request in its own repository, under that repository's maintainer and gate. This issue runs nothing and merges nothing.\n\n")
	for _, c := range p.Children {
		fmt.Fprintf(&b, "- `%s` in %s\n", c.ID, c.Repository)
	}
	plan, _ := json.MarshalIndent(p, "", "  ") // typed plan data: Marshal cannot fail
	b.WriteString("\nThe confirmed plan, as data:\n\n" + inert("json", string(plan)) + "\n")
	return fmt.Sprintf("Coordination %s: plan version %d", task, p.Version), b.String()
}

// CoordinationChildIssue is child c's issue in its own repository: its marker, its parent task,
// class and the bounded scope as an inert block.
func CoordinationChildIssue(task string, p v1alpha1.CoordinationPlan, c v1alpha1.CoordinationChild) (title, body string) {
	var b strings.Builder
	b.WriteString(CoordinationChildMarker(task, c.ID, p.Version) + "\n")
	fmt.Fprintf(&b, "Child `%s` of coordination `%s` (plan version %d), class `%s`, tier `%s`.\n\n", c.ID, task, p.Version, c.Class, c.Tier)
	b.WriteString("Its scope is frozen in the confirmed plan: an edit here changes nothing, and a change comes through a plan revision its maintainer authorizes again.\n\n")
	scope, _ := json.MarshalIndent(c.Scope, "", "  ")
	b.WriteString(inert("json", string(scope)) + "\n")
	return fmt.Sprintf("Coordination %s: %s", task, c.ID), b.String()
}

// CoordinationRepost is the plan repost comment for one accepted version: version, hash, changed
// sections and who reconfirms, from the fixed materiality table, and links to the children's
// issues, every one in a public repository by the time anything is filed.
func CoordinationRepost(task string, p v1alpha1.CoordinationPlan, hash string, changed, reauthorize []string, parentRequired bool, issues map[string]string) string {
	var b strings.Builder
	b.WriteString(CoordinationRepostMarker(task, p.Version, hash) + "\n")
	fmt.Fprintf(&b, "Plan version %d of coordination `%s`, sha256 `%s`.\n\n", p.Version, task, hash)
	for _, c := range p.Children {
		if u := issues[c.ID]; u != "" {
			fmt.Fprintf(&b, "- `%s`: %s\n", c.ID, u)
		}
	}
	m, _ := json.MarshalIndent(map[string]any{"changedSections": orEmpty(changed), "reauthorize": orEmpty(reauthorize),
		"parentRequired": parentRequired}, "", "  ")
	plan, _ := json.MarshalIndent(p, "", "  ")
	b.WriteString("\nMateriality:\n\n" + inert("json", string(m)) + "\n\nThe plan, as data:\n\n" + inert("json", string(plan)) + "\n")
	return b.String()
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// CoordinationIncident says on the parent issue that child of a Done coordination was reverted by
// pull request revert: a post-completion incident, recorded without reopening the parent or
// reverting any sibling (MR11). Once per child and revert.
func CoordinationIncident(parent, child string, revert int) Event {
	return Event{Key: fmt.Sprintf("coordination-incident-%s-%d", child, revert), Body: fmt.Sprintf("Child `%s` of agent factory "+
		"coordination `%s` was reverted by #%d after the coordination completed. The coordination stays done, and no sibling is "+
		"reverted: this is recorded as a post-completion incident.", child, parent, revert)}
}
