// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeBrief(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestARoleBindsOneCapability(t *testing.T) {
	dir := t.TempDir()
	writeBrief(t, dir, "roles/security-reviewer.md", "Review auth boundaries and secret handling.")

	raw := noTemplates + `roles:
  security-reviewer:
    capability: read
    model: agent-default
    effort: high
    brief: roles/security-reviewer.md
`
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load valid custom role: %v", err)
	}
	runRole, ok := c.RoleRunRole("security-reviewer")
	if !ok || runRole != "reviewer" {
		t.Fatalf("RoleRunRole = (%q, %v), want (reviewer, true)", runRole, ok)
	}
	if got := c.RoleBrief("security-reviewer"); !strings.Contains(got, "Review auth boundaries") {
		t.Fatalf("RoleBrief = %q", got)
	}
	if !slices.Contains(c.roles(), "security-reviewer") || !slices.Contains(c.roles(), "scoper") {
		t.Fatalf("c.roles() = %v, want built-in five plus security-reviewer", c.roles())
	}

	// Closed capability profiles: read -> reviewer, tests -> tester, write -> implementer.
	for name, want := range map[string]string{
		CapabilityRead:  "reviewer",
		CapabilityTests: "tester",
		CapabilityWrite: "implementer",
	} {
		if p, ok := CapabilityFor(name); !ok || p.RunRole != want {
			t.Fatalf("CapabilityFor(%q) = (%+v, %v), want RunRole %q", name, p, ok, want)
		}
	}
	if _, ok := CapabilityFor("admin"); ok {
		t.Fatal("unknown capability must not resolve")
	}

	// In release I, a custom role may bind read only.
	for _, tc := range []struct {
		name, cap, want string
	}{
		{"missing capability", "", "capability is required"},
		{"tests refused for custom role", "tests", "custom roles may bind read only"},
		{"write refused for custom role", "write", "custom roles may bind read only"},
		{"unknown capability", "admin", `unknown capability "admin"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y := noTemplates + "roles:\n  sec-review:\n    capability: " + tc.cap + "\n    brief: roles/security-reviewer.md\n"
			if _, err := ParseInDir([]byte(y), dir); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestABriefOutsideTheConfigDirectoryIsRefused(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "config")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "secret.md")
	if err := os.WriteFile(outside, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "roles"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "roles", "escaped.md")); err != nil {
		t.Fatal(err)
	}
	writeBrief(t, dir, "roles/huge.md", strings.Repeat("a", MaxRoleBriefBytes+1))
	writeBrief(t, dir, "roles/empty.md", "   \n")

	for name, tc := range map[string]struct {
		brief string
		want  string
	}{
		"parent traversal":  {"../secret.md", "escapes the config directory"},
		"nested traversal":  {"roles/../../secret.md", "escapes the config directory"},
		"absolute path":     {outside, "relative path"},
		"symlink escape":    {"roles/escaped.md", "cannot read brief"},
		"missing file":      {"roles/missing.md", "cannot read brief"},
		"oversized > 16KiB": {"roles/huge.md", "exceeds 16384 bytes"},
		"empty brief":       {"roles/empty.md", "brief is empty"},
		"blank path":        {"", "brief is required"},
	} {
		t.Run(name, func(t *testing.T) {
			raw := noTemplates + "roles:\n  sec:\n    capability: read\n    brief: " + tc.brief + "\n"
			if _, err := ParseInDir([]byte(raw), dir); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("brief %q: got %v, want %q", tc.brief, err, tc.want)
			}
		})
	}
}

func TestABuiltInRoleNameIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeBrief(t, dir, "roles/brief.md", "brief text")
	for _, builtIn := range []string{"implementer", "reviewer", "tester", "triager", "scoper", "judge", "publisher"} {
		t.Run(builtIn, func(t *testing.T) {
			raw := noTemplates + "roles:\n  " + builtIn + ":\n    capability: read\n    brief: roles/brief.md\n"
			if _, err := ParseInDir([]byte(raw), dir); err == nil || !strings.Contains(err.Error(), "built-in role name is reserved") {
				t.Fatalf("role %q: got %v, want built-in reservation error", builtIn, err)
			}
		})
	}
}
