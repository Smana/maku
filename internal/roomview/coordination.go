// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/summary"
)

// CoordinationLines is a coordination parent's family as the card, and later Slack, writes it:
// the plan, each visible child with its links, the artifacts between them, the integrations, the
// visible budget and the one recovery. It reads the viewer's filtered view only, so a withheld
// child is a count; it says what the family is doing, never that a child merged unless its phase
// says so.
func CoordinationLines(v coordinationview.View, o Options) []string {
	c := &card{o: o}
	hash := v.PlanHash
	if len(hash) > 7 {
		hash = hash[:7]
	}
	head := "Coordination: plan v" + strconv.Itoa(int(v.Plan.Version)) + " " + c.token(hash, false) +
		" · " + plural(len(v.Plan.Children), "child", "children")
	if v.RestrictedChildren > 0 {
		head += " · " + plural(v.RestrictedChildren, "child", "children") + " restricted"
	}
	lines := []string{c.bold(head)}

	f := coordinationview.FlowOf(v)
	for _, n := range f.Nodes {
		if n.Kind != summary.NodeChild {
			continue
		}
		l := "- " + c.token(n.ChildID, false) + " (" + c.token(n.Repository, false) + "): "
		switch {
		case n.Phase != "":
			l += c.token(n.Phase, false)
		case n.RequiredConfirmation != "":
			l += "awaiting " + n.RequiredConfirmation + " confirmation"
		default:
			l += "planned"
		}
		if n.Reason != "" {
			l += " · " + c.text(n.Reason, false)
		}
		if n.PRURL != "" {
			l += " · " + c.url("PR", n.PRURL)
		} else if n.IssueURL != "" {
			l += " · " + c.url("issue", n.IssueURL)
		}
		if n.Room != "" && o.Rooms != "" {
			l += " · " + c.url("room", strings.TrimRight(o.Rooms, "/")+"/r/"+n.Room)
		}
		lines = append(lines, l)
	}
	for _, e := range f.Edges {
		if e.Kind == summary.EdgeArtifact {
			lines = append(lines, "- "+c.token(e.To, false)+" needs "+c.token(e.Name, false)+": "+c.token(e.State, false))
		}
	}
	for _, n := range f.Nodes {
		if n.Kind == summary.NodeIntegration {
			state := "not started"
			if n.Phase != "" {
				state = c.token(n.Phase, false)
			}
			lines = append(lines, "- "+strings.TrimPrefix(n.ID, "integration:")+" integration: "+state)
		}
	}
	if b := v.Budget; b.TotalMicroUSD > 0 {
		spent := b.PlanningSettledMicroUSD + b.ChildrenSettledMicroUSD + b.IntegrationSettledMicroUSD
		lines = append(lines, "Spent "+dollars(spent)+" of "+dollars(b.TotalMicroUSD)+" ceiling")
	}
	if r := v.Recovery; r != nil {
		l := "Recovery: " + c.token(r.Code, false)
		if r.ChildID != "" {
			l += " (" + c.token(r.ChildID, false) + ")"
		}
		lines = append(lines, l)
	}
	return lines
}

// coordination writes a parent's family after its facts.
func (c *card) coordination(s summary.Summary) {
	v, ok := coordinationview.Of(s)
	if !ok {
		return
	}
	c.line("")
	for _, l := range CoordinationLines(v, c.o) {
		c.line(l)
	}
}
