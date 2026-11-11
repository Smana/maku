// SPDX-License-Identifier: Apache-2.0

package sanitize

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecisionGrammar(t *testing.T) {
	const acceptance = "## Acceptance check\ntask check\n"
	for _, c := range []struct {
		name string
		body string
		want []DecisionRecord
	}{
		{
			name: "interleaved planner and scoper records",
			body: acceptance + "\n## Decisions\n\nD1 [g1]: first choice\nA2 [g2]: assumed second\nD4: scoped later",
			want: []DecisionRecord{
				{Kind: "D", Number: 1, Gap: "g1", Text: "first choice"},
				{Kind: "A", Number: 2, Gap: "g2", Text: "assumed second"},
				{Kind: "D", Number: 4, Text: "scoped later"},
			},
		},
		{
			name: "split issue subset may skip numbers",
			body: acceptance + "\n## Decisions\nD1: first\nD3: third",
			want: []DecisionRecord{{Kind: "D", Number: 1, Text: "first"}, {Kind: "D", Number: 3, Text: "third"}},
		},
		{name: "section is optional", body: acceptance},
		{name: "record-like text elsewhere is not a record", body: "D1: not in Decisions\n" + acceptance},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := DecisionRecords(c.body)
			if err != nil {
				t.Fatalf("DecisionRecords() error = %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("DecisionRecords() = %#v, want %#v", got, c.want)
			}
		})
	}

	for _, c := range []struct {
		name    string
		records string
	}{
		{name: "duplicate", records: "D1: first\nD1: duplicate"},
		{name: "duplicate across kinds", records: "D1: first\nA1: duplicate"},
		{name: "descending", records: "D2: descending\nD1: later"},
		{name: "zero gap", records: "D1 [g0]: bad gap"},
		{name: "malformed gap", records: "D1 [g-1]: bad gap"},
		{name: "zero record", records: "D0: bad record"},
		{name: "empty text", records: "D1 [g1]: "},
		{name: "continuation is not single line", records: "D1 [g1]: first\ncontinued"},
	} {
		t.Run("reject "+c.name, func(t *testing.T) {
			body := acceptance + "\n## Decisions\n\n" + c.records
			if _, err := DecisionRecords(body); err == nil || !strings.HasPrefix(err.Error(), "invalid_decisions:") {
				t.Fatalf("DecisionRecords(%q) error = %v", body, err)
			}
		})
	}

	t.Run("reject Decisions before Acceptance", func(t *testing.T) {
		body := "## Decisions\nD1: too soon\n\n## Acceptance check\ntask check"
		if _, err := DecisionRecords(body); err == nil || !strings.HasPrefix(err.Error(), "invalid_decisions:") {
			t.Fatalf("DecisionRecords() error = %v", err)
		}
	})
}

func TestFinalDraft(t *testing.T) {
	const title = "docs: clarify the install command"
	validDecisions := goodBody + "\n\n## Decisions\nD1 [g1]: use task check\nA2 [g2]: assume Linux"
	for _, c := range []struct {
		name, body, want string
	}{
		{name: "without Decisions", body: goodBody},
		{name: "with valid Decisions", body: validDecisions},
		{name: "invalid draft keeps its error", body: "not the template", want: "invalid_draft:"},
		{name: "invalid Decisions", body: goodBody + "\n\n## Decisions\nD2: later\nD1: earlier", want: "invalid_decisions:"},
		{name: "remaining marker", body: strings.Replace(goodBody, "x", "[NEEDS CLARIFICATION: g1 which command?]", 1), want: "unresolved_marker:"},
		{name: "malformed marker ID", body: strings.Replace(goodBody, "x", "[NEEDS CLARIFICATION: g0 which command?]", 1), want: "unresolved_marker:"},
		{name: "duplicate marker IDs", body: goodBody + "\n[NEEDS CLARIFICATION: g1 first?]\n[NEEDS CLARIFICATION: g1 second?]", want: "unresolved_marker:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, err := CheckFinalDraft(title, c.body)
			if c.want == "" {
				if err != nil {
					t.Fatalf("CheckFinalDraft() error = %v", err)
				}
				if d.Title != title || d.Body != c.body {
					t.Fatalf("CheckFinalDraft() = %+v", d)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Fatalf("CheckFinalDraft() error = %v, want prefix %q", err, c.want)
			}
		})
	}

	t.Run("marker parser returns text and byte offsets", func(t *testing.T) {
		body := "é\n[NEEDS CLARIFICATION: g2 which?]\ntail"
		got, err := ClarificationMarkers(body)
		if err != nil {
			t.Fatalf("ClarificationMarkers() error = %v", err)
		}
		want := []ClarificationMarker{{ID: "g2", Text: "which?", Start: 3, End: 35}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ClarificationMarkers() = %#v, want %#v", got, want)
		}
	})
}
