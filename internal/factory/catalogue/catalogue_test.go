// SPDX-License-Identifier: Apache-2.0

package catalogue

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

// live is shaped like ai-native-ref's config: docs-links live, review off with variants.
func live() *config.Config {
	return &config.Config{
		Defaults: config.Defaults{Tier: "standard"},
		Tiers:    map[string]config.Tier{"light": {TaskUSD: 2}, "standard": {TaskUSD: 4}, "frontier": {TaskUSD: 8}},
		Templates: map[string]config.Template{
			"solo": {Roles: []string{"implementer"}},
			"trio": {Roles: []string{"implementer", "tester", "reviewer"}},
		},
		HarnessProfiles: map[string]config.HarnessProfile{
			"links-solo":      {Template: "solo", Tier: "light", Description: "One file of links, no logic"},
			"review-floor":    {Checks: true, Description: "The default: triage picks pair or trio"},
			"review-frontier": {Template: "trio", Tier: "frontier", Checks: true, TestFirst: true, Description: "Changes across several files"},
		},
		Evaluation: config.Evaluation{Mode: "observe", ControlPercent: 20},
		Repositories: []config.Repository{{
			Name:        "Smana/cloud-native-ref",
			Maintainers: []string{"Smana"},
			Merge:       config.Merge{ApproveToMerge: true},
			Classes: map[string]config.Class{
				"docs-links": {Description: "Dead links in docs", Live: true, Harness: "links-solo"},
				"review": {Description: "Anything else: a maintainer merges", Harness: "review-floor",
					HarnessAllowed: []string{"review-frontier"},
					Escalation:     &config.Escalation{OutOfBudget: "review-frontier", NotConverged: "review-frontier"}},
				"revert":     {Shadow: true},
				"deps-patch": {Shadow: true, Author: &config.Author{Adopt: &config.Adopt{Logins: []string{"renovate[bot]"}}}},
				"oracled":    {Oracle: &config.Oracle{}},
			},
		}},
	}
}

func names(c Catalogue) []string {
	var out []string
	for _, cl := range c.Classes {
		out = append(out, cl.Name)
	}
	return out
}

func TestBuildOffersClasses(t *testing.T) {
	c, ok := Build(live(), "smana/CLOUD-native-ref", true)
	if !ok {
		t.Fatal("served repository: not built")
	}
	if got := names(c); !slices.Equal(got, []string{"docs-links", "review"}) {
		t.Fatalf("offered = %v: want docs-links, review (no revert, adopted or oracle class)", got)
	}
	if c.Repository != "Smana/cloud-native-ref" {
		t.Fatalf("repository = %q: want the configured spelling", c.Repository)
	}
	dl, _ := c.Class("docs-links")
	if dl.Mode != ModeLive || dl.Floor != "links-solo" || dl.CeilingUSD != 2 || len(dl.Allowed) != 0 || dl.Escalation != nil {
		t.Fatalf("docs-links = %+v", dl)
	}
	rv, _ := c.Class("review")
	if rv.Mode != ModeOff || rv.Floor != "review-floor" || !slices.Equal(rv.Allowed, []string{"review-frontier"}) ||
		rv.Escalation == nil || rv.Escalation.OutOfBudget != "review-frontier" || rv.CeilingUSD != 4 {
		t.Fatalf("review = %+v", rv)
	}
	if !c.ApproveToMerge || !c.Maintainer || c.Evaluation != (Evaluation{Mode: "observe", ControlPercent: 20}) {
		t.Fatalf("catalogue = %+v", c)
	}

	t.Run("review comes from the factory default when the repository omits it", func(t *testing.T) {
		cfg := live()
		delete(cfg.Repositories[0].Classes, "review")
		c, _ := Build(cfg, "Smana/cloud-native-ref", false)
		rv, ok := c.Class("review")
		if !ok || rv.Mode != ModeOff || rv.Floor != "" || len(rv.Allowed) != 0 || c.Maintainer {
			t.Fatalf("review = %+v, %t", rv, ok)
		}
	})
	t.Run("a repository with no classes offers review", func(t *testing.T) {
		cfg := live()
		cfg.Repositories[0].Classes = nil
		c, _ := Build(cfg, "Smana/cloud-native-ref", false)
		if got := names(c); !slices.Equal(got, []string{"review"}) {
			t.Fatalf("offered = %v", got)
		}
	})
}

func TestProfileOffered(t *testing.T) {
	c, _ := Build(live(), "Smana/cloud-native-ref", false)
	if p := c.Profiles["links-solo"]; !p.Offered || p.NotOffered != "" || !slices.Equal(p.Roles, []string{"implementer"}) || p.Tier != "light" {
		t.Fatalf("links-solo = %+v", p)
	}
	if p := c.Profiles["review-frontier"]; !p.Offered || !slices.Equal(p.Roles, []string{"implementer", "tester", "reviewer"}) || !p.TestFirst {
		t.Fatalf("review-frontier = %+v", p)
	}
	if p := c.Profiles["review-floor"]; p.Offered || p.NotOffered != "no_template" || p.Roles != nil {
		t.Fatalf("review-floor = %+v: a profile with no template is not offered (P15)", p)
	}
	if got := c.OfferedProfiles("review"); !slices.Equal(got, []string{"review-frontier"}) {
		t.Fatalf("OfferedProfiles(review) = %v: the unoffered floor is left out", got)
	}
	if got := c.OfferedProfiles("docs-links"); !slices.Equal(got, []string{"links-solo"}) {
		t.Fatalf("OfferedProfiles(docs-links) = %v", got)
	}
	if got := c.OfferedProfiles("nope"); got != nil {
		t.Fatalf("OfferedProfiles(nope) = %v", got)
	}
	if _, ok := c.Profiles["revert"]; ok {
		t.Fatal("profiles of classes that are not offered leak into the catalogue")
	}
}

func TestModeOf(t *testing.T) {
	for _, tc := range []struct {
		name   string
		class  config.Class
		gateOn bool
		want   string
	}{
		{"live", config.Class{Live: true}, true, ModeLive},
		{"shadow", config.Class{Shadow: true}, true, ModeShadow},
		{"neither, gate on", config.Class{}, true, ModeOff},
		{"live with the gate off", config.Class{Live: true}, false, ModeUnarmed},
		{"neither, gate off", config.Class{}, false, ModeUnarmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModeOf(tc.class, tc.gateOn); got != tc.want {
				t.Fatalf("ModeOf = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOutcome(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		approve bool
		want    string
	}{
		{ModeLive, false, OutcomeAuto},
		{ModeLive, true, OutcomeAuto},
		{ModeShadow, true, OutcomeShadow},
		{ModeOff, true, OutcomeApproval},
		{ModeOff, false, OutcomeMaintainer},
		{ModeUnarmed, true, OutcomeMaintainer},
		{ModeUnarmed, false, OutcomeMaintainer},
	} {
		if got := Outcome(tc.mode, tc.approve); got != tc.want {
			t.Errorf("Outcome(%s, %t) = %q, want %q", tc.mode, tc.approve, got, tc.want)
		}
	}
}

func TestBuildUnservedRepository(t *testing.T) {
	if _, ok := Build(live(), "someone/else", true); ok {
		t.Fatal("an unserved repository was built")
	}
}

func TestBuildGateOff(t *testing.T) {
	cfg := live()
	cfg.Repositories[0].Classes = map[string]config.Class{"review": {Harness: "review-floor"}}
	c, _ := Build(cfg, "Smana/cloud-native-ref", false)
	rv, _ := c.Class("review")
	if rv.Mode != ModeUnarmed || c.ApproveToMerge {
		t.Fatalf("gate off: review %+v, approveToMerge %t: want unarmed and no approval", rv, c.ApproveToMerge)
	}
}

func TestBuildRepositoryAuthority(t *testing.T) {
	cfg := live()
	cfg.PolicyManagement.RepositoryAuthority = true
	cfg.Repositories[0].ClassAuthority = config.ClassAuthorityRepository
	cfg.Repositories[0].Classes = map[string]config.Class{"stale": {Harness: "links-solo"}}
	cfg.SetClassSource(func(string) (map[string]config.Class, bool) {
		return map[string]config.Class{"from-file": {Description: "from the repository's file", Harness: "links-solo"}}, true
	})
	c, _ := Build(cfg, "Smana/cloud-native-ref", false)
	if got := names(c); !slices.Equal(got, []string{"from-file", "review"}) {
		t.Fatalf("offered = %v: want the repository file's classes", got)
	}
}

func TestBuildGenerations(t *testing.T) {
	cfg := live()
	cfg.Generations = config.Generations{Factory: 12, Repos: map[string]config.RepoGenerations{
		"smana/cloud-native-ref": {Repository: 4, MergePolicy: 9}}}
	c, _ := Build(cfg, "Smana/cloud-native-ref", false)
	if c.Generations != (Generations{Factory: 12, Repository: 4, MergePolicy: 9}) {
		t.Fatalf("generations = %+v", c.Generations)
	}
}

func TestCatalogueJSON(t *testing.T) {
	c, _ := Build(live(), "Smana/cloud-native-ref", true)
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"ceilingUSD":2`, `"floor":"links-solo"`, `"allowed":[]`, `"escalation":null`,
		`"generations":{"factory":0,"repository":0,"mergePolicy":0}`, `"notOffered":"no_template"`, `"approveToMerge":true`,
		`"maintainer":true`, `"controlPercent":20`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON lacks %s:\n%s", want, b)
		}
	}
}
