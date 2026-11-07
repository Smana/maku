// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"unicode/utf8"
)

// The modes of a merge class, the gate's three outcomes: live arms once its conditions hold,
// shadow is decided like live and never armed, unarmed is never merged by the factory.
const (
	PolicyLive    = "live"
	PolicyShadow  = "shadow"
	PolicyUnarmed = "unarmed"
)

// The policy fact's bounds: the facts ride one bounded POST, which the broker refuses for good
// when they fail validation, so the factory clamps to these (Clip) and says so in Truncated.
const (
	MaxPolicyClasses = 16
	MaxPolicyList    = 8
	MaxPolicyEntry   = 120
)

// PolicyFact is the merge policy that governs the room's task (merge policy view D1): its class
// and mode, the class's declared conditions, and the repository's class table. It carries the
// room's own repository only, so it is visible to whoever may read the room (D2).
type PolicyFact struct {
	Class string `json:"class,omitempty"`
	Mode  string `json:"mode"`
	// GateOff: no class is live or shadow on the factory, so it holds no merger and merges nothing.
	GateOff bool `json:"gateOff,omitempty"`
	// ApproveToMerge: a maintainer's approval of the current commit arms the merger (AM1); never
	// for an adopted class.
	ApproveToMerge bool              `json:"approveToMerge,omitempty"`
	Adopted        bool              `json:"adopted,omitempty"`
	Conditions     *PolicyConditions `json:"conditions,omitempty"`
	Classes        []PolicyClass     `json:"classes,omitempty"`
	// MergedHow is how the merger merged the pull request: class or approved, "" otherwise.
	MergedHow string `json:"mergedHow,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// PolicyConditions are a class's declared conditions, as the factory config names them.
type PolicyConditions struct {
	AllowedFiles     []string `json:"allowedFiles,omitempty"`
	ProtectedFiles   []string `json:"protectedFiles,omitempty"`
	ModifyOnly       bool     `json:"modifyOnly,omitempty"`
	MaxPatchFiles    int      `json:"maxPatchFiles,omitempty"`
	MaxModifiedLines int      `json:"maxModifiedLines,omitempty"`
	DiffShape        string   `json:"diffShape,omitempty"`
	HoldoutChecks    []string `json:"holdoutChecks,omitempty"`
	MaxPerDay        int      `json:"maxPerDay,omitempty"`
	AdoptLogins      []string `json:"adoptLogins,omitempty"`
	RequiredLabel    string   `json:"requiredLabel,omitempty"`
	Oracle           bool     `json:"oracle,omitempty"`
}

// PolicyClass is one row of the repository's class table.
type PolicyClass struct {
	Name    string `json:"name"`
	Mode    string `json:"mode"`
	Adopted bool   `json:"adopted,omitempty"`
}

var className = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,62}[a-z0-9])?$`)

// ValidClassName reports whether s is a class name the policy fact carries.
func ValidClassName(s string) bool { return className.MatchString(s) }

func validMode(m string) bool { return m == PolicyLive || m == PolicyShadow || m == PolicyUnarmed }

// Clip is list within the policy's bounds, a copy, and whether anything was cut.
func Clip(list []string) ([]string, bool) {
	if list == nil {
		return nil, false
	}
	cut := len(list) > MaxPolicyList
	out := slices.Clone(list[:min(len(list), MaxPolicyList)])
	for i, s := range out {
		if utf8.RuneCountInString(s) > MaxPolicyEntry {
			out[i], cut = string([]rune(s)[:MaxPolicyEntry]), true
		}
	}
	return out, cut
}

func (p PolicyFact) validate() error {
	switch {
	case !validMode(p.Mode):
		return errors.New("mode is live, shadow or unarmed")
	case p.Class != "" && !ValidClassName(p.Class):
		return errors.New("class is a class name")
	case len(p.Classes) > MaxPolicyClasses:
		return fmt.Errorf("classes are at most %d, got %d", MaxPolicyClasses, len(p.Classes))
	case p.MergedHow != "" && p.MergedHow != "class" && p.MergedHow != "approved":
		return errors.New("mergedHow is class or approved")
	}
	for i, c := range p.Classes {
		if !ValidClassName(c.Name) || !validMode(c.Mode) {
			return fmt.Errorf("classes[%d] is a class name and a mode", i)
		}
	}
	if c := p.Conditions; c != nil {
		return c.validate()
	}
	return nil
}

func (c PolicyConditions) validate() error {
	if c.MaxPatchFiles < 0 || c.MaxModifiedLines < 0 || c.MaxPerDay < 0 {
		return errors.New("conditions: caps are 0 or more")
	}
	for _, l := range [][]string{c.AllowedFiles, c.ProtectedFiles, c.HoldoutChecks, c.AdoptLogins,
		{c.DiffShape, c.RequiredLabel}} {
		if len(l) > MaxPolicyList {
			return fmt.Errorf("conditions: a list is at most %d entries", MaxPolicyList)
		}
		for _, s := range l {
			if utf8.RuneCountInString(s) > MaxPolicyEntry {
				return fmt.Errorf("conditions: an entry is at most %d characters", MaxPolicyEntry)
			}
		}
	}
	for _, l := range c.AdoptLogins {
		if !ValidLogin(l) {
			return errors.New("conditions: adoptLogins are GitHub logins")
		}
	}
	return nil
}
