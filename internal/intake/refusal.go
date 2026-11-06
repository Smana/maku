// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Smana/maku/internal/factory/sanitize"
)

// The closed refusal list: the reasons the v3.x conversational intake design names, plus the ones it
// leaves to the builder, and nothing else. Each is a constant here with one sentence in sentences,
// so no call site invents vocabulary: a reason a metric labels, an audit line records and a person
// reads has exactly one spelling.
const (
	// ReasonPrivateRepository refused before any model call: the planner's route is public-only.
	ReasonPrivateRepository = "private_repository_unsupported"
	// ReasonRepositoryNotAllowed: no repository the allowlist serves and the requester may write in
	// matched the request. The one sentence never names a repository.
	ReasonRepositoryNotAllowed = "repository_not_allowed"
	// ReasonNoWrite: the requester cannot write to the one repository they named.
	ReasonNoWrite = "no_write"
	// ReasonInvalidDraft is `sanitize.CheckDraft`'s own message, verbatim.
	ReasonInvalidDraft = "invalid_draft"
	// ReasonPlannerInvalid: the reply, or the plan, is not the shape the contract asks for.
	ReasonPlannerInvalid = "planner_invalid"
	// ReasonPlannerUnavailable: the plan service is down or over budget.
	ReasonPlannerUnavailable = "planner_unavailable"
	// ReasonPlannerTimeout: the one call outlived its deadline.
	ReasonPlannerTimeout = "planner_timeout"
	// ReasonPlanChanged: the plan was superseded; the newer one is the one to confirm.
	ReasonPlanChanged = "plan_changed"
	// ReasonRepositoryUnverified: GitHub could not say whether the repository is public.
	ReasonRepositoryUnverified = "repository_unverified"
	// ReasonRepositoryNotInCandidates: the plan names a repository outside the candidate set. The
	// outsider is never echoed: naming it back would confirm a repository the requester may not know
	// exists (CD5, §security).
	ReasonRepositoryNotInCandidates = "repository_not_in_candidates"
	// ReasonInvalidClass: the class is not one this broker's config defines.
	ReasonInvalidClass = "invalid_class"
	// ReasonNotLinked: the requester's account is not linked to GitHub, so write access is unknown.
	ReasonNotLinked = "not_linked"
	// ReasonDailyPlansReached: the requester has used their day's plans.
	ReasonDailyPlansReached = "daily_plans_reached"
	// ReasonPlanTooLarge: a field is longer, or holds more items, than the contract's bound.
	ReasonPlanTooLarge = "plan_too_large"
)

// sentences is the closed list's one sentence each: what happened and what the reader can do, with
// no repository, no class the broker did not define and no plan text in it. `%s` is where a
// diagnostic — the field and the bound — goes, so it reads beside the diagnosis rather than after
// the remedy; a sentence without one takes no diagnostic, and refuse() enforces that.
var sentences = map[string]string{
	ReasonPrivateRepository:         "the planner reads public text only, and this repository is not public; file the draft with `issue_draft` and `issue_file` using your own model, or wait for the internal route",
	ReasonRepositoryNotAllowed:      "no repository the allowlist serves and you may write in matches this request; name one of the repositories listed",
	ReasonNoWrite:                   "you cannot write to this repository; ask for one you may write in, or show the draft to someone who can file it",
	ReasonInvalidDraft:              "the draft is not the issue template `<area>: <what is wrong>`, then the three headings in order",
	ReasonPlannerInvalid:            "the plan is not the shape the contract asks for (%s); ask again",
	ReasonPlannerUnavailable:        "the plan service is unavailable; try again later",
	ReasonPlannerTimeout:            "the plan service did not answer inside its deadline; try again",
	ReasonPlanChanged:               "the plan was superseded; the newer one is the one to confirm",
	ReasonRepositoryUnverified:      "GitHub could not confirm this repository, so the planner will not guess; try again later",
	ReasonRepositoryNotInCandidates: "the plan names a repository outside the ones this request may use; name one of them",
	ReasonInvalidClass:              "the class is not one this broker's config defines (%s); name one of the classes it serves",
	ReasonNotLinked:                 "your account is not linked to GitHub, so the broker cannot tell what you may write; link it first",
	ReasonDailyPlansReached:         "you have used today's plans; the cap resets at 00:00 UTC",
	ReasonPlanTooLarge:              "the plan is longer than a bound the contract sets (%s); nothing was truncated",
}

// The contract's bounds that Check applies. A draft's title and body are `sanitize.CheckDraft`'s
// bounds, and they are refused with its own sentence.
const (
	maxIssues        = 3
	maxEvidence      = 5
	maxEvidenceRunes = 280
)

// Refusal is why a plan was not made or not filed, whole. It is the answer a caller maps to one line,
// and it implements error so a tool's writer finds it with errors.As and shows Sentence rather than a
// failure's text.
type Refusal struct {
	// Reason is one of the closed list's constants: the metric label and the audit field.
	Reason string `json:"reason"`
	// Sentence is one line for the person: what happened and what they can do.
	Sentence string `json:"sentence"`
}

// Error is `reason: sentence`, except where the sentence already starts with it — a
// `sanitize.CheckDraft` message is refused verbatim, and the reader sees it once.
func (r Refusal) Error() string {
	if strings.HasPrefix(r.Sentence, r.Reason+":") {
		return r.Sentence
	}
	return r.Reason + ": " + r.Sentence
}

// refuse is a refusal of reason, with the diagnostic that names the field and the bound placed where
// its one sentence puts it. detail must never carry a repository name or plan text.
func refuse(reason, detail string) *Refusal {
	s, ok := sentences[reason]
	if !ok {
		// The list is closed: a reason without a sentence is a programming error, and inventing one
		// at a call site is what this table exists to prevent.
		panic("intake: refusal reason outside the closed list: " + reason)
	}
	switch scope := strings.Contains(s, "%s"); {
	case detail == "" && scope:
		// The sentence names its diagnostic's place, so it is never emitted unfilled: "(%s)" in a
		// person's line is the same defect as a reason nobody can label.
		panic("intake: refusal " + reason + " takes a diagnostic")
	case detail != "" && !scope:
		panic("intake: refusal " + reason + " takes no diagnostic")
	case detail != "":
		// detail is the format's argument, never its format.
		s = fmt.Sprintf(s, detail)
	}
	return &Refusal{Reason: reason, Sentence: s}
}

// invalidClass is the one sentence a class outside the config's table is refused with, wherever it is
// found: Compose refuses it before a plan exists, and Check refuses it on a plan read back.
func invalidClass(index int, class string) *Refusal {
	return refuse(ReasonInvalidClass,
		fmt.Sprintf("issue %d: class %s is not in the class table", index, strconv.Quote(class)))
}

// Check holds a plan to the contract's closed rules before anything is filed, in this order: the
// version, the issue count, each draft through `sanitize.CheckDraft`, the repository against the
// candidate set, each class against the config's table, the evidence bounds and the one question.
// It returns nil when the plan holds. It is the re-validation every consumer runs — `issue_file` and
// `run_create` alike — so a plan that changed after it was shown is refused here and nowhere else.
//
// A repository outside candidates is refused whole and never named (Review Focus 1): the plan may be
// guessing at repositories the requester does not know exist. Diagnostics name the field and the
// bound; a draft's message is `CheckDraft`'s own, verbatim.
func Check(p Plan, candidates []string, classes map[string]Class) *Refusal {
	if p.APIVersion != APIVersion {
		return refuse(ReasonPlannerInvalid, "apiVersion "+strconv.Quote(p.APIVersion)+", the contract's is "+APIVersion)
	}
	if n := len(p.Issues); n < 1 || n > maxIssues {
		return refuse(ReasonPlannerInvalid, fmt.Sprintf("issues: %d, the contract asks for 1 to %d", n, maxIssues))
	}
	for _, is := range p.Issues {
		if _, err := sanitize.CheckDraft(is.Title, is.Body); err != nil {
			return &Refusal{Reason: ReasonInvalidDraft, Sentence: err.Error()}
		}
	}
	if !slices.ContainsFunc(candidates, func(c string) bool { return strings.EqualFold(c, p.Repository) }) {
		return refuse(ReasonRepositoryNotInCandidates, "")
	}
	for _, is := range p.Issues {
		if _, ok := classes[is.Class]; !ok {
			return invalidClass(is.Index, is.Class)
		}
	}
	if n := len(p.RepositoryEvidence); n > maxEvidence {
		return refuse(ReasonPlanTooLarge, fmt.Sprintf("repositoryEvidence: %d items, the bound is %d", n, maxEvidence))
	}
	for i, e := range p.RepositoryEvidence {
		if n := utf8.RuneCountInString(e.Text); n > maxEvidenceRunes {
			return refuse(ReasonPlanTooLarge,
				fmt.Sprintf("repositoryEvidence[%d].text: %d characters, the bound is %d", i, n, maxEvidenceRunes))
		}
	}
	if q := p.Question; q != nil && (strings.TrimSpace(q.Text) == "" || strings.TrimSpace(q.Recommended.Answer) == "") {
		// At most one question is the shape's (Question is one value); a question with nothing to
		// answer is the one way a plan can carry two halves of a question and neither of them.
		return refuse(ReasonPlannerInvalid, "question: it carries text and a recommended answer, or it is absent")
	}
	return nil
}
