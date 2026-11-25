// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/narrate"
	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/factory/taskid"
	"github.com/Smana/maku/internal/intake"
)

// Outcome end reasons and commands for phases F4 and F5.
const (
	ReasonReviewPosted   = "review_posted"
	ReasonFindingsPosted = "findings_posted"
	cmdFix               = "/factory fix"
	maxFixSnapshotBytes  = 65536
)

// reviewPRNumber returns the pull request number targeted by a review task: first from source.ref
// (owner/name#<n>), else from the first GitHub pull request URL in spec.text.
func reviewPRNumber(t *v1alpha1.Task) int {
	if i := strings.LastIndex(t.Spec.Source.Ref, "#"); i >= 0 {
		if n, err := strconv.Atoi(t.Spec.Source.Ref[i+1:]); err == nil && n > 0 {
			return n
		}
	}
	return intake.ReviewPRNumber(t.Spec.Text)
}

// lastHandoffSummary returns the Summary of the newest valid Handoff event in evs.
func lastHandoffSummary(evs []envelope.Event) string {
	for i := len(evs) - 1; i >= 0; i-- {
		var p envelope.HandoffPayload
		if json.Unmarshal(evs[i].Payload, &p) == nil && strings.TrimSpace(p.Summary) != "" {
			return strings.TrimSpace(p.Summary)
		}
	}
	return ""
}

// reportFixable reports whether a completed report task is still watching its issue for a
// maintainer's /factory fix comment (phase F5).
func (r *Reconciler) reportFixable(t *v1alpha1.Task) bool {
	return t.Status.Phase == v1alpha1.PhaseDone &&
		t.Status.Reason == ReasonFindingsPosted &&
		t.Spec.Outcome == config.OutcomeReport &&
		t.Spec.Issue > 0 &&
		!slices.Contains(t.Status.Narrated, "fix-filed") &&
		!slices.ContainsFunc(t.Status.Outbox, func(o v1alpha1.Narration) bool { return o.Key == "fix-filed" })
}

// reportFix watches a completed report task's issue for a maintainer's `/factory fix` comment and
// files a new `change` Task on the same issue carrying the investigation's findings as its scope.
func (r *Reconciler) reportFix(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	c, ok, err := r.command(ctx, cfg, t, forge.PR{}, cmdFix)
	if err != nil || !ok {
		return err
	}
	var findings string
	if len(t.Status.Runs) > 0 && r.Rooms != nil && t.Status.RoomRef != "" {
		evs, _, err := r.Rooms.EventsSince(ctx, t.Status.RoomRef, current(t).StartSeq, handoffTo("implementer"))
		if err != nil {
			return err
		}
		findings = lastHandoffSummary(evs)
	}
	text := t.Spec.Text
	if findings != "" {
		cleanFindings, _ := sanitize.Text(findings)
		text = strings.TrimRight(text, "\n") + "\n\n## Findings\n\n" + cleanFindings
	}
	if len(text) > maxFixSnapshotBytes {
		text = strings.ToValidUTF8(text[:maxFixSnapshotBytes], "")
	}
	sumBytes := sha256.Sum256([]byte(text))
	key := taskid.FixKey(t.Spec.Repository, t.Spec.Issue, t.Name)
	name := taskid.Name(key)
	fix := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: t.Namespace,
			Labels:    map[string]string{v1alpha1.LabelIssue: strconv.Itoa(t.Spec.Issue)},
		},
		Spec: v1alpha1.TaskSpec{
			Source: v1alpha1.Source{
				Kind:          "issue",
				Ref:           fmt.Sprintf("%s#%d", t.Spec.Repository, t.Spec.Issue),
				Key:           key,
				RequestedBy:   "github:" + c.Author,
				Trust:         "untrusted",
				ContentSHA256: hex.EncodeToString(sumBytes[:]),
			},
			Repository:  t.Spec.Repository,
			Issue:       t.Spec.Issue,
			IssueAuthor: t.Spec.IssueAuthor,
			Text:        text,
			DataClass:   t.Spec.DataClass,
			Outcome:     config.OutcomeChange,
		},
	}
	if err := r.Client.Create(ctx, fix); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create fix task %s: %w", name, err)
	}
	markHandled(t, c.ID)
	narrateLater(t, narrate.FixFiled(t, name, c.Author))
	return nil
}
