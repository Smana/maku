// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// priced is good with a price row: the tiers' USD caps default from their token caps (TC5), as the
// host's do: standard $2.55/$5.10.
var priced = strings.Replace(good, "  logsURL:",
	"  prices: {agent-default: {input: 1.40, cacheRead: 0.26, output: 4.40}}\n  logsURL:", 1)

// Splitter design SL3, offline proof 1: the split parses and resolves its defaults; it is absent by
// default; a row's split reaches every class but docs-links, revert and an oracle class. Rows are
// keyed by workflow (WF10, stage B).
func TestSplitFor(t *testing.T) {
	cfg, err := Parse([]byte(priced + gatewayIdentity + `loops:
  default: {scope: scoper, split: {maxChildren: 2}}
  standard: {scope: scoper, split: {maxChildren: 4}}
  quick: {scope: scoper, split: {maxChildren: 3, familyUSD: 12, childTier: light, anyTier: true}}
  careful: {scope: scoper}
`))
	if err != nil {
		t.Fatal(err)
	}
	const repo = "Smana/cloud-native-ref"
	for _, tc := range []struct {
		class, row string
		want       *SplitPolicy
	}{
		{"docs", "standard", &SplitPolicy{MaxChildren: 4, FamilyUSD: 20.4, ChildTier: "standard"}}, // SP3, SP5
		{"docs", "quick", &SplitPolicy{MaxChildren: 3, FamilyUSD: 12, ChildTier: "light"}},
		{"review", "default", &SplitPolicy{MaxChildren: 2, FamilyUSD: 20.4, ChildTier: "standard"}},
		{"docs", "careful", &SplitPolicy{MaxChildren: 2, FamilyUSD: 20.4, ChildTier: "standard"}}, // default's split (WF10)
		{"docs-links", "standard", nil}, // never split (SL3)
		{"revert", "standard", nil},
	} {
		got := cfg.SplitFor(repo, tc.class, tc.row)
		if tc.row == "quick" && (got == nil || !got.AnyTier) || tc.row == "standard" && got != nil && got.AnyTier {
			t.Errorf("SplitFor(%s, %s).AnyTier = wrong", tc.class, tc.row)
		}
		if (got == nil) != (tc.want == nil) || (got != nil && (got.MaxChildren != tc.want.MaxChildren || got.ChildTier != tc.want.ChildTier ||
			MicroUSD(got.FamilyUSD) != MicroUSD(tc.want.FamilyUSD))) {
			t.Errorf("SplitFor(%s, %s) = %+v, want %+v", tc.class, tc.row, got, tc.want)
		}
	}
	if cfg.LoopFor("standard") != nil {
		t.Error("a split row switched the loop on")
	}
	none, err := Parse([]byte(priced))
	if err != nil {
		t.Fatal(err)
	}
	if none.SplitFor(repo, "docs", "standard") != nil {
		t.Error("no loops: a split is offered (D4)")
	}
}

func TestSplitsAreRefused(t *testing.T) {
	for name, c := range map[string]struct{ base, loops, want string }{
		"without the scoper":    {priced, "{standard: {scope: steering, split: {maxChildren: 3}}}", "loops.standard.split needs scope: scoper on the same row"},
		"maxChildren 6":         {priced, "{standard: {scope: scoper, split: {maxChildren: 6}}}", "loops.standard.split.maxChildren must be 2..5"},
		"no maxChildren":        {priced, "{standard: {scope: scoper, split: {}}}", "loops.standard.split.maxChildren must be 2..5"},
		"an unknown child tier": {priced, "{standard: {scope: scoper, split: {maxChildren: 3, childTier: huge}}}", `childTier "huge" is light, standard or frontier`},
		"below the child tier":  {priced, "{standard: {scope: scoper, split: {maxChildren: 3, familyUSD: 5}}}", "familyUSD 5.00 is below the standard tier's taskUSD 5.10"},
		"familyUSD NaN":         {priced, "{standard: {scope: scoper, split: {maxChildren: 3, familyUSD: .nan}}}", "unsupported value: NaN"},
		"familyUSD negative":    {priced, "{standard: {scope: scoper, split: {maxChildren: 3, familyUSD: -4}}}", "familyUSD must be 0 (the default) or positive and at most 1000"},
		"familyUSD huge":        {priced, "{standard: {scope: scoper, split: {maxChildren: 3, familyUSD: 1e300}}}", "familyUSD must be 0 (the default) or positive and at most 1000"},
		"no prices":             {good, "{standard: {scope: scoper, split: {maxChildren: 3}}}", "loops.standard.split needs meter.prices"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.base + "loops: " + c.loops + "\n"))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}
