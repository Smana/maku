// SPDX-License-Identifier: Apache-2.0

package upkeep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// StateConfigMap is the ConfigMap holding operational routine state in the factory namespace.
	StateConfigMap = "agent-factory-routine-state"
	// MaxSkipRecords caps stored skip diagnostics per (repository, routine) (P1-R5).
	MaxSkipRecords = 50
	// SkipTTL is the retention window for skip diagnostics (P1-R5).
	SkipTTL = 14 * 24 * time.Hour

	stateWriteAttempts = 8
)

// SkipRecord is one skipped tick diagnostic entry stored for Run History (P1-R5).
type SkipRecord struct {
	SlotKey string    `json:"slotKey,omitempty"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason"`
}

// RoutineState holds persisted operational state for one (repository, routine) pair.
// Breaker fields are reserved for Phase 2 and preserved across Phase 1 updates, along with
// any unknown future JSON fields.
type RoutineState struct {
	Paused       bool         `json:"paused,omitempty"`
	PausedReason string       `json:"pausedReason,omitempty"`
	PausedBy     string       `json:"pausedBy,omitempty"`
	PausedAt     *time.Time   `json:"pausedAt,omitempty"`
	Skips        []SkipRecord `json:"skips,omitempty"`

	// Phase 2 yield circuit breaker fields (preserved on read-modify-write).
	BreakerState     string     `json:"breakerState,omitempty"`
	BreakerReason    string     `json:"breakerReason,omitempty"`
	TrippedPromptSHA string     `json:"trippedPromptSHA,omitempty"`
	TrippedAt        *time.Time `json:"trippedAt,omitempty"`

	extra map[string]json.RawMessage
}

type routineStateAlias struct {
	Paused           bool         `json:"paused,omitempty"`
	PausedReason     string       `json:"pausedReason,omitempty"`
	PausedBy         string       `json:"pausedBy,omitempty"`
	PausedAt         *time.Time   `json:"pausedAt,omitempty"`
	Skips            []SkipRecord `json:"skips,omitempty"`
	BreakerState     string       `json:"breakerState,omitempty"`
	BreakerReason    string       `json:"breakerReason,omitempty"`
	TrippedPromptSHA string       `json:"trippedPromptSHA,omitempty"`
	TrippedAt        *time.Time   `json:"trippedAt,omitempty"`
}

var knownRoutineStateKeys = map[string]bool{
	"paused":           true,
	"pausedReason":     true,
	"pausedBy":         true,
	"pausedAt":         true,
	"skips":            true,
	"breakerState":     true,
	"breakerReason":    true,
	"trippedPromptSHA": true,
	"trippedAt":        true,
}

// UnmarshalJSON decodes RoutineState while retaining unknown future JSON fields.
func (s *RoutineState) UnmarshalJSON(data []byte) error {
	var alias routineStateAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k := range knownRoutineStateKeys {
		delete(raw, k)
	}
	*s = RoutineState{
		Paused:           alias.Paused,
		PausedReason:     alias.PausedReason,
		PausedBy:         alias.PausedBy,
		PausedAt:         alias.PausedAt,
		Skips:            alias.Skips,
		BreakerState:     alias.BreakerState,
		BreakerReason:    alias.BreakerReason,
		TrippedPromptSHA: alias.TrippedPromptSHA,
		TrippedAt:        alias.TrippedAt,
	}
	if len(raw) > 0 {
		s.extra = raw
	}
	return nil
}

// MarshalJSON encodes RoutineState while preserving any unknown future JSON fields.
func (s RoutineState) MarshalJSON() ([]byte, error) {
	alias := routineStateAlias{
		Paused:           s.Paused,
		PausedReason:     s.PausedReason,
		PausedBy:         s.PausedBy,
		PausedAt:         s.PausedAt,
		Skips:            s.Skips,
		BreakerState:     s.BreakerState,
		BreakerReason:    s.BreakerReason,
		TrippedPromptSHA: s.TrippedPromptSHA,
		TrippedAt:        s.TrippedAt,
	}
	if len(s.extra) == 0 {
		return json.Marshal(alias)
	}
	b, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	for k, v := range s.extra {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return json.Marshal(out)
}

func (s RoutineState) clone() RoutineState {
	out := s
	out.Skips = slices.Clone(s.Skips)
	if s.PausedAt != nil {
		t := *s.PausedAt
		out.PausedAt = &t
	}
	if s.TrippedAt != nil {
		t := *s.TrippedAt
		out.TrippedAt = &t
	}
	if len(s.extra) > 0 {
		out.extra = maps.Clone(s.extra)
	}
	return out
}

type repoStateDoc struct {
	Repository string                  `json:"repository"`
	Routines   map[string]RoutineState `json:"routines,omitempty"`
}

// StateStore manages operational routine state in ConfigMap agent-factory-routine-state using
// optimistic concurrency on resourceVersion. Client must read and write ConfigMaps live (uncached),
// because the factory manager's cache only watches agent-factory-stop.
type StateStore struct {
	Client    client.Client
	Reader    client.Reader
	Namespace string
	Now       func() time.Time
	Log       *slog.Logger
}

func (s *StateStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *StateStore) reader() client.Reader {
	if s.Reader != nil {
		return s.Reader
	}
	return s.Client
}

func (s *StateStore) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

func repoStateKey(repo string) string {
	return strings.ReplaceAll(strings.ToLower(repo), "/", "__") + ".json"
}

// Get reads the persisted RoutineState for (repo, routine). A missing ConfigMap or entry
// returns a zero RoutineState with nil error.
func (s *StateStore) Get(ctx context.Context, repo, routine string) (RoutineState, error) {
	if repo == "" || routine == "" {
		return RoutineState{}, errors.New("upkeep state: repository and routine are required")
	}
	if s.reader() == nil || s.Namespace == "" {
		return RoutineState{}, errors.New("upkeep state: reader and namespace are required")
	}
	var cm corev1.ConfigMap
	err := s.reader().Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: StateConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		return RoutineState{}, nil
	}
	if err != nil {
		return RoutineState{}, fmt.Errorf("upkeep state get: %w", err)
	}
	raw, ok := cm.Data[repoStateKey(repo)]
	if !ok || raw == "" {
		return RoutineState{}, nil
	}
	var doc repoStateDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return RoutineState{}, fmt.Errorf("upkeep state decode %s: %w", repo, err)
	}
	st, ok := doc.Routines[routine]
	if !ok {
		return RoutineState{}, nil
	}
	return st.clone(), nil
}

// List reads all persisted RoutineState entries from ConfigMap agent-factory-routine-state in
// one Get, keyed by lowercased repository name then routine name.
func (s *StateStore) List(ctx context.Context) (map[string]map[string]RoutineState, error) {
	if s.reader() == nil || s.Namespace == "" {
		return nil, errors.New("upkeep state: reader and namespace are required")
	}
	var cm corev1.ConfigMap
	err := s.reader().Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: StateConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		return map[string]map[string]RoutineState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("upkeep state list: %w", err)
	}
	out := map[string]map[string]RoutineState{}
	for key, raw := range cm.Data {
		if !strings.HasSuffix(key, ".json") || raw == "" {
			continue
		}
		var doc repoStateDoc
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return nil, fmt.Errorf("upkeep state decode %s: %w", key, err)
		}
		repoKey := strings.ToLower(doc.Repository)
		if repoKey == "" {
			repoKey = strings.ReplaceAll(strings.TrimSuffix(key, ".json"), "__", "/")
		}
		if len(doc.Routines) == 0 {
			continue
		}
		routines := make(map[string]RoutineState, len(doc.Routines))
		for name, st := range doc.Routines {
			routines[name] = st.clone()
		}
		out[repoKey] = routines
	}
	return out, nil
}

// SetPaused updates the manual pause flags for (repo, routine), performing zero API writes when
// the state already matches.
func (s *StateStore) SetPaused(ctx context.Context, repo, routine string, paused bool, reason, by string) error {
	return s.mutate(ctx, repo, routine, func(st *RoutineState, now time.Time) bool {
		if paused {
			if st.Paused && st.PausedReason == reason && st.PausedBy == by {
				return false
			}
			st.Paused = true
			st.PausedReason = reason
			st.PausedBy = by
			t := now
			st.PausedAt = &t
			return true
		}
		if !st.Paused && st.PausedReason == "" && st.PausedBy == "" && st.PausedAt == nil {
			return false
		}
		st.Paused = false
		st.PausedReason = ""
		st.PausedBy = ""
		st.PausedAt = nil
		return true
	})
}

// RecordSkip persists a bounded skip diagnostic entry deduplicated by slotKey, performing zero
// API writes when the same (slotKey, reason) is already recorded.
func (s *StateStore) RecordSkip(ctx context.Context, repo, routine, slotKey, reason string, at time.Time) error {
	if reason == "" {
		return errors.New("upkeep state: skip reason is required")
	}
	at = at.UTC()
	return s.mutate(ctx, repo, routine, func(st *RoutineState, _ time.Time) bool {
		cutoff := at.Add(-SkipTTL)
		kept := make([]SkipRecord, 0, len(st.Skips)+1)
		updated := false
		for _, e := range st.Skips {
			if e.At.Before(cutoff) {
				continue
			}
			if slotKey != "" && e.SlotKey == slotKey {
				if e.Reason == reason {
					return false
				}
				e.Reason = reason
				e.At = at
				updated = true
			}
			kept = append(kept, e)
		}
		if !updated {
			kept = append(kept, SkipRecord{SlotKey: slotKey, At: at, Reason: reason})
		}
		slices.SortStableFunc(kept, func(a, b SkipRecord) int {
			return a.At.Compare(b.At)
		})
		if len(kept) > MaxSkipRecords {
			kept = slices.Clone(kept[len(kept)-MaxSkipRecords:])
		}
		st.Skips = kept
		return true
	})
}

func (s *StateStore) mutate(ctx context.Context, repo, routine string, fn func(st *RoutineState, now time.Time) bool) error {
	if repo == "" || routine == "" {
		return errors.New("upkeep state: repository and routine are required")
	}
	if s.Client == nil || s.Namespace == "" {
		return errors.New("upkeep state: client and namespace are required")
	}
	cmKey := types.NamespacedName{Namespace: s.Namespace, Name: StateConfigMap}
	repoKey := repoStateKey(repo)
	for range stateWriteAttempts {
		var cm corev1.ConfigMap
		err := s.reader().Get(ctx, cmKey, &cm)
		existed := true
		if apierrors.IsNotFound(err) {
			cm = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: StateConfigMap}}
			existed = false
		} else if err != nil {
			return fmt.Errorf("upkeep state %s/%s: %w", repo, routine, err)
		}
		doc := repoStateDoc{
			Repository: repo,
			Routines:   map[string]RoutineState{},
		}
		if raw := cm.Data[repoKey]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &doc); err != nil {
				return fmt.Errorf("upkeep state decode %s: %w", repo, err)
			}
			if doc.Repository == "" {
				doc.Repository = repo
			}
			if doc.Routines == nil {
				doc.Routines = map[string]RoutineState{}
			}
		}
		st := doc.Routines[routine].clone()
		if !fn(&st, s.now()) {
			return nil
		}
		doc.Routines[routine] = st
		raw, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("upkeep state encode %s: %w", repo, err)
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[repoKey] = string(raw)
		if existed {
			err = s.Client.Update(ctx, &cm)
		} else {
			err = s.Client.Create(ctx, &cm)
		}
		switch {
		case err == nil:
			return nil
		case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
			s.log().Debug("upkeep state conflict, retrying", "repository", repo, "routine", routine)
			continue
		default:
			return fmt.Errorf("upkeep state write %s/%s: %w", repo, routine, err)
		}
	}
	return fmt.Errorf("upkeep state %s/%s: too many conflicts", repo, routine)
}
