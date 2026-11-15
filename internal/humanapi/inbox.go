// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/summary"
)

const inboxDescription = "Use to see what is waiting for the developer across their tasks."

var inboxInputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"since":{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"},"room":{"type":"string","pattern":"^[a-z2-7]{8}$"},"repo":{"type":"string","pattern":"^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$"}}}`)

var needsOutputSchema = json.RawMessage(`{"type":"object","required":["apiVersion","items","cursor","access"],"properties":{"apiVersion":{"type":"string"},"cursor":{"type":"string"},"access":{"type":"string"},"unread":{"type":"array","items":{"type":"string"}},"items":{"type":"array","items":{"type":"object","required":["kind","id","what","url","room"],"properties":{"kind":{"type":"string"},"id":{"type":"string"},"what":{"type":"string"},"url":{"type":"string"},"room":{"type":"string"},"why":{"type":"string"},"issue":{"type":"integer"},"pr":{"type":"integer"},"deadline":{"type":"string"},"at":{"type":"string"},"author":{"type":"string"},"approve":{"type":"boolean"},"action":{"type":"object","required":["kind","label"],"properties":{"kind":{"type":"string"},"label":{"type":"string"},"url":{"type":"string"}}}}}}}}`)

func (s *Server) inboxToolEntry() mcpTool {
	return mcpTool{
		name:        mcpwire.Inbox,
		call:        s.inboxTool,
		description: inboxDescription,
		schema:      inboxInputSchema,
		output:      needsOutputSchema,
	}
}

// inboxTool is inbox (UX1): what is waiting for the developer across their tasks as needs/v1,
// falling back to rooms_list {mine: true} when human.needs.enabled is off or nothing is waiting.
func (s *Server) inboxTool(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
	var a needsArgs
	bad := toolError("invalid_arguments: since is an RFC 3339 instant, as the answer's cursor is, and never in the future; " +
		"room is a room id; repo is owner/name")
	if mcpwire.DecodeArgs(args, &a) != nil {
		return nil, bad
	}
	if (a.Room != "" && !envelope.ValidID(a.Room)) || (a.Repo != "" && !repositoryRE.MatchString(a.Repo)) {
		return nil, bad
	}
	var since time.Time
	if a.Since != "" {
		t, err := time.Parse(time.RFC3339, a.Since)
		if err != nil || t.After(s.now()) {
			s.needsServedTool(ctx, p, needsOutcomeRefused)
			return nil, bad
		}
		since = t
	}

	cursor := s.now().UTC().Truncate(time.Second).Format(time.RFC3339)

	if p.Sponsored || s.Needs {
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
		if p.Sponsored || len(items) > 0 || len(unread) > 0 || a.Room != "" {
			var text strings.Builder
			if err := roomview.Needs(&text, items, unread, s.viewOptions(false)); err != nil {
				return nil, err
			}
			return viewResult{
				text: text.String(),
				data: needsEnvelope{
					APIVersion: needsVersion,
					Items:      items,
					Access:     access,
					Unread:     unread,
					Cursor:     cursor,
				},
			}, nil
		}
	}

	rows, access, err := s.roomRows(ctx, p, &roomFilter{repo: a.Repo, mine: true})
	if err != nil {
		return nil, err
	}
	views := make([]roomview.Row, len(rows))
	for i, r := range rows {
		views[i] = viewRow(r)
	}
	var text strings.Builder
	if err := roomview.Overview(&text, views, s.viewOptions(false)); err != nil {
		return nil, err
	}
	return viewResult{
		text: text.String(),
		data: needsEnvelope{
			APIVersion: needsVersion,
			Items:      []summary.Need{},
			Access:     access,
			Cursor:     cursor,
		},
	}, nil
}
