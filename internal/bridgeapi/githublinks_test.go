// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

type githubTable map[string]store.GitHubLink

func (g githubTable) GitHubLinkOf(_ context.Context, sub string) (store.GitHubLink, bool, error) {
	if sub == "down" {
		return store.GitHubLink{}, false, errors.New("database down")
	}
	l, ok := g[sub]
	return l, ok, nil
}

// The factory alone reads a sub's GitHub link: the numeric id, when its human was last seen, and
// whether that is inside the broker's activeWindow, so one window rules both components. Another
// system principal is not permitted, an unlinked sub a 404, a malformed sub a 400, a store failure a
// 503.
func TestGitHubLinkIsTheFactorysToRead(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s, _, _ := newServer(t)
	s.GitHubLinks, s.LinkReader, s.GitHubActiveWindow = githubTable{
		"zsub1": {Sub: "zsub1", GitHubID: 4242, LastSeenAt: now.Add(-time.Hour)},
		"stale": {Sub: "stale", GitHubID: 7, LastSeenAt: now.Add(-200 * time.Hour)}}, factory, 168*time.Hour
	s.Now = func() time.Time { return now }
	h := s.Routes()
	for sub, want := range map[string]githubLinkReply{
		"zsub1": {GitHubID: 4242, LastSeenAt: now.Add(-time.Hour), Active: true},
		"stale": {GitHubID: 7, LastSeenAt: now.Add(-200 * time.Hour), Active: false},
	} {
		rec := call(t, h, http.MethodGet, "/v1/links/github?sub="+sub, "sys:"+factory, nil)
		var got githubLinkReply
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || !got.LastSeenAt.Equal(want.LastSeenAt) ||
			got.GitHubID != want.GitHubID || got.Active != want.Active {
			t.Fatalf("%s: %d %s", sub, rec.Code, rec.Body)
		}
	}
	for name, c := range map[string]struct {
		path, token string
		code        int
		reason      string
	}{
		"another system principal": {"/v1/links/github?sub=zsub1", "sys:system:runlore", http.StatusForbidden, wire.ReasonNotPermitted},
		"no token":                 {"/v1/links/github?sub=zsub1", "", http.StatusUnauthorized, wire.ReasonUnauthenticated},
		"not linked":               {"/v1/links/github?sub=nobody", "sys:" + factory, http.StatusNotFound, wire.ReasonNotLinked},
		"an empty sub":             {"/v1/links/github?sub=", "sys:" + factory, http.StatusBadRequest, wire.ReasonBadSub},
		"a sub over 128 bytes":     {"/v1/links/github?sub=" + strings.Repeat("a", 129), "sys:" + factory, http.StatusBadRequest, wire.ReasonBadSub},
		"the store down":           {"/v1/links/github?sub=down", "sys:" + factory, http.StatusServiceUnavailable, wire.ReasonLogUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(t, h, http.MethodGet, c.path, c.token, nil)
			if rec.Code != c.code || reason(t, rec) != c.reason {
				t.Fatalf("%d %s, want %d %s", rec.Code, reason(t, rec), c.code, c.reason)
			}
		})
	}
	s.LinkReader = ""
	if rec := call(t, s.Routes(), http.MethodGet, "/v1/links/github?sub=zsub1", "sys:"+factory, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("off: %d", rec.Code)
	}
}
