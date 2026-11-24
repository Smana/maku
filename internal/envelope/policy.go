// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
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
	// Name is the merge policy the gate last decided the pull request under, chosen from its diff
	// (merge policies MR1); "" before a decision named one.
	Name      string `json:"name,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ProfileFact is the harness profile a task runs under, its evaluation cohort and the scoper's
// recommendation. It is the Task status projected into the room, never a fresh decision.
type ProfileFact struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Cohort      string `json:"cohort"`
	Recommended string `json:"recommended,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Why         string `json:"why,omitempty"`
	// Requested is the profile a person pinned, and Refused why the pin was not applied: a code of
	// ProfileRefusalCodes, empty when honoured.
	Requested string `json:"requested,omitempty"`
	Refused   string `json:"refused,omitempty"`
	// By is the login of a pin's applier when Source is "label".
	By string `json:"by,omitempty"`
	// CeilingMicroUSD and StepUpCeilingMicroUSD are the task's budget ceilings in micro-USD.
	CeilingMicroUSD       int64  `json:"ceilingMicroUSD,omitempty"`
	StepUpCeilingMicroUSD int64  `json:"stepUpCeilingMicroUSD,omitempty"`
	Description           string `json:"description,omitempty"`
	// StepUp is the one step-up taken or offered on the task (WR4).
	StepUp *StepUpFact `json:"stepUp,omitempty"`
}

// StepUpFact is the task's recorded or offered step-up projected into room facts (WR4).
type StepUpFact struct {
	From            string `json:"from,omitempty"`
	To              string `json:"to,omitempty"`
	Trigger         string `json:"trigger,omitempty"`
	Mode            string `json:"mode,omitempty"`
	CeilingMicroUSD int64  `json:"ceilingMicroUSD,omitempty"`
}

// ProfileSources are where a task's workflow came from (WF3, WF4): the Task CRD's
// status.profile.source enum. floor is a task triaged before the workflow left the class.
var ProfileSources = []string{"label", "schedule", "revert", "oracle", "repository", "floor", "control", "treatment", "scoper", "escalation"}

// ProfileRefusalCodes is intake's closed list of profile refusals (intake.ProfileRefusals, held within
// it by its test), and the four codes a task triaged before the workflow left the class may carry
// (profile_not_allowed, class_profile_fixed, class_no_profiles, profile_internal; WF4): envelope sits
// below intake, so the facts' validator keeps its own copy.
var ProfileRefusalCodes = []string{"profile_unknown", "profile_not_offered", "not_maintainer", "profile_conflict",
	"profile_no_tests_paths", "catalogue_unavailable",
	"profile_not_allowed", "class_profile_fixed", "class_no_profiles", "profile_internal"}

func (p ProfileFact) validate() error {
	switch {
	case p.Name == "" || utf8.RuneCountInString(p.Name) > 63:
		return errors.New("name is 1 to 63 characters")
	case !slices.Contains(ProfileSources, p.Source):
		return errors.New("source is " + strings.Join(ProfileSources, ", "))
	case !slices.Contains([]string{"labeled", "control", "treatment"}, p.Cohort):
		return errors.New("cohort is labeled, control or treatment")
	case utf8.RuneCountInString(p.Recommended) > 63:
		return errors.New("recommended is at most 63 characters")
	case utf8.RuneCountInString(p.Reason) > 32:
		return errors.New("reason is at most 32 characters")
	case utf8.RuneCountInString(p.Why) > 200:
		return errors.New("why is at most 200 characters")
	case utf8.RuneCountInString(p.Requested) > 63:
		return errors.New("requested is at most 63 characters")
	case p.Refused != "" && (p.Requested == "" || !slices.Contains(ProfileRefusalCodes, p.Refused)):
		return errors.New("refused is a code of the closed profile refusals, beside the profile requested")
	case p.By != "" && !ValidLogin(p.By):
		return errors.New("by is a GitHub login")
	case p.CeilingMicroUSD < 0 || p.CeilingMicroUSD > MaxMicroUSD:
		return errors.New("ceilingMicroUSD is out of range")
	case p.StepUpCeilingMicroUSD < 0 || p.StepUpCeilingMicroUSD > MaxMicroUSD:
		return errors.New("stepUpCeilingMicroUSD is out of range")
	case utf8.RuneCountInString(p.Description) > 120:
		return errors.New("description is at most 120 characters")
	}
	if s := p.StepUp; s != nil {
		switch {
		case utf8.RuneCountInString(s.From) > 63 || utf8.RuneCountInString(s.To) > 63:
			return errors.New("stepUp workflow is at most 63 characters")
		case utf8.RuneCountInString(s.Trigger) > 32:
			return errors.New("stepUp trigger is at most 32 characters")
		case s.Mode != "" && !slices.Contains([]string{"auto", "manual", "offered"}, s.Mode):
			return errors.New("stepUp mode is auto, manual or offered")
		case s.CeilingMicroUSD < 0 || s.CeilingMicroUSD > MaxMicroUSD:
			return errors.New("stepUp ceilingMicroUSD is out of range")
		}
	}
	return nil
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
	case p.Name != "" && !ValidClassName(p.Name):
		return errors.New("name is a policy name")
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
