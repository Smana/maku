// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"
	"testing"
)

// cutOver is good's repository with its classes in its own policy file (merge policy stage 4).
func cutOver() Repository {
	c, err := Parse([]byte(good))
	if err != nil {
		panic(err)
	}
	r := c.Repos()[0]
	r.Name, r.Classes, r.ClassAuthority = "Smana/a", nil, ClassAuthorityRepository
	return r
}

func TestRepositoryAuthorityValidates(t *testing.T) {
	for name, tc := range map[string]struct {
		pm   PolicyManagement
		repo func(*Repository)
		want string
	}{
		"the switch with read":       {PolicyManagement{Read: true, RepositoryAuthority: true}, func(*Repository) {}, ""},
		"the switch alone":           {PolicyManagement{RepositoryAuthority: true}, func(*Repository) {}, "repositoryAuthority needs read"},
		"a cut-over without classes": {PolicyManagement{Read: true, RepositoryAuthority: true}, func(*Repository) {}, ""},
		"a cut-over, the switch off": {PolicyManagement{Read: true}, func(*Repository) {}, "needs policyManagement.repositoryAuthority"},
		"two sources": {PolicyManagement{Read: true, RepositoryAuthority: true},
			func(r *Repository) { r.Classes = map[string]Class{"docs-links": {}} }, "two authoritative sources"},
		"an unknown authority": {PolicyManagement{Read: true, RepositoryAuthority: true},
			func(r *Repository) { r.ClassAuthority = "git" }, `classAuthority "git"`},
		"a schedule naming a class the file declares": {PolicyManagement{Read: true, RepositoryAuthority: true},
			func(r *Repository) {
				r.Schedules = []Schedule{{Name: "links", Cron: "0 6 * * 1", Class: "dead-links", Text: "fix links", Probe: "dead-links", ProbeAuthor: "x"}}
			}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := cutOver()
			tc.repo(&r)
			c := &Config{PolicyManagement: tc.pm, Caps: Caps{MaxTextBytes: 100}}
			var out []string
			bad := func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }
			c.validateMergePolicy(bad)
			c.validateRepository(&r, map[string]bool{}, bad)
			got := strings.Join(out, "\n")
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The repository's classes reach every reader through Repos and RepositoryFor, once a source is
// set: the gate, triage, intake and the monitor read one set of classes (MP15).
func TestACutOverRepositoryReadsItsClassesFromTheSource(t *testing.T) {
	central := Repository{Name: "Smana/b", Classes: map[string]Class{"docs-links": {Shadow: true}}}
	c := &Config{Repositories: []Repository{cutOver(), central}, PolicyManagement: PolicyManagement{Read: true, RepositoryAuthority: true}}
	if got := c.RepositoryFor("smana/a").Classes; got != nil {
		t.Fatalf("no source yet: %v", got)
	}
	c.SetClassSource(func(repository string) (map[string]Class, bool) {
		if repository != "Smana/a" {
			t.Fatalf("asked for %s", repository)
		}
		return map[string]Class{"docs-links": {Live: true}}, true
	})
	if !c.RepositoryFor("Smana/a").Classes["docs-links"].Live {
		t.Fatal("the source's class")
	}
	if !c.RepositoryFor("Smana/b").Classes["docs-links"].Shadow {
		t.Fatal("a central repository keeps its classes")
	}
	if c.Repositories[0].Classes != nil {
		t.Fatal("the config itself is not written")
	}
}

// Off, a source is never read: the switch gates it, whatever is wired.
func TestTheSourceIsReadOnlyWithTheSwitch(t *testing.T) {
	r := cutOver()
	c := &Config{Repositories: []Repository{r}}
	c.SetClassSource(func(string) (map[string]Class, bool) {
		t.Fatal("read with the switch off")
		return nil, false
	})
	_ = c.Repos()
}

// A cut-over repository's classes arrive at run time, so what the binary builds at start assumes
// they may be live, monitored and watching Flux.
func TestACutOverRepositoryNeedsTheMerger(t *testing.T) {
	c := &Config{Repositories: []Repository{cutOver()}, PolicyManagement: PolicyManagement{Read: true, RepositoryAuthority: true}}
	if !c.NeedsMerger() || !c.AnyMonitored() || !c.AnyFlux() {
		t.Fatal("a cut-over repository may hold any class")
	}
	if (&Config{Repositories: []Repository{{Name: "Smana/b"}}}).NeedsMerger() {
		t.Fatal("no class, no merger")
	}
}
