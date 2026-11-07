// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"regexp"
	"slices"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/intake"
	"github.com/Smana/maku/internal/factory/narrate"
)

// reviewTarget is where review-driven narration goes: the pull request the maintainer reviewed,
// else target's issue. Revising on the issue went unseen by the maintainer reading the pull request
// (cloud-native-ref#2737).
func reviewTarget(t *v1alpha1.Task) int {
	if pr := t.Status.PullRequest; pr != nil && pr.Number > 0 {
		return pr.Number
	}
	return target(t)
}

// codeSpanPath is a code span holding one path-like token: no space, and a "/" or a ".".
var codeSpanPath = regexp.MustCompile("`([^`\\s]*[/.][^`\\s]*)`")

// taskPaths are the repository paths the task's snapshot names: its `## Files` lines, then the code
// spans of its acceptance check that look like a path. Sorted, each once. A token that is not a path
// costs nothing: only a path the revision changed and the pull request no longer touches is reported.
func taskPaths(text string) []string {
	paths := intake.ProposalPaths(text)
	for _, m := range codeSpanPath.FindAllStringSubmatch(acceptanceText(text), -1) {
		paths = append(paths, m[1])
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// revisionStart is the head the current revision started on: recorded on the run a maintainer's
// review triggered, which a resumed run carries on.
func revisionStart(t *v1alpha1.Task) string {
	for i := len(t.Status.Runs) - 1; i >= 0; i-- {
		if r := t.Status.Runs[i]; r.Trigger != "resume" {
			return r.HeadSHA
		}
	}
	return ""
}

// revision is what the revision run pushed, read from GitHub: the files changed from its start head
// to pr's, and the task's paths it took out of the pull request (acceptance drift). A read GitHub
// refuses is said in the comment, never a reason to hold the hand-off: the maintainer reads the diff.
func (r *Reconciler) revision(ctx context.Context, t *v1alpha1.Task, pr forge.PR) narrate.Revision {
	rv := narrate.Revision{PR: pr.Number, Repository: t.Spec.Repository, From: revisionStart(t), Head: pr.HeadSHA}
	if rv.From == "" || rv.Head == "" || rv.Head == rv.From {
		return rv
	}
	files, err := r.merger(ctx).ChangedFiles(ctx, rv.From, rv.Head)
	if err != nil {
		r.log().Warn("revision files unread", "task.id", t.Name, "from", rv.From, "head", rv.Head, "err", err)
		return rv
	}
	rv.Files, rv.FilesRead = files, true
	named := slices.DeleteFunc(taskPaths(t.Spec.Text), func(p string) bool { return !slices.Contains(files, p) })
	if len(named) == 0 {
		return rv
	}
	fs, err := r.merger(ctx).PullFiles(ctx, pr.Number)
	if err != nil {
		// The drift cannot be judged: the comment's file list names the task's paths the revision changed.
		r.log().Warn("pull request files unread", "task.id", t.Name, "number", pr.Number, "err", err)
		return rv
	}
	for _, p := range named {
		if !slices.ContainsFunc(fs, func(f forge.FilePatch) bool { return f.Path == p || f.Previous == p }) {
			rv.Dropped = append(rv.Dropped, p)
		}
	}
	return rv
}
