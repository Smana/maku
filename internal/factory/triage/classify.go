// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

// ClassForge is the part of the forge triage reads: an issue's labels, and who applied them.
// (The forge package exports no interface; each consumer declares its own.)
type ClassForge interface {
	Issue(ctx context.Context, number int) (forge.Issue, error)
	LabelEvents(ctx context.Context, number int, label string) ([]forge.LabelEvent, error)
}

// Matrix is §2's table: the predicted class and the tier choose the team, never trio (WF2).
func Matrix(class, source, tier string) string {
	switch {
	case source == "review":
		return "review"
	case source == "runlore":
		return "investigate"
	case class == "docs-links" && tier != "frontier":
		return "solo"
	}
	return "pair" // trio left the shipped templates (WF2): a frontier task runs pair
}

// Investigates is whether t takes the investigate template, forced by its source and never by its
// body: a RunLore finding, or an api task of a declared internal principal (RunLore agent-to-agent
// RA4). A sponsored principal's task, and an api task of a principal the config no longer declares,
// are not forced.
func Investigates(cfg *config.Config, t *v1alpha1.Task) bool {
	switch t.Spec.Source.Kind {
	case "runlore":
		return true
	case "api":
		return cfg.InternalBy(t.Spec.Source.RequestedBy) != nil
	}
	return false
}

// Control puts percent % of tasks in the OD-14 control group, deterministically by task id.
func Control(taskID string, percent int) bool {
	h := fnv.New32a()
	_, _ = h.Write([]byte(taskID))
	return int64(h.Sum32()%100) < int64(percent) // widened, never narrowed (gosec G115)
}

// Cohort puts percent % of tasks in the evaluation's control cohort, hashed apart from Control so
// the two groups are independent.
func Cohort(taskID string, percent int) bool {
	return Control("evaluation/"+taskID, percent)
}

// Classify is the one triage per task (§2): deterministic rules plus one C7 call.
type Classify struct {
	C     Classifier
	Forge ClassForge
	// Clients is v2 D8: the factory App on a repository. Set, a task's labels are read on its
	// own repository, and one it has no connection for reads none; nil, Forge reads them.
	Clients func(repository string) (ClassForge, bool)
}

// forge is the connection that reads t's issue; nil when its repository has none.
func (c Classify) forge(t *v1alpha1.Task) ClassForge {
	if c.Clients == nil {
		return c.Forge
	}
	f, ok := c.Clients(t.Spec.Repository)
	if !ok {
		return nil
	}
	return f
}

// Triage fixes the class, then asks C7 for the tier: one call per task, whose failure falls
// back to static/standard rather than blocking the task (§2, C7).
func (c Classify) Triage(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (Decision, error) {
	dataClass := t.Spec.DataClass
	if dataClass == "" {
		dataClass = cfg.Defaults.DataClass
	}
	outcome, outcomeNote := ResolveOutcome(cfg, t)
	if t.Spec.Source.Kind == "review" {
		outcome, outcomeNote = config.OutcomeReview, ""
	}
	internalInvestigate := Investigates(cfg, t)
	if internalInvestigate {
		dataClass = "internal" // RunLore and internal API sources force internal (§2, RA4)
	}
	class, declared := c.predictClass(cfg, t)
	if outcome == config.OutcomeReport || outcome == config.OutcomeReview {
		class, declared = "review", false
	}
	pin, harnessPin, legacy, err := c.pin(ctx, cfg, t, outcome)
	if err != nil {
		return Decision{}, err
	}
	var notes []string
	if legacy {
		notes = append(notes, LegacyLabelNote)
	}
	if outcomeNote != "" {
		notes = append(notes, outcomeNote)
	}
	// Declared is no pin (P8): only an honoured workflow or harness pin is labeled.
	cohort := CohortOf(t.Name, pin.Honoured() || harnessPin.Honoured(), cfg.Evaluation.ControlPercent)
	if outcome == config.OutcomeReview {
		return Decision{Template: "review", PredictedClass: "review", DataClass: dataClass, Outcome: config.OutcomeReview,
			Budget:         BudgetFor(cfg, "standard"),
			Classification: v1alpha1.Classification{Tier: "standard", Classifier: "static", Fallback: "none"},
			HarnessPin:     harnessPin, Cohort: cohort, Notes: notes}, nil
	}
	if !internalInvestigate && outcome != config.OutcomeReport && oracle(cfg, t, class) {
		// MC6: the findings are the task and the oracle its verifier: one implementer at tier light,
		// no classifier call, so no control group either (OD-14 measures the classifier).
		return Decision{Template: "solo", PredictedClass: class, DataClass: dataClass, Outcome: outcome, Budget: BudgetFor(cfg, "light"),
			Classification: v1alpha1.Classification{Tier: "light", Classifier: "static", Fallback: "none"}, Declared: declared,
			Pinned: pin.Honoured(), Pin: pin, HarnessPin: harnessPin, Cohort: cohort, Notes: notes}, nil
	}
	res, err := C7Response{}, errors.New("no classifier") // no C7 service, no call (WF9)
	if c.C != nil {
		res, err = c.C.Classify(ctx, C7Request{Text: t.Spec.Text, Ref: t.Name, DataClass: dataClass})
	}
	if err != nil || (res.Tier != "light" && res.Tier != "standard" && res.Tier != "frontier") {
		res = C7Response{Tier: "standard", Classifier: "static", Fallback: "static"} // never blocks (C7)
	}
	cl := v1alpha1.Classification{Tier: res.Tier, Classifier: res.Classifier, Fallback: res.Fallback}
	if res.Confidence > 0 {
		cl.Confidence = fmt.Sprintf("%.2f", res.Confidence)
	}
	for _, s := range res.Shadow {
		cl.Shadow = append(cl.Shadow, v1alpha1.ShadowVerdict{Classifier: s.Classifier, Tier: s.Tier, Confidence: fmt.Sprintf("%.2f", s.Confidence)})
	}
	tier := res.Tier
	if Control(t.Name, cfg.Triage.ControlPercent) {
		cl.Control, tier = true, "frontier"
	}
	source := t.Spec.Source.Kind
	if internalInvestigate || outcome == config.OutcomeReport {
		source = "runlore" // Matrix keys on the source kind: investigate template ([triager])
	}
	return Decision{Template: Matrix(class, source, tier), PredictedClass: class, DataClass: dataClass, Outcome: outcome,
		Budget: BudgetFor(cfg, tier), Classification: cl, Declared: declared, Pinned: pin.Honoured(), Pin: pin, HarnessPin: harnessPin, Cohort: cohort, Notes: notes}, nil
}

// profileName is a harness profile's name as config and the Task's CRD spell it.
var profileName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// LegacyLabelNote is triage's note on a task whose issue carries a class: or a profile: label,
// which nothing reads since the workflow left the class (WF4, WF13).
const LegacyLabelNote = "the `class:` and `profile:` labels are no longer read; use `workflow:<name>`."

// pin is t's pinned workflow and harness as ResolvePin / ResolveHarnessPin judge them (P5, P6):
// workflow:<name> and harness:<name> labels on its issue with each one's last applier, and the
// intake's profile/harness with the sponsor login the factory resolved.
func (c Classify) pin(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, outcome string) (pin, harnessPin Pin, legacy bool, err error) {
	if Investigates(cfg, t) || t.Spec.Source.Kind == "schedule" || outcome == config.OutcomeReport || outcome == config.OutcomeReview {
		return Pin{}, Pin{}, false, nil
	}
	var wfLabels, harnessLabels []LabelPin
	if gh := c.forge(t); t.Spec.Issue != 0 && gh != nil {
		iss, err := gh.Issue(ctx, t.Spec.Issue)
		if err != nil {
			return Pin{}, Pin{}, false, err
		}
		for _, l := range iss.Labels {
			if strings.HasPrefix(l, "class:") || strings.HasPrefix(l, "profile:") {
				legacy = true
				continue
			}
			if name, ok := strings.CutPrefix(l, "workflow:"); ok && profileName.MatchString(name) {
				evs, err := gh.LabelEvents(ctx, t.Spec.Issue, l)
				if err != nil {
					return Pin{}, Pin{}, false, err
				}
				actor := ""
				if len(evs) > 0 {
					actor = evs[len(evs)-1].Actor
				}
				wfLabels = append(wfLabels, LabelPin{Name: name, Actor: actor})
				continue
			}
			if name, ok := strings.CutPrefix(l, "harness:"); ok && profileName.MatchString(name) {
				evs, err := gh.LabelEvents(ctx, t.Spec.Issue, l)
				if err != nil {
					return Pin{}, Pin{}, false, err
				}
				actor := ""
				if len(evs) > 0 {
					actor = evs[len(evs)-1].Actor
				}
				harnessLabels = append(harnessLabels, LabelPin{Name: name, Actor: actor})
			}
		}
	}
	var field, harnessField, login string
	if in := t.Spec.Intake; in != nil {
		field, harnessField, login = in.Profile, in.Harness, in.Login
	}
	return ResolvePin(cfg, t.Spec.Repository, wfLabels, field, login),
		ResolveHarnessPin(cfg, t.Spec.Repository, harnessLabels, harnessField, login),
		legacy, nil
}

// oracle reports whether class is an oracle class of t's repository; none when it is not served.
func oracle(cfg *config.Config, t *v1alpha1.Task, class string) bool {
	cl, _ := cfg.RepositoryFor(t.Spec.Repository).Class(class)
	return cl.Oracle != nil
}

// predictClass: the schedule's class; else a maintainer's intake class (P7); else the class an
// origin task was inferred under; else review. RunLore tasks are always review. A class: label is
// read by nothing since the workflow left the class (WF4): a class names an oracle policy on a
// schedule, and the gate reads the diff. declared is a class the schedule or a maintainer named,
// never one inferred.
func (c Classify) predictClass(cfg *config.Config, t *v1alpha1.Task) (class string, declared bool) {
	if Investigates(cfg, t) {
		return "review", false
	}
	if t.Spec.Source.Kind == "schedule" {
		if _, ok := cfg.RepositoryFor(t.Spec.Repository).Class(t.Spec.PredictedClass); ok {
			return t.Spec.PredictedClass, true
		}
		return "review", false
	}
	if class, ok := intakeClass(cfg, t); ok {
		return class, true
	}
	return c.inferred(cfg, t), false
}

// intakeClass is the class a planned task's intake carried (P7), when the sponsor login the factory
// resolved still maintains the repository and the repository declares the class; revert is never
// one.
func intakeClass(cfg *config.Config, t *v1alpha1.Task) (string, bool) {
	in := t.Spec.Intake
	if in == nil || in.Class == "" || in.Class == "revert" {
		return "", false
	}
	rc := cfg.RepositoryFor(t.Spec.Repository)
	if _, ok := rc.Class(in.Class); !ok || !rc.IsMaintainer(in.Login) {
		return "", false
	}
	return in.Class, true
}

// inferred: K5, the factory's own record, never a label it applied. An origin task keeps the class
// inferred at publish when its repository declares it and it is not revert; else review.
func (c Classify) inferred(cfg *config.Config, t *v1alpha1.Task) string {
	if t.Spec.Origin == "" || t.Spec.PredictedClass == "revert" {
		return "review"
	}
	if _, ok := cfg.RepositoryFor(t.Spec.Repository).Class(t.Spec.PredictedClass); ok {
		return t.Spec.PredictedClass
	}
	return "review"
}
