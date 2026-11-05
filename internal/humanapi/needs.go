// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/summary"
)

// needsVersion is the schema the door's answer carries, as summary/v1 names the summary's: one shape for
// the browser, the CLI and (PR 3's tool) the App, so a client reads a version and not a guess (CD21, CD28).
const needsVersion = "needs/v1"

// The needs list's metric labels: bounded, as every label of the set is.
const (
	needsSurfaceAPI = "api" // GET /api/needs, the human token's door (CD28)
	needsOutcomeOK  = "ok"
	// needsOutcomeUnread is a list served with a room whose state could not be read: the items are the
	// readable rooms', and the caller is told so.
	needsOutcomeUnread = "unread"
	// needsOutcomeRefused is a caller or a cursor this route would not read: nothing is listed.
	needsOutcomeRefused = "refused"
	// needsOutcomeError is a list the broker could not read at all: no room was listed.
	needsOutcomeError = "error"
)

// needsEnvelope is GET /api/needs' answer: needs/v1's items across the rooms the caller may read, the
// cursor to ask from next, what a room whose state could not be read was, and why the list may be short
// (access, the X-Rooms-Access header the room list sets).
type needsEnvelope struct {
	APIVersion string         `json:"apiVersion"`
	Items      []summary.Need `json:"items"`
	// Cursor is this answer's instant, truncated to the second: ask with it as `since` for what moved after.
	Cursor string `json:"cursor"`
	Access string `json:"access"`
	// Unread names the rooms whose facts could not be read: they contribute no item, and the list says
	// so in one line rather than passing them off as quiet (Review Focus 1).
	Unread []string `json:"unread,omitempty"`
}

// needsFilter narrows one needs read: to a room, to a repository, or to neither (both).
type needsFilter struct{ room, repo string }

// needsScan is the one scan both doors read (CD21, CD28): every room p may read, its needs from needs/v1,
// and the rooms whose state could not be read. access is X-Rooms-Access' value for the caller: a list may
// be short, and the value says why without naming a room. Its one error is errRoomsUnavailable.
func (s *Server) needsScan(ctx context.Context, p authn.Principal, f needsFilter) (items []summary.Need, unread []string, access string, err error) {
	var rooms v1alpha1.RoomList
	if err := s.Rooms.List(ctx, &rooms, client.InNamespace(s.Namespace)); err != nil {
		return nil, nil, "", errRoomsUnavailable
	}
	actx, acancel := context.WithTimeout(ctx, accessWait)
	defer acancel()
	access = accessOK
	if !s.Groups.IsAdmin(p) && (s.Access == nil || s.Identity == nil) {
		access = accessUnverified
	}
	// As the room list: the gate depends on the repository alone, and a failed check is not cached, so
	// rooms sharing an unreachable one cost one timeout, not one each.
	readable := map[string]bool{}
	items, unread = []summary.Need{}, []string{}
	for i := range rooms.Items {
		room := &rooms.Items[i]
		repo := room.Spec.Repository
		ok, seen := readable[repo]
		if !seen {
			v, err := s.gate(actx, room, p)
			switch {
			case v == unlinked:
				access = accessUnlinked // the hint that helps: no check can pass until they link
			case err != nil:
				s.log().Warn("room access unverified", "repository", repo, "err", err)
				if access == accessOK {
					access = accessUnverified
				}
			}
			ok = v == admitted
			readable[repo] = ok
		}
		if !ok {
			continue
		}
		sub, _ := s.you(room, p, room.Status.Driver)
		if !policy.Allowed(sub, policy.Read) {
			continue
		}
		// Filters run after the gate and the Read check, so one never reveals an unreadable room.
		if f.room != "" && room.Name != f.room {
			continue
		}
		if f.repo != "" && !strings.EqualFold(repo, f.repo) {
			continue
		}
		need, read := s.needsFor(ctx, room, p, sub)
		if !read && room.Status.Task != nil {
			// A room with no task has nothing to read, so it is quiet and not unread; one whose task's
			// facts cannot be read is named, and contributes no item (Review Focus 1).
			unread = append(unread, room.Name)
		}
		items = append(items, need...)
	}
	return items, unread, access, nil
}

// needsRoute is GET /api/needs: the human door onto needs/v1 (CD28), for the browser and the CLI. The
// caller's needs across every room they may read, in the one order (CD21), with a `since` cursor and one
// line naming a room whose state could not be read. It is a human token's route: a sponsored bearer is
// unauthenticated here, as on every route but /mcp (R13, C18), and the App reads through its own tool.
func (s *Server) needsRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok || !s.readAllowed(w, r, p) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()
	q := r.URL.Query()
	// Review Focus 3: a cursor that is not an instant is refused with the format it should have been,
	// never read as everything (a list nobody asked for) or as nothing (a cursor that hides work).
	var since time.Time
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			s.needsServed(ctx, p, needsOutcomeRefused)
			http.Error(w, "since is an RFC 3339 instant, as this answer's cursor is", http.StatusBadRequest)
			return
		}
		since = t
	}
	items, unread, access, err := s.needsScan(ctx, p, needsFilter{room: q.Get("room"), repo: q.Get("repo")})
	if err != nil {
		s.needsServed(ctx, p, needsOutcomeError)
		http.Error(w, err.Error(), roomStatus(err))
		return
	}
	outcome := needsOutcomeOK
	if len(unread) > 0 {
		outcome = needsOutcomeUnread
	}
	s.needsServed(ctx, p, outcome)
	items = newerThan(items, since)
	s.needsItems(items)
	w.Header().Set(accessHeader, access)
	writeJSON(w, needsEnvelope{APIVersion: needsVersion, Items: items, Access: access, Unread: unread,
		Cursor: s.now().UTC().Truncate(time.Second).Format(time.RFC3339)})
}

// newerThan keeps the items whose cause is newer than since. An item without a date is kept: a zero At
// says the broker cannot date it, not that it is old, and dropping it would hide work (Review Focus 3).
func newerThan(items []summary.Need, since time.Time) []summary.Need {
	if since.IsZero() {
		return items
	}
	out := make([]summary.Need, 0, len(items))
	for _, n := range items {
		if n.At.IsZero() || n.At.After(since) {
			out = append(out, n)
		}
	}
	return out
}

// needsServed counts one needs list by surface, principal and outcome (needs_list_total, served as
// maku_needs_list_total): needs/v1's adoption, per caller.
func (s *Server) needsServed(ctx context.Context, p authn.Principal, outcome string) {
	if s.Metrics != nil {
		s.Metrics.NeedsList(ctx, needsSurfaceAPI, p.ID, outcome)
	}
}

// needsItems records what the list just served is made of, by kind (needs_items, served as
// maku_needs_items): the split hole and the merge hole showed here first.
func (s *Server) needsItems(items []summary.Need) {
	if s.Metrics == nil {
		return
	}
	kinds := map[string]int{}
	for _, n := range items {
		kinds[n.Kind]++
	}
	s.Metrics.Needs(kinds)
}
