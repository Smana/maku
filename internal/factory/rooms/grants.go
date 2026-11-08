// SPDX-License-Identifier: Apache-2.0

package rooms

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Smana/maku/internal/factory/grant"
)

// The broker's grant routes (merge policy stage 5), system:factory only.
const (
	grantLookupRoute  = "/v1/grants/lookup"
	grantUseRoute     = "/v1/grants/{id}/use"
	grantDisableRoute = "/v1/grants/{id}/disable"
	// maxGrantReply bounds a lookup's answer: a class holds at most a few hundred grants, each
	// with 32 patterns of 256 bytes at most.
	maxGrantReply = 4 << 20
)

var (
	policyHashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	headRE       = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// LookupGrants reads repository class's active grants immediately before a decision. The broker
// first expires those past their expiry or maxLifetime since their last renewal, and suspends
// those made under another policy than policyHash; events are what it recorded. Never cached:
// every decision asks again (MP13).
func (c *Client) LookupGrants(ctx context.Context, repository, class, policyHash string, maxLifetime time.Duration) ([]grant.Grant, []string, error) {
	if !policyHashRE.MatchString(policyHash) || maxLifetime < time.Second {
		return nil, nil, fmt.Errorf("rooms: a grant lookup needs the policy's full sha256 and a lifetime")
	}
	in := struct {
		Repository         string `json:"repository"`
		Class              string `json:"class"`
		PolicyHash         string `json:"policyHash"`
		MaxLifetimeSeconds int64  `json:"maxLifetimeSeconds"`
	}{repository, class, policyHash, int64(maxLifetime.Seconds())}
	var out struct {
		Grants []grant.Grant `json:"grants"`
		Events []string      `json:"events"`
	}
	if err := c.do(ctx, http.MethodPost, grantLookupRoute, "", "", in, maxGrantReply, &out); err != nil {
		return nil, nil, err
	}
	return out.Grants, out.Events, nil
}

// UseGrant records that the factory merged pr at head on grant id's version.
func (c *Client) UseGrant(ctx context.Context, id string, version, pr int, head string) error {
	if !grant.IDRE.MatchString(id) || version < 1 || pr < 1 || !headRE.MatchString(head) {
		return fmt.Errorf("rooms: %q:%d is not a grant use", id, version)
	}
	in := struct {
		Version int    `json:"version"`
		PR      int    `json:"pr"`
		Head    string `json:"head"`
	}{version, pr, head}
	var out grant.Grant
	return c.grantDo(ctx, grantUseRoute, id, in, &out)
}

// DisableGrant disables grant id for good: the factory reverted pr, a merge it authorized.
func (c *Client) DisableGrant(ctx context.Context, id string, pr int, reason string) error {
	if !grant.IDRE.MatchString(id) || pr < 1 || len(reason) > grant.MaxReasonBytes {
		return fmt.Errorf("rooms: %q is not a grant to disable", id)
	}
	in := struct {
		PR     int    `json:"pr"`
		Reason string `json:"reason"`
	}{pr, reason}
	var out grant.Grant
	return c.grantDo(ctx, grantDisableRoute, id, in, &out)
}

// grantDo is a call on a grant's own route. Its span names the route, never the grant (MP18).
func (c *Client) grantDo(ctx context.Context, route, id string, in, out any) error {
	return c.call(ctx, http.MethodPost, route, strings.Replace(route, "{id}", url.PathEscape(id), 1), "", "", in,
		maxReplyOverhead+maxGrantReply/1024, out)
}
