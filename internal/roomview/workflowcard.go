// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/catalogue"
)

// WorkflowCard is the four facts every surface shows where a task is named (WR3).
type WorkflowCard struct {
	Title, Repository string
	Workflow, Reason  string   // Reason is built by Reason(), never model text
	Path              []string // implementer, reviewer, CI, merge gate
	CeilingUSD        float64
	StepUpUSD         float64 // 0: no step-up line
	SteppedFrom       string  // non-empty after a step-up
	TypicalUSD        float64 // 0 until phase D has data for the class
}

// Dollars formats a USD amount as a person reads it: two decimals, from the cent up.
func Dollars(usd float64) string {
	cents := int64(math.Round(usd * 100))
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

// Reason builds the workflow card's reason from structured facts only, never from a model's prose.
func Reason(source, by, recommended, trigger string, paths []string) string {
	var base string
	switch source {
	case "repository", "floor", "control":
		base = "this repository's default"
	case "label":
		if who := clean(by); who != "" {
			base = "pinned by @" + who
		} else {
			base = "pinned by a maintainer"
		}
	case "schedule":
		base = "this schedule's workflow"
	case "revert":
		base = "a revert"
	case "oracle":
		base = "a finding a check verifies"
	case "treatment":
		base = "the comparison's second arm"
	case "scoper":
		base = "the scoper recommended it"
	case "escalation":
		base = "stepped up after " + stepTriggerWords(trigger)
	default:
		base = "this repository's default"
	}
	if len(paths) > 0 && source == "class" {
		base = "touches " + clean(paths[0])
	}
	if rec := clean(recommended); rec != "" {
		base += "; the planner suggested " + rec
	}
	return base
}

func stepTriggerWords(trigger string) string {
	switch trigger {
	case "stalled":
		return "a run that committed nothing"
	case "budget-run", "budget-task", "outOfBudget":
		return "spending its budget"
	case "not_met", "notConverged":
		return "missing acceptance checks"
	case "review_rounds_exhausted":
		return "exhausting review rounds"
	case "ci_red":
		return "a red check"
	default:
		return "a failed run"
	}
}

// CardOf builds a WorkflowCard from a task's room facts and title.
func CardOf(f *envelope.TaskFacts, title string) WorkflowCard {
	if f == nil || f.Profile == nil {
		return WorkflowCard{Title: title}
	}
	p := f.Profile
	if title == "" && f.Issue != nil {
		title = f.Issue.Title
	}
	rec := ""
	if p.Recommended != "" && p.Recommended != p.Name && p.Source != "scoper" {
		rec = p.Recommended
	}
	trigger := ""
	steppedFrom := ""
	if p.Source == "escalation" {
		trigger = p.Reason
	}
	if p.StepUp != nil && (p.StepUp.Mode == "auto" || p.StepUp.Mode == "manual") {
		steppedFrom = p.StepUp.From
		if p.StepUp.Trigger != "" {
			trigger = p.StepUp.Trigger
		}
	}
	var path []string
	if len(f.Plan) > 0 {
		path = slices.Clone(f.Plan)
		if len(path) != 1 || path[0] != "triager" {
			path = append(path, "CI", "merge gate")
		}
	}
	ceiling := p.CeilingMicroUSD
	if ceiling == 0 && f.Spend != nil {
		ceiling = f.Spend.LimitMicroUSD
	}
	return WorkflowCard{
		Title:       title,
		Repository:  repoOf(urlOf(f.Issue), prURL(f.PR)),
		Workflow:    p.Name,
		Reason:      Reason(p.Source, p.By, rec, trigger, nil),
		Path:        path,
		CeilingUSD:  float64(ceiling) / 1e6,
		StepUpUSD:   float64(p.StepUpCeilingMicroUSD) / 1e6,
		SteppedFrom: steppedFrom,
	}
}

// CardOfPlan builds a WorkflowCard from a repository's catalogue for a planned workflow.
func CardOfPlan(c catalogue.Catalogue, workflow, source, by, suggested string) WorkflowCard {
	p := c.Profiles[workflow]
	rec := ""
	if suggested != "" && suggested != workflow {
		rec = suggested
	}
	return WorkflowCard{
		Repository: c.Repository,
		Workflow:   workflow,
		Reason:     Reason(source, by, rec, "", nil),
		Path:       slices.Clone(p.Path),
		CeilingUSD: p.CeilingUSD,
		StepUpUSD:  p.StepUpCeilingUSD,
	}
}

// costLine formats the Cost row's value: ceiling, optional step-up ceiling, optional typical cost.
func costLine(c WorkflowCard) string {
	if c.CeilingUSD <= 0 {
		return ""
	}
	s := "up to " + Dollars(c.CeilingUSD)
	if c.SteppedFrom != "" {
		s += " · stepped up from " + c.SteppedFrom
	} else if c.StepUpUSD > 0 {
		s += " · up to " + Dollars(c.StepUpUSD) + " if it steps up"
	}
	if c.TypicalUSD > 0 {
		s += " · typical " + Dollars(c.TypicalUSD)
	}
	return s
}

// WorkflowCardText renders c in Text/TextColor, Markdown or Slack format.
func WorkflowCardText(w io.Writer, c WorkflowCard, o Options) error {
	r := &card{o: o}
	if title, repo := strings.TrimSpace(c.Title), strings.TrimSpace(c.Repository); title != "" || repo != "" {
		switch {
		case title != "" && repo != "":
			r.line(r.text(title, false) + " · " + r.token(repo, false))
		case title != "":
			r.line(r.text(title, false))
		default:
			r.line(r.token(repo, false))
		}
	}
	wf := r.text(c.Workflow, false)
	if reason := strings.TrimSpace(c.Reason); reason != "" {
		wf += " · " + r.text(reason, false)
	}
	rows := [][2]string{{"Workflow", wf}}
	if len(c.Path) > 0 {
		parts := make([]string, len(c.Path))
		for i, step := range c.Path {
			parts[i] = r.text(step, false)
		}
		rows = append(rows, [2]string{"Will run", strings.Join(parts, " → ")})
	}
	if cost := costLine(c); cost != "" {
		rows = append(rows, [2]string{"Cost", cost})
	}
	switch {
	case r.md():
		for _, row := range rows {
			r.line("- " + r.bold(row[0]+":") + " " + row[1])
		}
	case r.slack():
		for _, row := range rows {
			r.line(r.bold(row[0]) + " " + row[1])
		}
	default:
		for _, row := range rows {
			pad := max(11-len(row[0]), 1)
			r.line(row[0] + strings.Repeat(" ", pad) + row[1])
		}
	}
	_, err := io.WriteString(w, r.b.String())
	return err
}
