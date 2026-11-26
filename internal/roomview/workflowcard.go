// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"cmp"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/catalogue"
)

// WorkflowCard is the facts every surface shows where a task is named (WR3, phase F).
type WorkflowCard struct {
	Title, Repository string
	Outcome           string   // change (or ""), plan-first, review, report
	OutcomeInferred   bool     // true when Outcome was inferred rather than explicit or default
	ReviewPR          int      // pull request number when Outcome is review
	Workflow, Reason  string   // Reason is built by Reason(), never model text
	Path              []string // what is still to run: implementer, reviewer, CI, merge gate
	Done              []string // stages that already ran and succeeded, shown first with a ✓
	CeilingUSD        float64
	StepUpUSD         float64 // 0: no step-up line
	StepUpTo          string  // the workflow a step-up runs, when known
	// ConfirmedUSD is the cost a person confirmed (the task's cap), WorkflowCeilingUSD the
	// workflow's own ceiling: when the cap lowers the ceiling, the Cost row says so.
	ConfirmedUSD       float64
	WorkflowCeilingUSD float64
	SteppedFrom        string  // non-empty after a step-up
	TypicalUSD         float64 // 0 until phase D has data for the class
}

// OutcomeWords renders a task's outcome in plain English for the workflow card's first row.
func OutcomeWords(outcome string, reviewPR int) string {
	switch outcome {
	case "plan-first":
		return "a plan to approve, then a pull request"
	case "review":
		if reviewPR > 0 {
			return fmt.Sprintf("a review of #%d", reviewPR)
		}
		return "a review of a pull request"
	case "report":
		return "findings on the issue"
	default:
		return "a pull request"
	}
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
	if len(p.RequiredStages) > 0 {
		path = withRequiredPath(path, p.RequiredStages)
	}
	done, path := Progress(path, f.Runs)
	reason := Reason(p.Source, p.By, rec, trigger, nil)
	if cls := clean(p.RequiredBy); cls != "" && len(p.RequiredStages) > 0 {
		reason += "; the class " + cls + " requires " + requiredStageWords(p.RequiredStages)
	}
	ceiling := p.CeilingMicroUSD
	if ceiling == 0 && f.Spend != nil {
		ceiling = f.Spend.LimitMicroUSD
	}
	reviewPR := 0
	if f.Outcome == "review" && f.PR != nil {
		reviewPR = f.PR.Number
	}
	return WorkflowCard{
		Title:       title,
		Repository:  repoOf(urlOf(f.Issue), prURL(f.PR)),
		Outcome:     f.Outcome,
		ReviewPR:    reviewPR,
		Workflow:    p.Name,
		Reason:      reason,
		Path:        path,
		Done:        done,
		CeilingUSD:  float64(ceiling) / 1e6,
		StepUpUSD:   float64(p.StepUpCeilingMicroUSD) / 1e6,
		StepUpTo:    p.StepUpTo,
		SteppedFrom: steppedFrom,

		ConfirmedUSD:       float64(p.ConfirmedMicroUSD) / 1e6,
		WorkflowCeilingUSD: float64(p.WorkflowCeilingMicroUSD) / 1e6,
	}
}

// Progress splits a workflow's path by the task's runs (oldest first): done is the stages whose
// latest run succeeded, in the order they first ran, and rest is the path from the stage that
// runs next. A step-up can hand a task a path without a stage that already ran (the scoper), or
// one that starts again with it: either way the stage shows once, as done, and is not promised
// again. A failed or live stage stays ahead, and a reviewer asking for changes sends the path
// back to the implementer. With no run, done is nil and rest is the whole path.
func Progress(path []string, runs []envelope.RunFact) (done, rest []string) {
	if len(path) == 0 || len(runs) == 0 {
		return nil, path
	}
	name := func(r envelope.RunFact) string { return cmp.Or(r.Persona, r.Role) }
	ok := func(r envelope.RunFact) bool {
		return r.Outcome == envelope.OutcomeSucceeded && r.Verdict != "changes"
	}
	latest := map[string]envelope.RunFact{}
	var order []string
	for _, r := range runs {
		n := name(r)
		if n == "" {
			continue
		}
		if _, seen := latest[n]; !seen {
			order = append(order, n)
		}
		latest[n] = r
	}
	last := runs[len(runs)-1]
	pos := 0
	if i := slices.Index(path, name(last)); i >= 0 {
		pos = i
		if ok(last) {
			pos = i + 1
		} else if last.Verdict == "changes" {
			if j := slices.Index(path, "implementer"); j >= 0 {
				pos = j
			}
		}
	}
	rest = path[pos:]
	for _, n := range order {
		if ok(latest[n]) && !slices.Contains(rest, n) {
			done = append(done, n)
		}
	}
	return done, rest
}

// PathLine is the Will run row: the done stages each with a ✓, then what is still to run, each
// step through text (the surface's escaping). Empty when there is neither.
func PathLine(c WorkflowCard, text func(string) string) string {
	var parts []string
	for _, st := range c.Done {
		parts = append(parts, text(st)+" ✓")
	}
	if len(c.Path) > 0 {
		next := make([]string, len(c.Path))
		for i, st := range c.Path {
			next[i] = text(st)
		}
		parts = append(parts, strings.Join(next, " → "))
	}
	return strings.Join(parts, " · ")
}

func requiredStageWords(stages []string) string {
	var parts []string
	for _, st := range stages {
		switch st {
		case "plan":
			parts = append(parts, "a plan")
		case "tests":
			parts = append(parts, "tests")
		case "judge":
			parts = append(parts, "a judge")
		case "review":
			parts = append(parts, "a review")
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

func withRequiredPath(path, required []string) []string {
	if len(path) == 0 {
		return path
	}
	if len(path) == 1 && path[0] == "triager" {
		return path
	}
	out := slices.Clone(path)
	insertBefore := func(item string, before ...string) {
		if slices.Contains(out, item) {
			return
		}
		for _, target := range before {
			if idx := slices.Index(out, target); idx >= 0 {
				out = slices.Insert(out, idx, item)
				return
			}
		}
		out = append(out, item)
	}
	if slices.Contains(required, "plan") {
		insertBefore("scoper", "tester", "implementer", "reviewer", "CI", "merge gate")
	}
	if slices.Contains(required, "tests") {
		insertBefore("tester", "implementer", "reviewer", "CI", "merge gate")
	}
	if slices.Contains(required, "review") {
		insertBefore("reviewer", "CI", "merge gate")
	}
	return out
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

// PlanStepUpUSD puts a workflow's step-up ceiling on the basis of a plan's ceiling. A plan's ceiling
// sums its issues' task ceilings, while the workflow's step-up ceiling is one task's: a three-issue
// plan at $5.10 a task shows "up to $15.30", and comparing that with one task's $13.60 step-up
// printed a step-up below the base. With more than one issue, each issue may step up, so the plan
// figure adds every issue's step-up headroom to the plan's ceiling. One issue, no task ceiling, or a
// step-up no higher than the task ceiling: the step-up figure is returned as is (CostLine omits a
// figure that is not above the base).
func PlanStepUpUSD(planCeilingUSD, taskCeilingUSD, taskStepUpUSD float64, issues int) float64 {
	if issues <= 1 || taskCeilingUSD <= 0 || taskStepUpUSD <= taskCeilingUSD {
		return taskStepUpUSD
	}
	return planCeilingUSD + float64(issues)*(taskStepUpUSD-taskCeilingUSD)
}

// CostLine formats the Cost row's value: ceiling, optional step-up ceiling, optional typical cost.
// A confirmed cost below the workflow's own ceiling caps the task, and the row says so rather than
// show a workflow's ceiling it will not reach. Under a confirmed cost a step-up runs only when its
// ceiling fits: one above it is offered to a maintainer, never run.
func CostLine(c WorkflowCard) string {
	if c.CeilingUSD <= 0 {
		return ""
	}
	capped := c.ConfirmedUSD > 0 && c.CeilingUSD <= c.ConfirmedUSD
	s := "up to " + Dollars(c.CeilingUSD)
	if capped && c.WorkflowCeilingUSD > c.CeilingUSD {
		s = "capped at your confirmed " + Dollars(c.ConfirmedUSD)
		if w := clean(c.Workflow); w != "" {
			s += " (" + w + "'s ceiling is " + Dollars(c.WorkflowCeilingUSD) + ")"
		}
	}
	switch {
	case c.SteppedFrom != "":
		s += " · stepped up from " + c.SteppedFrom
	case c.StepUpUSD > 0 && capped && c.StepUpUSD > c.ConfirmedUSD:
		to := "a step-up"
		if w := clean(c.StepUpTo); w != "" {
			to += " to " + w
		}
		s += " · " + to + " (" + Dollars(c.StepUpUSD) + ") is offered, not run"
	case c.StepUpUSD > c.CeilingUSD && capped:
		s += " · up to " + Dollars(c.StepUpUSD) + " if it steps up, within your " + Dollars(c.ConfirmedUSD)
	case c.StepUpUSD > 0 && capped:
		s += " · a step-up stays within your " + Dollars(c.ConfirmedUSD)
	case c.StepUpUSD > 0 && c.StepUpUSD > c.CeilingUSD:
		s += " · up to " + Dollars(c.StepUpUSD) + " if it steps up"
	}
	if c.TypicalUSD > 0 {
		s += " · typically " + Dollars(c.TypicalUSD)
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
	outcomeVal := r.text(OutcomeWords(c.Outcome, c.ReviewPR), false)
	if c.OutcomeInferred && c.Outcome != "" && c.Outcome != "change" {
		outcomeVal += " (inferred)"
	}
	wf := r.text(c.Workflow, false)
	if reason := strings.TrimSpace(c.Reason); reason != "" {
		wf += " · " + r.text(reason, false)
	}
	rows := [][2]string{{"Outcome", outcomeVal}, {"Workflow", wf}}
	if line := PathLine(c, func(s string) string { return r.text(s, false) }); line != "" {
		rows = append(rows, [2]string{"Will run", line})
	}
	if cost := CostLine(c); cost != "" {
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
