// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The task outcomes a factory may offer (phase F). Unset is change (a pull request).
const (
	OutcomeChange    = "change"
	OutcomePlanFirst = "plan-first"
	OutcomeReview    = "review"
	OutcomeReport    = "report"
)

// AllOutcomes returns the closed list of task outcomes.
func AllOutcomes() []string {
	return []string{OutcomeChange, OutcomePlanFirst, OutcomeReview, OutcomeReport}
}

// ValidOutcome reports whether s is a valid outcome name.
func ValidOutcome(s string) bool {
	return slices.Contains(AllOutcomes(), s)
}

// NormalizeOutcome maps an empty outcome to change.
func NormalizeOutcome(s string) string {
	if s == "" {
		return OutcomeChange
	}
	return s
}

var (
	reviewWordRE = regexp.MustCompile(`(?i)\breview(?:ing|ed|s)?\b`)

	// planFirstRE matches explicit requests to see or approve a plan before coding.
	planFirstRE = regexp.MustCompile(`(?i)\b(?:plan\s+first|show(?:\s+me)?\s+the\s+plan|share\s+the\s+plan|propose\s+a\s+plan|plan\s+before\s+(?:implementing|coding|changing|making|editing)|approve\s+the\s+plan)\b`)

	// reportLeadRE matches a request whose verb is investigate, find out, or why.
	reportLeadRE = regexp.MustCompile(`(?i)^(?:please\s+|can\s+you\s+|could\s+you\s+|would\s+you\s+|help(?:\s+me)?\s+|let'?s\s+|i\s+(?:want|need)\s+to\s+|we\s+(?:want|need)\s+to\s+)*(?:investigate\b|find\s+out\b|why\b)`)
	// reportClauseRE matches a subsequent clause whose verb is investigate or find out, or a why-question.
	reportClauseRE = regexp.MustCompile(`(?i)(?:[.!?;:,]\s+|\bto\s+)(?:investigate\b|find\s+out\b|why\s+(?:is|are|does|do|did|was|were|would|can't|cannot|won't|isn't|aren't|doesn't|didn't)\b)`)
	// reportNotChangeLeadRE guards against change verbs opening a request like "fix why ..." or "update the investigate ...".
	reportChangeLeadRE = regexp.MustCompile(`(?i)^(?:please\s+|can\s+you\s+|could\s+you\s+)*(?:fix|update|change|add|remove|delete|rename|refactor|implement|bump|upgrade|replace|rewrite|edit|modify|create)\b`)
)

// ReviewPRNumber returns the pull request number from the first GitHub pull request URL in text, or 0.
func ReviewPRNumber(text string) int {
	for tok := range strings.FieldsSeq(text) {
		tok = strings.Trim(tok, "<>()[]{}.,;:!?\"'")
		if before, _, ok := strings.Cut(tok, "|"); ok {
			tok = before
		}
		lower := strings.ToLower(tok)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			continue
		}
		u, err := url.Parse(tok)
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
			continue
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
			continue
		}
		n, err := strconv.Atoi(parts[3])
		if err == nil && n >= 1 {
			return n
		}
	}
	return 0
}

// InferOutcome deterministically infers a non-default outcome from the request text, or returns ""
// when no deterministic rule matches (so a model's choice or the default change applies).
//
// Rules (in priority order):
//   - a pull request URL with "review" → review
//   - "plan first", "show me the plan", etc. → plan-first
//   - "investigate", "why", "find out" as the request's verb → report
func InferOutcome(text string) string {
	s := strings.TrimSpace(text)
	if s == "" {
		return ""
	}
	if ReviewPRNumber(s) > 0 && reviewWordRE.MatchString(s) {
		return OutcomeReview
	}
	if planFirstRE.MatchString(s) {
		return OutcomePlanFirst
	}
	if reportLeadRE.MatchString(s) {
		return OutcomeReport
	}
	if !reportChangeLeadRE.MatchString(s) && reportClauseRE.MatchString(s) {
		return OutcomeReport
	}
	return ""
}

// ResolveOutcome combines deterministic request inference with the model's choice, bounded by the
// outcomes the factory offers (defaulting to [change] when offered is empty). An outcome not offered
// falls back to change with one explanatory note.
func ResolveOutcome(text, modelChoice string, offered []string) (outcome string, inferred bool, note string) {
	if len(offered) == 0 {
		offered = []string{OutcomeChange}
	}
	want := InferOutcome(text)
	if want == "" && ValidOutcome(modelChoice) && modelChoice != OutcomeChange {
		want = modelChoice
	}
	if want == "" || want == OutcomeChange {
		return OutcomeChange, false, ""
	}
	if slices.Contains(offered, want) {
		return want, true, ""
	}
	return OutcomeChange, false, fmt.Sprintf("outcome %q is not offered by this factory; running as change (a pull request).", want)
}

// IsOutcomeRevision reports whether candidate is base with only issues' outcomes changed.
func IsOutcomeRevision(base, candidate Plan) bool {
	if candidate.APIVersion != base.APIVersion || candidate.Repository != base.Repository || candidate.Budget != base.Budget ||
		len(candidate.Issues) != len(base.Issues) {
		return false
	}
	changed := false
	for i := range base.Issues {
		b, c := base.Issues[i], candidate.Issues[i]
		changed = changed || NormalizeOutcome(c.Outcome) != NormalizeOutcome(b.Outcome)
		if c.Index != b.Index || c.Title != b.Title || c.Body != b.Body || c.Class != b.Class ||
			c.Profile != b.Profile || !slices.Equal(c.Roles, b.Roles) || !slices.Equal(c.DependsOn, b.DependsOn) {
			return false
		}
	}
	return changed
}

func reviseOutcome(base, candidate Plan) (Plan, error) {
	out := cloneRevision(base)
	for i, is := range candidate.Issues {
		o := NormalizeOutcome(is.Outcome)
		if !ValidOutcome(o) {
			return Plan{}, refuse(ReasonPlannerInvalid, fmt.Sprintf("issue %d: outcome %q is not one of change, plan-first, review, report", i+1, is.Outcome))
		}
		out.Issues[i].Outcome = o
		out.Issues[i].OutcomeInferred = false
		out.Issues[i].OutcomeNote = ""
	}
	out.Hash = Hash(out)
	return out, nil
}
