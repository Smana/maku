// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"encoding/json"
	"slices"
	"testing"
)

// UH6: gh-aw's five levels, ordered, read from GitHub's author_association and raised by the
// factory's maintainers, trusted bots and a maintainer's vouch; blockedUsers wins over everything.
func TestLevelOf(t *testing.T) {
	p := Policy{Min: Approved, Maintainers: []string{"Maint"}, TrustedBots: []string{"renovate[bot]"}, Blocked: []string{"Spammer"}}
	for name, tc := range map[string]struct {
		a      Author
		vouch  bool
		want   Level
		before bool // withheld
	}{
		"owner":                   {Author{"o", "OWNER"}, false, Approved, false},
		"member":                  {Author{"m", "MEMBER"}, false, Approved, false},
		"collaborator":            {Author{"c", "COLLABORATOR"}, false, Approved, false},
		"contributor":             {Author{"c", "CONTRIBUTOR"}, false, Unapproved, true},
		"first-time contributor":  {Author{"f", "FIRST_TIME_CONTRIBUTOR"}, false, Unapproved, true},
		"first-timer":             {Author{"f", "FIRST_TIMER"}, false, None, true},
		"none":                    {Author{"s", "NONE"}, false, None, true},
		"an unknown association":  {Author{"s", "MANNEQUIN"}, false, None, true},
		"a maintainer, any case":  {Author{"maint", "NONE"}, false, Approved, false},
		"a trusted bot":           {Author{"renovate[bot]", "NONE"}, false, Approved, false},
		"a vouched stranger":      {Author{"s", "NONE"}, true, Approved, false},
		"blocked, even an owner":  {Author{"spammer", "OWNER"}, true, Blocked, true},
		"no login is no one":      {Author{"", "OWNER"}, false, None, true},
		"a lowercase association": {Author{"o", "owner"}, false, None, true},
	} {
		t.Run(name, func(t *testing.T) {
			got := p.LevelOf(tc.a, tc.vouch)
			if got != tc.want || p.Withholds(got) != tc.before {
				t.Fatalf("LevelOf = %s withheld %v, want %s withheld %v", got, p.Withholds(got), tc.want, tc.before)
			}
		})
	}
}

func TestLevelOrderAndText(t *testing.T) {
	if !(Blocked < None && None < Unapproved && Unapproved < Approved && Approved < Merged) {
		t.Fatal("levels are ordered blocked < none < unapproved < approved < merged")
	}
	for _, l := range []Level{Blocked, None, Unapproved, Approved, Merged} {
		back, err := ParseLevel(l.String())
		if err != nil || back != l {
			t.Fatalf("%s round trip: %v %v", l, back, err)
		}
		b, _ := json.Marshal(l)
		var j Level
		if err := json.Unmarshal(b, &j); err != nil || j != l {
			t.Fatalf("%s JSON round trip: %s %v", l, b, err)
		}
	}
	for _, s := range []string{"", "Approved", "trusted", "4"} {
		if _, err := ParseLevel(s); err == nil {
			t.Errorf("%q parsed", s)
		}
	}
	if Level(9).String() != "invalid" {
		t.Fatal("an out-of-range level names itself invalid")
	}
}

// UH7: machine sources get fixed levels, another run's room text its own minimum; the run records
// its lowest level, a count withheld per source, and each source's kind and level, never content.
func TestInputTrust(t *testing.T) {
	in := NewInputTrust()
	in.Record(SourceDefaultBranch, SourceDefaultBranch.Level())
	if in.Min != Merged {
		t.Fatalf("default branch only: %s", in.Min)
	}
	in.Record(SourceOwnCI, SourceOwnCI.Level())
	in.Record(SourceComment, Approved)
	if in.Min != Approved {
		t.Fatalf("own CI and an approved comment: %s", in.Min)
	}
	in.Withhold(SourceComment)
	in.Withhold(SourceComment)
	in.Record(SourceRunLore, SourceRunLore.Level())
	if in.Min != None || in.Withheld[SourceComment] != 2 {
		t.Fatalf("%+v", in)
	}
	// A withheld item never lowers the minimum: the run did not read it.
	if in.Min == Blocked {
		t.Fatal("withheld text counted toward the minimum")
	}
	want := []Source{{SourceDefaultBranch, Merged}, {SourceOwnCI, Approved}, {SourceComment, Approved}, {SourceRunLore, None}}
	if !slices.Equal(in.Sources, want) {
		t.Fatalf("sources %+v", in.Sources)
	}
	// Recording the same kind at the same level twice keeps one row: the record is bounded.
	in.Record(SourceComment, Approved)
	if len(in.Sources) != 4 {
		t.Fatalf("a repeat grew the record: %+v", in.Sources)
	}
	// Another run's room text inherits that run's minimum; a child inherits its parent's.
	child := NewInputTrust()
	child.Inherit(in)
	if child.Min != None || len(child.Sources) != 1 || child.Sources[0] != (Source{SourceParent, None}) {
		t.Fatalf("inherit %+v", child)
	}
	for kind, want := range map[SourceKind]Level{
		SourceDefaultBranch: Merged, SourceSteering: Merged, SourceFactory: Merged,
		SourceOwnCI: Approved, SourceRunLore: None, SourceToolResult: None, SourceForeignCI: None,
	} {
		if kind.Level() != want {
			t.Errorf("%s = %s, want %s", kind, kind.Level(), want)
		}
	}
	if SourceComment.Level() != Blocked || SourceRoom.Level() != Blocked {
		t.Fatal("a human or room source has no fixed level: its caller decides")
	}
}

func TestPlaceholder(t *testing.T) {
	for n, want := range map[int]string{0: "", 1: "[1 comment withheld: author trust]", 3: "[3 comments withheld: author trust]"} {
		if got := Placeholder(n, "comment"); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
}
