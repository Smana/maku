// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Smana/maku/internal/factory/stages"
)

// Stages is GET /v1/stages?repository=<owner/name>[&class=<c>][&last=<n>] with the caller's token (WR5).
func (f Factory) Stages(ctx context.Context, accessToken, repository, class string, last int) (stages.Report, error) {
	q := url.Values{}
	if repository != "" {
		q.Set("repository", repository)
	}
	if class != "" {
		q.Set("class", class)
	}
	if last > 0 {
		q.Set("last", strconv.Itoa(last))
	}
	path := "/v1/stages"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var out stages.Report
	err := f.call(ctx, http.MethodGet, accessToken, path, http.NoBody, &out)
	return out, err
}

// WorkflowDryRun is POST /v1/workflows/dry-run with the caller's token (WR11).
func (f Factory) WorkflowDryRun(ctx context.Context, accessToken string, in stages.DryRunRequest) (stages.DryRunResult, error) {
	var out stages.DryRunResult
	body, err := json.Marshal(in)
	if err != nil {
		return out, fmt.Errorf("runrequest: workflow dry-run: %w", err)
	}
	err = f.call(ctx, http.MethodPost, accessToken, "/v1/workflows/dry-run", bytes.NewReader(body), &out)
	return out, err
}
