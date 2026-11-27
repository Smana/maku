// SPDX-License-Identifier: Apache-2.0

package upkeep

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/factory/config"
)

const (
	// BindingsConfigMap is the ConfigMap holding UI-managed routine bindings when routines.uiWrite is true.
	BindingsConfigMap = "agent-factory-routine-bindings"

	bindingsWriteAttempts = 8
)

type repoBindingsDoc struct {
	Repository string                  `json:"repository"`
	Routines   []config.RoutineBinding `json:"routines,omitempty"`
}

// BindingsStore manages opt-in UI routine bindings in ConfigMap agent-factory-routine-bindings
// using optimistic concurrency on resourceVersion. Client must read and write ConfigMaps live
// (uncached) so concurrent updates see the current resourceVersion.
type BindingsStore struct {
	Client    client.Client
	Reader    client.Reader
	Namespace string
	Now       func() time.Time
	Log       *slog.Logger
}

func (s *BindingsStore) reader() client.Reader {
	if s.Reader != nil {
		return s.Reader
	}
	return s.Client
}

func (s *BindingsStore) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

// DecodeBindingsMap strictly decodes ConfigMap data keys (<owner>__<name>.json) into a
// lowercased repository -> sorted RoutineBinding slice map.
func DecodeBindingsMap(data map[string]string) (map[string][]config.RoutineBinding, error) {
	out := map[string][]config.RoutineBinding{}
	for _, key := range slices.Sorted(maps.Keys(data)) {
		raw := data[key]
		if !strings.HasSuffix(key, ".json") || strings.TrimSpace(raw) == "" {
			continue
		}
		doc, err := decodeBindingsDoc(raw)
		if err != nil {
			return nil, fmt.Errorf("upkeep bindings decode %s: %w", key, err)
		}
		repoKey := strings.ToLower(doc.Repository)
		if repoKey == "" {
			repoKey = strings.ReplaceAll(strings.TrimSuffix(key, ".json"), "__", "/")
		}
		if len(doc.Routines) == 0 {
			continue
		}
		bindings := slices.Clone(doc.Routines)
		slices.SortFunc(bindings, func(a, b config.RoutineBinding) int {
			return cmp.Compare(a.Name, b.Name)
		})
		out[repoKey] = bindings
	}
	return out, nil
}

func decodeBindingsDoc(raw string) (repoBindingsDoc, error) {
	var doc repoBindingsDoc
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return repoBindingsDoc{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return repoBindingsDoc{}, errors.New("unexpected trailing JSON content")
	}
	seen := make(map[string]bool, len(doc.Routines))
	for _, b := range doc.Routines {
		if b.Name == "" || !config.ValidRoutineName(b.Name) {
			return repoBindingsDoc{}, fmt.Errorf("invalid routine name %q", b.Name)
		}
		if seen[b.Name] {
			return repoBindingsDoc{}, fmt.Errorf("duplicate routine %q", b.Name)
		}
		seen[b.Name] = true
	}
	return doc, nil
}

// Get reads all UI-managed routine bindings for repo. A missing ConfigMap or entry returns nil, nil.
func (s *BindingsStore) Get(ctx context.Context, repo string) ([]config.RoutineBinding, error) {
	if repo == "" {
		return nil, errors.New("upkeep bindings: repository is required")
	}
	if s.reader() == nil || s.Namespace == "" {
		return nil, errors.New("upkeep bindings: reader and namespace are required")
	}
	var cm corev1.ConfigMap
	err := s.reader().Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: BindingsConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("upkeep bindings get: %w", err)
	}
	raw, ok := cm.Data[repoStateKey(repo)]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	doc, err := decodeBindingsDoc(raw)
	if err != nil {
		return nil, fmt.Errorf("upkeep bindings decode %s: %w", repo, err)
	}
	bindings := slices.Clone(doc.Routines)
	slices.SortFunc(bindings, func(a, b config.RoutineBinding) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return bindings, nil
}

// List reads all UI-managed routine bindings from ConfigMap agent-factory-routine-bindings in one
// Get, keyed by lowercased repository name.
func (s *BindingsStore) List(ctx context.Context) (map[string][]config.RoutineBinding, error) {
	if s.reader() == nil || s.Namespace == "" {
		return nil, errors.New("upkeep bindings: reader and namespace are required")
	}
	var cm corev1.ConfigMap
	err := s.reader().Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: BindingsConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		return map[string][]config.RoutineBinding{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("upkeep bindings list: %w", err)
	}
	return DecodeBindingsMap(cm.Data)
}

// Put upserts b for repo in ConfigMap agent-factory-routine-bindings with optimistic concurrency on
// resourceVersion, returning the SHA-256 digests of the binding before and after the write.
func (s *BindingsStore) Put(ctx context.Context, repo string, b config.RoutineBinding) (oldDigest, newDigest string, err error) {
	if repo == "" || !config.ValidRoutineName(b.Name) {
		return "", "", errors.New("upkeep bindings: repository and valid routine name are required")
	}
	err = s.mutate(ctx, repo, func(doc *repoBindingsDoc) bool {
		idx := slices.IndexFunc(doc.Routines, func(cur config.RoutineBinding) bool { return cur.Name == b.Name })
		if idx >= 0 {
			oldDigest = bindingDigest(doc.Routines[idx])
			newDigest = bindingDigest(b)
			if reflect.DeepEqual(doc.Routines[idx], b) {
				return false
			}
			doc.Routines[idx] = b
		} else {
			oldDigest = ""
			newDigest = bindingDigest(b)
			doc.Routines = append(doc.Routines, b)
		}
		slices.SortFunc(doc.Routines, func(x, y config.RoutineBinding) int {
			return cmp.Compare(x.Name, y.Name)
		})
		return true
	})
	if err != nil {
		return "", "", err
	}
	return oldDigest, newDigest, nil
}

// Delete removes routine from repo in ConfigMap agent-factory-routine-bindings, deleting the repo
// key when no bindings remain for repo.
func (s *BindingsStore) Delete(ctx context.Context, repo, routine string) error {
	if repo == "" || routine == "" {
		return errors.New("upkeep bindings: repository and routine are required")
	}
	return s.mutate(ctx, repo, func(doc *repoBindingsDoc) bool {
		idx := slices.IndexFunc(doc.Routines, func(cur config.RoutineBinding) bool { return cur.Name == routine })
		if idx < 0 {
			return false
		}
		doc.Routines = slices.Delete(doc.Routines, idx, idx+1)
		return true
	})
}

func (s *BindingsStore) mutate(ctx context.Context, repo string, fn func(doc *repoBindingsDoc) bool) error {
	if s.Client == nil || s.Namespace == "" {
		return errors.New("upkeep bindings: client and namespace are required")
	}
	cmKey := types.NamespacedName{Namespace: s.Namespace, Name: BindingsConfigMap}
	repoKey := repoStateKey(repo)
	for range bindingsWriteAttempts {
		var cm corev1.ConfigMap
		err := s.reader().Get(ctx, cmKey, &cm)
		existed := true
		if apierrors.IsNotFound(err) {
			cm = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: BindingsConfigMap}}
			existed = false
		} else if err != nil {
			return fmt.Errorf("upkeep bindings %s: %w", repo, err)
		}
		doc := repoBindingsDoc{Repository: repo}
		if raw := cm.Data[repoKey]; strings.TrimSpace(raw) != "" {
			decoded, err := decodeBindingsDoc(raw)
			if err != nil {
				return fmt.Errorf("upkeep bindings decode %s: %w", repo, err)
			}
			doc = decoded
			if doc.Repository == "" {
				doc.Repository = repo
			}
		}
		if !fn(&doc) {
			return nil
		}
		if len(doc.Routines) == 0 {
			if !existed {
				return nil
			}
			delete(cm.Data, repoKey)
		} else {
			raw, err := json.Marshal(doc)
			if err != nil {
				return fmt.Errorf("upkeep bindings encode %s: %w", repo, err)
			}
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data[repoKey] = string(raw)
		}
		if existed {
			err = s.Client.Update(ctx, &cm)
		} else {
			err = s.Client.Create(ctx, &cm)
		}
		switch {
		case err == nil:
			return nil
		case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
			s.log().Debug("upkeep bindings conflict, retrying", "repository", repo)
			continue
		default:
			return fmt.Errorf("upkeep bindings write %s: %w", repo, err)
		}
	}
	return fmt.Errorf("upkeep bindings %s: too many conflicts", repo)
}

func bindingDigest(b config.RoutineBinding) string {
	raw, err := json.Marshal(b)
	if err != nil {
		return ""
	}
	return sha256Hex(string(raw))
}
