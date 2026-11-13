// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/hardening"
)

// BrokerPrincipal is the room broker's system principal on this API (HR35): the api.systemPrincipals
// value its ServiceAccount maps to, the one caller the forge-read route admits.
const BrokerPrincipal = "system:broker"

// ForgeReadRoute is the route the broker's forge_read forwards to.
const ForgeReadRoute = "POST /v1/runs/{run}/forge-read"

// ReadFilterOff is the forge-read route's 409: the run's repository is not in hardening mode
// enforce, so its reads are not filtered and none is served.
const ReadFilterOff = "read_filter_off"

// VouchLabel is a maintainer's vouch on an issue or pull request (UH6): its body reads approved.
const VouchLabel = "factory/vouch"

// maxForgeChecks bounds the check runs one read returns, failed first.
const maxForgeChecks = 20

// ForgeThreads is what forge_read reads of a repository with the factory App (HR35): the task's
// own issue, pull request and checks. *forge.GitHub implements it.
type ForgeThreads interface {
	Issue(ctx context.Context, number int) (forge.Issue, error)
	AllComments(ctx context.Context, number int) ([]forge.Comment, error)
	PullRequest(ctx context.Context, number int) (forge.PR, error)
	PullRequestChecks(ctx context.Context, number int) (forge.Checks, error)
	FailedJobLog(ctx context.Context, jobID int64) (string, error)
}

// forgeReadRequest is the route's body: the target alone. The run is the path's, which the broker
// takes from the verified caller, never from the model's arguments.
type forgeReadRequest struct {
	Target string `json:"target"`
}

// forgeRead is POST /v1/runs/{run}/forge-read (HR35): the broker asks for a run's issue, pull
// request or checks; the factory resolves the run's task, reads that task's own objects with its
// App, and answers them filtered by the repository's trust policy. Withheld text never leaves this
// process: not in the response, not in a log line.
func (s *Server) forgeRead(w http.ResponseWriter, r *http.Request) {
	p, err := s.Auth.Authenticate(r)
	switch {
	case errors.Is(err, authn.ErrForbidden):
		refuse(w, http.StatusForbidden, "not_permitted")
		return
	case err != nil:
		refuse(w, http.StatusUnauthorized, "unauthenticated")
		return
	case p.Kind != envelope.ActorSystem || p.ID != BrokerPrincipal:
		refuse(w, http.StatusForbidden, "not_permitted")
		return
	}
	var in forgeReadRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	run := r.PathValue("run")
	if err := dec.Decode(&in); err != nil || !hardening.ValidTarget(in.Target) || !envelope.ValidID(run) {
		refuse(w, http.StatusBadRequest, "bad_request")
		return
	}
	task, err := s.taskOfRun(r.Context(), run)
	switch {
	case err != nil:
		s.log().Warn("forge read: tasks unread", "run.id", run, "err", err)
		refuse(w, http.StatusServiceUnavailable, "unavailable")
		return
	case task == nil:
		refuse(w, http.StatusNotFound, "unknown_run")
		return
	}
	cfg := s.Cfg.Current()
	repo := task.Spec.Repository
	if cfg.Demo || cfg.Hardening.ModeFor(repo) != hardening.ModeEnforce {
		refuse(w, http.StatusConflict, ReadFilterOff)
		return
	}
	var gh ForgeThreads
	if s.Readers != nil {
		gh = s.Readers(repo)
	}
	if gh == nil {
		refuse(w, http.StatusServiceUnavailable, "forge_unavailable")
		return
	}
	res, err := s.readForge(r.Context(), cfg, gh, task, in.Target)
	if err != nil {
		s.log().Warn("forge read failed", "run.id", run, "task.id", task.Name, "target", in.Target, "err", err)
		refuse(w, http.StatusBadGateway, "forge_error")
		return
	}
	s.log().Info("forge read", "run.id", run, "task.id", task.Name, "target", in.Target,
		"items", len(res.Items), "withheld", res.WithheldCount(), "min", res.InputTrust.Min.String())
	reply(w, http.StatusOK, res)
}

// taskOfRun is the Task that recorded run in status.runs; nil when none did.
func (s *Server) taskOfRun(ctx context.Context, run string) (*factoryv1.Task, error) {
	var ts factoryv1.TaskList
	if err := s.Rooms.List(ctx, &ts, client.InNamespace(s.Namespace)); err != nil {
		return nil, err
	}
	for i := range ts.Items {
		if slices.ContainsFunc(ts.Items[i].Status.Runs, func(rr factoryv1.RunRecord) bool { return rr.ID == run }) {
			return &ts.Items[i], nil
		}
	}
	return nil, nil
}

// trustPolicy is the repository's author-trust policy at its own floor (the fence file's block,
// through Trust), as the reconciler's input record reads it.
func (s *Server) trustPolicy(cfg *config.Config, repo string) hardening.Policy {
	central := cfg.Hardening.MinTrust()
	var rp hardening.RepositoryPolicy
	if s.Trust != nil {
		rp, _ = s.Trust.For(repo, central) // a block that does not parse is the floor and merged
	} else {
		rp, _ = hardening.ParseRepositoryBlock(nil, central) // empty: the floor and central
	}
	return cfg.TrustPolicy(repo, rp.MinTrust)
}

// readForge reads target of t and filters it: the task's own issue, its own pull request, or its
// pull request's checks, never another object. A task with no such object reads as empty.
func (s *Server) readForge(ctx context.Context, cfg *config.Config, gh ForgeThreads, t *factoryv1.Task, target string) (hardening.ForgeReadResult, error) {
	own := func(login string) bool {
		return login != "" && (strings.EqualFold(login, cfg.FactoryLogin) || strings.EqualFold(login, cfg.AgentsLogin))
	}
	pr := 0
	if t.Status.PullRequest != nil {
		pr = t.Status.PullRequest.Number
	}
	switch target {
	case hardening.TargetChecks:
		if pr == 0 {
			return hardening.FilterChecks(nil), nil
		}
		cs, err := checkTexts(ctx, gh, pr)
		if err != nil {
			return hardening.ForgeReadResult{}, err
		}
		return hardening.FilterChecks(cs), nil
	case hardening.TargetIssue:
		var ts []hardening.ForgeText
		if n := t.Spec.Issue; n > 0 {
			iss, err := gh.Issue(ctx, n)
			if err != nil {
				return hardening.ForgeReadResult{}, err
			}
			if !own(iss.Author) {
				// The trigger label, or an api task's accountable sponsor, vouches for the task's issue (UH6).
				vouched := t.Spec.Source.Kind == "issue" || t.Spec.Source.Kind == "api" || slices.Contains(iss.Labels, VouchLabel)
				ts = append(ts, hardening.ForgeText{Kind: "issue", Author: hardening.Author{Login: iss.Author, Association: iss.Association},
					Body: iss.Title + "\n\n" + iss.Body, Vouched: vouched})
			}
			cs, err := comments(ctx, gh, n, own)
			if err != nil {
				return hardening.ForgeReadResult{}, err
			}
			ts = append(ts, cs...)
		}
		return hardening.FilterThread(s.trustPolicy(cfg, t.Spec.Repository), ts), nil
	}
	var ts []hardening.ForgeText
	if pr > 0 {
		p, err := gh.PullRequest(ctx, pr)
		if err != nil {
			return hardening.ForgeReadResult{}, err
		}
		if !own(p.Author) && p.Body != "" {
			ts = append(ts, hardening.ForgeText{Kind: "pull_request", Author: hardening.Author{Login: p.Author},
				Body: p.Body, Vouched: slices.Contains(p.Labels, VouchLabel)})
		}
		for _, rv := range p.Reviews {
			if own(rv.Author) {
				continue
			}
			body := rv.Body
			for _, c := range rv.Comments {
				body += "\n" + c.Path + ":" + strconv.Itoa(c.Line) + ": " + c.Body
			}
			if strings.TrimSpace(body) == "" {
				continue
			}
			ts = append(ts, hardening.ForgeText{ID: rv.ID, Kind: "review", Author: hardening.Author{Login: rv.Author, Association: rv.Association}, Body: body})
		}
		cs, err := comments(ctx, gh, pr, own)
		if err != nil {
			return hardening.ForgeReadResult{}, err
		}
		ts = append(ts, cs...)
	}
	return hardening.FilterThread(s.trustPolicy(cfg, t.Spec.Repository), ts), nil
}

// comments are thread n's comments but the factory's and the agents' own, which are not human text.
func comments(ctx context.Context, gh ForgeThreads, n int, own func(string) bool) ([]hardening.ForgeText, error) {
	cs, err := gh.AllComments(ctx, n)
	if err != nil {
		return nil, err
	}
	var out []hardening.ForgeText
	for _, c := range cs {
		if own(c.Author) {
			continue
		}
		out = append(out, hardening.ForgeText{ID: c.ID, Kind: "comment", Author: hardening.Author{Login: c.Author, Association: c.Association}, Body: c.Body})
	}
	return out, nil
}

// checkTexts are the pull request head's check runs, failed first, at most maxForgeChecks, with
// the failed GitHub Actions jobs' log tails. The task's branch is the factory's (agent/<task>), so
// its CI is the run's own: approved.
func checkTexts(ctx context.Context, gh ForgeThreads, pr int) ([]hardening.CheckText, error) {
	cs, err := gh.PullRequestChecks(ctx, pr)
	if err != nil {
		return nil, err
	}
	runs := slices.Clone(cs.Runs)
	slices.SortStableFunc(runs, func(a, b forge.Check) int {
		return boolRank(b.State == "FAILURE") - boolRank(a.State == "FAILURE")
	})
	if len(runs) > maxForgeChecks {
		runs = runs[:maxForgeChecks]
	}
	out := make([]hardening.CheckText, 0, len(runs))
	for _, c := range runs {
		ct := hardening.CheckText{Name: c.Name, Conclusion: c.Conclusion}
		if c.State == "FAILURE" && c.App == forge.ActionsApp && c.ID > 0 {
			if ct.Log, err = gh.FailedJobLog(ctx, c.ID); err != nil {
				return nil, err
			}
		}
		out = append(out, ct)
	}
	return out, nil
}

// log is s.Log, or a logger that discards.
func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}
