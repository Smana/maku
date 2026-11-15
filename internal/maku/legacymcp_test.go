// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An agent set up with roomctl kept a factory entry that ran `roomctl mcp`: setup now rewrites that
// factory entry directly to `maku` running `maku mcp` and says so.
func TestSetupRewritesAFactoryEntryThatStillRunsRoomctl(t *testing.T) {
	for _, tc := range []struct {
		name, agent string
		project     bool
		file, body  string
	}{
		{"claude, user scope", "claude", false, ".claude.json", `{"mcpServers":{"factory":{"command":"roomctl","args":["mcp"]}}}`},
		{"claude, an absolute roomctl", "claude", false, ".claude.json", `{"mcpServers":{"factory":{"command":"/usr/local/bin/roomctl","args":["mcp"]}}}`},
		{"gemini, project scope", "gemini", true, ".gemini/settings.json", `{"mcpServers":{"factory":{"command":"roomctl","args":["mcp"]}}}`},
		{"codex", "codex", false, ".codex/config.toml", "[other]\ncommand = \"x\"\n\n[mcp_servers.factory]\ncommand = \"roomctl\"\nargs = [\"mcp\"]\n"},
		{"opencode, a command array", "opencode", false, ".config/opencode/opencode.json", `{"mcp":{"servers":{"factory":{"type":"local","command":["roomctl","mcp"]}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			root := home
			if tc.project {
				root = dir
			}
			p := filepath.Join(root, tc.file)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			s := Setup{Agent: Agents[tc.agent], Project: tc.project, Home: home, Dir: dir, Cmd: "maku", Version: "v1", LookPath: found, Out: &out,
				Run:     func(context.Context, []string) error { return nil },
				Capture: func(context.Context, []string) (string, error) { return "", errors.New("no service") }}
			if err := s.Do(t.Context()); err != nil {
				t.Fatal(err)
			}
			if legacyCommand(p) {
				b, _ := os.ReadFile(p)
				t.Fatalf("still has legacy roomctl factory entry:\n%s", b)
			}
			if !strings.Contains(out.String(), "rewrote the factory MCP server entry") || !strings.Contains(out.String(), p) {
				t.Fatalf("output did not report rewriting %s:\n%s", p, &out)
			}
		})
	}
}

// The doctor warns about every factory entry, in any agent's config, that still runs roomctl.
func TestDoctorWarnsOnAFactoryEntryThatStillRunsRoomctl(t *testing.T) {
	for _, tc := range []struct {
		name, cmd string
		warn      bool
	}{{"roomctl", "roomctl", true}, {"maku", "maku", false}} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			p := filepath.Join(home, ".claude.json")
			if err := os.WriteFile(p, []byte(`{"mcpServers":{"factory":{"command":"`+tc.cmd+`","args":["mcp"]}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			r := newDoctorRig(t)
			r.d.LegacyMCP = LegacyEntries(home, "")
			var out strings.Builder
			if err := r.d.Run(t.Context(), &out); err != nil {
				t.Fatalf("%v\n%s", err, &out)
			}
			got := strings.Contains(out.String(), "warn mcp: "+p+": the factory MCP server still runs roomctl")
			if got != tc.warn || (tc.warn && !strings.Contains(out.String(), "claude mcp remove --scope user factory")) {
				t.Fatalf("warn=%v want %v\n%s", got, tc.warn, &out)
			}
		})
	}
}
