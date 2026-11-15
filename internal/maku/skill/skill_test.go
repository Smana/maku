// SPDX-License-Identifier: Apache-2.0

package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// SkillText is the embedded maku-handoff/SKILL.md as text: the package embeds it, so a test
// needs no path and the text it reads is the text Install writes.
func SkillText(t *testing.T) string {
	t.Helper()
	b, err := files.ReadFile("maku-handoff/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// frontMatter parses SKILL.md's front matter with a real YAML loader: a hand-rolled split once
// hid a ` #` that a strict loader reads as a comment.
func frontMatter(t *testing.T, b []byte) map[string]any {
	t.Helper()
	parts := strings.SplitN(string(b), "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		t.Fatalf("no front matter in %q", b)
	}
	m := map[string]any{}
	if err := yaml.Unmarshal([]byte(parts[1]), &m); err != nil {
		t.Fatalf("front matter is not YAML: %v", err)
	}
	return m
}

func assertSpec(t *testing.T, fm map[string]any, wantName string) {
	t.Helper()
	if fm["name"] != wantName {
		t.Errorf("name = %v, must equal the directory name %s", fm["name"], wantName)
	}
	d, _ := fm["description"].(string)
	if d == "" || len(d) > 1024 {
		t.Errorf("description length = %d, want 1..1024", len(d))
	}
	for _, want := range []string{"anything waiting on me", "#N"} {
		if !strings.Contains(d, want) {
			t.Errorf("description lost %q: %q", want, d)
		}
	}
}

func TestInstallWritesTheSkill(t *testing.T) {
	dir := t.TempDir()
	got, err := Install(dir, "v0.8.0")
	if err != nil || len(got) != 2 {
		t.Fatalf("Install = %v, %v", got, err)
	}
	for _, skillDir := range []string{"maku-handoff"} {
		b, err := os.ReadFile(filepath.Join(dir, skillDir, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `maku-version: "v0.8.0"`) || strings.Contains(string(b), "{{VERSION}}") {
			t.Errorf("%s version not stamped:\n%s", skillDir, b)
		}
		tpl, err := os.ReadFile(filepath.Join(dir, skillDir, "references", "issue-template.md"))
		if err != nil || !strings.Contains(string(tpl), "Acceptance check") {
			t.Errorf("%s template: %v", skillDir, err)
		}
	}
	if fi, _ := os.Stat(got[0]); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	// A second install overwrites, stamping the new version.
	if err := os.WriteFile(got[0], []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, "v0.9.0"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got[0]); !strings.Contains(string(b), `"v0.9.0"`) {
		t.Errorf("not overwritten:\n%s", b)
	}
}

func TestFrontMatterMeetsTheAgentSkillsSpec(t *testing.T) {
	got, err := Install(t.TempDir(), "v0.8.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, skillDir := range []string{"maku-handoff"} {
		b, err := files.ReadFile(skillDir + "/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		fm := frontMatter(t, b)
		assertSpec(t, fm, skillDir)
		// Applying the label is the developer's decision (D1): no tool may do it.
		tools, _ := fm["allowed-tools"].(string)
		for _, bad := range []string{"gh issue edit", "label", "gh api"} {
			if strings.Contains(tools, bad) {
				t.Errorf("%s allowed-tools grants %q: %s", skillDir, bad, tools)
			}
		}
	}
	// The installed file, version substituted, must stay valid YAML too.
	inst, err := os.ReadFile(got[0])
	if err != nil {
		t.Fatal(err)
	}
	ifm := frontMatter(t, inst)
	assertSpec(t, ifm, "maku-handoff")
	if v := ifm["metadata"].(map[string]any)["maku-version"]; v != "v0.8.0" {
		t.Errorf("maku-version = %v", v)
	}
}

func TestInstallRefusesASymlinkedSkillDir(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "maku-handoff")); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, "v1"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want a symlink refusal", err)
	}
	if es, _ := os.ReadDir(outside); len(es) != 0 {
		t.Errorf("wrote through the symlink: %v", es)
	}
}

func TestInstallRefusesASymlinkedFile(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "maku-handoff"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "victim")
	if err := os.Symlink(target, filepath.Join(dir, "maku-handoff", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, "v1"); err == nil {
		t.Fatal("want a refusal")
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("wrote through the file symlink")
	}
}

func TestVersion(t *testing.T) {
	dir := t.TempDir()
	if v, ok, err := Version(dir); v != "" || ok || err != nil {
		t.Errorf("empty dir: Version = %q, %v, %v", v, ok, err)
	}
	if _, err := Install(dir, "v0.8.0"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := Version(dir); v != "v0.8.0" || !ok || err != nil {
		t.Errorf("installed: Version = %q, %v, %v", v, ok, err)
	}
	bad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, "maku-handoff"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "maku-handoff", "SKILL.md"), []byte("no front matter"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Version(bad); err == nil {
		t.Error("want an error for a SKILL.md without front matter")
	}
}

func TestVersionFrontMatterShapes(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "maku-handoff"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "maku-handoff", "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if v, ok, err := Version(write(t, "---\r\nname: maku-handoff\r\nmetadata:\r\n  roomctl-version: \"v0.3.0\"\r\n---\r\n# x\r\n")); v != "v0.3.0" || !ok || err != nil {
		t.Errorf("CRLF: Version = %q, %v, %v", v, ok, err)
	}
	for name, body := range map[string]string{
		"no metadata":  "---\nname: maku-handoff\n---\n# x\n",
		"empty stamp":  "---\nmetadata:\n  roomctl-version: \"\"\n---\n",
		"other fields": "---\nmetadata:\n  other: x\n---\n",
	} {
		if _, _, err := Version(write(t, body)); err == nil || !strings.Contains(err.Error(), "maku-version") {
			t.Errorf("%s: err = %v, want one naming maku-version", name, err)
		}
	}
}

// A skill roomctl installed before v0.30.0 carries metadata.roomctl-version: maku still reads it,
// so doctor reports a version, not a broken file.
func TestVersionReadsBothKeys(t *testing.T) {
	for _, tc := range []struct{ name, key string }{
		{"maku-version", "maku-version"},
		{"roomctl-version, before v0.30.0", "roomctl-version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "maku-handoff", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			body := "---\nname: maku-handoff\nmetadata:\n  " + tc.key + ": \"v0.29.9\"\n---\n# x\n"
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			v, ok, err := Version(dir)
			if err != nil || !ok || v != "v0.29.9" {
				t.Fatalf("Version = %q, %v, %v; want v0.29.9, true, nil", v, ok, err)
			}
		})
	}
}
