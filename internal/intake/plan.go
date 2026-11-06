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
	"time"
)

// APIVersion is the plan contract's one version. A plan whose apiVersion is any other value is
// refused whole: a consumer re-validates a plan before anything is filed, and a version it does not
// know is a plan it cannot re-validate.
const APIVersion = "plan/v1"

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
	Rev     int     `json:"rev"`
	Planner Planner `json:"planner"`
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
	// Question is the at-most-one thing the broker must ask before anything is filed.
	Question *Question `json:"question"`
	// Hash is the canonical hash the broker computed over Material. It is display in the sense that
	// Hash never reads it: a plan arrives with the hash its producer claimed, and the consumer
	// recomputes over Material and compares.
	Hash string `json:"hash"`
}

// Planner is who composed a plan, on which surface, when. Display: the model and the surface are
// audited beside the hash, and a person reading a proposal is told what wrote it.
type Planner struct {
	Model   string    `json:"model"`
	Surface string    `json:"surface"`
	At      time.Time `json:"at"`
}

// Evidence is one reason the planner chose a repository: a path, a thread, a channel. Text is
// untrusted and inert; URL is either empty or a link the broker built, never one the model sent.
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
	// Roles are the class's template, written by the broker from config. A role the model named is
	// dropped and logged (CD17).
	Roles []string `json:"roles"`
	// DependsOn is the indices of the earlier issues this one waits for; `Compose` keeps only the
	// values that name an earlier index, so a plan can be ordered but never circular.
	DependsOn []int `json:"dependsOn"`
}

// Budget is the ceiling this plan's work is charged against and where the number came from: the
// class's `taskUSD`, or the channel's lower `maxCostUSD`. The broker writes both (CD17); the display
// value is dollars, and Material hashes the same amount as an integer.
type Budget struct {
	CeilingUSD float64 `json:"ceilingUSD"`
	Source     string  `json:"source"`
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
// ceiling its work is charged against, both from config. It has no methods by design — `Compose`
// reads it and `Check` refuses a class that is not in the table, and neither asks it anything.
type Class struct {
	Roles      []string
	CeilingUSD float64
	Source     string
}

// material is CD27's hashed set, in its fixed order: the displayed set and the hashed set are the
// same set, or a field a person can read is a field they cannot confirm.
type material struct {
	APIVersion string          `json:"apiVersion"`
	Repository string          `json:"repository"`
	Budget     materialBudget  `json:"budget"`
	Issues     []materialIssue `json:"issues"`
}

// materialIssue is one issue as the hash sees it: the field order below is the contract.
type materialIssue struct {
	Index     int      `json:"index"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Class     string   `json:"class"`
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
		m.Issues = append(m.Issues, materialIssue{
			Index:     is.Index,
			Title:     is.Title,
			Body:      is.Body,
			Class:     is.Class,
			Roles:     list(is.Roles),
			DependsOn: list(is.DependsOn),
		})
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
	p.Issues = list(p.Issues)
	for i := range p.Issues {
		p.Issues[i].Roles = list(p.Issues[i].Roles)
		p.Issues[i].DependsOn = list(p.Issues[i].DependsOn)
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
