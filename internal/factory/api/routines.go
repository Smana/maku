// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/upkeep"
)

// RoutineStateStore reads and updates operational pause flags and skip diagnostics for routines.
type RoutineStateStore interface {
	Get(ctx context.Context, repo, routine string) (upkeep.RoutineState, error)
	List(ctx context.Context) (map[string]map[string]upkeep.RoutineState, error)
	SetPaused(ctx context.Context, repo, routine string, paused bool, reason, by string) error
}

// RoutineTrigger triggers a manual routine run without importing internal/factory/intake.
type RoutineTrigger interface {
	TriggerManual(ctx context.Context, repo, name, modeOverride, login, idempotencyKey string) (*factoryv1.Task, error)
}

// Typed sentinel errors mapped from intake.Scheduler.TriggerManual in internal/app/factory.go.
var (
	ErrRoutineKillSwitch          = errors.New("api: routine kill switch active")
	ErrRoutineRepoNotFound        = errors.New("api: routine repository not found")
	ErrRoutineNotFound            = errors.New("api: routine not found")
	ErrRoutineRepoNotOwned        = errors.New("api: routine repository not owned by this factory")
	ErrRoutineDisabled            = errors.New("api: routine is disabled")
	ErrRoutineInvalidMode         = errors.New("api: invalid routine mode")
	ErrRoutineModeWidening        = errors.New("api: routine mode cannot widen configured mode")
	ErrRoutineBadIdempotencyKey   = errors.New("api: invalid routine idempotency key")
	ErrRoutineIdempotencyConflict = errors.New("api: routine idempotency key conflict")
	ErrRoutineOpenCap             = errors.New("api: routine open PR cap reached")
	ErrRoutineBudgetExhausted     = errors.New("api: routine daily budget exhausted")
	ErrRoutineProbeEmpty          = errors.New("api: routine probe found no work")
	ErrRoutineProbeRefused        = errors.New("api: routine probe findings refused")
	ErrRoutinePromptInvalid       = errors.New("api: routine prompt invalid")
	ErrRoutineSkillMissing        = errors.New("api: routine skill missing")
	ErrRoutineSkillInvalid        = errors.New("api: routine skill invalid")
)

const (
	routineSparklineDays = 14
	routineHistoryLimit  = 50
	maxRoutineActionBody = 4 << 10
	maxPauseReasonRunes  = 200
)

// RoutineSparklineDay holds one UTC day's terminal/escalated routine outcome counts.
type RoutineSparklineDay struct {
	Date      string `json:"date"`
	Merged    int    `json:"merged"`
	Closed    int    `json:"closed"`
	NoOp      int    `json:"noOp"`
	Escalated int    `json:"escalated"`
}

// RoutineOverview summarizes one configured routine binding for GET /v1/routines.
type RoutineOverview struct {
	Repository        string                `json:"repository"`
	Routine           string                `json:"routine"`
	Description       string                `json:"description,omitempty"`
	Mode              string                `json:"mode"`
	Workflow          string                `json:"workflow,omitempty"`
	ProbeKind         string                `json:"probeKind,omitempty"`
	Cron              string                `json:"cron,omitempty"`
	Paused            bool                  `json:"paused"`
	DeclarativePaused bool                  `json:"declarativePaused,omitempty"`
	PausedReason      string                `json:"pausedReason,omitempty"`
	PausedBy          string                `json:"pausedBy,omitempty"`
	PausedAt          *time.Time            `json:"pausedAt,omitempty"`
	MaxOpenPRs        int                   `json:"maxOpenPRs"`
	MaxCostUSD        float64               `json:"maxCostUSD"`
	OpenCount         int                   `json:"openCount"`
	NextRunAt         *time.Time            `json:"nextRunAt"`
	Sparkline         []RoutineSparklineDay `json:"sparkline"`
	RollingMerged     int                   `json:"rollingMerged"`
	RollingClosed     int                   `json:"rollingClosed"`
	RollingNoOp       int                   `json:"rollingNoOp"`
	MergeRate         *float64              `json:"mergeRate"`
	USDPerMerged      *float64              `json:"usdPerMerged"`
	PromptEditURL     string                `json:"promptEditURL"`
	PromptSHA         string                `json:"promptSHA,omitempty"`
	Skill             string                `json:"skill,omitempty"`
	BreakerState      string                `json:"breakerState"`
	CanWrite          bool                  `json:"canWrite"`
}

// RoutineTaskRun summarizes one recent Task created for a routine.
type RoutineTaskRun struct {
	Name         string     `json:"name"`
	CreatedAt    time.Time  `json:"createdAt"`
	EndedAt      *time.Time `json:"endedAt,omitempty"`
	Phase        string     `json:"phase"`
	Reason       string     `json:"reason,omitempty"`
	RoomRef      string     `json:"roomRef,omitempty"`
	PRURL        string     `json:"prURL,omitempty"`
	PRNumber     int        `json:"prNumber,omitempty"`
	PromptSHA    string     `json:"promptSHA,omitempty"`
	PromptSource string     `json:"promptSource,omitempty"`
	RequestedBy  string     `json:"requestedBy,omitempty"`
	CostUSD      float64    `json:"costUSD"`
	Outcome      string     `json:"outcome,omitempty"`
}

// RoutineDetail is the GET /v1/routines/{owner}/{name}/{routine} response.
type RoutineDetail struct {
	RoutineOverview
	DefaultPrompt string              `json:"defaultPrompt,omitempty"`
	Runs          []RoutineTaskRun    `json:"runs"`
	Skips         []upkeep.SkipRecord `json:"skips"`
}

func (s *Server) nowTime() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Server) routineTaskReader() client.Reader {
	if s.Tasks != nil {
		return s.Tasks
	}
	return s.taskReader()
}

func (s *Server) routineReader(w http.ResponseWriter, r *http.Request) (authn.Principal, bool) {
	p, ok := s.policyReader(w, r)
	if !ok {
		return p, false
	}
	if (s.Groups.Member != "" || s.Groups.Admin != "") && !s.Groups.Admitted(p) {
		refuse(w, http.StatusForbidden, "not_permitted")
		return p, false
	}
	return p, true
}

func (s *Server) callerCanWrite(ctx context.Context, p authn.Principal, rc *config.Repository) bool {
	if s.Logins == nil {
		return false
	}
	login, err := s.Logins.Login(ctx, p.Sub, rc.Name)
	if err != nil || login == "" || !rc.IsMaintainer(login) {
		return false
	}
	can, err := s.actLoginWrites(ctx, rc.Name, login)
	return err == nil && can
}

func (s *Server) listRoutines(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, ok := s.routineReader(w, r)
	if !ok {
		return
	}
	var repos []config.Repository
	if asked := cmp.Or(r.URL.Query().Get("repository"), r.URL.Query().Get("repo")); asked != "" {
		repo := s.readable(cfg, w, r, p, asked)
		if repo == "" {
			return
		}
		if rc := cfg.RepositoryFor(repo); rc != nil {
			repos = []config.Repository{*rc}
		}
	} else {
		for _, rc := range cfg.Repos() {
			if can, err := s.reads(r.Context(), p, rc.Name); err == nil && can {
				repos = append(repos, rc)
			}
		}
	}

	out := []RoutineOverview{}
	hasAny := false
	for i := range repos {
		if len(routineEntryNames(&repos[i])) > 0 {
			hasAny = true
			break
		}
	}
	if !hasAny {
		w.Header().Set("Cache-Control", "no-store")
		reply(w, http.StatusOK, map[string][]RoutineOverview{"routines": out})
		return
	}

	reader := s.routineTaskReader()
	if reader == nil {
		refuse(w, http.StatusServiceUnavailable, "tasks_unavailable")
		return
	}
	var taskList factoryv1.TaskList
	if err := reader.List(r.Context(), &taskList, client.InNamespace(s.Namespace)); err != nil {
		refuse(w, http.StatusServiceUnavailable, "tasks_unavailable")
		return
	}

	var states map[string]map[string]upkeep.RoutineState
	if s.RoutineState != nil {
		var err error
		states, err = s.RoutineState.List(r.Context())
		if err != nil {
			s.log().WarnContext(r.Context(), "routine state list failed", "error", err)
			refuse(w, http.StatusServiceUnavailable, "routine_state_unavailable")
			return
		}
	}

	now := s.nowTime()
	for i := range repos {
		rc := &repos[i]
		names := routineEntryNames(rc)
		if len(names) == 0 {
			continue
		}
		canWrite := s.callerCanWrite(r.Context(), p, rc)
		repoStates := states[strings.ToLower(rc.Name)]
		for _, name := range names {
			rr, _, ok := findRoutineEntry(rc, name)
			if !ok {
				continue
			}
			tasks := routineTasksFor(taskList.Items, rc.Name, name)
			ov, _ := buildRoutineOverview(rc.Name, rr, repoStates[name], tasks, canWrite, now)
			out = append(out, ov)
		}
	}

	w.Header().Set("Cache-Control", "no-store")
	reply(w, http.StatusOK, map[string][]RoutineOverview{"routines": out})
}

func (s *Server) getRoutine(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, ok := s.routineReader(w, r)
	if !ok {
		return
	}
	asked := r.PathValue("owner") + "/" + r.PathValue("name")
	repo := s.readable(cfg, w, r, p, asked)
	if repo == "" {
		return
	}
	rc := cfg.RepositoryFor(repo)
	if rc == nil {
		refuse(w, http.StatusNotFound, "not_found")
		return
	}
	name := r.PathValue("routine")
	rr, _, ok := findRoutineEntry(rc, name)
	if !ok {
		refuse(w, http.StatusNotFound, "not_found")
		return
	}

	reader := s.routineTaskReader()
	if reader == nil {
		refuse(w, http.StatusServiceUnavailable, "tasks_unavailable")
		return
	}
	var taskList factoryv1.TaskList
	if err := reader.List(r.Context(), &taskList, client.InNamespace(s.Namespace)); err != nil {
		refuse(w, http.StatusServiceUnavailable, "tasks_unavailable")
		return
	}

	var st upkeep.RoutineState
	if s.RoutineState != nil {
		var err error
		st, err = s.RoutineState.Get(r.Context(), repo, name)
		if err != nil {
			s.log().WarnContext(r.Context(), "routine state get failed", "repository", repo, "routine", name, "error", err)
			refuse(w, http.StatusServiceUnavailable, "routine_state_unavailable")
			return
		}
	}

	now := s.nowTime()
	canWrite := s.callerCanWrite(r.Context(), p, rc)
	tasks := routineTasksFor(taskList.Items, repo, name)
	ov, sortedTasks := buildRoutineOverview(repo, rr, st, tasks, canWrite, now)

	runs := make([]RoutineTaskRun, 0, min(len(sortedTasks), routineHistoryLimit))
	for _, t := range sortedTasks {
		if len(runs) >= routineHistoryLimit {
			break
		}
		var endedAt *time.Time
		if t.Status.EndedAt != nil {
			tm := t.Status.EndedAt.UTC()
			endedAt = &tm
		}
		var prURL string
		var prNumber int
		if t.Status.PullRequest != nil {
			prURL = t.Status.PullRequest.URL
			prNumber = t.Status.PullRequest.Number
		}
		var promptSHA, promptSource string
		if t.Spec.Routine != nil {
			promptSHA = t.Spec.Routine.PromptSHA
			promptSource = t.Spec.Routine.PromptSource
		}
		runs = append(runs, RoutineTaskRun{
			Name:         t.Name,
			CreatedAt:    t.CreationTimestamp.UTC(),
			EndedAt:      endedAt,
			Phase:        t.Status.Phase,
			Reason:       t.Status.Reason,
			RoomRef:      t.Status.RoomRef,
			PRURL:        prURL,
			PRNumber:     prNumber,
			PromptSHA:    promptSHA,
			PromptSource: promptSource,
			RequestedBy:  t.Spec.Source.RequestedBy,
			CostUSD:      taskCostUSD(t),
			Outcome:      routineTaskOutcome(t),
		})
	}

	skips := make([]upkeep.SkipRecord, len(st.Skips))
	for i := range st.Skips {
		skips[i] = st.Skips[len(st.Skips)-1-i]
	}

	w.Header().Set("Cache-Control", "no-store")
	reply(w, http.StatusOK, RoutineDetail{
		RoutineOverview: ov,
		DefaultPrompt:   rr.DefaultPrompt,
		Runs:            runs,
		Skips:           skips,
	})
}

func (s *Server) routineWriterGate(w http.ResponseWriter, r *http.Request) (authn.Principal, *config.Repository, string, config.ResolvedRoutine, bool) {
	cfg := s.Cfg.Current()
	p, ok := s.routineReader(w, r)
	if !ok {
		return authn.Principal{}, nil, "", config.ResolvedRoutine{}, false
	}
	asked := r.PathValue("owner") + "/" + r.PathValue("name")
	repo := s.readable(cfg, w, r, p, asked)
	if repo == "" {
		return authn.Principal{}, nil, "", config.ResolvedRoutine{}, false
	}
	rc := cfg.RepositoryFor(repo)
	if rc == nil {
		refuse(w, http.StatusNotFound, "not_found")
		return authn.Principal{}, nil, "", config.ResolvedRoutine{}, false
	}
	name := r.PathValue("routine")
	rr, _, ok := findRoutineEntry(rc, name)
	if !ok {
		refuse(w, http.StatusNotFound, "not_found")
		return authn.Principal{}, nil, "", config.ResolvedRoutine{}, false
	}
	login, code, reason := s.linkedLogin(r.Context(), p, repo)
	if reason != "" {
		refuse(w, code, reason)
		return authn.Principal{}, nil, "", config.ResolvedRoutine{}, false
	}
	if !rc.IsMaintainer(login) {
		refuse(w, http.StatusForbidden, "not_a_maintainer")
		return authn.Principal{}, nil, "", config.ResolvedRoutine{}, false
	}
	canWrite, err := s.actLoginWrites(r.Context(), repo, login)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "access_unverified")
		return authn.Principal{}, nil, "", config.ResolvedRoutine{}, false
	}
	if !canWrite {
		refuse(w, http.StatusForbidden, "repository_permission")
		return authn.Principal{}, nil, "", config.ResolvedRoutine{}, false
	}
	return p, rc, login, rr, true
}

func decodeOptionalRoutineJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		return true
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRoutineActionBody))
	if err != nil {
		refuse(w, http.StatusBadRequest, "bad_json")
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		refuse(w, http.StatusBadRequest, "bad_json")
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		refuse(w, http.StatusBadRequest, "bad_json")
		return false
	}
	return true
}

func (s *Server) runRoutine(w http.ResponseWriter, r *http.Request) {
	_, rc, login, rr, ok := s.routineWriterGate(w, r)
	if !ok {
		return
	}
	if s.privateRefused(rc.Name) {
		refuse(w, http.StatusForbidden, "private_code_unacknowledged")
		return
	}
	var in struct {
		Mode           string `json:"mode"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if !decodeOptionalRoutineJSON(w, r, &in) {
		return
	}
	hdrKey := r.Header.Get("Idempotency-Key")
	if hdrKey != "" && in.IdempotencyKey != "" && hdrKey != in.IdempotencyKey {
		refuse(w, http.StatusBadRequest, "bad_idempotency_key")
		return
	}
	key := cmp.Or(hdrKey, in.IdempotencyKey)
	if s.RoutineTrigger == nil {
		refuse(w, http.StatusServiceUnavailable, "routines_unavailable")
		return
	}
	tk, err := s.RoutineTrigger.TriggerManual(r.Context(), rc.Name, rr.Name, in.Mode, login, key)
	if err != nil {
		s.refuseRoutineTrigger(w, r, rc.Name, rr.Name, err)
		return
	}
	mode := cmp.Or(in.Mode, rr.Mode)
	if tk.Spec.Routine != nil && tk.Spec.Routine.Mode != "" {
		mode = tk.Spec.Routine.Mode
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, http.StatusCreated, map[string]string{
		"task":        tk.Name,
		"room":        cmp.Or(tk.Status.RoomRef, tk.Name),
		"repository":  tk.Spec.Repository,
		"routine":     rr.Name,
		"mode":        mode,
		"requestedBy": tk.Spec.Source.RequestedBy,
	})
}

func (s *Server) refuseRoutineTrigger(w http.ResponseWriter, r *http.Request, repo, routine string, err error) {
	switch {
	case errors.Is(err, ErrRoutineModeWidening):
		refuse(w, http.StatusBadRequest, "mode_widening")
	case errors.Is(err, ErrRoutineInvalidMode):
		refuse(w, http.StatusBadRequest, "invalid_mode")
	case errors.Is(err, ErrRoutineBadIdempotencyKey):
		refuse(w, http.StatusBadRequest, "bad_idempotency_key")
	case errors.Is(err, ErrRoutineIdempotencyConflict):
		refuse(w, http.StatusConflict, "idempotency_conflict")
	case errors.Is(err, ErrRoutineKillSwitch):
		refuse(w, http.StatusServiceUnavailable, "kill_switch")
	case errors.Is(err, ErrRoutineRepoNotFound), errors.Is(err, ErrRoutineNotFound):
		refuse(w, http.StatusNotFound, "not_found")
	case errors.Is(err, ErrRoutineRepoNotOwned):
		refuse(w, http.StatusConflict, "repository_not_owned")
	case errors.Is(err, ErrRoutineDisabled):
		refuse(w, http.StatusConflict, "routine_disabled")
	case errors.Is(err, ErrRoutineOpenCap):
		refuse(w, http.StatusConflict, "open_cap")
	case errors.Is(err, ErrRoutineBudgetExhausted):
		refuse(w, http.StatusTooManyRequests, "budget_exhausted")
	case errors.Is(err, ErrRoutineProbeEmpty):
		refuse(w, http.StatusConflict, "probe_empty")
	case errors.Is(err, ErrRoutineProbeRefused):
		refuse(w, http.StatusUnprocessableEntity, "probe_refused")
	case errors.Is(err, ErrRoutinePromptInvalid):
		refuse(w, http.StatusUnprocessableEntity, "prompt_invalid")
	case errors.Is(err, ErrRoutineSkillMissing):
		refuse(w, http.StatusUnprocessableEntity, "skill_missing")
	case errors.Is(err, ErrRoutineSkillInvalid):
		refuse(w, http.StatusUnprocessableEntity, "skill_invalid")
	default:
		s.log().ErrorContext(r.Context(), "routine manual trigger failed", "repository", repo, "routine", routine, "error", err)
		refuse(w, http.StatusServiceUnavailable, "routines_unavailable")
	}
}

func (s *Server) pauseRoutine(w http.ResponseWriter, r *http.Request) {
	_, rc, login, rr, ok := s.routineWriterGate(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeOptionalRoutineJSON(w, r, &in) {
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "manual"
	}
	if !validPauseReason(reason) {
		refuse(w, http.StatusBadRequest, "bad_reason")
		return
	}
	if s.RoutineState == nil {
		refuse(w, http.StatusServiceUnavailable, "routines_unavailable")
		return
	}
	by := "github:" + strings.ToLower(login)
	if err := s.RoutineState.SetPaused(r.Context(), rc.Name, rr.Name, true, reason, by); err != nil {
		s.log().ErrorContext(r.Context(), "routine pause failed", "repository", rc.Name, "routine", rr.Name, "error", err)
		refuse(w, http.StatusServiceUnavailable, "routine_state_unavailable")
		return
	}
	if s.Metrics != nil {
		s.Metrics.RoutinePaused(r.Context(), rc.Name, rr.Name, true)
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, http.StatusOK, map[string]any{
		"repository":        rc.Name,
		"routine":           rr.Name,
		"paused":            true,
		"declarativePaused": rr.Paused,
		"pausedReason":      reason,
		"pausedBy":          by,
	})
}

func (s *Server) resumeRoutine(w http.ResponseWriter, r *http.Request) {
	_, rc, _, rr, ok := s.routineWriterGate(w, r)
	if !ok {
		return
	}
	var in struct{}
	if !decodeOptionalRoutineJSON(w, r, &in) {
		return
	}
	if s.RoutineState == nil {
		refuse(w, http.StatusServiceUnavailable, "routines_unavailable")
		return
	}
	if err := s.RoutineState.SetPaused(r.Context(), rc.Name, rr.Name, false, "", ""); err != nil {
		s.log().ErrorContext(r.Context(), "routine resume failed", "repository", rc.Name, "routine", rr.Name, "error", err)
		refuse(w, http.StatusServiceUnavailable, "routine_state_unavailable")
		return
	}
	if s.Metrics != nil {
		s.Metrics.RoutinePaused(r.Context(), rc.Name, rr.Name, rr.Paused)
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, http.StatusOK, map[string]any{
		"repository":        rc.Name,
		"routine":           rr.Name,
		"paused":            rr.Paused,
		"declarativePaused": rr.Paused,
	})
}

func validPauseReason(reason string) bool {
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > maxPauseReasonRunes {
		return false
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func routineEntryNames(rc *config.Repository) []string {
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
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func findRoutineEntry(rc *config.Repository, name string) (config.ResolvedRoutine, bool, bool) {
	if rr, ok := rc.ResolvedRoutines[name]; ok {
		return rr, false, true
	}
	for _, b := range rc.Routines {
		if b.Name == name && b.Mode == config.RoutineModeDisabled {
			return config.ResolvedRoutine{
				Name:       b.Name,
				Mode:       b.Mode,
				Paused:     b.Paused,
				Cron:       b.Cron,
				Workflow:   b.Workflow,
				DataClass:  b.DataClass,
				MaxOpenPRs: cmp.Or(b.MaxOpenPRs, 1),
				MaxCostUSD: cmp.Or(b.MaxCostUSD, config.DefaultRoutineMaxCostUSD),
				Breaker:    config.ResolvedBreaker{Window: config.DefaultBreakerWindow},
			}, true, true
		}
	}
	return config.ResolvedRoutine{}, false, false
}

func routineTasksFor(all []factoryv1.Task, repo, routine string) []*factoryv1.Task {
	var out []*factoryv1.Task
	for i := range all {
		t := &all[i]
		if strings.EqualFold(t.Spec.Repository, repo) &&
			t.Spec.Source.Kind == factoryv1.SourceKindRoutine &&
			t.Spec.Routine != nil &&
			t.Spec.Routine.Name == routine {
			out = append(out, t)
		}
	}
	return out
}

func buildRoutineOverview(repo string, rr config.ResolvedRoutine, st upkeep.RoutineState, tasks []*factoryv1.Task, canWrite bool, now time.Time) (RoutineOverview, []*factoryv1.Task) {
	now = now.UTC()
	today := now.Truncate(24 * time.Hour)
	startDay := today.AddDate(0, 0, -(routineSparklineDays - 1))

	sparkline := make([]RoutineSparklineDay, routineSparklineDays)
	for i := range routineSparklineDays {
		sparkline[i].Date = startDay.AddDate(0, 0, i).Format(time.DateOnly)
	}

	sorted := slices.Clone(tasks)
	slices.SortStableFunc(sorted, func(a, b *factoryv1.Task) int {
		if c := b.CreationTimestamp.Compare(a.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})

	endedSorted := slices.Clone(tasks)
	slices.SortStableFunc(endedSorted, func(a, b *factoryv1.Task) int {
		if c := taskBucketTime(b).Compare(taskBucketTime(a)); c != 0 {
			return c
		}
		if c := b.CreationTimestamp.Compare(a.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})

	openCount := 0
	var totalMicroUSD int64
	totalMerged := 0
	for _, t := range tasks {
		if !factoryv1.TerminalPhase(t.Status.Phase) {
			openCount++
		}
		totalMicroUSD += taskCostMicroUSD(t)
		tsDay := taskBucketTime(t).UTC().Truncate(24 * time.Hour)
		dayIdx := int(tsDay.Sub(startDay) / (24 * time.Hour))
		inSpark := dayIdx >= 0 && dayIdx < routineSparklineDays

		switch t.Status.Phase {
		case factoryv1.PhaseDone:
			if t.Status.Outcome == "failed" {
				if inSpark {
					sparkline[dayIdx].Escalated++
				}
			} else {
				totalMerged++
				if inSpark {
					sparkline[dayIdx].Merged++
				}
			}
		case factoryv1.PhaseClosed, factoryv1.PhaseReverted:
			if inSpark {
				sparkline[dayIdx].Closed++
			}
		case factoryv1.PhaseNoOp:
			if inSpark {
				sparkline[dayIdx].NoOp++
			}
		case factoryv1.PhaseEscalated, factoryv1.PhaseStopped, factoryv1.PhaseRejected:
			if inSpark {
				sparkline[dayIdx].Escalated++
			}
		}
	}

	window := cmp.Or(rr.Breaker.Window, config.DefaultBreakerWindow)
	rollingMerged := 0
	rollingClosed := 0
	rollingNoOp := 0
	endedCount := 0
	for _, t := range endedSorted {
		if !factoryv1.TerminalPhase(t.Status.Phase) {
			continue
		}
		endedCount++
		if endedCount <= window && t.Status.Phase == factoryv1.PhaseNoOp {
			rollingNoOp++
		}
		if rollingMerged+rollingClosed >= window {
			continue
		}
		switch t.Status.Phase {
		case factoryv1.PhaseDone:
			if t.Status.Outcome != "failed" {
				rollingMerged++
			}
		case factoryv1.PhaseClosed, factoryv1.PhaseReverted:
			rollingClosed++
		}
	}

	var mergeRate *float64
	if denom := rollingMerged + rollingClosed; denom > 0 {
		v := float64(rollingMerged) / float64(denom)
		mergeRate = &v
	}
	var usdPerMerged *float64
	if totalMerged > 0 {
		v := (float64(totalMicroUSD) / 1e6) / float64(totalMerged)
		usdPerMerged = &v
	}

	paused := rr.Paused || st.Paused || st.BreakerState == upkeep.BreakerPaused
	pausedReason := st.PausedReason
	if paused && pausedReason == "" {
		if rr.Paused {
			pausedReason = "declarative"
		} else if st.BreakerState == upkeep.BreakerPaused {
			pausedReason = st.BreakerReason
		}
	}

	var nextRunAt *time.Time
	if rr.Mode != config.RoutineModeDisabled && !paused && rr.Cron != "" {
		if sched, err := cron.ParseStandard(rr.Cron); err == nil {
			next := sched.Next(now).UTC()
			nextRunAt = &next
		}
	}

	var promptSHA string
	if rr.DefaultPrompt != "" {
		sum := sha256.Sum256([]byte(rr.DefaultPrompt))
		promptSHA = hex.EncodeToString(sum[:])
	}

	ov := RoutineOverview{
		Repository:        repo,
		Routine:           rr.Name,
		Description:       rr.Description,
		Mode:              rr.Mode,
		Workflow:          rr.Workflow,
		ProbeKind:         rr.Probe.Kind,
		Cron:              rr.Cron,
		Paused:            paused,
		DeclarativePaused: rr.Paused,
		PausedReason:      pausedReason,
		PausedBy:          st.PausedBy,
		PausedAt:          st.PausedAt,
		MaxOpenPRs:        cmp.Or(rr.MaxOpenPRs, 1),
		MaxCostUSD:        cmp.Or(rr.MaxCostUSD, config.DefaultRoutineMaxCostUSD),
		OpenCount:         openCount,
		NextRunAt:         nextRunAt,
		Sparkline:         sparkline,
		RollingMerged:     rollingMerged,
		RollingClosed:     rollingClosed,
		RollingNoOp:       rollingNoOp,
		MergeRate:         mergeRate,
		USDPerMerged:      usdPerMerged,
		PromptEditURL:     "https://github.com/" + repo + "/edit/main/.github/agent-factory/upkeep/" + url.PathEscape(rr.Name) + ".md",
		PromptSHA:         promptSHA,
		Skill:             rr.Skill,
		BreakerState:      cmp.Or(st.BreakerState, "ok"),
		CanWrite:          canWrite,
	}
	return ov, sorted
}

func taskBucketTime(t *factoryv1.Task) time.Time {
	if t.Status.EndedAt != nil {
		return t.Status.EndedAt.Time
	}
	if t.Status.PhaseSince != nil {
		return t.Status.PhaseSince.Time
	}
	return t.CreationTimestamp.Time
}

func taskCostMicroUSD(t *factoryv1.Task) int64 {
	var sum int64
	for _, rec := range t.Status.Runs {
		sum += rec.CostMicroUSD
	}
	return max(sum, t.Status.Usage.CostMicroUSD)
}

func taskCostUSD(t *factoryv1.Task) float64 {
	return float64(taskCostMicroUSD(t)) / 1e6
}

func routineTaskOutcome(t *factoryv1.Task) string {
	switch t.Status.Phase {
	case factoryv1.PhaseDone:
		switch {
		case t.Status.Outcome == "failed":
			return "failed"
		case t.Status.Outcome != "":
			return t.Status.Outcome
		case t.Status.PullRequest != nil && t.Status.PullRequest.HumanCommits:
			return "merged_human"
		default:
			return "accepted"
		}
	case factoryv1.PhaseNoOp:
		return "no_op"
	case factoryv1.PhaseClosed:
		return "closed"
	case factoryv1.PhaseStopped:
		return "stopped"
	case factoryv1.PhaseRejected:
		return "rejected"
	case factoryv1.PhaseReverted:
		return "reverted"
	default:
		return t.Status.Outcome
	}
}
