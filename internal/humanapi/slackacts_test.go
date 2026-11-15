// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/runrequest"
)

type recordedAct struct {
	method, path, auth, onBehalfOf string
}

type fakeFactoryActs struct {
	mu      sync.Mutex
	calls   []recordedAct
	result  string
	status  int
	errBody string
}

func newFakeFactoryActs(t *testing.T, result string) (*fakeFactoryActs, runrequest.Factory) {
	t.Helper()
	f := &fakeFactoryActs{result: result, status: http.StatusOK}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OnBehalfOf string `json:"onBehalfOf"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.calls = append(f.calls, recordedAct{
			method:     r.Method,
			path:       r.URL.Path,
			auth:       r.Header.Get("Authorization"),
			onBehalfOf: body.OnBehalfOf,
		})
		status, res, errBody := f.status, f.result, f.errBody
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if errBody != "" {
			_, _ = w.Write([]byte(errBody))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"result": res})
	}))
	t.Cleanup(ts.Close)
	return f, runrequest.Factory{URL: ts.URL, HC: ts.Client()}
}

func (f *fakeFactoryActs) recorded() []recordedAct {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedAct, len(f.calls))
	copy(out, f.calls)
	return out
}

type countingLinks struct {
	mu    sync.Mutex
	links map[string]string
	err   error
	reads int
}

func (c *countingLinks) SlackLink(_ context.Context, team, user string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	if c.err != nil {
		return "", false, c.err
	}
	sub, ok := c.links[team+"/"+user]
	return sub, ok, nil
}

func (c *countingLinks) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

func actPress(team, user, room string) SlackAct {
	return SlackAct{
		Team:      team,
		User:      user,
		Channel:   "C0CC4M8SNVD",
		MessageTS: "1730822400.123456",
		Room:      room,
	}
}

func postAct(t *testing.T, e env, path string, h http.Header, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = h.Clone()
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	raw, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(raw)
}

func callAct(t *testing.T, e env, path string, b SlackAct) (int, string) {
	t.Helper()
	rawBody, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	status, raw := postAct(t, e, path, bearer("slack-app"), string(rawBody))
	return answerOf(t, status, raw)
}

func slackActsRig(t *testing.T, links SlackLinkFinder, gh *decideGitHub, tasks TaskActor, stopOn, retryOn bool) option {
	t.Helper()
	return func(s *Server, _ *fanout.Hub, _ *hubView) {
		rooms := roomClient(t, slackRoomA(), repoRoom(roomB, "Smana/b"))
		s.Rooms = rooms
		mk := func() *SlackActor {
			d := &SlackDecider{}
			gh.gate(d)
			return &SlackActor{
				Store:     links,
				Principal: "system:slack-app",
				TeamID:    slackTeam,
				Rooms:     rooms,
				Namespace: namespace,
				Identity:  d.Identity,
				Access:    d.Access,
				Tasks:     tasks,
				Metrics:   s.Metrics,
				Logger:    s.log(),
			}
		}
		if stopOn {
			s.SlackStop = mk()
		}
		if retryOn {
			s.SlackRetry = mk()
		}
	}
}

func TestSlackStopOffIs404(t *testing.T) {
	e := setup(t, withSponsored(sponsoredP))
	body, err := json.Marshal(actPress(slackTeam, "U02", roomID))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/slack/stops", "/api/slack/retries"} {
		if got, raw := postAct(t, e, path, bearer("slack-app"), string(body)); got != http.StatusNotFound {
			t.Fatalf("%s off: %d %q", path, got, raw)
		}
	}

	// Enabling stop alone keeps retry at 404.
	_, tasks := newFakeFactoryActs(t, "stopped")
	links := &countingLinks{links: slackLinks()}
	onlyStop := setup(t, withSponsored(sponsoredP), slackActsRig(t, links, slackGitHub(), tasks, true, false))
	if got, raw := postAct(t, onlyStop, "/api/slack/retries", bearer("slack-app"), string(body)); got != http.StatusNotFound {
		t.Fatalf("retry off while stop on: %d %q", got, raw)
	}
}

func TestSlackStopShapesCheckedBeforeAnyRead(t *testing.T) {
	ff, tasks := newFakeFactoryActs(t, "stopped")
	links := &countingLinks{links: slackLinks(), err: errors.New("store must not be read")}
	gh := slackGitHub()
	e := setup(t, withSponsored(sponsoredP, otherBot), slackActsRig(t, links, gh, tasks, true, false),
		func(s *Server, _ *fanout.Hub, _ *hubView) {
			s.Rooms, s.SlackStop.Rooms = brokenRooms{}, brokenRooms{}
		})

	good, err := json.Marshal(actPress(slackTeam, "U02", roomID))
	if err != nil {
		t.Fatal(err)
	}

	// Non-App callers get 403 before any body or store read.
	for name, h := range map[string]http.Header{
		"another sponsored principal": bearer("other-bot"),
		"human web session":           header("own"),
		"human maku token":            viaRoomctl("own"),
	} {
		code, out := postAct(t, e, "/api/slack/stops", h, string(good))
		if code != http.StatusForbidden || strings.Contains(out, `"result"`) {
			t.Fatalf("%s: %d %q, want 403 without result", name, code, out)
		}
	}

	for name, tc := range map[string]struct {
		ctype string
		body  string
	}{
		"text/plain content-type": {"text/plain", string(good)},
		"5 KiB body":              {"application/json", string(good) + strings.Repeat(" ", 5<<10)},
		"unknown field": {"application/json", `{"team":"` + slackTeam + `","user":"U02","channel":"C0CC4M8SNVD",` +
			`"messageTS":"1730822400.123456","room":"` + roomID + `","extra":"x"}`},
		"trailing json": {"application/json", string(good) + `{}`},
		"bad team": {"application/json", `{"team":"bad","user":"U02","channel":"C0CC4M8SNVD",` +
			`"messageTS":"1730822400.123456","room":"` + roomID + `"}`},
		"bad user": {"application/json", `{"team":"` + slackTeam + `","user":"u02","channel":"C0CC4M8SNVD",` +
			`"messageTS":"1730822400.123456","room":"` + roomID + `"}`},
		"bad channel": {"application/json", `{"team":"` + slackTeam + `","user":"U02","channel":"general",` +
			`"messageTS":"1730822400.123456","room":"` + roomID + `"}`},
		"bad messageTS": {"application/json", `{"team":"` + slackTeam + `","user":"U02","channel":"C0CC4M8SNVD",` +
			`"messageTS":"now","room":"` + roomID + `"}`},
		"bad room": {"application/json", `{"team":"` + slackTeam + `","user":"U02","channel":"C0CC4M8SNVD",` +
			`"messageTS":"1730822400.123456","room":"NOT-A-ROOM"}`},
	} {
		t.Run(name, func(t *testing.T) {
			h := bearer("slack-app")
			h.Set("Content-Type", tc.ctype)
			status, raw := postAct(t, e, "/api/slack/stops", h, tc.body)
			if got, res := answerOf(t, status, raw); got != http.StatusBadRequest || res != "bad_request" {
				t.Fatalf("%d %s, want 400 bad_request", got, res)
			}
		})
	}

	if links.count() != 0 || gh.checked() != 0 || len(ff.recorded()) != 0 {
		t.Fatalf("reads made on malformed calls: links=%d gh=%d factory=%d", links.count(), gh.checked(), len(ff.recorded()))
	}
}

func TestSlackStopForwardsOnBehalfOf(t *testing.T) {
	ff, tasks := newFakeFactoryActs(t, "stopped")
	links := &countingLinks{links: slackLinks()}
	gh := slackGitHub()
	e := setup(t, withSponsored(sponsoredP), slackActsRig(t, links, gh, tasks, true, false))

	got, res := callAct(t, e, "/api/slack/stops", actPress(slackTeam, "U02", roomID))
	if got != http.StatusOK || res != "stopped" {
		t.Fatalf("got %d %s, want 200 stopped", got, res)
	}
	calls := ff.recorded()
	if len(calls) != 1 {
		t.Fatalf("factory calls = %+v, want 1", calls)
	}
	if calls[0].method != http.MethodPost || calls[0].path != "/v1/tasks/"+roomID+"/stops" {
		t.Fatalf("factory target = %s %s, want POST /v1/tasks/%s/stops", calls[0].method, calls[0].path, roomID)
	}
	if calls[0].auth != "Bearer "+sponsoredP.AccessToken {
		t.Fatalf("factory auth = %q, want %q", calls[0].auth, "Bearer "+sponsoredP.AccessToken)
	}
	if calls[0].onBehalfOf != "slack:"+slackTeam+"/U02" {
		t.Fatalf("factory onBehalfOf = %q, want %q", calls[0].onBehalfOf, "slack:"+slackTeam+"/U02")
	}

	// Early broker refusals never reach the factory.
	for name, tc := range map[string]struct {
		in   SlackAct
		want string
	}{
		"peer workspace":       {actPress("T02PEER", "U02", roomID), "not_permitted"},
		"unlinked slack user":  {actPress(slackTeam, "U03", roomID), "not_linked"},
		"unlinked github user": {actPress(slackTeam, "U04", roomID), "not_linked"},
		"zitadel down":         {actPress(slackTeam, "U05", roomID), "link_unverified"},
		"github down":          {actPress(slackTeam, "U06", roomID), "link_unverified"},
		"read-only user":       {actPress(slackTeam, "U07", roomID), "not_maintainer"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out := callAct(t, e, "/api/slack/stops", tc.in)
			if code != http.StatusOK || out != tc.want {
				t.Fatalf("got %d %s, want 200 %s", code, out, tc.want)
			}
		})
	}
	if len(ff.recorded()) != 1 {
		t.Fatalf("early refusals reached factory: %+v", ff.recorded())
	}

	// Factory result codes pass through verbatim.
	for _, code := range []string{"not_maintainer", "not_running", "unknown_task"} {
		ff.mu.Lock()
		ff.result = code
		ff.mu.Unlock()
		status, out := callAct(t, e, "/api/slack/stops", actPress(slackTeam, "U08", roomID))
		if status != http.StatusOK || out != code {
			t.Fatalf("factory %s: got %d %s", code, status, out)
		}
	}
}

func TestSlackStopRateLimited(t *testing.T) {
	ff, tasks := newFakeFactoryActs(t, "stopped")
	links := &countingLinks{links: slackLinks()}
	e := setup(t, withSponsored(sponsoredP), slackActsRig(t, links, slackGitHub(), tasks, true, false))

	for i := range 5 {
		if got, res := callAct(t, e, "/api/slack/stops", actPress(slackTeam, "U02", roomID)); got != http.StatusOK || res != "stopped" {
			t.Fatalf("press %d: %d %s, want 200 stopped", i+1, got, res)
		}
	}
	if got, res := callAct(t, e, "/api/slack/stops", actPress(slackTeam, "U02", roomID)); got != http.StatusTooManyRequests || res != "rate_limited" {
		t.Fatalf("sixth press: %d %s, want 429 rate_limited", got, res)
	}
	if len(ff.recorded()) != 5 {
		t.Fatalf("factory received %d calls, want 5", len(ff.recorded()))
	}
	// Another user's bucket is independent.
	if got, res := callAct(t, e, "/api/slack/stops", actPress(slackTeam, "U08", roomID)); got != http.StatusOK || res != "stopped" {
		t.Fatalf("another user after burst: %d %s, want 200 stopped", got, res)
	}
}

func TestSlackRetryMirrorsStop(t *testing.T) {
	ff, tasks := newFakeFactoryActs(t, "retried")
	links := &countingLinks{links: slackLinks()}
	e := setup(t, withSponsored(sponsoredP), slackActsRig(t, links, slackGitHub(), tasks, false, true))

	// Non-App caller refused with 403.
	good, _ := json.Marshal(actPress(slackTeam, "U02", roomID))
	if code, out := postAct(t, e, "/api/slack/retries", header("own"), string(good)); code != http.StatusForbidden || strings.Contains(out, `"result"`) {
		t.Fatalf("human caller: %d %q, want 403", code, out)
	}

	// Bad shape refused with 400 bad_request.
	bad, _ := json.Marshal(actPress(slackTeam, "U02", "BAD"))
	if status, raw := postAct(t, e, "/api/slack/retries", bearer("slack-app"), string(bad)); status != http.StatusBadRequest {
		t.Fatalf("bad shape: %d %q, want 400", status, raw)
	}

	// Burst of 5 forwards onBehalfOf and App token to /retries; 6th call is 429 rate_limited.
	for i := range 5 {
		got, res := callAct(t, e, "/api/slack/retries", actPress(slackTeam, "U02", roomID))
		if got != http.StatusOK || res != "retried" {
			t.Fatalf("retry %d: %d %s, want 200 retried", i+1, got, res)
		}
	}
	calls := ff.recorded()
	if len(calls) != 5 || calls[0].path != "/v1/tasks/"+roomID+"/retries" ||
		calls[0].auth != "Bearer "+sponsoredP.AccessToken || calls[0].onBehalfOf != "slack:"+slackTeam+"/U02" {
		t.Fatalf("retry calls = %+v", calls)
	}
	if got, res := callAct(t, e, "/api/slack/retries", actPress(slackTeam, "U02", roomID)); got != http.StatusTooManyRequests || res != "rate_limited" {
		t.Fatalf("sixth retry: %d %s, want 429 rate_limited", got, res)
	}

	// Factory not_escalated passes through for another user.
	ff.mu.Lock()
	ff.result = "not_escalated"
	ff.mu.Unlock()
	if got, res := callAct(t, e, "/api/slack/retries", actPress(slackTeam, "U08", roomID)); got != http.StatusOK || res != "not_escalated" {
		t.Fatalf("not_escalated: %d %s, want 200 not_escalated", got, res)
	}
}
