// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	threadMapName = "factory-slack-threads"
	threadTTL     = 30 * 24 * time.Hour
	putAttempts   = 3
)

// Thread is where a room filed from Slack is followed (R25). At is when it was put, for the
// 30-day prune.
type Thread struct {
	Channel string    `json:"channel"`
	TS      string    `json:"ts"`
	At      time.Time `json:"at"`
	// Author is the Slack user id of the requester; Filer is the Slack user id of the maintainer
	// who confirmed/filed the task (G6). Both are validated against userRE before any mention.
	Author string `json:"author,omitempty"`
	Filer  string `json:"filer,omitempty"`
	// EndTS is the ts of the thread's one end line (UX7): posted on room.ended (or room.merged when it
	// arrives first) and rewritten in place on a later merge or revert.
	EndTS string `json:"endTS,omitempty"`
	// DoneMentioned records that the thread's done notification has already mentioned once (G6),
	// so later end-line updates (room.merged after room.ended, or a revert) do not mention again.
	DoneMentioned bool `json:"doneMentioned,omitempty"`
	// Coordination marks a coordination parent's thread (v4): its deliveries are recomputed against
	// the channel at every event (projectCoordination), never posted as a single room's line.
	Coordination bool `json:"coordination,omitempty"`
}

// actorFor returns the one Slack user id who can act on a notification in this thread, or "" when
// none is recorded or valid. For a question to the issue's author (needs_author), the requester
// comes first; for maintainer actions and done notifications, the filer comes first.
func (t Thread) actorFor(reason string) string {
	first, second := t.Filer, t.Author
	if reason == "needs_author" {
		first, second = t.Author, t.Filer
	}
	if userRE.MatchString(first) {
		return first
	}
	if userRE.MatchString(second) {
		return second
	}
	return ""
}

// Threads is the thread map, the ConfigMap factory-slack-threads in production.
type Threads interface {
	Put(ctx context.Context, room string, t Thread) error
	Get(ctx context.Context, room string) (Thread, bool, error)
}

// ConfigMapThreads keeps the map in one ConfigMap: a key per room, the thread as JSON.
type ConfigMapThreads struct {
	C         client.Client
	Namespace string
	Now       func() time.Time
	MaxBytes  int          // the encoded map's cap; defaults to 900 KiB (a ConfigMap holds 1 MiB)
	Logger    *slog.Logger // optional
}

func newThreadCM(ns string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: threadMapName, Namespace: ns}}
}

// Put records the thread and drops every entry older than 30 days. A conflict (another replica
// or a restart racing) re-reads and retries, three attempts in all.
func (m ConfigMapThreads) Put(ctx context.Context, room string, t Thread) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	var last error
	for range putAttempts {
		cm := newThreadCM(m.Namespace)
		err := m.C.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		create := apierrors.IsNotFound(err)
		if err != nil && !create {
			return fmt.Errorf("slackapp: thread map: %w", err)
		}
		m.prune(cm)
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[room] = string(raw)
		m.bound(cm, room)
		if create {
			err = m.C.Create(ctx, cm)
		} else {
			err = m.C.Update(ctx, cm)
		}
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("slackapp: thread map: %w", err)
		}
		last = err
	}
	return fmt.Errorf("slackapp: thread map: %w", last)
}

// prune drops what is older than the TTL, and what cannot be read.
func (m ConfigMapThreads) prune(cm *corev1.ConfigMap) {
	for room, v := range cm.Data {
		var t Thread
		if json.Unmarshal([]byte(v), &t) != nil || m.Now().Sub(t.At) > threadTTL {
			delete(cm.Data, room)
		}
	}
}

// bound keeps the encoded map under MaxBytes (a ConfigMap holds 1 MiB) by dropping the oldest
// entries, never the one just put. It logs how many, never which.
func (m ConfigMapThreads) bound(cm *corev1.ConfigMap, keep string) {
	limit := m.MaxBytes
	if limit <= 0 {
		limit = 900 << 10
	}
	size := 0
	type entry struct {
		room string
		at   time.Time
	}
	entries := make([]entry, 0, len(cm.Data))
	for room, v := range cm.Data {
		size += len(room) + len(v)
		var t Thread
		_ = json.Unmarshal([]byte(v), &t)
		entries = append(entries, entry{room, t.At})
	}
	if size <= limit {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	dropped := 0
	for _, e := range entries {
		if size <= limit {
			break
		}
		if e.room == keep {
			continue
		}
		size -= len(e.room) + len(cm.Data[e.room])
		delete(cm.Data, e.room)
		dropped++
	}
	if m.Logger != nil {
		m.Logger.Warn("thread map over its cap, oldest entries dropped", "dropped", dropped)
	}
}

// Get is the thread a room was filed from, if any.
func (m ConfigMapThreads) Get(ctx context.Context, room string) (Thread, bool, error) {
	cm := newThreadCM(m.Namespace)
	if err := m.C.Get(ctx, client.ObjectKeyFromObject(cm), cm); err != nil {
		if apierrors.IsNotFound(err) {
			return Thread{}, false, nil
		}
		return Thread{}, false, fmt.Errorf("slackapp: thread map: %w", err)
	}
	v, ok := cm.Data[room]
	if !ok {
		return Thread{}, false, nil
	}
	var t Thread
	if err := json.Unmarshal([]byte(v), &t); err != nil {
		return Thread{}, false, nil
	}
	return t, true, nil
}
