// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
)

// Export writes a repository's central classes as a policy file (MP15): behaviour preserved, so
// each class keeps its mode. review stays implicit. A class whose checks and testFirst no profile
// holds is an error: the operator adds the profile first.
func Export(rc *config.Repository, c *config.Config) (*File, error) {
	f := &File{Factory: c.Factory, Classes: map[string]ClassPolicy{}}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(rc.Classes)) {
		if name == "review" {
			continue
		}
		p, err := exportClass(rc.Classes[name], c)
		if err != nil {
			errs = append(errs, fmt.Errorf("class %s: %w", name, err))
			continue
		}
		f.Classes[name] = p
	}
	return f, errors.Join(errs...)
}

func exportClass(cl config.Class, c *config.Config) (ClassPolicy, error) {
	harness, ok := profileFor(cl, c)
	if !ok {
		return ClassPolicy{}, fmt.Errorf("no harness profile holds checks=%t testFirst=%t adopted=%t", cl.Checks, cl.TestFirst, cl.Adopted())
	}
	p := ClassPolicy{Mode: ModeOff, Author: AuthorAgents, Harness: harness, Limits: Limits{MaxPerDay: cl.MaxPerDay}, Monitor: cl.Monitor}
	switch {
	case cl.Live:
		p.Mode = ModeLive
	case cl.Shadow:
		p.Mode = ModeShadow
	}
	if cl.Adopted() {
		p.Author, p.Adopt = AuthorAdopt, cl.Author.Adopt
	}
	add := func(l Leaf) { p.Conditions.All = append(p.Conditions.All, l) }
	if len(cl.AllowedFiles) > 0 {
		add(Leaf{AllowedFiles: cl.AllowedFiles})
	}
	if cl.ModifyOnly {
		add(Leaf{ModifyOnly: new(true)})
	}
	if cl.MaxPatchFiles > 0 {
		add(Leaf{MaxPatchFiles: new(cl.MaxPatchFiles)})
	}
	if cl.MaxModifiedLines > 0 {
		add(Leaf{MaxModifiedLines: new(cl.MaxModifiedLines)})
	}
	if cl.DiffShape != "" {
		add(Leaf{DiffShape: new(cl.DiffShape)})
	}
	if len(cl.HoldoutChecks) > 0 {
		add(Leaf{HoldoutChecks: cl.HoldoutChecks})
	}
	if cl.ReleaseAgeCheck != "" {
		add(Leaf{ReleaseAgeCheck: new(cl.ReleaseAgeCheck)})
	}
	if cl.Oracle != nil {
		add(Leaf{Oracle: cl.Oracle})
	}
	if len(cl.CompanionFiles) > 0 {
		add(Leaf{CompanionFiles: cl.CompanionFiles})
	}
	if cl.RequiredLabel != "" {
		add(Leaf{RequiredLabel: new(cl.RequiredLabel)})
	}
	if len(cl.ProtectedFiles) > 0 {
		p.Conditions.Deny = []DenyLeaf{{ProtectedFiles: cl.ProtectedFiles}}
	}
	return p, nil
}

// profileFor is the first profile by name that runs the class as it runs today.
func profileFor(cl config.Class, c *config.Config) (string, bool) {
	for _, name := range slices.Sorted(maps.Keys(c.HarnessProfiles)) {
		p := c.HarnessProfiles[name]
		if (p.Template == config.TemplateNone) == cl.Adopted() && p.Checks == cl.Checks && p.TestFirst == cl.TestFirst {
			return name, true
		}
	}
	return "", false
}

// Marshal writes the file. It is read back through Parse by the caller's test, never trusted.
func Marshal(f *File) ([]byte, error) { return yaml.Marshal(f) }

// Compare lists how the repository's classes differ from the central ones, class by class, with
// path and check lists read as sets. review is implicit on both sides.
func Compare(rc *config.Repository, f *File, c *config.Config) []string {
	var out []string
	names := map[string]bool{}
	for n := range rc.Classes {
		names[n] = true
	}
	for n := range f.Classes {
		names[n] = true
	}
	delete(names, "review")
	for _, name := range slices.Sorted(maps.Keys(names)) {
		central, inCentral := rc.Classes[name]
		repo, inRepo := f.Classes[name]
		switch {
		case !inRepo:
			out = append(out, name+": only in the central config")
		case !inCentral:
			out = append(out, name+": only in the repository")
		default:
			r, err := Resolve(name, repo, c)
			if err != nil {
				out = append(out, fmt.Sprintf("%s: %v", name, err))
				continue
			}
			if len(r.Approval) > 0 {
				out = append(out, name+": requiresApproval has no central form")
			}
			if a, b := canonical(central), canonical(r.Class); a != b {
				out = append(out, fmt.Sprintf("%s: central %s, repository %s", name, a, b))
			}
		}
	}
	return out
}

// canonical is a class's JSON with its sets sorted, for comparison and for the message.
func canonical(cl config.Class) string {
	for _, list := range []*[]string{&cl.AllowedFiles, &cl.ProtectedFiles, &cl.HoldoutChecks, &cl.CompanionFiles} {
		*list = slices.Sorted(slices.Values(*list))
	}
	b, err := json.Marshal(cl)
	if err != nil { // strings, ints, bools and Durations: Marshal cannot fail
		return err.Error()
	}
	return string(b)
}
