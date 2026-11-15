// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
)

// ActRequest is the POST /v1/tasks/{task}/stops and /retries body (SI23). Unknown fields are
// refused: the actor is resolved from the link onBehalfOf names, never supplied as a login.
type ActRequest struct {
	OnBehalfOf string `json:"onBehalfOf"`
}

// ActResult is the closed result vocabulary of POST /v1/tasks/{task}/stops and /retries.
type ActResult struct {
	Result string `json:"result"`
}

// Closed result codes answered by POST /v1/tasks/{task}/stops and /retries.
const (
	ActStopped       = "stopped"
	ActRetried       = "retried"
	ActNotLinked     = "not_linked"
	ActNotMaintainer = "not_maintainer"
	ActNotRunning    = "not_running"
	ActNotEscalated  = "not_escalated"
	ActUnknownTask   = "unknown_task"

	maxActBody = 4 << 10
)

func (s *Server) stopTask(w http.ResponseWriter, r *http.Request) {
	s.actTask(w, r, false)
}

func (s *Server) retryTask(w http.ResponseWriter, r *http.Request) {
	s.actTask(w, r, true)
}

func (s *Server) actTask(w http.ResponseWriter, r *http.Request, retry bool) {
	cfg := s.Cfg.Current()
	p, err := s.Auth.Authenticate(r)
	switch {
	case errors.Is(err, authn.ErrForbidden):
		refuse(w, http.StatusForbidden, "not_permitted")
		return
	case err != nil:
		refuse(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sp := cfg.SponsoredBy(p.ID)
	if !p.Sponsored || p.Internal || sp == nil {
		refuse(w, http.StatusForbidden, reasonSponsoredOnly)
		return
	}
	ctx := r.Context()
	if retry && s.Stopped != nil && s.Stopped(ctx) {
		refuse(w, http.StatusServiceUnavailable, "kill_switch")
		return
	}
	var in ActRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxActBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() || in.OnBehalfOf == "" {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	taskName := r.PathValue("task")
	if !envelope.ValidID(taskName) {
		reply(w, http.StatusOK, ActResult{Result: ActUnknownTask})
		return
	}

	var resolvedLogin string
	for attempt := 1; ; attempt++ {
		t, found, err := s.existing(ctx, taskName)
		if err != nil {
			s.failAct(ctx, w, p.ID, "tasks_unavailable", err)
			return
		}
		if !found {
			reply(w, http.StatusOK, ActResult{Result: ActUnknownTask})
			return
		}
		if attempt == 1 {
			repo := canonicalRepository(sp.Repositories, t.Spec.Repository)
			rc := cfg.RepositoryFor(repo)
			if !slices.Contains(sp.Repositories, repo) || rc == nil {
				refuse(w, http.StatusForbidden, "repository_not_allowed")
				return
			}
			id, ok := strings.CutPrefix(in.OnBehalfOf, "slack:")
			team, user, slash := strings.Cut(id, "/")
			if !ok || !slash || team == "" || user == "" {
				reply(w, http.StatusOK, ActResult{Result: ActNotLinked})
				return
			}
			login, code, reason := s.onBehalfLogin(ctx, sp, in.OnBehalfOf, repo)
			switch reason {
			case "":
			case "not_linked":
				reply(w, http.StatusOK, ActResult{Result: ActNotLinked})
				return
			default:
				refuse(w, code, reason)
				return
			}
			if !rc.IsMaintainer(login) {
				reply(w, http.StatusOK, ActResult{Result: ActNotMaintainer})
				return
			}
			canWrite, err := s.actLoginWrites(ctx, repo, login)
			if err != nil {
				s.failAct(ctx, w, p.ID, "access_unverified", err)
				return
			}
			if !canWrite {
				reply(w, http.StatusOK, ActResult{Result: ActNotMaintainer})
				return
			}
			resolvedLogin = strings.ToLower(login)
		}

		base := t.DeepCopy()
		if t.Annotations == nil {
			t.Annotations = map[string]string{}
		}
		var result string
		if retry {
			if t.Status.Phase != factoryv1.PhaseEscalated {
				reply(w, http.StatusOK, ActResult{Result: ActNotEscalated})
				return
			}
			t.Annotations[factoryv1.AnnotationRetry] = fmt.Sprintf("%d:%s", t.Status.Retries, resolvedLogin)
			result = ActRetried
		} else {
			if !stoppableTask(t) {
				reply(w, http.StatusOK, ActResult{Result: ActNotRunning})
				return
			}
			t.Annotations[factoryv1.AnnotationStop] = "slack"
			result = ActStopped
		}

		err = s.Rooms.Patch(ctx, t, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		if err == nil {
			reply(w, http.StatusOK, ActResult{Result: result})
			return
		}
		if !apierrors.IsConflict(err) || attempt == 3 {
			s.failAct(ctx, w, p.ID, "tasks_unavailable", err)
			return
		}
	}
}

func stoppableTask(t *factoryv1.Task) bool {
	if factoryv1.TerminalPhase(t.Status.Phase) || t.Status.Phase == factoryv1.PhaseMerged {
		return false
	}
	return t.Spec.Kind != factoryv1.TaskKindCoordination && t.Spec.Kind != factoryv1.TaskKindIntegration
}

func (s *Server) actLoginWrites(ctx context.Context, repository, login string) (bool, error) {
	if s.Writers != nil {
		return s.Writers(ctx, repository, login)
	}
	if lw, ok := s.Access.(interface {
		CanWriteLogin(ctx context.Context, repository, login string) (bool, error)
	}); ok {
		return lw.CanWriteLogin(ctx, repository, login)
	}
	return false, nil
}

func (s *Server) failAct(ctx context.Context, w http.ResponseWriter, principal, reason string, err error) {
	if s.Log != nil {
		s.Log.ErrorContext(ctx, "act: "+reason, "principal", principal, "error", err)
	}
	refuse(w, http.StatusServiceUnavailable, reason)
}
