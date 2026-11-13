// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/Smana/maku/internal/factory/config"
)

// The structural verdicts (MP8).
const (
	KindUnchanged  = "unchanged"
	KindTightening = "tightening"
	KindRelaxation = "relaxation"
)

// Change is one class's structural verdict from one policy to the next, and why it is not a
// tightening.
type Change struct {
	Class string
	Kind  string
	Why   []string
}

// Structural compares two valid policies class by class (MP8). A change is a tightening only when
// every variant of the new class is bounded by a variant of the old one on every row: fewer
// allowed paths, more denied ones, a wider approval subset, lower caps, more evidence, a lower
// mode. Anything else, or anything it cannot prove, such as a rewritten pattern or another
// harness profile, is a relaxation. Simulation illustrates the impact; only this proves it. When
// either file binds central policies, both are compared on their resolved classes (B15), each
// binding resolved for rc.
func Structural(old, next *File, rc *config.Repository, c *config.Config) ([]Change, error) {
	if old.Bound() || next.Bound() {
		ro, err := resolvedFile(old, rc, c)
		if err != nil {
			return nil, err
		}
		rn, err := resolvedFile(next, rc, c)
		if err != nil {
			return nil, err
		}
		return StructuralResolved(ro, rn), nil
	}
	names := map[string]bool{}
	for n := range old.Classes {
		names[n] = true
	}
	for n := range next.Classes {
		names[n] = true
	}
	var out []Change
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(names)) {
		o, inOld := old.Classes[name]
		n, inNext := next.Classes[name]
		switch {
		case !inNext:
			out = append(out, Change{Class: name, Kind: KindTightening})
			continue
		case !inOld && n.Mode == ModeOff:
			out = append(out, Change{Class: name, Kind: KindTightening})
			continue
		case !inOld:
			out = append(out, Change{Class: name, Kind: KindRelaxation, Why: []string{"the class is new"}})
			continue
		}
		ro, err := Resolve(name, o, c)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rn, err := Resolve(name, n, c)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, classChange(name, o, n, ro, rn))
	}
	return out, errors.Join(errs...)
}

// resolvedFile is a file's classes as the gate would read them: every binding must resolve.
func resolvedFile(f *File, rc *config.Repository, c *config.Config) (map[string]Resolved, error) {
	if f.Bound() {
		classes, refused := ResolveBindings(f, rc, c)
		var errs []error
		for _, b := range f.MergePolicies {
			if r, ok := refused[b.Name]; ok {
				errs = append(errs, fmt.Errorf("binding %s: %s: %s", b.Name, r.Reason, r.Message))
			}
		}
		return classes, errors.Join(errs...)
	}
	out := map[string]Resolved{}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(f.Classes)) {
		r, err := Resolve(name, f.Classes[name], c)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out[name] = r
	}
	return out, errors.Join(errs...)
}

// StructuralResolved compares two sets of resolved classes class by class, as Structural compares
// two files (MP8): a class removed or new in mode off is a tightening, any other new class a
// relaxation, and a class in both is ClassChange's verdict on each variant.
func StructuralResolved(old, next map[string]Resolved) []Change {
	names := map[string]bool{}
	for n := range old {
		names[n] = true
	}
	for n := range next {
		names[n] = true
	}
	var out []Change
	for _, name := range slices.Sorted(maps.Keys(names)) {
		o, inOld := old[name]
		n, inNext := next[name]
		var c Change
		switch {
		case !inNext:
			c = Change{Kind: KindTightening}
		case !inOld && !n.Class.Live && !n.Class.Shadow:
			c = Change{Kind: KindTightening}
		case !inOld:
			c = Change{Kind: KindRelaxation, Why: []string{"the class is new"}}
		default:
			c = resolvedChange(o, n)
		}
		c.Class = name
		out = append(out, c)
	}
	return out
}

// ClassChange is one resolved class's structural verdict from old to next (B11): unchanged when
// equal as written, a tightening when next is bounded by old on every row, else a relaxation and
// why. Its Class is the caller's to set.
func ClassChange(old, next config.Class) Change {
	return resolvedChange(resolvedOf(old), resolvedOf(next))
}

// resolvedChange is ClassChange over every variant: each of next's must be bounded by one of old's.
func resolvedChange(o, n Resolved) Change {
	if jsonOf(o.Variants()) == jsonOf(n.Variants()) && slices.Equal(o.Approval, n.Approval) {
		return Change{Kind: KindUnchanged}
	}
	var why []string
	if !superset(n.Approval, o.Approval) {
		why = append(why, "requiresApproval narrowed")
	}
	for i, v := range n.Variants() {
		best := []string(nil)
		for j, ov := range o.Variants() {
			w := append(floorLooser(v, ov), looser(v, ov)...)
			if len(w) == 0 {
				best = nil
				break
			}
			if j == 0 {
				best = w
			}
		}
		for _, w := range best {
			if len(n.Variants()) > 1 {
				w = fmt.Sprintf("branch %d: %s", i+1, w)
			}
			why = append(why, w)
		}
	}
	if len(why) > 0 {
		return Change{Kind: KindRelaxation, Why: why}
	}
	return Change{Kind: KindTightening}
}

// floorLooser is why n's evaluation floor is not bounded by o's, on resolved classes: a policy
// sets the harness, checks and testFirst directly, where a class-shape file reads them from its
// harness profile (classChange).
func floorLooser(n, o config.Class) []string {
	var why []string
	if n.Harness != o.Harness {
		why = append(why, fmt.Sprintf("harness %q to %q: not proven evidence-neutral", o.Harness, n.Harness))
	}
	if !superset(o.HarnessAllowed, n.HarnessAllowed) {
		why = append(why, "harnessAllowed broadened or rewritten: not proven evidence-neutral")
	}
	if n.Escalation != nil && jsonOf(n.Escalation) != jsonOf(o.Escalation) {
		why = append(why, "escalation added or changed: not proven evidence-neutral")
	}
	if o.Checks && !n.Checks {
		why = append(why, "checks removed")
	}
	if o.TestFirst && !n.TestFirst {
		why = append(why, "testFirst removed")
	}
	return why
}

func classChange(name string, o, n ClassPolicy, ro, rn Resolved) Change {
	if jsonOf(o) == jsonOf(n) { // as written, every list in order
		return Change{Class: name, Kind: KindUnchanged}
	}
	var why []string
	if o.Harness != n.Harness {
		why = append(why, fmt.Sprintf("harness %s to %s: not proven evidence-neutral", o.Harness, n.Harness))
	}
	// The harness axes are no evidence row: a profile that may run and did not before, or a step
	// added or moved, is as unproven as another harness; a subset of the old ones is bounded.
	if !superset(o.HarnessAllowed, n.HarnessAllowed) {
		why = append(why, "harnessAllowed broadened or rewritten: not proven evidence-neutral")
	}
	if n.Escalation != nil && jsonOf(n.Escalation) != jsonOf(o.Escalation) {
		why = append(why, "escalation added or changed: not proven evidence-neutral")
	}
	if !superset(rn.Approval, ro.Approval) {
		why = append(why, "requiresApproval narrowed")
	}
	for i, v := range rn.Variants() {
		best := []string(nil)
		for j, ov := range ro.Variants() {
			w := looser(v, ov)
			if len(w) == 0 {
				best = nil
				break
			}
			if j == 0 {
				best = w
			}
		}
		for _, w := range best {
			if len(rn.Variants()) > 1 {
				w = fmt.Sprintf("branch %d: %s", i+1, w)
			}
			why = append(why, w)
		}
	}
	if len(why) > 0 {
		return Change{Class: name, Kind: KindRelaxation, Why: why}
	}
	return Change{Class: name, Kind: KindTightening}
}

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil { // the policy types hold strings, ints, bools and Durations
		return err.Error()
	}
	return string(b)
}

// looser is why n is not bounded by o, row by row: nil when it is.
func looser(n, o config.Class) []string {
	var why []string
	add := func(ok bool, row string) {
		if !ok {
			why = append(why, row)
		}
	}
	add(modeRank(n) <= modeRank(o), "mode raised")
	add(jsonOf(n.Author) == jsonOf(o.Author) || adoptNarrower(n.Author, o.Author), "authorship broadened")
	add(len(o.AllowedFiles) == 0 || (len(n.AllowedFiles) > 0 && superset(o.AllowedFiles, n.AllowedFiles)), "allowedFiles broadened or rewritten")
	add(len(o.CompanionFiles) == 0 && len(n.CompanionFiles) == 0 || superset(o.CompanionFiles, n.CompanionFiles), "companionFiles broadened or rewritten")
	add(superset(n.ProtectedFiles, o.ProtectedFiles), "protectedFiles narrowed")
	add(n.ModifyOnly || !o.ModifyOnly, "modifyOnly removed")
	add(capBounded(n.MaxPatchFiles, o.MaxPatchFiles), "maxPatchFiles raised or removed")
	add(capBounded(n.MaxModifiedLines, o.MaxModifiedLines), "maxModifiedLines raised or removed")
	add(capBounded(n.MaxPerDay, o.MaxPerDay), "maxPerDay raised or removed")
	add(o.DiffShape == "" || n.DiffShape == o.DiffShape, "diffShape changed or removed")
	add(superset(n.HoldoutChecks, o.HoldoutChecks), "holdoutChecks removed")
	add(o.ReleaseAgeCheck == "" || n.ReleaseAgeCheck == o.ReleaseAgeCheck, "releaseAgeCheck changed or removed")
	add(o.RequiredLabel == "" || n.RequiredLabel == o.RequiredLabel, "requiredLabel changed or removed")
	add(o.Oracle == nil || jsonOf(n.Oracle) == jsonOf(o.Oracle), "oracle changed or removed")
	add(o.Monitor == nil || jsonOf(n.Monitor) == jsonOf(o.Monitor), "monitor changed or removed: its evidence")
	return why
}

func modeRank(cl config.Class) int {
	switch {
	case cl.Live:
		return 2
	case cl.Shadow:
		return 1
	}
	return 0
}

// adoptNarrower: both adopt, and the new logins and branches are among the old ones.
func adoptNarrower(n, o *config.Author) bool {
	if n == nil || o == nil || n.Adopt == nil || o.Adopt == nil {
		return false
	}
	return superset(o.Adopt.Logins, n.Adopt.Logins) && superset(o.Adopt.Branches, n.Adopt.Branches)
}

// capBounded: no old cap, or a new one at most the old.
func capBounded(n, o int) bool { return o == 0 || (n > 0 && n <= o) }

// superset: every element of sub is in set, compared as written.
func superset(set, sub []string) bool {
	return !slices.ContainsFunc(sub, func(s string) bool { return !slices.Contains(set, s) })
}
