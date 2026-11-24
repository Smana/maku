// SPDX-License-Identifier: Apache-2.0

package catalogue

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

// live is shaped like ai-native-ref's config: docs-links live, review off, three workflows and none.
func live() *config.Config {
	return &config.Config{
		Tiers: map[string]config.Tier{"light": {TaskUSD: 2}, "standard": {TaskUSD: 4}, "frontier": {TaskUSD: 8}},
		Templates: map[string]config.Template{
			"solo": {Roles: []string{"implementer"}},
			"pair": {Roles: []string{"implementer", "reviewer"}},
		},
		HarnessProfiles: map[string]config.HarnessProfile{
			"quick":    {Template: "solo", Tier: "light", Description: "One implementer"},
			"standard": {Checks: true, Description: "The default: triage picks the template"},
			"careful": {Template: "pair", Tier: "frontier", Checks: true, Description: "Changes across several files",
				Escalation: &config.Escalation{NotConverged: "standard"}},
			"none": {Template: config.TemplateNone},
		},
		Loops:      map[string]config.Loop{"careful": {Tests: true}},
		Evaluation: config.Evaluation{Mode: "observe", ControlPercent: 20, Treatment: "careful"},
		Repositories: []config.Repository{{
			Name:        "Smana/cloud-native-ref",
			Maintainers: []string{"Smana"},
			Merge:       config.Merge{ApproveToMerge: true, ContractCheck: "contract"},
			Workflow:    config.RepositoryWorkflow{Default: "careful", TestFirst: true},
			Classes: map[string]config.Class{
				"docs-links": {Description: "Dead links in docs", Live: true},
				"review":     {Description: "Anything else: a maintainer merges"},
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
	// Every class's ceiling is the default workflow's tier's (careful, frontier).
	if dl, _ := c.Class("docs-links"); dl != (Class{Name: "docs-links", Description: "Dead links in docs", Mode: ModeLive, CeilingUSD: 8}) {
		t.Fatalf("docs-links = %+v", dl)
	}
	if rv, _ := c.Class("review"); rv.Mode != ModeOff || rv.CeilingUSD != 8 {
		t.Fatalf("review = %+v", rv)
	}
	if !c.ApproveToMerge || !c.Maintainer || c.DefaultWorkflow != "careful" || c.Evaluation != (Evaluation{Mode: "observe", ControlPercent: 20, Treatment: "careful"}) {
		t.Fatalf("catalogue = %+v", c)
	}

	t.Run("review comes from the factory default when the repository omits it", func(t *testing.T) {
		cfg := live()
		delete(cfg.Repositories[0].Classes, "review")
		c, _ := Build(cfg, "Smana/cloud-native-ref", false)
		rv, ok := c.Class("review")
		if !ok || rv.Mode != ModeOff || c.Maintainer {
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

// WF4: every workflow is offered on every class, the repository's default first; none never is.
func TestProfileOffered(t *testing.T) {
	c, _ := Build(live(), "Smana/cloud-native-ref", false)
	if p := c.Profiles["quick"]; !p.Offered || p.NotOffered != "" || !slices.Equal(p.Roles, []string{"implementer"}) || p.Tier != "light" || p.TestFirst {
		t.Fatalf("quick = %+v", p)
	}
	if p := c.Profiles["careful"]; !p.Offered || !p.TestFirst || p.Escalation == nil || p.Escalation.NotConverged != "standard" {
		t.Fatalf("careful = %+v: the repository's testFirst runs in careful (WF7)", p)
	}
	if p := c.Profiles["standard"]; p.Offered || p.NotOffered != "no_template" || p.Roles != nil {
		t.Fatalf("standard = %+v: a workflow with no template is not offered (P15)", p)
	}
	if _, ok := c.Profiles["none"]; ok {
		t.Fatal("none is offered: it runs nothing")
	}
	for _, class := range []string{"review", "docs-links"} {
		if got := c.OfferedProfiles(class); !slices.Equal(got, []string{"careful", "quick"}) {
			t.Fatalf("OfferedProfiles(%s) = %v: the default first, the unoffered one left out", class, got)
		}
	}
	if got := c.OfferedProfiles("nope"); got != nil {
		t.Fatalf("OfferedProfiles(nope) = %v", got)
	}
	cfg := live()
	cfg.Repositories[0].Workflow = config.RepositoryWorkflow{}
	c, _ = Build(cfg, "Smana/cloud-native-ref", false)
	if c.DefaultWorkflow != "standard" || c.Profiles["careful"].TestFirst {
		t.Fatalf("a repository without a workflow block: default %q, careful %+v", c.DefaultWorkflow, c.Profiles["careful"])
	}
	if got := c.Workflows(); !slices.Equal(got, []string{"careful", "quick"}) {
		t.Fatalf("Workflows = %v: an unoffered default is left out, the rest by name", got)
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
	cfg.Repositories[0].Classes = map[string]config.Class{"review": {}}
	c, _ := Build(cfg, "Smana/cloud-native-ref", false)
	rv, _ := c.Class("review")
	if rv.Mode != ModeUnarmed || c.ApproveToMerge {
		t.Fatalf("gate off: review %+v, approveToMerge %t: want unarmed and no approval", rv, c.ApproveToMerge)
	}
}

func TestBuildRepositoryAuthority(t *testing.T) {
	cfg := live()
	cfg.PolicyManagement.Stage = config.StageManage
	cfg.Repositories[0].ClassAuthority = config.ClassAuthorityRepository
	cfg.Repositories[0].Classes = map[string]config.Class{"stale": {}}
	cfg.SetClassSource(func(string) (map[string]config.Class, bool) {
		return map[string]config.Class{"from-file": {Description: "from the repository's file"}}, true
	})
	c, _ := Build(cfg, "Smana/cloud-native-ref", false)
	if got := names(c); !slices.Equal(got, []string{"from-file", "review"}) {
		t.Fatalf("offered = %v: want the repository file's classes", got)
	}
}

func TestBuildGenerations(t *testing.T) {
	cfg := live()
	cfg.Generations = config.Generations{Factory: 12, Repos: map[string]config.RepoGenerations{
		"smana/cloud-native-ref": {Repository: 4}}}
	c, _ := Build(cfg, "Smana/cloud-native-ref", false)
	if !reflect.DeepEqual(c.Generations, Generations{Factory: 12, Repository: 4}) {
		t.Fatalf("generations = %+v", c.Generations)
	}
}

// B19, plan PR1: a bound repository's generations are per class; the single mergePolicy is gone (B8).
func TestBuildGenerationsPerClass(t *testing.T) {
	cfg := live()
	cfg.Generations = config.Generations{Factory: 12, Repos: map[string]config.RepoGenerations{
		"smana/cloud-native-ref": {Repository: 4, Classes: map[string]config.ClassGeneration{"dead-links": {Policy: 4, PolicyName: "dead-links"}}}}}
	c, _ := Build(cfg, "Smana/cloud-native-ref", false)
	if c.Generations.MergePolicies["dead-links"] != 4 || len(c.Generations.MergePolicies) != 1 {
		t.Fatalf("generations = %+v", c.Generations)
	}
	b, err := json.Marshal(c.Generations)
	if err != nil || !strings.Contains(string(b), `"mergePolicies":{"dead-links":4}`) {
		t.Fatalf("%s %v", b, err)
	}
}

func TestCatalogueJSON(t *testing.T) {
	c, _ := Build(live(), "Smana/cloud-native-ref", true)
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"ceilingUSD":8`, `"defaultWorkflow":"careful"`, `"escalation":{"outOfBudget":"","notConverged":"standard"}`,
		`"generations":{"factory":0,"repository":0}`, `"notOffered":"no_template"`, `"approveToMerge":true`,
		`"maintainer":true`, `"controlPercent":20`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON lacks %s:\n%s", want, b)
		}
	}
}

func cfgWith(repos ...config.Repository) *config.Config {
	return &config.Config{Repositories: repos}
}

func publicRepo(name string) config.Repository {
	return config.Repository{Name: name}
}

func privateUnacknowledged(name string) config.Repository {
	return config.Repository{Name: name, PrivateCode: &config.PrivateCode{Acknowledged: false}}
}

func privateAcknowledged(name string) config.Repository {
	return config.Repository{Name: name, PrivateCode: &config.PrivateCode{Acknowledged: true}}
}

func TestRepositoriesServed(t *testing.T) {
	got := Repositories(cfgWith(publicRepo("acme/b"), publicRepo("acme/a"), privateUnacknowledged("acme/secret")))
	if !slices.Equal(got, []string{"acme/a", "acme/b"}) {
		t.Fatalf("got %v", got)
	}
	t.Run("acknowledged private included, runtime refused excluded", func(t *testing.T) {
		cfg := cfgWith(publicRepo("acme/c"), privateAcknowledged("acme/acked"), publicRepo("acme/runtime-private"), publicRepo("acme/a"))
		cfg.SetPrivateRefused(func(repo string) bool { return strings.EqualFold(repo, "acme/runtime-private") })
		if got := Repositories(cfg); !slices.Equal(got, []string{"acme/a", "acme/acked", "acme/c"}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("nil config", func(t *testing.T) {
		if got := Repositories(nil); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
}
