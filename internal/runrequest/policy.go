// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

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

// PolicyDecisions is GET /v1/policy/decisions with the caller's own token: a pull request's
// decision timeline, for a reader of the repository.
func (f Factory) PolicyDecisions(ctx context.Context, accessToken, repository string, pr int) (policyview.Timeline, error) {
	var out policyview.Timeline
	q := url.Values{"repository": {repository}, "pr": {strconv.Itoa(pr)}}
	err := f.call(ctx, http.MethodGet, accessToken, "/v1/policy/decisions?"+q.Encode(), http.NoBody, &out)
	return out, err
}
