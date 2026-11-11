// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// MaxOverlapCandidates bounds the evidence retained for one overlap evaluation.
const MaxOverlapCandidates = 20

// CanonicalGoal normalizes sanitized goal text for exact and lexical comparison. Sanitization is
// deliberately separate: this function derives a comparison value and never renders a task brief.
func CanonicalGoal(text string) string {
	text = norm.NFKC.String(text)
	text = strings.ToLower(text)
	text = strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

// GoalFingerprint returns the repository-scoped SHA-256 of text's canonical goal.
func GoalFingerprint(repository, text string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(repository) + "\x00" + CanonicalGoal(text)))
	return hex.EncodeToString(sum[:])
}

// GoalTerms returns at most 32 sorted, distinct terms from text's canonical form.
func GoalTerms(text string) []string {
	terms := strings.Fields(CanonicalGoal(text))
	slices.Sort(terms)
	terms = slices.Compact(terms)
	if len(terms) > 32 {
		terms = terms[:32]
	}
	return terms
}

// OverlapCandidate is one structured piece of overlap evidence. Text is the sanitized stored goal
// only, never an AgentRun's rendered brief. Active also covers active run or room evidence whose
// Task phase alone cannot describe it.
type OverlapCandidate struct {
	Match      v1alpha1.OverlapMatch
	Repository string
	Text       string
	Evidence   []string
	Active     bool
	Recent     time.Time
}

// OverlapResult is the bounded, persistable outcome of an overlap evaluation.
type OverlapResult struct {
	Fingerprint    string
	Classification string
	Note           string
	Matches        []v1alpha1.OverlapMatch
	Sources        []string
	Pending        bool
}

// RankOverlapCandidates ranks sanitized candidates without I/O or side effects. The caller supplies
// only candidates from the goal's repository and excludes the current Task and its runs. Exact
// active matches are retained ahead of the lexical cap; all other matches share active-first,
// distinct-shared-term, recency, and stable identity ordering.
func RankOverlapCandidates(goal, acceptance string, candidates []OverlapCandidate) []OverlapCandidate {
	canonicalGoal := CanonicalGoal(goal)
	queryTerms := GoalTerms(goal + " " + acceptance)
	if len(queryTerms) == 0 {
		return nil
	}

	type ranked struct {
		candidate   OverlapCandidate
		shared      int
		exactActive bool
	}
	rankedCandidates := make([]ranked, 0, len(candidates))
	for _, candidate := range candidates {
		_, phaseActive := v1alpha1.OverlapPhase(candidate.Match.Phase)
		candidate.Active = candidate.Active || phaseActive

		exact := canonicalGoal != "" && CanonicalGoal(candidate.Text) == canonicalGoal
		shared := sharedGoalTerms(queryTerms, GoalTerms(candidate.Text))
		if !exact && shared == 0 {
			continue
		}
		candidate.Match.Relation = "lexical"
		if exact {
			candidate.Match.Relation = "exact"
		}
		rankedCandidates = append(rankedCandidates, ranked{
			candidate:   candidate,
			shared:      shared,
			exactActive: exact && candidate.Active,
		})
	}

	slices.SortStableFunc(rankedCandidates, func(a, b ranked) int {
		if a.exactActive != b.exactActive {
			if a.exactActive {
				return -1
			}
			return 1
		}
		if a.candidate.Active != b.candidate.Active {
			if a.candidate.Active {
				return -1
			}
			return 1
		}
		if a.shared != b.shared {
			return cmp.Compare(b.shared, a.shared)
		}
		if !a.candidate.Recent.Equal(b.candidate.Recent) {
			return b.candidate.Recent.Compare(a.candidate.Recent)
		}
		if order := cmp.Compare(a.candidate.Match.Task, b.candidate.Match.Task); order != 0 {
			return order
		}
		if order := cmp.Compare(a.candidate.Match.Run, b.candidate.Match.Run); order != 0 {
			return order
		}
		if order := cmp.Compare(a.candidate.Match.Issue, b.candidate.Match.Issue); order != 0 {
			return order
		}
		if order := cmp.Compare(a.candidate.Match.PR, b.candidate.Match.PR); order != 0 {
			return order
		}
		return cmp.Compare(a.candidate.Repository, b.candidate.Repository)
	})

	if len(rankedCandidates) > MaxOverlapCandidates {
		rankedCandidates = rankedCandidates[:MaxOverlapCandidates]
	}
	result := make([]OverlapCandidate, len(rankedCandidates))
	for i := range rankedCandidates {
		result[i] = rankedCandidates[i].candidate
	}
	return result
}

func sharedGoalTerms(query, candidate []string) int {
	shared := 0
	for i, j := 0, 0; i < len(query) && j < len(candidate); {
		switch {
		case query[i] < candidate[j]:
			i++
		case query[i] > candidate[j]:
			j++
		default:
			shared++
			i++
			j++
		}
	}
	return shared
}
