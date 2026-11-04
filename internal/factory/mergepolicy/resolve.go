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
	Class    config.Class
	Approval []string // requiresApproval's paths; the gate reads them from stage 5
	Profile  config.HarnessProfile
}

// Resolve turns a parsed class into the config.Class the gate decides on, under the guardrails:
// they add protected paths and mandatory checks, an undeclared cap inherits theirs, and a declared
// cap above theirs is refused (MP6).
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
	for _, d := range p.Conditions.Deny {
		cl.ProtectedFiles = append(cl.ProtectedFiles, d.ProtectedFiles...)
	}
	var approval []string
	for _, a := range p.RequiresApproval {
		approval = append(approval, a.ChangedFiles...)
	}
	g := c.MergeGuardrails
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
			return Resolved{}, fmt.Errorf("class %s: %w: %s %d is over the guardrail's %d", name, ErrGuardrail, f.key, *f.value, f.limit)
		}
	}
	return Resolved{Class: cl, Approval: approval, Profile: profile}, nil
}

// Validate is the whole file against the central config: a file with no classes has no policy and
// passes; one with classes declares docs-links and revert, never review, and each class passes
// Resolve and the gate's own class check.
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
		config.ValidateClass(name, r.Class, func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) })
	}
	return errors.Join(errs...)
}
