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

// Matrix is §2's table: the predicted class and the tier choose the team.
func Matrix(class, source, tier string) string {
	switch {
	case source == "runlore":
		return "investigate"
	case class == "docs-links" && tier == "frontier":
		return "pair"
	case class == "docs-links":
		return "solo"
	case tier == "frontier":
		return "trio"
	}
	return "pair"
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
	class, declared, err := c.predictClass(ctx, cfg, t)
	if err != nil {
		return Decision{}, err
	}
	if Investigates(cfg, t) {
		dataClass = "internal" // the investigate template forces it (§2)
	}
	pin, err := c.pin(ctx, cfg, t, class, dataClass == "public")
	if err != nil {
		return Decision{}, err
	}
	// Declared is no pin (P8): only an honoured profile pin is labeled.
	cohort := CohortOf(t.Name, pin.Honoured(), cfg.Evaluation.ControlPercent)
	if oracle(cfg, t, class) {
		// MC6: the findings are the task and the oracle its verifier: one implementer at tier light,
		// no classifier call, so no control group either (OD-14 measures the classifier).
		return Decision{Template: "solo", PredictedClass: class, DataClass: dataClass, Budget: BudgetFor(cfg, "light"),
			Classification: v1alpha1.Classification{Tier: "light", Classifier: "static", Fallback: "none"}, Declared: declared,
			Pinned: pin.Honoured(), Pin: pin, Cohort: cohort}, nil
	}
	res, err := C7Response{}, errors.New("no classifier") // unset URL: no C7 service, no call
	if cfg.Triage.ClassifierURL != "" && c.C != nil {
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
	if Investigates(cfg, t) {
		source = "runlore" // Matrix keys on the source kind: both take investigate
	}
	return Decision{Template: Matrix(class, source, tier), PredictedClass: class, DataClass: dataClass,
		Budget: BudgetFor(cfg, tier), Classification: cl, Declared: declared, Pinned: pin.Honoured(), Pin: pin, Cohort: cohort}, nil
}

// profileName is a harness profile's name as config and the Task's CRD spell it.
var profileName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// pin is t's pinned profile as ResolvePin judges it (P5, P6): the profile:<name> labels on its issue
// with each one's last applier, and the intake's profile with the sponsor login the factory resolved.
// A RunLore or internal task, and a scheduled one, has none: no person pinned it.
func (c Classify) pin(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, class string, public bool) (Pin, error) {
	if Investigates(cfg, t) || t.Spec.Source.Kind == "schedule" {
		return Pin{}, nil
	}
	var labels []LabelPin
	if gh := c.forge(t); t.Spec.Issue != 0 && gh != nil {
		iss, err := gh.Issue(ctx, t.Spec.Issue)
		if err != nil {
			return Pin{}, err
		}
		for _, l := range iss.Labels {
			name, ok := strings.CutPrefix(l, "profile:")
			if !ok || !profileName.MatchString(name) {
				continue // a name no profile can have pins nothing, and is never echoed back
			}
			evs, err := gh.LabelEvents(ctx, t.Spec.Issue, l)
			if err != nil {
				return Pin{}, err
			}
			actor := ""
			if len(evs) > 0 {
				actor = evs[len(evs)-1].Actor
			}
			labels = append(labels, LabelPin{Name: name, Actor: actor})
		}
	}
	var field, login string
	if in := t.Spec.Intake; in != nil {
		field, login = in.Profile, in.Login
	}
	return ResolvePin(cfg, t.Spec.Repository, class, labels, field, login, public), nil
}

// oracle reports whether class is an oracle class of t's repository; none when it is not served.
func oracle(cfg *config.Config, t *v1alpha1.Task, class string) bool {
	cl, _ := cfg.RepositoryFor(t.Spec.Repository).Class(class)
	return cl.Oracle != nil
}

// predictClass: the schedule's class; else a maintainer-applied class:<name> label; else a
// maintainer's intake class (P7); else review.
// RunLore tasks are always review. Labels are intent, never authority (§2, §8 T3).
// declared is a class the schedule or a maintainer named, never one inferred.
func (c Classify) predictClass(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (class string, declared bool, err error) {
	if Investigates(cfg, t) {
		return "review", false, nil
	}
	if t.Spec.Source.Kind == "schedule" {
		if _, ok := cfg.RepositoryFor(t.Spec.Repository).Class(t.Spec.PredictedClass); ok {
			return t.Spec.PredictedClass, true, nil
		}
		return "review", false, nil
	}
	gh := c.forge(t)
	if t.Spec.Issue == 0 || gh == nil {
		if class, ok := intakeClass(cfg, t); ok {
			return class, true, nil
		}
		return c.inferred(cfg, t), false, nil
	}
	iss, err := gh.Issue(ctx, t.Spec.Issue)
	if err != nil {
		return "", false, err
	}
	for _, l := range iss.Labels {
		name, ok := strings.CutPrefix(l, "class:")
		if _, known := cfg.RepositoryFor(t.Spec.Repository).Class(name); !ok || !known || name == "revert" {
			continue // revert is the factory's own class, never predicted from a label
		}
		evs, err := gh.LabelEvents(ctx, t.Spec.Issue, l)
		if err != nil {
			return "", false, err
		}
		if len(evs) > 0 && cfg.RepositoryFor(t.Spec.Repository).IsMaintainer(evs[len(evs)-1].Actor) {
			return name, true, nil
		}
	}
	if class, ok := intakeClass(cfg, t); ok {
		return class, true, nil
	}
	return c.inferred(cfg, t), false, nil
}

// intakeClass is the class a planned task's intake carried (P7), when the sponsor login the factory
// resolved still maintains the repository and the repository declares the class; revert is never
// one. A maintainer's class: label comes first: a label is the later, explicit choice.
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
