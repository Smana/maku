// SPDX-License-Identifier: Apache-2.0

// Package mergepolicy reads a repository's merge classes from its fence file: the bindings of
// central MergePolicies (B15), each resolved to the config.Class the gate already decides on, under
// the central guardrails and harness profiles. It holds no state of GitHub's and makes no
// decision: through stage 3, the central config is the only merge authority.
package mergepolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/fence"
)

// The file's bounds. The patterns compile to RE2, linear in the paths they match, but compiling
// them is not free: a crafted file of 32 bindings at 64 patterns a list cost seconds a read. So
// the count is bounded per list and per file, and Watch compiles a file once, not on every read.
const (
	MaxFileBytes    = 256 << 10
	MaxClasses      = 32
	MaxPatterns     = 64
	MaxFilePatterns = 256
	MaxPatternBytes = 512
)

// ErrClassesRemoved is why a file in the classes shape is refused (B8, B15, Q13).
const ErrClassesRemoved = "the classes shape was removed in this release: write mergePolicies with maku policy export"

// Mode is a class's state: live merges, shadow records, off does neither.
type Mode string

// The modes.
const (
	ModeLive   Mode = "live"
	ModeShadow Mode = "shadow"
	ModeOff    Mode = "off"
)

// File is .github/agent-factory.yaml: the factory it names and, in the binding shape (B15), the
// same list as a Repository's spec.mergePolicies, each naming a MergePolicy the factory serves.
type File struct {
	Factory       string           `json:"factory"`
	MergePolicies []config.Binding `json:"mergePolicies,omitempty"`
}

// Declares reports whether the file declares a policy: a file naming only the factory has none.
func (f *File) Declares() bool { return f.MergePolicies != nil }

// Bound reports whether the file binds policies: Declares, kept for the readers that ask it so.
func (f *File) Bound() bool { return f.MergePolicies != nil }

// ApprovalLeaf asks a human about a pull request touching one of its paths, even in a live class:
// config's own, so a resolved class carries the same leaves.
type ApprovalLeaf = config.ApprovalLeaf

var classRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`)

// Parse reads the file strictly: duplicate and unknown keys and a key in the wrong layer are all
// errors, and the removed classes shape is refused with ErrClassesRemoved. It checks shape, not the
// guardrails: Validate.
func Parse(raw []byte) (*File, error) {
	if len(raw) > MaxFileBytes {
		return nil, fmt.Errorf("mergepolicy: the file is over %d bytes", MaxFileBytes)
	}
	j, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return nil, fmt.Errorf("mergepolicy: not YAML: %w", err)
	}
	var tree any
	if err := json.Unmarshal(j, &tree); err != nil {
		return nil, fmt.Errorf("mergepolicy: %w", err)
	}
	root, _ := tree.(map[string]any)
	if _, classes := root["classes"]; classes {
		return nil, errors.New("mergepolicy: classes: " + ErrClassesRemoved)
	}
	if err := exactKeys(tree, reflect.TypeFor[File](), ""); err != nil {
		return nil, fmt.Errorf("mergepolicy: %w", err)
	}
	if err := reviewKeys(root); err != nil {
		return nil, fmt.Errorf("mergepolicy: %w", err)
	}
	if j, err = bareOff(root, j); err != nil {
		return nil, fmt.Errorf("mergepolicy: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("mergepolicy: %w", err)
	}
	if !fence.ValidName(f.Factory) {
		return nil, fmt.Errorf("mergepolicy: factory %q is not a factory name", f.Factory)
	}
	if len(f.MergePolicies) > MaxClasses {
		return nil, fmt.Errorf("mergepolicy: at most %d bindings in mergePolicies", MaxClasses)
	}
	var errs []error
	total := 0
	for i, b := range f.MergePolicies {
		for _, list := range bindingPatterns(b) {
			total += len(list)
		}
		if slices.IndexFunc(f.MergePolicies, func(o config.Binding) bool { return o.Name == b.Name }) != i {
			errs = append(errs, fmt.Errorf("%s is bound twice: a policy is bound once", b.Name))
		}
		if err := bindingShape(b); err != nil {
			for line := range strings.SplitSeq(err.Error(), "\n") {
				errs = append(errs, fmt.Errorf("binding %s: %s", b.Name, line))
			}
		}
		if !classRE.MatchString(b.Name) {
			errs = append(errs, fmt.Errorf("%q is not a class name", b.Name))
		}
	}
	if total > MaxFilePatterns {
		errs = append(errs, fmt.Errorf("at most %d patterns in the file, not %d", MaxFilePatterns, total))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("mergepolicy: %w", err)
	}
	return &f, nil
}

// bareOff reads a binding's bare off, which YAML reads as false, as the mode off. j is re-encoded only when one is found.
func bareOff(root map[string]any, j []byte) ([]byte, error) {
	list, _ := root["mergePolicies"].([]any)
	found := false
	for _, e := range list {
		if m, ok := e.(map[string]any); ok && m["mode"] == false {
			m["mode"], found = config.ModeOff, true
		}
	}
	if !found {
		return j, nil
	}
	return json.Marshal(root)
}

// bindingShape is what a binding may not say whatever it binds: the mode, the bounds of its lists,
// and a negative cap. Whether it resolves is Validate's (config.Resolve).
func bindingShape(b config.Binding) error {
	var errs []error
	if !slices.Contains([]string{config.ModeLive, config.ModeShadow, config.ModeOff}, b.Mode) {
		errs = append(errs, fmt.Errorf("mode %q is not live, shadow or off", b.Mode))
	}
	for _, c := range []struct {
		key string
		v   int
	}{{"maxPatchFiles", b.MaxPatchFiles}, {"maxModifiedLines", b.MaxModifiedLines}, {"maxPerDay", b.MaxPerDay}} {
		if c.v < 0 {
			errs = append(errs, fmt.Errorf("%s is 0 (the policy's) or more", c.key))
		}
	}
	if len(b.RequiresApproval) > maxApprovals {
		errs = append(errs, fmt.Errorf("requiresApproval holds at most %d entries", maxApprovals))
	}
	for _, a := range b.RequiresApproval {
		if len(a.ChangedFiles) == 0 {
			errs = append(errs, errors.New("a requiresApproval's changedFiles is empty: it asks about nothing"))
		}
	}
	if len(b.HoldoutChecks) > MaxPatterns {
		errs = append(errs, fmt.Errorf("holdoutChecks holds at most %d checks", MaxPatterns))
	}
	errs = append(errs, patternErrors(bindingPatterns(b))...)
	return errors.Join(errs...)
}

// maxApprovals is Binding.RequiresApproval's schema bound.
const maxApprovals = 16

// bindingPatterns are a binding's path lists, compiled when the class is resolved.
func bindingPatterns(b config.Binding) [][]string {
	out := [][]string{b.AllowedFiles, b.CompanionFiles, b.ProtectedFiles}
	for _, a := range b.RequiresApproval {
		out = append(out, a.ChangedFiles)
	}
	return out
}

// patternErrors bounds path lists: each at most MaxPatterns, no pattern empty or over
// MaxPatternBytes.
func patternErrors(lists [][]string) []error {
	var errs []error
	for _, list := range lists {
		if len(list) > MaxPatterns {
			errs = append(errs, fmt.Errorf("a list holds at most %d patterns", MaxPatterns))
		}
		for _, pat := range list {
			switch {
			case pat == "":
				errs = append(errs, errors.New("an empty pattern matches every path"))
			case len(pat) > MaxPatternBytes:
				errs = append(errs, fmt.Errorf("a pattern is at most %d bytes", MaxPatternBytes))
			}
		}
	}
	return errs
}

// reviewKeys refuses every key but name and mode on review's binding as written, before decoding:
// review is bound off, with no parameter (R30, B1), and an empty, null or zero value decodes to the
// absent value, and would otherwise pass as undeclared.
func reviewKeys(root map[string]any) error {
	var errs []error
	list, _ := root["mergePolicies"].([]any)
	for _, e := range list {
		b, _ := e.(map[string]any)
		if b["name"] != "review" {
			continue
		}
		for _, k := range slices.Sorted(maps.Keys(b)) {
			if k != "name" && k != "mode" {
				errs = append(errs, fmt.Errorf("binding review: %s is refused on review: it is bound with mode off and nothing else, "+
					"and a human merges it", k))
			}
		}
		if m := b["mode"]; m != false && m != config.ModeOff {
			errs = append(errs, errors.New("binding review: review is bound with mode off: a human merges it"))
		}
	}
	return errors.Join(errs...)
}

// exactKeys walks a decoded document against t: encoding/json matches keys case-insensitively, so
// {mode: shadow, Mode: live} would otherwise read as live. At a struct, every key must equal a
// field's JSON name exactly. Map keys are free-form; other values end the walk. at is the path
// walked so far, for the message.
func exactKeys(v any, t reflect.Type, at string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch v := v.(type) {
	case map[string]any:
		switch t.Kind() {
		case reflect.Map:
			for _, k := range slices.Sorted(maps.Keys(v)) {
				if err := exactKeys(v[k], t.Elem(), join(at, k)); err != nil {
					return err
				}
			}
		case reflect.Struct:
			fields := map[string]reflect.Type{}
			for f := range t.Fields() {
				name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				if name == "" {
					name = f.Name
				}
				fields[name] = f.Type
			}
			for _, k := range slices.Sorted(maps.Keys(v)) {
				ft, ok := fields[k]
				if !ok {
					return unknownKey(at, k, slices.Sorted(maps.Keys(fields)))
				}
				if err := exactKeys(v[k], ft, join(at, k)); err != nil {
					return err
				}
			}
		}
	case []any:
		if t.Kind() == reflect.Slice {
			for i, e := range v {
				if err := exactKeys(e, t.Elem(), fmt.Sprintf("%s[%d]", at, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func join(at, k string) string {
	if at == "" {
		return k
	}
	return at + "." + k
}

// aliases are the names other merge tools give a predicate this vocabulary holds (policy-bot,
// Mergify, Renovate, GitHub), lowercased, to the key meant.
var aliases = map[string]string{
	"maxfiles": "maxPatchFiles", "maxchangedfiles": "maxPatchFiles", "files_count": "maxPatchFiles",
	"maxlines": "maxModifiedLines", "maxchangedlines": "maxModifiedLines", "modifiedlines": "maxModifiedLines", "modified_lines": "maxModifiedLines",
	"files": "allowedFiles", "paths": "allowedFiles", "onlychangedfiles": "allowedFiles", "only_changed_files": "allowedFiles",
	"protectedpaths": "protectedFiles", "denyfiles": "protectedFiles", "nochangedfiles": "protectedFiles", "no_changed_files": "protectedFiles",
	"label": "requiredLabel", "labels": "requiredLabel", "requiredlabels": "requiredLabel",
	"checks": "holdoutChecks", "requiredchecks": "holdoutChecks", "check-success": "holdoutChecks",
	"minimumreleaseage": "releaseAgeCheck", "releaseage": "releaseAgeCheck",
}

// unknownKey refuses k at at, naming the key meant when k is another tool's name for it, the same
// key in another case, or two edits from it, and every key valid there.
func unknownKey(at, k string, valid []string) error {
	where := ""
	if at != "" {
		where = at + ": "
	}
	low := strings.ToLower(k)
	meant := ""
	if a, ok := aliases[low]; ok && slices.Contains(valid, a) {
		meant = a
	}
	for _, f := range valid {
		if meant == "" && levenshtein(low, strings.ToLower(f)) <= 2 {
			meant = f
		}
	}
	hint := ""
	if meant != "" {
		hint = fmt.Sprintf(" (did you mean %q?)", meant)
	}
	return fmt.Errorf("%skey %q is not a field%s; keys are matched exactly, in their documented case: one of %s",
		where, k, hint, strings.Join(valid, ", "))
}

// levenshtein is the edit distance of two short keys.
func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
