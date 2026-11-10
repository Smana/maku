// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/sanitize"
	summ "github.com/Smana/maku/internal/summary"
)

const statusIssueMarker = "<!-- agent-factory issue-status -->"

var (
	statusOwnerRE      = regexp.MustCompile(`<!-- agent-factory issue-status-owner task=([a-z2-7]{8}) generation=([0-9]+) -->`)
	statusGenerationRE = regexp.MustCompile(`:gen([0-9]+)$`)
)

// Status renders the factory facts an issue's one live comment carries. It deliberately omits
// live checks and spend: neither is a truthful, stable fact the factory owns at render time.
func Status(t *v1alpha1.Task, roomsURL string) string {
	phase := strings.ToLower(t.Status.Phase)
	if phase == "" {
		phase = "unknown"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Factory status · %s", phase)
	if t.Status.PhaseSince != nil {
		fmt.Fprintf(&b, " · updated %s", t.Status.PhaseSince.UTC().Format("2006-01-02 15:04 UTC"))
	}

	waiting := "nobody"
	reason, next := summ.Waiting(&envelope.TaskFacts{Phase: t.Status.Phase, Reason: t.Status.Reason})
	if reason != "" {
		waiting = reason
		if next != "" {
			waiting += "; next: " + next
		}
	}
	room := "none"
	if t.Status.RoomRef != "" {
		room = fmt.Sprintf("[%s](%s/r/%s)", t.Status.RoomRef, strings.TrimSuffix(roomsURL, "/"), t.Status.RoomRef)
	}
	fmt.Fprintf(&b, "\nRoom: %s · waiting on: %s", room, waiting)

	decisions, assumptions := 0, 0
	if records, err := sanitize.DecisionRecords(t.Spec.Text); err == nil {
		for _, record := range records {
			switch record.Kind {
			case "D":
				decisions++
			case "A":
				assumptions++
			}
		}
	}
	spec := "unknown"
	if t.Spec.Source.ContentSHA256 != "" {
		spec = "sha256:" + t.Spec.Source.ContentSHA256
	}
	fmt.Fprintf(&b, "\nSpec: %s · %s · %s", spec, statusCount(decisions, "decision"), statusCount(assumptions, "assumption"))
	if pr := t.Status.PullRequest; pr != nil {
		if pr.URL == "" {
			fmt.Fprintf(&b, "\nPR: #%d", pr.Number)
		} else {
			fmt.Fprintf(&b, "\nPR: [#%d](%s)", pr.Number, pr.URL)
		}
	}
	fmt.Fprintf(&b, "\n\n%s\n%s", statusIssueMarker, statusOwnerMarker(t))
	return b.String()
}

func statusCount(n int, noun string) string {
	if n != 1 {
		noun += "s"
	}
	return fmt.Sprintf("%d %s", n, noun)
}

func statusOwnerMarker(t *v1alpha1.Task) string {
	return fmt.Sprintf("<!-- agent-factory issue-status-owner task=%s generation=%d -->", t.Name, statusGeneration(t))
}

func statusGeneration(t *v1alpha1.Task) int {
	match := statusGenerationRE.FindStringSubmatch(t.Spec.Source.Key)
	if match == nil {
		return 0
	}
	generation, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return generation
}

// SyncStatus creates, adopts or edits the issue's one status comment. StatusComment records only
// remote state known to have succeeded, so a lost Task update or a failed write is safe to retry.
func (n Narrator) SyncStatus(ctx context.Context, t *v1alpha1.Task, roomsURL string) error {
	if t.Spec.Issue == 0 {
		return nil
	}
	body := Status(t, roomsURL)
	if ledger := t.Status.StatusComment; ledger != nil && ledger.ID != 0 && ledger.Body == body {
		return nil
	}
	if n.Login == "" {
		return errors.New("narrate: the factory's login is required to trust a status marker")
	}
	f, ok := n.Forge.(commenter)
	if !ok {
		return errors.New("narrate: status synchronization needs comment identity and editing")
	}

	var recordedID int64
	if t.Status.StatusComment != nil {
		recordedID = t.Status.StatusComment.ID
	}
	comments, err := f.AllComments(ctx, t.Spec.Issue)
	if err != nil {
		return fmt.Errorf("narrate: list status comments: %w", err)
	}
	current, found := findStatusComment(comments, n.Login, recordedID)
	if !found {
		if recordedID != 0 {
			return fmt.Errorf("narrate: status comment %d was not found", recordedID)
		}
		id, err := f.CommentWithID(ctx, t.Spec.Issue, body)
		if err != nil {
			return fmt.Errorf("narrate: create status comment: %w", err)
		}
		t.Status.StatusComment = &v1alpha1.StatusComment{ID: id, Body: body}
		return nil
	}
	if !statusMayOwn(t, current.Body) {
		return nil
	}
	if current.Body != body {
		if err := f.EditComment(ctx, current.ID, body); err != nil {
			return fmt.Errorf("narrate: edit status comment %d: %w", current.ID, err)
		}
	}
	t.Status.StatusComment = &v1alpha1.StatusComment{ID: current.ID, Body: body}
	return nil
}

func findStatusComment(comments []forge.Comment, login string, id int64) (forge.Comment, bool) {
	for _, comment := range comments {
		if !strings.EqualFold(comment.Author, login) || !strings.Contains(comment.Body, statusIssueMarker) {
			continue
		}
		if id == 0 || comment.ID == id {
			return comment, true
		}
	}
	return forge.Comment{}, false
}

func statusMayOwn(t *v1alpha1.Task, body string) bool {
	match := statusOwnerRE.FindStringSubmatch(body)
	if match == nil {
		return false
	}
	ownerGeneration, err := strconv.Atoi(match[2])
	if err != nil {
		return false
	}
	generation := statusGeneration(t)
	if ownerGeneration > generation {
		return false
	}
	return ownerGeneration < generation || match[1] == t.Name
}
