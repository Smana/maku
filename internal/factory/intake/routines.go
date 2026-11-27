// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/taskid"
	"github.com/Smana/maku/internal/factory/upkeep"
)

// Bounded skip reasons recorded by the routine scheduler in StateStore and RoutineSkipMetrics.
const (
	SkipKillSwitch   = "kill_switch"
	SkipNotOwned     = "not_owned"
	SkipDisabled     = "disabled"
	SkipPausedManual = "paused_manual"
	SkipOpenCap      = "open_cap"
	SkipCooldown     = "cooldown"
	SkipUpkeepBudget = "upkeep_budget"
	SkipProbeEmpty   = "probe_empty"
	SkipProbeRefused = "probe_refused"
)

// Typed refusal errors returned by Scheduler.TriggerManual for API status mapping.
var (
	ErrKillSwitch            = errors.New("intake: kill switch active")
	ErrRepoNotFound          = errors.New("intake: repository not found")
	ErrRoutineNotFound       = errors.New("intake: routine not found")
	ErrRepoNotOwned          = errors.New("intake: repository not owned by this factory")
	ErrRoutineDisabled       = errors.New("intake: routine is disabled")
	ErrModePhase2            = errors.New("intake: mode requires Phase 2 (mode_requires_phase2)")
	ErrInvalidMode           = errors.New("intake: invalid routine mode")
	ErrModeWidening          = errors.New("intake: mode cannot widen configured routine mode")
	ErrInvalidIdempotencyKey = errors.New("intake: invalid idempotency key")
	ErrIdempotencyConflict   = errors.New("intake: idempotency key already used for a different routine request")
	ErrOpenCap               = errors.New("intake: routine open PR cap reached")
	ErrBudgetExhausted       = errors.New("intake: daily budget exhausted")
	ErrProbeEmpty            = errors.New("intake: routine probe found no work")
	ErrProbeRefused          = errors.New("intake: routine probe findings refused")
)

const findingRejectMemoryWindow = 30 * 24 * time.Hour

type routineCacheEntry struct {
	pairKey string
	refused bool
}

type skipMetricEntry struct {
	slotKey string
	reason  string
}

type routinePausedMetrics interface {
	RoutinePaused(ctx context.Context, repository, routine string, paused bool)
}

type routineStateLister interface {
	List(ctx context.Context) (map[string]map[string]upkeep.RoutineState, error)
}

func (s *Scheduler) syncRoutinePaused(ctx context.Context, cfg *config.Config) {
	if cfg == nil || s.RoutineMetrics == nil {
		return
	}
	pm, ok := s.RoutineMetrics.(routinePausedMetrics)
	if !ok {
		return
	}
	repos := cfg.Repos()
	hasAny := false
	for i := range repos {
		if len(candidateRoutineNames(&repos[i])) > 0 {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return
	}

	var states map[string]map[string]upkeep.RoutineState
	useList := false
	if s.State != nil {
		if lister, ok := s.State.(routineStateLister); ok {
			var err error
			states, err = lister.List(ctx)
			if err != nil {
				s.log().Warn("routine paused sync list failed", "err", err)
				return
			}
			useList = true
		}
	}

	for i := range repos {
		rc := &repos[i]
		names := candidateRoutineNames(rc)
		if len(names) == 0 {
			continue
		}
		var repoStates map[string]upkeep.RoutineState
		if useList {
			repoStates = states[strings.ToLower(rc.Name)]
		}
		for _, name := range names {
			declPaused := rc.ResolvedRoutines[name].Paused
			if b, ok := findBinding(rc.Routines, name); ok && b.Paused {
				declPaused = true
			}
			statePaused := false
			if s.State != nil {
				if useList {
					statePaused = repoStates[name].Paused
				} else {
					st, err := s.State.Get(ctx, rc.Name, name)
					if err != nil {
						s.log().Warn("routine paused sync state read failed", "repository", rc.Name, "routine", name, "err", err)
						continue
					}
					statePaused = st.Paused
				}
			}
			pm.RoutinePaused(ctx, rc.Name, name, declPaused || statePaused)
		}
	}
}

type routineProbeResult struct {
	found          bool
	text           string
	findings       []v1alpha1.Finding
	predictedClass string
	findingIDs     []string
	pendingKey     string
}

func (s *Scheduler) nonce() string {
	if s.Nonce != nil {
		return s.Nonce()
	}
	return taskid.Random()
}

func (s *Scheduler) tickRoutines(ctx context.Context, cfg *config.Config, rc *config.Repository, stopped, owned bool) []error {
	names := candidateRoutineNames(rc)
	if len(names) == 0 {
		return nil
	}
	var l v1alpha1.TaskList
	if err := s.Client.List(ctx, &l, client.InNamespace(s.Namespace)); err != nil {
		s.log().Warn("routine task list failed", "repository", rc.Name, "err", err)
		return []error{err}
	}
	tasks := l.Items
	var errs []error
	for _, name := range names {
		b, hasBinding := findBinding(rc.Routines, name)
		rr, hasResolved := rc.ResolvedRoutines[name]
		isDisabled := hasBinding && b.Mode == config.RoutineModeDisabled
		if !hasResolved && !isDisabled {
			continue
		}
		cronExpr := rr.Cron
		if !hasResolved {
			cronExpr = b.Cron
		}
		now := s.Now().UTC()
		slot, ok := LastSlot(cronExpr, now)
		if !ok || now.Sub(slot) > time.Hour {
			continue
		}
		key := taskid.RoutineKey(rc.Name, name, slot)
		taskName := taskid.Name(key)
		slotKey := slot.UTC().Format(time.RFC3339)

		// Do not emit skip diagnostics for a slot whose Task already exists.
		if slices.ContainsFunc(tasks, func(t v1alpha1.Task) bool { return t.Name == taskName }) {
			continue
		}

		if stopped {
			s.recordRoutineSkip(ctx, rc.Name, name, slotKey, SkipKillSwitch, now)
			continue
		}
		if !owned {
			s.recordRoutineSkip(ctx, rc.Name, name, slotKey, SkipNotOwned, now)
			continue
		}
		if isDisabled || !hasResolved {
			s.recordRoutineSkip(ctx, rc.Name, name, slotKey, SkipDisabled, now)
			continue
		}

		var statePaused bool
		if s.State != nil {
			st, err := s.State.Get(ctx, rc.Name, name)
			if err != nil {
				s.log().Warn("routine state read failed", "repository", rc.Name, "routine", name, "err", err)
				errs = append(errs, fmt.Errorf("%s/%s state: %w", rc.Name, name, err))
				continue
			}
			statePaused = st.Paused
		}
		if rr.Paused || statePaused {
			s.recordRoutineSkip(ctx, rc.Name, name, slotKey, SkipPausedManual, now)
			continue
		}

		if countOpenRoutineTasks(tasks, rc.Name, name) >= max(1, rr.MaxOpenPRs) {
			s.recordRoutineSkip(ctx, rc.Name, name, slotKey, SkipOpenCap, now)
			continue
		}

		if rr.Cooldown > 0 && inRoutineCooldown(tasks, rc.Name, name, rr.Cooldown, now) {
			s.recordRoutineSkip(ctx, rc.Name, name, slotKey, SkipCooldown, now)
			continue
		}

		capMicro := cfg.Budgets.DailyMicroUSD(config.PrincipalUpkeep)
		if capMicro <= 0 {
			s.recordRoutineSkip(ctx, rc.Name, name, slotKey, SkipUpkeepBudget, now)
			continue
		}
		if s.Ledger != nil {
			spent, reserved, err := ledger.DayUsage(ctx, s.Ledger, s.Namespace, now, config.PrincipalUpkeep)
			if err != nil {
				s.log().Warn("routine ledger read failed", "repository", rc.Name, "routine", name, "err", err)
				errs = append(errs, fmt.Errorf("%s/%s ledger: %w", rc.Name, name, err))
				continue
			}
			if spent+reserved >= capMicro {
				s.recordRoutineSkip(ctx, rc.Name, name, slotKey, SkipUpkeepBudget, now)
				continue
			}
		}

		res, err := s.runRoutineProbe(ctx, cfg, rc, &rr, tasks, now, true)
		if isFindingsRefused(err) {
			s.log().Warn("routine probe findings refused", "repository", rc.Name, "routine", name, "err", err)
			s.recordRoutineSkip(ctx, rc.Name, name, slotKey, SkipProbeRefused, now)
			continue
		}
		if err != nil {
			s.log().Warn("routine probe failed", "repository", rc.Name, "routine", name, "err", err)
			errs = append(errs, fmt.Errorf("%s/%s probe: %w", rc.Name, name, err))
			continue
		}
		if !res.found {
			s.recordRoutineSkip(ctx, rc.Name, name, slotKey, SkipProbeEmpty, now)
			continue
		}

		tk := s.buildRoutineTask(cfg, rc, &rr, rr.Mode, taskName, key, "system:scheduler", res)
		if err := s.Client.Create(ctx, tk); err != nil && !apierrors.IsAlreadyExists(err) {
			errs = append(errs, err)
			continue
		}
		tasks = append(tasks, *tk)
		s.settleRoutine(res.pendingKey)
	}
	return errs
}

func (s *Scheduler) recordRoutineSkip(ctx context.Context, repo, routine, slotKey, reason string, at time.Time) {
	if s.State != nil {
		if err := s.State.RecordSkip(ctx, repo, routine, slotKey, reason, at); err != nil {
			s.log().Warn("routine skip persist failed", "repository", repo, "routine", routine, "reason", reason, "err", err)
		}
	}
	if s.RoutineMetrics != nil {
		dedupKey := repo + "\x00" + routine
		entry := skipMetricEntry{slotKey: slotKey, reason: reason}
		s.skipMetricMu.Lock()
		if s.skipMetricSeen == nil {
			s.skipMetricSeen = map[string]skipMetricEntry{}
		}
		already := s.skipMetricSeen[dedupKey] == entry
		if !already {
			s.skipMetricSeen[dedupKey] = entry
		}
		s.skipMetricMu.Unlock()
		if !already {
			s.RoutineMetrics.RoutineSkip(ctx, repo, routine, reason)
		}
	}
}

func candidateRoutineNames(rc *config.Repository) []string {
	set := make(map[string]struct{}, len(rc.ResolvedRoutines)+len(rc.Routines))
	for name := range rc.ResolvedRoutines {
		set[name] = struct{}{}
	}
	for _, b := range rc.Routines {
		if b.Mode == config.RoutineModeDisabled {
			set[b.Name] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func findBinding(bindings []config.RoutineBinding, name string) (config.RoutineBinding, bool) {
	for _, b := range bindings {
		if b.Name == name {
			return b, true
		}
	}
	return config.RoutineBinding{}, false
}

func countOpenRoutineTasks(tasks []v1alpha1.Task, repo, routine string) int {
	n := 0
	for i := range tasks {
		t := &tasks[i]
		if strings.EqualFold(t.Spec.Repository, repo) &&
			t.Spec.Source.Kind == v1alpha1.SourceKindRoutine &&
			t.Spec.Routine != nil && t.Spec.Routine.Name == routine &&
			!v1alpha1.TerminalPhase(t.Status.Phase) {
			n++
		}
	}
	return n
}

func taskEndedTime(t *v1alpha1.Task) *time.Time {
	if t.Status.PhaseSince != nil {
		tm := t.Status.PhaseSince.Time
		return &tm
	}
	if t.Status.EndedAt != nil {
		tm := t.Status.EndedAt.Time
		return &tm
	}
	return nil
}

func inRoutineCooldown(tasks []v1alpha1.Task, repo, routine string, cooldown time.Duration, now time.Time) bool {
	for i := range tasks {
		t := &tasks[i]
		if !strings.EqualFold(t.Spec.Repository, repo) ||
			t.Spec.Source.Kind != v1alpha1.SourceKindRoutine ||
			t.Spec.Routine == nil || t.Spec.Routine.Name != routine {
			continue
		}
		if t.Status.Phase != v1alpha1.PhaseClosed && t.Status.Phase != v1alpha1.PhaseReverted {
			continue
		}
		if ts := taskEndedTime(t); ts != nil && now.Sub(*ts) < cooldown {
			return true
		}
	}
	return false
}

func collectSkipFindingIDs(tasks []v1alpha1.Task, repo, routine string, window time.Duration, now time.Time) map[string]bool {
	var skip map[string]bool
	for i := range tasks {
		t := &tasks[i]
		if !strings.EqualFold(t.Spec.Repository, repo) ||
			t.Spec.Source.Kind != v1alpha1.SourceKindRoutine ||
			t.Spec.Routine == nil || t.Spec.Routine.Name != routine ||
			len(t.Spec.Routine.FindingIDs) == 0 {
			continue
		}
		include := !v1alpha1.TerminalPhase(t.Status.Phase)
		if !include && (t.Status.Phase == v1alpha1.PhaseClosed || t.Status.Phase == v1alpha1.PhaseReverted) {
			if ts := taskEndedTime(t); ts != nil && now.Sub(*ts) < window {
				include = true
			}
		}
		if !include {
			continue
		}
		if skip == nil {
			skip = make(map[string]bool, len(t.Spec.Routine.FindingIDs))
		}
		for _, id := range t.Spec.Routine.FindingIDs {
			skip[id] = true
		}
	}
	return skip
}

func (s *Scheduler) runRoutineProbe(ctx context.Context, cfg *config.Config, rc *config.Repository, rr *config.ResolvedRoutine, tasks []v1alpha1.Task, now time.Time, useCache bool) (routineProbeResult, error) {
	switch cmp.Or(rr.Probe.Kind, config.ProbeNone) {
	case config.ProbeNone:
		return routineProbeResult{found: true, text: rr.DefaultPrompt}, nil

	case config.ProbeRenovateRed:
		gh, merger, err := s.conns(rc)
		if err != nil {
			return routineProbeResult{}, err
		}
		if gh == nil || merger == nil {
			return routineProbeResult{}, nil
		}
		found, detail, err := s.renovateRedFindings(ctx, gh, merger, cmp.Or(rr.ProbeAuthor, "renovate[bot]"))
		if errors.Is(err, forge.ErrNoMerger) {
			return routineProbeResult{}, nil
		}
		if err != nil || !found {
			return routineProbeResult{}, err
		}
		return routineProbeResult{found: true, text: rr.DefaultPrompt + "\n\n" + detail}, nil

	case config.ProbeDeadLinks:
		cl, ok := rc.Class(rr.OracleClass)
		if !ok || (!cl.Live && !cl.Shadow) || cl.Oracle == nil {
			return routineProbeResult{}, nil
		}
		_, merger, err := s.conns(rc)
		if err != nil {
			return routineProbeResult{}, err
		}
		if merger == nil {
			return routineProbeResult{}, nil
		}
		two, err := detectorRuns(ctx, merger, rc, cl)
		if errors.Is(err, forge.ErrNoMerger) {
			return routineProbeResult{}, nil
		}
		if err != nil || two == nil {
			return routineProbeResult{}, err
		}
		cacheKey := "routine:" + rc.Name + ":" + rr.Name
		pairKey := runsCacheKey(two)
		if useCache {
			if hit, refused := s.lookupRoutineCache(cacheKey, pairKey); hit {
				if refused {
					return routineProbeResult{}, ErrFindingsRefused
				}
				return routineProbeResult{}, nil
			}
		}
		fs, err := evaluateRuns(ctx, merger, two, cl, s.log().With("routine", rr.Name))
		if errors.Is(err, ErrFindingsRefused) {
			if useCache {
				s.rememberRoutine(cacheKey, pairKey, true)
			}
			return routineProbeResult{}, err
		}
		if err != nil {
			return routineProbeResult{}, err
		}
		if len(fs) == 0 {
			if useCache {
				s.rememberRoutine(cacheKey, pairKey, false)
			}
			return routineProbeResult{}, nil
		}
		if rr.Probe.MaxFindings > 0 && len(fs) > rr.Probe.MaxFindings {
			fs = fs[:rr.Probe.MaxFindings]
		}
		budget := 0
		if limit := cfg.Caps.MaxTextBytes; limit > 0 {
			if budget = limit - len(rr.DefaultPrompt) - 2; budget <= 0 {
				if useCache {
					s.rememberRoutine(cacheKey, pairKey, false)
				}
				s.log().Info("dead-links routine text cap leaves no room", "routine", rr.Name)
				return routineProbeResult{}, nil
			}
		}
		detail, spec, left := deadLinksTask(fs, budget)
		if left > 0 {
			s.log().Info("dead-links routine text cap", "routine", rr.Name, "left_out", left)
		}
		if len(spec) == 0 {
			if useCache {
				s.rememberRoutine(cacheKey, pairKey, false)
			}
			return routineProbeResult{}, nil
		}
		if useCache {
			s.markRoutinePending(cacheKey, pairKey)
		}
		return routineProbeResult{
			found:          true,
			text:           rr.DefaultPrompt + "\n\n" + detail,
			findings:       spec,
			predictedClass: rr.OracleClass,
			pendingKey:     cacheKey,
		}, nil

	case config.ProbeActionsFindings:
		_, merger, err := s.conns(rc)
		if err != nil {
			return routineProbeResult{}, err
		}
		if merger == nil {
			return routineProbeResult{}, nil
		}
		req := max(1, rr.Probe.RequireConsecutive)
		qualifying, err := upkeep.QualifyingRuns(ctx, merger, rr.ProbeWorkflow, rc, req)
		if errors.Is(err, forge.ErrNoMerger) {
			return routineProbeResult{}, nil
		}
		if err != nil || len(qualifying) < req {
			return routineProbeResult{}, err
		}
		cacheKey := "routine:" + rc.Name + ":" + rr.Name
		pairKey := runsCacheKey(qualifying)
		if useCache {
			if hit, refused := s.lookupRoutineCache(cacheKey, pairKey); hit {
				if refused {
					return routineProbeResult{}, upkeep.ErrFindingsRefused
				}
				return routineProbeResult{}, nil
			}
		}
		perRun, err := upkeep.ReadFindingsRuns(ctx, merger, qualifying, rr.ProbeArtifact, rr.ProbeFile)
		if errors.Is(err, upkeep.ErrFindingsRefused) {
			if useCache {
				s.rememberRoutine(cacheKey, pairKey, true)
			}
			return routineProbeResult{}, err
		}
		if err != nil {
			return routineProbeResult{}, err
		}
		agreed := upkeep.AgreedFindings(perRun)
		skipIDs := collectSkipFindingIDs(tasks, rc.Name, rr.Name, max(rr.Cooldown, findingRejectMemoryWindow), now)
		filtered, _ := upkeep.FilterFindings(agreed, rr.ProtectedPaths, skipIDs, rr.Probe.MaxFindings)
		if len(filtered) == 0 {
			if useCache {
				s.rememberRoutine(cacheKey, pairKey, false)
			}
			return routineProbeResult{}, nil
		}
		text, ids, left := upkeep.RenderFindingsText(rr.DefaultPrompt, filtered, s.nonce(), cfg.Caps.MaxTextBytes)
		if left > 0 {
			s.log().Info("actions-findings text cap", "routine", rr.Name, "left_out", left)
		}
		if len(ids) == 0 {
			if useCache {
				s.rememberRoutine(cacheKey, pairKey, false)
			}
			return routineProbeResult{}, nil
		}
		if useCache {
			s.markRoutinePending(cacheKey, pairKey)
		}
		return routineProbeResult{
			found:      true,
			text:       text,
			findingIDs: ids,
			pendingKey: cacheKey,
		}, nil

	default:
		return routineProbeResult{}, fmt.Errorf("intake: unknown routine probe %q", rr.Probe.Kind)
	}
}

func runsCacheKey(runs []forge.WorkflowRun) string {
	var b strings.Builder
	for i, r := range runs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(r.ID, 10))
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(r.RunAttempt))
	}
	return b.String()
}

func (s *Scheduler) lookupRoutineCache(key, pairKey string) (hit, refused bool) {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	e, ok := s.routineSeen[key]
	if !ok || e.pairKey != pairKey {
		return false, false
	}
	return true, e.refused
}

func (s *Scheduler) rememberRoutine(key, pairKey string, refused bool) {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if s.routineSeen == nil {
		s.routineSeen = map[string]routineCacheEntry{}
	}
	s.routineSeen[key] = routineCacheEntry{pairKey: pairKey, refused: refused}
}

func (s *Scheduler) markRoutinePending(key, pairKey string) {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if s.routinePending == nil {
		s.routinePending = map[string]string{}
	}
	s.routinePending[key] = pairKey
}

func (s *Scheduler) settleRoutine(key string) {
	if key == "" {
		return
	}
	s.seenMu.Lock()
	pairKey, ok := s.routinePending[key]
	delete(s.routinePending, key)
	s.seenMu.Unlock()
	if ok {
		s.rememberRoutine(key, pairKey, false)
	}
}

func isFindingsRefused(err error) bool {
	return errors.Is(err, upkeep.ErrFindingsRefused) || errors.Is(err, ErrFindingsRefused)
}

func (s *Scheduler) buildRoutineTask(cfg *config.Config, rc *config.Repository, rr *config.ResolvedRoutine, mode, taskName, key, requestedBy string, res routineProbeResult) *v1alpha1.Task {
	dataClass := cmp.Or(rr.DataClass, cfg.Defaults.DataClass)
	textSum := sha256.Sum256([]byte(res.text))
	promptSum := sha256.Sum256([]byte(rr.DefaultPrompt))
	genFromCfg, _, _ := cfg.RoutineGenerations(rc.Name, rr.Name)
	routineGen := cmp.Or(rr.RoutineGeneration, genFromCfg)
	maxMicro := config.MicroUSD(cmp.Or(rr.MaxCostUSD, config.DefaultRoutineMaxCostUSD))

	return &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskName,
			Namespace: s.Namespace,
			Labels: map[string]string{
				v1alpha1.LabelRoutine: rr.Name,
			},
		},
		Spec: v1alpha1.TaskSpec{
			Repository: rc.Name,
			Source: v1alpha1.Source{
				Kind:          v1alpha1.SourceKindRoutine,
				Ref:           rr.Name,
				Key:           key,
				RequestedBy:   requestedBy,
				Trust:         "trusted",
				ContentSHA256: hex.EncodeToString(textSum[:]),
			},
			Text:           res.text,
			DataClass:      dataClass,
			PredictedClass: res.predictedClass,
			Findings:       res.findings,
			MaxMicroUSD:    maxMicro,
			Routine: &v1alpha1.TaskRoutine{
				Name:              rr.Name,
				Mode:              mode,
				RoutineGeneration: routineGen,
				PromptSource:      "routine",
				PromptSHA:         hex.EncodeToString(promptSum[:]),
				FindingIDs:        res.findingIDs,
			},
		},
	}
}

func routineModeRank(mode string) (int, bool) {
	switch mode {
	case config.RoutineModePropose:
		return 1, true
	case config.RoutineModeDraft:
		return 2, true
	case config.RoutineModePR:
		return 3, true
	default:
		return 0, false
	}
}

func validIdempotencyKey(repo, name, idempotencyKey string) bool {
	if idempotencyKey == "" || !utf8.ValidString(idempotencyKey) {
		return false
	}
	for _, r := range idempotencyKey {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return len(taskid.ManualRoutineKey(repo, name, idempotencyKey)) <= 512
}

// TriggerManual creates (or idempotently returns) a manually triggered routine Task for (repo, name).
// It enforces kill switch, ownership, disabled status, Phase 1 mode constraints, open_cap, and Q15
// budget pre-checks while bypassing cooldown and paused_manual.
func (s *Scheduler) TriggerManual(ctx context.Context, repo, name, modeOverride, login, idempotencyKey string) (*v1alpha1.Task, error) {
	if !validIdempotencyKey(repo, name, idempotencyKey) {
		return nil, ErrInvalidIdempotencyKey
	}
	if strings.TrimSpace(login) == "" {
		return nil, errors.New("intake: login is required")
	}
	if s.Stopped != nil && s.Stopped(ctx) {
		return nil, ErrKillSwitch
	}
	cfg := s.Cfg.Current()
	if repo == "" {
		return nil, ErrRepoNotFound
	}
	rc := cfg.RepositoryFor(repo)
	if rc == nil {
		return nil, ErrRepoNotFound
	}
	if s.Owned == nil || !s.Owned(rc.Name) {
		return nil, ErrRepoNotOwned
	}
	if b, ok := findBinding(rc.Routines, name); ok && b.Mode == config.RoutineModeDisabled {
		return nil, ErrRoutineDisabled
	}
	rr, ok := rc.ResolvedRoutines[name]
	if !ok {
		return nil, ErrRoutineNotFound
	}

	effectiveMode := rr.Mode
	if modeOverride != "" {
		rOver, ok := routineModeRank(modeOverride)
		if !ok {
			return nil, ErrInvalidMode
		}
		if rBase, ok := routineModeRank(rr.Mode); ok && rOver > rBase {
			return nil, ErrModeWidening
		}
		effectiveMode = modeOverride
	}
	if effectiveMode == config.RoutineModePropose || effectiveMode == config.RoutineModeDraft {
		return nil, ErrModePhase2
	}
	if effectiveMode != config.RoutineModePR {
		return nil, ErrInvalidMode
	}

	key := taskid.ManualRoutineKey(rc.Name, rr.Name, idempotencyKey)
	if len(key) > 512 {
		return nil, ErrInvalidIdempotencyKey
	}
	taskName := taskid.Name(key)
	reader := client.Reader(s.Client)
	if s.APIReader != nil {
		reader = s.APIReader
	}

	var existing v1alpha1.Task
	if err := reader.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: taskName}, &existing); err == nil {
		if manualTaskMatches(&existing, rc.Name, rr.Name, effectiveMode, key) {
			return &existing, nil
		}
		return nil, ErrIdempotencyConflict
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("intake: get manual routine task %s: %w", taskName, err)
	}

	var l v1alpha1.TaskList
	if err := s.Client.List(ctx, &l, client.InNamespace(s.Namespace)); err != nil {
		return nil, err
	}
	if countOpenRoutineTasks(l.Items, rc.Name, rr.Name) >= max(1, rr.MaxOpenPRs) {
		return nil, ErrOpenCap
	}

	now := s.Now().UTC()
	payer := config.PrincipalUpkeep
	if cfg.Budgets.PerPerson.Enabled {
		payer = v1alpha1.Person(login)
	}
	capMicro := cfg.Budgets.DailyMicroUSD(payer)
	if payer == config.PrincipalUpkeep || cfg.Budgets.EnforcePrincipal {
		if capMicro <= 0 {
			return nil, ErrBudgetExhausted
		}
		if s.Ledger != nil {
			spent, reserved, err := ledger.DayUsage(ctx, s.Ledger, s.Namespace, now, payer)
			if err != nil {
				return nil, fmt.Errorf("intake: ledger check: %w", err)
			}
			if spent+reserved >= capMicro {
				return nil, ErrBudgetExhausted
			}
		}
	}

	res, err := s.runRoutineProbe(ctx, cfg, rc, &rr, l.Items, now, false)
	if isFindingsRefused(err) {
		return nil, fmt.Errorf("%w: %w", ErrProbeRefused, err)
	}
	if err != nil {
		return nil, err
	}
	if !res.found {
		return nil, ErrProbeEmpty
	}

	tk := s.buildRoutineTask(cfg, rc, &rr, effectiveMode, taskName, key, v1alpha1.Person(login), res)
	if err := s.Client.Create(ctx, tk); apierrors.IsAlreadyExists(err) {
		var raced v1alpha1.Task
		if rerr := reader.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: taskName}, &raced); rerr != nil {
			return nil, fmt.Errorf("intake: task %s already exists but could not be read: %w", taskName, rerr)
		}
		if manualTaskMatches(&raced, rc.Name, rr.Name, effectiveMode, key) {
			return &raced, nil
		}
		return nil, ErrIdempotencyConflict
	} else if err != nil {
		return nil, err
	}
	return tk, nil
}

func manualTaskMatches(t *v1alpha1.Task, repo, routine, mode, key string) bool {
	return strings.EqualFold(t.Spec.Repository, repo) &&
		t.Spec.Source.Kind == v1alpha1.SourceKindRoutine &&
		t.Spec.Source.Key == key &&
		t.Spec.Routine != nil &&
		t.Spec.Routine.Name == routine &&
		t.Spec.Routine.Mode == mode
}
