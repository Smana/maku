// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	bconfig "github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/statuswriter"
)

type privateCodeRig struct {
	f   *forgetest.Fake
	cfg *config.Config
	pc  *privateCodeTracker
	log bytes.Buffer
	mu  sync.Mutex
	got []bool
}

func newPrivateCodeRig(t *testing.T, acknowledged bool) *privateCodeRig {
	t.Helper()
	g := &privateCodeRig{f: forgetest.NewFake()}
	r := config.Repository{Name: "Smana/demo"}
	if acknowledged {
		r.PrivateCode = &config.PrivateCode{Acknowledged: true}
	}
	g.cfg = &config.Config{Repositories: []config.Repository{r}}
	g.pc = &privateCodeTracker{Live: func() *config.Config { return g.cfg },
		Readers: func(string) repoInfoReader { return g.f },
		Record: func(_ context.Context, _ string, unacknowledged bool) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.got = append(g.got, unacknowledged)
		}, Log: slog.New(slog.NewTextHandler(&g.log, nil))}
	return g
}

func (g *privateCodeRig) recorded() []bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]bool(nil), g.got...)
}

// v4 D3, step 7 R1: on each fence pass the visibility is read through the factory App. Private,
// internal or unread code without privateCode.acknowledged sets the gauge, the condition's input and
// the banner's code; nothing else stops.
func TestPrivateCodeTracker(t *testing.T) {
	for name, tc := range map[string]struct {
		info   *forge.RepositoryInfo
		err    error
		acked  bool
		vis    string
		unack  bool
		warned bool
	}{
		"public":                {info: &forge.RepositoryInfo{FullName: "Smana/demo", Visibility: "public"}, vis: "public"},
		"private":               {info: &forge.RepositoryInfo{FullName: "Smana/demo", Private: true, Visibility: "private"}, vis: "private", unack: true, warned: true},
		"internal":              {info: &forge.RepositoryInfo{FullName: "Smana/demo", Private: true, Visibility: "internal"}, vis: "internal", unack: true, warned: true},
		"a failed read":         {err: errors.New("forge: repository: 502"), vis: statuswriter.VisibilityUnknown, unack: true, warned: true},
		"private, acknowledged": {info: &forge.RepositoryInfo{FullName: "Smana/demo", Private: true, Visibility: "private"}, acked: true, vis: "private"},
		"unread, acknowledged":  {err: errors.New("forge: repository: 502"), acked: true, vis: statuswriter.VisibilityUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			g := newPrivateCodeRig(t, tc.acked)
			if tc.info != nil {
				g.f.SetRepositoryInfo(*tc.info)
			}
			g.f.FailRepositoryInfo(tc.err)
			g.pc.Observe(t.Context(), "Smana/demo", true)
			if got := g.pc.Visibility("Smana/demo"); got != tc.vis {
				t.Fatalf("visibility %q, want %q", got, tc.vis)
			}
			if got := g.recorded(); len(got) != 1 || got[0] != tc.unack {
				t.Fatalf("recorded %v, want [%v]", got, tc.unack)
			}
			if got := g.pc.InstallUnsafe(); (len(got) == 1 && got[0] == bconfig.UnsafePrivateCodeUnacknowledged) != tc.unack || len(got) > 1 {
				t.Fatalf("banner %v, want %v", got, tc.unack)
			}
			if got := strings.Contains(g.log.String(), config.PrivateCodeSentence); got != tc.warned {
				t.Fatalf("warned %v, want %v:\n%s", got, tc.warned, g.log.String())
			}
		})
	}
}

// IN9: the warning is logged once per change of visibility, not on every pass; a repository the
// config no longer serves leaves the banner.
func TestPrivateCodeWarnsOncePerVisibility(t *testing.T) {
	g := newPrivateCodeRig(t, false)
	g.f.SetRepositoryInfo(forge.RepositoryInfo{FullName: "Smana/demo", Private: true, Visibility: "private"})
	g.pc.Observe(t.Context(), "Smana/demo", true)
	g.pc.Observe(t.Context(), "Smana/demo", true)
	g.f.SetRepositoryInfo(forge.RepositoryInfo{FullName: "Smana/demo", Private: true, Visibility: "internal"})
	g.pc.Observe(t.Context(), "Smana/demo", true)
	if n := strings.Count(g.log.String(), config.PrivateCodeSentence); n != 2 {
		t.Fatalf("warned %d times, want twice:\n%s", n, g.log.String())
	}
	if got := g.recorded(); len(got) != 3 || !got[0] || !got[1] || !got[2] {
		t.Fatalf("recorded %v", got)
	}
	g.cfg = &config.Config{}
	if g.pc.Unacknowledged() {
		t.Fatal("a repository no longer served still sets the banner")
	}
	if g.pc.Visibility("Smana/never") != "" {
		t.Fatal("a repository never read has a visibility")
	}
}

// IN9: the demo keeps requirePublic, stricter than D3: an acknowledged private repository is still
// refused, because the demo's agents can read its secrets.
func TestDemoRefusesPrivateEvenWhenAcknowledged(t *testing.T) {
	cfg := &config.Config{Demo: true, Repositories: []config.Repository{{Name: "Smana/demo", PrivateCode: &config.PrivateCode{Acknowledged: true}}}}
	private := func(context.Context) (bool, error) { return false, nil }
	if err := demoPublic(t.Context(), cfg, "Smana/demo", private); err == nil || !strings.Contains(err.Error(), "public repositories only") {
		t.Fatalf("got %v, want the demo's refusal", err)
	}
	cfg.Demo = false
	if err := demoPublic(t.Context(), cfg, "Smana/demo", private); err != nil {
		t.Fatalf("outside the demo a private repository is served: %v", err)
	}
}
