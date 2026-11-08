// SPDX-License-Identifier: Apache-2.0

package evidence

import (
	"cmp"
	"context"
	"sync"
	"time"
)

// Refresher keeps the leader's evidence warm: every Every, it reads each owned repository whose
// evidence is due (Service.due), so a page load finds it read. A follower's own reports start
// their reads when they find the evidence missing.
type Refresher struct {
	Service *Service
	// Owned is v2 D7's fence; nil owns nothing.
	Owned func(repository string) bool
	// Every is the tick, a minute when 0.
	Every time.Duration
	// OnAge records a repository's evidence age after each tick; nil records nothing.
	OnAge func(ctx context.Context, repository string, age time.Duration)
}

// NeedLeaderElection is true: one replica's reads are enough to bound GitHub.
func (r *Refresher) NeedLeaderElection() bool { return true }

// Start ticks until ctx ends.
func (r *Refresher) Start(ctx context.Context) error {
	t := time.NewTicker(cmp.Or(r.Every, time.Minute))
	defer t.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick reads the due owned repositories at once, each under the Service's deadline, then records
// their ages. A failure is the Service's to log.
func (r *Refresher) Tick(ctx context.Context) {
	var owned []string
	for _, rc := range r.Service.Cfg.Repos() {
		if r.Owned != nil && r.Owned(rc.Name) {
			owned = append(owned, rc.Name)
		}
	}
	var wg sync.WaitGroup
	for _, name := range owned {
		if r.Service.due(name) {
			wg.Go(func() { _ = r.Service.Refresh(ctx, name) })
		}
	}
	wg.Wait()
	if r.OnAge == nil {
		return
	}
	now := r.Service.Now()
	for _, name := range owned {
		if at, ok := r.Service.evidenceAt(name); ok {
			r.OnAge(ctx, name, now.Sub(at))
		}
	}
}
