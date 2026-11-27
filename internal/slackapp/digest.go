// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/Smana/maku/internal/webhook"
)

const digestWindow = "7d"

// StartDigest starts the background ticker that posts per-channel upkeep scorecards when their
// configured UTC cron schedule matches. Calling StartDigest is a no-op if no channel configures a
// digest schedule. App.Wait stops and drains it.
func (a *App) StartDigest(ctx context.Context) {
	hasDigest := false
	for _, ch := range a.Channels {
		if ch.Upkeep != nil && ch.Upkeep.Digest != nil {
			hasDigest = true
			break
		}
	}
	if !hasDigest {
		return
	}
	a.digestMu.Lock()
	if a.digestStop == nil {
		a.digestStop = make(chan struct{})
	}
	if a.digestClosed {
		a.digestMu.Unlock()
		return
	}
	stopCh := a.digestStop
	a.digestMu.Unlock()

	a.digestWG.Go(func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				a.runDigestTick(ctx)
			}
		}
	})
}

func (a *App) runDigestTick(ctx context.Context) {
	slotMinute := a.now().UTC().Truncate(time.Minute)
	for _, chID := range slices.Sorted(maps.Keys(a.Channels)) {
		ch := a.Channels[chID]
		if ch.Upkeep == nil || ch.Upkeep.Digest == nil {
			continue
		}
		sched, err := cron.ParseStandard(ch.Upkeep.Digest.Cron)
		if err != nil {
			continue
		}
		if !sched.Next(slotMinute.Add(-time.Minute)).UTC().Equal(slotMinute) {
			continue
		}
		dedupKey := chID + "\x00" + slotMinute.Format(time.RFC3339)
		if !a.digestSent.add(dedupKey) {
			continue
		}
		digest, err := a.Broker.RoutineDigest(ctx, ch.Upkeep.Repositories, digestWindow)
		if err != nil {
			a.digestSent.forget(dedupKey)
			a.log().Warn("routine digest fetch failed", "channel", chID, "err", err)
			continue
		}
		text, ok := formatDigestMessage(digest, ch.Upkeep)
		if !ok {
			continue
		}
		if _, err := a.Slack.Post(ctx, chID, "", text, nil); err != nil {
			a.digestSent.forget(dedupKey)
			a.log().Warn("routine digest post failed", "channel", chID, "err", err)
		}
	}
}

func routineEntryActive(e RoutineDigestEntry) bool {
	return e.Runs > 0 || e.NoOps > 0 || e.PRsOpened > 0 || e.Merged > 0 || e.Closed > 0 || e.Stale > 0 || e.Escalated > 0
}

func formatDigestMessage(digest RoutineDigest, up *UpkeepChannel) (string, bool) {
	if up == nil {
		return "", false
	}
	var lines []string
	anyActive := false
	for _, repo := range digest.Repositories {
		if !servesRepo(up.Repositories, repo.Repository) {
			continue
		}
		for _, e := range repo.Routines {
			if len(up.Routines) > 0 && !slices.Contains(up.Routines, e.Name) {
				continue
			}
			if !routineEntryActive(e) {
				continue
			}
			anyActive = true
			denom := e.Merged + e.Closed + e.Stale
			mergePart := "0/0 merged"
			if e.MergeRate != nil {
				mergePart = fmt.Sprintf("%d/%d merged (%.0f%%)", e.Merged, denom, *e.MergeRate*100)
			}
			pausedPart := ""
			if e.Paused {
				if e.BreakerReason != "" {
					pausedPart = ", paused (" + webhook.Clip("reason", e.BreakerReason) + ")"
				} else {
					pausedPart = ", paused"
				}
			}
			lines = append(lines, fmt.Sprintf("• %s [%s]: %d runs, %s, $%.2f%s",
				slackText(repo.Repository), e.Name, e.Runs, mergePart, e.CostUSD, pausedPart))
		}
	}
	if !anyActive {
		return "", false
	}
	win := cmp.Or(digest.Window, digestWindow)
	all := make([]string, 0, len(lines)+1)
	all = append(all, fmt.Sprintf("Weekly upkeep scorecard (%s):", win))
	all = append(all, lines...)
	return capLines(all), true
}
