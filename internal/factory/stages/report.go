// SPDX-License-Identifier: Apache-2.0

// Package stages folds ended Tasks' per-stage costs and effects into the repository/class/stage
// report (WR5) and caches the folded reports for the catalogue's typical cost.
package stages

import (
	"cmp"
	"context"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

const (
	// DefaultLast is the default number of newest ended Tasks folded by one report.
	DefaultLast = 500
	// MaxLast is the maximum number of newest ended Tasks folded by one report.
	MaxLast = 500
	// MinTypicalMerged is the minimum merged tasks in a class before TypicalUSD is reported.
	MinTypicalMerged = 30
	// CacheTTL is how long a folded repository report stays warm for the catalogue.
	CacheTTL = 10 * time.Minute
)

// stageOrder fixes the display order of stages in every class report.
var stageOrder = []string{"ask", "plan", "tests", "implement", "search", "judge", "review", "split", "step_up"}

// Report is one repository's folded per-class, per-stage summary.
type Report struct {
	Repository string        `json:"repository"`
	Tasks      int           `json:"tasks"`
	Merged     int           `json:"merged"`
	Classes    []ClassReport `json:"classes"`
	// Models is the same tasks by model (Gemini plan A9): `maku stages --by model`.
	Models []ModelReport `json:"models,omitempty"`
}

// ModelReport is one model's summary: the tasks whose implementer ran on it, their merges and
// spend, and every stage that ran on it, whichever task it belonged to.
type ModelReport struct {
	Model        string        `json:"model"`
	Tasks        int           `json:"tasks"`
	Merged       int           `json:"merged"`
	USD          float64       `json:"usd"`
	USDPerMerged float64       `json:"usdPerMerged,omitempty"`
	Stages       []StageReport `json:"stages"`
}

// Stage returns the named StageReport in m, or a zero StageReport when absent.
func (m ModelReport) Stage(name string) StageReport {
	for _, s := range m.Stages {
		if s.Stage == name {
			return s
		}
	}
	return StageReport{}
}

// ClassReport is one class's summary within a repository.
type ClassReport struct {
	Class      string        `json:"class"`
	Tasks      int           `json:"tasks"`
	Merged     int           `json:"merged"`
	TypicalUSD float64       `json:"typicalUSD,omitempty"`
	P90USD     float64       `json:"p90USD,omitempty"`
	Stages     []StageReport `json:"stages"`
}

// Stage returns the named StageReport in c, or a zero StageReport when absent.
func (c ClassReport) Stage(name string) StageReport {
	for _, s := range c.Stages {
		if s.Stage == name {
			return s
		}
	}
	return StageReport{}
}

// StageReport is one stage's aggregated task count, spend, and effect counts.
type StageReport struct {
	Stage   string         `json:"stage"`
	Tasks   int            `json:"tasks"`
	USD     float64        `json:"usd"`
	Effects map[string]int `json:"effects"`
}

// Fold summarizes up to last newest ended Tasks for repository, grouped by class and stage.
func Fold(repository string, tasks []v1alpha1.Task, last int) Report {
	if last <= 0 || last > MaxLast {
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
	if len(ended) > last {
		ended = ended[:last]
	}

	type classAgg struct {
		tasks     int
		merged    int
		mergedUSD []float64
		stages    map[string]*stageAgg
	}
	byClass := map[string]*classAgg{}
	type modelAgg struct {
		tasks, merged int
		micro         int64
		stages        map[string]*stageAgg
	}
	byModel := map[string]*modelAgg{}
	modelAt := func(m string) *modelAgg {
		ma, ok := byModel[m]
		if !ok {
			ma = &modelAgg{stages: map[string]*stageAgg{}}
			byModel[m] = ma
		}
		return ma
	}
	rep := Report{Repository: repository, Classes: []ClassReport{}}

	for i := range ended {
		t := &ended[i]
		rep.Tasks++
		merged := isMerged(t)
		if merged {
			rep.Merged++
		}
		cl := classOf(t)
		ca, ok := byClass[cl]
		if !ok {
			ca = &classAgg{stages: map[string]*stageAgg{}}
			byClass[cl] = ca
		}
		ca.tasks++
		if merged {
			ca.merged++
			ca.mergedUSD = append(ca.mergedUSD, float64(taskMicroUSD(t))/1e6)
		}
		ma := modelAt(modelOf(t, "implementer"))
		ma.tasks++
		ma.micro += taskMicroUSD(t)
		if merged {
			ma.merged++
		}
		for _, st := range t.Status.Stages {
			stages := modelAt(modelOf(t, roleOfStage(st.Stage))).stages
			sa, ok := stages[st.Stage]
			if !ok {
				sa = &stageAgg{effects: map[string]int{}}
				stages[st.Stage] = sa
			}
			sa.tasks++
			sa.micro += st.MicroUSD
			if st.Effect != "" {
				sa.effects[st.Effect]++
			}
		}
		for _, st := range t.Status.Stages {
			sa, ok := ca.stages[st.Stage]
			if !ok {
				sa = &stageAgg{effects: map[string]int{}}
				ca.stages[st.Stage] = sa
			}
			sa.tasks++
			sa.micro += st.MicroUSD
			if st.Effect != "" {
				sa.effects[st.Effect]++
			}
		}
	}

	var names []string
	for name := range byClass {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		ca := byClass[name]
		cr := ClassReport{
			Class:  name,
			Tasks:  ca.tasks,
			Merged: ca.merged,
			Stages: []StageReport{},
		}
		if len(ca.mergedUSD) >= MinTypicalMerged {
			slices.Sort(ca.mergedUSD)
			cr.TypicalUSD = roundCents(percentile(ca.mergedUSD, 0.50))
			cr.P90USD = roundCents(percentile(ca.mergedUSD, 0.90))
		}
		seen := map[string]bool{}
		addStage := func(st string) {
			sa, ok := ca.stages[st]
			if !ok || seen[st] {
				return
			}
			seen[st] = true
			cr.Stages = append(cr.Stages, StageReport{
				Stage:   st,
				Tasks:   sa.tasks,
				USD:     roundCents(float64(sa.micro) / 1e6),
				Effects: sa.effects,
			})
		}
		for _, st := range stageOrder {
			addStage(st)
		}
		var extras []string
		for st := range ca.stages {
			if !seen[st] {
				extras = append(extras, st)
			}
		}
		slices.Sort(extras)
		for _, st := range extras {
			addStage(st)
		}
		rep.Classes = append(rep.Classes, cr)
	}
	for _, name := range slices.Sorted(maps.Keys(byModel)) {
		ma := byModel[name]
		mr := ModelReport{Model: name, Tasks: ma.tasks, Merged: ma.merged, USD: roundCents(float64(ma.micro) / 1e6), Stages: []StageReport{}}
		if ma.merged > 0 {
			mr.USDPerMerged = roundCents(float64(ma.micro) / 1e6 / float64(ma.merged))
		}
		mr.Stages = orderedStages(ma.stages)
		rep.Models = append(rep.Models, mr)
	}
	return rep
}

// stageAgg is one stage's running totals while a report folds.
type stageAgg struct {
	tasks   int
	micro   int64
	effects map[string]int
}

// orderedStages is aggs as reports, in stageOrder, then any other stage by name.
func orderedStages(aggs map[string]*stageAgg) []StageReport {
	names := slices.Clone(stageOrder)
	for _, st := range slices.Sorted(maps.Keys(aggs)) {
		if !slices.Contains(names, st) {
			names = append(names, st)
		}
	}
	out := []StageReport{}
	for _, st := range names {
		if sa, ok := aggs[st]; ok {
			out = append(out, StageReport{Stage: st, Tasks: sa.tasks, USD: roundCents(float64(sa.micro) / 1e6), Effects: sa.effects})
		}
	}
	return out
}

// roleOfStage is the role whose runs a stage measures (WR5's stageOf, reversed).
func roleOfStage(stage string) string {
	switch stage {
	case "review":
		return "reviewer"
	case "tests":
		return "tester"
	case "plan", "ask":
		return "triager"
	}
	return "implementer"
}

// modelOf is the model role ran on in t: a run of role's served model (the meter's
// gen_ai_request_model), else the profile's frozen model for its roles (a model:<alias> label, the
// evaluation's treatment), else the budget's alias.
func modelOf(t *v1alpha1.Task, role string) string {
	for _, r := range t.Status.Runs {
		if r.Role == role && len(r.Provenance.ServedModels) > 0 {
			return r.Provenance.ServedModels[0]
		}
	}
	if p := t.Status.Profile; p != nil && p.Model != "" {
		roles := p.ModelRoles
		if len(roles) == 0 {
			roles = []string{"implementer", "reviewer"}
		}
		if slices.Contains(roles, role) {
			return p.Model
		}
	}
	return cmp.Or(t.Spec.Budget.Model, "unknown")
}

// FilterClass narrows r to one class, keeping the repository header and matching totals.
func (r Report) FilterClass(class string) Report {
	if class == "" {
		return r
	}
	out := Report{Repository: r.Repository, Classes: []ClassReport{}}
	for _, c := range r.Classes {
		if c.Class == class {
			out.Classes = append(out.Classes, c)
			out.Tasks += c.Tasks
			out.Merged += c.Merged
		}
	}
	return out
}

func taskTime(t *v1alpha1.Task) time.Time {
	if t.Status.EndedAt != nil {
		return t.Status.EndedAt.Time
	}
	if t.Status.PhaseSince != nil {
		return t.Status.PhaseSince.Time
	}
	return t.CreationTimestamp.Time
}

func classOf(t *v1alpha1.Task) string {
	if t.Status.PullRequest != nil && t.Status.PullRequest.Policy != "" {
		return t.Status.PullRequest.Policy
	}
	if t.Spec.PredictedClass != "" {
		return t.Spec.PredictedClass
	}
	return "review"
}

func isMerged(t *v1alpha1.Task) bool {
	if t.Status.Outcome != "" {
		return t.Status.Outcome == "accepted" || t.Status.Outcome == "merged_human"
	}
	return t.Status.Phase == v1alpha1.PhaseDone && t.Status.Reason != "integration_failed"
}

func taskMicroUSD(t *v1alpha1.Task) int64 {
	if t.Status.Usage.CostMicroUSD > 0 {
		return t.Status.Usage.CostMicroUSD
	}
	var sum int64
	for _, st := range t.Status.Stages {
		sum += st.MicroUSD
	}
	return sum
}

func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p == 0.50 {
		if n%2 == 1 {
			return sorted[n/2]
		}
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	idx := int(math.Ceil(p*float64(n))) - 1
	idx = max(0, min(idx, n-1))
	return sorted[idx]
}

func roundCents(v float64) float64 {
	return math.Round(v*100) / 100
}

// Cache keeps each repository's latest folded stage report for CacheTTL so the catalogue can read
// typical costs without listing Tasks on every request. A cold cache reports no typical figure.
type Cache struct {
	now     func() time.Time
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	rep Report
	at  time.Time
}

// NewCache creates an empty stage-report cache.
func NewCache(now func() time.Time) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{now: now, entries: map[string]cacheEntry{}}
}

// Store records rep as the current report for repository.
func (c *Cache) Store(repository string, rep Report) {
	if c == nil || repository == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[strings.ToLower(repository)] = cacheEntry{rep: rep, at: c.now()}
}

// Typical returns the cached typical USD and merged task count for repository/class, or ok=false
// when the cache is cold, expired, or has fewer than MinTypicalMerged merged tasks.
func (c *Cache) Typical(repository, class string) (usd float64, of int, ok bool) {
	if c == nil || repository == "" || class == "" {
		return 0, 0, false
	}
	c.mu.RLock()
	e, found := c.entries[strings.ToLower(repository)]
	c.mu.RUnlock()
	if !found || c.now().Sub(e.at) > CacheTTL {
		return 0, 0, false
	}
	for _, cl := range e.rep.Classes {
		if cl.Class == class && cl.TypicalUSD > 0 {
			return cl.TypicalUSD, cl.Merged, true
		}
	}
	return 0, 0, false
}

// Refresh folds tasks into reports for every served repository and updates the cache.
func (c *Cache) Refresh(_ context.Context, repos []string, tasks []v1alpha1.Task) {
	if c == nil {
		return
	}
	for _, repo := range repos {
		c.Store(repo, Fold(repo, tasks, MaxLast))
	}
}
