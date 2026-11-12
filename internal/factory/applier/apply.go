// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/hardening"
)

// baseRef is the branch every task's pull request targets: the base every run is cut from
// (reconciler implementerSpec).
const baseRef = "main"

// proposedLabel is the one label a run's follow-up issue gets: never the trigger label (HR11).
const proposedLabel = "factory/proposed"

// apply writes one checked payload, looking for its own earlier write first (HR16). It answers the
// object written, or a refusal decided here (output_target, HR17), or an error that leaves the
// request for the next tick.
func (a *Applier) apply(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord, payload any, p hardening.Provenance) (string, hardening.Refusal, error) {
	if is, ok := payload.(hardening.IssuePayload); ok {
		obj, err := a.applyIssue(ctx, t, rec, is, p)
		return obj, "", err
	}
	pub, err := a.Publisher(ctx, t.Spec.Repository)
	if err != nil {
		return "", "", fmt.Errorf("connect the publisher: %w", err)
	}
	switch o := payload.(type) {
	case hardening.PRPayload:
		obj, err := a.applyPR(ctx, t, pub, o, p)
		return obj, "", err
	case hardening.CommentPayload:
		return a.applyComment(ctx, t, pub, o, p.Output)
	case hardening.LabelsPayload:
		return a.applyLabels(ctx, t, pub, o)
	}
	return "", hardening.RefuseType, nil
}

// openPR is the task's open pull request, by its branch; a zero PR when it has none open.
func openPR(ctx context.Context, t *v1alpha1.Task, pub Publisher) (forge.PR, error) {
	n, err := pub.PullRequestForBranch(ctx, branch(t))
	if err != nil || n == 0 {
		return forge.PR{}, err
	}
	pr, err := pub.PullRequest(ctx, n)
	if err != nil {
		return forge.PR{}, fmt.Errorf("read pull request %d: %w", n, err)
	}
	if pr.State != "OPEN" {
		return forge.PR{}, nil
	}
	return pr, nil
}

// applyPR opens the task's pull request from agent/<task> into main, or edits the open one (HR18:
// draft on open only). The body is the factory's: neutralised, with the provenance footer naming
// the output. A body whose footer already names it is this request's earlier write.
func (a *Applier) applyPR(ctx context.Context, t *v1alpha1.Task, pub Publisher, o hardening.PRPayload, p hardening.Provenance) (string, error) {
	pr, err := openPR(ctx, t, pub)
	if err != nil {
		return "", err
	}
	if pr.Number > 0 && footerOutput(pr.Body) == p.Output {
		return pr.URL, nil
	}
	body := hardening.WithFooter(o.Body, p)
	if pr.Number > 0 {
		if pr, err = pub.EditPR(ctx, pr.Number, o.Title, body); err != nil {
			return "", fmt.Errorf("edit the pull request: %w", err)
		}
		return pr.URL, nil
	}
	pr, err = pub.CreatePR(ctx, baseRef, branch(t), o.Title, body, o.Draft)
	if errors.Is(err, forge.ErrPRExists) {
		return "", errors.New("the branch's pull request appeared since it was read: retried next tick")
	}
	if err != nil {
		return "", fmt.Errorf("open the pull request: %w", err)
	}
	return pr.URL, nil
}

// footerOutput is the Agent-Output a factory footer names, read from the body's last paragraph
// only: WithFooter marks any such line the model wrote above it.
func footerOutput(body string) string {
	text := strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \t\n")
	i := strings.LastIndex(text, "\n---\n")
	if i < 0 {
		return ""
	}
	for _, l := range strings.Split(text[i+len("\n---\n"):], "\n") {
		if id, ok := strings.CutPrefix(l, "Agent-Output: "); ok {
			return id
		}
	}
	return ""
}

// applyComment comments on the task's open pull request or its issue, the body ending with the
// output's marker. A recent comment of the agents App ending with it is this request's earlier
// write.
func (a *Applier) applyComment(ctx context.Context, t *v1alpha1.Task, pub Publisher, o hardening.CommentPayload, id string) (string, hardening.Refusal, error) {
	n, kind := t.Spec.Issue, "issues"
	if o.Target == hardening.TargetPR {
		pr, err := openPR(ctx, t, pub)
		if err != nil {
			return "", "", err
		}
		n, kind = pr.Number, "pull"
	}
	if n < 1 {
		return "", hardening.RefuseTarget, nil
	}
	url := func(c int64) string {
		return fmt.Sprintf("https://github.com/%s/%s/%d#issuecomment-%d", t.Spec.Repository, kind, n, c)
	}
	recent, err := pub.RecentComments(ctx, n)
	if err != nil {
		return "", "", fmt.Errorf("read recent comments: %w", err)
	}
	if i := slices.IndexFunc(recent, func(c forge.Comment) bool {
		got, ok := hardening.FindMarker(c.Body)
		return ok && got == id && sameLogin(c.Author, a.AgentsLogin)
	}); i >= 0 {
		return url(recent[i].ID), "", nil
	}
	c, err := pub.CommentWithID(ctx, n, hardening.WithMarker(o.Body, id))
	if err != nil {
		return "", "", fmt.Errorf("comment: %w", err)
	}
	return url(c), "", nil
}

// applyLabels adds allowlisted labels to the task's open pull request: adding a label twice is a
// no-op on GitHub, so a replay needs no marker.
func (a *Applier) applyLabels(ctx context.Context, t *v1alpha1.Task, pub Publisher, o hardening.LabelsPayload) (string, hardening.Refusal, error) {
	pr, err := openPR(ctx, t, pub)
	if err != nil {
		return "", "", err
	}
	if pr.Number < 1 {
		return "", hardening.RefuseTarget, nil
	}
	if err := pub.AddLabels(ctx, pr.Number, o.Add...); err != nil {
		return "", "", fmt.Errorf("add labels: %w", err)
	}
	return pr.URL, "", nil
}

// applyIssue files the run's follow-up as the factory App, labelled factory/proposed alone, with a
// paragraph naming its task, run and output, and the marker (HR11). A factory/proposed issue of
// the factory App since the run started that ends with the marker is this request's earlier write.
func (a *Applier) applyIssue(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord, o hardening.IssuePayload, p hardening.Provenance) (string, error) {
	var f Filer
	if a.Factory != nil {
		f = a.Factory(t.Spec.Repository)
	}
	if f == nil {
		return "", errors.New("no factory App connection for the repository")
	}
	issueURL := func(i forge.Issue) string {
		if i.URL != "" {
			return i.URL
		}
		return fmt.Sprintf("https://github.com/%s/issues/%d", t.Spec.Repository, i.Number)
	}
	since, err := f.IssuesLabelledSince(ctx, proposedLabel, rec.Started.Time)
	if err != nil {
		return "", fmt.Errorf("read factory/proposed issues: %w", err)
	}
	for _, i := range since {
		if got, ok := hardening.FindMarker(i.Body); ok && got == p.Output && sameLogin(i.Author, a.FactoryLogin) {
			return issueURL(i), nil
		}
	}
	about := fmt.Sprintf("Proposed by agent run `%s` of task `%s` (output `%s`).", p.Run, p.Task, p.Output)
	if p.TaskURL != "" {
		about = fmt.Sprintf("Proposed by agent run `%s` of task `%s` (%s), output `%s`.", p.Run, p.Task, p.TaskURL, p.Output)
	}
	body := hardening.WithMarker(strings.TrimRight(o.Body, " \t\r\n")+"\n\n---\n"+about, p.Output)
	i, err := f.CreateIssue(ctx, o.Title, body, []string{proposedLabel})
	if err != nil {
		return "", fmt.Errorf("file the issue: %w", err)
	}
	return issueURL(i), nil
}
