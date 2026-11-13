// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/httpx"
)

// maxForgeAnswer bounds the factory's forge-read answer: its items are each cut at
// hardening.MaxForgeBody, and a read returns at most a few dozen.
const maxForgeAnswer = 4 << 20

// ForgeClient is forge_read's port over the factory's forge-read route (HR35): it posts the run and
// target as the broker, with the broker's projected ServiceAccount token for the factory's
// audience, read from TokenFile at each call so the kubelet's rotation reaches it.
type ForgeClient struct {
	FactoryURL string
	TokenFile  string
	// HTTP is the client to the factory (its CA); called at each read.
	HTTP func() (*http.Client, error)
}

// ForgeRead is POST {FactoryURL}/v1/runs/{run}/forge-read. A 409 is ErrReadFilterOff; any other
// refusal is an error naming the status only.
func (c ForgeClient) ForgeRead(ctx context.Context, run, target string) (hardening.ForgeReadResult, error) {
	tok, err := os.ReadFile(filepath.Clean(c.TokenFile))
	if err != nil || len(bytes.TrimSpace(tok)) == 0 {
		return hardening.ForgeReadResult{}, fmt.Errorf("forge_read: the broker's factory token is unreadable: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"target": target})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.FactoryURL, "/")+"/v1/runs/"+url.PathEscape(run)+"/forge-read", bytes.NewReader(body))
	if err != nil {
		return hardening.ForgeReadResult{}, fmt.Errorf("forge_read: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+string(bytes.TrimSpace(tok)))
	req.Header.Set("Content-Type", "application/json")
	hc, err := c.HTTP()
	if err != nil {
		return hardening.ForgeReadResult{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return hardening.ForgeReadResult{}, fmt.Errorf("forge_read: factory: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxForgeAnswer)
	if err != nil {
		return hardening.ForgeReadResult{}, fmt.Errorf("forge_read: factory: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var out hardening.ForgeReadResult
		if err := json.Unmarshal(raw, &out); err != nil {
			return hardening.ForgeReadResult{}, fmt.Errorf("forge_read: factory answer: %w", err)
		}
		return out, nil
	case http.StatusConflict:
		return hardening.ForgeReadResult{}, ErrReadFilterOff
	}
	return hardening.ForgeReadResult{}, fmt.Errorf("forge_read: the factory refused the read (%d)", resp.StatusCode)
}
