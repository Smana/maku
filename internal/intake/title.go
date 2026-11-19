// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// titleAreaRE is a title that already opens with an area, as `sanitize.CheckDraft` reads one,
	// with any spacing around its colon.
	titleAreaRE = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._/-]*(?:\([A-Za-z0-9._/-]+\))?)\s*:\s*(\S.*)$`)
	// titleMarkupRE is the markdown a planner wraps a title in: emphasis and code font.
	titleMarkupRE = regexp.MustCompile("\\*\\*|__|`")
	// areaRE is one area word.
	areaRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// RepairTitle mends a planner's title the template check would refuse for its form alone: it trims
// it, joins its lines, strips markdown emphasis, a heading's hashes and wrapping quotes, writes the
// one space after the area's colon, prefixes an area when none opens it, and caps it at the
// template's 256 characters. The area is `docs` for a docs class, else the first segment of the
// first repository path the body names, else `chore`. A title with nothing left to mend is returned
// as it came, for the check to refuse.
func RepairTitle(title, class, body string) string {
	t := strings.Join(strings.Fields(titleMarkupRE.ReplaceAllString(title, "")), " ")
	t = strings.TrimSpace(strings.TrimLeft(t, "#"))
	t = strings.TrimSpace(strings.Trim(t, `"'“”`))
	if t == "" {
		return title
	}
	if m := titleAreaRE.FindStringSubmatch(t); m != nil {
		t = m[1] + ": " + m[2]
	} else {
		t = titleAreaFor(class, body) + ": " + t
	}
	if utf8.RuneCountInString(t) > maxTitleRunes {
		t = strings.TrimRightFunc(string([]rune(t)[:maxTitleRunes]), unicode.IsSpace)
	}
	return t
}

// titleAreaFor is the area a title without one is given: `docs` for a docs class, else the first
// segment of the first repository path body names, else `chore`.
func titleAreaFor(class, body string) string {
	if c := strings.ToLower(strings.TrimSpace(class)); c == "docs" || strings.HasPrefix(c, "docs-") {
		return "docs"
	}
	for _, p := range PathsIn(body) {
		seg, _, _ := strings.Cut(strings.TrimLeft(p, "./"), "/")
		if areaRE.MatchString(seg) {
			return seg
		}
	}
	return "chore"
}

// repairTitles is RepairTitle on every issue of d, before any check reads them.
func (d *Draft) repairTitles() {
	for i := range d.Issues {
		d.Issues[i].Title = RepairTitle(d.Issues[i].Title, d.Issues[i].Class, d.Issues[i].Body)
	}
}
