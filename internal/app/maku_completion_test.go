// SPDX-License-Identifier: Apache-2.0

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func completionScript(t *testing.T, shell string) string {
	t.Helper()
	var out strings.Builder
	if err := (Maku{Dir: t.TempDir(), Out: &out}).Run(t.Context(), []string{"completion", shell}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// Every command, subcommand and flag the usage lists is in each shell's script: the scripts are read
// from the usage, so a command the usage gains completes with no second list to keep.
func TestCompletionCoversTheUsage(t *testing.T) {
	var cmds, subs, flags []string
	flagRe := regexp.MustCompile(`--[a-z][a-z-]*`)
	for _, line := range strings.Split(makuUsage, "\n") {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		synopsis, _, _ := strings.Cut(strings.TrimSpace(line), "  ")
		if indent == 2 {
			f := strings.Fields(synopsis)
			cmds = append(cmds, f[0])
			if len(f) > 1 && isWordAlternatives(f[1]) {
				subs = append(subs, strings.Split(f[1], "|")...)
			}
		}
		if indent >= 2 && indent < usageDescCol {
			flags = append(flags, flagRe.FindAllString(synopsis, -1)...)
		}
	}
	if len(cmds) < 30 || len(subs) < 15 || len(flags) < 60 {
		t.Fatalf("the usage read too little: %d commands, %d subcommands, %d flags", len(cmds), len(subs), len(flags))
	}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		s := completionScript(t, shell)
		for _, w := range append(append(append(cmds, subs...), "help", "claude", "copilot", "md", "text", "json", "unattended", "triager"), flags...) {
			if shell == "fish" && strings.HasPrefix(w, "--") {
				w = "-l " + strings.TrimPrefix(w, "--")
			}
			if !regexp.MustCompile(`(^|[\s"(|'])` + regexp.QuoteMeta(w) + `($|[\s"')|;])`).MatchString(s) {
				t.Errorf("%s: %q is missing", shell, w)
			}
		}
	}
}

func TestCompletionRefusesAnUnknownShell(t *testing.T) {
	for _, args := range [][]string{{"completion"}, {"completion", "powershell"}, {"completion", "bash", "zsh"}} {
		err := (Maku{Dir: t.TempDir(), Out: &strings.Builder{}}).Run(t.Context(), args)
		if err == nil || !strings.Contains(err.Error(), completionUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
	var out strings.Builder
	if err := (Maku{Dir: t.TempDir(), Out: &out}).Run(t.Context(), []string{"completion", "--help"}); err != nil || !strings.Contains(out.String(), completionUsage) {
		t.Fatalf("--help: %v %q", err, out.String())
	}
}

// The scripts parse in their shells; zsh and fish are checked only where they are installed.
func TestCompletionScriptsParse(t *testing.T) {
	for shell, check := range map[string][]string{"bash": {"bash", "-n"}, "zsh": {"zsh", "-n"}, "fish": {"fish", "--no-execute"}} {
		if _, err := exec.LookPath(check[0]); err != nil {
			t.Logf("%s: not installed, not checked", shell)
			continue
		}
		path := filepath.Join(t.TempDir(), "maku."+shell)
		if err := os.WriteFile(path, []byte(completionScript(t, shell)), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.CommandContext(t.Context(), check[0], append(check[1:], path)...).CombinedOutput(); err != nil { //nolint:gosec // a fixed shell
			t.Errorf("%s: %v\n%s", shell, err, out)
		}
	}
}

// What bash offers at a few cursors.
func TestCompletionBashOffers(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	path := filepath.Join(t.TempDir(), "maku.bash")
	if err := os.WriteFile(path, []byte(completionScript(t, "bash")), 0o600); err != nil {
		t.Fatal(err)
	}
	for line, want := range map[string]string{
		"maku pol":                          "policy",
		"maku policy ":                      "list explain pause resume validate export check simulate diff",
		"maku plan ":                        "approve split revise",
		"maku setup c":                      "claude codex cursor copilot",
		"maku completion ":                  "bash zsh fish",
		"maku rooms --format ":              "md text",
		"maku rooms --n":                    "--needs-me",
		"maku retry t1 --":                  "--deeper",
		"maku policy validate --output ":    "json",
		"maku fork r1 --approvals ":         "attended unattended",
		"maku db bootstrap --rotate":        "--rotate-passwords",
		"maku policy validate --config ":    "",
		"maku repo add o/n --merge-default": "--merge-defaults",
	} {
		words := strings.Fields(line)
		if strings.HasSuffix(line, " ") {
			words = append(words, "")
		}
		script := `source "$1"; COMP_WORDS=("${@:2}"); COMP_CWORD=$(( ${#COMP_WORDS[@]} - 1 )); _maku; echo "${COMPREPLY[*]}"`
		out, err := exec.CommandContext(t.Context(), "bash", append([]string{"-c", script, "bash", path}, words...)...).CombinedOutput() //nolint:gosec // a fixed shell
		if err != nil {
			t.Fatalf("%q: %v\n%s", line, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("%q: got %q, want %q", line, got, want)
		}
	}
}
