// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"errors"
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
	// ReasonUnresolvedMarker: a final plan still has a ranked gap or a live clarification marker.
	ReasonUnresolvedMarker = "unresolved_marker"
	// ReasonInvalidDecisions: a final issue's Decisions section does not hold the closed grammar.
	ReasonInvalidDecisions = "invalid_decisions"
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
	// ReasonRepositoryNotServedHere: the request names a repository that is not in the candidate set.
	// Unlike repository_not_in_candidates it names that repository and the candidates, because the
	// requester typed the name: the plan is never made for a different one (#469).
	ReasonRepositoryNotServedHere = "repository_not_served_here"
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
	ReasonUnresolvedMarker:          "the plan still has a clarification to answer; resolve every gap and remove every marker before filing",
	ReasonInvalidDecisions:          "the Decisions section is not valid; use ascending, unique single-line D/A records with positive gap references",
	ReasonPlannerInvalid:            "the plan is not the shape the contract asks for (%s); ask again",
	ReasonPlannerUnavailable:        "the plan service is unavailable; try again later",
	ReasonPlannerTimeout:            "the plan service did not answer inside its deadline; try again",
	ReasonPlanChanged:               "the plan was superseded; the newer one is the one to confirm",
	ReasonRepositoryUnverified:      "GitHub could not confirm this repository, so the planner will not guess; try again later",
	ReasonRepositoryNotInCandidates: "the plan names a repository outside the ones this request may use; name one of them",
	ReasonRepositoryNotServedHere:   "%s; an operator adds it to the repositories served here",
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

// ReasonRefusal is reason's refusal, in the closed list's one sentence: for the reasons a caller
// decides itself rather than receives from a call — a Slack user the link tables do not know
// (`not_linked`), a spent daily cap (`daily_plans_reached`), a check that could not be made
// (`repository_unverified`) — so that no call site writes the sentence a person reads. A reason
// outside the list, or one whose sentence names a diagnostic, panics as refuse does: this is for the
// sentences that stand alone.
func ReasonRefusal(reason string) *Refusal { return refuse(reason, "") }

// RefusalOf is the closed refusal err spells, when err is one of the model call's sentinels:
// ErrInvalid is `planner_invalid` with the field and the bound the error names, ErrTimeout is
// `planner_timeout` and ErrUnavailable is `planner_unavailable`, each in the closed list's sentence
// rather than the vendor's text. ok is false for an error that is none of the three, and a caller
// then answers its own rather than passing a transport failure off as a refusal.
func RefusalOf(err error) (*Refusal, bool) {
	switch {
	case errors.Is(err, ErrInvalid):
		return refuse(ReasonPlannerInvalid, diagnostic(err)), true
	case errors.Is(err, ErrTimeout):
		return refuse(ReasonPlannerTimeout, ""), true
	case errors.Is(err, ErrUnavailable):
		return refuse(ReasonPlannerUnavailable, ""), true
	}
	return nil, false
}

// diagnostic is err's words after its sentinel's own reason: the field and the bound model.go named,
// which is what the `planner_invalid` sentence has a place for. It never carries plan text, since
// model.go's details name fields and bounds alone.
func diagnostic(err error) string {
	if _, rest, ok := strings.Cut(err.Error(), ": "); ok {
		return rest
	}
	return err.Error()
}

// Check holds a plan to the contract's closed rules before anything is filed, in this order: the
// version, the issue count, the numbering and the dependency order, each draft through
// `sanitize.CheckDraft`, the repository and every alternative against the candidate set, each class
// against the config's table, the evidence bounds and the one question.
// It returns nil when the plan holds. It is the re-validation every consumer runs — `issue_file` and
// `run_create` alike — so a plan that changed after it was shown is refused here and nowhere else.
//
// The issues' indices are the contract's numbering, not a label a plan carries: they are 1..n in
// array order, and a dependency names an earlier issue only. `Compose` writes them that way, so the
// rule is a no-op on a fresh plan and the whole of what a read-back plan can get wrong — a duplicate
// index filed twice, a gap, a dependency on work a filing pass has not reached.
//
// A repository outside candidates — the plan's own, or one named as an alternative — is refused whole
// and never named (Review Focus 1): the plan may be guessing at repositories the requester does not
// know exist, and an alternative is shown to the person as "also fits". Diagnostics name the field and
// the bound; a draft's message is `CheckDraft`'s own, verbatim.
func Check(p Plan, candidates []string, classes map[string]Class) *Refusal {
	if p.APIVersion != APIVersion {
		return refuse(ReasonPlannerInvalid, "apiVersion "+strconv.Quote(p.APIVersion)+", the contract's is "+APIVersion)
	}
	if n := len(p.Issues); n < 1 || n > maxIssues {
		return refuse(ReasonPlannerInvalid, fmt.Sprintf("issues: %d, the contract asks for 1 to %d", n, maxIssues))
	}
	for i, is := range p.Issues {
		if is.Index != i+1 {
			return refuse(ReasonPlannerInvalid,
				fmt.Sprintf("issue %d: index %d, the contract numbers its issues from 1 in order", i+1, is.Index))
		}
		for _, dep := range is.DependsOn {
			if dep < 1 || dep >= is.Index {
				return refuse(ReasonPlannerInvalid,
					fmt.Sprintf("issue %d: dependsOn %d, which is not an earlier issue", is.Index, dep))
			}
		}
	}
	for _, is := range p.Issues {
		if _, err := sanitize.CheckDraft(is.Title, is.Body); err != nil {
			return &Refusal{Reason: ReasonInvalidDraft, Sentence: err.Error()}
		}
	}
	if !isCandidate(candidates, p.Repository) {
		return refuse(ReasonRepositoryNotInCandidates, "")
	}
	for _, a := range p.Alternatives {
		// An alternative is display, and display is shown: naming an outsider as "also fits" would
		// tell the requester a repository exists that this request never allowed (CD5, §security).
		if !isCandidate(candidates, a.Repository) {
			return refuse(ReasonRepositoryNotInCandidates, "")
		}
	}
	for _, is := range p.Issues {
		if _, ok := classes[is.Class]; !ok {
			return invalidClass(is.Index, is.Class)
		}
		if is.Profile != "" && !profileNameRE.MatchString(is.Profile) {
			// Whether a pin holds is the catalogue's to say (CheckPin, at the door that reads one);
			// here it is only held to the shape a profile's name has, since it lands in a body and a label.
			return refuse(ReasonPlannerInvalid, fmt.Sprintf("issue %d: profile is a profile's name", is.Index))
		}
		if s := is.Suggestion; s != nil {
			if n := utf8.RuneCountInString(s.Why); n > MaxWhyRunes {
				return refuse(ReasonPlanTooLarge,
					fmt.Sprintf("issues[%d].suggestion.why: %d characters, the bound is %d", is.Index-1, n, MaxWhyRunes))
			}
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

// CheckFinal is Check plus the external filing rules. Intermediate plans may carry ranked gaps and
// their body markers; a final plan may carry neither, and every issue's optional Decisions section
// must hold the shared grammar. The two new refusals use the closed list's fixed sentences rather
// than echoing draft text.
func CheckFinal(p Plan, candidates []string, classes map[string]Class) *Refusal {
	if r := Check(p, candidates, classes); r != nil {
		return r
	}
	if len(p.Gaps) > 0 {
		return refuse(ReasonUnresolvedMarker, "")
	}
	for _, is := range p.Issues {
		if _, err := sanitize.CheckFinalDraft(is.Title, is.Body); err != nil {
			switch {
			case strings.HasPrefix(err.Error(), "unresolved_marker:"):
				return refuse(ReasonUnresolvedMarker, "")
			case strings.HasPrefix(err.Error(), "invalid_decisions:"):
				return refuse(ReasonInvalidDecisions, "")
			default:
				// Check already ran the same template validation, so this is defensive only.
				return &Refusal{Reason: ReasonInvalidDraft, Sentence: err.Error()}
			}
		}
	}
	return nil
}

// isCandidate is whether repository is one of candidates, as GitHub reads a name: without regard to
// case. It is the whole of the plan's repository rule, applied to the plan's own repository and to
// every alternative, so an outsider cannot enter a plan through the display fields.
func isCandidate(candidates []string, repository string) bool {
	return slices.ContainsFunc(candidates, func(c string) bool { return strings.EqualFold(c, repository) })
}

// The closed list of profile refusals (P12): why a pinned harness profile was not applied, shared by
// intake (CheckPin) and triage (ResolvePin), so a metric labels one spelling and a reader gets one
// sentence. A pin refused at triage is never a task failure: the task runs as if unpinned.
const (
	ProfileUnknown       = "profile_unknown"
	ProfileNotAllowed    = "profile_not_allowed"
	ProfileNotOffered    = "profile_not_offered" // intake only
	ClassProfileFixed    = "class_profile_fixed"
	ClassNoProfiles      = "class_no_profiles"
	NotMaintainer        = "not_maintainer"
	ProfileConflict      = "profile_conflict" // triage only
	ProfileInternal      = "profile_internal"
	ProfileNoTestsPaths  = "profile_no_tests_paths" // triage only
	CatalogueUnavailable = "catalogue_unavailable"  // intake only
)

// ProfileRefusals is the closed list, in the spec's order.
var ProfileRefusals = []string{ProfileUnknown, ProfileNotAllowed, ProfileNotOffered, ClassProfileFixed, ClassNoProfiles,
	NotMaintainer, ProfileConflict, ProfileInternal, ProfileNoTestsPaths, CatalogueUnavailable}

// ProfileRefusal is one refused pin with the fields its sentence names. It is kept apart from
// refuse()'s single-diagnostic rule because these sentences take several fields. Every field is a
// config name the reader may see in the catalogue: none is a repository.
type ProfileRefusal struct {
	Code, Class, Profile, Floor, Mode string
	Allowed                           []string
}

// Sentence is the code's one sentence. The switch is closed: a code outside the list panics, the
// same programming error refuse() panics on.
func (r ProfileRefusal) Sentence() string {
	switch r.Code {
	case ProfileUnknown:
		return fmt.Sprintf("`%s` is not a profile of this factory. `classes_list` shows the profiles.", r.Profile)
	case ProfileNotAllowed:
		runs := "`" + r.Floor + "`"
		if len(r.Allowed) > 0 {
			runs += " or `" + strings.Join(r.Allowed, "`, `") + "`"
		}
		return fmt.Sprintf("Class `%s` runs under %s; `%s` is not one of them.", r.Class, runs, r.Profile)
	case ProfileNotOffered:
		return fmt.Sprintf("`%s` sets no template, so its roles are chosen at triage; it runs only as the factory's choice.", r.Profile)
	case ClassProfileFixed:
		return fmt.Sprintf("Class `%s` is %s: its merge policy runs it under `%s` only.", r.Class, r.Mode, r.Floor)
	case ClassNoProfiles:
		return fmt.Sprintf("Class `%s` declares no profiles: the factory runs it as configured.", r.Class)
	case NotMaintainer:
		return "Only a maintainer of this repository can choose the profile; the factory will choose it."
	case ProfileConflict:
		return "The issue names more than one profile; none was applied."
	case ProfileInternal:
		return fmt.Sprintf("`%s` writes tests first, which runs on public tasks only.", r.Profile)
	case ProfileNoTestsPaths:
		return fmt.Sprintf("`%s` writes tests first, and this repository declares no tests paths.", r.Profile)
	case CatalogueUnavailable:
		return "The factory's list of classes could not be read; try again in a minute."
	}
	panic("intake: profile refusal outside the closed list: " + r.Code)
}

// Refusal is r as a plan_check refusal: its code and its one sentence.
func (r ProfileRefusal) Refusal() *Refusal { return &Refusal{Reason: r.Code, Sentence: r.Sentence()} }
