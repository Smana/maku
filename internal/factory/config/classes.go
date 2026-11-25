// SPDX-License-Identifier: Apache-2.0

package config

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// MergeClass is a MergePolicy's class conditions (MR8, MR9): what a policy declares, without
// mode (which belongs to each binding) or workflow fields (which moved to Factory.spec.workflows
// and Repository.spec.workflow).
// +kubebuilder:object:generate=true
type MergeClass struct {
	// Description says what the class is for, in plain words, for the settings pages and
	// `kubectl get`; the gate never reads it.
	// +kubebuilder:validation:MaxLength=280
	Description string `json:"description,omitempty"`
	// AllowedFiles: every changed path matches one of these regular expressions.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	AllowedFiles []string `json:"allowedFiles,omitempty"`
	// NarrowedFiles is a binding's allowedFiles over a policy that has its own (MR3): every changed
	// path matches one of these too. Set only by resolving a binding; refused on a MergePolicy.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	NarrowedFiles []string `json:"narrowedFiles,omitempty"`
	// ProtectedFiles: no changed path matches one of these.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	ProtectedFiles []string `json:"protectedFiles,omitempty"`
	// RequiresApproval asks a human about a pull request touching one of its paths, even in a live
	// class (merge policies MP12).
	// +kubebuilder:validation:MaxItems=16
	RequiresApproval []ApprovalLeaf `json:"requiresApproval,omitempty"`
	// ModifyOnly: no file is added, deleted, renamed or copied.
	ModifyOnly bool `json:"modifyOnly,omitempty"`
	// MaxPatchFiles caps the changed files and MaxModifiedLines their additions plus deletions,
	// as GitHub counts them (policy-bot's modified_lines); 0 is no cap.
	MaxPatchFiles    int `json:"maxPatchFiles,omitempty"`
	MaxModifiedLines int `json:"maxModifiedLines,omitempty"`
	// DiffShape is a shape every changed line must have: links-only is R52's LinksOnly.
	// +kubebuilder:validation:MaxLength=512
	DiffShape string `json:"diffShape,omitempty"`
	// HoldoutChecks are checks GitHub Actions must report SUCCESS on the head; their workflow is
	// a gate path, so the pull request cannot change them (loops L7).
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=1024
	HoldoutChecks []string `json:"holdoutChecks,omitempty"`
	// MaxPerDay caps this class's merges a day, under merge.maxPerDay; 0 is no cap of its own.
	MaxPerDay int `json:"maxPerDay,omitempty"`
	// Evidence is how many clean shadow decisions a promotion needs (MR4): low is 5, for a class
	// whose diff a shape or an oracle checks mechanically; standard, the default, is 20.
	// +kubebuilder:validation:Enum=low;standard
	Evidence string `json:"evidence,omitempty"`
	// Oracle makes the class oracle-backed (MC4–MC6): its tasks are a detector's findings, and its
	// gate holds only when the head no longer holds them and changes nothing else.
	Oracle *Oracle `json:"oracle,omitempty"`
	// Monitor watches each merge of the class after it lands (merge gate classes MC7): main's CI
	// for every merge, the detector's next runs for an oracle class, Flux on the factory's cluster,
	// and follow-up fixes for FollowUpWindow. Absent: no monitor, as before.
	Monitor *Monitor `json:"monitor,omitempty"`
	// Author is how the class's pull requests are authored (MC1's author row): absent, the agents
	// App through a task's runs; Adopt, another author's pull requests the factory adopts (MC10).
	Author *Author `json:"author,omitempty"`
	// ReleaseAgeCheck names the commit status that holds the release age (MC11), posted by an
	// adopted login: Renovate's renovate/stability-days.
	// +kubebuilder:validation:MaxLength=1024
	ReleaseAgeCheck string `json:"releaseAgeCheck,omitempty"`
	// CompanionFiles are the paths an added-entry pull request may append to beside its one added
	// entry (a knowledge base's index and log), anchored like AllowedFiles.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	CompanionFiles []string `json:"companionFiles,omitempty"`
	// RequiredLabel is the label an adopted pull request must carry, set by an adopted login, for
	// the factory to adopt it (K11): RunLore's ready-to-merge.
	// +kubebuilder:validation:MaxLength=253
	RequiredLabel string `json:"requiredLabel,omitempty"`
	// Requires names workflow stages that every task touching this class's paths must run (WR6):
	// plan, tests, judge, review. Adds stages to any workflow and never removes one.
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:MaxLength=16
	// +kubebuilder:validation:items:Enum=plan;tests;judge;review
	Requires []string `json:"requires,omitempty"`
}

// RequiredStages are the workflow stages a MergePolicy class may require (WR6).
func RequiredStages() []string { return []string{"plan", "tests", "judge", "review"} }

// Class returns a deep copy of m as a resolved Class with Live and Shadow unset.
func (m MergeClass) Class() Class {
	cp := m.DeepCopy()
	return Class{
		Description:      cp.Description,
		AllowedFiles:     cp.AllowedFiles,
		NarrowedFiles:    cp.NarrowedFiles,
		ProtectedFiles:   cp.ProtectedFiles,
		RequiresApproval: cp.RequiresApproval,
		ModifyOnly:       cp.ModifyOnly,
		MaxPatchFiles:    cp.MaxPatchFiles,
		MaxModifiedLines: cp.MaxModifiedLines,
		DiffShape:        cp.DiffShape,
		HoldoutChecks:    cp.HoldoutChecks,
		MaxPerDay:        cp.MaxPerDay,
		Evidence:         cp.Evidence,
		Oracle:           cp.Oracle,
		Monitor:          cp.Monitor,
		Author:           cp.Author,
		ReleaseAgeCheck:  cp.ReleaseAgeCheck,
		CompanionFiles:   cp.CompanionFiles,
		RequiredLabel:    cp.RequiredLabel,
		Requires:         cp.Requires,
	}
}

// Adopted reports whether the policy adopts external pull requests rather than deciding the
// factory's own.
func (m MergeClass) Adopted() bool { return m.Author != nil && m.Author.Adopt != nil }

// MinClean is the clean shadow decisions the policy's promotion needs without an exception.
func (m MergeClass) MinClean(name string) int { return m.Class().MinClean(name) }

// MergeClass returns a deep copy of c without its Live or Shadow mode.
func (c Class) MergeClass() MergeClass {
	cp := c.DeepCopy()
	return MergeClass{
		Description:      cp.Description,
		AllowedFiles:     cp.AllowedFiles,
		NarrowedFiles:    cp.NarrowedFiles,
		ProtectedFiles:   cp.ProtectedFiles,
		RequiresApproval: cp.RequiresApproval,
		ModifyOnly:       cp.ModifyOnly,
		MaxPatchFiles:    cp.MaxPatchFiles,
		MaxModifiedLines: cp.MaxModifiedLines,
		DiffShape:        cp.DiffShape,
		HoldoutChecks:    cp.HoldoutChecks,
		MaxPerDay:        cp.MaxPerDay,
		Evidence:         cp.Evidence,
		Oracle:           cp.Oracle,
		Monitor:          cp.Monitor,
		Author:           cp.Author,
		ReleaseAgeCheck:  cp.ReleaseAgeCheck,
		CompanionFiles:   cp.CompanionFiles,
		RequiredLabel:    cp.RequiredLabel,
		Requires:         cp.Requires,
	}
}

// Class is one merge class (§5.2): live classes auto-merge once policy-bot agrees; a shadow
// class is decided exactly like a live one and never armed until the wave (R32, owner,
// 2026-09-27); an unmarked class is disabled. Never both.
// +kubebuilder:object:generate=true
type Class struct {
	// Description says what the class is for, in plain words, for the settings pages and
	// `kubectl get`; the gate never reads it.
	// +kubebuilder:validation:MaxLength=280
	Description string `json:"description,omitempty"`
	Live        bool   `json:"live,omitempty"`
	Shadow      bool   `json:"shadow,omitempty"`
	// AllowedFiles: every changed path matches one of these regular expressions.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	AllowedFiles []string `json:"allowedFiles,omitempty"`
	// NarrowedFiles is a binding's allowedFiles over a policy that has its own (MR3): every changed
	// path matches one of these too. Set only by resolving a binding; refused on a MergePolicy.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	NarrowedFiles []string `json:"narrowedFiles,omitempty"`
	// ProtectedFiles: no changed path matches one of these.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	ProtectedFiles []string `json:"protectedFiles,omitempty"`
	// RequiresApproval asks a human about a pull request touching one of its paths, even in a live
	// class (merge policies MP12).
	// +kubebuilder:validation:MaxItems=16
	RequiresApproval []ApprovalLeaf `json:"requiresApproval,omitempty"`
	// ModifyOnly: no file is added, deleted, renamed or copied.
	ModifyOnly bool `json:"modifyOnly,omitempty"`
	// MaxPatchFiles caps the changed files and MaxModifiedLines their additions plus deletions,
	// as GitHub counts them (policy-bot's modified_lines); 0 is no cap.
	MaxPatchFiles    int `json:"maxPatchFiles,omitempty"`
	MaxModifiedLines int `json:"maxModifiedLines,omitempty"`
	// DiffShape is a shape every changed line must have: links-only is R52's LinksOnly.
	// +kubebuilder:validation:MaxLength=512
	DiffShape string `json:"diffShape,omitempty"`
	// HoldoutChecks are checks GitHub Actions must report SUCCESS on the head; their workflow is
	// a gate path, so the pull request cannot change them (loops L7).
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=1024
	HoldoutChecks []string `json:"holdoutChecks,omitempty"`
	// MaxPerDay caps this class's merges a day, under merge.maxPerDay; 0 is no cap of its own.
	MaxPerDay int `json:"maxPerDay,omitempty"`
	// Evidence is how many clean shadow decisions a promotion needs (MR4): low is 5, for a class
	// whose diff a shape or an oracle checks mechanically; standard, the default, is 20.
	// +kubebuilder:validation:Enum=low;standard
	Evidence string `json:"evidence,omitempty"`
	// Oracle makes the class oracle-backed (MC4–MC6): its tasks are a detector's findings, and its
	// gate holds only when the head no longer holds them and changes nothing else.
	Oracle *Oracle `json:"oracle,omitempty"`
	// Monitor watches each merge of the class after it lands (merge gate classes MC7): main's CI
	// for every merge, the detector's next runs for an oracle class, Flux on the factory's cluster,
	// and follow-up fixes for FollowUpWindow. Absent: no monitor, as before.
	Monitor *Monitor `json:"monitor,omitempty"`
	// Author is how the class's pull requests are authored (MC1's author row): absent, the agents
	// App through a task's runs; Adopt, another author's pull requests the factory adopts (MC10).
	Author *Author `json:"author,omitempty"`
	// ReleaseAgeCheck names the commit status that holds the release age (MC11), posted by an
	// adopted login: Renovate's renovate/stability-days.
	// +kubebuilder:validation:MaxLength=1024
	ReleaseAgeCheck string `json:"releaseAgeCheck,omitempty"`
	// CompanionFiles are the paths an added-entry pull request may append to beside its one added
	// entry (a knowledge base's index and log), anchored like AllowedFiles.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	CompanionFiles []string `json:"companionFiles,omitempty"`
	// RequiredLabel is the label an adopted pull request must carry, set by an adopted login, for
	// the factory to adopt it (K11): RunLore's ready-to-merge.
	// +kubebuilder:validation:MaxLength=253
	RequiredLabel string `json:"requiredLabel,omitempty"`
	// Requires names workflow stages that every task touching this class's paths must run (WR6):
	// plan, tests, judge, review. Adds stages to any workflow and never removes one.
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:MaxLength=16
	// +kubebuilder:validation:items:Enum=plan;tests;judge;review
	Requires []string `json:"requires,omitempty"`
}

// ApprovalLeaf asks a human about a pull request touching one of its paths, even in a live class.
// +kubebuilder:object:generate=true
type ApprovalLeaf struct {
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	ChangedFiles []string `json:"changedFiles"`
}

// Author is a class's author mode.
// +kubebuilder:object:generate=true
type Author struct {
	Adopt *Adopt `json:"adopt,omitempty"`
}

// Adopt names the logins whose pull requests are adopted and the head branches they come from
// (gh-aw's allowed-branches); every commit and every push of the branch must be theirs.
// +kubebuilder:object:generate=true
type Adopt struct {
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	Logins []string `json:"logins"`
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	Branches []string `json:"branches"`
}

// Adopted reports whether the class's pull requests are another author's, adopted (MC10).
func (c Class) Adopted() bool { return c.Author != nil && c.Author.Adopt != nil }

// Monitor is a class's post-merge watch (MC7).
// +kubebuilder:object:generate=true
type Monitor struct {
	// OracleRuns is how many scheduled detector runs after the merge must report none of the
	// targets the pull request wrote; 0 reads none. Needs an oracle.
	OracleRuns int `json:"oracleRuns,omitempty"`
	// Flux watches the factory's cluster once its source holds the merge commit.
	Flux *FluxWatch `json:"flux,omitempty"`
	// Verification reads an internal principal's verdict on the fix (RA11, R12): the merge is
	// watched for it, for at most Within after the merge. Unset by default.
	Verification *VerificationWatch `json:"verification,omitempty"`
	// FollowUpWindow is how long later merges are read for a fix of this one (MC8), and when the
	// monitor's verdict is final.
	FollowUpWindow Duration `json:"followUpWindow"`
}

// VerificationWatch is how long after the merge RunLore's verdict is waited for, and accepted.
// +kubebuilder:object:generate=true
type VerificationWatch struct {
	Within Duration `json:"within"`
}

// FluxWatch names the GitRepository that delivers the repository to the factory's cluster, and
// how long after it reaches the merge commit a Kustomization or HelmRelease turning not Ready is
// blamed on the merge.
// +kubebuilder:object:generate=true
type FluxWatch struct {
	// Source is namespace/name. Optional in the type, since a MergePolicy leaves it to the
	// repository's merge.fluxSource; required on a class.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Source string   `json:"source,omitempty"`
	Within Duration `json:"within"`
}

// Namespace splits Source.
func (f FluxWatch) Namespace() string { ns, _, _ := strings.Cut(f.Source, "/"); return ns }

// Name splits Source.
func (f FluxWatch) Name() string { _, n, _ := strings.Cut(f.Source, "/"); return n }

var sourceRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?/[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// Oracle is an oracle-backed class's detector: the probe that turns its findings into a task,
// the workflow on main whose runs report them, and the most findings one task holds.
// +kubebuilder:object:generate=true
type Oracle struct {
	// +kubebuilder:validation:MaxLength=1024
	Probe string `json:"probe"`
	// Workflow is optional in the type, since a MergePolicy leaves it to the binding's
	// oracleWorkflow; required on a class.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Workflow    string `json:"workflow,omitempty"`
	MaxFindings int    `json:"maxFindings"`
}

// The evidence levels of a class (MR4) and the clean decisions low needs; standard needs
// DefaultMinPromotionEvidence.
const (
	EvidenceLow      = "low"
	EvidenceStandard = "standard"
	MinCleanLow      = 5
)

// MinClean is the clean shadow decisions the class's promotion needs without an exception: 5 at
// evidence low, else 20. Low counts only where a shape (declared or built in) or an oracle checks
// the diff; validateClass refuses it otherwise.
func (c Class) MinClean(name string) int {
	if c.Evidence == EvidenceLow && (c.DiffShape != "" || BuiltInShape(name) != "" || c.Oracle != nil) {
		return MinCleanLow
	}
	return DefaultMinPromotionEvidence
}

// MaxFindings bounds oracle.maxFindings: the Task CRD's spec.findings holds at most 16.
const MaxFindings = 16

var workflowRE = regexp.MustCompile(`^[A-Za-z0-9._-]+\.ya?ml$`)

// DiffShapes are the shapes diffShape names.
func DiffShapes() []string {
	return []string{"links-only", "semver-patch", "semver-minor", "added-entry"}
}

// BuiltInShape is the diff shape the gate holds a class to when its config names none: R52's
// promise of the two entry classes (MC1: their conditions become built-in defaults at parity).
func BuiltInShape(name string) string {
	if name == "docs-links" {
		return "links-only"
	}
	return ""
}

// branchPrefixRE is an adopted branch pattern: ^ and a literal prefix, no other metacharacter, so
// an alternation or a wildcard cannot widen it to every branch.
var branchPrefixRE = regexp.MustCompile(`^\^[A-Za-z0-9._/-]+$`)

// ReadsDiff reports whether deciding the class reads the pull request's diff: a built-in shape
// or any declared condition on its files.
func (c Class) ReadsDiff(name string) bool {
	return BuiltInShape(name) != "" || len(c.AllowedFiles) > 0 || len(c.ProtectedFiles) > 0 || c.ModifyOnly ||
		c.MaxPatchFiles > 0 || c.MaxModifiedLines > 0 || c.DiffShape != "" || c.Oracle != nil
}

// Bounded reports whether the class says which diffs it may merge (MR6): allowedFiles, a declared
// diff shape, or the shape built in under its name. links-only carries its own Markdown path bound,
// whatever allowedFiles says.
func (c Class) Bounded(name string) bool {
	return len(c.AllowedFiles) > 0 || c.DiffShape != "" || BuiltInShape(name) != ""
}

// ValidateClass reports one class's problems, for a class read from outside this config.
func ValidateClass(name string, cl Class, bad func(string, ...any)) { validateClass(name, cl, bad) }

// validateClass reports one class's problems.
func validateClass(name string, cl Class, bad func(string, ...any)) {
	if n := utf8.RuneCountInString(cl.Description); n > 280 {
		bad("class %s: description is %d characters, at most 280", name, n)
	}
	if cl.Live && cl.Shadow {
		bad("class %s: live and shadow never together (R32)", name)
	}
	switch {
	case cl.Evidence != "" && cl.Evidence != EvidenceLow && cl.Evidence != EvidenceStandard:
		bad("class %s: evidence %q is low or standard", name, cl.Evidence)
	case cl.Evidence == EvidenceLow && cl.DiffShape == "" && BuiltInShape(name) == "" && cl.Oracle == nil:
		bad("class %s: evidence: low needs a diffShape or an oracle: without one, 5 decisions prove too little (MR4)", name)
	}
	if name == "review" {
		bad("class review: review is not a policy")
	}
	if m := cl.Monitor; m != nil {
		if w := m.FollowUpWindow.Duration; w < time.Hour || w > 720*time.Hour {
			bad("class %s: monitor.followUpWindow must be 1h..720h", name)
		}
		switch {
		case m.OracleRuns < 0 || m.OracleRuns > 5:
			bad("class %s: monitor.oracleRuns must be 0..5", name)
		case m.OracleRuns > 0 && cl.Oracle == nil:
			bad("class %s: monitor.oracleRuns needs an oracle", name)
		}
		if v := m.Verification; v != nil && (v.Within.Duration < time.Minute || v.Within.Duration > m.FollowUpWindow.Duration) {
			bad("class %s: monitor.verification.within must be 1m..followUpWindow", name)
		}
		if f := m.Flux; f != nil {
			if !sourceRE.MatchString(f.Source) {
				bad("class %s: monitor.flux.source %q is not namespace/name", name, f.Source)
			}
			if f.Within.Duration < 5*time.Minute || f.Within.Duration > 6*time.Hour {
				bad("class %s: monitor.flux.within must be 5m..6h", name)
			}
		}
	}
	if len(cl.Requires) > 4 {
		bad("class %s: requires has at most 4 stages", name)
	}
	for i, st := range cl.Requires {
		if !slices.Contains(RequiredStages(), st) || slices.Index(cl.Requires, st) != i {
			bad("class %s: requires %q is not one of %v, or is listed twice", name, st, RequiredStages())
		}
	}
	for _, f := range []struct {
		key  string
		pats []string
	}{{"allowedFiles", cl.AllowedFiles}, {"narrowedFiles", cl.NarrowedFiles}, {"companionFiles", cl.CompanionFiles}} {
		for _, p := range f.pats {
			// The match is unanchored, as policy-bot's is, so an allow list must say where a path
			// begins and ends: "docs/.+" would allow "evil/docs/x".
			if !strings.HasPrefix(p, "^") || !strings.HasSuffix(p, "$") {
				bad("class %s: %s %q must start with ^ and end with $", name, f.key, p)
			}
		}
	}
	for _, f := range []struct {
		key  string
		pats []string
	}{{"allowedFiles", cl.AllowedFiles}, {"narrowedFiles", cl.NarrowedFiles}, {"companionFiles", cl.CompanionFiles}, {"protectedFiles", cl.ProtectedFiles}} {
		for _, p := range f.pats {
			if _, err := regexp.Compile(p); err != nil || p == "" {
				bad("class %s: %s %q is not a regular expression", name, f.key, p)
			}
		}
	}
	for _, a := range cl.RequiresApproval {
		if len(a.ChangedFiles) == 0 {
			bad("class %s: a requiresApproval's changedFiles is empty: it asks about nothing", name)
		}
		for _, p := range a.ChangedFiles {
			if _, err := regexp.Compile(p); err != nil || p == "" {
				bad("class %s: requiresApproval %q is not a regular expression", name, p)
			}
		}
	}
	for _, f := range []struct {
		key string
		n   int
	}{{"maxPatchFiles", cl.MaxPatchFiles}, {"maxModifiedLines", cl.MaxModifiedLines}, {"maxPerDay", cl.MaxPerDay}} {
		if f.n < 0 {
			bad("class %s: %s must be 0 (no cap) or more", name, f.key)
		}
	}
	if cl.DiffShape != "" && !slices.Contains(DiffShapes(), cl.DiffShape) {
		bad("class %s: diffShape %q is not one of %v", name, cl.DiffShape, DiffShapes())
	}
	if b := BuiltInShape(name); b != "" && cl.DiffShape != "" && cl.DiffShape != b {
		bad("class %s: diffShape is %s, built in (R52)", name, b)
	}
	semver := cl.DiffShape == "semver-patch" || cl.DiffShape == "semver-minor"
	entry := cl.DiffShape == "added-entry"
	switch {
	case (semver || entry) && !cl.Adopted():
		bad("class %s: diffShape %s needs author.adopt (MC10)", name, cl.DiffShape)
	case cl.Adopted() && !semver && !entry:
		bad("class %s: author.adopt needs diffShape semver-patch, semver-minor or added-entry", name)
	case semver && !cl.ModifyOnly:
		bad("class %s: a semver shape needs modifyOnly: true", name)
	case entry && cl.ModifyOnly:
		bad("class %s: added-entry adds a file: modifyOnly must be false", name)
	case entry && len(cl.AllowedFiles) == 0:
		bad("class %s: added-entry needs allowedFiles: the entries' paths", name)
	case entry && len(cl.HoldoutChecks) == 0:
		bad("class %s: added-entry needs holdoutChecks: the validator's check", name)
	case entry && cl.RequiredLabel == "":
		bad("class %s: added-entry needs requiredLabel", name)
	case cl.DiffShape == "semver-minor" && cl.Live:
		bad("class %s: semver-minor is shadow only (M2)", name)
	case cl.ReleaseAgeCheck != "" && !cl.Adopted():
		bad("class %s: releaseAgeCheck needs author.adopt", name)
	}
	switch {
	case len(cl.CompanionFiles) > 0 && !entry:
		bad("class %s: companionFiles needs diffShape added-entry", name)
	case cl.RequiredLabel != "" && !cl.Adopted():
		bad("class %s: requiredLabel needs author.adopt", name)
	case utf8.RuneCountInString(cl.RequiredLabel) > 50:
		bad("class %s: requiredLabel is 1..50 characters, GitHub's bound", name)
	}
	if cl.Adopted() {
		a := cl.Author.Adopt
		if len(a.Logins) == 0 || len(a.Branches) == 0 {
			bad("class %s: author.adopt needs logins and branches", name)
		}
		for _, l := range a.Logins {
			// A User account can never be a Bot: only the suffix says the login is an App's.
			if !strings.HasSuffix(l, "[bot]") {
				bad("class %s: author.adopt.logins %q must end in [bot]", name, l)
			}
		}
		for _, b := range a.Branches {
			if !branchPrefixRE.MatchString(b) {
				bad("class %s: author.adopt.branches %q must be ^ and a literal prefix", name, b)
			}
		}
	}
	if o := cl.Oracle; o != nil {
		switch {
		case o.Probe != "dead-links":
			bad("class %s: oracle.probe %q is not dead-links", name, o.Probe)
		case !workflowRE.MatchString(o.Workflow):
			bad("class %s: oracle.workflow %q is not a workflow file name", name, o.Workflow)
		case o.MaxFindings < 1 || o.MaxFindings > MaxFindings:
			bad("class %s: oracle.maxFindings must be 1..%d", name, MaxFindings)
		case len(cl.HoldoutChecks) == 0:
			bad("class %s: an oracle needs holdoutChecks, or the head goes unproven (MC5)", name)
		case cl.DiffShape != "links-only":
			bad("class %s: an oracle reads link targets: diffShape must be links-only", name)
		case !cl.ModifyOnly:
			bad("class %s: an oracle needs modifyOnly: true, or an added or deleted empty file would pass", name)
		case len(cl.AllowedFiles) == 0:
			bad("class %s: an oracle needs allowedFiles, or the probe drops every finding", name)
		case cl.MaxModifiedLines > 0 && cl.MaxModifiedLines < 2*o.MaxFindings:
			bad("class %s: maxModifiedLines %d is under 2 x oracle.maxFindings (%d): one retarget changes two lines", name, cl.MaxModifiedLines, 2*o.MaxFindings)
		}
	}
	seen := map[string]bool{}
	for _, h := range cl.HoldoutChecks {
		if h == "" || seen[h] {
			bad("class %s: holdoutChecks names %q empty or twice", name, h)
		}
		seen[h] = true
	}
}
