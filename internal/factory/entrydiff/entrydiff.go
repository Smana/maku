// SPDX-License-Identifier: Apache-2.0

// Package entrydiff decides whether a diff adds one catalog entry and only appends to the
// catalog's companion files (RunLore KB, the KB class), from the diff alone: allowedFiles bound
// where the entry goes, companionFiles what else may grow, the holdout checks what it says.
package entrydiff

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/filefence"
	"github.com/Smana/maku/internal/factory/forge"
)

// MaxCompanionLines bounds the lines one companion file gains.
const MaxCompanionLines = 10

// Added reports whether d adds exactly one file matching cl.AllowedFiles, absent on the base
// (d.Base[p] == "": Diff stores an added file's base as an empty value) and non-empty valid UTF-8
// on the head, and changes every other file only by appending lines: modified,
// matching cl.CompanionFiles, 0 deleted, 1..MaxCompanionLines added, the base's lines kept in
// order. No control character but tab and newline, and no Unicode format character (zero-width,
// bidi), in the entry or an added line. The reason when it does not.
func Added(cl config.Class, d forge.Diff) (bool, string) {
	allowed := filefence.Compile(cl.AllowedFiles, true)
	companions := filefence.Compile(cl.CompanionFiles, true)
	entries := 0
	for _, f := range d.Files {
		if f.Previous != "" || f.Status == "renamed" {
			return false, "renamed: " + f.Path
		}
		switch {
		case f.Status == "added" && filefence.MatchesAny(allowed, f.Path):
			entries++
			if entries > 1 {
				return false, "more than one added entry"
			}
			if d.Base[f.Path] != "" {
				return false, "entry exists on the base: " + f.Path
			}
			if why := checkText(d.Head[f.Path]); why != "" {
				return false, "entry " + f.Path + ": " + why
			}
			if d.Head[f.Path] == "" {
				return false, "entry empty: " + f.Path
			}
		case f.Status == "modified" && filefence.MatchesAny(companions, f.Path):
			if why := appendOnly(f, d.Base[f.Path], d.Head[f.Path]); why != "" {
				return false, f.Path + ": " + why
			}
		default:
			return false, "file not allowed as " + f.Status + ": " + f.Path
		}
	}
	if entries != 1 {
		return false, "no added entry"
	}
	return true, ""
}

// checkText refuses invalid UTF-8, a control character but tab and newline, and a format character.
func checkText(s string) string {
	if !utf8.ValidString(s) {
		return "invalid UTF-8"
	}
	for _, r := range s {
		if r != '\t' && r != '\n' && unicode.IsControl(r) {
			return "control character"
		}
		if unicode.Is(unicode.Cf, r) {
			return "format character"
		}
	}
	return ""
}

func lines(s string) []string {
	l := strings.SplitAfter(s, "\n")
	if l[len(l)-1] == "" {
		l = l[:len(l)-1]
	}
	return l
}

// appendOnly accepts a head that is the base with lines inserted, as many as the stat's additions.
func appendOnly(f forge.FileStat, base, head string) string {
	if f.Deletions != 0 {
		return "deletes lines"
	}
	if f.Additions < 1 || f.Additions > MaxCompanionLines {
		return "adds an unaccepted number of lines"
	}
	bl, hl := lines(base), lines(head)
	if len(hl)-len(bl) != f.Additions {
		return "added lines differ from the stat"
	}
	i := 0
	for _, h := range hl {
		if i < len(bl) && h == bl[i] {
			i++
			continue
		}
		if why := checkText(h); why != "" {
			return "added line: " + why
		}
	}
	if i != len(bl) {
		return "base lines not kept in order"
	}
	return ""
}
