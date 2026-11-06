// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/roomview"
)

// needsSurfaceTool is needs_list_total's tool door (CD28): the App reads the list over /mcp, the browser
// and the CLI over GET /api/needs, and one query covers both (needsSurfaceAPI is the other).
const needsSurfaceTool = "tool"

// needsArgs are needs_list's arguments: the three filters GET /api/needs takes, and nothing that could
// carry a decision (CD28's closed schema — the tool's schema refuses any other property).
type needsArgs struct {
	Since string `json:"since"`
	Room  string `json:"room"`
	Repo  string `json:"repo"`
}

// needsServedTool counts one needs_list call by principal and outcome (needs_list_total, served as
// maku_needs_list_total), under the tool's surface: the same series the route counts.
func (s *Server) needsServedTool(ctx context.Context, p authn.Principal, outcome string) {
	if s.Metrics != nil {
		s.Metrics.NeedsList(ctx, needsSurfaceTool, p.ID, outcome)
	}
}

// needsList is needs_list: the caller's needs across every room they may read, as needs/v1 — GET
// /api/needs' answer for a client that reaches the broker through /mcp (CD21, CD28). Both doors read one
// scan (needsScan), so their items and their order cannot disagree, and the text is roomview.Needs', the
// list as the model shows it. It is listed only while human.needs.enabled serves the list (mcpTools), so
// the App's door and the browser's turn off together. A sponsored caller reads the room's needs, not a
// login's (summary.Caller.RoomScoped): it has no login, and every item's action is a link, never a decision.
func (s *Server) needsList(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
	var a needsArgs
	bad := toolError("invalid_arguments: since is an RFC 3339 instant, as the answer's cursor is, and never in the future; " +
		"room is a room id; repo is owner/name")
	if mcpwire.DecodeArgs(args, &a) != nil {
		return nil, bad
	}
	// Review Focus 3: a cursor that is not an instant, or one in the future, is refused — never read as
	// everything (a list nobody asked for), and never as nothing (a future cursor would filter every dated
	// item away and answer a quiet list nobody is in). The answer's own cursor is always past this check.
	var since time.Time
	if a.Since != "" {
		t, err := time.Parse(time.RFC3339, a.Since)
		if err != nil || t.After(s.now()) {
			s.needsServedTool(ctx, p, needsOutcomeRefused)
			return nil, bad
		}
		since = t
	}
	items, unread, access, err := s.needsScan(ctx, p, needsFilter{room: a.Room, repo: a.Repo, roomScoped: p.Sponsored})
	if err != nil {
		s.needsServedTool(ctx, p, needsOutcomeError)
		return nil, err
	}
	outcome := needsOutcomeOK
	if len(unread) > 0 {
		outcome = needsOutcomeUnread
	}
	s.needsServedTool(ctx, p, outcome)
	items = newerThan(items, since)
	s.needsItems(items)
	var text strings.Builder
	if err := roomview.Needs(&text, items, unread, s.viewOptions(false)); err != nil {
		return nil, err
	}
	// The envelope is the route's, field for field (needsList is needsRoute's answer without the HTTP
	// door): a client reads one shape whichever door it came through.
	return viewResult{text: text.String(), data: needsEnvelope{APIVersion: needsVersion, Items: items,
		Access: access, Unread: unread, Cursor: s.now().UTC().Truncate(time.Second).Format(time.RFC3339)}}, nil
}
