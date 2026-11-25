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

// StatusFacts carries the workflow summary, path strip and split parts shown on the pinned issue status comment.
type StatusFacts struct {
	Workflow        string
	Reason          string
	Strip           string
	CeilingMicroUSD int64
	PlanParkedHours int
	Split           *envelope.SplitFact
}

const statusIssueMarker = "<!-- agent-factory issue-status -->"

var (
	statusOwnerRE      = regexp.MustCompile(`<!-- agent-factory issue-status-owner task=([a-z2-7]{8}) generation=([0-9]+) -->`)
	statusGenerationRE = regexp.MustCompile(`:gen([0-9]+)$`)
)

// Status renders the factory facts an issue's one live comment carries. It deliberately omits
// live checks and spend: neither is a truthful, stable fact the factory owns at render time.
func Status(t *v1alpha1.Task, roomsURL string, wf StatusFacts, mergeByApproval ...bool) string {
	phase := strings.ToLower(t.Status.Phase)
	if phase == "" {
		phase = "unknown"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Factory status · %s", phase)
	if t.Status.PhaseSince != nil {
		fmt.Fprintf(&b, " · updated %s", t.Status.PhaseSince.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if wf.Workflow != "" {
		fmt.Fprintf(&b, "\nWorkflow: %s", wf.Workflow)
		if wf.Reason != "" {
			fmt.Fprintf(&b, " · %s", wf.Reason)
		}
		if wf.CeilingMicroUSD > 0 {
			cents := (wf.CeilingMicroUSD + 5_000) / 10_000
			fmt.Fprintf(&b, " · up to $%d.%02d", cents/100, cents%100)
		}
	}
	if wf.Strip != "" {
		fmt.Fprintf(&b, "\nPath: %s", wf.Strip)
	}

	waiting := "nobody"
	facts := &envelope.TaskFacts{Phase: t.Status.Phase, Reason: t.Status.Reason,
		MergeByApproval: len(mergeByApproval) > 0 && mergeByApproval[0]}
	if pr := t.Status.PullRequest; pr != nil {
		facts.PR = &envelope.PRFact{Number: pr.Number, URL: pr.URL}
	}
	reason, next := summ.Waiting(facts)
	if wf.PlanParkedHours > 0 {
		waiting = fmt.Sprintf("parked: the plan was not answered in %d hours; any of the three answers resumes it", wf.PlanParkedHours)
	} else if reason != "" {
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
	if tbl := splitPartsTable(wf.Split); tbl != "" {
		b.WriteString("\n\n")
		b.WriteString(tbl)
	}
	fmt.Fprintf(&b, "\n\n%s\n%s", statusIssueMarker, statusOwnerMarker(t))
	return b.String()
}

// splitPartsTable renders one markdown table of every part of a split parent for the pinned issue
// status comment: columns Part, Issue, Path, State, Cost.
func splitPartsTable(sp *envelope.SplitFact) string {
	if sp == nil || sp.Of <= 0 {
		return ""
	}
	byIndex := make(map[int]envelope.PartFact, len(sp.Parts))
	for _, p := range sp.Parts {
		byIndex[p.Index] = p
	}
	var b strings.Builder
	b.WriteString("| Part | Issue | Path | State | Cost |\n")
	b.WriteString("| --- | --- | --- | --- | --- |")
	for idx := 1; idx <= sp.Of; idx++ {
		p, ok := byIndex[idx]
		if !ok {
			fmt.Fprintf(&b, "\n| %d | — | — | queued | — |", idx)
			continue
		}
		issue := "—"
		if p.Issue > 0 {
			issue = fmt.Sprintf("#%d", p.Issue)
		}
		strip := statusTableCell(p.Strip)
		if strip == "" {
			strip = "—"
		}
		state := summ.PartState(p)
		cost := "—"
		if p.CostMicroUSD > 0 {
			cost = formatMicroUSD(p.CostMicroUSD)
		}
		fmt.Fprintf(&b, "\n| %d | %s | %s | %s | %s |", idx, issue, strip, state, cost)
	}
	return b.String()
}

func statusTableCell(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ", "|", "\\|").Replace(strings.TrimSpace(s))
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return s
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
// A permanent refusal or an ownership stand-down records Inactive without fabricating remote state.
func (n Narrator) SyncStatus(ctx context.Context, t *v1alpha1.Task, roomsURL string, wf StatusFacts, mergeByApproval ...bool) error {
	if ledger := t.Status.StatusComment; ledger != nil && ledger.Inactive {
		return nil
	}
	if t.Spec.Issue == 0 {
		return nil
	}
	body := Status(t, roomsURL, wf, mergeByApproval...)
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
		if statusPermanent(err) {
			retireStatus(t)
			return nil
		}
		return fmt.Errorf("narrate: list status comments: %w", err)
	}
	current, found := findStatusComment(comments, n.Login, recordedID)
	if !found && recordedID != 0 {
		current, found = findStatusComment(comments, n.Login, 0)
	}
	if !found {
		id, err := f.CommentWithID(ctx, t.Spec.Issue, body)
		if err != nil {
			if statusPermanent(err) {
				retireStatus(t)
				return nil
			}
			return fmt.Errorf("narrate: create status comment: %w", err)
		}
		t.Status.StatusComment = &v1alpha1.StatusComment{ID: id, Body: body}
		return nil
	}
	if !statusMayOwn(t, current.Body) {
		retireStatus(t)
		return nil
	}
	if current.Body != body {
		if err := f.EditComment(ctx, current.ID, body); err != nil {
			if statusPermanent(err) {
				retireStatus(t)
				return nil
			}
			return fmt.Errorf("narrate: edit status comment %d: %w", current.ID, err)
		}
	}
	t.Status.StatusComment = &v1alpha1.StatusComment{ID: current.ID, Body: body}
	return nil
}

func statusPermanent(err error) bool {
	var permanent *forge.PermanentError
	return errors.As(err, &permanent)
}

func retireStatus(t *v1alpha1.Task) {
	if t.Status.StatusComment == nil {
		t.Status.StatusComment = &v1alpha1.StatusComment{}
	}
	t.Status.StatusComment.Inactive = true
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
