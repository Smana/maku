// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func baseRoutine() RoutineSpec {
	return RoutineSpec{
		Description:   "Fix dead links",
		Workflow:      "quick",
		DataClass:     "public",
		DefaultPrompt: "Fix dead external links found by the detector.",
		Probe:         ProbeSpec{Kind: ProbeDeadLinks},
		Requires:      []string{"oracleClass"},
		MaxOpenPRs:    2,
		MaxCostUSD:    3.00,
		Cooldown:      Duration{Duration: 24 * time.Hour},
		Breaker: BreakerSpec{
			Window:                 10,
			MinSamples:             5,
			MinMergeRate:           new(0.25),
			MaxConsecutiveClosed:   3,
			MaxConsecutiveFailures: 3,
		},
		ProtectedPaths: []string{`^docs/specs/`},
		Egress:         []string{"pypi", "npm"},
	}
}

func baseRoutineRepo() *Repository {
	return &Repository{
		Name:   "Smana/maku",
		Checks: Checks{Egress: []string{"pypi", "npm"}},
		Classes: map[string]Class{
			"dead-links": {Oracle: &Oracle{Probe: ProbeDeadLinks, MaxFindings: 10}},
			"docs":       {},
		},
	}
}

func baseRoutineBinding() RoutineBinding {
	return RoutineBinding{
		Name:        "dead-links",
		Mode:        RoutineModePR,
		Cron:        "0 5 * * *",
		OracleClass: "dead-links",
	}
}

func TestResolveRoutine(t *testing.T) {
	t.Run("valid dead-links routine and defaults", func(t *testing.T) {
		rt := baseRoutine()
		b := baseRoutineBinding()
		b.ProtectedPaths = []string{`^docs/adr/`, `^docs/specs/`}
		b.Egress = []string{"pypi"}
		res, reason, err := ResolveRoutine("dead-links", rt, b, baseRoutineRepo(), nil)
		if err != nil || reason != "" {
			t.Fatalf("unexpected error (%s): %v", reason, err)
		}
		if res.Mode != RoutineModePR || res.OracleClass != "dead-links" || res.MaxOpenPRs != 2 || res.MaxCostUSD != 3.00 {
			t.Fatalf("unexpected resolved routine: %+v", res)
		}
		if res.Probe.MaxFindings != 1 || res.Probe.RequireConsecutive != 1 {
			t.Fatalf("expected Q16 defaults (1, 1), got %+v", res.Probe)
		}
		if !slices.Equal(res.ProtectedPaths, []string{`^docs/specs/`, `^docs/adr/`}) {
			t.Fatalf("protectedPaths = %v", res.ProtectedPaths)
		}
		if !slices.Equal(res.Egress, []string{"pypi"}) {
			t.Fatalf("egress = %v", res.Egress)
		}
	})

	t.Run("actions-findings defaults and requires probeWorkflow", func(t *testing.T) {
		rt := RoutineSpec{
			DefaultPrompt: "Remove dead code.",
			Probe:         ProbeSpec{Kind: ProbeActionsFindings},
			Requires:      []string{"probeWorkflow"},
		}
		b := RoutineBinding{Name: "dead-code", Mode: RoutineModePR, Cron: "0 3 * * 1", ProbeWorkflow: "deadcode.yaml"}
		res, reason, err := ResolveRoutine("dead-code", rt, b, &Repository{Name: "a/b"}, nil)
		if err != nil || reason != "" {
			t.Fatalf("unexpected error (%s): %v", reason, err)
		}
		if res.Probe.MaxFindings != 1 || res.Probe.RequireConsecutive != 1 {
			t.Fatalf("probe = %+v, want maxFindings=1 requireConsecutive=1", res.Probe)
		}
		if res.ProbeArtifact != DefaultProbeArtifact || res.ProbeFile != DefaultProbeFile {
			t.Fatalf("artifact=%q file=%q", res.ProbeArtifact, res.ProbeFile)
		}
		// A1: default positive maxCostUSD when both routine and binding omit it.
		if res.MaxCostUSD != DefaultRoutineMaxCostUSD {
			t.Fatalf("MaxCostUSD = %v, want %v", res.MaxCostUSD, DefaultRoutineMaxCostUSD)
		}
	})

	t.Run("renovate-red defaults probeAuthor", func(t *testing.T) {
		rt := RoutineSpec{
			DefaultPrompt: "Fix red renovate PRs.",
			Probe:         ProbeSpec{Kind: ProbeRenovateRed},
		}
		b := RoutineBinding{Name: "renovate-red", Mode: RoutineModePR, Cron: "0 */4 * * *"}
		res, reason, err := ResolveRoutine("renovate-red", rt, b, &Repository{Name: "a/b"}, nil)
		if err != nil || reason != "" {
			t.Fatalf("unexpected error (%s): %v", reason, err)
		}
		if res.ProbeAuthor != "renovate[bot]" {
			t.Fatalf("ProbeAuthor = %q", res.ProbeAuthor)
		}
	})

	t.Run("repoPrompt defaults true, respects false, and binds skill", func(t *testing.T) {
		rt := RoutineSpec{DefaultPrompt: "Check docs."}
		bDefault := RoutineBinding{Name: "doc-drift", Mode: RoutineModePR, Cron: "0 4 * * 1"}
		res, reason, err := ResolveRoutine("doc-drift", rt, bDefault, &Repository{Name: "a/b"}, nil)
		if err != nil || reason != "" {
			t.Fatalf("unexpected error (%s): %v", reason, err)
		}
		if !res.RepoPrompt || res.Skill != "" {
			t.Fatalf("RepoPrompt=%v Skill=%q, want true and empty", res.RepoPrompt, res.Skill)
		}

		bFalse := RoutineBinding{Name: "doc-drift", Mode: RoutineModePR, Cron: "0 4 * * 1", RepoPrompt: new(false), Skill: "docs-audit"}
		res, reason, err = ResolveRoutine("doc-drift", rt, bFalse, &Repository{Name: "a/b"}, nil)
		if err != nil || reason != "" {
			t.Fatalf("unexpected error (%s): %v", reason, err)
		}
		if res.RepoPrompt || res.Skill != "docs-audit" {
			t.Fatalf("RepoPrompt=%v Skill=%q, want false and docs-audit", res.RepoPrompt, res.Skill)
		}

		bTrue := RoutineBinding{Name: "doc-drift", Mode: RoutineModePR, Cron: "0 4 * * 1", RepoPrompt: new(true), Skill: "docs-audit"}
		res, reason, err = ResolveRoutine("doc-drift", rt, bTrue, &Repository{Name: "a/b"}, nil)
		if err != nil || reason != "" {
			t.Fatalf("unexpected error (%s): %v", reason, err)
		}
		if !res.RepoPrompt || res.Skill != "docs-audit" {
			t.Fatalf("RepoPrompt=%v Skill=%q, want true and docs-audit", res.RepoPrompt, res.Skill)
		}
	})

	cases := map[string]struct {
		name    string
		rt      func(*RoutineSpec)
		bind    func(*RoutineBinding)
		repo    func(*Repository)
		wantRsn string
		wantMsg string
	}{
		"widen maxOpenPRs": {
			bind:    func(b *RoutineBinding) { b.MaxOpenPRs = 3 },
			wantRsn: ReasonOverRoutine,
		},
		"widen maxCostUSD": {
			bind:    func(b *RoutineBinding) { b.MaxCostUSD = 4.00 },
			wantRsn: ReasonOverRoutine,
		},
		"binding maxCostUSD sub-micro-USD": {
			rt:      func(rt *RoutineSpec) { rt.MaxCostUSD = 0 },
			bind:    func(b *RoutineBinding) { b.MaxCostUSD = 1e-9 },
			wantRsn: ReasonInvalid,
		},
		"binding maxCostUSD over 1000": {
			rt:      func(rt *RoutineSpec) { rt.MaxCostUSD = 0 },
			bind:    func(b *RoutineBinding) { b.MaxCostUSD = 1001 },
			wantRsn: ReasonInvalid,
		},
		"routine maxCostUSD sub-micro-USD": {
			rt:      func(rt *RoutineSpec) { rt.MaxCostUSD = 1e-9 },
			wantRsn: ReasonInvalid,
		},
		"routine maxCostUSD over 1000": {
			rt:      func(rt *RoutineSpec) { rt.MaxCostUSD = 1001 },
			wantRsn: ReasonInvalid,
		},
		"widen probe maxFindings": {
			rt:      func(rt *RoutineSpec) { rt.Probe.MaxFindings = 2 },
			bind:    func(b *RoutineBinding) { b.MaxFindings = 5 },
			wantRsn: ReasonOverRoutine,
		},
		"widen dataClass internal to public": {
			rt:      func(rt *RoutineSpec) { rt.DataClass = "internal" },
			bind:    func(b *RoutineBinding) { b.DataClass = "public" },
			wantRsn: ReasonOverRoutine,
		},
		"tighten dataClass public to internal allowed": {
			rt:   func(rt *RoutineSpec) { rt.DataClass = "public" },
			bind: func(b *RoutineBinding) { b.DataClass = "internal" },
		},
		"loosen breaker minMergeRate": {
			bind:    func(b *RoutineBinding) { b.Breaker = &BreakerSpec{MinMergeRate: new(0.10)} },
			wantRsn: ReasonOverRoutine,
		},
		"loosen breaker minMergeRate to explicit zero": {
			bind:    func(b *RoutineBinding) { b.Breaker = &BreakerSpec{MinMergeRate: new(0.0)} },
			wantRsn: ReasonOverRoutine,
		},
		"raise breaker maxConsecutiveClosed": {
			bind:    func(b *RoutineBinding) { b.Breaker = &BreakerSpec{MaxConsecutiveClosed: 4} },
			wantRsn: ReasonOverRoutine,
		},
		"raise breaker maxConsecutiveFailures": {
			bind:    func(b *RoutineBinding) { b.Breaker = &BreakerSpec{MaxConsecutiveFailures: 4} },
			wantRsn: ReasonOverRoutine,
		},
		"egress outside routine egress": {
			bind:    func(b *RoutineBinding) { b.Egress = []string{"golang"} },
			repo:    func(r *Repository) { r.Checks.Egress = []string{"pypi", "npm", "golang"} },
			wantRsn: ReasonOverRoutine,
		},
		"egress outside repo checks.egress": {
			bind:    func(b *RoutineBinding) { b.Egress = []string{"pypi"} },
			repo:    func(r *Repository) { r.Checks.Egress = []string{"npm"} },
			wantRsn: ReasonInvalid,
		},
		"missing required parameter": {
			bind:    func(b *RoutineBinding) { b.OracleClass = "" },
			wantRsn: ReasonMissingParameter,
		},
		"parameter set on both routine and binding": {
			rt:      func(rt *RoutineSpec) { rt.Requires = nil; rt.OracleClass = "dead-links" },
			bind:    func(b *RoutineBinding) { b.OracleClass = "dead-links" },
			wantRsn: ReasonParameterTwice,
		},
		"actions-findings missing probeWorkflow": {
			rt: func(rt *RoutineSpec) {
				rt.Probe = ProbeSpec{Kind: ProbeActionsFindings}
				rt.Requires = nil
			},
			bind:    func(b *RoutineBinding) { b.OracleClass = "" },
			wantRsn: ReasonMissingParameter,
		},
		"dead-links unknown oracleClass": {
			bind:    func(b *RoutineBinding) { b.OracleClass = "missing-class" },
			wantRsn: ReasonInvalid,
		},
		"dead-links non-oracle oracleClass": {
			bind:    func(b *RoutineBinding) { b.OracleClass = "docs" },
			wantRsn: ReasonInvalid,
		},
		"mode propose allowed": {
			bind: func(b *RoutineBinding) { b.Mode = RoutineModePropose },
		},
		"mode draft allowed": {
			bind: func(b *RoutineBinding) { b.Mode = RoutineModeDraft },
		},
		"invalid skill name traversal": {
			bind:    func(b *RoutineBinding) { b.Skill = "../evil" },
			wantRsn: ReasonInvalid,
		},
		"invalid skill name uppercase": {
			bind:    func(b *RoutineBinding) { b.Skill = "Bad_Skill" },
			wantRsn: ReasonInvalid,
		},
		"invalid skill name slash": {
			bind:    func(b *RoutineBinding) { b.Skill = "a/b" },
			wantRsn: ReasonInvalid,
		},
		"missing cron when enabled": {
			bind:    func(b *RoutineBinding) { b.Cron = "" },
			wantRsn: ReasonMissingParameter,
		},
		"disabled without cron allowed": {
			bind: func(b *RoutineBinding) { b.Mode = RoutineModeDisabled; b.Cron = "" },
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rt := baseRoutine()
			b := baseRoutineBinding()
			repo := baseRoutineRepo()
			if tc.rt != nil {
				tc.rt(&rt)
			}
			if tc.bind != nil {
				tc.bind(&b)
			}
			if tc.repo != nil {
				tc.repo(repo)
			}
			rname := "dead-links"
			if tc.name != "" {
				rname = tc.name
			}
			_, reason, err := ResolveRoutine(rname, rt, b, repo, nil)
			if tc.wantRsn == "" {
				if err != nil || reason != "" {
					t.Fatalf("unexpected refusal (%s): %v", reason, err)
				}
				return
			}
			if err == nil || reason != tc.wantRsn {
				t.Fatalf("got (%s, %v), want reason %s", reason, err, tc.wantRsn)
			}
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error %q lacks %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestValidateRoutineSpec(t *testing.T) {
	if probs := ValidateRoutineSpec("dead-links", baseRoutine(), nil); len(probs) > 0 {
		t.Fatalf("valid routine refused: %v", probs)
	}
	bad := baseRoutine()
	bad.DefaultPrompt = ""
	if probs := ValidateRoutineSpec("dead-links", bad, nil); len(probs) == 0 {
		t.Fatal("empty defaultPrompt should fail")
	}
	badBreaker := baseRoutine()
	badBreaker.Breaker = BreakerSpec{Window: 3, MinSamples: 5}
	if probs := ValidateRoutineSpec("dead-links", badBreaker, nil); len(probs) == 0 {
		t.Fatal("minSamples > window should fail")
	}
	badRequires := baseRoutine()
	badRequires.ProbeWorkflow = "x.yaml"
	badRequires.Requires = []string{"probeWorkflow"}
	if probs := ValidateRoutineSpec("dead-links", badRequires, nil); len(probs) == 0 {
		t.Fatal("requires key also set on Routine should fail")
	}
	for _, badCost := range []float64{1e-9, 1001} {
		bad := baseRoutine()
		bad.MaxCostUSD = badCost
		if probs := ValidateRoutineSpec("dead-links", bad, nil); len(probs) == 0 {
			t.Fatalf("maxCostUSD %v should fail", badCost)
		}
	}
}

func TestRoutineBudgetsAndCaps(t *testing.T) {
	t.Run("upkeepDailyUSD default and DailyMicroUSD isolation", func(t *testing.T) {
		c, err := Parse([]byte(good))
		if err != nil {
			t.Fatal(err)
		}
		// factoryDailyUSD = 42.50 -> min(10.00, 0.25*42.50) = 10.00
		if got := c.Budgets.UpkeepDaily(); got != 10.00 {
			t.Fatalf("UpkeepDaily() = %v, want 10.00", got)
		}
		if got := c.Budgets.DailyMicroUSD(PrincipalUpkeep); got != 10_000_000 {
			t.Fatalf("DailyMicroUSD(PrincipalUpkeep) = %d, want 10_000_000", got)
		}
		// Even with PerPerson.Enabled, system:upkeep uses UpkeepDaily(), never PerPerson.DailyUSD.
		c.Budgets.PerPerson = PerPerson{Enabled: true, DailyUSD: 3.40}
		if got := c.Budgets.DailyMicroUSD(PrincipalUpkeep); got != 10_000_000 {
			t.Fatalf("DailyMicroUSD(PrincipalUpkeep) with PerPerson = %d, want 10_000_000", got)
		}
		// Even if sponsoredDays somehow carries PrincipalUpkeep, PrincipalUpkeep takes precedence.
		c.Budgets.addSponsoredDay(Sponsored{Name: "upkeep", DailyUSD: 99.00})
		if got := c.Budgets.DailyMicroUSD(PrincipalUpkeep); got != 10_000_000 {
			t.Fatalf("DailyMicroUSD(PrincipalUpkeep) with sponsoredDays = %d, want 10_000_000", got)
		}
		// awaitingHumanWIP = 5 -> max(1, 5/2) = 2
		if got := c.Caps.UpkeepAwaitingHumanWIP; got != 2 {
			t.Fatalf("UpkeepAwaitingHumanWIP = %d, want 2", got)
		}
		if got := c.Caps.UpkeepWIP(); got != 2 {
			t.Fatalf("UpkeepWIP() = %d, want 2", got)
		}
	})

	t.Run("upkeepDailyUSD quarter of smaller factoryDailyUSD", func(t *testing.T) {
		raw := strings.Replace(good, "factoryDailyUSD: 42.50", "factoryDailyUSD: 20.00", 1)
		c, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Budgets.UpkeepDaily(); got != 5.00 {
			t.Fatalf("UpkeepDaily() = %v, want 5.00", got)
		}
	})

	t.Run("upkeepDailyUSD explicit 0 disables cron upkeep", func(t *testing.T) {
		raw := strings.Replace(good, "factoryDailyUSD: 42.50", "factoryDailyUSD: 42.50, upkeepDailyUSD: 0", 1)
		c, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if c.Budgets.UpkeepDailyUSD == nil || *c.Budgets.UpkeepDailyUSD != 0 {
			t.Fatalf("UpkeepDailyUSD = %v, want *0", c.Budgets.UpkeepDailyUSD)
		}
		if got := c.Budgets.DailyMicroUSD(PrincipalUpkeep); got != 0 {
			t.Fatalf("DailyMicroUSD(PrincipalUpkeep) = %d, want 0", got)
		}
	})

	t.Run("upkeepDailyUSD out of range refused", func(t *testing.T) {
		for _, val := range []string{"-1", "50.00"} {
			raw := strings.Replace(good, "factoryDailyUSD: 42.50", "factoryDailyUSD: 42.50, upkeepDailyUSD: "+val, 1)
			if _, err := Parse([]byte(raw)); err == nil {
				t.Fatalf("upkeepDailyUSD: %s should be refused", val)
			}
		}
	})

	t.Run("upkeepAwaitingHumanWIP out of range refused", func(t *testing.T) {
		for _, val := range []string{"-1", "6"} {
			raw := strings.Replace(good, "awaitingHumanWIP: 5", "awaitingHumanWIP: 5, upkeepAwaitingHumanWIP: "+val, 1)
			if _, err := Parse([]byte(raw)); err == nil {
				t.Fatalf("upkeepAwaitingHumanWIP: %s should be refused", val)
			}
		}
	})
}

func TestRepositoryRoutinesValidation(t *testing.T) {
	t.Run("duplicate across schedules and routines fails DefinedTwice", func(t *testing.T) {
		raw := strings.Replace(good, "    triggerLabel: factory/ready", `    triggerLabel: factory/ready
    schedules:
      - {name: dead-links, cron: "0 5 * * *", text: "fix"}
    routines:
      - {name: dead-links, mode: pr, cron: "0 5 * * *"}`, 1)
		_, err := Parse([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), ReasonDefinedTwice) {
			t.Fatalf("got %v, want %s", err, ReasonDefinedTwice)
		}
	})

	t.Run("duplicate within routines fails DefinedTwice", func(t *testing.T) {
		raw := strings.Replace(good, "    triggerLabel: factory/ready", `    triggerLabel: factory/ready
    routines:
      - {name: dead-links, mode: pr, cron: "0 5 * * *"}
      - {name: dead-links, mode: disabled}`, 1)
		_, err := Parse([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), ReasonDefinedTwice) {
			t.Fatalf("got %v, want %s", err, ReasonDefinedTwice)
		}
	})
}

func TestRoutinesConfig(t *testing.T) {
	t.Run("defaults uiWrite to false", func(t *testing.T) {
		c, err := Parse([]byte(good))
		if err != nil {
			t.Fatal(err)
		}
		if c.Routines.UIWrite {
			t.Fatal("Routines.UIWrite must default to false")
		}
	})

	t.Run("accepts routines.uiWrite true", func(t *testing.T) {
		raw := good + "\nroutines:\n  uiWrite: true\n"
		c, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if !c.Routines.UIWrite {
			t.Fatal("expected Routines.UIWrite=true")
		}
	})

	t.Run("rejects unknown field inside routines", func(t *testing.T) {
		raw := good + "\nroutines:\n  uiWrtie: true\n"
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatal("expected unknown field error")
		}
	})

	t.Run("CheckBlocks rejects duplicate routines block and duplicate keys inside routines", func(t *testing.T) {
		dupBlock := good + "\nroutines:\n  uiWrite: true\nroutines:\n  uiWrite: false\n"
		if err := CheckBlocks([]byte(dupBlock)); err == nil {
			t.Fatal("expected duplicate routines block error")
		}
		dupKey := good + "\nroutines:\n  uiWrite: true\n  uiWrite: false\n"
		if err := CheckBlocks([]byte(dupKey)); err == nil {
			t.Fatal("expected duplicate key inside routines block error")
		}
	})

	t.Run("ValidateDisabledRoutineBinding enforces workflow and repository egress", func(t *testing.T) {
		c, err := Parse([]byte(good))
		if err != nil {
			t.Fatal(err)
		}
		checks := Checks{Egress: []string{"pypi"}}
		if msg := ValidateDisabledRoutineBinding(RoutineBinding{Name: "dead-links", Mode: RoutineModeDisabled, Workflow: "quick", Egress: []string{"pypi"}}, checks, c); msg != "" {
			t.Fatalf("unexpected error: %s", msg)
		}
		if msg := ValidateDisabledRoutineBinding(RoutineBinding{Name: "dead-links", Mode: RoutineModeDisabled, Egress: []string{"golang"}}, checks, c); msg == "" {
			t.Fatal("expected egress refusal")
		}
		if msg := ValidateDisabledRoutineBinding(RoutineBinding{Name: "dead-links", Mode: RoutineModeDisabled, Workflow: "none"}, checks, c); msg == "" {
			t.Fatal("expected workflow=none refusal")
		}
	})
}
