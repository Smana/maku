// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Smana/maku/internal/factory/policyview"
	"github.com/Smana/maku/internal/httpx"
)

// PolicyRecent lists a repository's recent per-policy decisions through the broker.
func (c Client) PolicyRecent(ctx context.Context, repository, since string) (policyview.Recent, error) {
	var out policyview.Recent
	path := "/api/policy/repositories/" + policyRepositoryPath(repository) + "/decisions"
	if since != "" {
		path += "?" + url.Values{"since": {since}}.Encode()
	}
	return out, c.policyGet(ctx, path, &out)
}

// PolicyTimeline explains one pull request's decision timeline through the broker.
func (c Client) PolicyTimeline(ctx context.Context, repository string, pr int) (policyview.Timeline, error) {
	var out policyview.Timeline
	path := fmt.Sprintf("/api/policy/repositories/%s/decisions/%d", policyRepositoryPath(repository), pr)
	return out, c.policyGet(ctx, path, &out)
}

func policyRepositoryPath(repository string) string {
	parts := strings.Split(repository, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (c Client) policyGet(ctx context.Context, path string, out any) error {
	h, err := c.header(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+path, nil)
	if err != nil {
		return err
	}
	req.Header = h
	resp, err := c.HC.Do(req)
	if err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return refusal("policy", resp)
	}
	body, err := httpx.ReadBody(resp.Body, maxFrame)
	if err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("policy: decode: %w", err)
	}
	return nil
}
