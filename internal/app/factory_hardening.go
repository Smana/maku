// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"slices"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/reconciler"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/httpx"
)

// hardeningPorts are the reconciler's hardening ports for cfg (stage H1): nil while hardening is
// off centrally and for every repository, so nothing is planted, recorded or shadowed. Otherwise a
// Checker with every layer at audit (T5 at its effective mode: off without an agent-guard URL),
// secrets as T3's scanner, and an empty RepositoryPolicies the fence's OnFile feeds. The ports are
// built once at startup: turning audit on from off takes a restart.
func hardeningPorts(cfg *config.Config, secrets hardening.SecretScanner) *reconciler.HardeningPorts {
	h := cfg.Hardening
	if !hardeningOn(h) {
		return nil
	}
	c := hardening.Checker{Layers: hardening.LayersFor(hardening.ModeAudit, h.EffectiveClassifier()), Secrets: secrets,
		LabelAllowlist: slices.Clone(h.LabelAllowlist)}
	if h.AgentGuard.URL != "" {
		c.Classifier = hardening.WebhookClassifier{URL: h.AgentGuard.URL, HC: httpx.New(h.AgentGuard.Timeout.Duration, nil)}
	}
	return &reconciler.HardeningPorts{Policies: &hardening.RepositoryPolicies{}, Checker: c}
}

// hardeningOn reports whether any repository runs at least audit: centrally, or one stricter.
func hardeningOn(h config.Hardening) bool {
	if h.Mode.AtLeast(hardening.ModeAudit) {
		return true
	}
	for _, r := range h.Repositories {
		if r.Mode.AtLeast(hardening.ModeAudit) {
			return true
		}
	}
	return false
}

// fenceObserver is the fence's OnFile.
type fenceObserver = func(ctx context.Context, repository string, body []byte)

// chainOnFile is one OnFile that hands each fence file to every non-nil observer in order; nil
// when there is none.
func chainOnFile(observers ...fenceObserver) fenceObserver {
	var fs []fenceObserver
	for _, f := range observers {
		if f != nil {
			fs = append(fs, f)
		}
	}
	switch len(fs) {
	case 0:
		return nil
	case 1:
		return fs[0]
	}
	return func(ctx context.Context, repository string, body []byte) {
		for _, f := range fs {
			f(ctx, repository, body)
		}
	}
}
