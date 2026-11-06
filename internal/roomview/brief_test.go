// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

// briefGoldens are the five briefs golden 4 draws: a full read, one with no cursor, the hostile title of
// Review Focus 4, and the two failures fast-follow 6 turns into a fix (PR 7).
var briefGoldens = []string{"normal", "no-cursor", "hostile", "permission-failure", "egress-failure"}

// loadBrief decodes one brief fixture: the file is the renderer's whole input, its now included.
func loadBrief(t *testing.T, name string) BriefIn {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "brief", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var in BriefIn
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

// CD11: the brief is fixed prose and quoted data, and every rendering parses inert (offline proof 8).
func TestBriefGoldens(t *testing.T) {
	for _, name := range briefGoldens {
		t.Run(name, func(t *testing.T) {
			in := loadBrief(t, name)
			if len(in.Untrusted()) == 0 {
				t.Fatal("the fixture has no untrusted string: the inert assertion would be vacuous")
			}
			var b strings.Builder
			if err := Brief(&b, in, Options{Format: Markdown, Now: in.Now, Rooms: rooms}); err != nil {
				t.Fatal(err)
			}
			golden(t, "brief/"+name+".md", b.String())
			assertInert(t, b.String(), rooms, in.Untrusted())
		})
	}
}

// Fast-follow 6: a failure the platform meets most says in one line what to change, after the blocker it
// explains and before the next step — summary.Fix's sentence, the renderer's only source for it.
func TestTheBriefNamesTheFix(t *testing.T) {
	for name, cause := range map[string]string{
		"permission-failure": summary.CauseAppPermission,
		"egress-failure":     summary.CauseCILogEgress,
	} {
		t.Run(name, func(t *testing.T) {
			in := loadBrief(t, name)
			if in.Cause != cause {
				t.Fatalf("the fixture's cause is %q, want %q", in.Cause, cause)
			}
			var b strings.Builder
			if err := Brief(&b, in, Options{Format: Markdown, Now: in.Now, Rooms: rooms}); err != nil {
				t.Fatal(err)
			}
			got := b.String()
			if lines := fixLines(got); len(lines) != 1 || lines[0] != "Fix: "+causeText(t, in.Cause) {
				t.Fatalf("%q:\n%s", lines, got)
			}
			blocked, fix := strings.Index(got, "\nBlocked on: "), strings.Index(got, "\nFix: ")
			if blocked < 0 || fix < blocked {
				t.Fatalf("Fix does not follow the blocker:\n%s", got)
			}
		})
	}
}

// Every cause's sentence is prose, not a quoted value: it names a file, a key and a host, and the backticks
// around the two keys must not open a code span of their own — the brief writes `text`, never `quoted`.
func TestEveryFixIsProse(t *testing.T) {
	for _, cause := range []string{summary.CauseAppPermission, summary.CauseCheckUnreadable, summary.CauseCILogEgress} {
		t.Run(cause, func(t *testing.T) {
			// The configuration failure has no golden: its sentence is the one with backticks and dots in it.
			in := BriefIn{Room: "26zfnuxm", RoomURL: "https://rooms.example/r/26zfnuxm",
				Facts: &envelope.TaskFacts{Phase: "AwaitingHuman"}, Cause: cause, Now: now}
			var b strings.Builder
			if err := Brief(&b, in, Options{Format: Markdown, Now: in.Now, Rooms: rooms}); err != nil {
				t.Fatal(err)
			}
			got := b.String()
			if lines := fixLines(got); len(lines) != 1 || lines[0] != "Fix: "+causeText(t, cause) {
				t.Fatalf("%q:\n%s", lines, got)
			}
			if spans := codeSpans(got); len(spans) != 0 {
				t.Errorf("the fix opened a code span of its own: %q", spans)
			}
			assertInert(t, got, rooms, nil)
		})
	}
}

// causeText is a fix as the brief writes it: in Markdown the sentence's punctuation is escaped, so the
// assertion reads it back the way a reader sees it.
func causeText(t *testing.T, cause string) string {
	t.Helper()
	fix := summary.Fix(cause)
	if fix == "" {
		t.Fatalf("%q has no fix", cause)
	}
	var b strings.Builder
	c := &card{o: Options{Format: Markdown}}
	b.WriteString(c.text(fix, false))
	return b.String()
}

// fixLines is the brief's Fix lines, in order: a cause earns at most one, and it is a line of its own.
func fixLines(md string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(md, "\n"), "\n") {
		if strings.HasPrefix(l, "Fix: ") {
			out = append(out, l)
		}
	}
	return out
}

// The same words in the brief, whose plan line reads the facts and not a Summary.
func TestTheBriefSaysSpendSettling(t *testing.T) {
	in := BriefIn{Room: "26zfnuxm", RoomURL: "https://rooms.example/r/26zfnuxm",
		Facts: &envelope.TaskFacts{Phase: "Done", Spend: &envelope.SpendFact{UsedMicroUSD: 3_100_000, LimitMicroUSD: 5_000_000}},
		Plan:  PlanVersion{Source: "scope", CeilingMicroUSD: 5_000_000}, Now: time.Now()}
	var b strings.Builder
	if err := Brief(&b, in, Options{Format: Text, Now: in.Now}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "spend settling") {
		t.Fatalf("%s", b.String())
	}
}

// CD23: once the factory's final fact has landed the plan line names the figure, a task whose runs the
// meter could not price says its dollars are unpriced rather than settling for ever, and a sealed room
// — whose log takes no other fact — names the figure it froze.
func TestTheBriefSaysTheSpendState(t *testing.T) {
	plan := PlanVersion{Source: "scope", CeilingMicroUSD: 5_000_000}
	for _, tc := range []struct {
		name      string
		spend     *envelope.SpendFact
		budget    *envelope.BudgetFact
		final     bool
		sealed    string
		want      string
		notWanted string
	}{
		{"the final figure", &envelope.SpendFact{UsedMicroUSD: 3_100_000, LimitMicroUSD: 5_000_000}, nil, true, "", "spend $3.10", "settling"},
		{"no figure", nil, &envelope.BudgetFact{UsedTokens: 182_000, LimitTokens: 1_500_000}, false, "", "spend unpriced", "settling"},
		{"a sealed room froze its figure", &envelope.SpendFact{UsedMicroUSD: 3_100_000, LimitMicroUSD: 5_000_000}, nil, false, "Closed", "spend $3.10", "settling"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := BriefIn{Room: "26zfnuxm", RoomURL: "https://rooms.example/r/26zfnuxm", Sealed: tc.sealed,
				Facts: &envelope.TaskFacts{Phase: "Done", Spend: tc.spend, Budget: tc.budget, SpendFinal: tc.final},
				Plan:  plan, Now: now}
			var b strings.Builder
			if err := Brief(&b, in, Options{Format: Text, Now: in.Now}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(b.String(), tc.want) || strings.Contains(b.String(), tc.notWanted) {
				t.Fatalf("want %q, not %q in\n%s", tc.want, tc.notWanted, b.String())
			}
		})
	}
}
