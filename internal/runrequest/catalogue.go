// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/Smana/maku/internal/factory/catalogue"
)

// Catalogue is GET /v1/catalogue/{owner}/{name} with the caller's own token: what the repository
// offers at intake (P2). A sponsored principal names the Slack user it acts for in onBehalfOf
// ("slack:<team>/<user>"); a human leaves it empty. A refusal is an ErrRefused with its bounded
// reason (not_found, human_only, not_linked, link_unverified).
func (f Factory) Catalogue(ctx context.Context, accessToken, repository, onBehalfOf string) (catalogue.Catalogue, error) {
	owner, name, _ := strings.Cut(repository, "/")
	path := "/v1/catalogue/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	if onBehalfOf != "" {
		path += "?" + url.Values{"onBehalfOf": {onBehalfOf}}.Encode()
	}
	var out catalogue.Catalogue
	err := f.call(ctx, http.MethodGet, accessToken, path, http.NoBody, &out)
	return out, err
}
