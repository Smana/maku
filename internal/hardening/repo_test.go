// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"strings"
	"testing"
)

// A repository's fence-file block extends the floor and may only raise the trust floor (UH10, HR6).
func TestParseRepositoryBlock(t *testing.T) {
	many := "protectedPaths:\n" + strings.Repeat("  - path: x/**\n", MaxExtraPaths+1)
	for name, tc := range map[string]struct {
		block    string
		central  Level
		ok       bool
		minTrust Level
		file     string
		on       PathAction
	}{
		"a blocked extension":    {"protectedPaths: [{path: scripts/ci/**, on: blocked}]\n", Approved, true, Approved, "scripts/ci/x.sh", OnBlocked},
		"approval by default":    {"protectedPaths: [{path: scripts/ci/**}]\n", Approved, true, Approved, "scripts/ci/x.sh", OnApproval},
		"the floor holds":        {"protectedPaths: [{path: scripts/ci/**}]\n", Approved, true, Approved, "AGENTS.md", OnApproval},
		"merged is kept":         {"minAuthorTrust: merged\n", Approved, true, Merged, "", ""},
		"equal is kept":          {"minAuthorTrust: approved\n", Approved, true, Approved, "", ""},
		"below a raised central": {"minAuthorTrust: approved\n", Merged, false, Merged, "", ""},
		"a quoted on key":        {"protectedPaths: [{path: deploy/**, \"on\": blocked}]\n", Approved, true, Approved, "deploy/x", OnBlocked},
		"on written twice":       {"protectedPaths: [{path: deploy/**, \"on\": blocked, on: approval}]\n", Approved, false, Merged, "", ""},
		"an empty block":         {"", Approved, true, Approved, "scripts/ci/x.sh", ""},
		"a null block":           {"null", Approved, true, Approved, "", ""},
		"an empty map":           {"{}", Unapproved, true, Unapproved, "", ""},
		"none lowers":            {"minAuthorTrust: none\n", Approved, false, Merged, "", ""},
		"blocked is no level":    {"minAuthorTrust: blocked\n", None, false, Merged, "", ""},
		"a bogus level":          {"minAuthorTrust: trusted\n", Approved, false, Merged, "", ""},
		"an unknown key":         {"slack: true\n", Approved, false, Merged, "", ""},
		"too many paths":         {many, Approved, false, Merged, "", ""},
		"on maybe":               {"protectedPaths: [{path: scripts/ci/**, on: maybe}]\n", Approved, false, Merged, "", ""},
		"an absolute path":       {"protectedPaths: [{path: /etc/x}]\n", Approved, false, Merged, "", ""},
		"a scalar":               {"3\n", Approved, false, Merged, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := ParseRepositoryBlock([]byte(tc.block), tc.central)
			if (err == nil) != tc.ok {
				t.Fatalf("err %v, want ok %v", err, tc.ok)
			}
			if p.MinTrust != tc.minTrust {
				t.Fatalf("min trust %s, want %s", p.MinTrust, tc.minTrust)
			}
			if tc.file != "" {
				if on, _ := p.Paths.Match(tc.file); on != tc.on {
					t.Fatalf("%s: %q, want %q", tc.file, on, tc.on)
				}
			}
			if _, ok := p.Paths.Match(".github/workflows/ci.yaml"); !ok {
				t.Fatal("the floor is always there, refused or not")
			}
		})
	}
}

// A block that does not parse answers the floor and Merged with an error: a typo never loosens.
func TestRepositoryBlockFailsClosed(t *testing.T) {
	p, err := ParseRepositoryBlock([]byte("protectedPaths: [\n"), None)
	if err == nil {
		t.Fatal("unparseable YAML is an error")
	}
	if p.MinTrust != Merged {
		t.Fatalf("min trust %s, want merged", p.MinTrust)
	}
	if on, ok := p.Paths.Match("go.mod"); !ok || on != OnApproval {
		t.Fatal("the floor holds")
	}
	if strings.Contains(err.Error(), "protectedPaths: [") {
		t.Fatalf("the error quotes the file: %v", err)
	}
}

// The cache holds the block of each owned repository's last read fence file, replaced at each read.
func TestRepositoryPolicies(t *testing.T) {
	ctx := t.Context()
	var c RepositoryPolicies
	p, err := c.For("Smana/never-read", Approved)
	if err != nil || p.MinTrust != Approved {
		t.Fatalf("a repository never read is the floor and the central level: %+v %v", p, err)
	}
	c.Observe(ctx, "Smana/Repo", []byte("factory: gcp-0\nhardening:\n  protectedPaths: [{path: scripts/ci/**, on: blocked}]\n  minAuthorTrust: merged\n"))
	p, err = c.For("smana/repo", Approved)
	if err != nil || p.MinTrust != Merged {
		t.Fatalf("%+v %v", p, err)
	}
	if on, _ := p.Paths.Match("scripts/ci/x.sh"); on != OnBlocked {
		t.Fatalf("extension lost: %q", on)
	}
	c.Observe(ctx, "Smana/Repo", []byte("factory: gcp-0\n"))
	p, err = c.For("Smana/Repo", Approved)
	if err != nil || p.MinTrust != Approved {
		t.Fatalf("a read without the block replaces it: %+v %v", p, err)
	}
	if on, _ := p.Paths.Match("scripts/ci/x.sh"); on != "" {
		t.Fatalf("the old extension stayed: %q", on)
	}
	c.Observe(ctx, "Smana/Repo", []byte("factory: gcp-0\nhardening: 3\n"))
	if p, err = c.For("Smana/Repo", None); err == nil || p.MinTrust != Merged {
		t.Fatalf("a block that does not parse reads closed: %+v %v", p, err)
	}
	c.Observe(ctx, "Smana/Repo", []byte("factory: [\n"))
	if p, err = c.For("Smana/Repo", None); err == nil || p.MinTrust != Merged {
		t.Fatalf("a file that does not parse reads closed: %+v %v", p, err)
	}
}

// A block that does not read is reported once per fence read, not on every check that reads it.
func TestRepositoryPoliciesReportOnce(t *testing.T) {
	var c RepositoryPolicies
	if c.ReportOnce("o/r") {
		t.Fatal("a repository never read has nothing to report")
	}
	c.Observe(t.Context(), "o/r", []byte("factory: gcp-0\nhardening: 3\n"))
	if !c.ReportOnce("O/R") || c.ReportOnce("o/r") {
		t.Fatal("reported other than once")
	}
	c.Observe(t.Context(), "o/r", []byte("factory: gcp-0\nhardening: 3\n"))
	if !c.ReportOnce("o/r") {
		t.Fatal("a new read is a new report")
	}
}

func TestRepositoryPoliciesForReusesCompiledPaths(t *testing.T) {
	var c RepositoryPolicies
	c.Observe(t.Context(), "Smana/Repo", []byte("factory: gcp-0\nhardening:\n  protectedPaths: [{path: scripts/ci/**, on: blocked}]\n  minAuthorTrust: approved\n"))
	p1, err := c.For("Smana/Repo", Unapproved)
	if err != nil || p1.MinTrust != Approved {
		t.Fatalf("For(Unapproved) = %+v, %v", p1, err)
	}
	p2, err := c.For("Smana/Repo", Approved)
	if err != nil || p2.MinTrust != Approved {
		t.Fatalf("For(Approved) = %+v, %v", p2, err)
	}
	if len(p1.Paths.rules) == 0 || &p1.Paths.rules[0] != &p2.Paths.rules[0] {
		t.Fatal("For recompiled ProtectedPaths instead of reusing Observe's compiled rules")
	}
	// Raising central above the observed minAuthorTrust still fails closed.
	p3, err := c.For("Smana/Repo", Merged)
	if err == nil || p3.MinTrust != Merged {
		t.Fatalf("For(Merged) = %+v, %v; want error and Merged", p3, err)
	}
}
