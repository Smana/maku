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
	"sync"
	"time"

	"golang.org/x/time/rate"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/ghidentity"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/repoaccess"
	"github.com/Smana/maku/internal/runrequest"
)

const (
	slackStopPath      = "/api/slack/stops"
	slackRetryPath     = "/api/slack/retries"
	slackNotMaintainer = "not_maintainer"
)

var slackActFactoryResults = []string{
	"stopped",
	"retried",
	slackNotLinked,
	slackNotMaintainer,
	"not_running",
	"not_escalated",
	"unknown_task",
}

// TaskActor reaches the factory's stop, retry and answer routes on behalf of a linked Slack user.
type TaskActor interface {
	StopTask(ctx context.Context, task, onBehalfOf, accessToken string) (string, error)
	RetryTask(ctx context.Context, task, onBehalfOf, accessToken string) (string, error)
	AnswerTask(ctx context.Context, task, onBehalfOf, text, accessToken string) (string, error)
}

// SlackAct is the POST /api/slack/stops and POST /api/slack/retries body (SI6).
type SlackAct struct {
	Team      string `json:"team"`
	User      string `json:"user"`
	Channel   string `json:"channel"`
	MessageTS string `json:"messageTS"`
	Room      string `json:"room"`
}

// SlackActor is human.slackStop or human.slackRetry: modelled on SlackDecider (SI6).
type SlackActor struct {
	Store     SlackLinkFinder
	Principal string // "system:slack-app"
	TeamID    string
	Rooms     client.Reader
	Namespace string
	Identity  *ghidentity.Resolver
	Access    *repoaccess.Checker
	Tasks     TaskActor
	Metrics   *metrics.Set
	Logger    *slog.Logger

	mu     sync.Mutex
	limits map[string]*rate.Limiter // (team,user) -> 10/min, burst 5, per replica
}

func (s *Server) slackStop(w http.ResponseWriter, r *http.Request) {
	s.slackAct(w, r, s.SlackStop, "stop")
}

func (s *Server) slackRetry(w http.ResponseWriter, r *http.Request) {
	s.slackAct(w, r, s.SlackRetry, "retry")
}

func (s *Server) slackAct(w http.ResponseWriter, r *http.Request, a *SlackActor, action string) {
	p, ok := s.sponsoredPrincipal(r)
	if !ok || !p.Sponsored || p.ID != a.Principal || p.AccessToken == "" {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	in, ok := decodeSlackAct(w, r)
	if !ok {
		a.answer(w, slackBadRequest)
		return
	}
	if !a.allow(in.Team + "/" + in.User) {
		a.answer(w, slackRateLimited)
		return
	}
	result := a.resolve(r.Context(), s, p, action, in)
	a.answer(w, result)
	a.logged(r.Context(), action, in, result)
}

func decodeSlackAct(w http.ResponseWriter, r *http.Request) (SlackAct, bool) {
	var in SlackAct
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return in, false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, slackDecideBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		return in, false
	}
	return in, slackActShaped(in)
}

func slackActShaped(in SlackAct) bool {
	return slackTeamRE.MatchString(in.Team) && slackUserRE.MatchString(in.User) &&
		slackChannelRE.MatchString(in.Channel) && slackMessageTSRE.MatchString(in.MessageTS) &&
		envelope.ValidID(in.Room)
}

func (a *SlackActor) allow(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return takeBounded(&a.limits, key, rate.Every(6*time.Second), 5, time.Now())
}

func (a *SlackActor) answer(w http.ResponseWriter, result string) {
	code := http.StatusOK
	switch result {
	case slackBadRequest:
		code = http.StatusBadRequest
	case slackRateLimited:
		code = http.StatusTooManyRequests
	case slackUnavailable:
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	writeJSON(w, slackDecisionResult{Result: result})
}

func (a *SlackActor) log() *slog.Logger {
	if a.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return a.Logger
}

func (a *SlackActor) logged(ctx context.Context, action string, in SlackAct, result string) {
	level := slog.LevelWarn
	if result == "stopped" || result == "retried" {
		level = slog.LevelInfo
	}
	a.log().Log(ctx, level, "slack "+action, "team", in.Team, "user", in.User, "room", in.Room, "result", result)
}

func (a *SlackActor) failed(ctx context.Context, action, what string, err error) string {
	a.log().WarnContext(ctx, "slack "+action+" unreadable", "read", what, "err", err)
	return slackUnavailable
}

func (a *SlackActor) resolve(ctx context.Context, s *Server, p authn.Principal, action string, in SlackAct) string {
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
		return a.failed(ctx, action, "slack link", err)
	case !found || sub == "":
		return slackNotLinked
	}

	// Early standing check when the room (live or closed) names a repository; the factory re-resolves
	// the login and enforces maintainer + repository write + task phase as the authority (SI7, SI23).
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
					return a.failed(ctx, action, "closed room", cerr)
				}
				room = closed
			}
		default:
			return a.failed(ctx, action, "room", err)
		}
	}
	if room != nil {
		if room.Spec.Repository == "" || a.Identity == nil || a.Access == nil {
			return slackNotMaintainer
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
		writes, err := a.Access.CanWrite(rctx, room.Spec.Repository, login)
		switch {
		case err != nil:
			return slackLinkUnverified
		case !writes:
			return slackNotMaintainer
		}
	}

	onBehalfOf := "slack:" + in.Team + "/" + in.User
	var res string
	if action == "retry" {
		res, err = a.Tasks.RetryTask(ctx, in.Room, onBehalfOf, p.AccessToken)
	} else {
		res, err = a.Tasks.StopTask(ctx, in.Room, onBehalfOf, p.AccessToken)
	}
	if err != nil {
		var refused runrequest.ErrRefused
		if errors.As(err, &refused) {
			switch refused.Reason {
			case "not_linked":
				return slackNotLinked
			case "link_unverified", "access_unverified":
				return slackLinkUnverified
			case "not_a_maintainer", "repository_not_allowed":
				return slackNotMaintainer
			}
		}
		return a.failed(ctx, action, "factory", err)
	}
	if !slices.Contains(slackActFactoryResults, res) {
		return a.failed(ctx, action, "factory result", errors.New(res))
	}
	return res
}
