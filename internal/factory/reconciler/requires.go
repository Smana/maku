// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/narrate"
)

// reasonRequiredStageMissing hands off a pull request when a stage required by its MergePolicy
// class could not run (e.g. tests without tests.paths or merge.contractCheck; WR6, E7).
const reasonRequiredStageMissing = "required_stage_missing"

func hasClassRequires(rc *config.Repository) bool {
	if rc == nil {
		return false
	}
	for _, cl := range rc.Classes {
		if len(cl.Requires) > 0 {
			return true
		}
	}
	return false
}

func appendUnique(dst []string, items ...string) []string {
	for _, item := range items {
		if item != "" && !slices.Contains(dst, item) {
			dst = append(dst, item)
		}
	}
	return dst
}

func matchesAnyRE(patterns []string, path string) bool {
	for _, pat := range patterns {
		re, err := regexp.Compile(pat)
		if err == nil && re.MatchString(path) {
			return true
		}
	}
	return false
}

// matchingRequireClasses returns sorted class names in rc that declare Requires and match at least
// one path in paths (by AllowedFiles/NarrowedFiles and not ProtectedFiles).
func matchingRequireClasses(rc *config.Repository, paths []string) []string {
	if rc == nil || len(paths) == 0 {
		return nil
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(rc.Classes)) {
		cl := rc.Classes[name]
		if len(cl.Requires) == 0 || cl.Adopted() || cl.Oracle != nil || len(cl.AllowedFiles) == 0 {
			continue
		}
		for _, p := range paths {
			if !matchesAnyRE(cl.AllowedFiles, p) {
				continue
			}
			if len(cl.NarrowedFiles) > 0 && !matchesAnyRE(cl.NarrowedFiles, p) {
				continue
			}
			if len(cl.ProtectedFiles) > 0 && matchesAnyRE(cl.ProtectedFiles, p) {
				continue
			}
			out = append(out, name)
			break
		}
	}
	return out
}

func ensureRequires(p *v1alpha1.ProfileStatus) *v1alpha1.ClassRequiresStatus {
	if p == nil {
		return nil
	}
	if p.Requires == nil {
		p.Requires = &v1alpha1.ClassRequiresStatus{}
	}
	return p.Requires
}

// applyClassRequires adds stages required by class to t's spec and profile at triage, never
// removing a stage the workflow already enables (WR6, E7).
func (r *Reconciler) applyClassRequires(ctx context.Context, cfg *config.Config, t *v1alpha1.Task,
	profile *v1alpha1.ProfileStatus, class string) error {
	rc := r.repo(cfg, t)
	if rc == nil || class == "" || cfg.Investigates(t.Spec.Template) {
		return nil
	}
	reqs := cfg.RequiredStagesFor(rc, class)
	if len(reqs) == 0 {
		return nil
	}
	if req := ensureRequires(profile); req != nil {
		req.By = class
		req.Stages = appendUnique(req.Stages, reqs...)
	}
	for _, req := range reqs {
		switch req {
		case "plan":
			t.Spec.ScopeMode = string(config.ScopeScoper)
			t.Spec.Outcome = config.OutcomePlanFirst
		case "review":
			if !slices.Contains(cfg.Templates[t.Spec.Template].Roles, "reviewer") {
				t.Spec.Template = "pair"
			}
		case "judge":
			if t.Spec.Loop == nil {
				t.Spec.Loop = &v1alpha1.Loop{
					StallSteps:    config.DefaultStallSteps,
					Continuations: config.DefaultContinuations,
					JudgeModel:    config.DefaultJudge,
				}
			} else if t.Spec.Loop.JudgeModel == "" {
				t.Spec.Loop.JudgeModel = config.DefaultJudge
			}
		case "tests":
			if t.Spec.DataClass != "public" {
				continue
			}
			var hasPaths bool
			if rc.Merge.ContractCheck != "" {
				paths, err := r.testPaths(ctx, t)
				if err != nil {
					return err
				}
				hasPaths = len(paths) > 0
			}
			if hasPaths {
				if profile != nil {
					profile.TestFirst = true
				}
			} else {
				if cr := ensureRequires(profile); cr != nil {
					cr.Unmet = appendUnique(cr.Unmet, "tests")
				}
				narrateLater(t, narrate.Event{
					Key: fmt.Sprintf("required-stage-unmet-%s-tests", class),
					Body: fmt.Sprintf("Maku task `%s`: class `%s` requires `tests`, but this repository has no `tests.paths` or `merge.contractCheck`; the merge gate will hand off to a maintainer (`%s`).",
						t.Name, class, reasonRequiredStageMissing),
				})
			}
		}
	}
	return nil
}

// applyLatePlanRequires checks class requirements against the paths the scoper's plan touches
// (Task E1/E7), adding any newly required stage before the implementer starts.
func (r *Reconciler) applyLatePlanRequires(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, touches []string) error {
	rc := r.repo(cfg, t)
	if !hasClassRequires(rc) || len(touches) == 0 || cfg.Investigates(t.Spec.Template) {
		return nil
	}
	classes := matchingRequireClasses(rc, touches)
	if len(classes) == 0 {
		return nil
	}
	specChanged := false
	type roomNote struct {
		key, msg string
	}
	var notes []roomNote
	for _, class := range classes {
		reqs := cfg.RequiredStagesFor(rc, class)
		var added []string
		for _, req := range reqs {
			switch req {
			case "review":
				if !slices.Contains(cfg.Templates[t.Spec.Template].Roles, "reviewer") {
					t.Spec.Template = "pair"
					specChanged = true
					added = append(added, "review")
				}
			case "judge":
				if t.Spec.Loop == nil {
					t.Spec.Loop = &v1alpha1.Loop{
						StallSteps:    config.DefaultStallSteps,
						Continuations: config.DefaultContinuations,
						JudgeModel:    config.DefaultJudge,
					}
					specChanged = true
					added = append(added, "judge")
				} else if t.Spec.Loop.JudgeModel == "" {
					t.Spec.Loop.JudgeModel = config.DefaultJudge
					specChanged = true
					added = append(added, "judge")
				}
			case "tests":
				if t.Spec.DataClass != "public" || ranTester(t) || t.Status.Contract != nil {
					continue
				}
				if t.Status.Profile != nil && t.Status.Profile.TestFirst {
					continue
				}
				var hasPaths bool
				if rc.Merge.ContractCheck != "" {
					paths, err := r.testPaths(ctx, t)
					if err != nil {
						return err
					}
					hasPaths = len(paths) > 0
				}
				if hasPaths {
					if t.Status.Profile != nil {
						t.Status.Profile.TestFirst = true
					}
					added = append(added, "tests")
				} else {
					if cr := ensureRequires(t.Status.Profile); cr != nil {
						cr.Unmet = appendUnique(cr.Unmet, "tests")
					}
					narrateLater(t, narrate.Event{
						Key: fmt.Sprintf("required-stage-unmet-%s-tests", class),
						Body: fmt.Sprintf("Maku task `%s`: class `%s` requires `tests`, but this repository has no `tests.paths` or `merge.contractCheck`; the merge gate will hand off to a maintainer (`%s`).",
							t.Name, class, reasonRequiredStageMissing),
					})
				}
			}
		}
		if len(added) > 0 {
			if cr := ensureRequires(t.Status.Profile); cr != nil {
				if cr.By == "" {
					cr.By = class
				}
				cr.Stages = appendUnique(cr.Stages, added...)
			}
			notes = append(notes, roomNote{
				key: "late-requires-plan-" + class,
				msg: fmt.Sprintf("Maku: class `%s` requires %s for paths in the plan; running %s.",
					class, strings.Join(added, ", "), strings.Join(added, ", ")),
			})
		}
	}
	if specChanged {
		status := t.Status
		if err := r.Client.Update(ctx, t); err != nil {
			return err
		}
		t.Status = status
	}
	for _, n := range notes {
		if err := narrate.Room(ctx, r.Rooms, t, n.key, n.msg,
			func(ctx context.Context) error { return r.Client.Status().Update(ctx, t) }); err != nil {
			r.log().Warn("late requires room note not posted", "task.id", t.Name, "err", err)
		}
	}
	return nil
}

// applyLateDiffRequires evaluates class requirements against the PR diff after the first push
// (Task E1/E7), running any newly required stage at most once and narrating why in the room.
func (r *Reconciler) applyLateDiffRequires(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (bool, error) {
	rc := r.repo(cfg, t)
	if !hasClassRequires(rc) || t.Status.PullRequest == nil || cfg.Investigates(t.Spec.Template) {
		return false, nil
	}
	if t.Status.Profile != nil && t.Status.Profile.Requires != nil && t.Status.Profile.Requires.DiffChecked {
		return false, nil
	}
	if r.Merger == nil {
		return false, nil
	}
	head := t.Status.PullRequest.HeadSHA
	if head == "" {
		pr, err := r.readPR(ctx, t)
		if err != nil {
			return false, err
		}
		head = pr.HeadSHA
	}
	if head == "" {
		return false, nil
	}
	diff, err := r.merger(ctx).Diff(ctx, "main", head)
	if err != nil {
		return false, err
	}
	if cr := ensureRequires(t.Status.Profile); cr != nil {
		cr.DiffChecked = true
	}
	var paths []string
	for _, f := range diff.Files {
		if f.Path != "" {
			paths = append(paths, f.Path)
		}
		if f.Previous != "" {
			paths = append(paths, f.Previous)
		}
	}
	classes := matchingRequireClasses(rc, paths)
	if len(classes) == 0 {
		return false, nil
	}
	specChanged := false
	requeue := false
	type roomNote struct {
		key, msg string
	}
	var notes []roomNote
	for _, class := range classes {
		reqs := cfg.RequiredStagesFor(rc, class)
		var added []string
		for _, req := range reqs {
			switch req {
			case "plan":
				if t.Status.Scope == nil && t.Spec.Scope == nil && t.Spec.ScopeMode != string(config.ScopeScoper) {
					t.Spec.ScopeMode = string(config.ScopeScoper)
					specChanged = true
					requeue = true
					added = append(added, "plan")
				}
			case "review":
				if !slices.Contains(cfg.Templates[t.Spec.Template].Roles, "reviewer") {
					t.Spec.Template = "pair"
					specChanged = true
					added = append(added, "review")
				}
			case "judge":
				if t.Spec.Loop == nil {
					t.Spec.Loop = &v1alpha1.Loop{
						StallSteps:    config.DefaultStallSteps,
						Continuations: config.DefaultContinuations,
						JudgeModel:    config.DefaultJudge,
					}
					specChanged = true
					added = append(added, "judge")
				} else if t.Spec.Loop.JudgeModel == "" {
					t.Spec.Loop.JudgeModel = config.DefaultJudge
					specChanged = true
					added = append(added, "judge")
				}
			case "tests":
				if t.Spec.DataClass != "public" || ranTester(t) || t.Status.Contract != nil {
					continue
				}
				var hasPaths bool
				if rc.Merge.ContractCheck != "" {
					tp, err := r.testPaths(ctx, t)
					if err != nil {
						return false, err
					}
					hasPaths = len(tp) > 0
				}
				if hasPaths {
					if t.Status.Profile != nil {
						t.Status.Profile.TestFirst = true
					}
					requeue = true
					added = append(added, "tests")
				} else {
					if cr := ensureRequires(t.Status.Profile); cr != nil {
						cr.Unmet = appendUnique(cr.Unmet, "tests")
					}
					narrateLater(t, narrate.Event{
						Key: fmt.Sprintf("required-stage-unmet-%s-tests", class),
						Body: fmt.Sprintf("Maku task `%s`: class `%s` requires `tests`, but this repository has no `tests.paths` or `merge.contractCheck`; the merge gate will hand off to a maintainer (`%s`).",
							t.Name, class, reasonRequiredStageMissing),
					})
				}
			}
		}
		if len(added) > 0 {
			if cr := ensureRequires(t.Status.Profile); cr != nil {
				if cr.By == "" {
					cr.By = class
				}
				cr.Stages = appendUnique(cr.Stages, added...)
			}
			notes = append(notes, roomNote{
				key: "late-requires-diff-" + class,
				msg: fmt.Sprintf("Maku: class `%s` requires %s for files changed in the diff; running %s.",
					class, strings.Join(added, ", "), strings.Join(added, ", ")),
			})
		}
	}
	if specChanged {
		status := t.Status
		if err := r.Client.Update(ctx, t); err != nil {
			return false, err
		}
		t.Status = status
	}
	for _, n := range notes {
		if err := narrate.Room(ctx, r.Rooms, t, n.key, n.msg,
			func(ctx context.Context) error { return r.Client.Status().Update(ctx, t) }); err != nil {
			r.log().Warn("late requires room note not posted", "task.id", t.Name, "err", err)
		}
	}
	if requeue {
		r.to(t, v1alpha1.PhaseQueued, "")
		return true, nil
	}
	return false, nil
}

// missingRequiredStage reports reasonRequiredStageMissing when a stage declared in cl.Requires (or
// recorded as unmet on t.Status.Profile.Requires.Unmet) did not run on the task (WR6, E7).
func missingRequiredStage(in ArmInputs, cfg *config.Config, cl config.Class) string {
	if in.Task == nil {
		return ""
	}
	if p := in.Task.Status.Profile; p != nil && p.Requires != nil && len(p.Requires.Unmet) > 0 {
		return reasonRequiredStageMissing
	}
	for _, req := range cl.Requires {
		switch req {
		case "plan":
			if in.Task.Status.Scope == nil && in.Task.Spec.Scope == nil {
				return reasonRequiredStageMissing
			}
		case "tests":
			if in.Task.Status.Contract == nil {
				return reasonRequiredStageMissing
			}
		case "review":
			if !slices.Contains(cfg.Templates[in.Task.Spec.Template].Roles, "reviewer") || in.Task.Status.Verdict != "approve" {
				return reasonRequiredStageMissing
			}
		case "judge":
			if in.Task.Spec.Loop == nil || in.Task.Spec.Loop.JudgeModel == "" {
				return reasonRequiredStageMissing
			}
		}
	}
	return ""
}
