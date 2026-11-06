// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"encoding/json"
	"net/url"

	"github.com/Smana/maku/internal/summary"
)

// NeedsFilter narrows the needs list; the broker applies it. Since is an RFC 3339 instant, the shape the
// answer's own cursor has, so one cut of the list can be asked for as is (CD28, Review Focus 3).
type NeedsFilter struct {
	Since, Room, Repo string
}

// query is the filter as the route reads it.
func (f NeedsFilter) query() string {
	q := url.Values{}
	if f.Since != "" {
		q.Set("since", f.Since)
	}
	if f.Room != "" {
		q.Set("room", f.Room)
	}
	if f.Repo != "" {
		q.Set("repo", f.Repo)
	}
	return q.Encode()
}

// NeedsPage is GET /api/needs' answer (needs/v1, CD21): the caller's needs across the rooms they may read, in
// the one order, the cursor to ask from next, and the rooms whose state could not be read, which contribute
// no item (Review Focus 1). It is the route's envelope and nothing of maku's, so the CLI and the tools read
// one shape; raw keeps that envelope as the broker sent it, for --json.
type NeedsPage struct {
	Items  []summary.Need `json:"items"`
	Cursor string         `json:"cursor"`
	Unread []string       `json:"unread,omitempty"`

	raw json.RawMessage
}

// Raw is the broker's answer as it sent it — apiVersion, access and the fields above — so `maku needs --json`
// shows a script what the browser reads, rather than an envelope maku rebuilt.
func (p NeedsPage) Raw() json.RawMessage { return p.raw }
