// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/githubapps"
)

type fakeGrants struct {
	missing map[string]minter.Permissions
	err     error
	calls   int
}

func (f *fakeGrants) CheckGranted(_ context.Context, _ []string) (map[string]minter.Permissions, error) {
	f.calls++
	return f.missing, f.err
}

type gaugeCall struct {
	app, permission string
	missing         bool
}

// D12: an installation lacking a permission of its App's required set is logged once, naming the
// repositories, and set on the gauge; every other required permission reads 0.
func TestGrantedCheckLogsOnceAndSetsTheGauge(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	chk := &fakeGrants{missing: map[string]minter.Permissions{
		"o/a": {"issues": "write"},
		"o/b": {"issues": "write"},
		"o/c": {},
	}}
	var got []gaugeCall
	gauge := func(_ context.Context, app, permission string, missing bool) {
		got = append(got, gaugeCall{app, permission, missing})
	}
	need := githubapps.Agents()
	checkAppGrants(t.Context(), "agents", need, chk, []string{"o/a", "o/b", "o/c"}, gauge, log)
	if n := strings.Count(buf.String(), "permission=issues"); n != 1 {
		t.Fatalf("issues logged %d times, want once:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "o/a") || !strings.Contains(buf.String(), "o/b") {
		t.Errorf("the log names neither repository:\n%s", buf.String())
	}
	if len(got) != len(need) {
		t.Fatalf("gauge set %d times, want once per required permission (%d): %v", len(got), len(need), got)
	}
	for _, g := range got {
		if g.app != "agents" || g.missing != (g.permission == "issues") {
			t.Errorf("gauge %+v", g)
		}
	}
}

// An installation that cannot be read sets no gauge: 0 would claim a grant nobody read.
func TestGrantedCheckUnreadSetsNothing(t *testing.T) {
	var buf bytes.Buffer
	chk := &fakeGrants{missing: map[string]minter.Permissions{}, err: errors.New("o/a: status 404")}
	called := false
	checkAppGrants(t.Context(), "factory", githubapps.Factory(githubapps.MergerOptions{}), chk, []string{"o/a"},
		func(context.Context, string, string, bool) { called = true }, slog.New(slog.NewTextHandler(&buf, nil)))
	if called {
		t.Error("an unread installation set the gauge")
	}
	if !strings.Contains(buf.String(), "status 404") {
		t.Errorf("the read error is not logged:\n%s", buf.String())
	}
}

// Review of #510: a partial read records the missing permissions it saw and no 0, since a 0 would
// claim a grant on a repository nobody read.
func TestGrantedCheckPartialReadRecordsOnlyWhatWasRead(t *testing.T) {
	chk := &fakeGrants{missing: map[string]minter.Permissions{"o/a": {"issues": "write"}}, err: errors.New("o/b: status 404")}
	var got []gaugeCall
	checkAppGrants(t.Context(), "agents", githubapps.Agents(), chk, []string{"o/a", "o/b"},
		func(_ context.Context, app, permission string, missing bool) {
			got = append(got, gaugeCall{app, permission, missing})
		}, slog.New(slog.DiscardHandler))
	if len(got) != 1 || got[0] != (gaugeCall{"agents", "issues", true}) {
		t.Fatalf("partial read recorded %v, want only issues missing", got)
	}
}

// The factory App is checked against its own set, the merger's folded in when the merger is it.
func TestFactoryAppNeedFollowsTheMerger(t *testing.T) {
	cfg := &config.Config{GitHub: config.GitHub{AppIDFile: "/f/app_id", MergerAppIDFile: "/m/app_id"}}
	if got := factoryAppNeed(cfg, true); got["contents"] != "write" {
		t.Errorf("folded: %v, want the merger's contents: write", got)
	}
	if got := factoryAppNeed(cfg, false); got["contents"] != "read" {
		t.Errorf("separate: %v, want the factory's own contents: read", got)
	}
}

// Review of #510: the two-App invariants hold on App ids, not on file paths. A Secret named twice
// mounts one App under two paths.
func TestAppIdentities(t *testing.T) {
	ids := func(m map[string]int64) func(string) (int64, error) {
		return func(f string) (int64, error) {
			if id, ok := m[f]; ok {
				return id, nil
			}
			return 0, os.ErrNotExist
		}
	}
	repo := func(merger string) config.Repository {
		return config.Repository{Name: "o/r", Merge: config.Merge{MergerLogin: merger}}
	}
	base := func() *config.Config {
		return &config.Config{FactoryLogin: "f[bot]", AgentsLogin: "a[bot]",
			GitHub:       config.GitHub{AppIDFile: "/f/app_id", MergerAppIDFile: "/m/app_id", AgentsAppIDFile: "/a/app_id", AgentsKeyFile: "/a/private_key"},
			RunTokens:    config.RunTokens{Enabled: true, PerRun: true},
			Repositories: []config.Repository{repo("m[bot]")}}
	}
	for name, tc := range map[string]struct {
		cfg    func() *config.Config
		ids    map[string]int64
		folded bool
		want   string
	}{
		"three Apps":       {base, map[string]int64{"/f/app_id": 1, "/m/app_id": 2, "/a/app_id": 3}, false, ""},
		"agents=factory":   {base, map[string]int64{"/f/app_id": 1, "/m/app_id": 2, "/a/app_id": 1}, false, "the agents App is the factory App (app id 1)"},
		"agents=merger":    {base, map[string]int64{"/f/app_id": 1, "/m/app_id": 2, "/a/app_id": 2}, false, "the agents App is the merger App (app id 2)"},
		"merger=factory":   {base, map[string]int64{"/f/app_id": 1, "/m/app_id": 1, "/a/app_id": 3}, true, "merge.mergerLogin must be factoryLogin when the merger signs with the factory App"},
		"merger unmounted": {base, map[string]int64{"/f/app_id": 1, "/a/app_id": 3}, false, ""},
		"folded by id, login right": {func() *config.Config {
			c := base()
			c.Repositories = []config.Repository{repo("F[bot]")}
			return c
		}, map[string]int64{"/f/app_id": 1, "/m/app_id": 1, "/a/app_id": 3}, true, ""},
		"the demo shares one App": {func() *config.Config {
			c := base()
			c.Demo, c.Repositories = true, []config.Repository{repo("f[bot]")}
			return c
		}, map[string]int64{"/f/app_id": 1, "/m/app_id": 1, "/a/app_id": 1}, true, ""},
		"the agents id unreadable": {base, map[string]int64{"/f/app_id": 1, "/m/app_id": 2}, false, "the agents App's id"},
		"step 2's shared writer keeps starting (D25)": {func() *config.Config {
			c := base()
			c.RunTokens.PerRun = false
			return c
		}, map[string]int64{"/f/app_id": 1, "/m/app_id": 2, "/a/app_id": 1}, false, ""},
	} {
		folded, err := appIdentities(tc.cfg(), ids(tc.ids))
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		case tc.want == "" && folded != tc.folded:
			t.Errorf("%s: folded %v, want %v", name, folded, tc.folded)
		}
	}
}
