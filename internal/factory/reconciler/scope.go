// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/steering"
	"github.com/Smana/maku/internal/factory/upkeep"
)

// scopeAtTriage fixes the task's scope mode and steering note (scoper design SC2, SC3): the
// mode profileSpec wrote, and in any other mode the class's note on the
// default branch of a repository this factory owns (v2 D7: a fence it is not named in is not
// read, and says so). An absent note is no steering; an invalid one is not used, counted and logged, and the
// task proceeds (SC4), as does one that is never readable (a directory, a symlink). A retryable read error is returned: the triage is retried.
func (r *Reconciler) scopeAtTriage(ctx context.Context, t *v1alpha1.Task) error {
	mode := config.ScopeMode(t.Spec.ScopeMode) // profileSpec's: the row's, off for an investigate task
	t.Spec.Steering = nil
	path := steering.Path(t.Spec.PredictedClass)
	if mode == config.ScopeOff || path == "" {
		return nil
	}
	class := t.Spec.PredictedClass
	if r.Owned == nil || !r.Owned(t.Spec.Repository) {
		// Not fenced to this factory, or its fence cache went stale (an outage): no read and no
		// retry, the task proceeds unsteered, visibly.
		r.log().Warn("steering note not read: repository not owned", "task.id", t.Name, "repository", t.Spec.Repository, "class", class)
		record(ctx, func(ctx context.Context) { r.Metrics.SteeringRead(ctx, class, "unowned") })
		return nil
	}
	text, sha, found, err := r.gh(ctx).DefaultBranchFile(ctx, path)
	if err != nil && !errors.Is(err, forge.ErrNotAFile) {
		return err
	}
	read := "used"
	switch verr := steering.Validate(text); {
	case errors.Is(err, forge.ErrNotAFile): // never readable: retrying cannot help (SC4)
		read = "invalid"
		r.log().Warn("steering note not used", "task.id", t.Name, "path", path, "sha", sha, "err", err)
	case !found:
		read = "absent"
	case verr != nil:
		read = "invalid"
		r.log().Warn("steering note not used", "task.id", t.Name, "path", path, "sha", sha, "err", verr)
	default:
		t.Spec.Steering = &v1alpha1.Steering{Path: path, SHA: sha, Text: text}
	}
	record(ctx, func(ctx context.Context) { r.Metrics.SteeringRead(ctx, class, read) })
	return nil
}

// hydrateRoutinePrompt reads and validates the task's repo-owned routine prompt and optional bound
// Agent Skill from the default branch when pinned on t.Spec.Routine. Fails closed on missing/invalid
// files or unowned repositories.
func (r *Reconciler) hydrateRoutinePrompt(ctx context.Context, t *v1alpha1.Task) (RoutinePrompt, error) {
	if t == nil || t.Spec.Routine == nil {
		return RoutinePrompt{}, nil
	}
	rt := t.Spec.Routine
	needPrompt := rt.PromptSource == "repo"
	needSkill := rt.Skill != ""
	if !needPrompt && !needSkill {
		return RoutinePrompt{}, nil
	}
	if r.Owned == nil || !r.Owned(t.Spec.Repository) {
		return RoutinePrompt{}, fmt.Errorf("%w: repository %s not owned", upkeep.ErrPromptInvalid, t.Spec.Repository)
	}
	var out RoutinePrompt
	if needPrompt {
		p := cmp.Or(rt.PromptPath, upkeep.PromptPath(rt.Name))
		if p == "" {
			return RoutinePrompt{}, fmt.Errorf("%w: invalid routine name %q", upkeep.ErrPromptInvalid, rt.Name)
		}
		text, sha, found, err := r.gh(ctx).DefaultBranchFile(ctx, p)
		if errors.Is(err, forge.ErrNotAFile) {
			return RoutinePrompt{}, fmt.Errorf("%w: %w", upkeep.ErrPromptInvalid, err)
		}
		if err != nil {
			return RoutinePrompt{}, err
		}
		if !found {
			return RoutinePrompt{}, fmt.Errorf("%w: %s missing on default branch", upkeep.ErrPromptInvalid, p)
		}
		if err := upkeep.ValidatePrompt(text); err != nil {
			return RoutinePrompt{}, err
		}
		out.PromptPath = p
		out.PromptSHA = cmp.Or(sha, rt.PromptSHA)
		out.PromptText = text
	}
	if needSkill {
		skPath := upkeep.SkillPath(rt.Skill)
		if skPath == "" {
			return RoutinePrompt{}, fmt.Errorf("%w: invalid skill name %q", upkeep.ErrSkillInvalid, rt.Skill)
		}
		skText, skSHA, found, err := r.gh(ctx).DefaultBranchFile(ctx, skPath)
		if errors.Is(err, forge.ErrNotAFile) {
			return RoutinePrompt{}, fmt.Errorf("%w: %w", upkeep.ErrSkillInvalid, err)
		}
		if err != nil {
			return RoutinePrompt{}, err
		}
		if !found {
			return RoutinePrompt{}, fmt.Errorf("%w: %s", upkeep.ErrSkillMissing, skPath)
		}
		if err := upkeep.ValidateSkill(skText); err != nil {
			return RoutinePrompt{}, err
		}
		out.Skill = rt.Skill
		out.SkillSHA = cmp.Or(skSHA, rt.SkillSHA)
		out.SkillText = skText
	}
	return out, nil
}

// hydrateRoutineHistory builds bounded cross-run rejection memory for a routine task before its
// first run. List or GitHub read errors log a warning and degrade to partial or empty history so
// they never block a run.
func (r *Reconciler) hydrateRoutineHistory(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) []upkeep.HistoryItem {
	if t == nil || t.Spec.Source.Kind != v1alpha1.SourceKindRoutine {
		return nil
	}
	routine := cmp.Or(routineName(t), t.Spec.Source.Ref)
	if routine == "" {
		return nil
	}
	var list v1alpha1.TaskList
	var tasks []v1alpha1.Task
	if r.Client != nil {
		if err := r.Client.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
			r.log().Warn("routine history task list failed; continuing without task history", "task", t.Name, "routine", routine, "err", err)
		} else {
			tasks = list.Items
		}
	}
	var isMaintainer func(string) bool
	if rc := r.repo(cfg, t); rc != nil {
		isMaintainer = rc.IsMaintainer
	}
	items, err := upkeep.BuildHistory(ctx, tasks, upkeep.HistorySource{
		Forge:        r.gh(ctx),
		Merger:       r.merger(ctx),
		IsMaintainer: isMaintainer,
		Log:          r.log(),
	}, t.Spec.Repository, routine, r.Now())
	if err != nil {
		r.log().Warn("routine history build failed; continuing without history", "task", t.Name, "routine", routine, "err", err)
		return nil
	}
	return items
}
