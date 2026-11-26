// SPDX-License-Identifier: Apache-2.0

// Package meaning is what checks a merge policy's changes for meaning (the correctness signal of
// the 2026-11-26 design, CS1) and the one sentence every surface prints about a policy (CS7).
// Pure: config in, words out. The web pages, `maku policy explain`, the decision comments, the
// promotion pull request and Slack print what it returns and never rebuild it.
package meaning

import (
	"cmp"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/factory/config"
)

// Signal is a policy's correctness signal, from a closed list; None when nothing independent of
// the author checks what a change means.
type Signal string

// The signals (CS1). An equivalence proof joins them when MT2's proofs ship.
const (
	LinksOnly   Signal = "links-only"
	SemverPatch Signal = "semver-patch"
	SemverMinor Signal = "semver-minor"
	Oracle      Signal = "oracle"
	Contract    Signal = "contract"
	None        Signal = "none"
)

// Signals is the closed list, for a reader that must refuse anything else.
func Signals() []Signal { return []Signal{LinksOnly, SemverPatch, SemverMinor, Oracle, Contract, None} }

// Of derives the signal from the fields the resolved class already has. Bounds (files, caps,
// modifyOnly), added-entry, holdouts, the reviewer and the monitor are never a signal.
func Of(name string, cl config.Class) Signal {
	if cl.Oracle != nil {
		return Oracle
	}
	switch cmp.Or(cl.DiffShape, config.BuiltInShape(name)) {
	case "links-only":
		return LinksOnly
	case "semver-patch":
		return SemverPatch
	case "semver-minor":
		return SemverMinor
	}
	if slices.Contains(cl.Requires, "plan") && slices.Contains(cl.Requires, "tests") {
		return Contract
	}
	return None
}

// proseSuffixes is the closed list of CS2: a pattern ending in one of them admits prose only.
var proseSuffixes = []string{`\.md$`, `\.mdx$`, `\.mdx?$`, `\.rst$`, `\.txt$`, `\.adoc$`}

func allProse(patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, p := range patterns {
		if !slices.ContainsFunc(proseSuffixes, func(s string) bool { return strings.HasSuffix(p, s) }) {
			return false
		}
	}
	return true
}

// Prose reports whether every file the class admits is prose: a changed path must match both
// AllowedFiles and a binding's NarrowedFiles, so either list being all prose is enough.
func Prose(cl config.Class) bool { return allProse(cl.AllowedFiles) || allProse(cl.NarrowedFiles) }

// Record is the decision record's signal= value for a decision that held: "<signal>:ok", or "none"
// when nothing judged the meaning. A contract policy's decision is contract:ok only when the task
// ran under a contract, whose gate rows (2b, 2c) a decision that held has passed.
func Record(s Signal, contractHeld bool) string {
	if s == None || (s == Contract && !contractHeld) {
		return string(None)
	}
	return string(s) + ":ok"
}
