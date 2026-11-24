// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"context"
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
