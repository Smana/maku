// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/github"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/verdictpost"
)

// githubAPIURL is shared with the factory (factory.go); the poster keeps its own timeout.
const (
	posterGitHubTimeout = 20 * time.Second
	posterEvery         = 15 * time.Second
)

// addVerdictPoster adds the leader's verdict poster (SP2 design §3): agents'
// verdicts to their pull request, as SP3's factory App (P28), whose key is
// mounted at dir (ROOMS_GITHUB_APP_DIR). Unset, no poster runs.
func addVerdictPoster(dir string, add func(manager.Runnable) error, roomLog verdictpost.Log, rooms client.Reader,
	ns, publicURL string, m *metrics.Set, log *slog.Logger,
) error {
	if dir == "" {
		return nil
	}
	p := &verdictpost.Poster{Log: roomLog, PublicURL: publicURL, Now: time.Now, Logger: log,
		GitHub:    &github.App{Dir: dir, API: githubAPIURL(), HC: httpx.New(posterGitHubTimeout, nil), Now: time.Now},
		DataClass: roomDataClass(rooms, ns), OnResult: verdictResults(m)}
	if sh, ok := roomLog.(roomScreenshots); ok {
		p.Screenshots = screenshotCount(sh)
	}
	return add(posterLoop(p, log, nil))
}

// posterLoop runs p every posterEvery on the leader, and gives p the loop's
// lease flag as Leading (ruling SY). tick paces it; nil means a time.Ticker.
func posterLoop(p *verdictpost.Poster, log *slog.Logger, tick func(time.Duration) (<-chan time.Time, func())) *leaderLoop {
	var leading atomic.Bool
	p.Leading = leading.Load
	return &leaderLoop{every: posterEvery, active: &leading, ticker: tick, run: func(ctx context.Context) {
		if err := p.Once(ctx); err != nil {
			log.Error("verdict poster", "err", err)
		}
	}}
}

// roomScreenshots lists a room's screenshots; the broker's store implements it.
type roomScreenshots interface {
	Screenshots(ctx context.Context, room string) ([]store.ScreenshotMeta, error)
}

// screenshotCount counts a room's browser screenshots for the verdict's gallery link; a store error
// counts none, so the verdict still posts.
func screenshotCount(sh roomScreenshots) func(ctx context.Context, room string) int {
	return func(ctx context.Context, room string) int {
		list, err := sh.Screenshots(ctx, room)
		if err != nil {
			return 0
		}
		n := 0
		for _, m := range list {
			if m.Source == store.SourceBrowser {
				n++
			}
		}
		return n
	}
}

// roomDataClass reads a Room's data class; a Room that cannot be read is never
// treated as public.
func roomDataClass(rooms client.Reader, ns string) func(ctx context.Context, room string) string {
	return func(ctx context.Context, room string) string {
		var r v1alpha1.Room
		if err := rooms.Get(ctx, client.ObjectKey{Namespace: ns, Name: room}, &r); err != nil {
			return ""
		}
		return r.Spec.DataClass
	}
}

// verdictResults counts rooms_verdict_posts_total{result}.
func verdictResults(m *metrics.Set) func(ctx context.Context, result string) {
	return func(ctx context.Context, result string) {
		m.VerdictPosts.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	}
}
