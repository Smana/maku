// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/hardening"
)

// AnnotationOutputIntent records, on the task, the non-push output the applier is about to write:
// "<output id> <sanitised digest>", written before apply so crash recovery recognises only the
// applier's own writes (SEC-H1, HR16).
const AnnotationOutputIntent = "agents.ogenki.io/output-intent"

func (a *Applier) intendedOutput(t *v1alpha1.Task, rs *runState, id, digest string) bool {
	return t.Annotations[AnnotationOutputIntent] == id+" "+digest && !rs.room.voidedOutputs[id+" "+digest]
}

func (a *Applier) recordOutputIntent(ctx context.Context, t *v1alpha1.Task, rs *runState, id, digest string) error {
	key := id + " " + digest
	delete(rs.room.voidedOutputs, key)
	if t.Annotations[AnnotationOutputIntent] == key {
		return nil
	}
	orig := t.DeepCopy()
	if t.Annotations == nil {
		t.Annotations = map[string]string{}
	}
	t.Annotations[AnnotationOutputIntent] = key
	if a.Tasks == nil {
		return nil
	}
	if err := a.Tasks.Patch(ctx, t, client.MergeFrom(orig)); err != nil {
		*t = *orig
		return fmt.Errorf("record the output's intent: %w", err)
	}
	return nil
}

func (a *Applier) clearOutputIntent(ctx context.Context, t *v1alpha1.Task, rs *runState, id, digest string) error {
	key := id + " " + digest
	rs.room.voidedOutputs[key] = true
	if t.Annotations[AnnotationOutputIntent] != key {
		return nil
	}
	orig := t.DeepCopy()
	delete(t.Annotations, AnnotationOutputIntent)
	if a.Tasks == nil {
		return nil
	}
	if err := a.Tasks.Patch(ctx, t, client.MergeFrom(orig)); err != nil {
		*t = *orig
		return fmt.Errorf("clear the output's intent: %w", err)
	}
	return nil
}

// baseRef is the branch every task's pull request targets: the base every run is cut from
// (reconciler implementerSpec).
const baseRef = "main"

// proposedLabel is the one label a run's follow-up issue gets: never the trigger label (HR11).
const proposedLabel = "factory/proposed"

// find looks for the checked request's own write on GitHub by the output id and sanitised content
// it carries (HR16, SEC-H1): for a PR, the Agent-Output footer plus normalised title and body; for
// a comment on the requested target, the marker plus normalised body; for an issue, the factory
// App's issue since the run started ending with the marker. A labels request carries no id: adding
// a label twice is a no-op.
func (a *Applier) find(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord, payload any, prov hardening.Provenance) (string, bool, error) {
	switch o := payload.(type) {
	case hardening.IssuePayload:
		return a.findIssue(ctx, t, rec, prov.Output)
	case hardening.PRPayload:
		pub, err := a.Publisher(ctx, t.Spec.Repository)
		if err != nil {
			return "", false, fmt.Errorf("connect the publisher: %w", err)
		}
		pr, err := branchPR(ctx, t, pub)
		if err != nil {
			return "", false, err
		}
		wantBody := prBody(t, o.Body, prov, a.RoomsURL)
		ok := pr.Number > 0 &&
			footerOutput(pr.Body) == prov.Output &&
			sameLogin(pr.Author, a.AgentsLogin) &&
			normText(pr.Title) == normText(o.Title) &&
			normText(pr.Body) == normText(wantBody)
		return pr.URL, ok, nil
	case hardening.CommentPayload:
		pub, err := a.Publisher(ctx, t.Spec.Repository)
		if err != nil {
			return "", false, fmt.Errorf("connect the publisher: %w", err)
		}
		n, kind := t.Spec.Issue, "issues"
		if o.Target == hardening.TargetPR {
			pr, err := branchPR(ctx, t, pub)
			if err != nil {
				return "", false, err
			}
			n, kind = pr.Number, "pull"
		}
		if n < 1 {
			return "", false, nil
		}
		recent, err := pub.RecentComments(ctx, n)
		if err != nil {
			return "", false, fmt.Errorf("read recent comments: %w", err)
		}
		wantBody := normText(hardening.WithMarker(o.Body, prov.Output))
		for _, c := range recent {
			if got, ok := hardening.FindMarker(c.Body); ok && got == prov.Output &&
				sameLogin(c.Author, a.AgentsLogin) && normText(c.Body) == wantBody {
				return commentURL(t, kind, n, c.ID), true, nil
			}
		}
		return "", false, nil
	}
	return "", false, nil
}

func normText(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), " \t\n")
}

// findIssue is the factory App's issue since the run started that ends with the marker. It reads
// by author, not by label, so a relabelled issue is still found and never filed twice.
func (a *Applier) findIssue(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord, id string) (string, bool, error) {
	f := a.filer(t)
	if f == nil || a.FactoryLogin == "" {
		return "", false, nil // nothing can have been filed: apply refuses it output_unavailable
	}
	since, err := f.IssuesByAuthorSince(ctx, a.FactoryLogin, rec.Started.Time)
	if err != nil {
		return "", false, fmt.Errorf("read the factory App's issues: %w", err)
	}
	for _, i := range since {
		if got, ok := hardening.FindMarker(i.Body); ok && got == id && sameLogin(i.Author, a.FactoryLogin) {
			return issueURL(t, i), true, nil
		}
	}
	return "", false, nil
}

func (a *Applier) filer(t *v1alpha1.Task) Filer {
	if a.Factory == nil {
		return nil
	}
	return a.Factory(t.Spec.Repository)
}

// apply writes one checked payload that find did not find. It answers the object written, or a
// refusal decided here (output_cap, output_target), or an error: forge.Unappliable ones are
// answered output_unavailable, the rest leave the request for the next tick.
func (a *Applier) apply(ctx context.Context, t *v1alpha1.Task, payload any, p hardening.Provenance, prs map[string]bool) (string, hardening.Refusal, error) {
	if is, ok := payload.(hardening.IssuePayload); ok {
		obj, err := a.applyIssue(ctx, t, is, p)
		return obj, "", err
	}
	pub, err := a.Publisher(ctx, t.Spec.Repository)
	if err != nil {
		return "", "", fmt.Errorf("connect the publisher: %w", err)
	}
	switch o := payload.(type) {
	case hardening.PRPayload:
		return a.applyPR(ctx, t, pub, o, p, prs)
	case hardening.CommentPayload:
		return applyComment(ctx, t, pub, o, p.Output)
	case hardening.LabelsPayload:
		return applyLabels(ctx, t, pub, o)
	}
	return "", hardening.RefuseType, nil
}

// branchPR is the newest pull request from the task's branch, whatever its state; a zero PR when
// it has none.
func branchPR(ctx context.Context, t *v1alpha1.Task, pub Publisher) (forge.PR, error) {
	n, err := pub.PullRequestForBranch(ctx, branch(t))
	if err != nil || n == 0 {
		return forge.PR{}, err
	}
	pr, err := pub.PullRequest(ctx, n)
	if err != nil {
		return forge.PR{}, fmt.Errorf("read pull request %d: %w", n, err)
	}
	return pr, nil
}

// openPR is the branch's pull request when it is open, else a zero PR.
func openPR(ctx context.Context, t *v1alpha1.Task, pub Publisher) (forge.PR, error) {
	pr, err := branchPR(ctx, t, pub)
	if err != nil || pr.State != "OPEN" {
		return forge.PR{}, err
	}
	return pr, nil
}

// applyPR opens the task's one pull request from agent/<task> into main, or edits it (one open and
// five edits per run; HR18: draft on open only). A closed or merged one is never replaced, and one
// the agents App did not open is not the task's to edit. Nor is an open one whose footer names none
// of the task's applied pr outputs: a run holding pull_requests: write opened it directly, and
// editing it would launder that write. Each is output_target, a refusal that spent its cap, so the
// counts rebuilt from the log (runState.answer) match the ones spent here. The body is the
// factory's: neutralised, with the provenance footer naming the output.
func (a *Applier) applyPR(ctx context.Context, t *v1alpha1.Task, pub Publisher, o hardening.PRPayload, p hardening.Provenance,
	ours map[string]bool) (string, hardening.Refusal, error) {
	pr, err := branchPR(ctx, t, pub)
	switch {
	case err != nil:
		return "", "", err
	case pr.Number > 0 && pr.State != "OPEN":
		return "", hardening.RefuseTarget, nil
	case pr.Number > 0 && !sameLogin(pr.Author, a.AgentsLogin):
		return "", hardening.RefuseTarget, nil
	case pr.Number > 0 && !ours[footerOutput(pr.Body)]:
		a.Log.Warn("output applier: the branch's pull request was not opened by an applied output", "task.id", t.Name,
			"run.id", p.Run, "output.id", p.Output, "pr", pr.Number)
		return "", hardening.RefuseTarget, nil
	}
	body := prBody(t, o.Body, p, a.RoomsURL)
	if pr.Number > 0 {
		if pr, err = pub.EditPR(ctx, pr.Number, o.Title, body); err != nil {
			return "", "", fmt.Errorf("edit the pull request: %w", err)
		}
		return pr.URL, "", nil
	}
	draft := o.Draft || isDraftRoutine(t)
	pr, err = pub.CreatePR(ctx, baseRef, branch(t), o.Title, body, draft)
	if errors.Is(err, forge.ErrPRExists) {
		return "", "", errors.New("the branch's pull request appeared since it was read: retried next tick")
	}
	if err != nil {
		return "", "", fmt.Errorf("open the pull request: %w", err)
	}
	return pr.URL, "", nil
}

func isDraftRoutine(t *v1alpha1.Task) bool {
	return t != nil && t.Spec.Source.Kind == v1alpha1.SourceKindRoutine &&
		t.Spec.Routine != nil && t.Spec.Routine.Mode == config.RoutineModeDraft
}

// footerOutput is the Agent-Output a factory footer names, read from the body's last paragraph
// only: WithFooter marks any such line the model wrote above it.
func footerOutput(body string) string {
	_, id := hardening.SplitFooter(body)
	return id
}

func commentURL(t *v1alpha1.Task, kind string, n int, id int64) string {
	return fmt.Sprintf("https://github.com/%s/%s/%d#issuecomment-%d", t.Spec.Repository, kind, n, id)
}

// applyComment comments on the task's open pull request or its issue, the body ending with the
// output's marker.
func applyComment(ctx context.Context, t *v1alpha1.Task, pub Publisher, o hardening.CommentPayload, id string) (string, hardening.Refusal, error) {
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
	c, err := pub.CommentWithID(ctx, n, hardening.WithMarker(o.Body, id))
	if err != nil {
		return "", "", fmt.Errorf("comment: %w", err)
	}
	return commentURL(t, kind, n, c), "", nil
}

// applyLabels adds allowlisted labels to the task's open pull request.
func applyLabels(ctx context.Context, t *v1alpha1.Task, pub Publisher, o hardening.LabelsPayload) (string, hardening.Refusal, error) {
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

func issueURL(t *v1alpha1.Task, i forge.Issue) string {
	if i.URL != "" {
		return i.URL
	}
	return fmt.Sprintf("https://github.com/%s/issues/%d", t.Spec.Repository, i.Number)
}

// applyIssue files the run's follow-up as the factory App, labelled factory/proposed alone, with a
// paragraph naming its task, run and output, and the marker (HR11). With no factory App connection
// it is forge.ErrNoConnection, which no retry changes.
func (a *Applier) applyIssue(ctx context.Context, t *v1alpha1.Task, o hardening.IssuePayload, p hardening.Provenance) (string, error) {
	f := a.filer(t)
	if f == nil || a.FactoryLogin == "" {
		return "", forge.ErrNoConnection
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
	return issueURL(t, i), nil
}
