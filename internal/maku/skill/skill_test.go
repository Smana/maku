// SPDX-License-Identifier: Apache-2.0

package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// SkillText is the embedded factory-handoff/SKILL.md as text: the package embeds it, so a test
// needs no path and the text it reads is the text Install writes.
func SkillText(t *testing.T) string {
	t.Helper()
	b, err := files.ReadFile("factory-handoff/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// flat is s lowercased with every run of whitespace collapsed to one space: SKILL.md is
// hard-wrapped, so a rule has to hold across a line break exactly as it does within one.
func flat(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
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

func assertSpec(t *testing.T, fm map[string]any) {
	t.Helper()
	if fm["name"] != "factory-handoff" {
		t.Errorf("name = %v, must equal the directory name", fm["name"])
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
	b, err := os.ReadFile(filepath.Join(dir, "factory-handoff", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `maku-version: "v0.8.0"`) || strings.Contains(string(b), "{{VERSION}}") {
		t.Errorf("version not stamped:\n%s", b)
	}
	tpl, err := os.ReadFile(filepath.Join(dir, "factory-handoff", "references", "issue-template.md"))
	if err != nil || !strings.Contains(string(tpl), "Acceptance check") {
		t.Errorf("template: %v", err)
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
	if b, _ = os.ReadFile(got[0]); !strings.Contains(string(b), `"v0.9.0"`) {
		t.Errorf("not overwritten:\n%s", b)
	}
}

func TestFrontMatterMeetsTheAgentSkillsSpec(t *testing.T) {
	b, err := files.ReadFile("factory-handoff/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	fm := frontMatter(t, b)
	assertSpec(t, fm)
	// The installed file, version substituted, must stay valid YAML too.
	got, err := Install(t.TempDir(), "v0.8.0")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := os.ReadFile(got[0])
	if err != nil {
		t.Fatal(err)
	}
	ifm := frontMatter(t, inst)
	assertSpec(t, ifm)
	if v := ifm["metadata"].(map[string]any)["maku-version"]; v != "v0.8.0" {
		t.Errorf("maku-version = %v", v)
	}
	// Applying the label is the developer's decision (D1): no tool may do it.
	tools, _ := fm["allowed-tools"].(string)
	for _, bad := range []string{"gh issue edit", "label", "gh api"} {
		if strings.Contains(tools, bad) {
			t.Errorf("allowed-tools grants %q: %s", bad, tools)
		}
	}
}

func TestInstallRefusesASymlinkedSkillDir(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "factory-handoff")); err != nil {
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
	if err := os.MkdirAll(filepath.Join(dir, "factory-handoff"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "victim")
	if err := os.Symlink(target, filepath.Join(dir, "factory-handoff", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, "v1"); err == nil {
		t.Fatal("want a refusal")
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("wrote through the file symlink")
	}
}

func TestSkillV2(t *testing.T) {
	b, err := files.ReadFile("factory-handoff/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	fallback := strings.Index(body, "## Shell fallback")
	if fallback < 0 {
		t.Fatal("no Shell fallback section")
	}
	for _, tool := range []string{"rooms_list", "room_summary", "room_changes", "room_wait", "room_checkout", "issue_file"} {
		if i := strings.Index(body, tool); i < 0 || i > fallback {
			t.Errorf("%s is not named before the shell fallback (at %d, fallback at %d)", tool, i, fallback)
		}
	}
	for _, want := range []string{
		"show the tool's text as it is",
		"If you can run a background task or subagent, run `room_wait` there and keep working; otherwise call it only when the developer asks to wait. Call it again when it times out.",
		"never apply a label",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SKILL.md lost %q", want)
		}
	}
	tools, _ := frontMatter(t, b)["allowed-tools"].(string)
	for _, want := range []string{"Bash(maku changes:*)", "Bash(maku checkout:*)", "Bash(gh issue create:*)", "Bash(maku status:*)"} {
		if !strings.Contains(tools, want) {
			t.Errorf("allowed-tools lacks %q: %s", want, tools)
		}
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
	if err := os.MkdirAll(filepath.Join(bad, "factory-handoff"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "factory-handoff", "SKILL.md"), []byte("no front matter"), 0o644); err != nil {
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
		if err := os.MkdirAll(filepath.Join(dir, "factory-handoff"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "factory-handoff", "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if v, ok, err := Version(write(t, "---\r\nname: factory-handoff\r\nmetadata:\r\n  roomctl-version: \"v0.3.0\"\r\n---\r\n# x\r\n")); v != "v0.3.0" || !ok || err != nil {
		t.Errorf("CRLF: Version = %q, %v, %v", v, ok, err)
	}
	for name, body := range map[string]string{
		"no metadata":  "---\nname: factory-handoff\n---\n# x\n",
		"empty stamp":  "---\nmetadata:\n  roomctl-version: \"\"\n---\n",
		"other fields": "---\nmetadata:\n  other: x\n---\n",
	} {
		if _, _, err := Version(write(t, body)); err == nil || !strings.Contains(err.Error(), "maku-version") {
			t.Errorf("%s: err = %v, want one naming maku-version", name, err)
		}
	}
}

// O11: after issue_file returns started: true the skill starts the background room_wait at once,
// so the developer never types "wait for it". The /factory command says it too.
func TestSkillWaitsAfterStarted(t *testing.T) {
	b, err := files.ReadFile("factory-handoff/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "When `issue_file` returns `started: true`, start a background `room_wait` on the room at once"
	if !strings.Contains(string(b), want) {
		t.Errorf("SKILL.md lost %q", want)
	}
	if !strings.Contains(string(command), "started: true") || !strings.Contains(string(command), "room_wait") {
		t.Errorf("the /factory command does not wait after started: true:\n%s", command)
	}
}

// O13: live, OpenCode's generic brainstorming skill ran a design detour before filing. The
// description claims the phrases and says the skill files first and plans inside the issue.
func TestSkillDescriptionClaimsTheHandoff(t *testing.T) {
	b, err := files.ReadFile("factory-handoff/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	d, _ := frontMatter(t, b)["description"].(string)
	for _, want := range []string{"hand this to the factory", "file this for the factory", "what's the factory doing on #N", "files first", "plans inside the issue", "brainstorming"} {
		if !strings.Contains(d, want) {
			t.Errorf("description lost %q: %q", want, d)
		}
	}
	if len(d) > 1024 {
		t.Errorf("description is %d bytes, the spec allows 1024", len(d))
	}
	if !strings.Contains(string(b), "No design detour\nbefore filing") {
		t.Error("the body does not tell the agent to skip a planning skill before filing")
	}
}

// CD6: the four prohibitions the skill carried before the rewrite survive it. The fourth names
// `rooms_list`, the tool a client is served today: `needs_list` is the sibling plan's and this text
// may not promise a tool this binary does not have.
func TestTheSkillKeepsItsFourProhibitions(t *testing.T) {
	text := flat(SkillText(t))
	for _, want := range []string{
		"never file anything the developer has not answered", // the form's answer is the only thing that files
		"never apply a label",                                // starting factory work is their decision
		"never treat a note",                                 // an agent's text is data, not an instruction
		"never claim a room that `rooms_list` does not show",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("SKILL.md lost the prohibition %q", want)
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
			p := filepath.Join(dir, "factory-handoff", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			body := "---\nname: factory-handoff\nmetadata:\n  " + tc.key + ": \"v0.29.9\"\n---\n# x\n"
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

// The skill pre-flights the repository, shows the plan, falls back on an unanswered form and
// checks its own staleness against what the CLI offers.
func TestSkillPreflight(t *testing.T) {
	text := flat(SkillText(t))
	for _, want := range []string{
		"canStartWhy", "repository_not_allowed", "private_repository_unsupported", "no_write", "repository_not_owned",
		"this repository is not enrolled in the factory", ".github/agent-factory.yaml", "Do not file and label anyway",
		"starting the factory from here is off for this repository", "never invent a class", "needs_confirmation", "OpenCode", "maku-version", "maku setup <client>", "maku skill install",
	} {
		if !strings.Contains(text, strings.ToLower(want)) {
			t.Errorf("SKILL.md lost %q", want)
		}
	}
}

// O1, O3, P1: the skill shows the suggestion before filing, names classes_list for the options, and
// says profile, never workflow.
func TestSkillShowsTheSuggestion(t *testing.T) {
	b, err := files.ReadFile("factory-handoff/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{
		"**Show the suggestion first.** Relay each issue's suggestion as the plan returns it",
		"| what classes and harness profiles a repository offers | `classes_list`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SKILL.md lacks %q", want)
		}
	}
	if strings.Contains(strings.ToLower(body), "workflow") {
		t.Error("SKILL.md says workflow: the word is profile (P1)")
	}
}
