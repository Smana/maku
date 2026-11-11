// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/factory/sanitize"
)

// The three elements the floor holds a plan to, each with the one question it asks. The question is
// the marker's text and the gap's text, so the two views of a gap are one string.
const (
	floorShouldQuestion     = "What observable behaviour MUST the change produce? Name what a user or reviewer sees."
	floorAcceptanceQuestion = "Which command or observation proves the change is done?"
	floorScopeQuestion      = "Which file, path or component should this change touch?"
)

var (
	// observableRE is what "What it should be" must contain at least once: an RFC 2119 keyword
	// (D12) or a verb of something a person can see. Matching is on whole words.
	observableRE = regexp.MustCompile(`\b(MUST|SHOULD|SHALL|MAY)\b|\b(shows?|returns?|prints?|displays?|lists?|reports?|writes?|exits?|responds?|emits?|renders?|posts?|comments?|refuses|fails|appears?|names?|says?|tells?)\b`)
	// backtickedRE is a command or identifier in code font: the acceptance check's first form.
	backtickedRE = regexp.MustCompile("`[^`\n]+`")
	// observationRE is the acceptance check's second form: MUST or SHOULD, then a verb that shows a
	// result, in one sentence.
	observationRE = regexp.MustCompile(`\b(MUST|SHOULD)\b[^.\n]*\b(shows?|returns?|passes?|prints?|exits?|displays?|lists?|reports?|contains?|outputs?|equals?|appears?)\b`)
	// fileRE, pathRE and identRE are what "names a file, path or component" means: a name with a
	// file extension, a slash-separated path, a code-font span, a CamelCase or snake_case identifier.
	fileRE  = regexp.MustCompile(`\b[\w-]+(?:\.[\w-]+)*\.(?:go|md|ya?ml|json|toml|tsx?|jsx?|sh|py|tf|html|css|sql|proto|mod|rs)\b`)
	pathRE  = regexp.MustCompile(`[\w.-]+/[\w.-]+`)
	identRE = regexp.MustCompile(`\b[A-Z][a-z0-9]+[A-Z][A-Za-z0-9]*\b|\b[a-z][a-z0-9]*_[a-z0-9_]+\b`)
	// gapNumberRE finds every g<n> a body already uses, in a marker or a record's reference.
	gapNumberRE = regexp.MustCompile(`\bg([1-9][0-9]*)\b`)
)

// ApplyFloor is the deterministic gap floor (#456): after the model has answered, the broker reads
// each issue body itself and adds the ranked gaps the model did not. It calls nothing and the same
// plan always yields the same result.
//
// An element is missing when:
//   - "What it should be" names no observable behaviour (no RFC 2119 keyword, no verb of a visible
//     result), or is empty or only a "not stated" placeholder;
//   - "Acceptance check" is missing, or has neither a code-font command nor a MUST/SHOULD
//     observation;
//   - request (the person's words and context) names no file, path or component. Only the request
//     counts, never the plan's own citations: the broker reads no repository on this path, so a
//     file the plan cites is the model's claim and the live failure this floor exists for.
//
// An element that already holds a live marker is left alone: the model asked. Each added gap has a
// marker in the right section (scope before the first heading, the others at the end of their own
// section, above any Decisions), a conservative recommendation, and a rank after every gap already
// there. IDs continue after the highest g<n> in any body or gap, so a marker never reuses a number
// a record already settled. The base plan's hash is recomputed; Gaps stay outside Material.
func ApplyFloor(p Plan, request string) Plan {
	type addition struct {
		issue    int
		section  string
		question string
		answer   Answer
	}
	var adds []addition
	if len(p.Issues) > 0 && !namesComponent(request) && !hasMarkerIn(p.Issues[0].Body, "scope") {
		area := titleArea(p.Issues[0].Title)
		adds = append(adds, addition{0, "scope", floorScopeQuestion, Answer{
			Answer: fmt.Sprintf("Limit the change to the %s area named in the title, and name the files it touches in the pull request.", area),
			Why:    "The request names no file, path or component, so the title's area is the only boundary the plan can state without guessing a path.",
		}})
	}
	for i, is := range p.Issues {
		should, acceptance := sectionBody(is.Body, "## What it should be"), sectionBody(is.Body, "## Acceptance check")
		if !hasMarkerIn(is.Body, "what-it-should-be") && !observable(should) {
			adds = append(adds, addition{i, "what-it-should-be", floorShouldQuestion, Answer{
				Answer: "The problem described in What is wrong MUST no longer occur, and the corrected behaviour MUST be visible to the person who reported it.",
				Why:    "The issue states no observable result; the conservative reading is that the reported problem stops happening.",
			}})
		}
		if !hasMarkerIn(is.Body, "acceptance-check") && !acceptable(acceptance) {
			adds = append(adds, addition{i, "acceptance-check", floorAcceptanceQuestion, Answer{
				Answer: "A reviewer repeats the steps in What is wrong and the problem no longer appears, and the repository's own test gate passes.",
				Why:    "The issue names no command or observation; this is the weakest check that still tells done from not done.",
			}})
		}
	}
	if len(adds) == 0 {
		return p
	}

	next := highestGap(p)
	issues := make([]Issue, len(p.Issues))
	copy(issues, p.Issues)
	p.Issues = issues
	gaps := append([]Gap(nil), p.Gaps...)
	for _, a := range adds {
		id := "g" + strconv.Itoa(next+1)
		body := insertMarker(p.Issues[a.issue].Body, a.section, id, a.question)
		if len(body) > maxBodyBytes {
			// The broker's own addition never makes a plan the model kept in bounds too large.
			continue
		}
		next++
		p.Issues[a.issue].Body = body
		gaps = append(gaps, Gap{ID: id, Section: a.section, Text: a.question, Recommended: a.answer})
	}
	p = WithGaps(p, gaps)
	p.Hash = Hash(p)
	return p
}

// namesComponent is whether text names a file, a path or a component.
func namesComponent(text string) bool {
	return fileRE.MatchString(text) || pathRE.MatchString(text) || backtickedRE.MatchString(text) || identRE.MatchString(text)
}

// observable is whether a "What it should be" section names behaviour a person can see.
func observable(section string) bool {
	return observableRE.MatchString(stripMarkers(section))
}

// acceptable is whether an "Acceptance check" holds a command or an observation.
func acceptable(section string) bool {
	s := stripMarkers(section)
	return backtickedRE.MatchString(s) || observationRE.MatchString(s)
}

// stripMarkers removes live markers: a question is not an answer, and a placeholder is not either.
func stripMarkers(s string) string {
	markers, err := sanitize.ClarificationMarkers(s)
	if err != nil {
		return s
	}
	for i := len(markers) - 1; i >= 0; i-- {
		s = s[:markers[i].Start] + s[markers[i].End:]
	}
	return s
}

// sectionBody is the text under heading, up to the next "## " heading, or "" when the heading is
// absent.
func sectionBody(body, heading string) string {
	var out []string
	in := false
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == heading:
			in = true
		case in && strings.HasPrefix(trimmed, "## "):
			return strings.Join(out, "\n")
		case in:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// hasMarkerIn is whether a live marker already sits where gap section's marker belongs.
func hasMarkerIn(body, section string) bool {
	markers, err := sanitize.ClarificationMarkers(body)
	if err != nil {
		return false
	}
	for _, m := range markers {
		if sectionAt(body, m.Start) == section {
			return true
		}
	}
	return false
}

// sectionAt names the gap section a byte offset of body falls in.
func sectionAt(body string, at int) string {
	section := "scope"
	for start := 0; start < at && start < len(body); {
		end := strings.IndexByte(body[start:], '\n')
		if end < 0 {
			end = len(body) - start
		}
		switch strings.TrimSpace(body[start : start+end]) {
		case "## What is wrong":
			section = "what-is-wrong"
		case "## What it should be":
			section = "what-it-should-be"
		case "## Acceptance check":
			section = "acceptance-check"
		}
		start += end + 1
	}
	return section
}

// insertMarker adds the marker for gap id in its section: scope before the first heading, the other
// two at the end of their own section, above the next heading (Decisions included).
func insertMarker(body, section, id, question string) string {
	marker := "[NEEDS CLARIFICATION: " + id + " " + question + "]"
	if section == "scope" {
		return marker + "\n\n" + body
	}
	heading := "## What it should be"
	if section == "acceptance-check" {
		heading = "## Acceptance check"
	}
	lines := strings.Split(body, "\n")
	in, at := false, len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if in && strings.HasPrefix(trimmed, "## ") {
			at = i
			break
		}
		if trimmed == heading {
			in = true
		}
	}
	// Trailing blank lines stay between the section and what follows it.
	for at > 0 && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	out := append([]string{}, lines[:at]...)
	out = append(out, marker)
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n")
}

// highestGap is the largest g<n> the plan already uses in a body, a record reference or a gap.
func highestGap(p Plan) int {
	top := 0
	note := func(s string) {
		for _, m := range gapNumberRE.FindAllStringSubmatch(s, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil && n > top {
				top = n
			}
		}
	}
	for _, is := range p.Issues {
		note(is.Body)
	}
	for _, g := range p.Gaps {
		note(g.ID)
	}
	return top
}

// titleArea is the `<area>` of a `<area>: <what is wrong>` title.
func titleArea(title string) string {
	area, _, _ := strings.Cut(title, ":")
	return strings.TrimSpace(area)
}
