// SPDX-License-Identifier: Apache-2.0

package minter

import (
	"flag"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/hardening"
)

var update = flag.Bool("update", false, "rewrite the role table in docs/factory.md from Roles")

type ot = hardening.OutputType

func enf(ts ...ot) []ot { return ts }

func TestTableHR12(t *testing.T) {
	R := shared
	cases := []struct {
		name     string
		role     string
		enforced []ot
		want     Permissions
	}{
		{"implementer, nothing enforced", Implementer, nil, R(Permissions{"contents": "write", "pull_requests": "write"})},
		{"implementer, pr enforced", Implementer, enf(hardening.PR), R(Permissions{"contents": "write", "pull_requests": "read"})},
		{"implementer, comment enforced", Implementer, enf(hardening.Comment), R(Permissions{"contents": "write", "pull_requests": "read"})},
		{"implementer, labels enforced", Implementer, enf(hardening.Labels), R(Permissions{"contents": "write", "pull_requests": "read"})},
		{"implementer, issue alone changes nothing", Implementer, enf(hardening.Issue), R(Permissions{"contents": "write", "pull_requests": "write"})},
		{"implementer, push enforced", Implementer, enf(hardening.Push), R(Permissions{"contents": "read", "pull_requests": "write"})},
		{"implementer, everything enforced", Implementer, hardening.Types(), R(Permissions{"contents": "read", "pull_requests": "read"})},
		{"tester, nothing enforced", Tester, nil, R(Permissions{"contents": "write", "pull_requests": "read"})},
		{"tester, pr changes nothing", Tester, enf(hardening.PR), R(Permissions{"contents": "write", "pull_requests": "read"})},
		{"tester, push enforced", Tester, enf(hardening.Push), R(Permissions{"contents": "read", "pull_requests": "read"})},
		{"reviewer", Reviewer, hardening.Types(), R(Permissions{"contents": "read", "pull_requests": "read"})},
		{"triager", Triager, nil, R(Permissions{"contents": "read", "pull_requests": "read"})},
		{"publisher, pr enforced", Publisher, enf(hardening.PR),
			Permissions{"contents": "read", "pull_requests": "write", "issues": "write", "metadata": "read"}},
		{"publisher, push enforced", Publisher, enf(hardening.Push),
			Permissions{"contents": "write", "pull_requests": "write", "issues": "write", "metadata": "read"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := TableHR12.For(c.role, c.enforced)
			if err != nil || !maps.Equal(got, c.want) {
				t.Fatalf("For(%s, %v) = %v, %v; want %v", c.role, c.enforced, got, err, c.want)
			}
		})
	}
	for _, role := range []string{Reviewer, Triager} {
		for _, set := range subsets() {
			p, _ := TableHR12.For(role, set)
			for k, v := range p {
				if v == "write" {
					t.Fatalf("%s with %v holds %s: write", role, set, k)
				}
			}
		}
	}
}

func TestTableD10(t *testing.T) {
	for _, set := range subsets() {
		p, err := TableD10.For(Implementer, set)
		if err != nil || !maps.Equal(p, shared(Permissions{"contents": "write", "pull_requests": "write"})) {
			t.Fatalf("D10 implementer with %v = %v: enforcement changes nothing", set, p)
		}
	}
	if p, _ := TableD10.For(Reviewer, nil); p["pull_requests"] != "write" || p["contents"] != "read" {
		t.Fatalf("D10 reviewer = %v: read plus review", p)
	}
}

// subsets is every subset of the five output types, the empty one included.
func subsets() [][]ot {
	types := hardening.Types()
	var out [][]ot
	for mask := range 1 << len(types) {
		var s []ot
		for i, t := range types {
			if mask&(1<<i) != 0 {
				s = append(s, t)
			}
		}
		out = append(out, s)
	}
	return out
}

func TestInvariants(t *testing.T) {
	allowed := map[string]bool{"contents": true, "pull_requests": true, "issues": true, "checks": true, "actions": true, "metadata": true}
	for name, table := range map[string]Table{"HR12": TableHR12, "D10": TableD10} {
		if len(table) != len(RoleNames()) {
			t.Fatalf("%s holds %d roles", name, len(table))
		}
		for _, role := range RoleNames() {
			for _, set := range subsets() {
				p, err := table.For(role, set)
				if err != nil {
					t.Fatalf("%s %s: %v", name, role, err)
				}
				if p["metadata"] != "read" {
					t.Fatalf("%s %s %v: metadata read is always asked", name, role, set)
				}
				for k, v := range p {
					switch {
					case !allowed[k]:
						t.Fatalf("%s %s %v holds %s: outside the allowed keys", name, role, set, k)
					case v != "read" && v != "write":
						t.Fatalf("%s %s %v: %s: %s", name, role, set, k, v)
					case k == "issues" && v == "write" && role != Publisher:
						t.Fatalf("%s %s %v holds issues: write: the publisher's alone", name, role, set)
					case (k == "checks" || k == "actions") && v == "write":
						t.Fatalf("%s %s %v holds %s: write", name, role, set, k)
					}
				}
			}
		}
	}
}

func TestUnknownRole(t *testing.T) {
	for _, role := range []string{"scoper", "", "Implementer"} {
		if p, err := Roles.For(role, nil); err == nil {
			t.Fatalf("For(%q) = %v: an unknown role is an error, never read", role, p)
		}
	}
}

func TestForReturnsACopy(t *testing.T) {
	p, _ := Roles.For(Implementer, nil)
	p["contents"] = "admin"
	if q, _ := Roles.For(Implementer, nil); q["contents"] != "write" {
		t.Fatal("For must not hand out the table's own map")
	}
}

func TestRolesIsAVariant(t *testing.T) {
	if !reflect.DeepEqual(Roles, TableHR12) && !reflect.DeepEqual(Roles, TableD10) {
		t.Fatal("Roles is neither TableHR12 nor TableD10")
	}
}

const (
	docsPath   = "../../../docs/factory.md"
	rolesBegin = "<!-- roles:begin -->\n"
	rolesEnd   = "<!-- roles:end -->"
)

func TestDocsTable(t *testing.T) {
	raw, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	head, rest, ok := strings.Cut(doc, rolesBegin)
	if !ok {
		t.Fatal("docs/factory.md has no roles:begin marker")
	}
	got, tail, ok := strings.Cut(rest, rolesEnd)
	if !ok {
		t.Fatal("docs/factory.md has no roles:end marker")
	}
	want := Render(Roles)
	if got == want {
		return
	}
	if *update {
		if err := os.WriteFile(docsPath, []byte(head+rolesBegin+want+rolesEnd+tail), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("docs/factory.md's role table differs from Render(Roles); rerun with -update:\n%s", want)
}

// No code outside this package builds a run's permission map: the pull_requests: write literals
// left are the factory's and the merger's own sets, step 2's shared writer, the demo App's
// manifest and the verdict poster's scope.
func TestOneRoleTable(t *testing.T) {
	allowed := map[string]bool{
		"factory/forge/app.go":         true, // the factory and merger Apps' own scopes
		"factory/runtoken/runtoken.go": true, // step 2's shared writer, deleted at step 8
		"app/maku_demo_app.go":         true, // the demo App's manifest
		"github/app.go":                true, // the verdict poster's scope
	}
	re := regexp.MustCompile(`"pull_requests":\s*"write"`)
	root := "../.."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "factory/minter/") || allowed[rel] {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if re.Match(raw) {
			t.Errorf("%s builds a pull_requests: write map: take it from minter.Roles", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
