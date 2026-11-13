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
)

// PolicyEvidence is GET /v1/policy/evidence (merge policy stage 6) with the caller's own token: the
// repository's promotion evidence, which the factory reads for a reader of it on GitHub.
func (f Factory) PolicyEvidence(ctx context.Context, accessToken, repository string) (evidence.Report, error) {
	var out evidence.Report
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/policy/evidence?repository="+url.QueryEscape(repository), http.NoBody, &out)
	return out, err
}

// PolicyRepositories is GET /v1/policy/repositories with the caller's own token: the configured
// repositories they read.
func (f Factory) PolicyRepositories(ctx context.Context, accessToken string) ([]string, error) {
	var out struct {
		Repositories []string `json:"repositories"`
	}
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/policy/repositories", http.NoBody, &out)
	return out.Repositories, err
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

// PolicyDecisions is GET /v1/policy/decisions with the caller's own token: a pull request's
// decision timeline, for a reader of the repository.
func (f Factory) PolicyDecisions(ctx context.Context, accessToken, repository string, pr int) (policyview.Timeline, error) {
	var out policyview.Timeline
	q := url.Values{"repository": {repository}, "pr": {strconv.Itoa(pr)}}
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/policy/decisions?"+q.Encode(), http.NoBody, &out)
	return out, err
}
