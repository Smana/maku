// SPDX-License-Identifier: Apache-2.0

package meaning

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/factory/config"
)

// Rules are the rules of the correctness-signals design's second plan that the words may promise:
// an unchecked policy that is not prose stays in shadow (CS2), live prose is audited (CS4), and
// factory/wrong is read (CS6).
type Rules struct {
	UncheckedStaysShadow bool
	AuditEvery           int
	WrongLabel           bool
}

// InForce is the rules this build enforces: none yet, so no surface promises a rule the gate does
// not apply. Each field turns on in the commit that enforces its rule.
var InForce = Rules{}

// Input is one policy, or one repository's binding of it.
type Input struct {
	Name string
	// Class is a binding's resolved class, or the policy's own (PolicySpec.Class()).
	Class config.Class
	// Mode is config.ModeLive, ModeShadow or ModeDisabled; "" for a policy read without a repository.
	Mode string
	// VerifyFor and RevertWindow are the repository's merge.verifyFor and merge.revertWindow; 0
	// leaves After and Undo out.
	VerifyFor, RevertWindow time.Duration
	AutoRevert              bool
	Rules                   Rules
}

// Sentence is the sentence of CS7 in parts, and whole in Text (Markdown: **bold** labels and
// `code`). A surface prints Text, or SignalLine, and never assembles the parts again.
type Sentence struct {
	Signal     Signal `json:"signal"`
	Prose      bool   `json:"prose"`
	Lead       string `json:"lead"`
	When       string `json:"when,omitempty"`
	CheckedBy  string `json:"checkedBy"`
	NotChecked string `json:"notChecked,omitempty"`
	OnceLive   string `json:"onceLive,omitempty"`
	After      string `json:"after,omitempty"`
	Undo       string `json:"undo,omitempty"`
	WayOut     string `json:"wayOut,omitempty"`
	Text       string `json:"text"`
}

var checkedBy = map[Signal]string{
	LinksOnly:   "the `links-only` shape: every changed line is a link target on the same host",
	SemverPatch: "the `semver-patch` shape: only a patch version moves",
	SemverMinor: "the `semver-minor` shape: only a minor or patch version moves",
	Oracle:      "a detector: the `%s` probe named the broken links, and the diff changes exactly those",
	Contract:    "the test contract: red before the change, green after",
}

var notChecked = map[Signal]string{
	LinksOnly:   "what the new page says",
	SemverPatch: "the release itself; Maku waits for its release age",
	SemverMinor: "the release itself, and what the new version renders. This policy is shadow only",
	Oracle:      "the detector's own errors",
	Contract:    "behaviour the plan did not name",
	None:        "what a change means",
}

var verbs = map[Signal]string{
	LinksOnly:   "changes only link targets",
	SemverPatch: "moves only a patch version",
	SemverMinor: "moves only a minor or patch version",
	Oracle:      "fixes only the links a detector reported",
}

const (
	wayOutNow   = "To have its meaning checked,"
	wayOutLater = "To let it merge alone,"
	wayOutHow   = "require a plan and tests."
)

// Build is the sentence of CS7 for one policy, or one repository's binding of it.
func Build(in Input) Sentence {
	cl, s := in.Class, Of(in.Name, in.Class)
	out := Sentence{Signal: s, Prose: Prose(cl)}
	if in.Mode == config.ModeDisabled {
		out.Lead, out.CheckedBy = "Merges nothing here:", "nothing"
		out.Text = "**Merges nothing here:** the policy is off in this repository."
		return out
	}
	files := Files(patternsOf(cl))
	if s == None && !out.Prose && in.Rules.UncheckedStaysShadow {
		out.Lead, out.CheckedBy = "Never merges without you:", "nothing"
		out.When = "nothing checks what a change to " + files + " means. Maku records when it would have merged."
		out.WayOut = wayOutLater + " " + wayOutHow
		out.Text = "**" + out.Lead + "** " + out.When + " **" + wayOutLater + "** " + wayOutHow
		return out
	}
	verb := "Would merge"
	if in.Mode == config.ModeLive {
		verb = "Merges"
	}
	out.Lead = verb + " without you when"
	if s == Contract {
		out.Lead = verb + " after you approve the plan, without a review of the diff, when"
	}
	out.When = when(cl, s, files)
	switch {
	case s == Oracle:
		out.CheckedBy = fmt.Sprintf(checkedBy[Oracle], cl.Oracle.Probe)
	case s != None:
		out.CheckedBy = checkedBy[s]
	case out.Prose:
		out.CheckedBy = "nothing: this is prose"
	default:
		out.CheckedBy = "nothing"
	}
	if s != None || !out.Prose {
		out.NotChecked = notChecked[s]
	}
	if s == None && out.Prose && in.Rules.AuditEvery > 0 {
		out.OnceLive = "Maku asks you about every " + ordinal(in.Rules.AuditEvery) + " one."
	}
	if s == None && !out.Prose {
		out.WayOut = wayOutNow + " " + wayOutHow
	}
	if in.VerifyFor > 0 {
		out.After = after(in, cl)
	}
	if in.RevertWindow > 0 {
		out.Undo = "`factory/revert` within " + human(in.RevertWindow) + "."
	}
	out.Text = text(out)
	return out
}

// SignalLine is the one line a decision message carries (guidance surface 6).
func (s Sentence) SignalLine() string {
	switch {
	case s.Signal != None && s.Signal != "":
		by, _, _ := strings.Cut(s.CheckedBy, ":")
		return "Meaning checked by " + by + "."
	case s.Prose:
		return "Meaning not checked: this is prose."
	}
	return "Meaning not checked."
}

// patternsOf is the files a change may touch: a binding's narrowed files, else the policy's.
func patternsOf(cl config.Class) []string {
	if len(cl.NarrowedFiles) > 0 {
		return cl.NarrowedFiles
	}
	return cl.AllowedFiles
}

func code(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "`" + x + "`"
	}
	return out
}

// when is who changes what, within which caps, and on which conditions.
func when(cl config.Class, s Signal, files string) string {
	who := "an agent"
	if cl.Adopted() {
		who = natural(code(cl.Author.Adopt.Logins), "or")
	}
	head := who + " "
	switch {
	case verbs[s] != "":
		head += verbs[s]
		if s == LinksOnly || s == Oracle {
			head += " in " + cmpStr(files, "Markdown")
		}
	case cl.DiffShape == "added-entry":
		head += "adds one entry"
		if files != "" {
			head += " to " + files
		}
	default:
		head += "changes"
		if files != "" {
			head += " " + files
		} else {
			head += " files"
		}
	}
	var extras []string
	var caps []string
	if cl.MaxPatchFiles > 0 {
		caps = append(caps, strconv.Itoa(cl.MaxPatchFiles)+" files")
	}
	if cl.MaxModifiedLines > 0 {
		caps = append(caps, strconv.Itoa(cl.MaxModifiedLines)+" lines")
	}
	if len(caps) > 0 {
		extras = append(extras, "at most "+natural(caps, "and"))
	}
	if cl.MaxPerDay > 0 {
		extras = append(extras, strconv.Itoa(cl.MaxPerDay)+" a day")
	}
	var conds []string
	checks := append([]string{}, cl.HoldoutChecks...)
	if cl.ReleaseAgeCheck != "" {
		checks = append(checks, cl.ReleaseAgeCheck)
	}
	if len(checks) > 0 {
		verb := " passes"
		if len(checks) > 1 {
			verb = " pass"
		}
		conds = append(conds, natural(code(checks), "and")+verb)
	}
	if (s == None || s == Contract) && !cl.Adopted() {
		conds = append(conds, "a reviewer approves")
	}
	out := head
	if len(extras) > 0 {
		out += ", " + strings.Join(extras, ", ")
	}
	if len(conds) > 0 {
		if len(extras) > 0 {
			out += ","
		}
		out += " and " + strings.Join(conds, " and ")
	}
	return out
}

func cmpStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// after is what Maku watches once the change merged.
func after(in Input, cl config.Class) string {
	parts := []string{"Maku watches main for " + human(in.VerifyFor)}
	if in.AutoRevert {
		parts[0] += " and reverts on red"
	} else {
		parts[0] += " and opens a revert on red"
	}
	if m := cl.Monitor; m != nil {
		if m.Flux != nil && m.Flux.Within.Duration > 0 {
			parts = append(parts, "watches Flux for "+human(m.Flux.Within.Duration))
		}
		if m.Verification != nil && m.Verification.Within.Duration > 0 {
			parts = append(parts, "waits "+human(m.Verification.Within.Duration)+" for RunLore's verdict")
		}
		if m.OracleRuns > 0 {
			parts = append(parts, fmt.Sprintf("reads the detector's next %d runs", m.OracleRuns))
		}
		if m.FollowUpWindow.Duration > 0 {
			parts = append(parts, "reads follow-up fixes for "+human(m.FollowUpWindow.Duration))
		}
	}
	return strings.Join(parts, "; ")
}

func text(s Sentence) string {
	var b strings.Builder
	b.WriteString("**" + s.Lead + "** " + s.When + ".")
	b.WriteString(" **Meaning checked by** " + s.CheckedBy + ".")
	if s.NotChecked != "" {
		b.WriteString(" **Not checked:** " + s.NotChecked + ".")
	}
	if s.OnceLive != "" {
		b.WriteString(" **Once live** " + s.OnceLive)
	}
	if s.After != "" {
		b.WriteString(" **After the merge** " + s.After + ".")
	}
	if s.Undo != "" {
		b.WriteString(" **Undo:** " + s.Undo)
	}
	if s.WayOut != "" {
		label, rest, _ := strings.Cut(s.WayOut, ", ")
		b.WriteString(" **" + label + ",** " + rest)
	}
	return b.String()
}

// human is a duration in words: minutes under an hour, hours under a week or when not whole days.
func human(d time.Duration) string {
	plural := func(n int64, unit string) string {
		if n == 1 {
			return "1 " + unit
		}
		return strconv.FormatInt(n, 10) + " " + unit + "s"
	}
	switch {
	case d < time.Hour:
		return plural(int64(d/time.Minute), "minute")
	case d >= 7*24*time.Hour && d%(24*time.Hour) == 0:
		return plural(int64(d/(24*time.Hour)), "day")
	}
	return plural(int64(d/time.Hour), "hour")
}

var ordinals = []string{2: "second", 3: "third", 4: "fourth", 5: "fifth", 6: "sixth", 7: "seventh", 8: "eighth", 9: "ninth", 10: "tenth"}

func ordinal(n int) string {
	if n >= 2 && n < len(ordinals) {
		return ordinals[n]
	}
	return strconv.Itoa(n) + "th"
}

var stageWords = map[string]string{
	"plan":   "a plan you approve",
	"tests":  "tests written before the change",
	"judge":  "a completion judge",
	"review": "a review",
}

// StageWords is each workflow stage a policy can require, in words.
func StageWords() map[string]string {
	out := make(map[string]string, len(stageWords))
	for k, v := range stageWords {
		out[k] = v
	}
	return out
}

// Stages is the stages a policy requires (spec.requires), in words.
func Stages(requires []string) string {
	out := make([]string, 0, len(requires))
	for _, r := range requires {
		out = append(out, cmpStr(stageWords[r], r))
	}
	return natural(out, "and")
}
