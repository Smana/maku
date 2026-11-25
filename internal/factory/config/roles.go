// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/envelope"
)

// MaxRoles bounds Factory.spec.roles (WR11).
const MaxRoles = 16

// MaxRoleBriefBytes bounds a custom role's brief file on disk (16 KiB, WR11).
const MaxRoleBriefBytes = 16 * 1024

// Closed capability profile names (WR11): enforcement lives in the run role mapped from this set.
const (
	CapabilityRead  = "read"
	CapabilityTests = "tests"
	CapabilityWrite = "write"
)

// CapabilityProfile binds a capability name to the AgentRun run role on the claim. The Task CRD's
// run-role enum does not grow: a custom role is a persona over one of the four run roles.
type CapabilityProfile struct {
	Name    string
	RunRole string
}

var capabilityProfiles = map[string]CapabilityProfile{
	CapabilityRead:  {Name: CapabilityRead, RunRole: "reviewer"},
	CapabilityTests: {Name: CapabilityTests, RunRole: "tester"},
	CapabilityWrite: {Name: CapabilityWrite, RunRole: "implementer"},
}

// CapabilityFor returns the closed capability profile for name, or false when unknown.
func CapabilityFor(name string) (CapabilityProfile, bool) {
	p, ok := capabilityProfiles[name]
	return p, ok
}

// RoleDef declares one custom role in Factory.spec.roles (WR11).
// +kubebuilder:object:generate=true
type RoleDef struct {
	// Capability is the closed capability profile the role binds (read in this release).
	// +kubebuilder:validation:Enum=read;tests;write
	// +kubebuilder:validation:MaxLength=16
	Capability string `json:"capability"`
	// Model is an optional model alias for the role.
	// +kubebuilder:validation:MaxLength=253
	Model string `json:"model,omitempty"`
	// Effort is low, medium or high; unset keeps the harness's default.
	// +kubebuilder:validation:Enum=low;medium;high
	// +kubebuilder:validation:MaxLength=6
	Effort string `json:"effort,omitempty"`
	// Brief is a relative path under the factory's configuration directory, read at load (≤ 16 KiB).
	// +kubebuilder:validation:MaxLength=512
	Brief string `json:"brief"`
}

// builtInRoleNames are the reserved built-in role names a custom role may never shadow.
var builtInRoleNames = []string{"implementer", "reviewer", "tester", "triager", "scoper", "judge", "publisher"}

// isBuiltInRole reports whether name is reserved for a built-in role.
func isBuiltInRole(name string) bool {
	return slices.Contains(builtInRoleNames, name)
}

// RoleBrief returns the loaded brief text of a declared custom role, or "" if none was loaded.
func (c *Config) RoleBrief(name string) string {
	if c == nil {
		return ""
	}
	return c.roleBriefs[name]
}

// SetRoleBrief stores a custom role's brief text directly (for tests building Config in memory).
func (c *Config) SetRoleBrief(name, text string) {
	if c.roleBriefs == nil {
		c.roleBriefs = map[string]string{}
	}
	c.roleBriefs[name] = text
}

// RoleRunRole returns the AgentRun run role for a declared custom role, or false if unknown.
func (c *Config) RoleRunRole(name string) (string, bool) {
	if c == nil {
		return "", false
	}
	def, ok := c.Roles[name]
	if !ok {
		return "", false
	}
	prof, ok := CapabilityFor(def.Capability)
	if !ok {
		return "", false
	}
	return prof.RunRole, true
}

// validateRoles validates Factory.spec.roles and, when configDir is known, loads each brief file
// strictly confined under configDir (no path traversal, no symlink escape, at most 16 KiB).
func (c *Config) validateRoles(bad func(string, ...any)) {
	if len(c.Roles) == 0 {
		return
	}
	if len(c.Roles) > MaxRoles {
		bad("roles holds %d entries, at most %d", len(c.Roles), MaxRoles)
	}
	var root *os.Root
	if c.configDir != "" {
		r, err := os.OpenRoot(c.configDir)
		if err != nil {
			bad("roles: cannot open config directory %q: %v", c.configDir, err)
			return
		}
		defer func() { _ = r.Close() }()
		root = r
	}
	for _, name := range slices.Sorted(maps.Keys(c.Roles)) {
		def := c.Roles[name]
		switch {
		case !profileRE.MatchString(name) || !envelope.ValidPersona(name):
			bad("roles: %q is not a valid role name", name)
			continue
		case isBuiltInRole(name):
			bad("roles.%s: built-in role name is reserved", name)
			continue
		}
		switch def.Capability {
		case "":
			bad("roles.%s: capability is required (read)", name)
		case CapabilityRead:
		case CapabilityTests, CapabilityWrite:
			bad("roles.%s: capability %q is reserved for built-in roles; custom roles may bind read only", name, def.Capability)
		default:
			bad("roles.%s: unknown capability %q (must be read)", name, def.Capability)
		}
		if def.Effort != "" && !slices.Contains([]string{"low", "medium", "high"}, def.Effort) {
			bad("roles.%s.effort %q is low, medium or high", name, def.Effort)
		}
		if def.Model != "" {
			_, provider := providerModels()[def.Model]
			switch {
			case !slices.Contains(models(), def.Model) && !provider:
				bad("roles.%s.model: %q is not a model alias", name, def.Model)
			case c.Defaults.DataClass == "internal" && provider:
				bad("roles.%s.model %s: an internal task takes tier-light, tier-standard or tier-frontier", name, def.Model)
			case provider:
				c.needsPrice(bad, "roles."+name+".model", def.Model)
			}
		}
		rel, err := cleanBriefPath(def.Brief)
		if err != nil {
			bad("roles.%s.brief: %v", name, err)
			continue
		}
		if root != nil {
			text, err := readRoleBrief(root, rel)
			if err != nil {
				bad("roles.%s.brief %q: %v", name, def.Brief, err)
				continue
			}
			c.SetRoleBrief(name, text)
		}
	}
}

// cleanBriefPath refuses empty, absolute, backslash, NUL or escaping brief paths.
func cleanBriefPath(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("brief is required")
	}
	if strings.ContainsRune(raw, 0) || strings.ContainsRune(raw, '\\') {
		return "", fmt.Errorf("%q is not a valid relative path", raw)
	}
	if filepath.IsAbs(raw) || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("%q must be a relative path under the config directory", raw)
	}
	cleaned := filepath.Clean(raw)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q escapes the config directory", raw)
	}
	return cleaned, nil
}

// readRoleBrief reads rel inside root (os.Root enforces no symlink or .. escape) up to MaxRoleBriefBytes.
func readRoleBrief(root *os.Root, rel string) (string, error) {
	f, err := root.Open(rel)
	if err != nil {
		return "", fmt.Errorf("cannot read brief under config directory: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("brief path is a directory")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxRoleBriefBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxRoleBriefBytes {
		return "", fmt.Errorf("brief exceeds %d bytes", MaxRoleBriefBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", fmt.Errorf("brief is empty")
	}
	return string(data), nil
}
