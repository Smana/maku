// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
)

func repoFrom(t *testing.T, file string) *config.Repository {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	var classes map[string]config.Class
	if err := yaml.UnmarshalStrict(raw, &classes); err != nil {
		t.Fatal(err)
	}
	return &config.Repository{Name: "o/r", Classes: classes}
}

// Stage 1's exit: every real class survives export, the file, and the parser, unchanged.
func TestRealClassesRoundTrip(t *testing.T) {
	for _, file := range []string{"classes-cloud-native-ref.yaml", "classes-ai-native-ref.yaml"} {
		t.Run(file, func(t *testing.T) {
			rc, c := repoFrom(t, file), central()
			c.Factory = "aws-0"
			f, err := Export(rc, c)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			g, err := Parse(raw)
			if err != nil {
				t.Fatalf("%v\n%s", err, raw)
			}
			if err := Validate(g, c); err != nil {
				t.Fatalf("%v\n%s", err, raw)
			}
			if d := Compare(rc, g, c); len(d) != 0 {
				t.Fatalf("%v\n%s", d, raw)
			}
			if _, ok := g.Classes["review"]; ok {
				t.Fatal("review is never exported")
			}
		})
	}
}

func TestExportNeedsAProfile(t *testing.T) {
	rc := &config.Repository{Name: "o/r", Classes: map[string]config.Class{"docs-links": {Checks: true}}}
	c := &config.Config{Factory: "aws-0", HarnessProfiles: map[string]config.HarnessProfile{"plain": {}}}
	if _, err := Export(rc, c); err == nil || !strings.Contains(err.Error(), "no harness profile") {
		t.Fatalf("got %v", err)
	}
}

// A setting that moved is reported whichever way, so a list reordered is not a divergence.
func TestCompareReportsEveryDifference(t *testing.T) {
	c := central()
	rc := &config.Repository{Name: "o/r", Classes: map[string]config.Class{
		"docs-links": {Live: true},
		"revert":     {Live: true},
		"gone":       {Shadow: true},
		"moved":      {Shadow: true, MaxPatchFiles: 3},
		"sorted":     {Shadow: true, ProtectedFiles: []string{"^a", "^b"}},
		"review":     {Checks: true},
	}}
	f, err := Parse([]byte(`factory: aws-0
classes:
  docs-links: {mode: live, author: agents, harness: plain}
  revert: {mode: live, author: agents, harness: plain}
  moved:
    mode: shadow
    author: agents
    harness: plain
    conditions: {all: [{maxPatchFiles: 4}]}
  sorted:
    mode: shadow
    author: agents
    harness: plain
    conditions: {deny: [{protectedFiles: ['^b', '^a']}]}
  extra: {mode: off, author: agents, harness: plain}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(Compare(rc, f, c), "\n")
	for _, want := range []string{"extra: only in the repository", "gone: only in the central config", "moved: "} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, not := range []string{"sorted", "review", "docs-links"} {
		if strings.Contains(got, not) {
			t.Errorf("%q is no divergence:\n%s", not, got)
		}
	}
}

// requiresApproval has no central form: a class declaring it is not yet the same class.
func TestCompareCountsAnApprovalSubset(t *testing.T) {
	rc := &config.Repository{Name: "o/r", Classes: map[string]config.Class{"c": {Shadow: true}}}
	f, err := Parse([]byte("factory: aws-0\nclasses:\n  c:\n    mode: shadow\n    author: agents\n    harness: plain\n    requiresApproval: [{changedFiles: ['^a']}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d := Compare(rc, f, central()); len(d) != 1 || !strings.Contains(d[0], "requiresApproval") {
		t.Fatalf("%v", d)
	}
}

// Each field a repository file can express, differing alone from a bare shadow class: Compare
// reports it, and Resolve carries the file's value into the class the gate would read.
func TestCompareSeesEveryField(t *testing.T) {
	c := central()
	c.HarnessProfiles["tested"] = config.HarnessProfile{Checks: true, TestFirst: true}
	const head = "mode: shadow\n    author: agents\n    harness: plain\n"
	for name, tc := range map[string]struct {
		body string // the class's lines, 4-space indented
		want config.Class
	}{
		"mode":             {"mode: live\n    author: agents\n    harness: plain\n", config.Class{Live: true}},
		"checks":           {"mode: shadow\n    author: agents\n    harness: checked\n", config.Class{Shadow: true, Checks: true}},
		"testFirst":        {"mode: shadow\n    author: agents\n    harness: tested\n", config.Class{Shadow: true, Checks: true, TestFirst: true}},
		"allowedFiles":     {head + "    conditions: {all: [{allowedFiles: ['^docs/']}]}\n", config.Class{Shadow: true, AllowedFiles: []string{"^docs/"}}},
		"protectedFiles":   {head + "    conditions: {deny: [{protectedFiles: ['^infra/']}]}\n", config.Class{Shadow: true, ProtectedFiles: []string{"^infra/"}}},
		"modifyOnly":       {head + "    conditions: {all: [{modifyOnly: true}]}\n", config.Class{Shadow: true, ModifyOnly: true}},
		"maxPatchFiles":    {head + "    conditions: {all: [{maxPatchFiles: 3}]}\n", config.Class{Shadow: true, MaxPatchFiles: 3}},
		"maxModifiedLines": {head + "    conditions: {all: [{maxModifiedLines: 40}]}\n", config.Class{Shadow: true, MaxModifiedLines: 40}},
		"diffShape":        {head + "    conditions: {all: [{diffShape: links-only}]}\n", config.Class{Shadow: true, DiffShape: "links-only"}},
		"holdoutChecks":    {head + "    conditions: {all: [{holdoutChecks: [ci]}]}\n", config.Class{Shadow: true, HoldoutChecks: []string{"ci"}}},
		"maxPerDay":        {head + "    limits: {maxPerDay: 2}\n", config.Class{Shadow: true, MaxPerDay: 2}},
		"oracle": {head + "    conditions: {all: [{oracle: {probe: p, workflow: w.yml, maxFindings: 3}}]}\n",
			config.Class{Shadow: true, Oracle: &config.Oracle{Probe: "p", Workflow: "w.yml", MaxFindings: 3}}},
		"monitor": {head + "    monitor: {oracleRuns: 2}\n", config.Class{Shadow: true, Monitor: &config.Monitor{OracleRuns: 2}}},
		"author adopt": {"mode: shadow\n    author: adopt\n    adopt: {logins: [bot], branches: ['^b/']}\n    harness: adopted-none\n",
			config.Class{Shadow: true, Author: &config.Author{Adopt: &config.Adopt{Logins: []string{"bot"}, Branches: []string{"^b/"}}}}},
		"releaseAgeCheck": {head + "    conditions: {all: [{releaseAgeCheck: renovate/stability-days}]}\n", config.Class{Shadow: true, ReleaseAgeCheck: "renovate/stability-days"}},
		"companionFiles":  {head + "    conditions: {all: [{companionFiles: ['^kb/index.md$']}]}\n", config.Class{Shadow: true, CompanionFiles: []string{"^kb/index.md$"}}},
		"requiredLabel":   {head + "    conditions: {all: [{requiredLabel: ready-to-merge}]}\n", config.Class{Shadow: true, RequiredLabel: "ready-to-merge"}},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Parse([]byte("factory: aws-0\nclasses:\n  c:\n    " + tc.body))
			if err != nil {
				t.Fatal(err)
			}
			r, err := Resolve("c", f.Classes["c"], c)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(r.Class)
			want, _ := json.Marshal(tc.want)
			if string(got) != string(want) {
				t.Fatalf("Resolve carried\n%s\nwant\n%s", got, want)
			}
			rc := &config.Repository{Name: "o/r", Classes: map[string]config.Class{"c": {Shadow: true}}}
			if d := Compare(rc, f, c); len(d) != 1 || !strings.HasPrefix(d[0], "c: central") {
				t.Fatalf("no divergence for %s: %v", name, d)
			}
		})
	}
}
