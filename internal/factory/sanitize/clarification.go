// SPDX-License-Identifier: Apache-2.0

package sanitize

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const clarificationPrefix = "[NEEDS CLARIFICATION:"

// clarificationRE is the one live marker shape. IDs are canonical positive integers and the question
// stays on one line; Start and End below are byte offsets so a revision can replace the exact span.
var clarificationRE = regexp.MustCompile(`\[NEEDS CLARIFICATION: (g[1-9][0-9]*) ([^\]\r\n]+)\]`)

// decisionRE is the issue-local Decision/Assumption grammar. The optional gap is checked separately
// so g0 and non-canonical numbers are rejected rather than becoming a different identifier.
var decisionRE = regexp.MustCompile(`^([DA])([0-9]+)(?: \[(g[0-9]+)\])?: (.+)$`)

// ClarificationMarker is one [NEEDS CLARIFICATION: g<n> <text>] marker. Start is the opening
// bracket's byte offset and End is just after the closing bracket.
type ClarificationMarker struct {
	ID, Text   string
	Start, End int
}

// ClarificationMarkers parses every live marker in body. A malformed marker or an ID repeated in the
// same body is an error: silently skipping either would let an unresolved clarification reach filing.
func ClarificationMarkers(body string) ([]ClarificationMarker, error) {
	matches := clarificationRE.FindAllStringSubmatchIndex(body, -1)
	markers := make([]ClarificationMarker, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	scanned := 0
	for _, match := range matches {
		if at := strings.Index(body[scanned:match[0]], clarificationPrefix); at >= 0 {
			return nil, fmt.Errorf("invalid_marker: malformed marker at byte %d", scanned+at)
		}
		id := body[match[2]:match[3]]
		text := body[match[4]:match[5]]
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("invalid_marker: %s has an empty question", id)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("invalid_marker: duplicate ID %s", id)
		}
		seen[id] = struct{}{}
		markers = append(markers, ClarificationMarker{ID: id, Text: text, Start: match[0], End: match[1]})
		scanned = match[1]
	}
	if at := strings.Index(body[scanned:], clarificationPrefix); at >= 0 {
		return nil, fmt.Errorf("invalid_marker: malformed marker at byte %d", scanned+at)
	}
	return markers, nil
}

// DecisionRecord is one numbered Decision or Assumption. Gap is empty for a post-filing scoper
// record; Text excludes the record's prefix and colon.
type DecisionRecord struct {
	Kind   string
	Number int
	Gap    string
	Text   string
}

// DecisionRecords parses the optional Decisions section after Acceptance check. Every non-empty line
// in that section is one record; IDs ascend and are unique within this body, but need not be
// contiguous because split issues carry only their needed subset.
func DecisionRecords(body string) ([]DecisionRecord, error) {
	var records []DecisionRecord
	acceptanceSeen, decisionsSeen, inDecisions := false, false, false
	previous := 0
	for i, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "## Acceptance check":
			acceptanceSeen = true
			if inDecisions {
				return nil, fmt.Errorf("invalid_decisions: line %d is not a single-line record", i+1)
			}
			continue
		case "## Decisions":
			if !acceptanceSeen {
				return nil, fmt.Errorf("invalid_decisions: Decisions precedes Acceptance check")
			}
			if decisionsSeen {
				return nil, fmt.Errorf("invalid_decisions: repeated Decisions section at line %d", i+1)
			}
			decisionsSeen, inDecisions = true, true
			continue
		}
		if !inDecisions || trimmed == "" {
			continue
		}
		match := decisionRE.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("invalid_decisions: line %d is not a single-line record", i+1)
		}
		number, ok := positiveCanonical(match[2])
		if !ok || strings.TrimSpace(match[4]) == "" {
			return nil, fmt.Errorf("invalid_decisions: line %d has an invalid ID or empty text", i+1)
		}
		if number <= previous {
			return nil, fmt.Errorf("invalid_decisions: line %d repeats or descends from record %d", i+1, previous)
		}
		if match[3] != "" {
			if _, ok := positiveCanonical(strings.TrimPrefix(match[3], "g")); !ok {
				return nil, fmt.Errorf("invalid_decisions: line %d has an invalid gap reference", i+1)
			}
		}
		records = append(records, DecisionRecord{Kind: match[1], Number: number, Gap: match[3], Text: match[4]})
		previous = number
	}
	return records, nil
}

// positiveCanonical parses a decimal ID and rejects zero, signs, overflow and leading zeroes.
func positiveCanonical(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0 && strconv.Itoa(n) == s
}

// CheckFinalDraft applies the external filing rules: the ordinary issue template, valid Decisions,
// and no live or malformed clarification marker. Intermediate planner output uses CheckDraft instead.
func CheckFinalDraft(title, body string) (Draft, error) {
	d, err := CheckDraft(title, body)
	if err != nil {
		return Draft{}, err
	}
	if _, err := DecisionRecords(d.Body); err != nil {
		return Draft{}, err
	}
	markers, err := ClarificationMarkers(d.Body)
	if err != nil {
		return Draft{}, fmt.Errorf("unresolved_marker: %w", err)
	}
	if len(markers) > 0 {
		return Draft{}, fmt.Errorf("unresolved_marker: %s remains in the draft", markers[0].ID)
	}
	return d, nil
}
