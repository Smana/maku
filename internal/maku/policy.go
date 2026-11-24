// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Smana/maku/internal/factory/policyview"
	"github.com/Smana/maku/internal/factory/promote"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/runrequest"
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

// PolicyBacktest replays an edited policy file through the broker.
func (c Client) PolicyBacktest(ctx context.Context, repository, file, since string) (policyview.Backtest, error) {
	var out policyview.Backtest
	path := "/api/policy/repositories/" + policyRepositoryPath(repository) + "/backtest"
	body, err := json.Marshal(map[string]any{"kind": "file", "file": file, "since": since})
	if err != nil {
		return out, err
	}
	return out, c.policyPost(ctx, path, body, &out)
}

// PolicyPromote opens a policy's promotion pull request through the broker.
func (c Client) PolicyPromote(ctx context.Context, repository, class string) (promote.Result, error) {
	var out promote.Result
	body, err := json.Marshal(map[string]string{"repository": repository, "class": class})
	if err != nil {
		return out, err
	}
	return out, c.policyPost(ctx, "/api/policy/promotions", body, &out)
}

// PolicyDrill opens a labelled rollback-drill revert through the broker.
func (c Client) PolicyDrill(ctx context.Context, repository string, number int) (runrequest.PolicyDrillResult, error) {
	var out runrequest.PolicyDrillResult
	path := "/api/policy/repositories/" + policyRepositoryPath(repository) + "/drill"
	body, err := json.Marshal(map[string]any{"pullRequest": number})
	if err != nil {
		return out, err
	}
	return out, c.policyPost(ctx, path, body, &out)
}

func policyRepositoryPath(repository string) string {
	parts := strings.Split(repository, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (c Client) policyGet(ctx context.Context, path string, out any) error {
	return c.policyDo(ctx, http.MethodGet, path, nil, out)
}

func (c Client) policyPost(ctx context.Context, path string, body []byte, out any) error {
	return c.policyDo(ctx, http.MethodPost, path, body, out)
}

func (c Client) policyDo(ctx context.Context, method, path string, body []byte, out any) error {
	h, err := c.header(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = h
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return refusal("policy", resp)
	}
	raw, err := httpx.ReadBody(resp.Body, maxFrame)
	if err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("policy: decode: %w", err)
	}
	return nil
}
