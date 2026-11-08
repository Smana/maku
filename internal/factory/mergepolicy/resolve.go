// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/Smana/maku/internal/factory/config"
)

// ErrGuardrail marks a class refused by the central config rather than by its own shape, for the
// load-error metric's reason.
var ErrGuardrail = errors.New("guardrail")

// Resolved is a class as the gate would read it, with what the gate does not read yet.
type Resolved struct {
	// Class is all's leaves, the deny and the guardrails: the class itself when it has no any.
	Class config.Class
	// Branches are Class with each branch of any added, one variant per branch; nil without any.
	Branches []config.Class
	Approval []string // requiresApproval's paths
	Profile  config.HarnessProfile
}

// Variants are the classes the evaluator decides on, one at a time: the class holds when one
// variant does.
func (r Resolved) Variants() []config.Class {
	if r.Branches != nil {
		return r.Branches
	}
	return []config.Class{r.Class}
}

// inShadow is r with every live variant in shadow (stage 4's pause and MP8's demotion).
func (r Resolved) inShadow() Resolved {
	shadow := func(cl config.Class) config.Class {
		if cl.Live {
			cl.Live, cl.Shadow = false, true
		}
		return cl
	}
	r.Class = shadow(r.Class)
	if r.Branches != nil {
		bs := make([]config.Class, len(r.Branches))
		for i, b := range r.Branches {
			bs[i] = shadow(b)
		}
		r.Branches = bs
	}
	return r
}

// Resolve turns a parsed class into the config.Class the gate decides on, under the guardrails:
// they add protected paths and mandatory checks, an undeclared cap inherits theirs, and a declared
// cap above theirs is refused (MP6). Each branch of any is resolved the same way.
func Resolve(name string, p ClassPolicy, c *config.Config) (Resolved, error) {
	profile, ok := c.HarnessProfiles[p.Harness]
	if !ok {
		return Resolved{}, fmt.Errorf("class %s: %w: harness %q is not in harnessProfiles", name, ErrGuardrail, p.Harness)
	}
	adopted := p.Author == AuthorAdopt
	if none := profile.Template == config.TemplateNone; none != adopted {
		return Resolved{}, fmt.Errorf("class %s: %w: harness %q: template none runs nothing, and only an adopted class takes it", name, ErrGuardrail, p.Harness)
	}
	cl := config.Class{Live: p.Mode == ModeLive, Shadow: p.Mode == ModeShadow,
		Checks: profile.Checks, TestFirst: profile.TestFirst, MaxPerDay: p.Limits.MaxPerDay, Monitor: p.Monitor}
	if adopted {
		cl.Author = &config.Author{Adopt: p.Adopt}
	}
	for _, l := range p.Conditions.All {
		apply(&cl, l)
	}
	for _, d := range p.Conditions.Deny {
		cl.ProtectedFiles = append(cl.ProtectedFiles, d.ProtectedFiles...)
	}
	r := Resolved{Profile: profile}
	for _, a := range p.RequiresApproval {
		r.Approval = append(r.Approval, a.ChangedFiles...)
	}
	for _, branch := range p.Conditions.Any {
		v := clone(cl)
		for _, l := range branch {
			apply(&v, l)
		}
		r.Branches = append(r.Branches, v)
	}
	var errs []error
	for i := range r.Branches {
		errs = append(errs, guard(name, &r.Branches[i], c.MergeGuardrails))
	}
	errs = append(errs, guard(name, &cl, c.MergeGuardrails))
	if err := errors.Join(errs...); err != nil {
		return Resolved{}, err
	}
	r.Class = cl
	return r, nil
}

// apply adds one leaf to a class.
func apply(cl *config.Class, l Leaf) {
	cl.AllowedFiles = append(cl.AllowedFiles, l.AllowedFiles...)
	cl.HoldoutChecks = append(cl.HoldoutChecks, l.HoldoutChecks...)
	cl.CompanionFiles = append(cl.CompanionFiles, l.CompanionFiles...)
	switch {
	case l.ModifyOnly != nil:
		cl.ModifyOnly = *l.ModifyOnly
	case l.MaxPatchFiles != nil:
		cl.MaxPatchFiles = *l.MaxPatchFiles
	case l.MaxModifiedLines != nil:
		cl.MaxModifiedLines = *l.MaxModifiedLines
	case l.DiffShape != nil:
		cl.DiffShape = *l.DiffShape
	case l.ReleaseAgeCheck != nil:
		cl.ReleaseAgeCheck = *l.ReleaseAgeCheck
	case l.Oracle != nil:
		cl.Oracle = l.Oracle
	case l.RequiredLabel != nil:
		cl.RequiredLabel = *l.RequiredLabel
	}
}

// clone copies a class's lists, so a branch's additions stay its own.
func clone(cl config.Class) config.Class {
	cl.AllowedFiles = slices.Clone(cl.AllowedFiles)
	cl.ProtectedFiles = slices.Clone(cl.ProtectedFiles)
	cl.HoldoutChecks = slices.Clone(cl.HoldoutChecks)
	cl.CompanionFiles = slices.Clone(cl.CompanionFiles)
	return cl
}

// guard applies the guardrails to one variant. Parse has refused a negative cap.
func guard(name string, cl *config.Class, g config.MergeGuardrails) error {
	for _, pf := range g.ProtectedFiles {
		if !slices.Contains(cl.ProtectedFiles, pf) {
			cl.ProtectedFiles = append(cl.ProtectedFiles, pf)
		}
	}
	for _, n := range g.MandatoryChecks {
		if !slices.Contains(cl.HoldoutChecks, n) {
			cl.HoldoutChecks = append(cl.HoldoutChecks, n)
		}
	}
	for _, f := range []struct {
		key   string
		value *int
		limit int
	}{{"maxPatchFiles", &cl.MaxPatchFiles, g.MaxPatchFiles}, {"maxModifiedLines", &cl.MaxModifiedLines, g.MaxModifiedLines}, {"maxPerDay", &cl.MaxPerDay, g.MaxPerDay}} {
		switch {
		case f.limit == 0:
		case *f.value == 0:
			*f.value = f.limit
		case *f.value > f.limit:
			return fmt.Errorf("class %s: %w: %s %d is over the guardrail's %d", name, ErrGuardrail, f.key, *f.value, f.limit)
		}
	}
	return nil
}

// OnlyGuardrail is true when every fault err joins is ErrGuardrail's: a file that is also invalid
// is invalid, whatever errors.Is finds somewhere in the join.
func OnlyGuardrail(err error) bool {
	if err == nil {
		return false
	}
	switch e := err.(type) { //nolint:errorlint // walking the tree is the point: errors.Is answers "any", this is "all"
	case interface{ Unwrap() []error }:
		for _, c := range e.Unwrap() {
			if !OnlyGuardrail(c) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		if e.Unwrap() == ErrGuardrail { //nolint:errorlint // the sentinel itself, one level down
			return true
		}
		return OnlyGuardrail(e.Unwrap())
	}
	return false
}

// Validate is the whole file against the central config: a file with no classes has no policy and
// passes; one with classes declares docs-links and revert, never review, and each class passes
// Resolve and the gate's own class check, every variant of it. review stays central: its settings
// are the repository's merge block (approveToMerge, maintainers), which no class file holds.
func Validate(f *File, c *config.Config) error {
	if f.Classes == nil {
		return nil
	}
	var errs []error
	for _, name := range []string{"docs-links", "revert"} {
		if _, ok := f.Classes[name]; !ok {
			errs = append(errs, fmt.Errorf("class %s is required", name))
		}
	}
	if _, ok := f.Classes["review"]; ok {
		errs = append(errs, errors.New("class review is implicit: it is never declared"))
	}
	for _, name := range slices.Sorted(maps.Keys(f.Classes)) {
		r, err := Resolve(name, f.Classes[name], c)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, v := range r.Variants() {
			config.ValidateClass(name, v, func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) })
		}
	}
	return errors.Join(errs...)
}
