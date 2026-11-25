// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

type startTable map[string]store.HandoffStart

func (s startTable) GetHandoffStart(_ context.Context, repository string, issue int, _ time.Time) (store.HandoffStart, bool, error) {
	if repository == "Smana/down" {
		return store.HandoffStart{}, false, errors.New("database down")
	}
	h, ok := s[strings.ToLower(repository)+"#"+itoa(issue)]
	return h, ok, nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// The factory alone reads a web start's record, which names the ZITADEL sub, never a login; any
// other system principal is not permitted, an issue the broker filed nothing for is a 404.
func TestHandoffStartIsTheFactorysToRead(t *testing.T) {
	s, _, _ := newServer(t)
	at := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	s.HandoffStarts, s.LinkReader = startTable{"smana/maku#512": {Repository: "smana/maku", Issue: 512, Handoff: "h1", Sub: "zsub1",
		Login: "dev", Labels: []string{"factory/ready"}, StartedAt: at}}, factory
	h := s.Routes()
	rec := call(t, h, http.MethodGet, "/v1/handoff-starts?repository=Smana/maku&issue=512", "sys:"+factory, nil)
	want := `{"handoff":"h1","repository":"smana/maku","issue":512,"sub":"zsub1","labels":["factory/ready"],"startedAt":"2026-11-25T09:00:00Z"}` + "\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("the factory's read: %d %s", rec.Code, rec.Body)
	}
	for name, c := range map[string]struct {
		path, token string
		code        int
		reason      string
	}{
		"another system principal": {"/v1/handoff-starts?repository=Smana/maku&issue=512", "sys:system:runlore", http.StatusForbidden, wire.ReasonNotPermitted},
		"no token":                 {"/v1/handoff-starts?repository=Smana/maku&issue=512", "", http.StatusUnauthorized, wire.ReasonUnauthenticated},
		"another issue":            {"/v1/handoff-starts?repository=Smana/maku&issue=513", "sys:" + factory, http.StatusNotFound, wire.ReasonNoHandoffStart},
		"a malformed repository":   {"/v1/handoff-starts?repository=maku&issue=512", "sys:" + factory, http.StatusBadRequest, wire.ReasonBadHandoffStart},
		"a malformed issue":        {"/v1/handoff-starts?repository=Smana/maku&issue=0", "sys:" + factory, http.StatusBadRequest, wire.ReasonBadHandoffStart},
		"the store down":           {"/v1/handoff-starts?repository=Smana/down&issue=1", "sys:" + factory, http.StatusServiceUnavailable, wire.ReasonLogUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(t, h, http.MethodGet, c.path, c.token, nil)
			if rec.Code != c.code || reason(t, rec) != c.reason {
				t.Fatalf("%d %s, want %d %s", rec.Code, reason(t, rec), c.code, c.reason)
			}
		})
	}
	s.LinkReader = ""
	if rec := call(t, s.Routes(), http.MethodGet, "/v1/handoff-starts?repository=Smana/maku&issue=512", "sys:"+factory, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("off: %d", rec.Code)
	}
}
