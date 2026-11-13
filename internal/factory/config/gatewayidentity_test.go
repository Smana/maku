// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
)

// GW10: runs.gatewayIdentity states that portable runs call the model gateway with their own token,
// so the meter prices them. It lifts step 2 PR B's refusals of loops.<class>.models and
// loops.<class>.split on maku.ogenki.io; without it both stay, renamed. It is refused on the XR,
// which always has the identity.
func TestGatewayIdentity(t *testing.T) {
	const (
		models   = "loops: {review: {models: {reviewer: {frontier: kimi-frontier}}}}\n"
		split    = "loops: {docs: {scope: scoper, split: {maxChildren: 3, familyUSD: 12}}}\n"
		portable = "runs: {group: maku.ogenki.io, gatewayIdentity: true}\n"
		noKey    = "runs: {group: maku.ogenki.io}\n"
	)
	demoMeter := strings.Replace(demoGood, "runs: {group: maku.ogenki.io}\nloops: {docs: {scope: scoper}}\n", "", 1)
	for name, c := range map[string]struct{ raw, err string }{
		"portable with the key: models":             {byModel + models + portable, ""},
		"portable with the key: split":              {priced + split + portable, ""},
		"portable with the key: split, demo":        {demoMeter + split + portable, "loops.docs.split needs a meter: a family's cap is in USD"},
		"portable without the key: models":          {byModel + models + noKey, "loops.review.models needs runs.gatewayIdentity (a model gateway, rollout step 4)"},
		"portable without the key: split":           {priced + split + noKey, "loops.docs.split needs runs.gatewayIdentity (a model gateway, rollout step 4)"},
		"the XR with the key":                       {good + "runs: {group: cloud.ogenki.io, gatewayIdentity: true}\n", "runs.gatewayIdentity is only for runs.group maku.ogenki.io: cloud-native-ref's AgentRun always carries a gateway identity"},
		"the XR by default, with the key":           {good + "runs: {gatewayIdentity: true}\n", "runs.gatewayIdentity is only for runs.group maku.ogenki.io"},
		"the XR without the key: models, as before": {byModel + models + "runs: {group: cloud.ogenki.io}\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(c.raw))
			if c.err == "" {
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(c.raw, "gatewayIdentity: true") != cfg.Runs.GatewayIdentity {
					t.Fatalf("gatewayIdentity %v", cfg.Runs.GatewayIdentity)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("got %v, want %q", err, c.err)
			}
		})
	}
}

// GW10: a configured meter prices no run of a portable runtime without the key; the factory says so
// at startup, and only then.
func TestGatewayIdentityWarning(t *testing.T) {
	const warning = "the meter prices no run of this runtime: its runs carry no gateway identity (runs.gatewayIdentity)"
	for name, c := range map[string]struct {
		raw  string
		warn bool
	}{
		"portable, no key, a meter":  {good + "runs: {group: maku.ogenki.io}\n", true},
		"portable, the key, a meter": {good + "runs: {group: maku.ogenki.io, gatewayIdentity: true}\n", false},
		"portable, no key, no meter": {demoGood, false},
		"the XR, a meter":            {good, false},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(c.raw))
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(cfg.Warnings, warning); got != c.warn {
				t.Fatalf("warned %v, want %v: %q", got, c.warn, cfg.Warnings)
			}
		})
	}
}
