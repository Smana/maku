// SPDX-License-Identifier: Apache-2.0

// Package preflight answers, seam by seam, whether an install can work (v4 D24). It holds the check
// catalog (IN2), the report (IN5) and the /preflight shape each component serves. Each check runs
// where its traffic runs: the cluster checks in the preflight pod, the component checks inside the
// component that makes the call, the outside checks in the CLI.
//
// A check that cannot decide fails with the code undecided, never passes. No result names a value:
// a message names a Secret and key, a host, an HTTP status or a SQLSTATE.
package preflight

import (
	"fmt"
	"slices"
)

// Seam is a row of the spec's seam table: the summary groups checks in this order.
type Seam string

// The seams, in docs/install.md's order.
const (
	SeamCluster       Seam = "cluster"
	SeamSandbox       Seam = "sandbox"
	SeamSecrets       Seam = "secrets"
	SeamNetwork       Seam = "network"
	SeamPostgres      Seam = "postgres"
	SeamIdentity      Seam = "identity"
	SeamGitHub        Seam = "github"
	SeamModels        Seam = "models"
	SeamRunLore       Seam = "runlore"
	SeamExposure      Seam = "exposure"
	SeamObservability Seam = "observability"
)

// SeamOrder is the summary's order.
var SeamOrder = []Seam{SeamCluster, SeamSandbox, SeamSecrets, SeamNetwork, SeamPostgres, SeamIdentity, SeamGitHub,
	SeamModels, SeamRunLore, SeamExposure, SeamObservability}

// Where is where a check runs: the preflight pod, a component, or the CLI.
type Where string

// The places.
const (
	InPod           Where = "pod"
	InRunController Where = "run-controller"
	InBroker        Where = "broker"
	InFactory       Where = "factory"
	InCLI           Where = "cli"
)

// The two codes every check may fail with.
const (
	// Undecided: the check could not decide (a timeout, a 5xx, an RBAC denial on its own read).
	Undecided = "undecided"
	// ComponentTooOld: the component serves no /preflight; its image predates it.
	ComponentTooOld = "component_too_old"
)

// Check is one row of the catalog: its id, its seams (the first orders it), where it runs, and the
// closed set of codes a failure or a warning may carry.
type Check struct {
	ID     string
	Seams  []Seam
	RunsIn []Where
	Fail   []string
	Warn   []string
	What   string // what it checks, for docs/install.md
	From   string // the spec decisions it proves
}

// Catalog is IN2's table.
var Catalog = []Check{
	{ID: "kubernetes", Seams: []Seam{SeamCluster}, RunsIn: []Where{InPod}, Fail: []string{"kubernetes_too_old"},
		What: "the server version is 1.30 or later", From: "D26"},
	{ID: "crds", Seams: []Seam{SeamCluster}, RunsIn: []Where{InPod}, Fail: []string{"crd_missing", "crd_version"},
		What: "the six maku CRDs at the chart's served versions, and each CRD the values need (agentgateway's, Gateway API's, prometheus-operator's, ESO's)",
		From: "D13, A11"},
	{ID: "runtimeclass", Seams: []Seam{SeamSandbox}, RunsIn: []Where{InPod}, Fail: []string{"runtimeclass_missing"},
		Warn: []string{"unsafe_allow_runc"}, What: "the RuntimeClass sandbox.runtimeClassName names exists", From: "D18"},
	{ID: "secrets", Seams: []Seam{SeamSecrets}, RunsIn: []Where{InPod}, Fail: []string{"secret_missing", "secret_key_missing"},
		What: "each Secret of the registry the values enable exists, with its documented keys", From: "D14"},
	{ID: "run-path", Seams: []Seam{SeamNetwork, SeamSandbox}, RunsIn: []Where{InRunController},
		Fail: []string{"run_path_runtimeclass", "run_path_git_secrets", "run_path_network", "run_path_gateway"},
		Warn: []string{"network_policy_none"}, What: "the run-controller's preflight: RuntimeClass, git Secrets, the CNI probe, the gateway",
		From: "D6, D18"},
	{ID: "database", Seams: []Seam{SeamPostgres}, RunsIn: []Where{InBroker},
		Fail: []string{"db_unreachable", "db_too_old", "db_role_bypasses_rls", "db_wrong_role", "db_schema_behind", "db_schema_ahead"},
		Warn: []string{"schema_unverified"}, What: "the three DSNs: reachable, Postgres 16 or later, roles without BYPASSRLS, the schema current",
		From: "D9"},
	{ID: "oidc", Seams: []Seam{SeamIdentity}, RunsIn: []Where{InBroker, InFactory},
		Fail: []string{"oidc_discovery", "oidc_issuer_mismatch", "oidc_jwks"},
		What: "discovery answers, its issuer matches, the JWKS fetches", From: "D8"},
	{ID: "github-link", Seams: []Seam{SeamIdentity}, RunsIn: []Where{InBroker}, Fail: []string{"link_client", "link_callback_mismatch"},
		What: "the GitHub link's OAuth client authorizes, and its callback is the web host's", From: "D8"},
	{ID: "apps", Seams: []Seam{SeamGitHub}, RunsIn: []Where{InFactory}, Fail: []string{"app_not_installed", "app_permission_missing"},
		What: "each served repository has both App installations, with their grants", From: "D10, D12"},
	{ID: "merge-gate", Seams: []Seam{SeamGitHub}, RunsIn: []Where{InFactory}, Fail: []string{"merge_gate_not_required"},
		Warn: []string{"merge_gate_rules_unread"}, What: "the default branch's ruleset requires maku/merge-gate from the posting App", From: "D11"},
	{ID: "private-code", Seams: []Seam{SeamGitHub}, RunsIn: []Where{InFactory}, Fail: []string{"private_code_unacknowledged"},
		What: "each private repository has privateCode.acknowledged: without it, it is refused new tasks", From: "D3"},
	{ID: "models", Seams: []Seam{SeamModels}, RunsIn: []Where{InBroker, InRunController}, Fail: []string{"model_unreachable", "model_refused"},
		What: "the planner, the classifier and one run alias per listener answer a one-token completion", From: "D7"},
	{ID: "knowledge", Seams: []Seam{SeamRunLore}, RunsIn: []Where{InBroker}, Fail: []string{"knowledge_unreachable", "knowledge_tools_missing"},
		What: "tools/list lists kb_search and kb_get within the timeout", From: "D20"},
	{ID: "exposure", Seams: []Seam{SeamExposure}, RunsIn: []Where{InFactory, InCLI},
		Fail: []string{"broker_exposed_around_proxy", "host_unresolved", "route_unanswered", "slack_route_too_wide"},
		What: "no route reaches the broker around the proxy; each host resolves and answers", From: "D19"},
	{ID: "tracing", Seams: []Seam{SeamObservability}, RunsIn: []Where{InRunController}, Fail: []string{"collector_unreachable"},
		What: "the collector's OTLP port accepts a run's connection", From: "D21"},
}

// lookup is the catalog's entry for id.
func lookup(id string) (Check, bool) {
	i := slices.IndexFunc(Catalog, func(c Check) bool { return c.ID == id })
	if i < 0 {
		return Check{}, false
	}
	return Catalog[i], true
}

// Outcome is a check's verdict.
type Outcome string

// The outcomes.
const (
	Pass Outcome = "pass"
	Warn Outcome = "warn"
	Fail Outcome = "fail"
	Skip Outcome = "skip"
)

// Result is one check's verdict in one place: IN5's JSON line.
type Result struct {
	Check     string  `json:"check"`
	Component string  `json:"component"`
	Outcome   Outcome `json:"outcome"`
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	Seconds   float64 `json:"seconds"`
}

// panicOnInvalid makes an invalid result panic: this package's tests set it, so a new code never
// ships without its catalog entry.
var panicOnInvalid = false

// normalize holds a result to the catalog: a fail or warn code outside its check's set, an unknown
// check or outcome, is a bug. In tests it panics, so it never ships; in production it becomes a
// fail undecided, never a pass.
func normalize(r Result) Result {
	if err := validate(r); err != nil {
		if panicOnInvalid {
			panic(err)
		}
		r.Outcome, r.Code, r.Message = Fail, Undecided, "the check reported an invalid result: "+err.Error()
	}
	return r
}

func validate(r Result) error {
	c, ok := lookup(r.Check)
	if !ok {
		return fmt.Errorf("check %q is not in the catalog", r.Check)
	}
	switch r.Outcome {
	case Pass, Skip:
		return nil
	case Fail:
		if r.Code == Undecided || r.Code == ComponentTooOld || slices.Contains(c.Fail, r.Code) {
			return nil
		}
	case Warn:
		if slices.Contains(c.Warn, r.Code) {
			return nil
		}
	default:
		return fmt.Errorf("outcome %q is not pass, warn, fail or skip", r.Outcome)
	}
	return fmt.Errorf("%s: code %q is not a %s code of the catalog", r.Check, r.Code, r.Outcome)
}
