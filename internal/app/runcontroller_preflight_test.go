// SPDX-License-Identifier: Apache-2.0

package app

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/preflight"
	"github.com/Smana/maku/internal/runctl"
)

// The run-controller serves its own preflight's last result as run-path, each refusal mapped one to
// one to a code; tracing and models report skip until their probes exist.
func TestRunPathResults(t *testing.T) {
	for name, c := range map[string]struct {
		res     runctl.Result
		mode    string
		outcome preflight.Outcome
		code    string
	}{
		"passed":                       {runctl.Result{State: runctl.PreflightPassed}, runctl.NetworkPolicyKubernetes, preflight.Pass, ""},
		"not run yet":                  {runctl.Result{State: runctl.PreflightUnknown}, runctl.NetworkPolicyKubernetes, preflight.Fail, preflight.Undecided},
		"a missing RuntimeClass":       {runctl.Result{State: runctl.PreflightFailed, Check: "runtimeClass", Message: "RuntimeClass \"gvisor\" not found"}, "", preflight.Fail, "run_path_runtimeclass"},
		"an unfilled git Secret":       {runctl.Result{State: runctl.PreflightWaiting, Check: "gitSecrets", Message: "agent-git has no token"}, "", preflight.Fail, "run_path_git_secrets"},
		"the CNI does not enforce":     {runctl.Result{State: runctl.PreflightFailed, Check: "network", Message: "the denied connection succeeded"}, runctl.NetworkPolicyKubernetes, preflight.Fail, "run_path_network"},
		"the gateway's token TTL":      {runctl.Result{State: runctl.PreflightFailed, Check: "gateway-token-ttl", Message: "ttl"}, "", preflight.Fail, "run_path_gateway"},
		"nothing confines the network": {runctl.Result{State: runctl.PreflightPassed}, runctl.NetworkPolicyNone, preflight.Warn, "network_policy_none"},
	} {
		t.Run(name, func(t *testing.T) {
			rs := byCheck(runControllerResults(c.res, c.mode))
			r := rs["run-path"]
			if r.Outcome != c.outcome || r.Code != c.code {
				t.Fatalf("%+v", r)
			}
			if c.res.Message != "" && c.outcome == preflight.Fail && !strings.Contains(r.Message, c.res.Message) {
				t.Fatalf("the refusal's message is lost: %+v", r)
			}
			if rs["tracing"].Outcome != preflight.Skip || rs["models"].Outcome != preflight.Skip {
				t.Fatalf("tracing and models: %+v", rs)
			}
			var rep preflight.Report
			for _, x := range rs {
				if err := rep.Add(x); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
