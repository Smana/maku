// SPDX-License-Identifier: Apache-2.0

// Package hardening is the untrusted-input hardening core (hardening design UH1–UH14): author trust
// and provenance, the safe-output vocabulary and its threat check, the protected-path floor, the
// readiness port coordination's execute gate asks, and the injection drill scorer. Stage H0 ships
// the contracts only: nothing here is wired into a run, and Readiness answers false.
package hardening

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Level is how far the factory trusts a text's author (UH6), gh-aw's five levels in order.
type Level int

// The levels, lowest first: a comparison is a trust comparison.
const (
	Blocked Level = iota
	None
	Unapproved
	Approved
	Merged
)

var levelNames = []string{"blocked", "none", "unapproved", "approved", "merged"}

func (l Level) String() string {
	if l < Blocked || l > Merged {
		return "invalid"
	}
	return levelNames[l]
}

// ParseLevel reads a level's name, exactly as String writes it.
func ParseLevel(s string) (Level, error) {
	if i := slices.Index(levelNames, s); i >= 0 {
		return Level(i), nil
	}
	return Blocked, fmt.Errorf("%q is none of %s", s, strings.Join(levelNames, ", "))
}

// MarshalJSON writes the level's name: a record must read the same after a reorder.
func (l Level) MarshalJSON() ([]byte, error) { return json.Marshal(l.String()) }

// UnmarshalJSON reads a level's name.
func (l *Level) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a trust level is a string: %w", err)
	}
	v, err := ParseLevel(s)
	if err != nil {
		return err
	}
	*l = v
	return nil
}

// Author is a forge object's author: the login and GitHub's author_association on that object.
type Author struct {
	Login       string
	Association string
}

// Policy decides a human author's level. Logins compare case-insensitively, as GitHub's do.
type Policy struct {
	Min         Level
	Maintainers []string
	TrustedBots []string
	Blocked     []string
}

// LevelOf is a's level (UH6 table): blocked users first, then the association, raised to approved
// by the factory's maintainers, trusted bots and a maintainer's vouch (the trigger label on the
// issue it labels, or factory/vouch). An association GitHub may add later reads as none.
func (p Policy) LevelOf(a Author, vouched bool) Level {
	switch {
	case a.Login == "":
		return None
	case has(p.Blocked, a.Login):
		return Blocked
	case vouched, has(p.Maintainers, a.Login), has(p.TrustedBots, a.Login):
		return Approved
	}
	switch a.Association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return Approved
	case "CONTRIBUTOR", "FIRST_TIME_CONTRIBUTOR":
		return Unapproved
	}
	return None
}

// Withholds reports whether text at l stays out of every run: below Min, and blocked always.
func (p Policy) Withholds(l Level) bool { return l == Blocked || l < p.Min }

func has(logins []string, login string) bool {
	return slices.ContainsFunc(logins, func(s string) bool { return strings.EqualFold(s, login) })
}

// Placeholder is what a run reads in place of n withheld items of a kind: a count, never content.
func Placeholder(n int, kind string) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "[1 " + kind + " withheld: author trust]"
	}
	return fmt.Sprintf("[%d %ss withheld: author trust]", n, kind)
}

// SourceKind is where a run's input came from (UH7). Its values are bounded: they label records.
type SourceKind string

// The source kinds. Machine kinds have a fixed level; comment, room and parent are levelled by
// their caller.
const (
	SourceDefaultBranch SourceKind = "default_branch"
	SourceSteering      SourceKind = "steering"
	SourceFactory       SourceKind = "factory"
	SourceIssue         SourceKind = "issue"
	SourceComment       SourceKind = "comment"
	SourceOwnCI         SourceKind = "own_ci"
	SourceForeignCI     SourceKind = "foreign_ci"
	SourceRoom          SourceKind = "room"
	SourceParent        SourceKind = "parent"
	SourceRunLore       SourceKind = "runlore"
	SourceToolResult    SourceKind = "tool_result"
)

// Level is a machine source's fixed level (UH7 table). A human or inherited source has none and
// answers Blocked, so a caller that forgets to level it lowers the run's minimum, never raises it.
func (k SourceKind) Level() Level {
	switch k {
	case SourceDefaultBranch, SourceSteering, SourceFactory:
		return Merged
	case SourceOwnCI:
		return Approved
	case SourceRunLore, SourceToolResult, SourceForeignCI:
		return None
	}
	return Blocked
}

// Source is one input's kind and level: never its content.
type Source struct {
	Kind  SourceKind `json:"kind"`
	Level Level      `json:"level"`
}

// InputTrust is what a task records of its inputs (status.inputTrust): the lowest level it read,
// the items withheld per kind, and each distinct kind and level read.
type InputTrust struct {
	Min      Level              `json:"min"`
	Withheld map[SourceKind]int `json:"withheld,omitempty"`
	Sources  []Source           `json:"sources,omitempty"`
}

// NewInputTrust is the record of a run that has read nothing yet: merged until an input says less.
func NewInputTrust() InputTrust { return InputTrust{Min: Merged} }

// Record notes an input the run read at l.
func (t *InputTrust) Record(kind SourceKind, l Level) {
	t.Min = min(t.Min, l)
	if s := (Source{kind, l}); !slices.Contains(t.Sources, s) {
		t.Sources = append(t.Sources, s)
	}
}

// Withhold counts an item kept from the run. It does not lower Min: the run never read it.
func (t *InputTrust) Withhold(kind SourceKind) {
	if t.Withheld == nil {
		t.Withheld = map[SourceKind]int{}
	}
	t.Withheld[kind]++
}

// Inherit records a parent's minimum: a split child and an integration task read what it read.
func (t *InputTrust) Inherit(parent InputTrust) { t.Record(SourceParent, parent.Min) }
