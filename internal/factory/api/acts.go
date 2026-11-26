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
	"github.com/Smana/maku/internal/factory/config"
)

// ActRequest is the POST /v1/tasks/{task}/stops and /retries body (SI23, WR4). Unknown fields are
// refused: the actor is resolved from the link onBehalfOf names (for sponsored callers) or the
// caller's own GitHub link (for human callers on /retries), never supplied as a login.
type ActRequest struct {
	OnBehalfOf string `json:"onBehalfOf,omitempty"`
	Deeper     bool   `json:"deeper,omitempty"`
}

// ActResult is the closed result vocabulary of POST /v1/tasks/{task}/stops and /retries.
type ActResult struct {
	Result          string `json:"result"`
	Workflow        string `json:"workflow,omitempty"`
	CeilingMicroUSD int64  `json:"ceilingMicroUSD,omitempty"`
}

// Closed result codes answered by POST /v1/tasks/{task}/stops and /retries.
const (
	ActStopped          = "stopped"
	ActRetried          = "retried"
	ActSteppedUp        = "stepped_up"
	ActNoDeeperWorkflow = "no_deeper_workflow"
	ActDeeperOverCap    = "deeper_over_cap"
	ActNotLinked        = "not_linked"
	ActNotMaintainer    = "not_maintainer"
	ActNotRunning       = "not_running"
	ActNotEscalated     = "not_escalated"
	ActUnknownTask      = "unknown_task"
	ActNoPlan           = "no_plan"
	ActNotAllowed       = "not_allowed"
	ActPlanApproved     = "plan_approved"
	ActPlanRevised      = "plan_revised"
	ActPlanSplit        = "plan_split"
	ActSplitAccepted    = "split_accepted"
	ActSplitDeclined    = "split_declined"
	ActNoSplit          = "no_split"
	ActTaskCapHolds     = "task_cap_holds" // F-4: the task cap leaves less than a run; raise it on a new task

	maxActBody  = 4 << 10
	maxPlanNote = 1024
)

// SplitRequest is the POST /v1/tasks/{task}/splits body (phase G1): accept or decline a proposed split.
type SplitRequest struct {
	OnBehalfOf string `json:"onBehalfOf,omitempty"`
	Decision   string `json:"decision"`
}

func (s *Server) splitTask(w http.ResponseWriter, r *http.Request) {
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
	humanSplit := !p.Sponsored && !p.Internal && p.Kind == envelope.ActorHuman
	if !humanSplit && (!p.Sponsored || p.Internal || sp == nil) {
		refuse(w, http.StatusForbidden, reasonSponsoredOnly)
		return
	}
	ctx := r.Context()
	if s.Stopped != nil && s.Stopped(ctx) {
		refuse(w, http.StatusServiceUnavailable, "kill_switch")
		return
	}
	var in SplitRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxActBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	if humanSplit {
		if in.OnBehalfOf != "" {
			refuse(w, http.StatusForbidden, reasonSponsoredOnly)
			return
		}
	} else if in.OnBehalfOf == "" {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	if in.Decision != "accept" && in.Decision != "decline" {
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
			var repo string
			var rc *config.Repository
			var login string
			var code int
			var reason string
			if humanSplit {
				repo = t.Spec.Repository
				rc = cfg.RepositoryFor(repo)
				if rc == nil {
					refuse(w, http.StatusForbidden, "repository_not_allowed")
					return
				}
				login, code, reason = s.linkedLogin(ctx, p, repo)
			} else {
				repo = canonicalRepository(sp.Repositories, t.Spec.Repository)
				rc = cfg.RepositoryFor(repo)
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
				login, code, reason = s.onBehalfLogin(ctx, sp, in.OnBehalfOf, repo)
			}
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
		if t.Status.Phase != factoryv1.PhaseEscalated || t.Status.Reason != "split_proposed" ||
			t.Status.Split == nil || t.Status.Split.State != "proposed" {
			reply(w, http.StatusOK, ActResult{Result: ActNoSplit})
			return
		}
		payload, err := json.Marshal(struct {
			Decision string `json:"decision"`
			By       string `json:"by"`
			Seq      int64  `json:"seq"`
		}{Decision: in.Decision, By: resolvedLogin, Seq: t.Status.Split.Seq})
		if err != nil {
			s.failAct(ctx, w, p.ID, "tasks_unavailable", err)
			return
		}
		base := t.DeepCopy()
		if t.Annotations == nil {
			t.Annotations = map[string]string{}
		}
		t.Annotations[factoryv1.AnnotationSplitDecision] = string(payload)
		err = s.Rooms.Patch(ctx, t, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		if err == nil {
			resCode := ActSplitAccepted
			if in.Decision == "decline" {
				resCode = ActSplitDeclined
			}
			reply(w, http.StatusOK, ActResult{Result: resCode})
			return
		}
		if !apierrors.IsConflict(err) || attempt == 3 {
			s.failAct(ctx, w, p.ID, "tasks_unavailable", err)
			return
		}
	}
}

// PlanRequest is the POST /v1/tasks/{task}/plans body (phase F3): approve, revise (with a
// one-line note), or split.
type PlanRequest struct {
	OnBehalfOf string `json:"onBehalfOf,omitempty"`
	Decision   string `json:"decision"`
	Note       string `json:"note,omitempty"`
}

func (s *Server) planTask(w http.ResponseWriter, r *http.Request) {
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
	humanPlan := !p.Sponsored && !p.Internal && p.Kind == envelope.ActorHuman
	if !humanPlan && (!p.Sponsored || p.Internal || sp == nil) {
		refuse(w, http.StatusForbidden, reasonSponsoredOnly)
		return
	}
	ctx := r.Context()
	if s.Stopped != nil && s.Stopped(ctx) {
		refuse(w, http.StatusServiceUnavailable, "kill_switch")
		return
	}
	var in PlanRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxActBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	if humanPlan {
		if in.OnBehalfOf != "" {
			refuse(w, http.StatusForbidden, reasonSponsoredOnly)
			return
		}
	} else if in.OnBehalfOf == "" {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	var note string
	switch in.Decision {
	case "approve", "split":
		if strings.TrimSpace(in.Note) != "" {
			refuse(w, http.StatusBadRequest, "bad_json")
			return
		}
	case "revise":
		if len(in.Note) > maxPlanNote {
			refuse(w, http.StatusBadRequest, "bad_json")
			return
		}
		var ok bool
		note, ok = cleanAnswerText(in.Note)
		if !ok {
			refuse(w, http.StatusBadRequest, "invalid_draft")
			return
		}
		if note == "" {
			refuse(w, http.StatusBadRequest, "bad_json")
			return
		}
		if s.Redact != nil {
			if _, rules := s.Redact.String(note); len(rules) > 0 {
				refuse(w, http.StatusBadRequest, "invalid_draft")
				return
			}
		}
	default:
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
			var repo string
			var rc *config.Repository
			var login string
			var code int
			var reason string
			if humanPlan {
				repo = t.Spec.Repository
				rc = cfg.RepositoryFor(repo)
				if rc == nil {
					refuse(w, http.StatusForbidden, "repository_not_allowed")
					return
				}
				login, code, reason = s.linkedLogin(ctx, p, repo)
			} else {
				repo = canonicalRepository(sp.Repositories, t.Spec.Repository)
				rc = cfg.RepositoryFor(repo)
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
				login, code, reason = s.onBehalfLogin(ctx, sp, in.OnBehalfOf, repo)
			}
			switch reason {
			case "":
			case "not_linked":
				reply(w, http.StatusOK, ActResult{Result: ActNotLinked})
				return
			default:
				refuse(w, code, reason)
				return
			}
			isAuthor := t.Spec.IssueAuthor != "" && strings.EqualFold(t.Spec.IssueAuthor, login)
			isMaintainer := false
			if !isAuthor && rc.IsMaintainer(login) {
				canWrite, err := s.actLoginWrites(ctx, repo, login)
				if err != nil {
					s.failAct(ctx, w, p.ID, "access_unverified", err)
					return
				}
				isMaintainer = canWrite
			}
			if !isAuthor && !isMaintainer {
				reply(w, http.StatusOK, ActResult{Result: ActNotAllowed})
				return
			}
			resolvedLogin = strings.ToLower(login)
		}
		if t.Status.Phase != factoryv1.PhaseAwaitingHuman || t.Status.Reason != "plan_review" {
			reply(w, http.StatusOK, ActResult{Result: ActNoPlan})
			return
		}
		var round int32
		if t.Status.Plan != nil {
			round = t.Status.Plan.Revisions
		}
		payload, err := json.Marshal(struct {
			Decision string `json:"decision"`
			Note     string `json:"note,omitempty"`
			By       string `json:"by"`
			Round    int32  `json:"round"`
		}{Decision: in.Decision, Note: note, By: resolvedLogin, Round: round})
		if err != nil {
			s.failAct(ctx, w, p.ID, "tasks_unavailable", err)
			return
		}
		base := t.DeepCopy()
		if t.Annotations == nil {
			t.Annotations = map[string]string{}
		}
		t.Annotations[factoryv1.AnnotationPlanDecision] = string(payload)
		err = s.Rooms.Patch(ctx, t, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		if err == nil {
			resCode := ActPlanApproved
			switch in.Decision {
			case "revise":
				resCode = ActPlanRevised
			case "split":
				resCode = ActPlanSplit
			}
			reply(w, http.StatusOK, ActResult{Result: resCode})
			return
		}
		if !apierrors.IsConflict(err) || attempt == 3 {
			s.failAct(ctx, w, p.ID, "tasks_unavailable", err)
			return
		}
	}
}

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
	humanRetry := retry && !p.Sponsored && !p.Internal && p.Kind == envelope.ActorHuman
	if !humanRetry && (!p.Sponsored || p.Internal || sp == nil) {
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
	if err := dec.Decode(&in); err != nil || dec.More() {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	if humanRetry {
		if in.OnBehalfOf != "" {
			refuse(w, http.StatusForbidden, reasonSponsoredOnly)
			return
		}
	} else if in.OnBehalfOf == "" {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	if !retry && in.Deeper {
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
			var repo string
			var rc *config.Repository
			var login string
			var code int
			var reason string
			if humanRetry {
				repo = t.Spec.Repository
				rc = cfg.RepositoryFor(repo)
				if rc == nil {
					refuse(w, http.StatusForbidden, "repository_not_allowed")
					return
				}
				login, code, reason = s.linkedLogin(ctx, p, repo)
			} else {
				repo = canonicalRepository(sp.Repositories, t.Spec.Repository)
				rc = cfg.RepositoryFor(repo)
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
				login, code, reason = s.onBehalfLogin(ctx, sp, in.OnBehalfOf, repo)
			}
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
		var out ActResult
		if retry {
			if in.Deeper {
				if t.Status.Phase != factoryv1.PhaseEscalated && t.Status.Phase != factoryv1.PhaseNoOp {
					reply(w, http.StatusOK, ActResult{Result: ActNotEscalated})
					return
				}
				target, ceiling, failCode := planDeeperRetry(cfg, t)
				if failCode != "" {
					reply(w, http.StatusOK, ActResult{Result: failCode})
					return
				}
				t.Annotations[factoryv1.AnnotationRetry] = fmt.Sprintf("%d:%s:deeper", t.Status.Retries, resolvedLogin)
				out = ActResult{Result: ActSteppedUp, Workflow: target, CeilingMicroUSD: ceiling}
			} else {
				if t.Status.Phase != factoryv1.PhaseEscalated {
					reply(w, http.StatusOK, ActResult{Result: ActNotEscalated})
					return
				}
				if s.RetryHeld != nil && s.RetryHeld(cfg, t) {
					reply(w, http.StatusOK, ActResult{Result: ActTaskCapHolds})
					return
				}
				t.Annotations[factoryv1.AnnotationRetry] = fmt.Sprintf("%d:%s", t.Status.Retries, resolvedLogin)
				out = ActResult{Result: ActRetried}
			}
		} else {
			if !stoppableTask(t) {
				reply(w, http.StatusOK, ActResult{Result: ActNotRunning})
				return
			}
			t.Annotations[factoryv1.AnnotationStop] = "slack"
			out = ActResult{Result: ActStopped}
		}

		err = s.Rooms.Patch(ctx, t, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		if err == nil {
			reply(w, http.StatusOK, out)
			return
		}
		if !apierrors.IsConflict(err) || attempt == 3 {
			s.failAct(ctx, w, p.ID, "tasks_unavailable", err)
			return
		}
	}
}

func planDeeperRetry(cfg *config.Config, t *factoryv1.Task) (target string, ceiling int64, fail string) {
	p := t.Status.Profile
	if p == nil || p.Source == "escalation" ||
		(p.StepUp != nil && (p.StepUp.Mode == "auto" || p.StepUp.Mode == "manual")) ||
		strings.HasSuffix(t.Annotations[factoryv1.AnnotationRetry], ":deeper") {
		return "", 0, ActNoDeeperWorkflow
	}
	target = cfg.StepUpFor(p.Name, "stalled")
	if target == "" && p.StepUp != nil && p.StepUp.Mode == "offered" {
		target = p.StepUp.To
	}
	if target == "" || target == p.Name {
		return "", 0, ActNoDeeperWorkflow
	}
	v, ok := cfg.Workflow(target)
	if !ok {
		return "", 0, ActNoDeeperWorkflow
	}
	tiers := []string{"light", "standard", "frontier"}
	tier := t.Spec.Budget.Tier
	if tier == "" {
		tier = "standard"
	}
	if vt := v.EffectiveTier(); slices.Index(tiers, vt) > slices.Index(tiers, tier) {
		tier = vt // a list-form workflow's budget: names its tier, not tier:
	}
	if usd := cfg.Tiers[tier].TaskUSD; usd > 0 {
		ceiling = int64(usd * 1e6)
	} else {
		ceiling = t.Spec.Budget.TaskMicroUSD
	}
	if t.Spec.MaxMicroUSD > 0 && ceiling > t.Spec.MaxMicroUSD {
		return "", 0, ActDeeperOverCap
	}
	if runUSD := cfg.Tiers[tier].RunUSD; runUSD > 0 && ceiling > 0 && t.Status.Usage.CostMicroUSD+int64(runUSD*1e6) > ceiling {
		return "", 0, ActDeeperOverCap
	}
	return target, ceiling, ""
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
