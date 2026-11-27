// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	fconfig "github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/summary"
	"github.com/Smana/maku/internal/webhook"
)

func (a *App) upkeepChannels(repo, routine, eventType string) []string {
	if !repoRE.MatchString(repo) || !fconfig.ValidRoutineName(routine) {
		return nil
	}
	var out []string
	for _, chID := range slices.Sorted(maps.Keys(a.Channels)) {
		ch := a.Channels[chID]
		if ch.Upkeep == nil {
			continue
		}
		if !servesRepo(ch.Upkeep.Repositories, repo) {
			continue
		}
		if len(ch.Upkeep.Routines) > 0 && !slices.Contains(ch.Upkeep.Routines, routine) {
			continue
		}
		if !ch.Upkeep.notifies(eventType) {
			continue
		}
		out = append(out, chID)
	}
	return out
}

func (a *App) deliverUpkeep(ctx context.Context, e webhook.Event) string {
	switch e.Type {
	case "room.task_received", "room.task_triaged":
		return "ignored"
	case "room.ended":
		if e.Reason == "no_op" || !isEscalatedEnd(e.State) {
			return "ignored"
		}
	case "room.pr_opened", "room.waiting_for_human":
	default:
		return "ignored"
	}

	channels := a.upkeepChannels(e.Repo, e.Routine, e.Type)
	if len(channels) == 0 {
		return "ignored"
	}

	sum := a.taskSummary(ctx, e.Room)
	var meta *Metadata
	if payload, err := metaPayload(MarkerPayload{Repo: e.Repo, Room: e.Room}); err == nil {
		meta = &Metadata{EventType: filedEventType, EventPayload: payload}
	}

	var firstCh, firstTS string
	for _, chID := range channels {
		ch := a.Channels[chID]
		text, blocks := a.upkeepRootMessage(e, ch, sum)
		var (
			ts  string
			err error
		)
		if len(blocks) > 0 {
			ts, err = a.Slack.PostCard(ctx, chID, "", text, blocks, meta)
		} else {
			ts, err = a.Slack.Post(ctx, chID, "", text, meta)
		}
		if err != nil {
			a.log().Warn("upkeep slack post failed", "channel", chID, "room", e.Room, "err", err)
			if firstTS == "" {
				return "error"
			}
			continue
		}
		if firstTS == "" {
			firstCh, firstTS = chID, ts
		}
	}
	if firstTS == "" {
		return "error"
	}
	th := Thread{
		Channel: firstCh,
		TS:      firstTS,
		At:      a.now(),
	}
	if e.Type == "room.ended" {
		th.EndTS = firstTS
	}
	if err := a.Threads.Put(ctx, e.Room, th); err != nil {
		a.log().Warn("thread map put failed", "room", e.Room, "err", err)
	}
	return "posted"
}

func (a *App) upkeepRootMessage(e webhook.Event, ch Channel, sum summary.Summary) (string, json.RawMessage) {
	roomURL := strings.TrimSuffix(a.RoomsURL, "/") + "/r/" + e.Room
	repo := slackText(e.Repo)
	switch e.Type {
	case "room.pr_opened":
		pr := e.PR
		if pr <= 0 && sum.Status.PR != nil {
			pr = sum.Status.PR.Number
		}
		prPart := fmt.Sprintf("#%d", pr)
		if sum.Status.PR != nil && sum.Status.PR.URL != "" {
			if link := brokerLink(sum.Status.PR.URL, prPart); link != "" {
				prPart = link
			}
		}
		return fmt.Sprintf("[%s] %s — Pull request %s opened · Room: %s", e.Routine, repo, prPart, roomURL), nil
	case "room.waiting_for_human":
		reason := webhook.Clip("reason", e.Reason)
		text := fmt.Sprintf("[%s] %s — Needs you (%s) · Room: %s", e.Routine, repo, reason, roomURL)
		return text, actLineBlocks(e, ch, text)
	case "room.ended":
		text := fmt.Sprintf("[%s] %s — %s · Room: %s", e.Routine, repo, endLine(e, sum), roomURL)
		return text, actLineBlocks(e, ch, text, sum)
	default:
		return "", nil
	}
}

func (a *App) deliverRoutinePaused(ctx context.Context, e webhook.Event) string {
	channels := a.upkeepChannels(e.Repo, e.Routine, "routine.paused")
	if len(channels) == 0 {
		return "ignored"
	}
	reason := webhook.Clip("reason", e.Reason)
	promptURL := "https://github.com/" + e.Repo + "/edit/main/.github/agent-factory/upkeep/" + url.PathEscape(e.Routine) + ".md"
	text := fmt.Sprintf("[%s] %s — Routine paused (%s). Tune prompt: <%s|.github/agent-factory/upkeep/%s.md>",
		e.Routine, slackText(e.Repo), reason, promptURL, e.Routine)
	for _, chID := range channels {
		if _, err := a.Slack.Post(ctx, chID, "", text, nil); err != nil {
			a.log().Warn("routine paused slack post failed", "channel", chID, "routine", e.Routine, "err", err)
			return "error"
		}
	}
	return "posted"
}
