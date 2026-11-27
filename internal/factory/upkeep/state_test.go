// SPDX-License-Identifier: Apache-2.0

package upkeep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func stateScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	return s
}

type countingStateClient struct {
	client.Client
	creates  atomic.Int32
	updates  atomic.Int32
	onCreate func(context.Context, client.Object) error
	onUpdate func(context.Context, client.Object) error
}

func (c *countingStateClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if c.onCreate != nil {
		if err := c.onCreate(ctx, obj); err != nil {
			return err
		}
	}
	c.creates.Add(1)
	return c.Client.Create(ctx, obj, opts...)
}

func (c *countingStateClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if c.onUpdate != nil {
		if err := c.onUpdate(ctx, obj); err != nil {
			return err
		}
	}
	c.updates.Add(1)
	return c.Client.Update(ctx, obj, opts...)
}

func TestStateStoreGetAndSetPaused(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	cc := &countingStateClient{Client: fake.NewClientBuilder().WithScheme(stateScheme()).Build()}
	st := &StateStore{
		Client:    cc,
		Namespace: "maku",
		Now:       func() time.Time { return now },
	}

	got, err := st.Get(t.Context(), "acme/app", "dead-code")
	if err != nil || got.Paused || len(got.Skips) != 0 {
		t.Fatalf("missing cm Get: got=%+v err=%v", got, err)
	}

	if err := st.SetPaused(t.Context(), "acme/app", "dead-code", true, "noisy", "github:alice"); err != nil {
		t.Fatal(err)
	}
	if cc.creates.Load() != 1 || cc.updates.Load() != 0 {
		t.Fatalf("creates=%d updates=%d", cc.creates.Load(), cc.updates.Load())
	}

	got, err = st.Get(t.Context(), "acme/app", "dead-code")
	if err != nil || !got.Paused || got.PausedReason != "noisy" || got.PausedBy != "github:alice" || got.PausedAt == nil || !got.PausedAt.Equal(now) {
		t.Fatalf("paused Get: %+v err=%v", got, err)
	}

	// Idempotent SetPaused(true) triggers zero writes.
	if err := st.SetPaused(t.Context(), "acme/app", "dead-code", true, "noisy", "github:alice"); err != nil {
		t.Fatal(err)
	}
	if cc.creates.Load() != 1 || cc.updates.Load() != 0 {
		t.Fatalf("idempotent pause wrote: creates=%d updates=%d", cc.creates.Load(), cc.updates.Load())
	}

	// Record a skip, then unpause: skips remain intact while pause fields clear.
	if err := st.RecordSkip(t.Context(), "acme/app", "dead-code", "slot-1", "open_cap", now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPaused(t.Context(), "acme/app", "dead-code", false, "", ""); err != nil {
		t.Fatal(err)
	}
	got, err = st.Get(t.Context(), "acme/app", "dead-code")
	if err != nil || got.Paused || got.PausedReason != "" || got.PausedBy != "" || got.PausedAt != nil || len(got.Skips) != 1 {
		t.Fatalf("unpaused Get: %+v err=%v", got, err)
	}

	// List returns all repos keyed lowercased.
	all, err := st.List(t.Context())
	if err != nil || len(all["acme/app"]) != 1 {
		t.Fatalf("List: %+v err=%v", all, err)
	}
}

func TestStateStoreConflictRetry(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	base := fake.NewClientBuilder().WithScheme(stateScheme()).Build()
	var injectCreateOnce, injectUpdateOnce atomic.Bool
	injectCreateOnce.Store(true)
	injectUpdateOnce.Store(true)

	cc := &countingStateClient{
		Client: base,
		onCreate: func(ctx context.Context, _ client.Object) error {
			if injectCreateOnce.Swap(false) {
				// Another writer creates the ConfigMap first with another routine's state.
				other := &StateStore{Client: base, Namespace: "maku", Now: func() time.Time { return now }}
				if err := other.SetPaused(ctx, "acme/app", "doc-drift", true, "concurrent", "github:bob"); err != nil {
					return err
				}
				return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "configmaps"}, StateConfigMap)
			}
			return nil
		},
		onUpdate: func(ctx context.Context, _ client.Object) error {
			if injectUpdateOnce.Swap(false) {
				// Simulate a concurrent writer advancing resourceVersion with another routine's skip.
				other := &StateStore{Client: base, Namespace: "maku", Now: func() time.Time { return now }}
				if err := other.RecordSkip(ctx, "acme/app", "flaky-tests", "s1", "cooldown", now); err != nil {
					return err
				}
				return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, StateConfigMap, errors.New("rv conflict"))
			}
			return nil
		},
	}

	st := &StateStore{Client: cc, Namespace: "maku", Now: func() time.Time { return now }}
	if err := st.SetPaused(t.Context(), "acme/app", "dead-code", true, "manual", "github:alice"); err != nil {
		t.Fatalf("SetPaused after AlreadyExists + Conflict: %v", err)
	}
	gotDC, err := st.Get(t.Context(), "acme/app", "dead-code")
	if err != nil || !gotDC.Paused {
		t.Fatalf("dead-code: %+v err=%v", gotDC, err)
	}
	gotDD, err := st.Get(t.Context(), "acme/app", "doc-drift")
	if err != nil || !gotDD.Paused {
		t.Fatalf("doc-drift concurrent write lost: %+v err=%v", gotDD, err)
	}
	gotFT, err := st.Get(t.Context(), "acme/app", "flaky-tests")
	if err != nil || len(gotFT.Skips) != 1 {
		t.Fatalf("flaky-tests concurrent write lost: %+v err=%v", gotFT, err)
	}

	// Concurrent goroutines under -race.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rName := fmt.Sprintf("routine-%d", idx)
			s := &StateStore{Client: base, Namespace: "maku", Now: func() time.Time { return now }}
			for attempt := 0; attempt < 5; attempt++ {
				err := s.RecordSkip(t.Context(), "acme/app", rName, "slot-race", "open_cap", now)
				if err == nil {
					return
				}
			}
			t.Errorf("concurrent RecordSkip for %s did not succeed", rName)
		}(i)
	}
	wg.Wait()
}

func TestStateStoreRecordSkip(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	cc := &countingStateClient{Client: fake.NewClientBuilder().WithScheme(stateScheme()).Build()}
	st := &StateStore{Client: cc, Namespace: "maku", Now: func() time.Time { return now }}

	// Same (slotKey, reason) on repeated 1-minute ticks emits zero extra writes.
	for i := range 5 {
		if err := st.RecordSkip(t.Context(), "acme/app", "dead-code", "2026-11-26T12:00:00Z", "open_cap", now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if cc.creates.Load() != 1 || cc.updates.Load() != 0 {
		t.Fatalf("repeated identical skip wrote: creates=%d updates=%d", cc.creates.Load(), cc.updates.Load())
	}

	// Changed reason for the same slotKey updates in place instead of duplicating.
	if err := st.RecordSkip(t.Context(), "acme/app", "dead-code", "2026-11-26T12:00:00Z", "cooldown", now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(t.Context(), "acme/app", "dead-code")
	if err != nil || len(got.Skips) != 1 || got.Skips[0].Reason != "cooldown" {
		t.Fatalf("changed reason: %+v err=%v", got.Skips, err)
	}

	// Trims oldest past 50 entries and prunes entries older than 14 days.
	if err := st.RecordSkip(t.Context(), "acme/app", "dead-code", "old-15d", "probe_empty", now.Add(-15*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for i := range 55 {
		slot := fmt.Sprintf("slot-%02d", i)
		if err := st.RecordSkip(t.Context(), "acme/app", "dead-code", slot, "probe_empty", now.Add(time.Duration(i+10)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got, err = st.Get(t.Context(), "acme/app", "dead-code")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skips) != MaxSkipRecords {
		t.Fatalf("len(Skips) = %d, want %d", len(got.Skips), MaxSkipRecords)
	}
	for _, sk := range got.Skips {
		if sk.SlotKey == "old-15d" || sk.SlotKey == "2026-11-26T12:00:00Z" || sk.SlotKey == "slot-00" {
			t.Fatalf("expected old/trimmed slot to be pruned, still present: %+v", sk)
		}
	}
}

func TestStateStorePreservesPhase2Fields(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	trippedAt := now.Add(-2 * time.Hour)
	seed := map[string]any{
		"repository": "acme/app",
		"routines": map[string]any{
			"dead-code": map[string]any{
				"paused":           false,
				"breakerState":     "half_open",
				"breakerReason":    "min_merge_rate",
				"trippedPromptSHA": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				"trippedAt":        trippedAt.Format(time.RFC3339),
				"futurePhase2Key":  map[string]any{"count": 3},
			},
		},
	}
	raw, _ := json.Marshal(seed)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: StateConfigMap, Namespace: "maku"},
		Data:       map[string]string{"acme__app.json": string(raw)},
	}
	base := fake.NewClientBuilder().WithScheme(stateScheme()).WithObjects(cm).Build()
	st := &StateStore{Client: base, Namespace: "maku", Now: func() time.Time { return now }}

	if err := st.SetPaused(t.Context(), "acme/app", "dead-code", true, "manual", "github:alice"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSkip(t.Context(), "acme/app", "dead-code", "slot-p2", "paused_manual", now); err != nil {
		t.Fatal(err)
	}

	got, err := st.Get(t.Context(), "acme/app", "dead-code")
	if err != nil {
		t.Fatal(err)
	}
	if got.BreakerState != "half_open" || got.BreakerReason != "min_merge_rate" ||
		got.TrippedPromptSHA != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" ||
		got.TrippedAt == nil || !got.TrippedAt.Equal(trippedAt) {
		t.Fatalf("Phase 2 fields lost: %+v", got)
	}

	// Verify unknown future JSON field survived round-trip in raw ConfigMap JSON.
	var storedCM corev1.ConfigMap
	if err := base.Get(t.Context(), types.NamespacedName{Namespace: "maku", Name: StateConfigMap}, &storedCM); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(storedCM.Data["acme__app.json"]), &doc); err != nil {
		t.Fatal(err)
	}
	rtMap := doc["routines"].(map[string]any)["dead-code"].(map[string]any)
	future, ok := rtMap["futurePhase2Key"].(map[string]any)
	if !ok || future["count"] != float64(3) {
		t.Fatalf("unknown future field lost: %+v", rtMap)
	}
}
