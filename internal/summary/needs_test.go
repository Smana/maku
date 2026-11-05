// SPDX-License-Identifier: Apache-2.0

package summary_test

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

// needsWant is one item a case expects, as testdata/needs/cases.json writes it.
type needsWant struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Action     string `json:"action"`
	URL        string `json:"url"`
	WhatPrefix string `json:"whatPrefix"`
	Approve    bool   `json:"approve"`
}

// needsCaller is one caller a case is asked about: what the policy says, and their GitHub standing.
type needsCaller struct {
	Login      string `json:"login"`
	WebUI      bool   `json:"webui"`
	CanClose   bool   `json:"canClose"`
	CanDecide  bool   `json:"canDecide"`
	CanRelease bool   `json:"canRelease"`
	MayWrite   bool   `json:"mayWrite"`
	RoomScoped bool   `json:"roomScoped"`
}

// needsCase is one room in one fixture: its facts, its approvals, and what each caller is owed.
type needsCase struct {
	Name       string              `json:"name"`
	Room       string              `json:"room"`
	Repository string              `json:"repository"`
	FactsAt    time.Time           `json:"factsAt"`
	Facts      *envelope.TaskFacts `json:"facts"`
	Reviewers  []string            `json:"reviewers"`
	Approvals  []struct {
		ID        string    `json:"id"`
		What      string    `json:"what"`
		ExpiresAt time.Time `json:"expiresAt"`
		At        time.Time `json:"at"`
	} `json:"approvals"`
	Callers map[string]needsCaller `json:"callers"`
	Want    map[string][]needsWant `json:"want"`
}

// in is the case as caller sees it: one NeedsIn, as every surface builds it (CD21, CD26).
func (c needsCase) in(caller, now string) summary.NeedsIn {
	cu := c.Callers[caller]
	in := summary.NeedsIn{
		Room: c.Room, URL: "https://rooms.example/r/" + c.Room, Repository: c.Repository,
		FactsRead: true, Facts: c.Facts, FactsAt: c.FactsAt,
		GitHub: summary.GitHubStanding{Login: cu.Login, MayWrite: cu.MayWrite, Reviewers: c.Reviewers},
		Caller: summary.Caller{WebUI: cu.WebUI, CanDecide: cu.CanDecide, CanClose: cu.CanClose,
			CanRelease: cu.CanRelease, RoomScoped: cu.RoomScoped},
	}
	in.Now, _ = time.Parse(time.RFC3339, now)
	for _, a := range c.Approvals {
		in.Approvals = append(in.Approvals, summary.Approval{ID: a.ID, What: a.What, ExpiresAt: a.ExpiresAt, At: a.At})
	}
	return in
}

// wantAt is the i-th item the case expects for caller, if it names one.
func (c needsCase) wantAt(i int, caller string) needsWant {
	if w := c.Want[caller]; i < len(w) {
		return w[i]
	}
	return needsWant{}
}

// wantKinds is the case's items for caller as "<kind>/<id>/<action kind>", in the only order (CD21).
func (c needsCase) wantKinds(caller string) []string {
	var out []string
	for _, w := range c.Want[caller] {
		out = append(out, w.Kind+"/"+w.ID+"/"+w.Action)
	}
	return out
}

// loadNeedsCases reads the fixture: the one table every surface's derivation test asserts against.
func loadNeedsCases(t *testing.T) (now string, cases []needsCase) {
	t.Helper()
	b, err := os.ReadFile("testdata/needs/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Now   string      `json:"now"`
		Cases []needsCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("testdata/needs/cases.json: %v", err)
	}
	if f.Now == "" || len(f.Cases) == 0 {
		t.Fatal("testdata/needs/cases.json names no now or no cases")
	}
	return f.Now, f.Cases
}

// allowedURL reports whether a need's link is one a view may render as a link: the room's page or a
// validated GitHub one, https only. NeedsIn builds every action URL from the room URL and the facts'
// validated URLs, never from a note (needs/v1).
func allowedURL(u string) bool {
	return strings.HasPrefix(u, "https://rooms.example/") || strings.HasPrefix(u, "https://github.com/")
}

// The fixture is the contract: every surface asserts against these same cases (CD21, offline proof 6).
func TestNeedsCases(t *testing.T) {
	now, cases := loadNeedsCases(t)
	for _, c := range cases {
		for caller := range c.Callers {
			got := summary.Ordered(summary.Items(c.in(caller, now)))
			var kinds []string
			for _, n := range got {
				kinds = append(kinds, n.Kind+"/"+n.ID+"/"+n.Action.Kind)
				if n.Action.URL != "" && !allowedURL(n.Action.URL) {
					t.Fatalf("%s/%s: off-allowlist url %q", c.Name, caller, n.Action.URL)
				}
				if !strings.HasPrefix(n.URL, "https://") { // the kind's own link is a validated one too
					t.Fatalf("%s/%s: off-allowlist need url %q", c.Name, caller, n.URL)
				}
			}
			if !slices.Equal(kinds, c.wantKinds(caller)) {
				t.Fatalf("%s/%s: %v, want %v", c.Name, caller, kinds, c.wantKinds(caller))
			}
			for i, n := range got {
				if p := c.wantAt(i, caller).WhatPrefix; p != "" && !strings.HasPrefix(n.What, p) {
					t.Fatalf("%s/%s: %q does not start with %q", c.Name, caller, n.What, p)
				}
				if want := c.wantAt(i, caller).Approve; n.Approve != want {
					t.Fatalf("%s/%s: approve %v, want %v (CD26, AM3)", c.Name, caller, n.Approve, want)
				}
				if u := c.wantAt(i, caller).URL; u != "" && n.Action.URL != u {
					t.Fatalf("%s/%s: action url %q, want %q", c.Name, caller, n.Action.URL, u)
				}
			}
		}
	}
}

// CD21's kind order is the only ordering: a deadline first, then the kind, then the room (offline proof 6).
func TestNeedsOrder(t *testing.T) {
	in := []summary.Need{
		{Room: "bbbbbbbb", Kind: "hold", ID: "x"},
		{Room: "aaaaaaaa", Kind: "merge", ID: "12"},
		{Room: "cccccccc", Kind: "merge", ID: "13"},
		{Room: "aaaaaaaa", Kind: "approval", ID: "01", Deadline: time.Date(2026, 11, 4, 19, 20, 0, 0, time.UTC)},
	}
	var got []string
	for _, n := range summary.Ordered(in) {
		got = append(got, n.Key())
	}
	want := []string{"aaaaaaaa/approval/01", "aaaaaaaa/merge/12", "cccccccc/merge/13", "bbbbbbbb/hold/x"}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}

// A sealed room contributes nothing: its last facts outlive the PR it names (Review Focus 2).
func TestASealedRoomHasNoNeeds(t *testing.T) {
	got := summary.Items(summary.NeedsIn{Room: "26zfnuxm", Sealed: true,
		Facts:  &envelope.TaskFacts{Phase: "AwaitingHuman", PR: &envelope.PRFact{Number: 9, URL: "https://github.com/Smana/ai-native-ref/pull/9"}},
		GitHub: summary.GitHubStanding{Login: "ann", MayWrite: true}})
	if len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
