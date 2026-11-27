// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/upkeep"
)

const (
	defaultDigestWindowDays = 7
	maxDigestWindowDays     = 30
)

var digestRepoRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// RoutineDigest is the GET /v1/routines/digest scorecard response across served repositories.
type RoutineDigest struct {
	Window       string              `json:"window"`
	GeneratedAt  time.Time           `json:"generatedAt"`
	Repositories []RoutineDigestRepo `json:"repositories"`
}

// RoutineDigestRepo holds one repository's routine scorecard rows.
type RoutineDigestRepo struct {
	Repository string               `json:"repository"`
	Routines   []RoutineDigestEntry `json:"routines"`
}

// RoutineDigestEntry holds bounded counts and rates for one routine over the digest window.
type RoutineDigestEntry struct {
	Name          string   `json:"name"`
	Runs          int      `json:"runs"`
	NoOps         int      `json:"noOps"`
	PRsOpened     int      `json:"prsOpened"`
	Merged        int      `json:"merged"`
	Closed        int      `json:"closed"`
	Stale         int      `json:"stale"`
	Escalated     int      `json:"escalated"`
	MergeRate     *float64 `json:"mergeRate"`
	CostUSD       float64  `json:"costUSD"`
	USDPerMerged  *float64 `json:"usdPerMerged"`
	Paused        bool     `json:"paused"`
	BreakerReason string   `json:"breakerReason,omitempty"`
}

func parseDigestWindow(raw string) (string, int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "7d", defaultDigestWindowDays, true
	}
	if !strings.HasSuffix(raw, "d") {
		return "", 0, false
	}
	numStr := strings.TrimSuffix(raw, "d")
	n, err := strconv.Atoi(numStr)
	if err != nil || n < 1 || n > maxDigestWindowDays || strconv.Itoa(n)+"d" != raw {
		return "", 0, false
	}
	return raw, n, true
}

func validDigestRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || strings.Contains(name, "/") || owner == "" || owner == "." || owner == ".." || name == "" || name == "." || name == ".." {
		return false
	}
	return digestRepoRE.MatchString(repo)
}

func parseDigestRepos(q url.Values) ([]string, bool) {
	var out []string
	for _, key := range []string{"repository", "repo", "repositories"} {
		for _, raw := range q[key] {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			for _, part := range strings.Split(raw, ",") {
				item := strings.TrimSpace(part)
				if !validDigestRepo(item) {
					return nil, false
				}
				out = append(out, item)
			}
		}
	}
	return out, true
}

func (s *Server) routineDigest(w http.ResponseWriter, r *http.Request) {
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

	var sp *config.Sponsored
	switch {
	case p.Internal:
		refuse(w, http.StatusForbidden, "human_only")
		return
	case p.Sponsored:
		sp = cfg.SponsoredBy(p.ID)
		if sp == nil {
			refuse(w, http.StatusForbidden, "human_only")
			return
		}
	case p.Kind != envelope.ActorHuman || p.Sub == "":
		refuse(w, http.StatusForbidden, "human_only")
		return
	default:
		if (s.Groups.Member != "" || s.Groups.Admin != "") && !s.Groups.Admitted(p) {
			refuse(w, http.StatusForbidden, "not_permitted")
			return
		}
	}

	windowStr, days, ok := parseDigestWindow(r.URL.Query().Get("window"))
	if !ok {
		refuse(w, http.StatusBadRequest, "bad_window")
		return
	}
	askedRepos, ok := parseDigestRepos(r.URL.Query())
	if !ok {
		refuse(w, http.StatusBadRequest, "bad_repository")
		return
	}

	var repos []config.Repository
	seen := make(map[string]bool)
	addRepo := func(rc *config.Repository) {
		if rc == nil {
			return
		}
		key := strings.ToLower(rc.Name)
		if seen[key] {
			return
		}
		seen[key] = true
		repos = append(repos, *rc)
	}

	if len(askedRepos) > 0 {
		for _, asked := range askedRepos {
			if sp != nil {
				canon := canonicalRepository(sp.Repositories, asked)
				if !slices.Contains(sp.Repositories, canon) {
					refuse(w, http.StatusNotFound, "not_found")
					return
				}
				rc := cfg.RepositoryFor(canon)
				if rc == nil {
					refuse(w, http.StatusNotFound, "not_found")
					return
				}
				addRepo(rc)
			} else {
				repo := s.readable(cfg, w, r, p, asked)
				if repo == "" {
					return
				}
				addRepo(cfg.RepositoryFor(repo))
			}
		}
	} else {
		for _, rc := range cfg.Repos() {
			if sp != nil {
				if slices.ContainsFunc(sp.Repositories, func(a string) bool { return strings.EqualFold(a, rc.Name) }) {
					rCopy := rc
					addRepo(&rCopy)
				}
			} else if can, err := s.reads(r.Context(), p, rc.Name); err == nil && can {
				rCopy := rc
				addRepo(&rCopy)
			}
		}
	}

	slices.SortFunc(repos, func(a, b config.Repository) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	now := s.nowTime()
	hasAny := false
	for i := range repos {
		if len(routineEntryNames(&repos[i])) > 0 {
			hasAny = true
			break
		}
	}
	if !hasAny {
		w.Header().Set("Cache-Control", "no-store")
		reply(w, http.StatusOK, RoutineDigest{
			Window:       windowStr,
			GeneratedAt:  now,
			Repositories: []RoutineDigestRepo{},
		})
		return
	}

	reader := s.routineTaskReader()
	if reader == nil {
		refuse(w, http.StatusServiceUnavailable, "tasks_unavailable")
		return
	}
	var taskList factoryv1.TaskList
	if err := reader.List(r.Context(), &taskList, client.InNamespace(s.Namespace)); err != nil {
		refuse(w, http.StatusServiceUnavailable, "tasks_unavailable")
		return
	}

	var states map[string]map[string]upkeep.RoutineState
	if s.RoutineState != nil {
		states, err = s.RoutineState.List(r.Context())
		if err != nil {
			s.log().WarnContext(r.Context(), "routine state list failed", "error", err)
			refuse(w, http.StatusServiceUnavailable, "routine_state_unavailable")
			return
		}
	}

	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	outRepos := make([]RoutineDigestRepo, 0, len(repos))
	for i := range repos {
		rc := &repos[i]
		names := routineEntryNames(rc)
		if len(names) == 0 {
			continue
		}
		repoStates := states[strings.ToLower(rc.Name)]
		entries := make([]RoutineDigestEntry, 0, len(names))
		for _, name := range names {
			rr, _, found := findRoutineEntry(cfg, rc, name)
			if !found {
				continue
			}
			tasks := routineTasksFor(taskList.Items, rc.Name, name)
			entries = append(entries, buildRoutineDigestEntry(rr, repoStates[name], tasks, cutoff, now))
		}
		outRepos = append(outRepos, RoutineDigestRepo{
			Repository: rc.Name,
			Routines:   entries,
		})
	}

	w.Header().Set("Cache-Control", "no-store")
	reply(w, http.StatusOK, RoutineDigest{
		Window:       windowStr,
		GeneratedAt:  now,
		Repositories: outRepos,
	})
}

func buildRoutineDigestEntry(rr config.ResolvedRoutine, st upkeep.RoutineState, tasks []*factoryv1.Task, cutoff, now time.Time) RoutineDigestEntry {
	entry := RoutineDigestEntry{
		Name: rr.Name,
	}
	var totalMicroUSD int64
	for _, t := range tasks {
		ts := taskBucketTime(t).UTC()
		if ts.Before(cutoff) || ts.After(now) {
			continue
		}
		entry.Runs++
		totalMicroUSD += taskCostMicroUSD(t)
		if t.Status.PullRequest != nil && t.Status.PullRequest.Number > 0 {
			entry.PRsOpened++
		}
		switch t.Status.Phase {
		case factoryv1.PhaseNoOp:
			entry.NoOps++
		case factoryv1.PhaseDone:
			switch {
			case t.Status.Outcome == "failed":
				entry.Closed++
			case (t.Status.Reason == "proposal_filed" || t.Status.Reason == "proposal_ready") &&
				t.Status.PullRequest == nil && t.Status.Published == nil:
				// Pending propose-mode issue; do not count as merged or closed.
			default:
				entry.Merged++
			}
		case factoryv1.PhaseClosed:
			if t.Status.Reason == "stale" {
				entry.Stale++
			} else {
				entry.Closed++
			}
		case factoryv1.PhaseReverted:
			if t.Annotations[factoryv1.AnnotationDrill] != "true" {
				entry.Closed++
			}
		case factoryv1.PhaseEscalated:
			entry.Escalated++
		}
	}

	entry.CostUSD = float64(totalMicroUSD) / 1e6
	if denom := entry.Merged + entry.Closed + entry.Stale; denom > 0 {
		v := float64(entry.Merged) / float64(denom)
		entry.MergeRate = &v
	}
	if entry.Merged > 0 {
		v := entry.CostUSD / float64(entry.Merged)
		entry.USDPerMerged = &v
	}
	entry.Paused = rr.Paused || st.Paused || st.BreakerState == upkeep.BreakerPaused
	if st.BreakerState == upkeep.BreakerPaused {
		entry.BreakerReason = st.BreakerReason
	}
	return entry
}
