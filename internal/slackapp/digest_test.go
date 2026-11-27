// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDigestSchedulerSlotDedupAndZeroActivitySkip(t *testing.T) {
	now := time.Date(2026, 11, 23, 9, 0, 15, 0, time.UTC) // Monday 09:00:15 UTC
	rig := upkeepRig(t, &UpkeepChannel{
		Repositories: []string{"Smana/ai-native-ref"},
		Routines:     []string{"dead-links", "doc-drift"},
		Digest:       &DigestSchedule{Cron: "0 9 * * 1"},
	})
	rig.now = now
	mr := 0.75
	upm := 0.50
	rig.broker.digest = RoutineDigest{
		Window: "7d",
		Repositories: []RoutineDigestRepo{{
			Repository: "Smana/ai-native-ref",
			Routines: []RoutineDigestEntry{
				{Name: "dead-links", Runs: 4, PRsOpened: 4, Merged: 3, Closed: 1, MergeRate: &mr, CostUSD: 1.50, USDPerMerged: &upm},
				{Name: "doc-drift", Runs: 0},
				{Name: "excluded-routine", Runs: 5, Merged: 5},
			},
		}},
	}

	// Non-matching minute posts nothing.
	rig.now = now.Add(-time.Minute)
	rig.app.runDigestTick(t.Context())
	if len(rig.posts()) != 0 {
		t.Fatalf("non-matching minute posted: %+v", rig.posts())
	}

	// Matching minute fires once; second 30s tick in the same minute is deduplicated.
	rig.now = now
	rig.app.runDigestTick(t.Context())
	rig.now = now.Add(30 * time.Second)
	rig.app.runDigestTick(t.Context())
	posts := rig.posts()
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(posts))
	}
	text := posts[0].text
	if !strings.Contains(text, "Smana/ai-native-ref [dead-links]: 4 runs, 3/4 merged (75%), $1.50") {
		t.Fatalf("unexpected scorecard: %q", text)
	}
	if strings.Contains(text, "excluded-routine") {
		t.Fatalf("scorecard leaked unsubscribed routine: %q", text)
	}
	if lines := strings.Split(text, "\n"); len(lines) > 12 {
		t.Fatalf("scorecard exceeds 12 lines (%d): %q", len(lines), text)
	}

	// Next Monday with zero activity skips posting.
	rig.now = now.Add(7 * 24 * time.Hour)
	rig.broker.digest = RoutineDigest{
		Window: "7d",
		Repositories: []RoutineDigestRepo{{
			Repository: "Smana/ai-native-ref",
			Routines:   []RoutineDigestEntry{{Name: "dead-links", Runs: 0}},
		}},
	}
	rig.app.runDigestTick(t.Context())
	if len(rig.posts()) != 1 {
		t.Fatalf("zero activity digest should not post: %+v", rig.posts())
	}
}

func TestDigestLineCapAt12(t *testing.T) {
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	rig := upkeepRig(t, &UpkeepChannel{
		Repositories: []string{"Smana/ai-native-ref"},
		Digest:       &DigestSchedule{Cron: "0 9 * * 1"},
	})
	rig.now = now
	var entries []RoutineDigestEntry
	for i := range 15 {
		entries = append(entries, RoutineDigestEntry{
			Name:    "routine-" + string(rune('a'+i)),
			Runs:    i + 1,
			CostUSD: 0.25,
		})
	}
	rig.broker.digest = RoutineDigest{
		Window: "7d",
		Repositories: []RoutineDigestRepo{{
			Repository: "Smana/ai-native-ref",
			Routines:   entries,
		}},
	}
	rig.app.runDigestTick(t.Context())
	posts := rig.posts()
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(posts))
	}
	if lines := strings.Split(posts[0].text, "\n"); len(lines) > 12 {
		t.Fatalf("lines = %d > 12: %q", len(lines), posts[0].text)
	}
}

func TestStartDigestDrainsOnWait(t *testing.T) {
	rig := upkeepRig(t, &UpkeepChannel{
		Repositories: []string{"Smana/ai-native-ref"},
		Digest:       &DigestSchedule{Cron: "0 9 * * 1"},
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rig.app.StartDigest(ctx)
	wctx, wcancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer wcancel()
	if err := rig.app.Wait(wctx); err != nil {
		t.Fatalf("App.Wait failed to drain StartDigest: %v", err)
	}
}
