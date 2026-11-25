// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/reconciler"
)

// BacktestRequest is one edited policy file, binding set, or reusable policy to replay.
type BacktestRequest struct {
	Kind     string             `json:"kind"`
	File     string             `json:"file,omitempty"`
	Bindings []config.Binding   `json:"bindings,omitempty"`
	Policy   string             `json:"policy,omitempty"`
	Spec     *config.PolicySpec `json:"spec,omitempty"`
}

// BacktestSource reads the immutable pull request, its checks, and its recorded diff.
type BacktestSource interface {
	PullRequest(ctx context.Context, number int) (forge.PR, error)
	PullRequestChecks(ctx context.Context, number int) (forge.Checks, error)
	Diff(ctx context.Context, base, head string) (forge.Diff, error)
}

// BacktestCounts summarizes every newest per-policy decision considered.
type BacktestCounts struct {
	Considered int `json:"considered"`
	Changed    int `json:"changed"`
	Unchanged  int `json:"unchanged"`
	Unknown    int `json:"unknown"`
	Matched    int `json:"matched"`
}

// BacktestRow is one changed or unknown decision, linked to its real pull request.
type BacktestRow struct {
	PullRequest int                `json:"pullRequest"`
	URL         string             `json:"url"`
	Policy      string             `json:"policy"`
	Before      reconciler.Outcome `json:"before"`
	After       reconciler.Outcome `json:"after"`
	Change      string             `json:"change"`
}

// Backtest is the last-30-days replay. Rows contains changes and unknowns, never unchanged rows.
type Backtest struct {
	Valid      bool           `json:"valid"`
	Errors     []string       `json:"errors,omitempty"`
	Repository string         `json:"repository"`
	Since      string         `json:"since"`
	Counts     BacktestCounts `json:"counts"`
	Rows       []BacktestRow  `json:"rows"`
}

type replayRecord struct {
	pr       evidence.PR
	decision RecentDecision
}

// Replay re-decides recent recorded diffs under an edited policy. Missing immutable references,
// unreadable diffs, and oracle findings are unknown rather than unchanged.
func Replay(ctx context.Context, cfg *config.Config, rc *config.Repository, rep evidence.Report, req BacktestRequest, recent Recent, source BacktestSource) Backtest {
	out := Backtest{Repository: rc.Name, Since: recent.Since, Rows: []BacktestRow{}}
	classes, errs := replayClasses(cfg, rc, rep, req)
	if len(errs) > 0 {
		out.Errors = errs
		return out
	}
	out.Valid = true
	prs := map[int]evidence.PR{}
	for _, pr := range recent.pullRequests {
		prs[pr.Number] = pr
	}
	latest := map[string]bool{}
	var records []replayRecord
	for _, d := range recent.Decisions {
		key := fmt.Sprintf("%d\x00%s", d.PullRequest, d.Policy)
		if latest[key] {
			continue
		}
		latest[key] = true
		records = append(records, replayRecord{pr: prs[d.PullRequest], decision: d})
	}
	cache := prefetch(ctx, records, cfg, rc, source)
	for _, rec := range records {
		d := rec.decision
		out.Counts.Considered++
		row := BacktestRow{PullRequest: d.PullRequest, URL: d.CommentURL, Policy: d.Policy,
			Before: reconciler.Outcome{Verdict: d.Outcome, Reason: recordedReason(d.Reason)}, Change: "unknown"}
		input, err := replayInputFor(ctx, rec, cfg, rc, source, cache)
		if err == nil && classes[d.Policy] != nil && classes[d.Policy].Oracle != nil {
			err = errors.New("oracle findings were not recorded")
		}
		if err != nil {
			row.After = reconciler.Outcome{Verdict: "unknown", Reason: "diff_unrecorded"}
			out.Counts.Unknown++
			out.Rows = append(out.Rows, row)
			continue
		}
		input.Task.Spec.PredictedClass = d.Policy
		decision := reconciler.DecideRecorded(input, classes[d.Policy], d.Rows.Reviewer)
		row.After = reconciler.Outcome{Verdict: decision.Verdict, Reason: decision.Reason}
		if decision.Matched == d.Policy && (decision.Verdict == "arm" || decision.Verdict == "shadow") {
			out.Counts.Matched++
		}
		if row.Before == row.After {
			out.Counts.Unchanged++
			continue
		}
		row.Change = "changed"
		out.Counts.Changed++
		out.Rows = append(out.Rows, row)
	}
	return out
}

func recordedReason(reason string) string {
	if reason == "clean" || reason == "armed" {
		return ""
	}
	return reason
}

// replayParallel bounds the pull requests a backtest reads at once. Each costs about five GitHub
// reads (the pull request, its checks, the compare and both sides of each file), some 2.5 s in
// all: read one after another, ai-native-ref's 17 decided pull requests outlast the broker's 15 s
// call to the factory.
const replayParallel = 6

// prefetch reads each record's pull request inputs, replayParallel at once; the first record of a
// pull request, its newest decision, is the one read, as replayInputFor would.
func prefetch(ctx context.Context, records []replayRecord, cfg *config.Config, rc *config.Repository, source BacktestSource) map[int]replayInput {
	cache := map[int]replayInput{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, replayParallel)
	seen := map[int]bool{}
	for _, rec := range records {
		n := rec.decision.PullRequest
		if seen[n] {
			continue
		}
		seen[n] = true
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			var got replayInput
			got.err = buildReplayInput(ctx, rec, cfg, rc, source, &got.input)
			mu.Lock()
			cache[n] = got
			mu.Unlock()
		}()
	}
	wg.Wait()
	return cache
}

type replayInput struct {
	input reconciler.ArmInputs
	err   error
}

func replayInputFor(ctx context.Context, rec replayRecord, cfg *config.Config, rc *config.Repository, source BacktestSource, cache map[int]replayInput) (reconciler.ArmInputs, error) {
	got, ok := cache[rec.decision.PullRequest]
	if !ok {
		got.err = buildReplayInput(ctx, rec, cfg, rc, source, &got.input)
		cache[rec.decision.PullRequest] = got
	}
	in := got.input
	if in.Task == nil {
		return in, got.err
	}
	in.Files.Base = copyStrings(in.Files.Base)
	in.Files.Head = copyStrings(in.Files.Head)
	in.Stats = append([]forge.FileStat(nil), in.Stats...)
	task := *in.Task
	in.Task = &task
	return in, got.err
}

func copyStrings(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func buildReplayInput(ctx context.Context, rec replayRecord, cfg *config.Config, rc *config.Repository, source BacktestSource, in *reconciler.ArmInputs) error {
	if source == nil || rec.pr.MergeCommitOid == "" || rec.decision.Head == "" || rec.decision.Head != rec.pr.HeadRefOid {
		return errors.New("the recorded diff references are unavailable")
	}
	pr, err := source.PullRequest(ctx, rec.pr.Number)
	if err != nil || pr.HeadSHA != rec.decision.Head {
		return errors.New("the recorded pull request is unavailable")
	}
	if pr.BaseRef == "" {
		pr.BaseRef = rec.pr.BaseRefName
	}
	checks, err := source.PullRequestChecks(ctx, pr.Number)
	if err != nil {
		return errors.New("the recorded checks are unavailable")
	}
	diff, err := source.Diff(ctx, rec.pr.MergeCommitOid+"^1", rec.decision.Head)
	if err != nil {
		return errors.New("the recorded diff is unavailable")
	}
	*in = reconciler.ArmInputs{PR: pr, Checks: checks, Cfg: cfg, DiffRead: true,
		Task: &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Repository: rc.Name, PredictedClass: rec.decision.Policy}}}
	in.Files.Base, in.Files.Head, in.Stats = diff.Base, diff.Head, diff.Files
	return nil
}

func replayClasses(cfg *config.Config, rc *config.Repository, rep evidence.Report, req BacktestRequest) (map[string]*config.Class, []string) {
	out := map[string]*config.Class{}
	switch req.Kind {
	case "", "file":
		pv := Check(cfg, rc, rep, req.File)
		if !pv.Valid {
			return nil, pv.Errors
		}
		next, err := mergepolicy.Parse([]byte(req.File))
		if err != nil {
			return nil, []string{err.Error()}
		}
		checked, err := mergepolicy.ValidateFile(next, rc, cfg)
		if err != nil {
			return nil, strings.Split(err.Error(), "\n")
		}
		for name, resolved := range checked.Classes {
			cl := resolved.Class
			out[name] = &cl
		}
	case "binding":
		pv := CheckBindings(cfg, rc, rep, req.Bindings)
		if !pv.Valid {
			return nil, pv.Errors
		}
		for _, b := range req.Bindings {
			p, ok := cfg.Policies[b.Name]
			if !ok {
				continue
			}
			cl, refused := config.Resolve(b.Name, p.Spec, b, policyContext(cfg), rc.Merge)
			if refused != nil {
				return nil, []string{fmt.Sprintf("%s: %s: %s", b.Name, refused.Reason, refused.Message)}
			}
			out[b.Name] = &cl
		}
	case "policy":
		if req.Spec == nil || req.Policy == "" {
			return nil, []string{"policy and spec are required"}
		}
		pv := CheckPolicy(cfg, req.Policy, *req.Spec, "", func(string) bool { return true })
		if !pv.Valid {
			return nil, pv.Errors
		}
		for _, b := range rc.Bindings {
			if b.Name != req.Policy {
				continue
			}
			cl, refused := config.Resolve(req.Policy, *req.Spec, b, policyContext(cfg), rc.Merge)
			if refused != nil {
				return nil, []string{fmt.Sprintf("%s: %s: %s", b.Name, refused.Reason, refused.Message)}
			}
			out[req.Policy] = &cl
		}
	default:
		return nil, []string{"backtest kind is file, binding or policy"}
	}
	return out, nil
}
