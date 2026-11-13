// SPDX-License-Identifier: Apache-2.0

// Package mergepolicy reads a repository's merge classes from its fence file: a closed, typed
// vocabulary (merge policy MP4), each class resolved to the config.Class the gate already decides
// on, under the central guardrails and harness profiles. It holds no state of GitHub's and makes no
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
// them is not free: a crafted file of 32 classes at 64 patterns a list cost 2.8 to 8 s a read. So
// the count is bounded per list and per file, and Watch compiles a file once, not on every read.
const (
	MaxFileBytes    = 256 << 10
	MaxClasses      = 32
	MaxPatterns     = 64
	MaxFilePatterns = 256
	MaxPatternBytes = 512
	MaxBranches     = 8
)

// Mode is a class's state: live merges, shadow records, off does neither.
type Mode string

// The modes.
const (
	ModeLive   Mode = "live"
	ModeShadow Mode = "shadow"
	ModeOff    Mode = "off"
)

// UnmarshalJSON reads a string, or false as off: YAML reads a bare off as false.
func (m *Mode) UnmarshalJSON(b []byte) error {
	if string(b) == "false" {
		*m = ModeOff
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("mode is live, shadow or off: %s", b)
	}
	*m = Mode(s)
	return nil
}

// The author modes.
const (
	AuthorAgents = "agents"
	AuthorAdopt  = "adopt"
)

// File is .github/agent-factory.yaml. It declares its classes in one of two shapes, never both:
// Classes, the old shape, each class defined inline; or MergePolicies, the binding shape (B15),
// the same list as a Repository's spec.mergePolicies, each naming a MergePolicy the factory serves.
type File struct {
	Factory       string                 `json:"factory"`
	Classes       map[string]ClassPolicy `json:"classes,omitempty"`
	MergePolicies []config.Binding       `json:"mergePolicies,omitempty"`
}

// Declares reports whether the file declares a policy, in either shape: a file naming only the
// factory has none.
func (f *File) Declares() bool { return f.Classes != nil || f.MergePolicies != nil }

// Bound reports whether the file is in the binding shape.
func (f *File) Bound() bool { return f.MergePolicies != nil }

// ClassPolicy is one class as a repository declares it. review declares harness, harnessAllowed
// and escalation only (R30): its merge authority is implicit, so mode and author are absent there
// and required everywhere else.
type ClassPolicy struct {
	Description      string             `json:"description,omitempty"`
	Mode             Mode               `json:"mode,omitempty"`
	Author           string             `json:"author,omitempty"`
	Adopt            *config.Adopt      `json:"adopt,omitempty"`
	Harness          string             `json:"harness"`
	HarnessAllowed   []string           `json:"harnessAllowed,omitempty"`
	Escalation       *config.Escalation `json:"escalation,omitempty"`
	Conditions       Conditions         `json:"conditions,omitzero"`
	RequiresApproval []ApprovalLeaf     `json:"requiresApproval,omitempty"`
	Limits           Limits             `json:"limits,omitzero"`
	Monitor          *config.Monitor    `json:"monitor,omitempty"`
}

// Conditions are a class's predicates. Any leaf absent from All and Any is not checked. Any is
// one level of branches, each a conjunction of leaves: the class holds when All holds and one
// branch does. A key is in All or in the branches, never both, so no branch overrides All.
type Conditions struct {
	All  []Leaf     `json:"all,omitempty"`
	Any  [][]Leaf   `json:"any,omitempty"`
	Deny []DenyLeaf `json:"deny,omitempty"`
}

// Leaf is one predicate: exactly one field set.
type Leaf struct {
	AllowedFiles     []string       `json:"allowedFiles,omitempty"`
	ModifyOnly       *bool          `json:"modifyOnly,omitempty"`
	MaxPatchFiles    *int           `json:"maxPatchFiles,omitempty"`
	MaxModifiedLines *int           `json:"maxModifiedLines,omitempty"`
	DiffShape        *string        `json:"diffShape,omitempty"`
	HoldoutChecks    []string       `json:"holdoutChecks,omitempty"`
	ReleaseAgeCheck  *string        `json:"releaseAgeCheck,omitempty"`
	Oracle           *config.Oracle `json:"oracle,omitempty"`
	CompanionFiles   []string       `json:"companionFiles,omitempty"`
	RequiredLabel    *string        `json:"requiredLabel,omitempty"`
}

// names are the keys the leaf sets.
func (l Leaf) names() []string {
	var out []string
	for _, f := range []struct {
		key string
		set bool
	}{
		{"allowedFiles", l.AllowedFiles != nil}, {"modifyOnly", l.ModifyOnly != nil},
		{"maxPatchFiles", l.MaxPatchFiles != nil}, {"maxModifiedLines", l.MaxModifiedLines != nil},
		{"diffShape", l.DiffShape != nil}, {"holdoutChecks", l.HoldoutChecks != nil},
		{"releaseAgeCheck", l.ReleaseAgeCheck != nil}, {"oracle", l.Oracle != nil},
		{"companionFiles", l.CompanionFiles != nil}, {"requiredLabel", l.RequiredLabel != nil},
	} {
		if f.set {
			out = append(out, f.key)
		}
	}
	return out
}

// DenyLeaf refuses a pull request touching one of its paths.
type DenyLeaf struct {
	ProtectedFiles []string `json:"protectedFiles"`
}

// ApprovalLeaf asks a human about a pull request touching one of its paths, even in a live class:
// config's own, so a resolved class carries the same leaves.
type ApprovalLeaf = config.ApprovalLeaf

// Limits are a class's own caps, below the guardrails.
type Limits struct {
	MaxPerDay int `json:"maxPerDay,omitempty"`
}

var classRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`)

// Parse reads the file strictly: duplicate and unknown keys, a key in the wrong layer and a leaf
// that is not exactly one predicate are all errors. It checks shape, not the guardrails: Validate.
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
	if err := exactKeys(tree, reflect.TypeFor[File](), ""); err != nil {
		return nil, fmt.Errorf("mergepolicy: %w", err)
	}
	if err := reviewKeys(tree); err != nil {
		return nil, fmt.Errorf("mergepolicy: %w", err)
	}
	root, _ := tree.(map[string]any)
	_, classes := root["classes"]
	if _, bound := root["mergePolicies"]; bound && classes {
		return nil, errors.New("mergepolicy: the file declares classes (the old shape) or mergePolicies, never both")
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
	if len(f.Classes) > MaxClasses {
		return nil, fmt.Errorf("mergepolicy: at most %d classes", MaxClasses)
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
	for _, name := range slices.Sorted(maps.Keys(f.Classes)) {
		for _, list := range f.Classes[name].patterns() {
			total += len(list)
		}
		if err := f.Classes[name].shape(name); err != nil {
			// One error a line, each naming its class: the editor points each at the class's line.
			for line := range strings.SplitSeq(err.Error(), "\n") {
				errs = append(errs, fmt.Errorf("class %s: %s", name, line))
			}
		}
		if !classRE.MatchString(name) {
			errs = append(errs, fmt.Errorf("%q is not a class name", name))
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

// bareOff reads a binding's bare off, which YAML reads as false, as the mode off: the class
// shape's Mode does the same. j is re-encoded only when one is found.
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

func (p ClassPolicy) shape(name string) error {
	if name == "review" {
		return p.reviewShape()
	}
	var errs []error
	if !slices.Contains([]Mode{ModeLive, ModeShadow, ModeOff}, p.Mode) {
		errs = append(errs, fmt.Errorf("mode %q is not live, shadow or off", p.Mode))
	}
	switch {
	case p.Author != AuthorAgents && p.Author != AuthorAdopt:
		errs = append(errs, fmt.Errorf("author %q is not agents or adopt", p.Author))
	case (p.Author == AuthorAdopt) != (p.Adopt != nil):
		errs = append(errs, errors.New("adopt is required with author: adopt, and refused otherwise"))
	}
	if p.Harness == "" {
		errs = append(errs, errors.New("harness is required: a profile of the central list"))
	}
	if p.Limits.MaxPerDay < 0 {
		errs = append(errs, errors.New("limits.maxPerDay is 0 (the guardrail's) or more"))
	}
	inAll := map[string]bool{}
	errs = append(errs, leaves(p.Conditions.All, inAll)...)
	if b := p.Conditions.Any; b != nil {
		switch {
		case len(b) < 2:
			errs = append(errs, errors.New("any holds at least two branches: one is all"))
		case len(b) > MaxBranches:
			errs = append(errs, fmt.Errorf("any holds at most %d branches", MaxBranches))
		}
	}
	for _, branch := range p.Conditions.Any {
		if len(branch) == 0 {
			errs = append(errs, errors.New("a branch of any is empty: it would hold on everything"))
		}
		seen := map[string]bool{}
		errs = append(errs, leaves(branch, seen)...)
		for _, k := range slices.Sorted(maps.Keys(seen)) {
			if inAll[k] {
				errs = append(errs, fmt.Errorf("%s is in all and in a branch of any", k))
			}
		}
	}
	for _, d := range p.Conditions.Deny {
		if len(d.ProtectedFiles) == 0 {
			errs = append(errs, errors.New("a deny's protectedFiles is empty: it refuses nothing"))
		}
	}
	for _, a := range p.RequiresApproval {
		if len(a.ChangedFiles) == 0 {
			errs = append(errs, errors.New("a requiresApproval's changedFiles is empty: it asks about nothing"))
		}
	}
	errs = append(errs, patternErrors(p.patterns())...)
	return errors.Join(errs...)
}

// reviewKeysAllowed are the keys classes.review may carry (R30): the evaluation keys, and nothing
// that would give the class a merge authority of its own.
var reviewKeysAllowed = []string{"harness", "harnessAllowed", "escalation"}

// reviewKeys refuses every other key on classes.review as written, before decoding: an empty, null
// or zero value decodes to the absent value, and would otherwise pass as undeclared.
func reviewKeys(tree any) error {
	root, _ := tree.(map[string]any)
	var errs []error
	// The binding shape: review is bound off, with no parameter (R30, B1).
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
	classes, _ := root["classes"].(map[string]any)
	review, ok := classes["review"].(map[string]any)
	if !ok {
		return errors.Join(errs...)
	}
	for _, k := range slices.Sorted(maps.Keys(review)) {
		if !slices.Contains(reviewKeysAllowed, k) {
			errs = append(errs, fmt.Errorf("class review: %s is refused on review: it declares harness, harnessAllowed and escalation only, "+
				"and a human merges it", k))
		}
	}
	return errors.Join(errs...)
}

// reviewShape is shape for review: the floor is required. The other keys were refused as written
// (reviewKeys).
func (p ClassPolicy) reviewShape() error {
	if p.Harness == "" {
		return errors.New("harness is required: a profile of the central list")
	}
	return nil
}

// patterns are the class's path lists, each compiled when the class is validated.
func (p ClassPolicy) patterns() [][]string {
	var out [][]string
	for _, l := range slices.Concat(append([][]Leaf{p.Conditions.All}, p.Conditions.Any...)...) {
		out = append(out, l.AllowedFiles, l.CompanionFiles)
	}
	for _, d := range p.Conditions.Deny {
		out = append(out, d.ProtectedFiles)
	}
	for _, a := range p.RequiresApproval {
		out = append(out, a.ChangedFiles)
	}
	return out
}

// leaves checks a conjunction: each leaf sets exactly one key, no key twice, and no leaf is empty,
// since an empty value reads as absent and would pass as equivalent while checking nothing. The
// keys go into seen.
func leaves(ls []Leaf, seen map[string]bool) []error {
	var errs []error
	for _, l := range ls {
		names := l.names()
		if len(names) != 1 {
			errs = append(errs, fmt.Errorf("a leaf sets exactly one key, not %v", names))
			continue
		}
		if seen[names[0]] {
			errs = append(errs, fmt.Errorf("%s is declared twice", names[0]))
		}
		seen[names[0]] = true
		if err := l.empty(names[0]); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// empty is the error of a leaf whose one key holds nothing.
func (l Leaf) empty(key string) error {
	switch {
	case l.ModifyOnly != nil && !*l.ModifyOnly:
		return errors.New("modifyOnly: false says nothing: leave it out")
	case l.AllowedFiles != nil && len(l.AllowedFiles) == 0, l.HoldoutChecks != nil && len(l.HoldoutChecks) == 0,
		l.CompanionFiles != nil && len(l.CompanionFiles) == 0:
		return fmt.Errorf("%s is empty: leave it out", key)
	case l.MaxPatchFiles != nil && *l.MaxPatchFiles < 1, l.MaxModifiedLines != nil && *l.MaxModifiedLines < 1:
		return fmt.Errorf("%s is at least 1: leave it out for the guardrail's", key)
	case l.DiffShape != nil && *l.DiffShape == "", l.ReleaseAgeCheck != nil && *l.ReleaseAgeCheck == "",
		l.RequiredLabel != nil && *l.RequiredLabel == "":
		return fmt.Errorf("%s is empty: leave it out", key)
	}
	return nil
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
