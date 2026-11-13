// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/Smana/maku/internal/factory/api"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/statuswriter"
)

// repoInfoReader reads a repository's document through its factory App; *forge.GitHub.
type repoInfoReader interface {
	RepositoryInfo(ctx context.Context) (forge.RepositoryInfo, error)
}

// privateCodeTracker is v4 D3 in this release (step 7 R1): on each fence pass it reads every
// served repository's visibility through the factory App. Private, internal or unread code whose
// Repository has no spec.privateCode.acknowledged sets the gauge, the Repository's PrivateCode
// condition (through Visibility) and the install banner's code (through Unacknowledged), and logs
// one warning per change of visibility. It refuses nothing: the work continues. Safe for
// concurrent use.
type privateCodeTracker struct {
	Live func() *config.Config
	// Readers is repository's factory connection; nil reads nothing, which counts as private.
	Readers func(repository string) repoInfoReader
	// Record is the gauge (fmetrics.Set.PrivateCodeUnacknowledged).
	Record func(ctx context.Context, repository string, unacknowledged bool)
	Log    *slog.Logger

	mu  sync.Mutex
	vis map[string]string // lowercased repository → its visibility as last read
}

// Observe is the fence's OnState: one read of repository's visibility, whoever owns it.
func (p *privateCodeTracker) Observe(ctx context.Context, repository string, _ bool) {
	vis := statuswriter.VisibilityUnknown
	if rr := p.Readers(repository); rr != nil {
		switch info, err := rr.RepositoryInfo(ctx); {
		case err != nil:
			p.Log.Warn("the repository's visibility is unreadable: it counts as private code", "repository", repository, "err", err)
		case info.PrivateCode() && info.Visibility == "public": // a private flag beside a public visibility
			vis = "private"
		default:
			vis = info.Visibility
		}
	}
	unacknowledged := vis != "public" && !p.Live().RepositoryFor(repository).PrivateCodeAcknowledged()
	p.Record(ctx, repository, unacknowledged)
	key := strings.ToLower(repository)
	p.mu.Lock()
	if p.vis == nil {
		p.vis = map[string]string{}
	}
	was, seen := p.vis[key]
	p.vis[key] = vis
	p.mu.Unlock()
	if unacknowledged && (!seen || was != vis) {
		p.Log.Warn("private code is served without an acknowledgement; it is refused from the next release: "+config.PrivateCodeSentence,
			"repository", repository, "visibility", vis)
	}
}

// Visibility is repository's visibility as last read, "" before the first read: the status
// writer's input.
func (p *privateCodeTracker) Visibility(repository string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.vis[strings.ToLower(repository)]
}

// Unacknowledged reports whether any repository the config serves now is private code without an
// acknowledgement, as last read: the banner's private_code_unacknowledged.
func (p *privateCodeTracker) Unacknowledged() bool {
	cfg := p.Live()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range cfg.Repos() {
		if vis, ok := p.vis[strings.ToLower(r.Name)]; ok && vis != "public" && !r.PrivateCodeAcknowledged() {
			return true
		}
	}
	return false
}

// InstallUnsafe is the factory's banner codes (GET /v1/install): private_code_unacknowledged while
// Unacknowledged holds.
func (p *privateCodeTracker) InstallUnsafe() []string {
	if p.Unacknowledged() {
		return []string{api.InstallPrivateCodeUnacknowledged}
	}
	return nil
}

// demoPublic is the demo's start check on a repository (requirePublic), stricter than D3: the
// demo's agents can read the model key and the GitHub token, so an acknowledged private repository
// is refused all the same. Outside the demo it checks nothing.
func demoPublic(ctx context.Context, cfg *config.Config, repository string, public func(context.Context) (bool, error)) error {
	if !cfg.Demo {
		return nil
	}
	return requirePublic(ctx, []string{repository}, func(ctx context.Context, _ string) (bool, error) { return public(ctx) })
}
