// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"regexp"
	"slices"
	"strings"
)

var (
	// quotedRE is a term a question quotes: "…", '…', `…` or “…”.
	quotedRE = regexp.MustCompile("\"([^\"\n]+)\"|`([^`\n]+)`|“([^”\n]+)”|(?:^|\\s)'([^'\n]+)'")
	// optionsRE is the two options a question offers, "X or Y".
	optionsRE = regexp.MustCompile(`([\w.:/-]+)\s+or\s+([\w.:/-]+)`)
	// numberRE is a number a question names.
	numberRE = regexp.MustCompile(`\d+(?:\.\d+)?`)
	// contrastRE is a contrast marker: the value after it is the right one, except after "instead
	// of", where the right one is before it.
	contrastRE = regexp.MustCompile(`(?i)\b(?:but|instead of|should be|is actually|caps it at|sets)\b`)
	// clauseEndRE ends the clause a contrast marker governs.
	clauseEndRE = regexp.MustCompile(`[;\n]|\.(?:\s|$)`)
)

// statedAnswer is the value the request itself gives for g's question, and whether it gives one: the
// question quotes or offers two values the request both names, and a contrast marker in the request
// ("but", "should be", "caps it at", …) settles which is right. It is conservative: a question it
// cannot settle this way is asked.
func statedAnswer(g Gap, request string) (string, bool) {
	var terms []string
	add := func(s string) {
		s = strings.Trim(s, " ?.,;:!")
		if s != "" && !slices.Contains(terms, s) && containsTerm(request, s) {
			terms = append(terms, s)
		}
	}
	for _, m := range quotedRE.FindAllStringSubmatch(g.Text, -1) {
		for _, s := range m[1:] {
			add(s)
		}
	}
	for _, m := range optionsRE.FindAllStringSubmatch(g.Text, -1) {
		add(m[1])
		add(m[2])
	}
	for _, n := range numberRE.FindAllString(g.Text, -1) {
		add(n)
	}
	if len(terms) < 2 {
		return "", false
	}
	for _, loc := range contrastRE.FindAllStringIndex(request, -1) {
		var clause string
		if strings.EqualFold(request[loc[0]:loc[1]], "instead of") {
			clause = request[:loc[0]]
			if ends := clauseEndRE.FindAllStringIndex(clause, -1); len(ends) > 0 {
				clause = clause[ends[len(ends)-1][1]:]
			}
		} else {
			clause = request[loc[1]:]
			if end := clauseEndRE.FindStringIndex(clause); end != nil {
				clause = clause[:end[0]]
			}
		}
		var in []string
		for _, t := range terms {
			if containsTerm(clause, t) {
				in = append(in, t)
			}
		}
		if len(in) == 1 {
			return in[0], true
		}
	}
	return "", false
}

// containsTerm is whether text names term as a whole token, case-insensitively: "5" is not in "15"
// or "0.5".
func containsTerm(text, term string) bool {
	re := regexp.MustCompile(`(?i)(?:^|[^\w.])` + regexp.QuoteMeta(term) + `(?:$|[^\w.]|\.(?:\s|$))`)
	return re.MatchString(text)
}

const (
	// docVerbs are what a document is said to show: the wrong value follows.
	docVerbs = `(?:says?|shows?|lists?|states?|reads?|gives?|documents?|names?|claims?)`
	// sourceVerbs are what the source does with the right value, longest first.
	sourceVerbs = `(caps it at|caps|sets|has|is|uses|defines|names|gives|says)`
	// statedEnd ends the right value: a sentence end, a semicolon, a line end or the request's end.
	statedEnd = `\s*(?:\.(?:\s|$)|[;\n]|$)`
)

var (
	// shouldBeRE is "<doc> says X, but it should be Y".
	shouldBeRE = regexp.MustCompile(`(?i)\b` + docVerbs + `\b\s+\S.*?,?\s+but\s+(?:it\s+|they\s+)?should\s+be\s+(\S.*?)` + statedEnd)
	// sourceSetsRE is "<doc> says X, but <source> sets Y".
	sourceSetsRE = regexp.MustCompile(`(?i)\b` + docVerbs + `\b\s+\S.*?,?\s+but\s+(\S.*?)\s+` + sourceVerbs + `\s+(\S.*?)` + statedEnd)
	// judgementRE is a right value that is only a verdict ("but it is wrong"): no value is stated.
	judgementRE = regexp.MustCompile(`(?i)^(?:not\s+)?(?:wrong|incorrect|outdated|out of date|stale|broken|confusing|unclear|right|correct|off|missing)\b`)
	// pronounRE is a source that only points back at the document.
	pronounRE = regexp.MustCompile(`(?i)^(?:it|that|this|they|which)$`)
	// valueQuestionRE is a question about which value is right.
	valueQuestionRE = regexp.MustCompile(`(?i)\b(?:correct|right|actual|accurate|intended|true|real)\b|\bwhich (?:one|value|number|is)\b|\bwhat (?:is|are|should) the (?:value|values|number|limit|maximum|minimum|cap|model)\b|\bshould\b[^?]*\b(?:be|say|state|show|read|match)\b`)
	// sourceQuestionRE is a question about which source is authoritative.
	sourceQuestionRE = regexp.MustCompile(`(?i)source of truth|authoritative|canonical|\bwhich (?:file|source|document|one) (?:is|wins|should)\b`)
)

// Correction is what a request states about a wrong value: the right one, and the source that holds
// it with what it does ("sets", "caps it at"), when it names one.
type Correction struct {
	Right, Source, Verb string
}

// StatedCorrection is the correction a request states as "<doc> says/shows X, but <source>
// sets/caps it at/has/is Y" or "… but it should be Y", and whether it states one. A right value that
// is only a verdict ("but it is wrong") and a source that is only a pronoun state none.
func StatedCorrection(request string) (Correction, bool) {
	for line := range strings.SplitSeq(request, "\n") {
		if m := shouldBeRE.FindStringSubmatch(line); m != nil {
			if right := cleanStated(m[1]); right != "" && !judgementRE.MatchString(right) {
				return Correction{Right: right}, true
			}
		}
		if m := sourceSetsRE.FindStringSubmatch(line); m != nil {
			source, verb, right := cleanStated(m[1]), strings.ToLower(m[2]), cleanStated(m[3])
			if right != "" && source != "" && !judgementRE.MatchString(right) && !pronounRE.MatchString(source) {
				return Correction{Right: right, Source: source, Verb: verb}, true
			}
		}
	}
	return Correction{}, false
}

// cleanStated trims a stated phrase of spacing and trailing punctuation.
func cleanStated(s string) string {
	return strings.TrimRight(strings.Join(strings.Fields(s), " "), ".,;: ")
}

// correctionAnswer is the answer a stated correction gives g's question, and whether it gives one:
// the right value for a question about the value, the source and what it does for one about which
// source is authoritative. Any other question is the person's to answer.
func correctionAnswer(g Gap, request string) (string, bool) {
	c, ok := StatedCorrection(request)
	if !ok {
		return "", false
	}
	switch {
	case sourceQuestionRE.MatchString(g.Text) || (c.Source != "" && strings.Contains(strings.ToLower(g.Text), "authoritative")):
		if c.Source == "" {
			return "", false
		}
		return c.Source + ", which " + c.Verb + " " + c.Right, true
	case valueQuestionRE.MatchString(g.Text):
		return c.Right, true
	}
	return "", false
}
