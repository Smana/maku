// SPDX-License-Identifier: Apache-2.0

package stages

import (
	"cmp"
	"slices"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

const (
	// DefaultDryRunLast is the default number of newest ended tasks simulated by workflow dry-run.
	DefaultDryRunLast = 20
	// MinMedianSamples is the minimum recorded runs of a stage in a class before a class median
	// is used to estimate an added stage; below this the task estimate is unknown (?).
	MinMedianSamples = 5
)

// DryRunRequest is the body of POST /v1/workflows/dry-run and POST /api/workflows/dry-run.
type DryRunRequest struct {
	Workflow   string `json:"workflow"`
	Definition string `json:"definition,omitempty"`
	Repository string `json:"repository,omitempty"`
	Last       int    `json:"last,omitempty"`
}

// DryRunResult is the output of simulating a workflow definition over recent ended tasks.
type DryRunResult struct {
	Workflow     string      `json:"workflow"`
	Repository   string      `json:"repository,omitempty"`
	Tasks        int         `json:"tasks"`
	ActualUSD    float64     `json:"actualUSD"`
	EstimatedUSD float64     `json:"estimatedUSD"`
	EstimatedOf  int         `json:"estimatedOf"`
	MedianUsed   int         `json:"medianUsed"`
	UnknownCount int         `json:"unknownCount,omitempty"`
	Rows         []DryRunRow `json:"rows"`
}

// DryRunRow is one ended task's actual stages vs simulated stages and cost estimate.
type DryRunRow struct {
	Task        string   `json:"task"`
	Repository  string   `json:"repository,omitempty"`
	Class       string   `json:"class"`
	RanWorkflow string   `json:"ranWorkflow,omitempty"`
	Ran         []string `json:"ran"`
	WouldRun    []string `json:"wouldRun"`
	CostUSD     float64  `json:"costUSD"`
	EstimateUSD *float64 `json:"estimateUSD,omitempty"`
	Unknown     bool     `json:"unknown,omitempty"`
	UsedMedian  bool     `json:"usedMedian,omitempty"`
}

// canonicalStageKey maps stage role names and status.stages keys into one shared key space.
func canonicalStageKey(name string) string {
	switch name {
	case "scoper", "plan":
		return "plan"
	case "tester", "tests":
		return "tests"
	case "implementer", "implement":
		return "implement"
	case "reviewer", "review":
		return "review"
	default:
		return name
	}
}

// displayStageName renders a canonical stage key as the role name shown in workflow dry-run.
func displayStageName(key string) string {
	switch key {
	case "plan":
		return "scoper"
	case "tests":
		return "tester"
	case "implement":
		return "implementer"
	case "review":
		return "reviewer"
	default:
		return key
	}
}

// DryRun simulates running workflow profile p (named workflowName) against up to last newest ended
// tasks in tasks (filtered by repository when non-empty). Class medians are computed across all
// ended tasks in tasks (matching repository when non-empty) so the sample pool is not truncated
// to last. Each row's estimate is the recorded dollars of stages shared by actual and simulated
// runs, plus the class median dollars for each stage added by p, minus stages dropped by p.
// Any added stage with fewer than MinMedianSamples recorded runs in the task's class marks the
// row Unknown and excludes that row from EstimatedUSD.
func DryRun(cfg *config.Config, workflowName string, p config.HarnessProfile, repository string, tasks []v1alpha1.Task, last int) DryRunResult {
	if last <= 0 {
		last = DefaultDryRunLast
	}
	if last > MaxLast {
		last = MaxLast
	}

	var ended []v1alpha1.Task
	for _, t := range tasks {
		if !v1alpha1.TerminalPhase(t.Status.Phase) {
			continue
		}
		if repository != "" && !strings.EqualFold(t.Spec.Repository, repository) {
			continue
		}
		ended = append(ended, t)
	}
	slices.SortFunc(ended, func(a, b v1alpha1.Task) int {
		ta, tb := taskTime(&a), taskTime(&b)
		if !ta.Equal(tb) {
			if ta.After(tb) {
				return -1
			}
			return 1
		}
		return cmp.Compare(b.Name, a.Name)
	})

	// Build per-(class, stage) dollar sample distributions across all ended tasks in scope.
	samples := map[string]map[string][]float64{}
	for i := range ended {
		t := &ended[i]
		cl := classOf(t)
		perStage, ok := samples[cl]
		if !ok {
			perStage = map[string][]float64{}
			samples[cl] = perStage
		}
		// Fold each stage's spend on this task once.
		taskStageMicro := map[string]int64{}
		taskStageRan := map[string]bool{}
		for _, sr := range t.Status.Stages {
			if sr.Runs <= 0 && sr.MicroUSD <= 0 {
				continue
			}
			k := canonicalStageKey(sr.Stage)
			if k == "ask" || k == "split" || k == "step_up" {
				continue
			}
			taskStageRan[k] = true
			taskStageMicro[k] += sr.MicroUSD
		}
		for k := range taskStageRan {
			perStage[k] = append(perStage[k], float64(taskStageMicro[k])/1e6)
		}
	}
	// Precompute medians for every (class, stage) with at least MinMedianSamples samples.
	medians := map[string]map[string]float64{}
	for cl, perStage := range samples {
		m := map[string]float64{}
		for k, vals := range perStage {
			if len(vals) < MinMedianSamples {
				continue
			}
			slices.Sort(vals)
			m[k] = percentile(vals, 0.50)
		}
		medians[cl] = m
	}

	window := ended
	if len(window) > last {
		window = window[:last]
	}

	res := DryRunResult{
		Workflow:   workflowName,
		Repository: repository,
		Tasks:      len(window),
		Rows:       make([]DryRunRow, 0, len(window)),
	}

	var actualTotal, estTotal float64
	for i := range window {
		t := &window[i]
		cl := classOf(t)
		ranKeys, ranCost := actualTaskStages(t)
		wouldKeys := simulatedTaskStages(cfg, workflowName, p, t, cl)

		ranSet := make(map[string]bool, len(ranKeys))
		for _, k := range ranKeys {
			ranSet[k] = true
		}

		actualUSD := float64(taskMicroUSD(t)) / 1e6
		// Estimate: recorded dollars of shared stages + class median for each added stage.
		var est float64
		unknown := false
		usedMedian := false
		for _, k := range wouldKeys {
			if ranSet[k] {
				est += float64(ranCost[k]) / 1e6
				continue
			}
			med, ok := medians[cl][k]
			if !ok {
				unknown = true
				break
			}
			est += med
			usedMedian = true
		}

		ranDisplay := make([]string, len(ranKeys))
		for j, k := range ranKeys {
			ranDisplay[j] = displayStageName(k)
		}
		wouldDisplay := make([]string, len(wouldKeys))
		for j, k := range wouldKeys {
			wouldDisplay[j] = displayStageName(k)
		}
		ranWf := ""
		if t.Status.Profile != nil {
			ranWf = t.Status.Profile.Name
		}

		row := DryRunRow{
			Task:        t.Name,
			Repository:  t.Spec.Repository,
			Class:       cl,
			RanWorkflow: ranWf,
			Ran:         ranDisplay,
			WouldRun:    wouldDisplay,
			CostUSD:     roundCents(actualUSD),
			Unknown:     unknown,
			UsedMedian:  usedMedian && !unknown,
		}
		actualTotal += actualUSD
		if unknown {
			res.UnknownCount++
		} else {
			v := roundCents(est)
			row.EstimateUSD = &v
			estTotal += est
			res.EstimatedOf++
			if usedMedian {
				res.MedianUsed++
			}
		}
		res.Rows = append(res.Rows, row)
	}

	res.ActualUSD = roundCents(actualTotal)
	res.EstimatedUSD = roundCents(estTotal)
	return res
}

// actualTaskStages extracts the ordered canonical stage keys that ran on t and each stage's
// recorded micro-USD cost from t.Status.Stages.
func actualTaskStages(t *v1alpha1.Task) ([]string, map[string]int64) {
	costs := map[string]int64{}
	present := map[string]bool{}
	var extras []string
	for _, sr := range t.Status.Stages {
		if sr.Runs <= 0 && sr.MicroUSD <= 0 {
			continue
		}
		k := canonicalStageKey(sr.Stage)
		if k == "ask" || k == "split" || k == "step_up" {
			continue
		}
		if !present[k] {
			present[k] = true
			if k != "plan" && k != "tests" && k != "implement" && k != "review" {
				extras = append(extras, k)
			}
		}
		costs[k] += sr.MicroUSD
	}
	var order []string
	for _, k := range []string{"plan", "tests", "implement", "review"} {
		if present[k] {
			order = append(order, k)
		}
	}
	order = append(order, extras...)
	return order, costs
}

// simulatedTaskStages resolves the canonical stage keys that workflow profile p would run for
// task t, taking into account t's repository settings, merge class requirements, and outcome.
func simulatedTaskStages(cfg *config.Config, workflowName string, p config.HarnessProfile, t *v1alpha1.Task, class string) []string {
	var rc *config.Repository
	if cfg != nil {
		rc = cfg.RepositoryFor(t.Spec.Repository)
	}
	compiled := cfg.CompiledStagesForProfile(rc, workflowName, p)

	var keys []string
	has := func(k string) bool { return slices.Contains(keys, k) }

	for _, s := range compiled {
		switch s.Role {
		case "scoper":
			keys = append(keys, "plan")
		case "tester":
			if t.Spec.DataClass == "internal" {
				continue
			}
			if s.When == config.TestWhenRepositoryTestFirst && (rc == nil || !rc.Workflow.TestFirst) {
				continue
			}
			keys = append(keys, "tests")
		case "implementer":
			keys = append(keys, "implement")
		case "reviewer":
			keys = append(keys, "review")
		default:
			keys = append(keys, canonicalStageKey(s.Role))
		}
	}

	// Apply class required stages (WR6): never removes, only adds.
	if cfg != nil && rc != nil && class != "" {
		for _, req := range cfg.RequiredStagesFor(rc, class) {
			switch req {
			case "plan":
				if !has("plan") {
					keys = append([]string{"plan"}, keys...)
				}
			case "tests":
				if t.Spec.DataClass != "internal" && rc.Merge.ContractCheck != "" && !has("tests") {
					idx := slices.Index(keys, "implement")
					if idx < 0 {
						idx = len(keys)
					}
					keys = slices.Insert(keys, idx, "tests")
				}
			case "review":
				if !has("review") {
					idx := slices.Index(keys, "implement")
					if idx >= 0 {
						keys = slices.Insert(keys, idx+1, "review")
					} else {
						keys = append(keys, "review")
					}
				}
			}
		}
	}

	// Apply task outcome (WR8): plan-first always runs the scoper.
	if config.NormalizeOutcome(t.Spec.Outcome) == config.OutcomePlanFirst && !has("plan") {
		keys = append([]string{"plan"}, keys...)
	}
	return keys
}
