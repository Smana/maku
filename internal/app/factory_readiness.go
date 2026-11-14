// SPDX-License-Identifier: Apache-2.0

package app

import (
	"log/slog"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/hardeningreport"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/version"
)

// hardeningEvaluator is the production hardening.Readiness (H5, HR41): the live config's settings
// under this Maku version (R1, R4, R5, R7), the minter's role table (R2, R3, HR42), and the broker's
// drill reports (R6). Every fact is read afresh on each call.
func hardeningEvaluator(live func() *config.Config, grants tokenGrants, drills hardening.Drills, now func() time.Time) hardening.Evaluator {
	return hardening.Evaluator{
		Settings: func(repo string) hardening.Settings { return live().HardeningSettings(repo, version.Version) },
		Grants:   grants, Drills: drills, Now: now}
}

// readinessReporter reports each repository's readiness at mode audit or above, on the leader.
func readinessReporter(live func() *config.Config, r hardening.Readiness, m hardeningreport.Metrics, log *slog.Logger) *hardeningreport.Reporter {
	return &hardeningreport.Reporter{Readiness: r, Metrics: m, Log: log, Repositories: func() []string {
		cfg := live()
		var out []string
		for _, repo := range cfg.Repos() {
			if cfg.Hardening.ModeFor(repo.Name).AtLeast(hardening.ModeAudit) {
				out = append(out, repo.Name)
			}
		}
		return out
	}}
}

// coordinationPrerequisites is execute's capability gate (v4 MR16, HR41): the live config's class
// authority composed with hardening readiness, both asked afresh at every admission.
func coordinationPrerequisites(live config.Source, r hardening.Readiness) coordination.Prerequisites {
	return coordination.Prerequisites{Classes: coordination.ConfigAuthority{Cfg: live}, Hardening: r}
}
