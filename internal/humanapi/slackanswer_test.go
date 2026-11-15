// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/fanout"
)

func answerPress(team, user, room, text string) SlackAnswer {
	return SlackAnswer{
		Team:      team,
		User:      user,
		Channel:   "C0CC4M8SNVD",
		MessageTS: "1730822400.123456",
		Room:      room,
		Text:      text,
	}
}

func callAnswer(t *testing.T, e env, b SlackAnswer) (int, string) {
	t.Helper()
	rawBody, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	status, raw := postAct(t, e, "/api/slack/answers", bearer("slack-app"), string(rawBody))
	return answerOf(t, status, raw)
}

func slackAnswerRig(t *testing.T, links SlackLinkFinder, gh *decideGitHub, tasks TaskActor, answerOn bool) option {
	t.Helper()
	return func(s *Server, _ *fanout.Hub, _ *hubView) {
		rooms := roomClient(t, slackRoomA(), repoRoom(roomB, "Smana/b"))
		s.Rooms = rooms
		if !answerOn {
			return
		}
		d := &SlackDecider{}
		gh.gate(d)
		s.SlackAnswer = &SlackActor{
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
}

func TestSlackAnswerOffIs404(t *testing.T) {
	e := setup(t, withSponsored(sponsoredP))
	body, err := json.Marshal(answerPress(slackTeam, "U02", roomID, "use task check"))
	if err != nil {
		t.Fatal(err)
	}
	if got, raw := postAct(t, e, "/api/slack/answers", bearer("slack-app"), string(body)); got != http.StatusNotFound {
		t.Fatalf("/api/slack/answers off: %d %q", got, raw)
	}
}

func TestSlackAnswerShapesCheckedBeforeAnyRead(t *testing.T) {
	ff, tasks := newFakeFactoryActs(t, "answered_restarted")
	links := &countingLinks{links: slackLinks(), err: errors.New("store must not be read")}
	gh := slackGitHub()
	e := setup(t, withSponsored(sponsoredP, otherBot), slackAnswerRig(t, links, gh, tasks, true),
		func(s *Server, _ *fanout.Hub, _ *hubView) {
			s.Rooms, s.SlackAnswer.Rooms = brokenRooms{}, brokenRooms{}
		})

	good, err := json.Marshal(answerPress(slackTeam, "U02", roomID, "use task check"))
	if err != nil {
		t.Fatal(err)
	}

	for name, h := range map[string]http.Header{
		"another sponsored principal": bearer("other-bot"),
		"human web session":           header("own"),
	} {
		code, out := postAct(t, e, "/api/slack/answers", h, string(good))
		if code != http.StatusForbidden || strings.Contains(out, `"result"`) {
			t.Fatalf("%s: %d %q, want 403 without result", name, code, out)
		}
	}

	for name, body := range map[string]string{
		"blank text": `{"team":"` + slackTeam + `","user":"U02","channel":"C0CC4M8SNVD",` +
			`"messageTS":"1730822400.123456","room":"` + roomID + `","text":"   "}`,
		"text over 2 KiB": `{"team":"` + slackTeam + `","user":"U02","channel":"C0CC4M8SNVD",` +
			`"messageTS":"1730822400.123456","room":"` + roomID + `","text":"` + strings.Repeat("a", 2049) + `"}`,
		"login field in body": `{"team":"` + slackTeam + `","user":"U02","channel":"C0CC4M8SNVD",` +
			`"messageTS":"1730822400.123456","room":"` + roomID + `","text":"ok","login":"Smana"}`,
	} {
		t.Run(name, func(t *testing.T) {
			status, raw := postAct(t, e, "/api/slack/answers", bearer("slack-app"), body)
			if got, res := answerOf(t, status, raw); got != http.StatusBadRequest || res != "bad_request" {
				t.Fatalf("%d %s, want 400 bad_request", got, res)
			}
		})
	}

	if links.count() != 0 || gh.checked() != 0 || len(ff.recorded()) != 0 {
		t.Fatalf("reads made on malformed calls: links=%d gh=%d factory=%d", links.count(), gh.checked(), len(ff.recorded()))
	}
}

func TestSlackAnswerForwardsOnBehalfOfAndText(t *testing.T) {
	ff, tasks := newFakeFactoryActs(t, "answered_restarted")
	links := &countingLinks{links: slackLinks()}
	gh := slackGitHub()
	e := setup(t, withSponsored(sponsoredP), slackAnswerRig(t, links, gh, tasks, true))

	got, res := callAnswer(t, e, answerPress(slackTeam, "U02", roomID, "use task check"))
	if got != http.StatusOK || res != "answered_restarted" {
		t.Fatalf("%d %s, want 200 answered_restarted", got, res)
	}
	calls := ff.recorded()
	if len(calls) != 1 || calls[0].path != "/v1/tasks/"+roomID+"/answers" || calls[0].onBehalfOf != "slack:"+slackTeam+"/U02" {
		t.Fatalf("factory calls = %+v", calls)
	}

	// Author without repository write is forwarded to the factory, which checks whether they are the issue author.
	ff.mu.Lock()
	ff.result = "answered"
	ff.mu.Unlock()
	if got, res := callAnswer(t, e, answerPress(slackTeam, "U07", roomID, "use task check")); got != http.StatusOK || res != "answered" {
		t.Fatalf("linked non-writer (issue author): %d %s, want 200 answered", got, res)
	}

	// Unlinked user is refused before any factory call.
	if got, res := callAnswer(t, e, answerPress(slackTeam, "U99", roomID, "use task check")); got != http.StatusOK || res != "not_linked" {
		t.Fatalf("unlinked: %d %s, want 200 not_linked", got, res)
	}
	if len(ff.recorded()) != 2 {
		t.Fatalf("unlinked user reached factory: %+v", ff.recorded())
	}
}
