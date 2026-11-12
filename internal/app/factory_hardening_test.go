// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"slices"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/redact"
)

// H1: under audit, centrally or for one repository, the reconciler gets the shadow pass's ports, a
// Checker with every layer at audit, T5 only when agent-guard is configured, the redactor as T3,
// and repository policies the fence feeds; under off it gets none.
func TestFactoryHardeningWiring(t *testing.T) {
	red, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		h          config.Hardening
		wired      bool
		classifier hardening.Mode
	}{
		{"off", config.Hardening{}, false, ""},
		{"central audit, no agent-guard", config.Hardening{Mode: hardening.ModeAudit, LabelAllowlist: []string{"documentation"}}, true, hardening.ModeOff},
		{"central audit with agent-guard", config.Hardening{Mode: hardening.ModeAudit,
			AgentGuard: config.AgentGuard{URL: "http://agent-guard.agent-system.svc:8080"}}, true, hardening.ModeAudit},
		{"one repository in audit", config.Hardening{Repositories: map[string]config.HardeningRepository{
			"Smana/demo": {Mode: hardening.ModeAudit}}}, true, hardening.ModeOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Hardening: tc.h}
			cfg.Hardening.ApplyDefaults()
			p := hardeningPorts(cfg, red)
			if (p != nil) != tc.wired {
				t.Fatalf("ports %+v, want wired %v", p, tc.wired)
			}
			if p == nil {
				return
			}
			c := p.Checker
			want := hardening.Layers{Schema: hardening.ModeAudit, Sanitise: hardening.ModeAudit, Secrets: hardening.ModeAudit,
				Paths: hardening.ModeAudit, Classifier: tc.classifier}
			if c.Layers != want {
				t.Fatalf("layers %+v, want %+v", c.Layers, want)
			}
			if c.Secrets == nil || p.Policies == nil {
				t.Fatalf("secrets %v, policies %v", c.Secrets, p.Policies)
			}
			wc, ok := c.Classifier.(hardening.WebhookClassifier)
			if (tc.classifier == hardening.ModeAudit) != ok || (ok && (wc.URL != tc.h.AgentGuard.URL || wc.HC == nil)) {
				t.Fatalf("classifier %#v", c.Classifier)
			}
			if !slices.Equal(c.LabelAllowlist, tc.h.LabelAllowlist) {
				t.Fatalf("allowlist %v", c.LabelAllowlist)
			}
		})
	}
}

// The fence feeds the repository policies beside whatever else hears its file: the merge policy's
// watch keeps hearing it.
func TestFenceFeedsTheRepositoryPolicies(t *testing.T) {
	cfg := &config.Config{Hardening: config.Hardening{Mode: hardening.ModeAudit}}
	cfg.Hardening.ApplyDefaults()
	p := hardeningPorts(cfg, nil)
	var heard []string
	onFile := chainOnFile(func(_ context.Context, repo string, _ []byte) { heard = append(heard, repo) }, nil, p.Policies.Observe)
	onFile(t.Context(), "Smana/demo", []byte("factory: gcp-0\nhardening:\n  minAuthorTrust: merged\n"))
	if !slices.Equal(heard, []string{"Smana/demo"}) {
		t.Fatalf("the first observer heard %v", heard)
	}
	got, err := p.Policies.For("Smana/demo", hardening.Approved)
	if err != nil || got.MinTrust != hardening.Merged {
		t.Fatalf("policy %+v %v", got, err)
	}
	if chainOnFile(nil, nil) != nil {
		t.Fatal("no observer is no hook")
	}
}

// The demo profile is never hardened (R7): whatever its hardening block says, it gets no ports and
// so no fence hook, canary, record or shadow.
func TestFactoryHardeningOffInTheDemo(t *testing.T) {
	cfg := &config.Config{Demo: true, Hardening: config.Hardening{Mode: hardening.ModeAudit}}
	cfg.Hardening.ApplyDefaults()
	if p := hardeningPorts(cfg, nil); p != nil {
		t.Fatalf("the demo is wired: %+v", p)
	}
}
