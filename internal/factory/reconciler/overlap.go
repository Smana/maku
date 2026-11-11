// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/intake"
	"github.com/Smana/maku/internal/factory/runs"
)

// overlapEvaluator is the reconciler's candidate-retrieval boundary. Its result contains no
// untrusted status text; production retrieval reads Tasks, Runs, Rooms, then matched GitHub refs.
type overlapEvaluator interface {
	retrieveOverlap(ctx context.Context, current *v1alpha1.Task) intake.OverlapResult
}

type taskOverlapEvaluator struct{ r *Reconciler }

// retrieveOverlap selects the configured evaluator, defaulting to the factory's own sources.
func (r *Reconciler) retrieveOverlap(ctx context.Context, current *v1alpha1.Task) intake.OverlapResult {
	if r.Overlap != nil {
		return r.Overlap.retrieveOverlap(ctx, current)
	}
	return (taskOverlapEvaluator{r: r}).retrieveOverlap(ctx, current)
}

// overlapDue records a new evaluation, or replays the one already on the Task. A true result ends
// the current pass so every success effect follows the status write that persists the evaluation.
// A final fingerprint and checkedAt are immutable here: replay never derives them again; a new
// Task generation gets its own evaluation and timestamp.
func (r *Reconciler) overlapDue(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (bool, error) {
	if o := t.Status.Overlap; o != nil {
		if o.Classification == "" && o.ScoperRun == "" {
			o.ScoperRun = runID(t.Name, len(t.Status.Runs))
			return true, nil
		}
		if o.Classification == v1alpha1.OverlapActiveDuplicate && cfg.Intake.Overlap.Mode == config.OverlapEnforce {
			return true, r.end(ctx, cfg, t, v1alpha1.PhaseRejected, "overlap_active_duplicate")
		}
		return false, nil
	}
	if cfg.Intake.Overlap.Mode == "" || cfg.Intake.Overlap.Mode == config.OverlapOff || !ordinaryOverlapTask(t) {
		return false, nil
	}

	started := r.Now()
	result := r.retrieveOverlap(ctx, t)
	if result.Fingerprint == "" {
		result.Fingerprint = intake.GoalFingerprint(t.Spec.Repository, t.Spec.Text)
	}
	if result.Classification == "" && !result.Pending {
		result.Classification = v1alpha1.OverlapUnverifiedOverlap
	}
	o := &v1alpha1.OverlapStatus{
		Fingerprint: result.Fingerprint,
		Matches:     slices.Clone(result.Matches),
		Sources:     slices.Clone(result.Sources),
		Note:        cut(result.Note, 512),
	}
	if result.Pending {
		o.ScoperRun = runID(t.Name, len(t.Status.Runs))
	} else {
		checked := metav1.NewTime(r.Now())
		o.Classification, o.CheckedAt = result.Classification, &checked
	}
	t.Status.Overlap = o

	// Metrics are effects of the persisted evaluation, never of a read whose status write failed.
	for _, source := range o.Sources {
		classification, source := o.Classification, source
		if classification != "" {
			record(ctx, func(ctx context.Context) { r.Metrics.OverlapEvaluation(ctx, classification, source) })
		}
	}
	record(ctx, func(ctx context.Context) { r.Metrics.OverlapEvaluationDuration(ctx, r.Now().Sub(started)) })
	if o.Classification == v1alpha1.OverlapActiveDuplicate && cfg.Intake.Overlap.Mode == config.OverlapEnforce {
		return true, r.end(ctx, cfg, t, v1alpha1.PhaseRejected, "overlap_active_duplicate")
	}
	return true, nil
}

// ordinaryOverlapTask limits both evaluation and candidates to issue, API, and schedule work.
// Split parts/parents, coordination, adoption, and RunLore keep their existing lifecycles.
func ordinaryOverlapTask(t *v1alpha1.Task) bool {
	if t.Spec.Kind != "" || t.Spec.Parent != nil || t.Spec.CoordinationParent != nil || t.Status.Phase == v1alpha1.PhaseSplit {
		return false
	}
	switch t.Spec.Source.Kind {
	case "issue", "api", "schedule":
		return true
	default:
		return false
	}
}

func (e taskOverlapEvaluator) retrieveOverlap(ctx context.Context, current *v1alpha1.Task) intake.OverlapResult {
	r := e.r
	result := intake.OverlapResult{Fingerprint: intake.GoalFingerprint(current.Spec.Repository, current.Spec.Text)}
	var failed []string
	checked := map[string]bool{}
	check := func(source string) {
		if !checked[source] {
			checked[source] = true
			result.Sources = append(result.Sources, source)
		}
	}
	fail := func(source string) {
		check(source)
		if !slices.Contains(failed, source) {
			failed = append(failed, source)
		}
	}

	check("tasks")
	tasksByName := map[string]*v1alpha1.Task{}
	var candidates []intake.OverlapCandidate
	var list v1alpha1.TaskList
	if err := r.Client.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		fail("tasks")
	} else {
		for i := range list.Items {
			candidate := &list.Items[i]
			if candidate.Name == current.Name || !ordinaryOverlapTask(candidate) || !strings.EqualFold(candidate.Spec.Repository, current.Spec.Repository) {
				continue
			}
			tasksByName[candidate.Name] = candidate
			match := v1alpha1.OverlapMatch{Task: candidate.Name, Issue: candidate.Spec.Issue, Phase: candidate.Status.Phase}
			if pr := candidate.Status.PullRequest; pr != nil {
				match.PR = pr.Number
			}
			_, active := v1alpha1.OverlapPhase(candidate.Status.Phase)
			candidates = append(candidates, intake.OverlapCandidate{Match: match, Repository: candidate.Spec.Repository,
				Text: candidate.Spec.Text, Active: active, Recent: candidate.CreationTimestamp.Time})
		}
	}

	check("runs")
	currentRuns := map[string]bool{}
	for _, record := range current.Status.Runs {
		currentRuns[record.ID] = true
	}
	allRuns, err := r.Runs.List(ctx)
	runRooms := map[string]string{}
	if err != nil {
		fail("runs")
	} else {
		for _, run := range allRuns {
			runRooms[run.ID] = run.RoomRef
			candidate, ok := tasksByName[run.TaskID]
			if !ok || run.TaskID == current.Name || currentRuns[run.ID] {
				continue
			}
			match := v1alpha1.OverlapMatch{Task: candidate.Name, Run: run.ID, Issue: candidate.Spec.Issue, Phase: run.Phase, PR: runPR(run)}
			if match.PR == 0 && candidate.Status.PullRequest != nil {
				match.PR = candidate.Status.PullRequest.Number
			}
			recent := run.Created
			if run.Finished.After(recent) {
				recent = run.Finished
			}
			candidates = append(candidates, intake.OverlapCandidate{Match: match, Repository: candidate.Spec.Repository,
				Text: candidate.Spec.Text, Active: !run.Deleting && !runs.Terminal(run.Phase), Recent: recent})
		}
	}

	ranked := intake.RankOverlapCandidates(current.Spec.Text, acceptanceText(current.Spec.Text), candidates)
	e.readRooms(ctx, ranked, tasksByName, runRooms, check, fail)
	e.readGitHub(ctx, ranked, check, fail)
	result.Matches = make([]v1alpha1.OverlapMatch, len(ranked))
	for i := range ranked {
		result.Matches[i] = ranked[i].Match
	}

	switch {
	case len(failed) > 0:
		result.Classification = v1alpha1.OverlapUnverifiedOverlap
		result.Note = "candidate evidence unavailable: " + strings.Join(failed, ", ")
	case len(ranked) == 0:
		result.Classification = v1alpha1.OverlapNoMatch
		result.Note = "no duplicate found in checked sources"
	default:
		for _, candidate := range ranked {
			_, active := v1alpha1.OverlapPhase(candidate.Match.Phase)
			if candidate.Match.Relation == "exact" && (candidate.Active || active) {
				result.Classification = v1alpha1.OverlapActiveDuplicate
				result.Note = "exact active outcome match"
				return result
			}
		}
		result.Pending = true
	}
	return result
}

func runPR(run runs.Run) int {
	url := strings.TrimSuffix(run.PullRequest, "/")
	slash := strings.LastIndex(url, "/")
	if slash < 0 {
		return 0
	}
	n, _ := strconv.Atoi(url[slash+1:])
	return n
}

// readRooms enriches only retained candidates. Evidence remains in memory for candidate review;
// Task status carries its bounded IDs, never room payloads.
func (e taskOverlapEvaluator) readRooms(ctx context.Context, candidates []intake.OverlapCandidate,
	tasks map[string]*v1alpha1.Task, runRooms map[string]string, check, fail func(string)) {
	roomsByName := map[string][]int{}
	for i := range candidates {
		match := candidates[i].Match
		room := ""
		if task := tasks[match.Task]; task != nil {
			room = task.Status.RoomRef
		}
		if room == "" && match.Run != "" {
			room = runRooms[match.Run] // a lost Task status does not erase the run's room evidence
		}
		if room != "" {
			roomsByName[room] = append(roomsByName[room], i)
		}
	}
	names := slices.Sorted(maps.Keys(roomsByName))
	for _, room := range names {
		check("rooms")
		events, _, err := e.r.Rooms.EventsSince(ctx, room, 0, func(event envelope.Event) bool {
			return event.Type == envelope.Handoff || event.Type == envelope.Message
		})
		if err != nil {
			fail("rooms")
			continue
		}
		for _, index := range roomsByName[room] {
			for _, event := range events {
				if match := candidates[index].Match; match.Run != "" && event.RunID != "" && event.RunID != match.Run {
					continue
				}
				evidence := fmt.Sprintf("%s run=%s seq=%d %s", event.Type, event.RunID, event.Seq, event.Payload)
				candidates[index].Evidence = appendBoundedOverlapEvidence(candidates[index].Evidence, evidence)
			}
		}
	}
}

// readGitHub reads only refs of retained candidates, never an unrestricted issue/PR search.
func (e taskOverlapEvaluator) readGitHub(ctx context.Context, candidates []intake.OverlapCandidate, check, fail func(string)) {
	issues, prs := map[int]bool{}, map[int]bool{}
	for i := range candidates {
		match := candidates[i].Match
		if match.Issue > 0 && !issues[match.Issue] {
			issues[match.Issue] = true
			check("github")
			issue, err := e.r.gh(ctx).Issue(ctx, match.Issue)
			if err != nil {
				fail("github")
			} else {
				for j := range candidates {
					if candidates[j].Match.Issue == match.Issue {
						evidence := fmt.Sprintf("issue #%d state=%s url=%s", issue.Number, issue.State, issue.URL)
						candidates[j].Evidence = appendBoundedOverlapEvidence(candidates[j].Evidence, evidence)
					}
				}
			}
		}
		if match.PR > 0 && !prs[match.PR] {
			prs[match.PR] = true
			check("github")
			pr, err := e.r.gh(ctx).PullRequest(ctx, match.PR)
			if err != nil {
				fail("github")
			} else {
				for j := range candidates {
					if candidates[j].Match.PR == match.PR {
						evidence := fmt.Sprintf("pull request #%d state=%s url=%s", pr.Number, pr.State, pr.URL)
						candidates[j].Evidence = appendBoundedOverlapEvidence(candidates[j].Evidence, evidence)
					}
				}
			}
		}
	}
}

const (
	maxOverlapEvidenceItems = 8
	maxOverlapEvidenceBytes = 1024
)

func appendBoundedOverlapEvidence(evidence []string, item string) []string {
	if len(evidence) >= maxOverlapEvidenceItems {
		return evidence
	}
	return append(evidence, cut(item, maxOverlapEvidenceBytes))
}
