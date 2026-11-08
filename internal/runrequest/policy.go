// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Smana/maku/internal/factory/evidence"
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
