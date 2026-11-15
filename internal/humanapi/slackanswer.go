// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/ghidentity"
	"github.com/Smana/maku/internal/runrequest"
)

const (
	slackAnswerPath    = "/api/slack/answers"
	slackNotAllowed    = "not_allowed"
	slackAnswerMaxText = 2 << 10
)

var slackAnswerFactoryResults = []string{
	"answered_restarted",
	"answered",
	slackNotLinked,
	slackNotAllowed,
	"no_question",
	"unknown_task",
}

// SlackAnswer is the POST /api/slack/answers body (SI6).
type SlackAnswer struct {
	Team      string `json:"team"`
	User      string `json:"user"`
	Channel   string `json:"channel"`
	MessageTS string `json:"messageTS"`
	Room      string `json:"room"`
	Text      string `json:"text"`
}

func (s *Server) slackAnswer(w http.ResponseWriter, r *http.Request) {
	a := s.SlackAnswer
	p, ok := s.sponsoredPrincipal(r)
	if !ok || !p.Sponsored || p.ID != a.Principal || p.AccessToken == "" {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	in, ok := decodeSlackAnswer(w, r)
	if !ok {
		a.answer(w, slackBadRequest)
		return
	}
	if !a.allow(in.Team + "/" + in.User) {
		a.answer(w, slackRateLimited)
		return
	}
	result := a.resolveAnswer(r.Context(), s, p, in)
	a.answer(w, result)
	a.loggedAnswer(r.Context(), in, result)
}

func decodeSlackAnswer(w http.ResponseWriter, r *http.Request) (SlackAnswer, bool) {
	var in SlackAnswer
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return in, false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, slackDecideBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		return in, false
	}
	return in, slackAnswerShaped(in)
}

func slackAnswerShaped(in SlackAnswer) bool {
	if !slackTeamRE.MatchString(in.Team) || !slackUserRE.MatchString(in.User) ||
		!slackChannelRE.MatchString(in.Channel) || !slackMessageTSRE.MatchString(in.MessageTS) ||
		!envelope.ValidID(in.Room) {
		return false
	}
	return len(in.Text) <= slackAnswerMaxText && strings.TrimSpace(in.Text) != ""
}

func (a *SlackActor) loggedAnswer(ctx context.Context, in SlackAnswer, result string) {
	level := slog.LevelWarn
	if result == "answered_restarted" || result == "answered" {
		level = slog.LevelInfo
	}
	a.log().Log(ctx, level, "slack answer", "team", in.Team, "user", in.User, "room", in.Room, "result", result)
}

func (a *SlackActor) resolveAnswer(ctx context.Context, s *Server, p authn.Principal, in SlackAnswer) string {
	if !p.Sponsored || p.ID != a.Principal || p.AccessToken == "" {
		return slackNotPermitted
	}
	if in.Team != a.TeamID {
		return slackNotPermitted
	}
	if a.Store == nil || a.Tasks == nil {
		return slackUnavailable
	}
	sub, found, err := a.Store.SlackLink(ctx, in.Team, in.User)
	switch {
	case err != nil:
		return a.failed(ctx, "answer", "slack link", err)
	case !found || sub == "":
		return slackNotLinked
	}

	var room *v1alpha1.Room
	if a.Rooms != nil {
		r := &v1alpha1.Room{}
		err := a.Rooms.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: in.Room}, r)
		switch {
		case err == nil:
			room = r
		case apierrors.IsNotFound(err):
			if s != nil {
				closed, cerr := s.closedRoom(ctx, in.Room)
				if cerr != nil {
					return a.failed(ctx, "answer", "closed room", cerr)
				}
				room = closed
			}
		default:
			return a.failed(ctx, "answer", "room", err)
		}
	}
	if room != nil {
		if room.Spec.Repository == "" || a.Identity == nil {
			return slackNotAllowed
		}
		rctx, cancel := context.WithTimeout(ctx, accessWait)
		defer cancel()
		login, err := a.Identity.Login(rctx, sub, room.Spec.Repository)
		switch {
		case errors.Is(err, ghidentity.ErrInactive):
			return slackNotLinked
		case err != nil:
			return slackLinkUnverified
		case login == "":
			return slackNotLinked
		}
	}

	onBehalfOf := "slack:" + in.Team + "/" + in.User
	res, err := a.Tasks.AnswerTask(ctx, in.Room, onBehalfOf, in.Text, p.AccessToken)
	if err != nil {
		var refused runrequest.ErrRefused
		if errors.As(err, &refused) {
			switch refused.Reason {
			case "not_linked":
				return slackNotLinked
			case "link_unverified", "access_unverified":
				return slackLinkUnverified
			case "not_a_maintainer", "repository_not_allowed":
				return slackNotAllowed
			}
		}
		return a.failed(ctx, "answer", "factory", err)
	}
	if !slices.Contains(slackAnswerFactoryResults, res) {
		return a.failed(ctx, "answer", "factory result", errors.New(res))
	}
	return res
}
