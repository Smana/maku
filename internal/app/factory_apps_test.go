// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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

// The factory App is checked against its own set, the merger's folded in when the config folds it.
func TestFactoryAppNeedFollowsTheMerger(t *testing.T) {
	folded := &config.Config{GitHub: config.GitHub{AppIDFile: "/f/app_id", MergerAppIDFile: "/f/app_id"}}
	if got := factoryAppNeed(folded); got["contents"] != "write" {
		t.Errorf("folded: %v, want the merger's contents: write", got)
	}
	separate := &config.Config{GitHub: config.GitHub{AppIDFile: "/f/app_id", MergerAppIDFile: "/m/app_id"}}
	if got := factoryAppNeed(separate); got["contents"] != "read" {
		t.Errorf("separate: %v, want the factory's own contents: read", got)
	}
}
