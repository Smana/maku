// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"strings"
	"testing"
)

const example = `factory: aws-0
classes:
  docs-broad:
    mode: shadow
    author: agents
    harness: plain
    conditions:
      all:
        - allowedFiles: ['^docs/.+\.md$', '^README\.md$']
        - maxPatchFiles: 10
        - maxModifiedLines: 200
        - holdoutChecks: ['Check the documentation links 🔗']
      deny:
        - protectedFiles: ['^docs/superpowers/', '^docs/specs/']
    requiresApproval:
      - changedFiles: ['^docs/security/']
    limits:
      maxPerDay: 2
    monitor:
      followUpWindow: 168h
`

func TestParseTheSpecsExample(t *testing.T) {
	f, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	cl, ok := f.Classes["docs-broad"]
	switch {
	case f.Factory != "aws-0" || !ok:
		t.Fatalf("%+v", f)
	case cl.Mode != ModeShadow || cl.Author != AuthorAgents || cl.Harness != "plain":
		t.Fatalf("%+v", cl)
	case len(cl.Conditions.All) != 4 || len(cl.Conditions.Deny) != 1 || len(cl.RequiresApproval) != 1:
		t.Fatalf("%+v", cl)
	case cl.Limits.MaxPerDay != 2 || cl.Monitor == nil:
		t.Fatalf("%+v", cl)
	}
}

// A file naming only the factory has no policy: not an error, and no classes.
func TestParseAFenceOnlyFile(t *testing.T) {
	f, err := Parse([]byte("factory: aws-0\n"))
	if err != nil || f.Classes != nil {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestABareOffIsTheOffMode(t *testing.T) {
	f, err := Parse([]byte("factory: aws-0\nclasses:\n  c: {mode: off, author: agents, harness: p}\n"))
	if err != nil || f.Classes["c"].Mode != ModeOff {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestParseRefuses(t *testing.T) {
	cl := func(body string) string { return "factory: aws-0\nclasses:\n  c: " + body + "\n" }
	long := strings.Repeat("a", MaxPatternBytes+1)
	for name, tc := range map[string]struct{ in, want string }{
		"not yaml":            {"factory: [\n", "not YAML"},
		"too big":             {"factory: aws-0\n#" + strings.Repeat("x", MaxFileBytes), "bytes"},
		"bad factory":         {"factory: AWS 0\n", "factory"},
		"unknown top key":     {"factory: aws-0\nalso: x\n", "also"},
		"a duplicate key":     {cl("{mode: shadow, mode: live, author: agents, harness: p}"), "mode"},
		"an invalid mode":     {cl("{mode: armed, author: agents, harness: p}"), "mode"},
		"no mode":             {cl("{author: agents, harness: p}"), "mode"},
		"an invalid author":   {cl("{mode: shadow, author: people, harness: p}"), "author"},
		"adopt with no block": {cl("{mode: shadow, author: adopt, harness: p}"), "adopt"},
		"a block on agents":   {cl("{mode: shadow, author: agents, harness: p, adopt: {logins: [], branches: []}}"), "adopt"},
		"no harness":          {cl("{mode: shadow, author: agents}"), "harness"},
		"a bad class name":    {"factory: aws-0\nclasses:\n  Bad_Name: {mode: off, author: agents, harness: p}\n", "class name"},
		"an unknown leaf":     {cl("{mode: shadow, author: agents, harness: p, conditions: {all: [{sizeBelow: 3}]}}"), "sizeBelow"},
		"protected under all": {cl("{mode: shadow, author: agents, harness: p, conditions: {all: [{protectedFiles: [a]}]}}"), "protectedFiles"},
		"changed under deny":  {cl("{mode: shadow, author: agents, harness: p, conditions: {deny: [{changedFiles: [a]}]}}"), "changedFiles"},
		"two keys in a leaf":  {cl("{mode: shadow, author: agents, harness: p, conditions: {all: [{maxPatchFiles: 1, maxModifiedLines: 1}]}}"), "exactly one"},
		"an empty leaf":       {cl("{mode: shadow, author: agents, harness: p, conditions: {all: [{}]}}"), "exactly one"},
		"a repeated leaf":     {cl("{mode: shadow, author: agents, harness: p, conditions: {all: [{maxPatchFiles: 1}, {maxPatchFiles: 2}]}}"), "twice"},
		"modifyOnly false":    {cl("{mode: shadow, author: agents, harness: p, conditions: {all: [{modifyOnly: false}]}}"), "modifyOnly"},
		"a cased top key":     {"Factory: aws-0\n", "Factory"},
		"a cased dup of mode": {cl("{mode: shadow, Mode: live, author: agents, harness: p}"), "Mode"},
		"a cased mode alone":  {cl("{Mode: live, author: agents, harness: p}"), "Mode"},
		"a cased leaf key":    {cl("{mode: shadow, author: agents, harness: p, conditions: {all: [{MaxPatchFiles: 1}]}}"), "MaxPatchFiles"},
		"a cased monitor key": {cl("{mode: shadow, author: agents, harness: p, monitor: {FollowUpWindow: 1h}}"), "FollowUpWindow"},
		"a long pattern":      {cl("{mode: shadow, author: agents, harness: p, conditions: {all: [{allowedFiles: [" + long + "]}]}}"), "bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
