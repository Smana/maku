// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/narrate"
)

// The parent's filing reasons (v4 Issues and confirmations).
const (
	// ReasonFilingIncomplete is a filing that has started and not finished: visibly linked issues
	// may exist, and no child Task starts until every marker is durable.
	ReasonFilingIncomplete = "filing_incomplete"
	// ReasonFiled is every issue filed and recorded: the parent may admit its children.
	ReasonFiled = "filed"
)

// IssueForge is the writes filing makes in one repository, through that repository's own factory
// App connection; forge.GitHub implements it.
type IssueForge interface {
	CreateIssue(ctx context.Context, title, body string, labels []string) (forge.Issue, error)
	IssuesLabelled(ctx context.Context, label string) ([]forge.Issue, error)
	Comment(ctx context.Context, number int, body string) error
	RecentComments(ctx context.Context, number int) ([]forge.Comment, error)
}

// File files the parent issue in the primary, then one child issue per repository, then the plan
// repost, recording each issue in the parent's status as soon as it exists (v4 Issues and
// confirmations). Every authority and the capability gate are re-read before the first write. A
// crash leaves the parent filing_incomplete; a replay adopts each issue by its factory author and
// first-line marker instead of filing it twice. Only when all are recorded is the parent filed.
func (s *Service) File(ctx context.Context, parentTask string) error {
	t, err := s.parent(ctx, parentTask)
	if err != nil {
		return err
	}
	cs := t.Status.Coordination
	if t.Status.Reason == ReasonFiled {
		return nil
	}
	if err := s.ExecuteReady(ctx, cs, nil); err != nil {
		return err
	}
	p, hash := *cs.Plan, cs.PlanHash
	forges := map[string]IssueForge{}
	for _, c := range p.Children {
		f := s.issueForge(c.Repository)
		if f == nil {
			return fmt.Errorf("%w: %s has no App connection", ErrAppUnavailable, c.Repository)
		}
		forges[c.Repository] = f
	}
	if err := s.setStatus(ctx, parentTask, func(t *v1alpha1.Task) { t.Status.Reason = ReasonFilingIncomplete }); err != nil {
		return err
	}
	parentIssue := t.Spec.Issue
	if parentIssue == 0 {
		title, body := narrate.CoordinationParentIssue(parentTask, p, hash)
		parentIssue, err = s.fileOnce(ctx, forges[p.Primary.Repository], narrate.LabelCoordination, title, body, []string{narrate.LabelCoordination},
			func(i forge.Issue) bool {
				task, ok := narrate.CoordinationParentOf(i.Body)
				return ok && task == parentTask
			})
		if err != nil {
			return err
		}
		if err := s.linkParentIssue(ctx, parentTask, parentIssue); err != nil {
			return err
		}
	}
	links := map[string]string{}
	for _, c := range p.Children {
		if i := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == c.ID }); i >= 0 && cs.Children[i].Issue > 0 {
			links[c.ID] = issueURL(c.Repository, cs.Children[i].Issue)
			continue
		}
		title, body := narrate.CoordinationChildIssue(parentTask, p, c)
		n, err := s.fileOnce(ctx, forges[c.Repository], narrate.LabelCoordinationChild, title, body,
			[]string{narrate.LabelCoordinationChild, narrate.ClassLabel(c.Class)},
			func(i forge.Issue) bool {
				task, id, ok := narrate.CoordinationChildOf(i.Body)
				return ok && task == parentTask && id == c.ID
			})
		if err != nil {
			return err
		}
		rec := v1alpha1.CoordinationChildStatus{ID: c.ID, Repository: c.Repository, Fingerprint: ChildFingerprint(p, c.ID), Issue: n,
			Generation: 1, AllocationMicroUSD: c.BudgetMicroUSD}
		if err := s.setStatus(ctx, parentTask, func(t *v1alpha1.Task) { recordChild(t.Status.Coordination, rec) }); err != nil {
			return err
		}
		links[c.ID] = issueURL(c.Repository, n)
	}
	if err := s.repost(ctx, forges[p.Primary.Repository], parentTask, parentIssue, p, hash, links, Materiality{}); err != nil {
		return err
	}
	if err := s.setStatus(ctx, parentTask, func(t *v1alpha1.Task) { t.Status.Reason = ReasonFiled }); err != nil {
		return err
	}
	s.observe(func(m Metrics) { m.CoordinationFamily(ctx, p.Primary.Repository, "filed") })
	return nil
}

// parent is name's coordination parent with its current plan.
func (s *Service) parent(ctx context.Context, name string) (*v1alpha1.Task, error) {
	var t v1alpha1.Task
	switch err := s.Client.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: name}, &t); {
	case apierrors.IsNotFound(err):
		return nil, ErrParentNotFound
	case err != nil:
		return nil, err
	case t.Spec.Kind != v1alpha1.TaskKindCoordination:
		return nil, ErrParentConflict
	case t.Status.Coordination == nil || t.Status.Coordination.Plan == nil:
		return nil, ErrParentNotFound
	}
	return &t, nil
}

func (s *Service) issueForge(repository string) IssueForge {
	if s.Forges == nil {
		return nil
	}
	return s.Forges(repository)
}

// fileOnce is the factory-authored issue under label that match recognizes, else a new one.
func (s *Service) fileOnce(ctx context.Context, f IssueForge, label, title, body string, labels []string, match func(forge.Issue) bool) (int, error) {
	existing, err := f.IssuesLabelled(ctx, label)
	if err != nil {
		return 0, fmt.Errorf("%w: list %s: %w", ErrAccessUnverified, label, err)
	}
	for _, i := range existing {
		if forge.SameLogin(i.Author, s.Cfg.FactoryLogin) && match(i) {
			return i.Number, nil
		}
	}
	i, err := f.CreateIssue(ctx, title, body, labels)
	if err != nil {
		return 0, fmt.Errorf("file %s issue: %w", label, err)
	}
	return i.Number, nil
}

// repost comments the plan once per (parent, version, hash) on the parent issue. A comment is
// recovery evidence only: none is ever read back as control input.
func (s *Service) repost(ctx context.Context, f IssueForge, task string, issue int, p v1alpha1.CoordinationPlan, hash string, links map[string]string, m Materiality) error {
	marker := narrate.CoordinationRepostMarker(task, p.Version, hash)
	cs, err := f.RecentComments(ctx, issue)
	if err != nil {
		return fmt.Errorf("%w: comments: %w", ErrAccessUnverified, err)
	}
	for _, c := range cs {
		if forge.SameLogin(c.Author, s.Cfg.FactoryLogin) && strings.HasPrefix(c.Body, marker) {
			return nil
		}
	}
	return f.Comment(ctx, issue, narrate.CoordinationRepost(task, p, hash, m.ChangedSections, m.Reauthorize, m.ParentRequired, links))
}

// linkParentIssue records the parent issue on the parent Task, as the api intake links its issue.
func (s *Service) linkParentIssue(ctx context.Context, name string, issue int) error {
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"issue": issue},
		"metadata": map[string]any{"labels": map[string]string{v1alpha1.LabelIssue: strconv.Itoa(issue)}}})
	return s.Client.Patch(ctx, &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace}},
		client.RawPatch(types.MergePatchType, patch))
}

// setStatus applies fn to name's freshly read status and writes it, retrying a conflict.
func (s *Service) setStatus(ctx context.Context, name string, fn func(*v1alpha1.Task)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var t v1alpha1.Task
		if err := s.Client.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: name}, &t); err != nil {
			return err
		}
		fn(&t)
		return s.Client.Status().Update(ctx, &t)
	})
}

// recordChild writes rec as its child's lineage record, replacing an older one.
func recordChild(cs *v1alpha1.CoordinationStatus, rec v1alpha1.CoordinationChildStatus) {
	if i := slices.IndexFunc(cs.Children, func(x v1alpha1.CoordinationChildStatus) bool { return x.ID == rec.ID }); i >= 0 {
		cs.Children[i] = rec
		return
	}
	cs.Children = append(cs.Children, rec)
}

func issueURL(repository string, n int) string {
	return fmt.Sprintf("https://github.com/%s/issues/%d", repository, n)
}
