// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/policyview"
	"github.com/Smana/maku/internal/factory/promote"
)

// PolicyEvidence is GET /v1/policy/evidence (merge policy stage 6) with the caller's own token: the
// repository's promotion evidence, which the factory reads for a reader of it on GitHub.
func (f Factory) PolicyEvidence(ctx context.Context, accessToken, repository string) (evidence.Report, error) {
	var out evidence.Report
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/policy/evidence?repository="+url.QueryEscape(repository), http.NoBody, &out)
	return out, err
}

// PolicyIndex is the configured repositories a caller reads, and the ones of them they maintain.
// Maintains is nil from a factory that does not say (an older release).
type PolicyIndex struct {
	Repositories []string `json:"repositories"`
	Maintains    []string `json:"maintains"`
}

// PolicyRepositories is GET /v1/policy/repositories with the caller's own token.
func (f Factory) PolicyRepositories(ctx context.Context, accessToken string) (PolicyIndex, error) {
	var out PolicyIndex
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/policy/repositories", http.NoBody, &out)
	return out, err
}

// PolicyPreview is POST /v1/policy/preview with the caller's own token: a proposed policy file
// checked, compared and simulated by the factory for a maintainer of the repository.
func (f Factory) PolicyPreview(ctx context.Context, accessToken, repository, file string) (policyview.Preview, error) {
	body, err := json.Marshal(map[string]string{"repository": repository, "file": file})
	if err != nil {
		return policyview.Preview{}, err
	}
	var out policyview.Preview
	err = f.call(ctx, http.MethodPost, accessToken, "/v1/policy/preview", bytes.NewReader(body), &out)
	return out, err
}

// PolicyList is GET /v1/policy/policies with the caller's own token: the MergePolicies they read a
// consumer of (B16).
func (f Factory) PolicyList(ctx context.Context, accessToken string) ([]evidence.PolicyReport, error) {
	var out struct {
		Policies []evidence.PolicyReport `json:"policies"`
	}
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/policy/policies", http.NoBody, &out)
	return out.Policies, err
}

// Policy is GET /v1/policy/policies/{name} with the caller's own token (B16).
func (f Factory) Policy(ctx context.Context, accessToken, name string) (evidence.PolicyReport, error) {
	var out evidence.PolicyReport
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/policy/policies/"+url.PathEscape(name), http.NoBody, &out)
	return out, err
}

// PolicyPreviewBindings is POST /v1/policy/preview of kind binding with the caller's own token: one
// repository's proposed mergePolicies, for a maintainer of it (B16).
func (f Factory) PolicyPreviewBindings(ctx context.Context, accessToken, repository string, bindings []config.Binding) (policyview.BindingPreview, error) {
	body, err := json.Marshal(map[string]any{"kind": "binding", "repository": repository, "bindings": bindings})
	if err != nil {
		return policyview.BindingPreview{}, err
	}
	var out policyview.BindingPreview
	err = f.call(ctx, http.MethodPost, accessToken, "/v1/policy/preview", bytes.NewReader(body), &out)
	return out, err
}

// PolicyPreviewPolicy is POST /v1/policy/preview of kind policy with the caller's own token: a
// proposed MergePolicy and the consumers they read (B11, B16).
func (f Factory) PolicyPreviewPolicy(ctx context.Context, accessToken, name string, spec config.PolicySpec) (policyview.PolicyPreview, error) {
	body, err := json.Marshal(map[string]any{"kind": "policy", "policy": name, "spec": spec})
	if err != nil {
		return policyview.PolicyPreview{}, err
	}
	var out policyview.PolicyPreview
	err = f.call(ctx, http.MethodPost, accessToken, "/v1/policy/preview", bytes.NewReader(body), &out)
	return out, err
}

// PolicyBacktest replays an edited policy over a repository's recorded diffs.
func (f Factory) PolicyBacktest(ctx context.Context, accessToken, repository string, request policyview.BacktestRequest, since string) (policyview.Backtest, error) {
	body, err := json.Marshal(struct {
		policyview.BacktestRequest
		Repository string `json:"repository"`
		Backtest   bool   `json:"backtest"`
		Since      string `json:"since,omitempty"`
	}{BacktestRequest: request, Repository: repository, Backtest: true, Since: since})
	if err != nil {
		return policyview.Backtest{}, err
	}
	var out policyview.Backtest
	err = f.call(ctx, http.MethodPost, accessToken, "/v1/policy/preview", bytes.NewReader(body), &out)
	return out, err
}

// PolicyRecentDecisions is GET /v1/policy/decisions with no pr: a repository's recent per-policy
// decisions, for a reader of it.
func (f Factory) PolicyRecentDecisions(ctx context.Context, accessToken, repository, since string) (policyview.Recent, error) {
	var out policyview.Recent
	q := url.Values{"repository": {repository}}
	if since != "" {
		q.Set("since", since)
	}
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/policy/decisions?"+q.Encode(), http.NoBody, &out)
	return out, err
}

// PolicyDecisions is GET /v1/policy/decisions with the caller's own token: a pull request's
// decision timeline, for a reader of the repository.
func (f Factory) PolicyDecisions(ctx context.Context, accessToken, repository string, pr int) (policyview.Timeline, error) {
	var out policyview.Timeline
	q := url.Values{"repository": {repository}, "pr": {strconv.Itoa(pr)}}
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/policy/decisions?"+q.Encode(), http.NoBody, &out)
	return out, err
}

// PolicyPromote opens a one-line promotion pull request for a maintainer.
func (f Factory) PolicyPromote(ctx context.Context, accessToken, repository, class string, minimum int) (promote.Result, error) {
	body, err := json.Marshal(map[string]any{"repository": repository, "class": class, "minimum": minimum})
	if err != nil {
		return promote.Result{}, err
	}
	var out promote.Result
	err = f.call(ctx, http.MethodPost, accessToken, "/v1/policy/promote", bytes.NewReader(body), &out)
	return out, err
}

// PolicyPromotionStatus reads the facts that close a promotion record.
func (f Factory) PolicyPromotionStatus(ctx context.Context, accessToken, repository, class string, number int) (promote.Status, error) {
	var out promote.Status
	q := url.Values{"repository": {repository}, "class": {class}, "pr": {strconv.Itoa(number)}}
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/policy/promotion?"+q.Encode(), http.NoBody, &out)
	return out, err
}

// PolicyDrillResult is a rollback drill's pull request.
type PolicyDrillResult struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// PolicyDrill opens a labelled rollback-drill revert for a maintainer.
func (f Factory) PolicyDrill(ctx context.Context, accessToken, repository string, number int) (PolicyDrillResult, error) {
	body, err := json.Marshal(map[string]any{"repository": repository, "pullRequest": number})
	if err != nil {
		return PolicyDrillResult{}, err
	}
	var out PolicyDrillResult
	err = f.call(ctx, http.MethodPost, accessToken, "/v1/policy/drill", bytes.NewReader(body), &out)
	return out, err
}

// InstallInfo is the factory's GET /v1/install answer: its banner codes and bounded non-sensitive
// environment metadata (never a repository name).
type InstallInfo struct {
	Unsafe          []string `json:"unsafe"`
	Version         string   `json:"version,omitempty"`
	Factory         string   `json:"factory,omitempty"`
	Namespace       string   `json:"namespace,omitempty"`
	DefaultHarness  string   `json:"defaultHarness,omitempty"`
	PolicyStage     string   `json:"policyStage,omitempty"`
	Hardening       string   `json:"hardening,omitempty"`
	RoutinesUIWrite *bool    `json:"routinesUIWrite,omitempty"`
	Demo            bool     `json:"demo,omitempty"`
}

// Install is GET /v1/install with the caller's own token: the factory's banner codes and bounded
// environment metadata (v4 D3).
func (f Factory) Install(ctx context.Context, accessToken string) (InstallInfo, error) {
	var out InstallInfo
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/install", http.NoBody, &out)
	return out, err
}
