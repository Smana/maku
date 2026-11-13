// SPDX-License-Identifier: Apache-2.0

package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/wire"
)

// The drill report routes (hardening HR38).
const (
	drillsRoute   = "/v1/drills"
	lastPassRoute = "/v1/drills/last-pass" //nolint:gosec // G101: a route, not a credential
	// maxDrillReport is the broker's cap on one report's JSON.
	maxDrillReport = 256 << 10
)

var (
	// ErrDrillExists is 409 drill_exists: a report for that drill id is already stored.
	ErrDrillExists = errors.New("rooms: the broker already holds a report for this drill")

	drillIDRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	drillVersionRE = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
)

// PutDrillReport stores one scored drill in the broker (HR38): the factory is its only writer. A
// second report under the same id is ErrDrillExists.
func (c *Client) PutDrillReport(ctx context.Context, r hardening.DrillReport) error {
	if err := validDrillKey(hardening.DrillKey{Version: r.Version, ConfigHash: r.ConfigHash}); err != nil {
		return err
	}
	if !drillIDRE.MatchString(r.ID) || r.At.IsZero() {
		return errors.New("rooms: a drill report has a lowercase id and a time")
	}
	report, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("rooms: encode the drill report: %w", err)
	}
	if len(report) > maxDrillReport {
		return errors.New("rooms: the drill report is over 256 KiB")
	}
	in := struct {
		ID         string          `json:"id"`
		Version    string          `json:"version"`
		ConfigHash string          `json:"configHash"`
		At         time.Time       `json:"at"`
		Pass       bool            `json:"pass"`
		Report     json.RawMessage `json:"report"`
	}{r.ID, r.Version, r.ConfigHash, r.At.UTC(), r.Pass, report}
	var out struct {
		ID string `json:"id"`
	}
	err = c.call(ctx, http.MethodPost, drillsRoute, drillsRoute, "", "", in, maxReplyOverhead, &out)
	var api *APIError
	if errors.As(err, &api) && api.Reason == wire.ReasonDrillExists {
		return fmt.Errorf("%w: %s", ErrDrillExists, r.ID)
	}
	return err
}

// LastPass is the newest passing drill of key's version and configuration hash (R6):
// hardening.Drills. No passing drill is (DrillRecord{}, false, nil).
func (c *Client) LastPass(ctx context.Context, key hardening.DrillKey) (hardening.DrillRecord, bool, error) {
	if err := validDrillKey(key); err != nil {
		return hardening.DrillRecord{}, false, err
	}
	q := "?" + url.Values{"version": {key.Version}, "configHash": {key.ConfigHash}}.Encode()
	var out struct {
		ID string    `json:"id"`
		At time.Time `json:"at"`
	}
	err := c.call(ctx, http.MethodGet, lastPassRoute, lastPassRoute, "", q, nil, maxReplyOverhead, &out)
	var api *APIError
	switch {
	case errors.As(err, &api) && api.Reason == wire.ReasonNoDrill:
		return hardening.DrillRecord{}, false, nil
	case err != nil:
		return hardening.DrillRecord{}, false, err
	case !drillIDRE.MatchString(out.ID) || out.At.IsZero():
		return hardening.DrillRecord{}, false, errors.New("rooms: the broker's last passing drill has no id or time")
	}
	return hardening.DrillRecord{ID: out.ID, At: out.At.UTC()}, true, nil
}

func validDrillKey(k hardening.DrillKey) error {
	if !drillVersionRE.MatchString(k.Version) || !digestRE.MatchString(k.ConfigHash) {
		return errors.New("rooms: a drill key is a version and a 64-hex configuration hash")
	}
	return nil
}
