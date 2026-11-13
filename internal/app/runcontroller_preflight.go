// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"strings"

	"github.com/Smana/maku/internal/preflight"
	"github.com/Smana/maku/internal/runctl"
)

// runPathCodes maps each of the run-controller's preflight checks to its catalog code, one to one.
var runPathCodes = map[string]string{ //nolint:gosec // catalog codes, not credentials
	"runtimeClass": "run_path_runtimeclass",
	"gitSecrets":   "run_path_git_secrets",
	"network":      "run_path_network",
}

// runControllerResults is the run-controller's /preflight (v4 D24): its own preflight's last result
// as run-path, never a fresh run of it (the leader runs it every 10 minutes and after each change).
// tracing and models report skip: their probes need a pod of their own, which would otherwise put a
// collector or a provider outage in the way of new runs.
func runControllerResults(res runctl.Result, mode string) []preflight.Result {
	run := preflight.Result{Check: "run-path"}
	switch res.State {
	case runctl.PreflightPassed:
		run.Outcome, run.Message = preflight.Pass, "the run-controller's preflight passed"
		if mode == runctl.NetworkPolicyNone {
			run.Outcome, run.Code, run.Message = preflight.Warn, "network_policy_none", "networkPolicies.mode none: nothing confines a run's network"
		}
	case runctl.PreflightFailed, runctl.PreflightWaiting:
		code, ok := runPathCodes[res.Check]
		if !ok && strings.HasPrefix(res.Check, "gateway") {
			code, ok = "run_path_gateway", true
		}
		if !ok {
			code = preflight.Undecided
		}
		run.Outcome, run.Code, run.Message = preflight.Fail, code, res.Check+": "+res.Message
	default:
		run.Outcome, run.Code, run.Message = preflight.Fail, preflight.Undecided,
			"the run-controller's preflight has not finished, or this replica does not lead"
	}
	return []preflight.Result{run,
		{Check: "tracing", Outcome: preflight.Skip, Message: "the collector's probe from a run pod is not built yet"},
		{Check: "models", Outcome: preflight.Skip, Message: "the run aliases' probe through the gateway is not built yet"}}
}

// runControllerSource serves pf's latest result; nil before the preflight exists.
func runControllerSource(pf func() *runctl.Preflight, mode string) preflight.Source {
	return func(context.Context) []preflight.Result {
		p := pf()
		if p == nil {
			return runControllerResults(runctl.Result{State: runctl.PreflightUnknown}, mode)
		}
		return runControllerResults(p.Result(), mode)
	}
}
