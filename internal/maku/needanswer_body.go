// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"strings"

	"github.com/Smana/maku/internal/factory/issuebody"
)

const (
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

// AnswerBody applies one confirmed answer: an acceptance answer goes into the Acceptance check section
// first, then every answer is recorded as the next issue-local Decision.
func AnswerBody(body, answer string, acceptance bool) (string, error) {
	if !acceptance {
		return AppendDecision(body, answer)
	}
	placed, err := issuebody.PlaceAcceptance(body, answer)
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
