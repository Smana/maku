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

// The file's bounds. The patterns compile to RE2, linear in their input, so only their size is
// bounded here: GitHub's contents API and the validator do the rest.
const (
	MaxFileBytes    = 256 << 10
	MaxClasses      = 32
	MaxPatterns     = 64
	MaxPatternBytes = 512
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

// File is .github/agent-factory.yaml.
type File struct {
	Factory string                 `json:"factory"`
	Classes map[string]ClassPolicy `json:"classes,omitempty"`
}

// ClassPolicy is one class as a repository declares it.
type ClassPolicy struct {
	Mode             Mode            `json:"mode"`
	Author           string          `json:"author"`
	Adopt            *config.Adopt   `json:"adopt,omitempty"`
	Harness          string          `json:"harness"`
	Conditions       Conditions      `json:"conditions,omitzero"`
	RequiresApproval []ApprovalLeaf  `json:"requiresApproval,omitempty"`
	Limits           Limits          `json:"limits,omitzero"`
	Monitor          *config.Monitor `json:"monitor,omitempty"`
}

// Conditions are a class's predicates. Any leaf absent from All is not checked.
type Conditions struct {
	All  []Leaf     `json:"all,omitempty"`
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

// ApprovalLeaf asks a human about a pull request touching one of its paths, even in a live class.
type ApprovalLeaf struct {
	ChangedFiles []string `json:"changedFiles"`
}

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
	if err := exactKeys(tree, reflect.TypeFor[File]()); err != nil {
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
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(f.Classes)) {
		if err := f.Classes[name].shape(); err != nil {
			errs = append(errs, fmt.Errorf("class %s: %w", name, err))
		}
		if !classRE.MatchString(name) {
			errs = append(errs, fmt.Errorf("%q is not a class name", name))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("mergepolicy: %w", err)
	}
	return &f, nil
}

func (p ClassPolicy) shape() error {
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
	seen := map[string]bool{}
	var patterns [][]string
	for _, l := range p.Conditions.All {
		names := l.names()
		if len(names) != 1 {
			errs = append(errs, fmt.Errorf("a leaf of all sets exactly one key, not %v", names))
			continue
		}
		if seen[names[0]] {
			errs = append(errs, fmt.Errorf("%s is declared twice", names[0]))
		}
		seen[names[0]] = true
		if l.ModifyOnly != nil && !*l.ModifyOnly {
			errs = append(errs, errors.New("modifyOnly: false says nothing: leave it out"))
		}
		patterns = append(patterns, l.AllowedFiles, l.CompanionFiles)
	}
	for _, d := range p.Conditions.Deny {
		patterns = append(patterns, d.ProtectedFiles)
	}
	for _, a := range p.RequiresApproval {
		patterns = append(patterns, a.ChangedFiles)
	}
	for _, list := range patterns {
		if len(list) > MaxPatterns {
			errs = append(errs, fmt.Errorf("a list holds at most %d patterns", MaxPatterns))
		}
		for _, pat := range list {
			if len(pat) > MaxPatternBytes {
				errs = append(errs, fmt.Errorf("a pattern is at most %d bytes", MaxPatternBytes))
			}
		}
	}
	return errors.Join(errs...)
}

// exactKeys walks a decoded document against t: encoding/json matches keys case-insensitively, so
// {mode: shadow, Mode: live} would otherwise read as live. At a struct, every key must equal a
// field's JSON name exactly. Map keys are free-form; other values end the walk.
func exactKeys(v any, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch v := v.(type) {
	case map[string]any:
		switch t.Kind() {
		case reflect.Map:
			for _, k := range slices.Sorted(maps.Keys(v)) {
				if err := exactKeys(v[k], t.Elem()); err != nil {
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
					return fmt.Errorf("key %q is not a field: keys are matched exactly, in their documented case", k)
				}
				if err := exactKeys(v[k], ft); err != nil {
					return err
				}
			}
		}
	case []any:
		if t.Kind() == reflect.Slice {
			for _, e := range v {
				if err := exactKeys(e, t.Elem()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
