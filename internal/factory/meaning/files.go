// SPDX-License-Identifier: Apache-2.0

package meaning

import (
	"regexp"
	"strings"
)

// The gloss of an anchored file pattern, ported from web/src/policy-words.ts (patternGloss): the
// sentence is built here, so the words for files are too.
var (
	alternativesRE = regexp.MustCompile(`^\(([^)]+)\)(/.*)$`)
	extensionRE    = regexp.MustCompile(`\\\.(mdx?|ya?\??ml|json|go|ts)(?:\?|$)`)
	metaRE         = regexp.MustCompile(`[.\[\]()+*?|]`)
	unescape       = strings.NewReplacer(`\.`, ".", `\/`, "/")
)

func gloss(pattern string) string {
	p := strings.TrimSuffix(strings.TrimPrefix(pattern, "^"), "$")
	if !strings.HasSuffix(p, "/") && !strings.Contains(p, ".+") && !strings.Contains(p, ".*") && !strings.ContainsAny(p, "[(") {
		return "`" + unescape.Replace(p) + "`"
	}
	rest, prefixes := p, []string{}
	if m := alternativesRE.FindStringSubmatch(p); m != nil {
		rest = m[2]
		for _, x := range strings.Split(m[1], "|") {
			prefixes = append(prefixes, x+"/")
		}
	} else if i := metaRE.FindStringIndex(p); i == nil {
		prefixes = append(prefixes, p)
	} else {
		prefixes = append(prefixes, p[:i[0]])
	}
	kind := "files"
	if m := extensionRE.FindStringSubmatch(rest); m != nil {
		switch {
		case strings.HasPrefix(m[1], "md"):
			kind = "Markdown"
		case strings.HasPrefix(m[1], "ya"):
			kind = "YAML"
		default:
			kind = strings.ToUpper(m[1])
		}
	}
	var dirs []string
	for _, x := range prefixes {
		if x = unescape.Replace(x); x != "" {
			dirs = append(dirs, "`"+x+"`")
		}
	}
	if len(dirs) == 0 {
		return kind + " files"
	}
	return kind + " under " + natural(dirs, "or")
}

// Files is patterns in words, joined with "or"; "" for none.
func Files(patterns []string) string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, gloss(p))
	}
	return natural(out, "or")
}

// natural joins "a, b and c".
func natural(xs []string, word string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " " + word + " " + xs[len(xs)-1]
}
