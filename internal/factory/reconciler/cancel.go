// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"sync"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
)

// The stop reasons of #644: a cancel or a discard from the task's room, the CLI or the MCP tool,
// and an issue deleted on GitHub under a task still running.
const (
	StopCancelled    = "cancelled"
	StopDiscarded    = "discarded"
	StopIssueDeleted = "issue_deleted"
)

// issueCloser and branchDeleter are what a discard writes beyond TaskForge: *forge.GitHub has both.
// The branch goes through the merger App, which holds contents: write; the factory App's own set
// is contents: read once a separate merger App is configured (docs/github-apps.md).
type (
	issueCloser interface {
		CloseIssueNotPlanned(ctx context.Context, number int) error
	}
	branchDeleter interface {
		DeleteBranch(ctx context.Context, branch string) error
	}
)

// issueProber reads whether a task's issue is gone on GitHub (404 or 410); *forge.GitHub has it.
type issueProber interface {
	IssueGone(ctx context.Context, number int) (bool, error)
}

// issueProbeEvery bounds the deleted-issue probe to one REST read per task per period: a deleted
// issue ends its task within it, and a busy factory does not spend its rate limit on the check.
const issueProbeEvery = 5 * time.Minute

// probeClock remembers when each task's issue was last probed, per process: a restart probes
// again at once, which is harmless.
type probeClock struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// due reports whether task's probe is due at now, and if so records it.
func (c *probeClock) due(task string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = map[string]time.Time{}
	}
	if at, ok := c.last[task]; ok && now.Sub(at) < issueProbeEvery {
		return false
	}
	c.last[task] = now
	return true
}

// forget drops an ended task's entry.
func (c *probeClock) forget(task string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.last, task)
}

// issueDeleted reports whether t's issue is gone on GitHub: the task then ends (#644) instead of
// running on with nowhere to narrate. A failed read is no answer: the next probe asks again.
func (r *Reconciler) issueDeleted(ctx context.Context, t *v1alpha1.Task) bool {
	if t.Spec.Issue <= 0 {
		return false
	}
	p, ok := r.gh(ctx).(issueProber)
	if !ok || !r.probes.due(t.Name, r.Now()) {
		return false
	}
	gone, err := p.IssueGone(ctx, t.Spec.Issue)
	if err != nil {
		r.log().Warn("issue probe failed", "task.id", t.Name, "issue", t.Spec.Issue, "err", err)
		return false
	}
	return gone
}

// discard is a discard's clean-up, after its runs are stopped (#644): the pull request closed with
// a comment, the agent/<task> branch deleted and the issue closed as not planned, since a GitHub
// App cannot delete an issue. The ending's comment on the issue says the rest. Each step is
// repeatable, so a pass that fails midway replays them all; an issue already gone is skipped.
func (r *Reconciler) discard(ctx context.Context, t *v1alpha1.Task) error {
	gh := r.gh(ctx)
	if pr := t.Status.PullRequest; pr != nil && pr.Number > 0 {
		if err := gh.Comment(ctx, pr.Number, discardPRComment); err != nil && !permanentErr(err) {
			return err
		}
		if err := gh.ClosePR(ctx, pr.Number); err != nil && !permanentErr(err) {
			return err
		}
	}
	bd, ok := r.merger(ctx).(branchDeleter)
	if !ok {
		bd, ok = gh.(branchDeleter)
	}
	if ok {
		if err := bd.DeleteBranch(ctx, "agent/"+t.Name); err != nil {
			return err
		}
	} else {
		r.log().Warn("discard: no connection deletes the branch", "task.id", t.Name)
	}
	if ic, ok := gh.(issueCloser); ok && t.Spec.Issue > 0 {
		if err := ic.CloseIssueNotPlanned(ctx, t.Spec.Issue); err != nil && !permanentErr(err) {
			return err
		}
	}
	return nil
}

// discardPRComment is the pull request's last word from a discard.
const discardPRComment = "This task was discarded from its room: the factory closes this pull request and deletes its branch."

func permanentErr(err error) bool {
	var pe *forge.PermanentError
	return errors.As(err, &pe)
}
