// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/narrate"
	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/factory/taskid"
)

// reviews reads factory/review on open pull requests (phase F4): a maintainer's or the pull
// request author's label creates a read-only review Task (outcome: review, source.kind: review)
// and consumes the label; anyone else's label is refused once and removed.
func (p *IssuePoller) reviews(ctx context.Context, cfg *config.Config) error {
	items, err := p.Forge.Labeled(ctx, LabelReview)
	if err != nil || len(items) == 0 {
		return err
	}
	var errs []error
	for _, it := range items {
		if !it.PullRequest {
			continue // factory/review acts on pull requests only
		}
		if err := p.reviewOne(ctx, cfg, it.Number); err != nil {
			errs = append(errs, fmt.Errorf("review #%d: %w", it.Number, err))
		}
	}
	return errors.Join(errs...)
}

func (p *IssuePoller) refuseReview(ctx context.Context, cfg *config.Config, n int, last forge.LabelEvent, reason string) error {
	if err := p.narrator(cfg).PostOnce(ctx, n, fmt.Sprintf("pr-%d", n), narrate.Refused(n, reason, last.At)); err != nil {
		return err
	}
	return p.Forge.RemoveLabel(ctx, n, LabelReview)
}

func (p *IssuePoller) reviewOne(ctx context.Context, cfg *config.Config, n int) error {
	evs, ok, err := p.labelEvents(ctx, n, LabelReview)
	if err != nil || !ok {
		return err
	}
	pr, err := p.Forge.PullRequest(ctx, n)
	if err != nil {
		return err
	}
	gen, last, byMaintainer := Generation(evs, p.repo(cfg).IsMaintainer)
	authorized := false
	if byMaintainer {
		w, err := p.writer(ctx, last.Actor)
		if err != nil {
			return err // transient write check failure: wait for next poll
		}
		authorized = w
	}
	if !authorized && pr.Author != "" && forge.SameLogin(last.Actor, pr.Author) &&
		!forge.SameLogin(last.Actor, cfg.FactoryLogin) && !forge.SameLogin(last.Actor, cfg.AgentsLogin) {
		authorized = true
	}
	if !authorized {
		return p.refuseReview(ctx, cfg, n, last, "unauthorised_labeller")
	}
	if pr.State != "" && pr.State != "OPEN" {
		return p.Forge.RemoveLabel(ctx, n, LabelReview)
	}
	if p.Demo || !p.repo(cfg).PrivateCodeAcknowledged() {
		switch public, err := p.Forge.Public(ctx); {
		case err != nil:
			return fmt.Errorf("repository visibility: %w", err)
		case !public && p.Demo:
			return p.refuseReview(ctx, cfg, n, last, "private_repository")
		case !public:
			return p.refuseReview(ctx, cfg, n, last, "private_code_unacknowledged")
		}
	}
	raw := fmt.Sprintf("# %s\n\n%s", strings.TrimSpace(pr.Title), strings.TrimSpace(pr.Body))
	if strings.TrimSpace(pr.Title) == "" && strings.TrimSpace(pr.Body) == "" {
		raw = fmt.Sprintf("Review pull request #%d (%s).", n, pr.HeadRef)
	}
	clean := p.Sanitize
	if clean == nil {
		clean = sanitize.Text
	}
	text, rep := clean(raw)
	if rep.Withheld {
		return p.refuseReview(ctx, cfg, n, last, "unsanitisable")
	}
	if len(text) > maxSnapshot {
		text = strings.ToValidUTF8(text[:maxSnapshot], "")
	}
	sumBytes := sha256.Sum256([]byte(raw))
	sum := hex.EncodeToString(sumBytes[:])

	repo := p.repo(cfg).Name
	ref := fmt.Sprintf("%s#%d", repo, n)
	key := taskid.ReviewKey(repo, n, gen)
	name := taskid.Name(key)
	open, err := p.openReviewTasks(ctx, cfg, ref)
	if err != nil {
		return err
	}
	for _, t := range open {
		if t.Name != name {
			return p.refuseReview(ctx, cfg, n, last, "task_active")
		}
	}
	if !slices.ContainsFunc(open, func(t *v1alpha1.Task) bool { return t.Name == name }) {
		if spent, err := p.personSpent(ctx, cfg, n, last); err != nil || spent {
			return err
		}
	}
	t := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: p.Namespace,
			Labels:    map[string]string{v1alpha1.LabelIssue: strconv.Itoa(n)},
		},
		Spec: v1alpha1.TaskSpec{
			Source: v1alpha1.Source{
				Kind:          "review",
				Ref:           ref,
				Key:           key,
				RequestedBy:   "github:" + last.Actor,
				Trust:         "untrusted",
				ContentSHA256: sum,
			},
			Repository:  repo,
			IssueAuthor: pr.Author,
			Text:        text,
			DataClass:   cfg.Defaults.DataClass,
			Outcome:     config.OutcomeReview,
		},
	}
	if err := p.Client.Create(ctx, t); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create review task %s: %w", name, err)
	}
	return p.Forge.RemoveLabel(ctx, n, LabelReview)
}

func (p *IssuePoller) openReviewTasks(ctx context.Context, cfg *config.Config, ref string) ([]*v1alpha1.Task, error) {
	var l v1alpha1.TaskList
	if err := p.Client.List(ctx, &l, client.InNamespace(p.Namespace)); err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	var open []*v1alpha1.Task
	for i := range l.Items {
		t := &l.Items[i]
		if !v1alpha1.TerminalPhase(t.Status.Phase) && p.owns(cfg, t) &&
			t.Spec.Source.Kind == "review" && t.Spec.Source.Ref == ref {
			open = append(open, t)
		}
	}
	return open, nil
}
