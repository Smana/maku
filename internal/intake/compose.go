// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/factory/sanitize"
)

// ComposeIn is what a plan takes from outside the reply: the surface and model it was asked on, when,
// its revision, the config's class table, and — when the broker is answering in a Slack channel —
// that channel's ceiling (CD17).
type ComposeIn struct {
	Surface Surface
	Model   string
	// ServedModel is the model the gateway reported answering, recorded in `planner.servedModel`.
	ServedModel string
	Now         time.Time
	Rev         int
	Round       int
	RoundsLeft  int
	Classes     map[string]Class
	// CeilingUSD and CeilingSource are the channel's ceiling, when the channel has one: `maxCostUSD`
	// and the string that says so (`channel:C1.maxCostUSD`). Zero means no channel ceiling.
	CeilingUSD    float64
	CeilingSource string
	// Logger receives the one line naming every value the reply named that the config owns. A nil
	// logger discards it: the plan is the answer, and the line is the operator's.
	Logger *slog.Logger
}

// Compose is the strict path from a reply to a plan (CD17, CD27): the reply becomes a plan, or it
// becomes a refusal — never half of one and never a plan config did not write the roles, the ceiling
// and the class of.
//
// What config owns, config writes:
//   - each issue's `class` is the reply's choice, checked against `in.Classes`; a class the table does
//     not define is refused (`invalid_class`), never guessed;
//   - each issue's `roles` are that class's template, and every role the reply named is dropped;
//   - the plan's `ceilingUSD` is the **sum** of its issues' class ceilings — two $2.00 issues cost
//     $4.00, because a plan of two issues spends two classes' `taskUSD` — lowered to the channel's when
//     the channel's is lower, and `source` names every class that contributed, or the channel when it
//     won (controller ruling, fix round 1);
//   - `dependsOn` keeps only the indices of earlier issues, and an entry that names anything else
//     drops the list: a plan is ordered, never circular.
//
// What the broker owns, the broker empties: an `Evidence.URL` the reply sent is dropped, because a
// link a person clicks is one the broker built from a repository it verified (§security). The text
// stays — it is what the reply says it read — but its kind becomes `model_claim`: nothing on this
// path reads the repository, so a `path` the model cites is not a path anyone checked (#456).
//
// The repository is the candidate's spelling: lowercased, because the guard hands candidates over
// lowercased and a plan's hash must not depend on how the model capitalised a name.
//
// A plan from Compose carries its hash, and holds `Check` — the same re-validation `issue_file` runs.
func Compose(d Draft, in ComposeIn) (Plan, error) {
	if in.Surface != SurfaceSlack && in.Surface != SurfaceSession {
		return Plan{}, refuse(ReasonPlannerInvalid,
			fmt.Sprintf("surface %s, the contract's are %s and %s", strconv.Quote(string(in.Surface)), SurfaceSlack, SurfaceSession))
	}
	if detail := d.invalid(); detail != "" {
		return Plan{}, refuse(ReasonPlannerInvalid, detail)
	}

	p := Plan{
		APIVersion:   APIVersion,
		Rev:          in.Rev,
		Round:        in.Round,
		RoundsLeft:   in.RoundsLeft,
		Planner:      Planner{Model: in.Model, ServedModel: in.ServedModel, Surface: string(in.Surface), At: in.Now},
		Repository:   strings.ToLower(strings.TrimSpace(d.Repository)),
		Alternatives: slices.Clone(d.Alternatives),
	}
	// Every value the reply named that config owns, in the one line an operator reads: the fields,
	// never the values the model chose.
	dropped := make([]string, 0, len(d.Issues))

	// The ceiling the plan is charged against: the sum of its issues' class ceilings — a plan of two
	// issues spends two classes' taskUSD, not the larger of them (controller ruling, fix round 1) —
	// then the channel's when that is lower. Issues in array order, so two processes agree.
	chosen := make([]Class, 0, len(d.Issues))
	for i, is := range d.Issues {
		class, ok := in.Classes[is.Class]
		if !ok {
			return Plan{}, invalidClass(i+1, is.Class)
		}
		// The validator's own message is the one a person reads, so its refusal is verbatim.
		checked, err := sanitize.CheckDraft(is.Title, is.Body)
		if err != nil {
			return Plan{}, &Refusal{Reason: ReasonInvalidDraft, Sentence: err.Error()}
		}
		if len(is.Roles) > 0 {
			dropped = append(dropped, fmt.Sprintf("roles@%d", i+1))
		}
		if is.BudgetUSD != 0 {
			dropped = append(dropped, fmt.Sprintf("budgetUSD@%d", i+1))
		}
		// The classes the money comes from, in issue order: sumCeilings adds them up, once, for
		// this path and for the composer's (CD17).
		chosen = append(chosen, class)
		p.Issues = append(p.Issues, Issue{
			Index:     i + 1,
			Title:     checked.Title,
			Body:      checked.Body,
			Class:     is.Class,
			Roles:     slices.Clone(class.Roles),
			DependsOn: earlier(is.DependsOn, i+1),
		})
	}
	ceiling, source := sumCeilings(chosen)
	if in.CeilingUSD > 0 && in.CeilingUSD < ceiling {
		ceiling, source = in.CeilingUSD, in.CeilingSource
	}
	p.Budget = Budget{CeilingUSD: ceiling, Source: source}
	p.RepositoryEvidence = evidenceWithoutLinks(d.Evidence)
	p = WithGaps(p, d.Gaps)
	p.Hash = Hash(p)

	if len(dropped) > 0 {
		// The fields, never the repository and never the values the model chose: at this point in the
		// call nothing has checked the repository against the candidates, and this line is an
		// operator's, not a person's (fix round 1, finding 2).
		in.log().Warn("intake: the reply named values the config owns; dropped", "dropped", dropped)
	}
	return p, nil
}

// sumCeilings is what a plan of these classes is charged against, and the source that names where the
// money came from: the sum of the class ceilings — a plan of two issues spends two classes' taskUSD,
// not the larger of them (controller ruling, fix round 1) — and one source entry per class that
// contributed, in issue order and once each, so the string is the sum's own account of itself.
//
// It is the one place this package adds money up: Compose hands it every issue's class and the
// composer hands it the one class it files in, so the two paths cannot disagree about a ceiling
// (CD17).
func sumCeilings(chosen []Class) (float64, string) {
	ceiling := 0.0
	sources := make([]string, 0, len(chosen))
	for _, class := range chosen {
		ceiling += class.CeilingUSD
		if !slices.Contains(sources, class.Source) {
			sources = append(sources, class.Source)
		}
	}
	return ceiling, strings.Join(sources, "+")
}

// earlier is deps when every entry names an issue before index, and nil otherwise: a dependency the
// plan cannot honour is worse than no dependency, because the factory would order work that is not
// there.
func earlier(deps []int, index int) []int {
	kept := make([]int, 0, len(deps))
	for _, dep := range deps {
		if dep < 1 || dep >= index {
			return nil
		}
		kept = append(kept, dep)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// ModelClaim is the kind of every evidence line the model wrote. The broker reads no repository on
// the planning path, so none of it is verified, and the kind says so instead of the model's own
// `path`.
const ModelClaim = "model_claim"

// evidenceWithoutLinks is the reply's evidence with every URL emptied and every kind set to
// ModelClaim: the model may say what it read, only the broker says where a link goes, and only a
// check could call a claim a path.
func evidenceWithoutLinks(in []Evidence) []Evidence {
	if len(in) == 0 {
		return nil
	}
	out := make([]Evidence, 0, len(in))
	for _, e := range in {
		out = append(out, Evidence{Kind: ModelClaim, Text: e.Text})
	}
	return out
}

// log is the injected logger, or one that discards: a package never reaches the process default.
func (in ComposeIn) log() *slog.Logger {
	if in.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return in.Logger
}
