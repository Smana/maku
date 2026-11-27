// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

// The CRs embed these types (factory resources D16), so the API machinery deep-copies them: a
// copy shares no map, slice or pointer with its source.
func TestDeepCopySharesNothing(t *testing.T) {
	src := Class{Live: true, AllowedFiles: []string{"^docs/"},
		Monitor: &Monitor{Flux: &FluxWatch{Source: "flux-system/flux-system"}},
		Author:  &Author{Adopt: &Adopt{Logins: []string{"renovate[bot]"}}}}
	cp := src.DeepCopy()
	cp.AllowedFiles[0] = "x"
	cp.Monitor.Flux.Source = "x"
	cp.Author.Adopt.Logins[0] = "x"
	if src.AllowedFiles[0] != "^docs/" || src.Monitor.Flux.Source != "flux-system/flux-system" ||
		src.Author.Adopt.Logins[0] != "renovate[bot]" {
		t.Fatalf("the copy shares memory with its source: %+v", src)
	}
	l := Loop{Models: map[string]ModelAliases{"reviewer": {"frontier": "kimi"}}}
	lc := l.DeepCopy()
	lc.Models["reviewer"]["frontier"] = "x"
	if l.Models["reviewer"]["frontier"] != "kimi" {
		t.Fatal("Loop.Models is shared")
	}

	rt := RoutineSpec{
		DefaultPrompt:  "prompt",
		Requires:       []string{"probeWorkflow"},
		ProtectedPaths: []string{"^docs/"},
		Egress:         []string{"pypi"},
		Breaker:        BreakerSpec{MinMergeRate: new(0.25)},
	}
	rtc := rt.DeepCopy()
	rtc.Requires[0] = "x"
	rtc.ProtectedPaths[0] = "x"
	rtc.Egress[0] = "x"
	*rtc.Breaker.MinMergeRate = 0.9
	if rt.Requires[0] != "probeWorkflow" || rt.ProtectedPaths[0] != "^docs/" || rt.Egress[0] != "pypi" || *rt.Breaker.MinMergeRate != 0.25 {
		t.Fatal("RoutineSpec DeepCopy shares memory with its source")
	}

	rb := RoutineBinding{
		Name:           "dead-links",
		Mode:           RoutineModePR,
		RepoPrompt:     new(false),
		ProtectedPaths: []string{"^docs/"},
		Egress:         []string{"pypi"},
		Breaker:        &BreakerSpec{MinMergeRate: new(0.5)},
	}
	rbc := rb.DeepCopy()
	*rbc.RepoPrompt = true
	rbc.ProtectedPaths[0] = "x"
	rbc.Egress[0] = "x"
	*rbc.Breaker.MinMergeRate = 0.1
	if *rb.RepoPrompt || rb.ProtectedPaths[0] != "^docs/" || rb.Egress[0] != "pypi" || *rb.Breaker.MinMergeRate != 0.5 {
		t.Fatal("RoutineBinding DeepCopy shares memory with its source")
	}

	bg := Budgets{UpkeepDailyUSD: new(5.0)}
	bgc := bg.DeepCopy()
	*bgc.UpkeepDailyUSD = 1.0
	if *bg.UpkeepDailyUSD != 5.0 {
		t.Fatal("Budgets DeepCopy shares UpkeepDailyUSD")
	}
}
