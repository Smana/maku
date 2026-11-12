// SPDX-License-Identifier: Apache-2.0

// Package intake is the broker's governed planner (v3.x conversational intake): `plan/v1` and the
// canonical hash over its material set, the candidate and access guards that bound what a plan may
// name, and the one bounded model call whose strict decode is the only way a model's reply becomes
// a plan. Nothing here is reachable from a route: the tools that expose it are the broker's.
package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"
)

// APIVersion is the plan contract's one version. A plan whose apiVersion is any other value is
// refused whole: a consumer re-validates a plan before anything is filed, and a version it does not
// know is a plan it cannot re-validate.
const APIVersion = "plan/v2"

// APIVersionV1 is the version before the profile: recognised only by Upgrade, which shows the same
// issues again as plan/v2 with a new hash (P14). Check refuses it, so a v1 plan is never filed.
const APIVersionV1 = "plan/v1"

// MaxWhyRunes bounds a suggestion's why: one line of untrusted model text.
const MaxWhyRunes = 200

// Surface is where a plan was asked for. It is the metric label and the audit field, so the set is
// closed: a surface nobody labels is a plan nobody can account for.
type Surface string

// The surfaces the planner answers on.
const (
	SurfaceSlack   Surface = "slack"
	SurfaceSession Surface = "session"
)

// Plan is `plan/v1`: bounded by construction, every string data, and the whole of what a person is
// asked to confirm. Everything outside Material is display: a plan read back from a Slack message's
// metadata or a tool's result re-validates through Check and Hash, and neither reads a display
// field.
type Plan struct {
	APIVersion string `json:"apiVersion"`
	// Rev counts the plan's revisions in its thread or session. It is display: the hash binds the
	// plan, and a second Confirm is idempotent on the thread key, not on a revision number.
	Rev int `json:"rev"`
	// Round and RoundsLeft are the intake's clarification round and the rounds remaining before its
	// cap. Both are display only, and separate from Rev's consumer revision count.
	Round      int     `json:"round"`
	RoundsLeft int     `json:"roundsLeft"`
	Planner    Planner `json:"planner"`
	// Repository is the one repository the plan files into. It is material: a plan for another
	// repository is another plan.
	Repository string `json:"repository"`
	// RepositoryEvidence is why this repository, in the planner's words. Untrusted text, shown and
	// never followed; the URLs are the broker's, never the model's.
	RepositoryEvidence []Evidence `json:"repositoryEvidence"`
	// Alternatives are the other candidates that also fit, with their one line of evidence. Display
	// only: a plan names one repository to file into, and the alternatives are the question's
	// context.
	Alternatives []Alternative `json:"alternatives"`
	Issues       []Issue       `json:"issues"`
	// Budget is the ceiling the broker charges this plan's work against, from config (CD17).
	Budget Budget `json:"budget"`
	// Gaps are the ranked clarifications left in the issue bodies' markers. They are display only:
	// the markers themselves are already in Material.
	Gaps []Gap `json:"gaps"`
	// Question is the highest-ranked gap, kept for clients that render one question.
	Question *Question `json:"question"`
	// Hash is the canonical hash the broker computed over Material. It is display in the sense that
	// Hash never reads it: a plan arrives with the hash its producer claimed, and the consumer
	// recomputes over Material and compares.
	Hash string `json:"hash"`
}

// Planner is who composed a plan, on which surface, when. Display: the model and the surface are
// audited beside the hash, and a person reading a proposal is told what wrote it.
type Planner struct {
	Model string `json:"model"`
	// ServedModel is the `model` the gateway's reply named: what actually answered behind the
	// alias, so a swap behind `tier-frontier` shows in the plan. Empty when the reply names none
	// and for the composer. Display only: `Hash` never reads it.
	ServedModel string    `json:"servedModel,omitempty"`
	Surface     string    `json:"surface"`
	At          time.Time `json:"at"`
}

// Evidence is one reason the planner chose a repository: a path, a thread, a channel. Text is
// untrusted and inert; URL is either empty or a link the broker built, never one the model sent.
// Kind is `model_claim` for everything the model wrote (nothing on this path reads the repository to
// check it) and `candidate` for the composer's own line.
type Evidence struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}

// Alternative is another candidate that also fits, and why. Display only.
type Alternative struct {
	Repository string `json:"repository"`
	Why        string `json:"why"`
}

// Issue is one defect's draft, as `sanitize.CheckDraft` holds it, with the class it belongs to and
// the roles and order the broker derives. It is material in every field.
type Issue struct {
	Index int    `json:"index"`
	Title string `json:"title"`
	Body  string `json:"body"`
	// Class is the model's one sourced choice, checked against the config's class table: a class
	// the config does not define is refused (`invalid_class`), never guessed (CD17).
	Class string `json:"class"`
	// Profile is the pinned harness profile, "" for the factory's choice. Accepting a suggestion
	// pins nothing (P4): only a revision a person makes writes it.
	Profile string `json:"profile"`
	// Roles are the class's template, written by the broker from config. A role the model named is
	// dropped and logged (CD17).
	Roles []string `json:"roles"`
	// DependsOn is the indices of the earlier issues this one waits for; `Compose` keeps only the
	// values that name an earlier index, so a plan can be ordered but never circular.
	DependsOn []int `json:"dependsOn"`
	// Suggestion is the planner's suggested profile and the merge outcome the broker computed:
	// display, so a person sees it and confirms nothing by it. Nil when the plan was made without
	// the factory's catalogue.
	Suggestion *Suggestion `json:"suggestion,omitempty"`
}

// Suggestion is what the planner suggests for an issue and what the broker read from the catalogue
// to show beside it. Display only: the hash never reads it.
type Suggestion struct {
	// Profile is an offered profile of the issue's class, "" for the factory's choice.
	Profile string `json:"profile"`
	// Roles are the suggested profile's template roles, from the catalogue (G3).
	Roles []string `json:"roles"`
	// Merge is the broker's, from the class's mode and approveToMerge, never the model's (P3).
	Merge MergeShown `json:"merge"`
	// Why is the model's one line, at most MaxWhyRunes characters; untrusted text.
	Why string `json:"why"`
}

// MergeShown is how the issue's pull request would merge: outcome `auto`, `shadow`, `approval` or
// `maintainer`, and the repository's approveToMerge it was computed with.
type MergeShown struct {
	Outcome        string `json:"outcome"`
	ApproveToMerge bool   `json:"approveToMerge"`
}

// Budget is the ceiling this plan's work is charged against and where the number came from: the
// class's `taskUSD`, or the channel's lower `maxCostUSD`. The broker writes both (CD17); the display
// value is dollars, and Material hashes the same amount as an integer.
type Budget struct {
	CeilingUSD float64 `json:"ceilingUSD"`
	Source     string  `json:"source"`
}

// Gap is one ranked clarification. ID names its one [NEEDS CLARIFICATION: …] marker in an issue
// body; Section is what-is-wrong, what-it-should-be, acceptance-check, or scope.
type Gap struct {
	ID          string `json:"id"`
	Section     string `json:"section"`
	Text        string `json:"text"`
	Recommended Answer `json:"recommended"`
}

// WithGaps sets a plan's ranked display gaps and maps the first to the legacy Question view. The
// input is cloned so later caller edits cannot mutate a plan that has already been composed.
func WithGaps(p Plan, gaps []Gap) Plan {
	p.Gaps = slices.Clone(gaps)
	p.Question = nil
	if len(p.Gaps) > 0 {
		p.Question = &Question{Text: p.Gaps[0].Text, Recommended: p.Gaps[0].Recommended}
	}
	return p
}

// Question is the at most one thing the broker asks before filing, with its recommendation: a
// question without a recommendation is a question the reader has to answer without the broker's
// reading of the evidence.
type Question struct {
	Text        string `json:"text"`
	Recommended Answer `json:"recommended"`
}

// Answer is the recommendation, and why.
type Answer struct {
	Answer string `json:"answer"`
	Why    string `json:"why"`
}

// Class is one row of the broker's class table: the roles a plan of that class carries and the
// ceiling its work is charged against, from config or, per repository, from the factory's
// catalogue. `Compose` reads it and `Check` refuses a class that is not in the table.
type Class struct {
	Roles      []string
	CeilingUSD float64
	Source     string
	// The catalogue's reading of the class (ClassesFromCatalogue), all zero for a config row: its
	// description, mode (live, shadow, off or unarmed), floor profile, the offered profiles (floor
	// first) with their template roles, and the repository's approveToMerge.
	Description    string
	Mode           string
	Floor          string
	Offered        []string
	ProfileRoles   map[string][]string
	ApproveToMerge bool
}

// material is CD27's hashed set, in its fixed order: the displayed set and the hashed set are the
// same set, or a field a person can read is a field they cannot confirm.
type material struct {
	APIVersion string          `json:"apiVersion"`
	Repository string          `json:"repository"`
	Budget     materialBudget  `json:"budget"`
	Issues     []materialIssue `json:"issues"`
}

// materialIssue is one issue as the hash sees it: the field order below is the contract. Profile
// is plan/v2's; a plan/v1 is hashed without it, so it keeps the hash it was shown with.
type materialIssue struct {
	Index     int      `json:"index"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Class     string   `json:"class"`
	Profile   *string  `json:"profile,omitempty"`
	Roles     []string `json:"roles"`
	DependsOn []int    `json:"dependsOn"`
}

// The ceiling is hashed in micro-dollars: two processes formatting a float64 the same way is a
// promise Go makes and JSON does not, and a cent is the smallest amount that can be filed. The
// plan displays ceilingUSD; this field is the integer the hash covers (CD27).
type materialBudget struct {
	CeilingMicroUSD int64  `json:"ceilingMicroUSD"`
	Source          string `json:"source"`
}

// Material is the canonical JSON the hash covers: CD27's set, in its fixed order, issues in array
// order, compact, and with `[]` rather than `null` for an empty roles or dependsOn — a list that is
// absent and a list that is empty are the same promise, so they must be the same bytes.
func Material(p Plan) ([]byte, error) {
	m := material{
		APIVersion: p.APIVersion,
		Repository: p.Repository,
		Budget: materialBudget{
			CeilingMicroUSD: microUSD(p.Budget.CeilingUSD),
			Source:          p.Budget.Source,
		},
		Issues: make([]materialIssue, 0, len(p.Issues)),
	}
	for _, is := range p.Issues {
		mi := materialIssue{
			Index:     is.Index,
			Title:     is.Title,
			Body:      is.Body,
			Class:     is.Class,
			Roles:     list(is.Roles),
			DependsOn: list(is.DependsOn),
		}
		if p.APIVersion != APIVersionV1 {
			mi.Profile = &is.Profile // "" is hashed too: the factory's choice is a promise
		}
		m.Issues = append(m.Issues, mi)
	}
	j, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal plan material: %w", err)
	}
	return j, nil
}

// Hash is `sha256:<hex>` over Material: the value a workflow carries so that what it files is what
// the person confirmed. It never reads p.Hash, so a caller's claim cannot agree with itself.
func Hash(p Plan) string {
	m, err := Material(p)
	if err != nil {
		// Material marshals strings, ints and their slices: it has no failing input. Reaching this
		// is a programming error, and returning a hash of nothing would be a plan that "holds".
		panic(fmt.Sprintf("intake: hash a plan: %v", err))
	}
	sum := sha256.Sum256(m)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// JSON is the plan as a reader sees it: compact, the contract's order, and `[]` for the lists a plan
// always has. A plan without a hash gains the one Hash computes; a plan that arrived with one keeps
// the value it claims, so a consumer can show what it was given beside what it computed.
func (p Plan) JSON() ([]byte, error) {
	p.RepositoryEvidence = list(p.RepositoryEvidence)
	p.Alternatives = list(p.Alternatives)
	p.Gaps = list(p.Gaps)
	p.Issues = list(p.Issues)
	for i := range p.Issues {
		p.Issues[i].Roles = list(p.Issues[i].Roles)
		p.Issues[i].DependsOn = list(p.Issues[i].DependsOn)
		if s := p.Issues[i].Suggestion; s != nil {
			c := *s
			c.Roles = list(c.Roles)
			p.Issues[i].Suggestion = &c
		}
	}
	if p.Hash == "" {
		p.Hash = Hash(p)
	}
	j, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal plan: %w", err)
	}
	return j, nil
}

// microUSD is a dollar amount in millionths, rounded: a cent is the smallest amount that can be
// filed, and rounding once here keeps `2.00` from arriving as `2.0000000000000004`.
func microUSD(usd float64) int64 { return int64(math.Round(usd * 1e6)) }

// list is s, or an empty slice when s is nil: JSON's `null` and `[]` are different bytes and the
// same fact.
func list[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
