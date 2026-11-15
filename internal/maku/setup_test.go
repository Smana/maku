// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/setup/*.txt from the current output")

func noRun(t *testing.T) func(context.Context, []string) error {
	return func(_ context.Context, argv []string) error {
		t.Errorf("ran %v, want nothing", argv)
		return nil
	}
}

func found(string) (string, error)  { return "/usr/bin/x", nil }
func absent(string) (string, error) { return "", errors.New("not found") }

func filesUnder(t *testing.T, roots ...string) []string {
	t.Helper()
	var out []string
	for _, r := range roots {
		_ = filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				out = append(out, p)
			}
			return nil
		})
	}
	return out
}

func TestSetupPrintGolden(t *testing.T) {
	for name, a := range Agents {
		for _, project := range []bool{false, true} {
			file := name
			if project {
				file += "-project"
			}
			t.Run(file, func(t *testing.T) {
				home, dir := t.TempDir(), t.TempDir()
				var out bytes.Buffer
				// Home and Dir are fresh: the golden names the logical paths, so the fake roots
				// are the documented ones and writes would show in the temp dirs checked below.
				s := Setup{Agent: a, Project: project, Print: true, Home: "/home/dev", Dir: "/work/repo", Cmd: "maku",
					Version: "v0", LookPath: found, Run: noRun(t), Out: &out}
				if err := s.Do(context.Background()); err != nil {
					t.Fatal(err)
				}
				golden := filepath.Join("testdata", "setup", file+".txt")
				if *updateGolden {
					if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				if out.String() != string(want) {
					t.Errorf("--print output:\n%s\nwant:\n%s", out.String(), want)
				}
				if got := filesUnder(t, home, dir); len(got) != 0 {
					t.Errorf("wrote %v", got)
				}
			})
		}
	}
}

func TestSetupPrintWritesNothing(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	s := Setup{Agent: Agents["claude"], Print: true, Home: home, Dir: dir, Cmd: "maku", LookPath: found, Run: noRun(t), Out: &bytes.Buffer{}}
	if err := s.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := filesUnder(t, home, dir); len(got) != 0 {
		t.Errorf("--print wrote %v", got)
	}
}

func TestSetupRegistersThroughTheCLI(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	var ran [][]string
	var out bytes.Buffer
	s := Setup{Agent: Agents["claude"], Home: home, Dir: dir, Cmd: "maku", Version: "v1", LookPath: found, Out: &out,
		Run: func(_ context.Context, argv []string) error { ran = append(ran, argv); return nil }}
	if err := s.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "mcp", "add", "--scope", "user", "maku", "--", "maku", "mcp"}
	if len(ran) != 1 || !slices.Equal(ran[0], want) {
		t.Fatalf("ran %v, want %v", ran, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "maku-handoff", "SKILL.md")); err != nil {
		t.Errorf("maku-handoff is not in <home>/.claude/skills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "maku-handoff", "SKILL.md")); err != nil {
		t.Errorf("maku-handoff is not in <home>/.claude/skills: %v", err)
	}
	if got := filesUnder(t, dir); len(got) != 0 {
		t.Errorf("a user-level setup wrote into the project: %v", got)
	}
}

func TestSetupProjectInstallsInTheProject(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	var ran [][]string
	s := Setup{Agent: Agents["claude"], Project: true, Home: home, Dir: dir, Cmd: "maku", Version: "v1", LookPath: found, Out: &bytes.Buffer{},
		Run: func(_ context.Context, argv []string) error { ran = append(ran, argv); return nil }}
	if err := s.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0][4] != "project" {
		t.Errorf("ran %v, want --scope project", ran)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", "maku-handoff", "SKILL.md")); err != nil {
		t.Errorf("the skill is not in <dir>/.claude/skills: %v", err)
	}
	if got := filesUnder(t, home); len(got) != 0 {
		t.Errorf("a project setup wrote into home: %v", got)
	}
}

func TestSetupPrintsWhenTheCLIIsMissing(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	var out bytes.Buffer
	s := Setup{Agent: Agents["claude"], Project: true, Home: home, Dir: dir, Cmd: "maku", Version: "v1", LookPath: absent, Run: noRun(t), Out: &out}
	if err := s.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"mcpServers"`) || !strings.Contains(out.String(), ".mcp.json") {
		t.Errorf("no snippet in:\n%s", out.String())
	}
}

func TestSetupNeverEditsAConfig(t *testing.T) {
	for name, a := range Agents {
		for _, project := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				home, dir := t.TempDir(), t.TempDir()
				s := Setup{Agent: a, Project: project, Home: home, Dir: dir, Cmd: "maku", Version: "v1", LookPath: absent, Run: noRun(t), Out: &bytes.Buffer{}}
				if err := s.Do(context.Background()); err != nil {
					t.Fatal(err)
				}
				root := home
				if project {
					root = dir
				}
				skills := filepath.Join(root, strings.TrimPrefix(a.skillDir(project), "~/"))
				var commands []string
				if c := a.commandDir(project); c != "" {
					cd := filepath.Join(root, strings.TrimPrefix(c, "~/"))
					commands = []string{filepath.Join(cd, "maku.md")}
				}
				for _, f := range filesUnder(t, home, dir) {
					inSkill := strings.HasPrefix(f, filepath.Join(skills, "maku-handoff")+string(filepath.Separator))
					if !inSkill && !slices.Contains(commands, f) {
						t.Errorf("wrote %s, not a skill file or a command file", f)
					}
				}
			})
		}
	}
}

func TestSetupRunFailureIsReturned(t *testing.T) {
	s := Setup{Agent: Agents["claude"], Home: t.TempDir(), Dir: t.TempDir(), Cmd: "maku", LookPath: found, Out: &bytes.Buffer{},
		Run: func(context.Context, []string) error { return errors.New("boom") }}
	if err := s.Do(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestSetupUnknownClient(t *testing.T) {
	_, err := LookupAgent("vim")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, n := range []string{"claude", "opencode", "codex", "gemini", "cursor", "copilot"} {
		if !strings.Contains(err.Error(), n) {
			t.Errorf("%q does not name %s", err, n)
		}
	}
}

func TestSetupCmdPrefersPATH(t *testing.T) {
	exe := func() (string, error) { return "/opt/bin/maku", nil }
	onPath := func(n string) (string, error) { return "/usr/bin/" + n, nil }
	if got := SetupCmd(onPath, exe); got != "maku" {
		t.Errorf("on PATH: %q, want maku", got)
	}
	if got := SetupCmd(absent, exe); got != "/opt/bin/maku" {
		t.Errorf("off PATH: %q, want the absolute executable", got)
	}
}

func TestSetupCopilotProjectIsWhatTheCLIReads(t *testing.T) {
	a := Agents["copilot"]
	if a.SnippetProjectPath != ".github/mcp.json" {
		t.Errorf("project path %q, want .github/mcp.json", a.SnippetProjectPath)
	}
	if a.SnippetProject != a.Snippet || !strings.Contains(a.SnippetProject, `"mcpServers"`) {
		t.Errorf("project snippet is not the CLI's mcpServers shape:\n%s", a.SnippetProject)
	}
}

func TestSetupCursorSnippetIsStdio(t *testing.T) {
	if !strings.Contains(Agents["cursor"].Snippet, `"type": "stdio"`) {
		t.Errorf("cursor snippet lacks type stdio:\n%s", Agents["cursor"].Snippet)
	}
}

func TestSetupSkillDirsOfCodex(t *testing.T) {
	a := Agents["codex"]
	if a.SkillDir != "~/.agents/skills" || a.SkillProjectDir != ".agents/skills" {
		t.Errorf("codex skill dirs %q, %q", a.SkillDir, a.SkillProjectDir)
	}
}

func TestSetupGeminiHintsAtTrust(t *testing.T) {
	var out bytes.Buffer
	s := Setup{Agent: Agents["gemini"], Home: t.TempDir(), Dir: t.TempDir(), Cmd: "maku", LookPath: absent, Run: noRun(t), Out: &out}
	if err := s.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "trusted folder") {
		t.Errorf("no trust hint in:\n%s", out.String())
	}
}

func TestSetupWarnsOnAnAbsolutePathInAProjectSnippet(t *testing.T) {
	for _, tc := range []struct {
		cmd     string
		project bool
		warn    bool
	}{{"/opt/bin/maku", true, true}, {"/opt/bin/maku", false, false}, {"maku", true, false}} {
		var out bytes.Buffer
		s := Setup{Agent: Agents["cursor"], Project: tc.project, Home: t.TempDir(), Dir: t.TempDir(), Cmd: tc.cmd, LookPath: absent, Run: noRun(t), Out: &out}
		if err := s.Do(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out.String(), "teammates"); got != tc.warn {
			t.Errorf("%+v: warning = %v in:\n%s", tc, got, out.String())
		}
	}
}

func TestSetupRefusesASymlinkedSkillDir(t *testing.T) {
	home, dir, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, ".cursor", "skills")); err != nil {
		t.Fatal(err)
	}
	s := Setup{Agent: Agents["cursor"], Home: home, Dir: dir, Cmd: "maku", LookPath: absent, Run: noRun(t), Out: &bytes.Buffer{}}
	err := s.Do(context.Background())
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want a symlink refusal", err)
	}
	if got := filesUnder(t, elsewhere); len(got) != 0 {
		t.Errorf("wrote through the symlink: %v", got)
	}
}

func TestSetupTwiceSucceedsAndRefreshesTheSkill(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	adds := 0
	run := func(context.Context, []string) error {
		if adds++; adds > 1 {
			return errors.New("exit status 1: MCP server factory already exists in user config") // claude, verbatim
		}
		return nil
	}
	s := Setup{Agent: Agents["claude"], Home: home, Dir: dir, Cmd: "maku", Version: "v1", LookPath: found, Out: &bytes.Buffer{}, Run: run}
	if err := s.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	skillMD := filepath.Join(home, ".claude", "skills", "maku-handoff", "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	s.Out = &out
	if err := s.Do(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if b, _ := os.ReadFile(skillMD); string(b) == "stale" {
		t.Error("the second run did not refresh the skill")
	}
	if !strings.Contains(out.String(), "already registered") {
		t.Errorf("no already-registered line in:\n%s", out.String())
	}
}

func TestSetupInstallsTheSkillBeforeRegistering(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude"), []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Setup{Agent: Agents["claude"], Home: home, Dir: t.TempDir(), Cmd: "maku", LookPath: found, Out: &bytes.Buffer{}, Run: noRun(t)}
	if err := s.Do(context.Background()); err == nil {
		t.Error("want the install to fail")
	}
}

// What the CLI writes, then setup's codemode flag (O9).
func TestSetupOpencodeSnippetIsWhatTheCLIWrites(t *testing.T) {
	want := `{"mcp":{"servers":{"maku":{"type":"local","command":["maku","mcp"],"codemode":false,"timeout":{"execution":900000}}}}}`
	got := strings.NewReplacer(" ", "", "\n", "").Replace(strings.ReplaceAll(Agents["opencode"].Snippet, "{CMD}", "maku"))
	if got != want {
		t.Errorf("opencode snippet %s, want %s", got, want)
	}
}

func TestSkillDirsWithoutARepositorySkipsTheProjectScope(t *testing.T) {
	for _, d := range SkillDirs("/home/dev", "") {
		if !strings.HasPrefix(d, "/home/dev/") {
			t.Errorf("%s is not under home", d)
		}
	}
	if len(SkillDirs("/home/dev", "/repo")) <= len(SkillDirs("/home/dev", "")) {
		t.Error("a repository adds no project dirs")
	}
}

// setup installs the short /maku command for the agents that have a command mechanism.
func TestSetupInstallsTheMakuCommand(t *testing.T) {
	for name, tc := range map[string]struct{ user, project string }{
		"claude":   {".claude/commands", ".claude/commands"},
		"opencode": {".config/opencode/commands", ".opencode/commands"},
	} {
		for _, project := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				home, dir := t.TempDir(), t.TempDir()
				s := Setup{Agent: Agents[name], Project: project, Home: home, Dir: dir, Cmd: "maku", Version: "v1", LookPath: absent, Run: noRun(t), Out: &bytes.Buffer{}}
				if err := s.Do(context.Background()); err != nil {
					t.Fatal(err)
				}
				root, sub := home, tc.user
				if project {
					root, sub = dir, tc.project
				}
				makuMD, err := os.ReadFile(filepath.Join(root, sub, "maku.md"))
				if err != nil {
					t.Fatal(err)
				}
				for _, w := range []string{"description:", "$ARGUMENTS", "handoff", "task", "inbox", "start", "wait", "never apply a label", "accept the rest"} {
					if !strings.Contains(string(makuMD), w) {
						t.Errorf("maku.md lacks %q:\n%s", w, makuMD)
					}
				}
			})
		}
	}
	// An agent with no command mechanism gets none.
	home, dir := t.TempDir(), t.TempDir()
	s := Setup{Agent: Agents["codex"], Home: home, Dir: dir, Cmd: "maku", Version: "v1", LookPath: absent, Run: noRun(t), Out: &bytes.Buffer{}}
	if err := s.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, f := range filesUnder(t, home, dir) {
		if strings.HasSuffix(f, "maku.md") {
			t.Errorf("codex got %s", f)
		}
	}
}

// O8: OpenCode's background service keeps the config it started with, so sessions attached to it
// have no factory tools although `opencode mcp list` says connected. setup says so, and restarts
// the service only on a yes (a terminal's, or --restart-service), never silently.
func TestSetupOpenCodeServiceRestart(t *testing.T) {
	type rig struct {
		ran      [][]string
		status   string
		statusEr error
		asked    int
	}
	run := func(t *testing.T, r *rig, restart bool, ask func(string) (bool, error)) string {
		t.Helper()
		var out bytes.Buffer
		s := Setup{Agent: Agents["opencode"], Home: t.TempDir(), Dir: t.TempDir(), Cmd: "maku", Version: "v1", LookPath: found, Out: &out,
			Run: func(_ context.Context, argv []string) error { r.ran = append(r.ran, argv); return nil },
			Capture: func(_ context.Context, argv []string) (string, error) {
				r.ran = append(r.ran, argv)
				return r.status, r.statusEr
			},
			RestartService: restart, Ask: ask}
		if err := s.Do(context.Background()); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	restarted := func(r *rig) bool {
		return slices.ContainsFunc(r.ran, func(a []string) bool { return slices.Equal(a, []string{"opencode", "service", "restart"}) })
	}
	t.Run("running, no terminal: says it, restarts nothing", func(t *testing.T) {
		r := &rig{status: "http://127.0.0.1:49374\n"}
		out := run(t, r, false, nil)
		if restarted(r) || !strings.Contains(out, "opencode service restart") || !strings.Contains(out, "restart") || !strings.Contains(out, "factory tools") {
			t.Fatalf("restarted=%v\n%s", restarted(r), out)
		}
	})
	t.Run("running, terminal says no", func(t *testing.T) {
		r := &rig{status: "http://127.0.0.1:49374\n"}
		out := run(t, r, false, func(string) (bool, error) { r.asked++; return false, nil })
		if restarted(r) || r.asked != 1 || !strings.Contains(out, "not restarted") {
			t.Fatalf("restarted=%v asked=%d\n%s", restarted(r), r.asked, out)
		}
	})
	t.Run("running, terminal says yes", func(t *testing.T) {
		r := &rig{status: "http://127.0.0.1:49374\n"}
		out := run(t, r, false, func(string) (bool, error) { return true, nil })
		if !restarted(r) || !strings.Contains(out, "restarted") {
			t.Fatalf("restarted=%v\n%s", restarted(r), out)
		}
	})
	t.Run("--restart-service restarts without asking", func(t *testing.T) {
		r := &rig{status: "http://127.0.0.1:49374\n"}
		run(t, r, true, func(string) (bool, error) { r.asked++; return false, nil })
		if !restarted(r) || r.asked != 0 {
			t.Fatalf("restarted=%v asked=%d", restarted(r), r.asked)
		}
	})
	t.Run("not running: nothing said, nothing restarted", func(t *testing.T) {
		r := &rig{status: "not running\n"}
		out := run(t, r, true, nil)
		if restarted(r) || strings.Contains(out, "service") {
			t.Fatalf("restarted=%v\n%s", restarted(r), out)
		}
	})
	t.Run("no service subcommand: the hint only", func(t *testing.T) {
		r := &rig{statusEr: errors.New("unknown command service")}
		out := run(t, r, true, func(string) (bool, error) { return true, nil })
		if restarted(r) || !strings.Contains(out, "opencode service restart") {
			t.Fatalf("restarted=%v\n%s", restarted(r), out)
		}
	})
	t.Run("another client is not asked about it", func(t *testing.T) {
		var out bytes.Buffer
		s := Setup{Agent: Agents["claude"], Home: t.TempDir(), Dir: t.TempDir(), Cmd: "maku", Version: "v1", LookPath: found, Out: &out,
			Run:     func(context.Context, []string) error { return nil },
			Capture: func(context.Context, []string) (string, error) { t.Error("captured"); return "", nil }}
		if err := s.Do(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// O9: OpenCode v2 exposes MCP tools through Code Mode by default, and a config with an `execute`
// deny rule turns Code Mode off, hiding every Code Mode server. `"codemode": false` on the server
// keeps its tools on the native list, where the elicitation forms and room_wait also belong.
func TestSetupOpenCodeWritesCodeModeOff(t *testing.T) {
	const existing = `{"theme":"dark","permission":[{"action":"execute","resource":"*","effect":"deny"}],"mcp":{"servers":{"other":{"type":"local","command":["x"]},"maku":{"type":"local","command":["maku","mcp"]}}}}`
	for name, project := range map[string]bool{"user": false, "project": true} {
		t.Run(name, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			path := filepath.Join(home, ".config", "opencode", "opencode.json")
			if project {
				path = filepath.Join(dir, "opencode.json")
			}
			var out bytes.Buffer
			// `opencode mcp add` writes the file; the stub plays it.
			add := func(context.Context, []string) error {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				return os.WriteFile(path, []byte(existing), 0o644)
			}
			s := Setup{Agent: Agents["opencode"], Project: project, Home: home, Dir: dir, Cmd: "maku", Version: "v1", LookPath: found, Out: &out, Run: add}
			for range 2 { // a second setup changes nothing more
				if err := s.Do(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			var got map[string]any
			b, _ := os.ReadFile(path)
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("%v\n%s", err, b)
			}
			servers := got["mcp"].(map[string]any)["servers"].(map[string]any)
			if f := servers["maku"].(map[string]any); f["codemode"] != false || f["type"] != "local" || fmt.Sprint(f["timeout"]) != "map[execution:900000]" {
				t.Errorf("maku: %v", f)
			}
			if _, ok := servers["other"]; !ok || got["theme"] != "dark" || got["permission"] == nil {
				t.Errorf("the rest of the file was not kept: %s", b)
			}
			if !strings.Contains(out.String(), "codemode") {
				t.Errorf("setup does not say it:\n%s", &out)
			}
		})
	}
}

// A config with comments is not rewritten; the developer is told what to add.
func TestSetupOpenCodeLeavesACommentedConfig(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	const commented = "// mine\n{\"mcp\":{\"servers\":{\"maku\":{}}}}\n"
	var out bytes.Buffer
	s := Setup{Agent: Agents["opencode"], Home: home, Dir: t.TempDir(), Cmd: "maku", Version: "v1", LookPath: found, Out: &out,
		Run: func(context.Context, []string) error {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			return os.WriteFile(path, []byte(commented), 0o644)
		}}
	if err := s.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != commented || !strings.Contains(out.String(), `"codemode": false`) {
		t.Fatalf("%q\n%s", b, &out)
	}
}

// The snippet a developer pastes has it too.
func TestSetupOpenCodeSnippetHasCodeModeOff(t *testing.T) {
	var out bytes.Buffer
	s := Setup{Agent: Agents["opencode"], Home: t.TempDir(), Dir: t.TempDir(), Cmd: "maku", Version: "v1", LookPath: absent, Run: noRun(t), Out: &out}
	if err := s.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"codemode": false`) || !strings.Contains(out.String(), `"execution": 900000`) {
		t.Fatalf("%s", &out)
	}
}

func TestSetupRegistersMaku(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "setup"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("goldens: %v (%d)", err, len(entries))
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("testdata", "setup", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		txt := string(b)
		if !strings.Contains(txt, `"maku":`) && !strings.Contains(txt, `[mcp_servers.maku]`) {
			t.Errorf("%s registers no server named maku:\n%s", e.Name(), txt)
		}
		for _, old := range []string{`"factory":`, `[mcp_servers.factory]`, ` user factory `, ` project factory `, ` --global factory `, ` add factory `} {
			if strings.Contains(txt, old) {
				t.Errorf("%s still registers %q:\n%s", e.Name(), old, txt)
			}
		}
	}

	for _, tc := range []struct {
		name, agent string
		project     bool
		file, body  string
		look        func(string) (string, error)
		check       func(t *testing.T, raw string)
	}{
		{
			name: "claude json", agent: "claude", file: ".claude.json", look: found,
			body: `{"mcpServers":{"factory":{"command":"maku","args":["mcp"]},"other":{"command":"x"}}}`,
			check: func(t *testing.T, raw string) {
				var m map[string]any
				if err := json.Unmarshal([]byte(raw), &m); err != nil {
					t.Fatal(err)
				}
				srvs := m["mcpServers"].(map[string]any)
				if _, ok := srvs["factory"]; ok {
					t.Errorf("factory kept: %s", raw)
				}
				if got := srvs["maku"].(map[string]any); got["command"] != "maku" || srvs["other"] == nil {
					t.Errorf("maku entry or other lost: %s", raw)
				}
			},
		},
		{
			name: "cursor json without CLI", agent: "cursor", project: true, file: ".cursor/mcp.json", look: absent,
			body: `{"mcpServers":{"factory":{"type":"stdio","command":"maku","args":["mcp"]},"other":{"command":"x"}}}`,
			check: func(t *testing.T, raw string) {
				var m map[string]any
				if err := json.Unmarshal([]byte(raw), &m); err != nil {
					t.Fatal(err)
				}
				srvs := m["mcpServers"].(map[string]any)
				if _, ok := srvs["factory"]; ok {
					t.Errorf("factory kept: %s", raw)
				}
				if got := srvs["maku"].(map[string]any); got["type"] != "stdio" || got["command"] != "maku" || srvs["other"] == nil {
					t.Errorf("maku entry or other lost: %s", raw)
				}
			},
		},
		{
			name: "opencode json", agent: "opencode", file: ".config/opencode/opencode.json", look: found,
			body: `{"theme":"dark","mcp":{"servers":{"factory":{"type":"local","command":["maku","mcp"]},"other":{"type":"local","command":["x"]}}}}`,
			check: func(t *testing.T, raw string) {
				var m map[string]any
				if err := json.Unmarshal([]byte(raw), &m); err != nil {
					t.Fatal(err)
				}
				srvs := m["mcp"].(map[string]any)["servers"].(map[string]any)
				if _, ok := srvs["factory"]; ok {
					t.Errorf("factory kept: %s", raw)
				}
				if got := srvs["maku"].(map[string]any); got["codemode"] != false || srvs["other"] == nil || m["theme"] != "dark" {
					t.Errorf("maku entry or other lost: %s", raw)
				}
			},
		},
		{
			name: "codex toml", agent: "codex", file: ".codex/config.toml", look: found,
			body: "[other]\ncommand = \"x\"\n\n[mcp_servers.factory]\ncommand = \"maku\"\nargs = [\"mcp\"]\n",
			check: func(t *testing.T, raw string) {
				if strings.Contains(raw, "[mcp_servers.factory]") || !strings.Contains(raw, "[mcp_servers.maku]") || !strings.Contains(raw, "[other]") {
					t.Errorf("toml not rewritten cleanly:\n%s", raw)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			root := home
			if tc.project {
				root = dir
			}
			p := filepath.Join(root, tc.file)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			s := Setup{
				Agent: Agents[tc.agent], Project: tc.project, Home: home, Dir: dir, Cmd: "maku", Version: "v1",
				LookPath: tc.look, Out: &out,
				Run:     func(context.Context, []string) error { return nil },
				Capture: func(context.Context, []string) (string, error) { return "not running\n", nil },
			}
			if err := s.Do(context.Background()); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, string(b))
			if !strings.Contains(out.String(), "factory") || !strings.Contains(out.String(), "maku") {
				t.Errorf("output did not report rewriting factory to maku:\n%s", &out)
			}
		})
	}
}

func TestSetupAdvancedRegistersFlag(t *testing.T) {
	for _, agent := range []string{"claude", "opencode", "codex", "gemini"} {
		t.Run(agent+" cli", func(t *testing.T) {
			var ran [][]string
			s := Setup{
				Agent: Agents[agent], Advanced: true, Home: t.TempDir(), Dir: t.TempDir(), Cmd: "maku", Version: "v1",
				LookPath: found, Out: &bytes.Buffer{},
				Run:     func(_ context.Context, argv []string) error { ran = append(ran, argv); return nil },
				Capture: func(context.Context, []string) (string, error) { return "not running\n", nil },
			}
			if err := s.Do(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(ran) == 0 || ran[0][len(ran[0])-1] != "--advanced" {
				t.Fatalf("ran %v, want trailing --advanced", ran)
			}
		})
	}
	for _, agent := range []string{"claude", "opencode", "codex", "cursor", "copilot"} {
		t.Run(agent+" print", func(t *testing.T) {
			var out bytes.Buffer
			s := Setup{
				Agent: Agents[agent], Advanced: true, Print: true, Home: "/home/dev", Dir: "/work/repo", Cmd: "maku",
				Version: "v1", LookPath: found, Run: noRun(t), Out: &out,
			}
			if err := s.Do(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), `"--advanced"`) {
				t.Fatalf("%s snippet lacks --advanced:\n%s", agent, &out)
			}
		})
	}
}

func TestSetupRefusesNothingButRegistersOnlyMaku(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "setup"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("goldens: %v (%d)", err, len(entries))
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("testdata", "setup", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		txt := string(b)
		for _, old := range []string{`"factory"`, `[mcp_servers.factory]`, `/factory`, `factory-handoff`, ` user factory`, ` project factory`, ` --global factory`, ` add factory`} {
			if strings.Contains(txt, old) {
				t.Errorf("%s still mentions %q:\n%s", e.Name(), old, txt)
			}
		}
	}

	home, dir := t.TempDir(), t.TempDir()
	cfgPath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(cfgPath, []byte(`{"mcpServers":{"factory":{"command":"maku","args":["mcp"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	s := Setup{
		Agent: Agents["claude"], Home: home, Dir: dir, Cmd: "maku", Version: "v1", LookPath: found, Out: &out,
		Run: func(context.Context, []string) error { return nil },
	}
	if err := s.Do(context.Background()); err != nil {
		t.Fatalf("setup refused an existing factory entry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "maku-handoff", "SKILL.md")); err != nil {
		t.Errorf("maku-handoff not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "factory-handoff")); !os.IsNotExist(err) {
		t.Errorf("factory-handoff still installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "commands", "maku.md")); err != nil {
		t.Errorf("maku.md not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "commands", "factory.md")); !os.IsNotExist(err) {
		t.Errorf("factory.md still installed: %v", err)
	}
}
