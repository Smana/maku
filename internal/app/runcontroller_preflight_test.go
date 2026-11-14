// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
			if c.res.Message != "" && strings.Contains(r.Message, c.res.Message) {
				t.Fatalf("the refusal's own text crossed /preflight: %+v", r)
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

// No free text crosses /preflight (v4 D24): runctl's messages carry API server errors, a probe pod's
// termination output and event messages, so run-path answers one fixed message per code. Each shape
// of secret, in each refusal, must stay out of the served answer.
func TestRunPathAnswersFixedMessages(t *testing.T) {
	secrets := []string{
		"sk-proj-abcdefghijklmnop", "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln", "password=hunter2",
		"bearer abc.def.ghi", "Basic dXNlcjpwYXNz", "ghr_0123456789abcdefABCDEF", "ghp_0123456789abcdef",
		"postgres://u:p@h/db", "-----BEGIN PRIVATE KEY-----",
	}
	for _, check := range []string{"runtimeClass", "gitSecrets", "network", "gateway-jwks", "something-new"} {
		for _, state := range []string{runctl.PreflightFailed, runctl.PreflightWaiting} {
			res := runctl.Result{State: state, Check: check, Message: "Get \"https://10.0.0.1:443/api\": " + strings.Join(secrets, " ")}
			srv := httptest.NewServer(preflight.Handler("run-controller", "v0", func(context.Context) []preflight.Result {
				return runControllerResults(res, runctl.NetworkPolicyKubernetes)
			}, nil))
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			srv.Close()
			for _, sec := range secrets {
				if strings.Contains(string(body), sec) {
					t.Fatalf("%s/%s: %q reached /preflight: %s", check, state, sec, body)
				}
			}
			if strings.Contains(string(body), "10.0.0.1") {
				t.Fatalf("%s: the API server's error crossed /preflight: %s", check, body)
			}
		}
	}
}

// /preflight is served only when the chart's preflight.enabled sets MAKU_PREFLIGHT (v4 D24): a
// default install's pods cannot make a component spend its credentials on demand.
func TestPreflightMountedOnlyWhenEnabled(t *testing.T) {
	ops := http.NotFoundHandler()
	pf := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") })
	for env, want := range map[string]int{"": http.StatusNotFound, "false": http.StatusNotFound, "true": http.StatusOK} {
		getenv := func(k string) string {
			if k == "MAKU_PREFLIGHT" {
				return env
			}
			return ""
		}
		rec := httptest.NewRecorder()
		withPreflight(ops, preflightOn(getenv), func() http.Handler { return pf }).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/preflight", nil))
		if rec.Code != want {
			t.Fatalf("MAKU_PREFLIGHT=%q: %d, want %d", env, rec.Code, want)
		}
		if got := runControllerExtraHandlers(preflightOn(getenv), pf); (len(got) == 1) != (want == http.StatusOK) {
			t.Fatalf("MAKU_PREFLIGHT=%q: the run-controller's extra handlers are %v", env, got)
		}
	}
}
