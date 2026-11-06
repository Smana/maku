// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Smana/maku/internal/factory/sanitize"
)

// TemplateLabel is what every surface says about a plan the composer wrote: "template draft: the plan
// service is unavailable" (CD18, §failures). It lives here so the surfaces that show a degraded plan
// say it in one spelling, and so the live proof can tell the two apart by reading it.
const TemplateLabel = "template draft: the plan service is unavailable"

// composerModel is what a plan no model wrote carries in `planner.model`. It is the field a surface,
// a dashboard and the live proof read to know this is not the natural-language experience: nothing
// else in this package writes it, so no plan can claim to be the composer's or the reverse.
const composerModel = "composer"

// composerArea is the title's area. The composer filed nothing and read nothing, so the one area it
// can name truly is the factory's own; the alternative is guessing a package from a sentence, which
// is the model's job and not this mode's.
const composerArea = "factory"

// What the composer writes where the request said nothing. A template that invented "what it should
// be" would be a model without a model: both sections say the request did not say.
const (
	notStatedShould  = "Not stated in the request: this is a template draft, so nothing here was read from the repository."
	notStatedCheck   = "Not stated in the request: the reviewer confirms the change removes the problem above."
	composerContext  = "Context they gave, as background only:"
	composerEvidence = "the only repository this request may use"
)

// ComposeTemplate is the degraded composer (CD18): a plan written by a template instead of a model,
// off by default and reachable only when `human.intake.plan.fallback: composer` names it (PR 2).
//
// It calls nothing. There is no request, no client, no alias and no key here: one issue, the three
// sections of the issue template in the template's order, the person's words in "what is wrong" and
// its context beneath them, and the composer's own defaults where the request said nothing.
//
// The title is `factory: <the request's first line>`: the composer read nothing, so the factory is
// the only area it can name truly, and borrowing an area from the sentence is the inference this mode
// does not make. A first line that already reads `docs: …` becomes `factory: docs: …` — still a title
// the validator holds, and the request keeps its own words. `CheckDraft` stays the judge of the
// title's shape, so a first line no title can carry is refused in the validator's words
// (`invalid_draft`), never truncated.
//
// It guesses no repository either: it files in the one candidate the request may use, copied as it
// was given, or it refuses (`repository_not_allowed`) without naming any of them (CD5, §security).
// With several candidates there is no deterministic answer to "which one", and a template that
// picked one would be the guess this mode exists to avoid.
//
// Roles and money are the config's, through the same sum Compose uses (CD17), and the class is the
// alphabetically first one the table defines — a pick, not an inference, so two processes reading one
// table write the same plan. Every other refusal is the model path's, with the same reason and the
// same field-and-bound detail, because the plan is held to the same contract: `planner_invalid` for a
// request the contract cannot carry, the validator's own `invalid_draft`, and `plan_too_large` for a
// body longer than 16 KiB — refused whole, never truncated.
//
// The plan carries no alternatives and no question: the composer has nothing to recommend and nothing
// to ask. `planner.at` is when it composed (display; `Hash` never covers it), and `planner.surface` is
// the caller's: the signature carries no surface, and labelling a Slack plan as a session's would be
// the one lie this mode must not tell. Every surface says TemplateLabel beside the plan regardless.
func ComposeTemplate(text, context string, candidates []string, classes map[string]Class) (Plan, *Refusal) {
	text, context = strings.TrimSpace(text), strings.TrimSpace(context)
	if n := len(text); n > maxText {
		return Plan{}, refuse(ReasonPlannerInvalid, fmt.Sprintf("text: %d bytes, the bound is %d KiB", n, maxText>>10))
	}
	if n := len(context); n > maxContext {
		return Plan{}, refuse(ReasonPlannerInvalid, fmt.Sprintf("context: %d bytes, the bound is %d KiB", n, maxContext>>10))
	}
	if text == "" {
		// The model path's own line, so the same defect reads the same way on either path.
		return Plan{}, refuse(ReasonPlannerInvalid, "text: empty, the request is a sentence about what is wrong")
	}
	if len(candidates) != 1 {
		// The sentence names no repository, and this refusal must not add one: with two candidates
		// the composer would have to pick, and telling the person which one it nearly picked
		// reveals a repository they may not know exists.
		return Plan{}, refuse(ReasonRepositoryNotAllowed, "")
	}
	repository := strings.TrimSpace(candidates[0])
	if repository == "" {
		return Plan{}, refuse(ReasonRepositoryNotAllowed, "")
	}
	name, class, ok := firstClass(classes)
	if !ok {
		// An operator's config, not a person's mistake: the composer refuses rather than panicking,
		// because a degraded mode that takes the surface down is worse than no degraded mode.
		return Plan{}, refuse(ReasonPlannerInvalid, "classes: none in the config's table, and the composer files in the first class it defines")
	}

	body := templateBody(text, context)
	if n := len(body); n > maxBodyBytes {
		// The text and context bounds together are 20 KiB and the template's sections are more: a
		// request that fills both is refused with the body's bound rather than filed half-written.
		return Plan{}, refuse(ReasonPlanTooLarge, fmt.Sprintf("body: %d bytes, the bound is %d", n, maxBodyBytes))
	}
	// The validator stays the one judge of the draft: the composer builds the template, and
	// `CheckDraft` is what says whether it holds it, in its own words.
	checked, err := sanitize.CheckDraft(composerArea+": "+firstLine(text), body)
	if err != nil {
		return Plan{}, &Refusal{Reason: ReasonInvalidDraft, Sentence: err.Error()}
	}

	ceiling, source := sumCeilings([]Class{class})
	p := Plan{
		APIVersion:   APIVersion,
		Planner:      Planner{Model: composerModel, At: time.Now().UTC()},
		Repository:   repository,
		Alternatives: nil,
		Issues: []Issue{{
			Index: 1,
			Title: checked.Title,
			Body:  checked.Body,
			Class: name,
			Roles: slices.Clone(class.Roles),
		}},
		Budget: Budget{CeilingUSD: ceiling, Source: source},
		// Why this repository, in the composer's words: it did not choose one, so the evidence is
		// the candidate set's size and nothing this mode would have to have read to say.
		RepositoryEvidence: []Evidence{{Kind: "candidate", Text: composerEvidence}},
		Question:           nil,
	}
	p.Hash = Hash(p)
	return p, nil
}

// templateBody is the composer's issue body: the template's three headings in the template's order,
// the person's words kept whole under the first, their context beneath them as background, and the
// composer's defaults for the two sections a template cannot fill (CD19: one defect, one issue).
func templateBody(text, context string) string {
	var b strings.Builder
	b.WriteString("## What is wrong\n")
	b.WriteString(text)
	if context != "" {
		// Background, not an expectation: the composer does not read a context as "what it should
		// be", because nothing here infers.
		b.WriteString("\n\n")
		b.WriteString(composerContext)
		b.WriteString("\n")
		b.WriteString(context)
	}
	b.WriteString("\n\n## What it should be\n")
	b.WriteString(notStatedShould)
	b.WriteString("\n\n## Acceptance check\n")
	b.WriteString(notStatedCheck)
	return b.String()
}

// firstClass is the class the composer files in: the alphabetically first name the table defines, so
// the choice is a pick two processes agree on rather than an inference. ok is false for an empty
// table, which has no first class.
func firstClass(classes map[string]Class) (string, Class, bool) {
	names := make([]string, 0, len(classes))
	for name := range classes {
		names = append(names, name)
	}
	if len(names) == 0 {
		return "", Class{}, false
	}
	slices.Sort(names)
	return names[0], classes[names[0]], true
}

// firstLine is text's first line, trimmed: a title is one line, and the body already carries the
// whole request.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(line)
}
