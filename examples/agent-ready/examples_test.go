// SPDX-License-Identifier: Apache-2.0

// Package agentready holds the worked files of docs/make-a-repository-ready.md: one ready repository
// per stack. The test holds each to the rules the harness's agent_checks.py validate() applies, and
// each declared tool to a pin in the example's mise.toml, as agent_checks.py pins() reads it.
package agentready

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"sigs.k8s.io/yaml"
)

var (
	stepName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	// miseTool is one "name = ..." line of mise.toml's [tools] table, the only table the examples have.
	miseTool = regexp.MustCompile(`(?m)^"?([A-Za-z0-9:_./-]+)"?\s*=`)
	topKeys  = []string{"version", "timeoutMinutes", "tools", "definedBy", "steps", "ciOnly", "tests"}
	stepKeys = []string{"name", "run", "ci", "timeoutMinutes", "stopOnFailure"}
)

func TestEveryExampleIsReady(t *testing.T) {
	stacks := []string{"go", "jvm", "node", "python"}
	for _, stack := range stacks {
		t.Run(stack, func(t *testing.T) {
			for _, f := range []string{".github/agent-checks.yaml", "mise.toml", "AGENTS.md"} {
				if _, err := os.Stat(filepath.Join(stack, f)); err != nil {
					t.Fatalf("missing %s: %v", f, err)
				}
			}
			raw, err := os.ReadFile(filepath.Join(stack, ".github", "agent-checks.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			checkDeclaration(t, doc)
			mise, err := os.ReadFile(filepath.Join(stack, "mise.toml"))
			if err != nil {
				t.Fatal(err)
			}
			var pinned []string
			for _, m := range miseTool.FindAllStringSubmatch(string(mise), -1) {
				pinned = append(pinned, m[1])
			}
			for _, tool := range doc["tools"].([]any) {
				if !slices.Contains(pinned, tool.(string)) {
					t.Errorf("tool %v is not pinned in mise.toml (pinned: %v)", tool, pinned)
				}
			}
		})
	}
}

func checkDeclaration(t *testing.T, doc map[string]any) {
	t.Helper()
	for k := range doc {
		if !slices.Contains(topKeys, k) {
			t.Errorf("unknown key %q", k)
		}
	}
	if v, _ := doc["version"].(float64); v != 1 {
		t.Errorf("version must be 1, got %v", doc["version"])
	}
	minutes := 8.0 // agent_checks.py DEFAULT_MINUTES
	if m, ok := doc["timeoutMinutes"].(float64); ok {
		minutes = m
	}
	if minutes < 1 || minutes > 15 {
		t.Errorf("timeoutMinutes %v is outside 1 to 15 (agent_checks.py MAX_MINUTES)", minutes)
	}
	steps, _ := doc["steps"].([]any)
	if len(steps) == 0 {
		t.Fatal("steps must be a non-empty list")
	}
	names := map[string]bool{}
	for _, s := range steps {
		step := s.(map[string]any)
		for k := range step {
			if !slices.Contains(stepKeys, k) {
				t.Errorf("step key %q", k)
			}
		}
		name, _ := step["name"].(string)
		if !stepName.MatchString(name) || names[name] {
			t.Errorf("step name %q is not unique or does not match %s", name, stepName)
		}
		names[name] = true
		if r, _ := step["run"].(string); r == "" {
			t.Errorf("step %s has no run", name)
		}
		if c, _ := step["ci"].(string); c == "" {
			t.Errorf("step %s names no ci check", name)
		}
	}
	ciOnly, _ := doc["ciOnly"].([]any)
	for _, c := range ciOnly {
		entry := c.(map[string]any)
		if len(entry) != 3 || entry["check"] == nil || entry["part"] == nil || entry["why"] == nil {
			t.Errorf("a ciOnly entry has exactly check, part and why: %v", entry)
		}
	}
}
