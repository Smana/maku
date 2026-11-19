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
