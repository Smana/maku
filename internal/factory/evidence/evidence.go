// SPDX-License-Identifier: Apache-2.0

// Package evidence is a class's promotion evidence (merge policy stage 6, MP7): the count
// scripts/shadow-merge-tally.sh makes, in Go, from the same pull request records. Shadow is the
// script's default mode (merged unchanged at the judged head, per reviewer model), Outcomes its
// --outcomes mode (bad mature outcomes and the Clopper-Pearson bound), Evaluate the eligibility
// MP7 sets. Pure: the caller reads GitHub, and the tests read the script's own fixtures.
package evidence

import (
	"cmp"
	"maps"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Actor is a GitHub login as gh prints it: {"login": ...}.
type Actor struct {
	Login string `json:"login"`
}

// Label is a label as gh prints it.
type Label struct {
	Name string `json:"name"`
}

// Comment is one issue comment of a pull request.
type Comment struct {
	Author    Actor     `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
	Body      string    `json:"body"`
}

// PR is a merged pull request in `gh pr list --json number,headRefOid,mergedBy,mergedAt,labels,comments`
// shape, the script's input.
type PR struct {
	Number     int       `json:"number"`
	HeadRefOid string    `json:"headRefOid"`
	MergedBy   *Actor    `json:"mergedBy"`
	MergedAt   time.Time `json:"mergedAt"`
	Labels     []Label   `json:"labels"`
	Comments   []Comment `json:"comments"`
}

// Revert is a pull request titled "Revert", any state: GitHub's revert body or the merger's
// factory/revert-of:#<n> label names what it reverts.
type Revert struct {
	Body   string  `json:"body"`
	Labels []Label `json:"labels"`
	State  string  `json:"state"`
}

// Logins are the factory App's comment author and the merger App's merge actor.
type Logins struct {
	Factory, Merger string
}

// login is the script's: lowercased, without app/ or [bot].
func login(s string) string {
	s = strings.TrimPrefix(strings.ToLower(s), "app/")
	return strings.TrimSuffix(s, "[bot]")
}

func (p PR) labelled(name string) bool {
	return slices.ContainsFunc(p.Labels, func(l Label) bool { return l.Name == name })
}

// The labels and markers the script reads.
const (
	labelDrill       = "factory/drill"
	labelRevertOf    = "factory/revert-of:#"
	labelRegression  = "factory/monitor:regression"
	stateClosed      = "CLOSED"
	stateMerged      = "MERGED"
	verdictHealthy   = "healthy"
	verdictRegressed = "regression"
)

var (
	wouldArm = regexp.MustCompile(`event=would-arm-[0-9]+ head=([0-9a-f]+)`)
	monitor  = regexp.MustCompile(`event=monitor-[0-9]+ verdict=([a-z]+)`)
	record   = regexp.MustCompile(`agent-factory-decision head=([^ ]+) class=([^ ]+) decision=([a-z_]+):([a-z_]+) files=([0-9]+|-)/([0-9]+) lines=([0-9]+|-)/([0-9]+) .*reviewer=([^ ]+) -->`)
)

// targets are the pull requests r reverts.
func (r Revert) targets(repo string) []int {
	var out []int
	re := regexp.MustCompile(`Reverts ` + regexp.QuoteMeta(repo) + `#([0-9]+)`)
	for _, m := range re.FindAllStringSubmatch(r.Body, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}
	for _, l := range r.Labels {
		if n, err := strconv.Atoi(strings.TrimPrefix(l.Name, labelRevertOf)); strings.HasPrefix(l.Name, labelRevertOf) && err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (r Revert) drill() bool {
	return slices.ContainsFunc(r.Labels, func(l Label) bool { return l.Name == labelDrill })
}

// reverted are the pull requests an open or merged revert reverts, drills apart (AM6).
func reverted(reverts []Revert, repo string, drill bool) map[int]bool {
	out := map[int]bool{}
	for _, r := range reverts {
		if r.State == stateClosed || r.drill() != drill {
			continue
		}
		for _, n := range r.targets(repo) {
			out[n] = true
		}
	}
	return out
}

// own are the factory's comments of p matching re, oldest first; the newest is the last.
func own(p PR, factory string, re *regexp.Regexp) []Comment {
	var out []Comment
	for _, c := range p.Comments {
		if login(c.Author.Login) == login(factory) && re.MatchString(c.Body) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Reviewer is one reviewer model's share of the judged pull requests (AM10).
type Reviewer struct {
	Model           string `json:"model"`
	MergedUnchanged int    `json:"mergedUnchanged"`
	Changed         int    `json:"changed"`
}

// ShadowTally is the script's default mode for one class: each merged pull request whose newest
// factory would-arm comment names the class, by what became of the head it judged.
type ShadowTally struct {
	MergedUnchanged int        `json:"mergedUnchanged"`
	Changed         int        `json:"changed"`
	Reverted        int        `json:"reverted"`
	AutoMerged      int        `json:"autoMerged"`
	Drill           int        `json:"drill"`
	Reviewers       []Reviewer `json:"reviewers"`
	// Refused and RefusedMergedUnchanged are --strictness: the newest decisions a class row refused,
	// and those a human then merged unchanged at that head (the gate's too-strict side).
	Refused                int `json:"refused"`
	RefusedMergedUnchanged int `json:"refusedMergedUnchanged"`
	// ByGeneration splits MergedUnchanged and Changed by the policy generation each decision was
	// made under (Q6): a bound class's records, newest generation first, then the ones made
	// before the binding (no repogen=, PR13). The card shows it; Evaluate counts them all.
	ByGeneration []GenerationCount `json:"byGeneration,omitempty"`
	// Judged and Drilled are the pull request numbers, for the drill check.
	Judged  []int `json:"-"`
	Drilled []int `json:"-"`
}

// GenerationCount is one policy generation's share of the shadow decisions, or, Before, the share
// decided before the class was bound.
type GenerationCount struct {
	Generation      int64 `json:"generation,omitempty"`
	Before          bool  `json:"before,omitempty"`
	MergedUnchanged int   `json:"mergedUnchanged"`
	Changed         int   `json:"changed"`
}

var (
	policyGen = regexp.MustCompile(` policygen=([0-9]+)`)
	repoGen   = regexp.MustCompile(` repogen=[0-9]+`)
)

// generationOf is the bucket a would-arm comment's record falls in: its policygen= when the record
// names repogen= (a bound class's decision), before otherwise.
func generationOf(body string) GenerationCount {
	m := record.FindString(body)
	if m == "" || !repoGen.MatchString(m) {
		return GenerationCount{Before: true}
	}
	g := policyGen.FindStringSubmatch(m)
	if g == nil {
		return GenerationCount{Before: true}
	}
	n, _ := strconv.ParseInt(g[1], 10, 64)
	return GenerationCount{Generation: n}
}

// Shadow counts prs as `shadow-merge-tally.sh --class class` does.
func Shadow(prs []PR, reverts []Revert, class, repo string, l Logins) ShadowTally {
	rev, drilled := reverted(reverts, repo, false), reverted(reverts, repo, true)
	var t ShadowTally
	models := map[string]*Reviewer{}
	gens := map[GenerationCount]*GenerationCount{}
	for _, p := range prs {
		t.strictness(p, class, l)
		mine := own(p, l.Factory, wouldArm)
		if len(mine) == 0 {
			continue
		}
		last := mine[len(mine)-1]
		if !strings.Contains(last.Body, "would auto-merge: `"+class+"`") {
			continue
		}
		head := wouldArm.FindStringSubmatch(last.Body)[1]
		by := ""
		if p.MergedBy != nil {
			by = login(p.MergedBy.Login)
		}
		t.Judged = append(t.Judged, p.Number)
		if drilled[p.Number] {
			t.Drill++
			t.Drilled = append(t.Drilled, p.Number)
		}
		model := "-"
		if m := record.FindStringSubmatch(last.Body); m != nil {
			model = m[9]
		}
		r := models[model]
		if r == nil {
			r = &Reviewer{Model: model}
			models[model] = r
		}
		switch {
		case by == login(l.Merger):
			t.AutoMerged++
		case rev[p.Number]:
			t.Reverted++
		case p.HeadRefOid == head:
			t.MergedUnchanged++
			r.MergedUnchanged++
			bucket(gens, last.Body).MergedUnchanged++
		default:
			t.Changed++
			r.Changed++
			bucket(gens, last.Body).Changed++
		}
	}
	for _, g := range gens {
		t.ByGeneration = append(t.ByGeneration, *g)
	}
	slices.SortFunc(t.ByGeneration, func(a, b GenerationCount) int { // newest first, before last
		if a.Before != b.Before {
			if a.Before {
				return 1
			}
			return -1
		}
		return cmp.Compare(b.Generation, a.Generation)
	})
	for _, m := range slices.Sorted(maps.Keys(models)) {
		if m != "-" {
			t.Reviewers = append(t.Reviewers, *models[m])
		}
	}
	return t
}

func bucket(gens map[GenerationCount]*GenerationCount, body string) *GenerationCount {
	k := generationOf(body)
	if gens[k] == nil {
		c := k
		gens[k] = &c
	}
	return gens[k]
}

// strictRows are the class rows whose refusal --strictness counts.
var strictRows = []string{"patch_too_large", "file_not_allowed", "file_protected", "file_added_or_deleted", "class_mismatch",
	"holdout_red", "oracle_finding_open", "oracle_unrequested", "release_age_red"}

var handOff = regexp.MustCompile(`event=human-([a-z_]+)-[0-9]+`)

// strictness counts p as --strictness does: its newest factory decision record, when it is
// class's, refused by a class row (a hand-off takes the reason its event names).
func (t *ShadowTally) strictness(p PR, class string, l Logins) {
	rs := own(p, l.Factory, record)
	if len(rs) == 0 {
		return
	}
	body := rs[len(rs)-1].Body
	m := record.FindStringSubmatch(body)
	if m[2] != class {
		return
	}
	verdict, reason := m[3], m[4]
	if h := handOff.FindStringSubmatch(body); h != nil {
		verdict, reason = "human", h[1]
	}
	if verdict != "human" || !slices.Contains(strictRows, reason) {
		return
	}
	t.Refused++
	if p.HeadRefOid == m[1] && (p.MergedBy == nil || login(p.MergedBy.Login) != login(l.Merger)) {
		t.RefusedMergedUnchanged++
	}
}

// OutcomeTally is the script's --outcomes mode: what became of the class's merges.
type OutcomeTally struct {
	Merges       int `json:"merges"`
	Mature       int `json:"mature"`
	Conclusive   int `json:"conclusive"`
	Inconclusive int `json:"inconclusive"`
	Bad          int `json:"bad"`
	Reverted     int `json:"reverted"`
	FollowUp     int `json:"followUp"`
	Regression   int `json:"regression"`
	Drill        int `json:"drill"`
	// Numbers and Drilled are the merges' numbers, for the drill check.
	Numbers []int `json:"-"`
	Drilled []int `json:"-"`
}

// Outcomes counts the class's merges (labelled factory/merged:<class> or factory/auto-merged:<class>,
// one per number) as `--outcomes --window window` does at now; followed names the merges a merged
// pull request labelled factory/follow-up-of:#<n> fixed.
func Outcomes(merges []PR, reverts []Revert, followed map[int]bool, repo string, l Logins, now time.Time, window time.Duration) OutcomeTally {
	rev, drilled := reverted(reverts, repo, false), reverted(reverts, repo, true)
	cutoff := now.Add(-window)
	var t OutcomeTally
	seen := map[int]bool{}
	for _, p := range merges {
		if seen[p.Number] {
			continue
		}
		seen[p.Number] = true
		t.Merges++
		t.Numbers = append(t.Numbers, p.Number)
		verdict := "none"
		if v := own(p, l.Factory, monitor); len(v) > 0 {
			verdict = monitor.FindStringSubmatch(v[len(v)-1].Body)[1]
		}
		isRev, isFollow := rev[p.Number], followed[p.Number]
		isReg := p.labelled(labelRegression) || verdict == verdictRegressed
		bad := isRev || isFollow || isReg
		if drilled[p.Number] {
			t.Drill++
			t.Drilled = append(t.Drilled, p.Number)
		}
		if p.MergedAt.After(cutoff) {
			continue // immature: neither counts nor blocks
		}
		t.Mature++
		if verdict != verdictHealthy && verdict != verdictRegressed && !bad {
			t.Inconclusive++
			continue
		}
		t.Conclusive++
		if bad {
			t.Bad++
		}
		t.Reverted += b2i(isRev)
		t.FollowUp += b2i(isFollow)
		t.Regression += b2i(isReg)
	}
	return t
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UpperBound is clopper-pearson.py's one-sided 95% upper bound on a failure rate after bad
// failures in n: the p at which P(X <= bad) = 0.05, X ~ Binomial(n, p), by the same 60 bisections.
// n == 0 bounds nothing: 1.
func UpperBound(bad, n int) float64 {
	if n == 0 {
		return 1
	}
	lo, hi := 0.0, 1.0
	for range 60 {
		p := (lo + hi) / 2
		if cdf(bad, n, p) > 0.05 {
			lo = p
		} else {
			hi = p
		}
	}
	return hi
}

// cdf is P(X <= k), X ~ Binomial(n, p), each term in log space so a large n never overflows.
func cdf(k, n int, p float64) float64 {
	sum := 0.0
	for i := 0; i <= k; i++ {
		ln, _ := math.Lgamma(float64(n + 1))
		li, _ := math.Lgamma(float64(i + 1))
		lni, _ := math.Lgamma(float64(n - i + 1))
		sum += math.Exp(ln - li - lni + float64(i)*math.Log(p) + float64(n-i)*math.Log1p(-p))
	}
	return sum
}

// Budget is the class bad-rate budget the widening bound is compared with (MP7's 10%).
const Budget = 0.10

// The drill states: a factory/drill revert (AM6) of one of the class's pull requests merged, one
// still open, or none.
const (
	DrillPassed = "passed"
	DrillOpen   = "open"
	DrillNone   = "none"
)

// The reasons a class is not eligible, in this order.
const (
	BlockBelowMinimum = "below_minimum"
	BlockBadOutcome   = "bad_outcome"
	BlockNoDrill      = "no_drill"
)

// Step is one rung of the widening ladder (MP7): Merges mature, conclusive merges with at most
// MaxBad bad bound the bad rate under Budget.
type Step struct {
	Merges  int  `json:"merges"`
	MaxBad  int  `json:"maxBad"`
	Reached bool `json:"reached"`
}

// Evidence is a class's promotion evidence and MP7's verdict on it.
type Evidence struct {
	Shadow   ShadowTally  `json:"shadow"`
	Outcomes OutcomeTally `json:"outcomes"`
	// Clean is the shadow decisions merged unchanged at their exact heads; Bad the bad mature
	// outcomes; Immature the merges younger than the follow-up window; Pending the mature ones
	// with no verdict yet.
	Clean    int      `json:"clean"`
	Bad      int      `json:"bad"`
	Immature int      `json:"immature"`
	Pending  int      `json:"pending"`
	Bound    float64  `json:"bound"`
	Budget   float64  `json:"budget"`
	Widen    bool     `json:"widen"`
	Ladder   []Step   `json:"ladder"`
	Drill    string   `json:"drill"`
	Minimum  int      `json:"minimum"`
	Eligible bool     `json:"eligible"`
	Blockers []string `json:"blockers,omitempty"`
}

// Evaluate is MP7 on one class: eligible with at least minimum clean decisions, no bad mature
// outcome, and a passed drill on one of its pull requests.
func Evaluate(sh ShadowTally, out OutcomeTally, reverts []Revert, repo string, minimum int) Evidence {
	e := Evidence{Shadow: sh, Outcomes: out, Clean: sh.MergedUnchanged, Bad: out.Bad,
		Immature: out.Merges - out.Mature, Pending: out.Inconclusive,
		Bound: UpperBound(out.Bad, out.Conclusive), Budget: Budget, Minimum: minimum, Drill: DrillNone}
	e.Widen = e.Bound < Budget
	for i, n := range []int{29, 46, 61} {
		e.Ladder = append(e.Ladder, Step{Merges: n, MaxBad: i, Reached: out.Conclusive >= n && out.Bad <= i})
	}
	mine := map[int]bool{}
	for _, n := range slices.Concat(sh.Judged, out.Numbers) {
		mine[n] = true
	}
	for _, r := range reverts {
		if !r.drill() || r.State == stateClosed || !slices.ContainsFunc(r.targets(repo), func(n int) bool { return mine[n] }) {
			continue
		}
		if r.State == stateMerged {
			e.Drill = DrillPassed
		} else if e.Drill != DrillPassed {
			e.Drill = DrillOpen
		}
	}
	if e.Clean < minimum {
		e.Blockers = append(e.Blockers, BlockBelowMinimum)
	}
	if e.Bad > 0 {
		e.Blockers = append(e.Blockers, BlockBadOutcome)
	}
	if e.Drill != DrillPassed {
		e.Blockers = append(e.Blockers, BlockNoDrill)
	}
	e.Eligible = len(e.Blockers) == 0
	return e
}
