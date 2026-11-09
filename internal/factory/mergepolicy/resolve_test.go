// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

func central() *config.Config {
	return &config.Config{HarnessProfiles: map[string]config.HarnessProfile{
		"plain":        {},
		"checked":      {Checks: true},
		"adopted-none": {Template: config.TemplateNone},
	}}
}

func resolved(t *testing.T, raw string, c *config.Config) Resolved {
	t.Helper()
	f, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Resolve("docs-broad", f.Classes["docs-broad"], c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResolveFillsTheClassTheGateReads(t *testing.T) {
	r := resolved(t, example, central())
	cl := r.Class
	switch {
	case cl.Live || !cl.Shadow || cl.Adopted():
		t.Fatalf("%+v", cl)
	case !slices.Equal(cl.AllowedFiles, []string{`^docs/.+\.md$`, `^README\.md$`}):
		t.Fatalf("%v", cl.AllowedFiles)
	case !slices.Equal(cl.ProtectedFiles, []string{"^docs/superpowers/", "^docs/specs/"}):
		t.Fatalf("%v", cl.ProtectedFiles)
	case cl.MaxPatchFiles != 10 || cl.MaxModifiedLines != 200 || cl.MaxPerDay != 2:
		t.Fatalf("%+v", cl)
	case len(cl.HoldoutChecks) != 1 || cl.Monitor == nil || cl.Monitor.FollowUpWindow.Hours() != 168:
		t.Fatalf("%+v", cl)
	case !slices.Equal(r.Approval, []string{"^docs/security/"}):
		t.Fatalf("%v", r.Approval)
	}
}

func TestTheProfileSetsChecks(t *testing.T) {
	raw := strings.Replace(example, "harness: plain", "harness: checked", 1)
	if r := resolved(t, raw, central()); !r.Class.Checks || r.Class.TestFirst {
		t.Fatalf("%+v", r.Class)
	}
}

// Guardrails add to a class, and an undeclared cap inherits the guardrail's.
func TestGuardrailsOnlyTighten(t *testing.T) {
	c := central()
	c.MergeGuardrails = config.MergeGuardrails{ProtectedFiles: []string{"^secrets/"}, MandatoryChecks: []string{"Leak scan"},
		MaxPatchFiles: 20, MaxModifiedLines: 500, MaxPerDay: 5}
	cl := resolved(t, example, c).Class
	switch {
	case !slices.Contains(cl.ProtectedFiles, "^secrets/") || !slices.Contains(cl.HoldoutChecks, "Leak scan"):
		t.Fatalf("%+v", cl)
	case cl.MaxPatchFiles != 10 || cl.MaxModifiedLines != 200 || cl.MaxPerDay != 2:
		t.Fatalf("a lower cap stands: %+v", cl)
	}
	bare := "factory: aws-0\nclasses:\n  docs-broad: {mode: shadow, author: agents, harness: plain}\n"
	cl = resolved(t, bare, c).Class
	if cl.MaxPatchFiles != 20 || cl.MaxModifiedLines != 500 || cl.MaxPerDay != 5 {
		t.Fatalf("an undeclared cap inherits: %+v", cl)
	}
}

func TestResolveRefuses(t *testing.T) {
	over := func() *config.Config { c := central(); c.MergeGuardrails.MaxPatchFiles = 5; return c }
	for name, tc := range map[string]struct {
		raw  string
		cfg  *config.Config
		want string
	}{
		"an unknown profile": {strings.Replace(example, "harness: plain", "harness: nope", 1), central(), "nope"},
		"a cap over":         {example, over(), "maxPatchFiles"},
		"agents with none":   {strings.Replace(example, "harness: plain", "harness: adopted-none", 1), central(), "runs nothing"},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Parse([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			_, err = Resolve("docs-broad", f.Classes["docs-broad"], tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.Is(err, ErrGuardrail) {
				t.Fatalf("got %v, want %q and ErrGuardrail", err, tc.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	required := "  docs-links: {mode: live, author: agents, harness: plain}\n  revert: {mode: live, author: agents, harness: plain}\n"
	head := "factory: aws-0\nclasses:\n"
	for name, tc := range map[string]struct{ raw, want string }{
		"fine":           {head + required, ""},
		"fence only":     {"factory: aws-0\n", ""},
		"no revert":      {head + "  docs-links: {mode: live, author: agents, harness: plain}\n", "revert"},
		"the class rule": {head + required + "  bad:\n    mode: shadow\n    author: agents\n    harness: plain\n    conditions: {all: [{allowedFiles: ['docs/.+']}]}\n", "must start with ^"},
		"no revert, how": {head + "  docs-links: {mode: live, author: agents, harness: plain}\n", "mode: off"},
		"unbounded":      {head + required + "  docs: {mode: shadow, author: agents, harness: plain}\n", "class docs: a live or shadow class needs allowedFiles"},
		"unbounded any": {head + required + "  docs:\n    mode: live\n    author: agents\n    harness: plain\n    conditions: {any: [[{allowedFiles: ['^a$']}], [{maxPatchFiles: 2}]]}\n",
			"class docs: a live or shadow class needs allowedFiles"},
		"bounded any":   {head + required + "  docs:\n    mode: live\n    author: agents\n    harness: plain\n    conditions: {any: [[{allowedFiles: ['^a$']}], [{allowedFiles: ['^b$']}]]}\n", ""},
		"off unbounded": {head + required + "  docs: {mode: off, author: agents, harness: plain}\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Parse([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			err = Validate(f, central())
			if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestResolveCopiesTheVariants(t *testing.T) {
	c := central()
	c.HarnessProfiles["richer"] = config.HarnessProfile{Checks: true}
	raw := "factory: aws-0\nclasses:\n  docs-broad:\n    mode: \"off\"\n    author: agents\n    harness: plain\n    harnessAllowed: [richer]\n    escalation: {outOfBudget: richer}\n"
	cl := resolved(t, raw, c).Class
	if cl.Harness != "plain" || !slices.Equal(cl.HarnessAllowed, []string{"richer"}) || cl.Escalation == nil || cl.Escalation.OutOfBudget != "richer" {
		t.Fatalf("%+v", cl)
	}
	f, err := Parse([]byte(strings.Replace(raw, "[richer]", "[nope]", 1)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Resolve("docs-broad", f.Classes["docs-broad"], c)
	if !errors.Is(err, ErrGuardrail) || !strings.Contains(err.Error(), `harnessAllowed "nope" is not in harnessProfiles (TE2)`) {
		t.Fatalf("got %v", err)
	}
}

// R30: a repository's review class declares harness, harnessAllowed and escalation, nothing else;
// its merge authority is implicit and no file gives it a mode, an author, conditions or caps.
func TestAReviewClassDeclaresOnlyTheEvaluationKeys(t *testing.T) {
	c := central()
	c.HarnessProfiles["other"] = config.HarnessProfile{}
	required := "factory: aws-0\nclasses:\n  docs-links: {mode: live, author: agents, harness: plain}\n  revert: {mode: live, author: agents, harness: plain}\n"
	for name, tc := range map[string]struct{ review, want string }{
		"the floor alone":   {"  review: {harness: plain}\n", ""},
		"floor and steps":   {"  review: {harness: plain, harnessAllowed: [other], escalation: {outOfBudget: other}}\n", ""},
		"no harness":        {"  review: {harnessAllowed: [other]}\n", "harness is required"},
		"a mode":            {"  review: {mode: off, harness: plain}\n", "review"},
		"an author":         {"  review: {author: agents, harness: plain}\n", "review"},
		"adopt":             {"  review: {harness: plain, adopt: {logins: ['a[bot]'], branches: ['^x/']}}\n", "review"},
		"conditions":        {"  review: {harness: plain, conditions: {all: [{maxPatchFiles: 2}]}}\n", "review"},
		"an approval":       {"  review: {harness: plain, requiresApproval: [{changedFiles: ['^a']}]}\n", "review"},
		"a cap":             {"  review: {harness: plain, limits: {maxPerDay: 2}}\n", "review"},
		"a monitor":         {"  review: {harness: plain, monitor: {followUpWindow: 24h}}\n", "review"},
		"a missing variant": {"  review: {harness: plain, harnessAllowed: [nope]}\n", "harnessAllowed \"nope\""},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Parse([]byte(required + tc.review))
			if err == nil {
				err = Validate(f, c)
			}
			if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if err != nil {
				return
			}
			r, err := Resolve("review", f.Classes["review"], c)
			if err != nil || r.Class.Live || r.Class.Shadow || r.Class.Harness != "plain" || r.Branches != nil {
				t.Fatalf("%v %+v", err, r)
			}
		})
	}
}
