// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/factory/sanitize"
)

// repositoryQuestionPrefix opens the one question OneQuestion asks when nothing grounds the
// repository: IsRepositoryGap recognises the gap by it.
const repositoryQuestionPrefix = "Which repository should this change go to:"

// maxChoicesNamed bounds how many repositories the repository question lists.
const maxChoicesNamed = 5

var (
	// doneWhenRE is a done-when the person stated: "done when …", "it is done when …",
	// "Acceptance: …". The clause runs to the end of its sentence or line.
	doneWhenRE = regexp.MustCompile(`(?i)(?:\bdone when|\bacceptance:)\s*([^\n]+?)\s*(?:\.(?:\s|$)|\n|$)`)
	// repoPathRE is a repository path: two or more slash-separated segments ending in a file with an
	// extension, as a whole token.
	repoPathRE = regexp.MustCompile(`(?:^|[\s(\x60'"])((?:[\w.-]+/)+[\w-][\w.-]*\.[A-Za-z0-9]+)\b`)
)

// IsTemplateGap is whether g is one of the template's own questions, the acceptance check or the
// observable behaviour, rather than a gap in what the person asked (#625). The broker never asks
// one: it assumes the conservative default instead.
func IsTemplateGap(g Gap) bool {
	return g.Section == "acceptance-check" || (g.Section == "what-it-should-be" && g.Text == floorShouldQuestion)
}

// IsRepositoryGap is whether g is the repository question OneQuestion adds.
func IsRepositoryGap(g Gap) bool { return strings.HasPrefix(g.Text, repositoryQuestionPrefix) }

// DoneWhen is the acceptance check the request states, "Done when <clause>.", or "" when it states
// none.
func DoneWhen(request string) string {
	m := doneWhenRE.FindStringSubmatch(request)
	if m == nil {
		return ""
	}
	clause := strings.TrimRight(strings.Join(strings.Fields(m[1]), " "), ".;, ")
	if clause == "" {
		return ""
	}
	if r := []rune(clause); len(r) > MaxWhyRunes {
		clause = string(r[:MaxWhyRunes])
	}
	return "Done when " + clause + "."
}

// AcceptanceAnswered reports whether the plan or request has settled its acceptance check so the
// factory's intake ask guard marks the task answered rather than asking again (WR6).
func AcceptanceAnswered(p Plan, request string) bool {
	if DoneWhen(request) != "" {
		return true
	}
	for _, g := range p.Gaps {
		if g.Section == "acceptance-check" {
			return false
		}
	}
	if len(p.Issues) == 0 {
		return false
	}
	for _, is := range p.Issues {
		if strings.TrimSpace(sectionBody(is.Body, "## Acceptance check")) == "" {
			return false
		}
	}
	return true
}

// PathsIn is the repository paths text names, in order and without duplicates; a URL's path is not
// one, since a link names a page, not a file in a served repository.
func PathsIn(text string) []string {
	var out []string
	for _, field := range strings.Fields(text) {
		if strings.Contains(field, "://") {
			text = strings.Replace(text, field, " ", 1)
		}
	}
	for _, m := range repoPathRE.FindAllStringSubmatch(text, -1) {
		if p := m[1]; !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// templateAnswer is the conservative default for a template section, the floor's own.
func templateAnswer(section string) Answer {
	switch section {
	case "acceptance-check":
		return floorAcceptanceAnswer
	case "what-it-should-be":
		return floorShouldAnswer
	}
	return Answer{}
}

// OneQuestion holds a handoff's plan to at most one question (#625, #626). Every gap but the one
// kept is assumed: its marker is replaced by its recommendation and an A record says so, as
// acceptRest would.
//
// The question kept is, in order: the repository, when choices holds more than one repository and
// nothing grounded the choice (the planner's pick is the recommendation); else the highest-ranked
// gap that is not a template one and that the request does not already answer (statedAnswer: that
// gap is assumed with the value the request states). answered keeps none: the person has already answered once, and a
// re-plan may not re-open what they settled. An acceptance check the request states (DoneWhen)
// replaces the template's default. A template gap whose section is already concrete (an acceptance
// check that names a result, a behaviour that is observable or carries a Decision), or whose
// assumption a gap before it in the same section already wrote, is dropped with no A record: the
// issue says it once.
func OneQuestion(p Plan, request string, answered bool, choices []string) (Plan, error) {
	p = cloneRevision(p)
	gaps := append([]Gap(nil), p.Gaps...)
	keep := ""
	if !answered && len(choices) > 1 && len(p.Issues) > 0 {
		id := "g" + strconv.Itoa(highestGap(p)+1)
		q := repositoryQuestion(choices)
		if b := insertMarker(p.Issues[0].Body, "scope", id, q); len(b) <= maxBodyBytes {
			p.Issues[0].Body = b
			why := "it is the planner's reading of the request"
			if len(p.RepositoryEvidence) > 0 {
				why = oneLine(p.RepositoryEvidence[0].Text)
			}
			gaps = append([]Gap{{ID: id, Section: "scope", Text: q, Recommended: Answer{Answer: p.Repository, Why: why}}}, gaps...)
			keep = id
		}
	}
	// A question the request already answers is never asked (statedAnswer): its stated value is
	// assumed, and the next real gap, if any, is the one kept.
	stated := map[string]string{}
	if keep == "" && !answered {
		for _, g := range gaps {
			if IsTemplateGap(g) {
				continue
			}
			if v, ok := statedAnswer(g, request); ok {
				stated[g.ID] = v
				continue
			}
			if v, ok := correctionAnswer(g, request); ok {
				stated[g.ID] = v
				continue
			}
			keep = g.ID
			break
		}
	}
	assume := map[string]Answer{}
	byID := map[string]Gap{}
	var kept []Gap
	for _, g := range gaps {
		byID[g.ID] = g
		if g.ID == keep {
			kept = append(kept, g)
			continue
		}
		a := g.Recommended
		if v, ok := stated[g.ID]; ok {
			a = Answer{Answer: v, Why: "the request states it"}
		}
		if g.Section == "acceptance-check" {
			if d := DoneWhen(request); d != "" {
				a = Answer{Answer: d, Why: "the request states when it is done"}
			} else if c, ok := StatedCorrection(request); ok {
				a = Answer{Answer: correctionCheck(c), Why: "the request states the right value"}
			}
		}
		if strings.TrimSpace(a.Answer) == "" {
			a = templateAnswer(g.Section)
		}
		if strings.TrimSpace(a.Answer) == "" {
			return Plan{}, refuse(ReasonPlannerInvalid, "gap "+g.ID+" has no recommendation to assume")
		}
		assume[g.ID] = Answer{Answer: oneLine(a.Answer), Why: oneLine(a.Why)}
	}
	if len(assume) > 0 {
		last := 0
		for _, is := range p.Issues {
			recs, err := sanitize.DecisionRecords(is.Body)
			if err != nil {
				return Plan{}, refuse(ReasonPlannerInvalid, "the plan has invalid Decisions")
			}
			for _, r := range recs {
				last = max(last, r.Number)
			}
		}
		for i := range p.Issues {
			markers, err := sanitize.ClarificationMarkers(p.Issues[i].Body)
			if err != nil {
				return Plan{}, refuse(ReasonPlannerInvalid, "the plan has an invalid clarification marker")
			}
			body := p.Issues[i].Body
			drop := settledTemplate(body, markers, byID, assume, request)
			var records []string
			for j := len(markers) - 1; j >= 0; j-- {
				if drop[markers[j].ID] {
					body = dropMarker(body, markers[j])
				} else if a, ok := assume[markers[j].ID]; ok {
					body = body[:markers[j].Start] + a.Answer + body[markers[j].End:]
				}
			}
			for _, m := range markers {
				a, ok := assume[m.ID]
				if !ok || drop[m.ID] {
					continue
				}
				last++
				text := "assumed " + a.Answer
				if a.Why != "" {
					text += ", because " + a.Why
				}
				records = append(records, fmt.Sprintf("A%d [%s]: %s", last, m.ID, text))
			}
			for _, r := range records {
				body = appendRevisionRecord(body, r)
			}
			if _, err := sanitize.DecisionRecords(body); err != nil {
				return Plan{}, refuse(ReasonPlannerInvalid, "the assumptions do not form valid Decisions")
			}
			p.Issues[i].Body = body
		}
	}
	p = WithGaps(p, kept)
	p.Hash = Hash(p)
	return p, nil
}

// settledTemplate is the assumed template gaps of one body to drop rather than write: its section
// is already concrete without them (concreteShould, concreteCheck: a stated value counts), or an
// earlier marker in the section assumes the same text. A done-when the request states is the
// person's own check, and is always written.
func settledTemplate(body string, markers []sanitize.ClarificationMarker, gaps map[string]Gap, assume map[string]Answer, request string) map[string]bool {
	stated := DoneWhen(request)
	drop := map[string]bool{}
	written := map[string]bool{}
	for _, m := range markers {
		a, ok := assume[m.ID]
		g := gaps[m.ID]
		if !ok || !IsTemplateGap(g) {
			continue
		}
		section := sectionAt(body, m.Start)
		key := section + "\x00" + a.Answer
		switch {
		case written[key]:
			drop[m.ID] = true
		case section == "acceptance-check" && (stated == "" || a.Answer != stated) && concreteCheck(sectionBody(body, "## Acceptance check"), request):
			drop[m.ID] = true
		case section == "what-it-should-be":
			drop[m.ID] = concreteShould(body, sectionBody(body, "## What it should be"), request)
		}
		written[key] = !drop[m.ID]
	}
	return drop
}

// dropMarker removes m from body, and its line when the marker was all the line held.
func dropMarker(body string, m sanitize.ClarificationMarker) string {
	start, end := m.Start, m.End
	lineStart := strings.LastIndexByte(body[:start], '\n') + 1
	lineEnd := len(body)
	if i := strings.IndexByte(body[end:], '\n'); i >= 0 {
		lineEnd = end + i
	}
	if strings.TrimSpace(body[lineStart:start]+body[end:lineEnd]) != "" {
		return body[:start] + body[end:]
	}
	switch {
	case lineEnd < len(body):
		return body[:lineStart] + body[lineEnd+1:]
	case lineStart > 0:
		return body[:lineStart-1]
	}
	return ""
}

// repositoryQuestion is the one line asking which repository, naming the first choices.
func repositoryQuestion(choices []string) string {
	named := choices
	more := ""
	if len(named) > maxChoicesNamed {
		named, more = named[:maxChoicesNamed], ", or another served repository"
	}
	list := strings.Join(named[:len(named)-1], ", ") + " or " + named[len(named)-1]
	return repositoryQuestionPrefix + " " + list + more + "?"
}
