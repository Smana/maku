// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"fmt"
	"strings"

	"github.com/Smana/maku/internal/factory/sanitize"
)

const (
	acceptanceHeading = "## Acceptance check"
	decisionsHeading  = "## Decisions"
	shouldBeHeading   = "## What it should be"
	// scoperAcceptanceLead prefixes the scoper's own proposal when an issue states no acceptance.
	scoperAcceptanceLead = "No acceptance stated; proceed on the scoper's own: "
)

// IsAcceptanceQuestion reports whether the scoper asked for the acceptance check, whose answer
// belongs in the "## Acceptance check" section rather than only in a Decision record.
func IsAcceptanceQuestion(question string) bool {
	q := strings.ToLower(question)
	return strings.Contains(q, "acceptance") || strings.Contains(q, "proves it is done")
}

// AcceptanceFromRecommendation turns the scoper's recommendation into the text it would write: the
// "No acceptance stated; proceed on the scoper's own: " lead is dropped, and the rest is one line.
func AcceptanceFromRecommendation(recommended string) string {
	return strings.TrimSpace(OneLine(strings.TrimPrefix(strings.TrimSpace(recommended), scoperAcceptanceLead)))
}

type bodyLine struct {
	text       string
	start, end int // byte range, end including the newline
}

func splitBodyLines(body string) []bodyLine {
	var lines []bodyLine
	for start := 0; start < len(body); {
		end := len(body)
		if i := strings.IndexByte(body[start:], '\n'); i >= 0 {
			end = start + i + 1
		}
		lines = append(lines, bodyLine{text: strings.TrimRight(body[start:end], "\r\n"), start: start, end: end})
		start = end
	}
	return lines
}

// sectionRange returns the byte range of the section under heading: from its heading line through
// the line before the next "## " heading (or the end). ok is false when the heading is absent.
func sectionRange(lines []bodyLine, bodyLen int, heading string) (start, end int, ok bool) {
	for i, l := range lines {
		if strings.TrimSpace(l.text) != heading {
			continue
		}
		end = bodyLen
		for _, next := range lines[i+1:] {
			if strings.HasPrefix(next.text, "## ") {
				end = next.start
				break
			}
		}
		return l.start, end, true
	}
	return 0, 0, false
}

// PlaceAcceptance writes a confirmed one-line acceptance answer into body's Acceptance check section:
//   - no section: it is created after "What it should be" (before "## Decisions" when that comes
//     first, at the end otherwise);
//   - a section holding a [NEEDS CLARIFICATION: g<n> ...] marker: the first marker is replaced;
//   - a section with text and no marker: the answer is appended as its own paragraph;
//   - an empty section: the answer fills it.
//
// Text outside the touched region is kept byte for byte.
func PlaceAcceptance(body, answer string) (string, error) {
	if strings.ContainsAny(answer, "\r\n") {
		return "", fmt.Errorf("invalid_answer: an acceptance answer must be one line")
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", fmt.Errorf("invalid_answer: an acceptance answer must not be blank")
	}
	lines := splitBodyLines(body)
	if start, end, ok := sectionRange(lines, len(body), acceptanceHeading); ok {
		section := body[start:end]
		markers, err := sanitize.ClarificationMarkers(section)
		if err != nil {
			return "", err
		}
		if len(markers) > 0 {
			m := markers[0]
			return body[:start+m.Start] + answer + body[start+m.End:], nil
		}
		// Headings may carry trailing spaces or CR: keep them, and work from the end of the heading line.
		headEnd := start
		for _, l := range lines {
			if l.start == start {
				headEnd = l.end
				break
			}
		}
		if strings.TrimSpace(body[headEnd:end]) == "" {
			rest := body[end:]
			prefix := body[:headEnd]
			if !strings.HasSuffix(prefix, "\n") {
				prefix += "\n"
			}
			insert := "\n" + answer + "\n"
			if rest != "" {
				insert += "\n"
			}
			return prefix + insert + rest, nil
		}
		trimmed := strings.TrimRight(body[:end], "\n")
		tail := body[end:]
		if tail != "" {
			tail = "\n\n" + tail
		} else if strings.HasSuffix(body, "\n") {
			tail = "\n"
		}
		return trimmed + "\n\n" + answer + tail, nil
	}

	at := len(body)
	if _, end, ok := sectionRange(lines, len(body), shouldBeHeading); ok {
		at = end
	}
	if start, _, ok := sectionRange(lines, len(body), decisionsHeading); ok && start < at {
		at = start
	}
	head, tail := strings.TrimRight(body[:at], "\n"), body[at:]
	section := acceptanceHeading + "\n\n" + answer
	if head == "" {
		return section + tail, nil
	}
	if tail == "" {
		return head + "\n\n" + section, nil
	}
	return head + "\n\n" + section + "\n\n" + tail, nil
}

// AnswerBody applies one confirmed answer: an acceptance answer goes into the Acceptance check section
// first, then every answer is recorded as the next issue-local Decision.
func AnswerBody(body, answer string, acceptance bool) (string, error) {
	if !acceptance {
		return AppendDecision(body, answer)
	}
	placed, err := PlaceAcceptance(body, answer)
	if err != nil {
		return "", err
	}
	return AppendDecision(placed, "Acceptance check: "+strings.TrimSpace(answer))
}

// bodyDiff renders a line diff of old to new, unchanged lines prefixed "  ", removed "- ", added "+ ".
// Issue bodies are small, so a quadratic longest-common-subsequence table is fine.
func bodyDiff(oldBody, newBody string) string {
	a, b := strings.Split(oldBody, "\n"), strings.Split(newBody, "\n")
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			out = append(out, "  "+a[i])
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			out = append(out, "+ "+b[j])
			j++
		default:
			out = append(out, "- "+a[i])
			i++
		}
	}
	return strings.Join(out, "\n")
}
