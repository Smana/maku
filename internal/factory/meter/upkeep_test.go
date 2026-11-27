// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"context"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/runs"
)

func TestUpkeepDayCapIsEnforcedWithoutEnforcePrincipalOrPerPerson(t *testing.T) {
	day := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		budgets config.Budgets
	}{
		"enforcePrincipal off and perPerson off": {
			budgets: config.Budgets{
				FactoryDailyUSD:  42.50,
				HumanDailyUSD:    5.00,
				UpkeepDailyUSD:   new(5.00),
				EnforcePrincipal: false,
				PerPerson:        config.PerPerson{Enabled: false, DailyUSD: 1.00},
			},
		},
		"perPerson lower than upkeepDailyUSD is ignored for system:upkeep": {
			budgets: config.Budgets{
				FactoryDailyUSD:  42.50,
				HumanDailyUSD:    5.00,
				UpkeepDailyUSD:   new(5.00),
				EnforcePrincipal: false,
				PerPerson:        config.PerPerson{Enabled: true, DailyUSD: 1.00},
			},
		},
		"enforcePrincipal on with fleet below cap": {
			budgets: config.Budgets{
				FactoryDailyUSD:  42.50,
				HumanDailyUSD:    5.00,
				UpkeepDailyUSD:   new(5.00),
				EnforcePrincipal: true,
				PerPerson:        config.PerPerson{Enabled: true, DailyUSD: 1.00},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{
				{ID: "aaaaaaaa", Phase: "Running", MaxTokens: 5_000_000, Principal: runs.PrincipalFactory, ChargedTo: config.PrincipalUpkeep, Created: day},
			}}
			// Seed $4.00 already spent, plus this tick's tokens (~$1.70 for 1_000_000 ref tokens) -> >= $5.00.
			c := fake.NewClientBuilder().WithScheme(cmScheme()).WithObjects(
				ledgerDay(t, "20260927", map[string]int64{config.PrincipalUpkeep: 4_000_000}, nil)).Build()
			var gotPrincipal, gotReason, gotLimit string
			m := &Meter{
				Runs:      st,
				Source:    source{"aaaaaaaa": 1_000_000},
				Cfg:       priced(nil, nil, tc.budgets),
				Ledger:    c,
				Namespace: "agent-system",
				Now:       func() time.Time { return day },
				OnRevoke: func(ctx context.Context, reason, limit string) {
					gotPrincipal, gotReason, gotLimit = fmetrics.PrincipalOf(ctx), reason, limit
				},
			}
			if err := m.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := st.patches["aaaaaaaa"][runs.AnnRevoked]; got != "budget-upkeep" {
				t.Fatalf("AnnRevoked = %q, want budget-upkeep", got)
			}
			if gotPrincipal != config.PrincipalUpkeep || gotReason != "budget-upkeep" || gotLimit != "upkeep-day" {
				t.Fatalf("OnRevoke = (%q, %q, %q), want (%q, budget-upkeep, upkeep-day)",
					gotPrincipal, gotReason, gotLimit, config.PrincipalUpkeep)
			}
		})
	}
}

func TestUpkeepExhaustionDoesNotRevokeHumanOrFactoryRuns(t *testing.T) {
	day := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	b := config.Budgets{
		FactoryDailyUSD:  42.50,
		HumanDailyUSD:    5.00,
		UpkeepDailyUSD:   new(5.00),
		EnforcePrincipal: true,
		PerPerson:        config.PerPerson{Enabled: true, DailyUSD: 5.00},
	}
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{
		{ID: "aaaaaaaa", Phase: "Running", MaxTokens: 5_000_000, Principal: runs.PrincipalFactory, ChargedTo: config.PrincipalUpkeep, Created: day},
		{ID: "bbbbbbbb", Phase: "Running", MaxTokens: 5_000_000, Principal: runs.PrincipalFactory, ChargedTo: "github:alice", Created: day},
		{ID: "cccccccc", Phase: "Running", MaxTokens: 5_000_000, Principal: runs.PrincipalFactory, Created: day},
	}}
	c := fake.NewClientBuilder().WithScheme(cmScheme()).WithObjects(
		ledgerDay(t, "20260927", map[string]int64{config.PrincipalUpkeep: 5_000_000}, nil)).Build()
	m := &Meter{
		Runs:      st,
		Source:    source{"aaaaaaaa": 1_000, "bbbbbbbb": 1_000, "cccccccc": 1_000},
		Cfg:       priced(nil, nil, b),
		Ledger:    c,
		Namespace: "agent-system",
		Now:       func() time.Time { return day },
	}
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"aaaaaaaa": "budget-upkeep", "bbbbbbbb": "", "cccccccc": ""} {
		if got := st.patches[id][runs.AnnRevoked]; got != want {
			t.Errorf("%s: AnnRevoked = %q, want %q", id, got, want)
		}
	}
}

func TestUpkeepRunDoesNotFailClosedOnMissingSponsoredCap(t *testing.T) {
	day := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	r := standardRun("aaaaaaaa")
	r.Principal = runs.PrincipalFactory
	r.ChargedTo = config.PrincipalUpkeep
	r.MaxMicroUSD = 0
	r.Created = day
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{r}}
	var revoked []revocation
	var fell []string
	m := usdMeter(st, typed{"aaaaaaaa": typedUsage(Usage{Input: 100_000, Output: 10_000})}, &revoked, &fell)
	with(m, func(c *config.Config) {
		c.Budgets = config.Budgets{
			FactoryDailyUSD: 42.50,
			UpkeepDailyUSD:  new(10.00),
		}
	})
	m.Now = func() time.Time { return day }
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 0 || st.patches["aaaaaaaa"][runs.AnnRevoked] != "" {
		t.Fatalf("unexpected revocation on upkeep run with MaxMicroUSD=0: revoked=%v patch=%v", revoked, st.patches["aaaaaaaa"])
	}
}

func TestUpkeepDailyGaugeReportedViaTellDay(t *testing.T) {
	day := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	c := fake.NewClientBuilder().WithScheme(cmScheme()).Build()
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{
		{ID: "aaaaaaaa", Phase: "Running", MaxTokens: 5_000_000, Principal: runs.PrincipalFactory, ChargedTo: config.PrincipalUpkeep, Created: day},
	}}
	type told struct{ spent, limit int64 }
	gotDay := map[string]told{}
	m := &Meter{
		Runs:      st,
		Source:    source{"aaaaaaaa": 1_000},
		Ledger:    c,
		Namespace: "agent-system",
		Cfg: priced(nil, nil, config.Budgets{
			FactoryDailyUSD: 42.50,
			UpkeepDailyUSD:  new(10.00),
		}),
		Now: func() time.Time { return day },
		Day: func(p string, spent, limit int64) { gotDay[p] = told{spent, limit} },
	}
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := gotDay[config.PrincipalUpkeep]; got != (told{spent: 1700, limit: 10_000_000}) {
		t.Fatalf("Day[%q] = %+v, want {spent:1700 limit:10000000}", config.PrincipalUpkeep, got)
	}
}

func TestUpkeepDailyGaugeEmittedWithZeroCapAndZeroSettledSpend(t *testing.T) {
	day := time.Date(2026, 9, 27, 23, 59, 30, 0, time.UTC)
	c := fake.NewClientBuilder().WithScheme(cmScheme()).Build()
	st := &store{patches: map[string]map[string]string{}}
	type told struct{ spent, limit int64 }
	var calls []told
	gotDay := map[string]told{}
	m := &Meter{
		Runs:      st,
		Source:    source{},
		Ledger:    c,
		Namespace: "agent-system",
		Cfg: priced(nil, nil, config.Budgets{
			FactoryDailyUSD: 42.50,
			UpkeepDailyUSD:  new(0.0),
		}),
		Now: func() time.Time { return day },
		Day: func(p string, spent, limit int64) {
			gotDay[p] = told{spent, limit}
			if p == config.PrincipalUpkeep {
				calls = append(calls, told{spent, limit})
			}
		},
	}
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, ok := gotDay[config.PrincipalUpkeep]
	if !ok || got != (told{spent: 0, limit: 0}) {
		t.Fatalf("Day[%q] = (%+v, ok=%v), want ({spent:0 limit:0}, true)", config.PrincipalUpkeep, got, ok)
	}
	// Rollover past 00:00 UTC still emits system:upkeep exactly once with (0, 0).
	day = day.Add(time.Minute)
	gotDay = map[string]told{}
	calls = nil
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, ok = gotDay[config.PrincipalUpkeep]
	if !ok || got != (told{spent: 0, limit: 0}) || len(calls) != 1 {
		t.Fatalf("after midnight: Day[%q] = (%+v, ok=%v), calls=%v", config.PrincipalUpkeep, got, ok, calls)
	}
}
