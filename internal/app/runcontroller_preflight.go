// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/Smana/maku/internal/preflight"
	"github.com/Smana/maku/internal/runctl"
)

// runPathCodes maps each of the run-controller's preflight checks to its catalog code, one to one.
var runPathCodes = map[string]string{ //nolint:gosec // catalog codes, not credentials
	"runtimeClass": "run_path_runtimeclass",
	"gitSecrets":   "run_path_git_secrets",
	"network":      "run_path_network",
}

// runPathMessages is run-path's one fixed message per code: runctl's own messages carry API server
// errors, a probe pod's output and event text, so none crosses /preflight. The detail stays in the
// run-controller's log, and on each refused run's Ready condition.
var runPathMessages = map[string]string{
	"run_path_runtimeclass": "the runs' RuntimeClass is missing or unusable: see the run-controller's log",
	"run_path_git_secrets":  "a git Secret the runs need is missing or not filled: see the run-controller's log",
	"run_path_network":      "the CNI does not enforce the runs' NetworkPolicy: see the run-controller's log",
	"run_path_gateway":      "the model gateway's identity, aliases or token TTL refused the runs: see the run-controller's log",
	preflight.Undecided:     "a run-controller preflight check refused the runs: see the run-controller's log",
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
		run.Outcome, run.Code, run.Message = preflight.Fail, code, runPathMessages[code]
	default:
		run.Outcome, run.Code, run.Message = preflight.Fail, preflight.Undecided,
			"the run-controller's preflight has not finished, or this replica does not lead"
	}
	return []preflight.Result{run,
		{Check: "tracing", Outcome: preflight.Skip, Message: "the collector's probe from a run pod is not built yet"},
		{Check: "models", Outcome: preflight.Skip, Message: "the run aliases' probe through the gateway is not built yet"}}
}

// runControllerSource serves pf's latest result; nil before the preflight exists. A refusal's own
// text is logged, once per change, and never served.
func runControllerSource(pf func() *runctl.Preflight, mode string, log *slog.Logger) preflight.Source {
	var (
		mu   sync.Mutex
		last runctl.Result
	)
	return func(context.Context) []preflight.Result {
		res := runctl.Result{State: runctl.PreflightUnknown}
		if p := pf(); p != nil {
			res = p.Result()
		}
		if log != nil && (res.State == runctl.PreflightFailed || res.State == runctl.PreflightWaiting) {
			mu.Lock()
			if res.State != last.State || res.Check != last.Check || res.Message != last.Message {
				log.Warn("preflight: run-path refused", "check", res.Check, "state", res.State, "detail", res.Message)
			}
			last = res
			mu.Unlock()
		}
		return runControllerResults(res, mode)
	}
}

// runControllerExtraHandlers mounts /preflight on the metrics server only when on (MAKU_PREFLIGHT).
func runControllerExtraHandlers(on bool, pf http.Handler) map[string]http.Handler {
	if !on {
		return nil
	}
	return map[string]http.Handler{"/preflight": pf}
}
