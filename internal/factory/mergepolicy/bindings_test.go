// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// bindingFile is the spec's stage-4 file (B15).
const bindingFile = `factory: aws-0
mergePolicies:
  - {name: docs-links, mode: live, holdoutChecks: ['Check the documentation links 🔗']}
  - {name: revert,     mode: live, holdoutChecks: ['Check the documentation links 🔗']}
  - name: alert-rules
    mode: shadow
    allowedFiles: ['^observability/.+\.ya?ml$']
    requiresApproval: [{changedFiles: ['^observability/base/']}]
`

// boundCentral is a factory serving the spec's three policies, and the repository whose file binds
// them.
func boundCentral() (*config.Config, *config.Repository) {
	c := central()
	c.Factory = "aws-0"
	c.Policies = map[string]config.ServedPolicy{
		"docs-links":  {Spec: config.PolicySpec{Class: config.Class{Description: "Fixes a broken link in the docs."}, Requires: []string{"holdoutChecks"}}, Generation: 1},
		"revert":      {Spec: config.PolicySpec{Requires: []string{"holdoutChecks"}}, Generation: 1},
		"alert-rules": {Spec: config.PolicySpec{Class: config.Class{Description: "Tunes an alert rule.", MaxModifiedLines: 40}, Requires: []string{"allowedFiles"}}, Generation: 2},
	}
	rc := config.Repository{Name: "o/r", Merge: config.Merge{PolicyBotLogin: "gate[bot]"}}
	c.Repositories = []config.Repository{rc}
	return c, &c.Repositories[0]
}

func TestParseTheSpecsBindingFile(t *testing.T) {
	f, err := Parse([]byte(bindingFile))
	if err != nil {
		t.Fatal(err)
	}
	if f.Classes != nil || len(f.MergePolicies) != 3 || f.MergePolicies[2].Name != "alert-rules" || f.MergePolicies[2].Mode != config.ModeShadow ||
		len(f.MergePolicies[2].RequiresApproval) != 1 {
		t.Fatalf("%+v", f)
	}
	if !f.Declares() {
		t.Fatal("a binding file declares a policy")
	}
}

// YAML reads a bare off as false: a binding's mode reads it as off, as a class's does.
func TestABareOffBindsOff(t *testing.T) {
	f, err := Parse([]byte("factory: aws-0\nmergePolicies:\n  - {name: docs, mode: off}\n"))
	if err != nil || f.MergePolicies[0].Mode != config.ModeOff {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestParseRefusesABadBindingFile(t *testing.T) {
	many := "factory: aws-0\nmergePolicies:\n"
	for i := range MaxClasses + 1 {
		many += fmt.Sprintf("  - {name: c%d, mode: shadow}\n", i)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"both shapes":             {"factory: aws-0\nclasses: {docs-links: {mode: off, author: agents, harness: plain}}\nmergePolicies: [{name: docs-links, mode: live}]\n", "classes (the old shape) or mergePolicies, never both"},
		"a name twice":            {"factory: aws-0\nmergePolicies: [{name: docs, mode: live}, {name: docs, mode: off}]\n", "docs is bound twice"},
		"too many":                {many, fmt.Sprintf("at most %d", MaxClasses)},
		"a bad mode":              {"factory: aws-0\nmergePolicies: [{name: docs, mode: armed}]\n", `mode "armed"`},
		"no mode":                 {"factory: aws-0\nmergePolicies: [{name: docs}]\n", `mode ""`},
		"a bad name":              {"factory: aws-0\nmergePolicies: [{name: Docs, mode: live}]\n", `"Docs" is not a class name`},
		"an unknown key":          {"factory: aws-0\nmergePolicies: [{name: docs, mode: live, harness: plain}]\n", `key "harness" is not a field`},
		"an empty path":           {"factory: aws-0\nmergePolicies: [{name: docs, mode: live, allowedFiles: ['']}]\n", "an empty pattern"},
		"an empty asking":         {"factory: aws-0\nmergePolicies: [{name: docs, mode: live, requiresApproval: [{changedFiles: []}]}]\n", "asks about nothing"},
		"review with a parameter": {"factory: aws-0\nmergePolicies: [{name: review, mode: \"off\", holdoutChecks: [x]}]\n", "binding review: holdoutChecks is refused on review"},
		"review in shadow":        {"factory: aws-0\nmergePolicies: [{name: review, mode: shadow}]\n", "binding review: review is bound with mode off"},
		"a negative cap":          {"factory: aws-0\nmergePolicies: [{name: docs, mode: live, maxPerDay: -1}]\n", "maxPerDay is 0"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// B15: each binding resolves through config.Resolve against the policy the factory serves, under
// the guardrails and the repository's merge.
func TestValidateResolvesTheBindings(t *testing.T) {
	c, rc := boundCentral()
	f, err := Parse([]byte(bindingFile))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ValidateFile(f, rc, c)
	if err != nil || len(rep.BindingErrors) != 0 {
		t.Fatalf("%v %+v", err, rep.BindingErrors)
	}
	a, ok := rep.Classes["alert-rules"]
	switch {
	case !ok || !a.Class.Shadow || a.Class.Live:
		t.Fatalf("%+v", rep.Classes)
	case !slices.Equal(a.Class.AllowedFiles, []string{`^observability/.+\.ya?ml$`}) || a.Class.MaxModifiedLines != 40:
		t.Fatalf("%+v", a.Class)
	case !slices.Equal(a.Approval, []string{"^observability/base/"}) || len(a.Class.RequiresApproval) != 1:
		t.Fatalf("approval %+v %+v", a.Approval, a.Class.RequiresApproval)
	case !rep.Classes["docs-links"].Class.Live || rep.Classes["docs-links"].Class.Description != "Fixes a broken link in the docs.":
		t.Fatalf("%+v", rep.Classes["docs-links"])
	}
	if want, _ := config.Resolve("alert-rules", c.Policies["alert-rules"].Spec, f.MergePolicies[2], policyContext(c), rc.Merge); !equalClass(a.Class, want) {
		t.Fatalf("not config.Resolve's class:\n got %+v\nwant %+v", a.Class, want)
	}
}

// B9, B15: a binding that does not resolve is reported apart, and the file and its other
// bindings stay valid.
func TestABindingThatDoesNotResolveIsIsolated(t *testing.T) {
	c, rc := boundCentral()
	c.MergeGuardrails = config.MergeGuardrails{MaxPerDay: 2}
	f, err := Parse([]byte(bindingFile + "  - {name: foo, mode: shadow}\n" +
		"  - {name: alert-rules-2, mode: shadow}\n"))
	if err != nil {
		t.Fatal(err)
	}
	c.Policies["alert-rules-2"] = config.ServedPolicy{Spec: config.PolicySpec{Class: config.Class{AllowedFiles: []string{"^x$"}, MaxPerDay: 5}}}
	rep, err := ValidateFile(f, rc, c)
	if err != nil {
		t.Fatalf("the file stays valid: %v", err)
	}
	if r := rep.BindingErrors["foo"]; r.Reason != v1alpha1.ReasonMissingRef || !strings.Contains(r.Message, "MergePolicy foo") {
		t.Fatalf("foo: %+v", r)
	}
	if r := rep.BindingErrors["alert-rules-2"]; r.Reason != config.ReasonGuardrailMismatch {
		t.Fatalf("alert-rules-2: %+v", r)
	}
	if _, ok := rep.Classes["foo"]; ok || len(rep.Classes) != 3 {
		t.Fatalf("classes %v", slices.Sorted(func(y func(string) bool) {
			for k := range rep.Classes {
				if !y(k) {
					return
				}
			}
		}))
	}
	if err := Validate(f, c); err != nil {
		t.Fatalf("Validate is the file's error alone: %v", err)
	}
}

// B6 in the file: the later of two adopted bindings that would adopt the same pull requests is
// refused, and keeps the earlier serving.
func TestAnAmbiguousFileBindingIsRefused(t *testing.T) {
	c, rc := boundCentral()
	adopt := config.Class{Author: &config.Author{Adopt: &config.Adopt{Logins: []string{"renovate[bot]"}, Branches: []string{"^renovate/"}}},
		DiffShape: "semver-patch", ModifyOnly: true, Harness: "adopted-none", ReleaseAgeCheck: "age"}
	c.Policies["deps-a"] = config.ServedPolicy{Spec: config.PolicySpec{Class: adopt}}
	c.Policies["deps-b"] = config.ServedPolicy{Spec: config.PolicySpec{Class: adopt}}
	f, err := Parse([]byte(bindingFile + "  - {name: deps-b, mode: \"off\"}\n  - {name: deps-a, mode: shadow}\n"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ValidateFile(f, rc, c)
	if err != nil {
		t.Fatal(err)
	}
	if r := rep.BindingErrors["deps-a"]; r.Reason != config.ReasonAmbiguousAdoption || !strings.Contains(r.Message, "deps-b") {
		t.Fatalf("%+v %+v", rep.BindingErrors, rep.Classes["deps-a"])
	}
	if _, ok := rep.Classes["deps-b"]; !ok {
		t.Fatal("the earlier binding serves")
	}
}

// OD-8 holds on the binding names: a file that binds any policy binds docs-links and revert.
func TestABindingFileWithoutRevertIsInvalid(t *testing.T) {
	c, rc := boundCentral()
	f, err := Parse([]byte("factory: aws-0\nmergePolicies:\n  - {name: docs-links, mode: live, holdoutChecks: [x]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFile(f, rc, c); err == nil || !strings.Contains(err.Error(), "revert") {
		t.Fatalf("got %v", err)
	}
}

// PR4: a legacy file branching with any still validates in this release, with a deprecation.
func TestALegacyAnyIsDeprecated(t *testing.T) {
	f, err := Parse([]byte(`factory: aws-0
classes:
  docs-links: {mode: live, author: agents, harness: plain}
  revert: {mode: live, author: agents, harness: plain}
  docs:
    mode: shadow
    author: agents
    harness: plain
    conditions:
      any:
        - [{allowedFiles: ['^docs/.+$']}]
        - [{allowedFiles: ['^README\.md$']}]
`))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ValidateFile(f, nil, central())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Deprecations) != 1 || !strings.Contains(rep.Deprecations[0], "class docs: conditions.any") {
		t.Fatalf("%q", rep.Deprecations)
	}
}

func equalClass(a, b config.Class) bool {
	return config.ResolvedHash(a) == config.ResolvedHash(b) && a.Live == b.Live && a.Shadow == b.Shadow
}

// R30 for the binding shape: review is bound off and nothing else, and parses so.
func TestReviewIsBoundOff(t *testing.T) {
	f, err := Parse([]byte("factory: aws-0\nmergePolicies: [{name: review, mode: off}]\n"))
	if err != nil || f.MergePolicies[0].Mode != config.ModeOff {
		t.Fatalf("%+v %v", f, err)
	}
}
