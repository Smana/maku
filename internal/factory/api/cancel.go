// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
)

// CancelRoute is a task's cancel or discard by a human (#644). It is mounted whenever the API is,
// unlike the sponsored /stops: the room page, maku cancel and the cancel tool reach it through
// the broker with the person's own token, and it needs no issue, so it ends a task whose issue
// was deleted.
const CancelRoute = "POST /v1/tasks/{task}/cancellations"

// CancelRequest is the cancellations body: discard also closes the pull request, deletes the
// agent/<task> branch and closes the issue as not planned.
type CancelRequest struct {
	Discard bool `json:"discard"`
}

// Closed result codes the cancellations route answers beside not_linked, not_maintainer,
// not_running and unknown_task.
const (
	ActCancelled = "cancelled"
	ActDiscarded = "discarded"
)

// The stop annotation's values the cancellations route writes, which the reconciler reads.
const (
	stopCancel  = "cancel"
	stopDiscard = "discard"
)

// cancelTask annotates the task for a stop as the caller asked. The caller is a human, resolved to
// their GitHub login through their own link (never a login they send), who is a maintainer of the
// task's repository or the task's own requester, and holds write there now.
func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, ok := s.coordinationCaller(w, r)
	if !ok {
		return
	}
	if p.Sponsored || p.Internal || p.Kind != envelope.ActorHuman {
		refuse(w, http.StatusForbidden, "human_only")
		return
	}
	var in CancelRequest
	if !decodeStrict(w, r, &in) {
		return
	}
	ctx := r.Context()
	name := r.PathValue("task")
	if !envelope.ValidID(name) {
		reply(w, http.StatusOK, ActResult{Result: ActUnknownTask})
		return
	}
	value, result := stopCancel, ActCancelled
	if in.Discard {
		value, result = stopDiscard, ActDiscarded
	}
	for attempt := 1; ; attempt++ {
		t, found, err := s.existing(ctx, name)
		if err != nil {
			s.failAct(ctx, w, p.ID, "tasks_unavailable", err)
			return
		}
		if !found {
			reply(w, http.StatusOK, ActResult{Result: ActUnknownTask})
			return
		}
		if attempt == 1 {
			rc := cfg.RepositoryFor(t.Spec.Repository)
			if rc == nil {
				refuse(w, http.StatusForbidden, "repository_not_allowed")
				return
			}
			login, code, reason := s.linkedLogin(ctx, p, t.Spec.Repository)
			switch reason {
			case "":
			case "not_linked":
				reply(w, http.StatusOK, ActResult{Result: ActNotLinked})
				return
			default:
				refuse(w, code, reason)
				return
			}
			if !rc.IsMaintainer(login) && !requestedBy(t, login) {
				reply(w, http.StatusOK, ActResult{Result: ActNotMaintainer})
				return
			}
			canWrite, err := s.actLoginWrites(ctx, t.Spec.Repository, login)
			if err != nil {
				s.failAct(ctx, w, p.ID, "access_unverified", err)
				return
			}
			if !canWrite {
				reply(w, http.StatusOK, ActResult{Result: ActNotMaintainer})
				return
			}
		}
		if !stoppableTask(t) {
			reply(w, http.StatusOK, ActResult{Result: ActNotRunning})
			return
		}
		base := t.DeepCopy()
		if t.Annotations == nil {
			t.Annotations = map[string]string{}
		}
		if t.Annotations[factoryv1.AnnotationStop] == stopDiscard {
			value, result = stopDiscard, ActDiscarded // a cancel never narrows a discard already asked
		}
		t.Annotations[factoryv1.AnnotationStop] = value
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

// requestedBy is whether login asked for t: its issue's author, or the requester its source names.
func requestedBy(t *factoryv1.Task, login string) bool {
	if login == "" {
		return false
	}
	return strings.EqualFold(t.Spec.IssueAuthor, login) || strings.EqualFold(t.Spec.Source.RequestedBy, "github:"+login)
}
