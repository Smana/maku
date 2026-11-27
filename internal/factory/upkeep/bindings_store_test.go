// SPDX-License-Identifier: Apache-2.0

package upkeep

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/internal/factory/config"
)

func TestBindingsStoreCRUDAndCleanup(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	cc := &countingStateClient{Client: fake.NewClientBuilder().WithScheme(stateScheme()).Build()}
	bs := &BindingsStore{
		Client:    cc,
		Namespace: "maku",
		Now:       func() time.Time { return now },
	}

	got, err := bs.Get(t.Context(), "Acme/App")
	if err != nil || len(got) != 0 {
		t.Fatalf("missing ConfigMap Get: got=%+v err=%v", got, err)
	}
	all, err := bs.List(t.Context())
	if err != nil || len(all) != 0 {
		t.Fatalf("missing ConfigMap List: all=%+v err=%v", all, err)
	}

	b1 := config.RoutineBinding{Name: "dead-links", Mode: config.RoutineModePropose, Cron: "0 3 * * 1"}
	oldD, newD, err := bs.Put(t.Context(), "Acme/App", b1)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if oldD != "" || newD == "" {
		t.Fatalf("first Put digests = (%q, %q), want empty old and non-empty new", oldD, newD)
	}
	if cc.creates.Load() != 1 || cc.updates.Load() != 0 {
		t.Fatalf("creates=%d updates=%d", cc.creates.Load(), cc.updates.Load())
	}

	// Idempotent Put returns identical digests with zero writes.
	oldD2, newD2, err := bs.Put(t.Context(), "Acme/App", b1)
	if err != nil || oldD2 != newD || newD2 != newD {
		t.Fatalf("idempotent Put = (%q, %q, %v), want (%q, %q, nil)", oldD2, newD2, err, newD, newD)
	}
	if cc.creates.Load() != 1 || cc.updates.Load() != 0 {
		t.Fatalf("idempotent Put wrote: creates=%d updates=%d", cc.creates.Load(), cc.updates.Load())
	}

	// Add second routine and second repo.
	b2 := config.RoutineBinding{Name: "doc-drift", Mode: config.RoutineModeDraft, Cron: "0 4 * * 1"}
	if _, _, err := bs.Put(t.Context(), "Acme/App", b2); err != nil {
		t.Fatalf("second routine Put: %v", err)
	}
	bOther := config.RoutineBinding{Name: "dead-code", Mode: config.RoutineModePR, Cron: "0 5 * * *"}
	if _, _, err := bs.Put(t.Context(), "Acme/Lib", bOther); err != nil {
		t.Fatalf("other repo Put: %v", err)
	}

	got, err = bs.Get(t.Context(), "acme/app")
	if err != nil || len(got) != 2 || got[0].Name != "dead-links" || got[1].Name != "doc-drift" {
		t.Fatalf("Get Acme/App = %+v err=%v", got, err)
	}

	// Update existing routine: oldDigest != newDigest.
	b1Updated := config.RoutineBinding{Name: "dead-links", Mode: config.RoutineModeDraft, Cron: "0 6 * * 1"}
	oldD3, newD3, err := bs.Put(t.Context(), "Acme/App", b1Updated)
	if err != nil || oldD3 != newD || newD3 == "" || newD3 == oldD3 {
		t.Fatalf("update Put digests = (%q, %q, %v)", oldD3, newD3, err)
	}

	all, err = bs.List(t.Context())
	if err != nil || len(all) != 2 || len(all["acme/app"]) != 2 || len(all["acme/lib"]) != 1 {
		t.Fatalf("List = %+v err=%v", all, err)
	}

	// Delete one routine from Acme/App, leaving doc-drift.
	if err := bs.Delete(t.Context(), "Acme/App", "dead-links"); err != nil {
		t.Fatalf("Delete dead-links: %v", err)
	}
	got, err = bs.Get(t.Context(), "Acme/App")
	if err != nil || len(got) != 1 || got[0].Name != "doc-drift" {
		t.Fatalf("Get after Delete = %+v err=%v", got, err)
	}

	// Delete last routine from Acme/App -> key removed from ConfigMap.
	if err := bs.Delete(t.Context(), "Acme/App", "doc-drift"); err != nil {
		t.Fatalf("Delete doc-drift: %v", err)
	}
	var cm corev1.ConfigMap
	if err := cc.Get(t.Context(), types.NamespacedName{Namespace: "maku", Name: BindingsConfigMap}, &cm); err != nil {
		t.Fatal(err)
	}
	if _, exists := cm.Data["acme__app.json"]; exists {
		t.Fatalf("empty repo key acme__app.json was not cleaned up: %+v", cm.Data)
	}
	if _, exists := cm.Data["acme__lib.json"]; !exists {
		t.Fatalf("sibling repo key acme__lib.json missing: %+v", cm.Data)
	}
}

func TestBindingsStoreConflictRetry(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(stateScheme()).Build()
	var createConflicts, updateConflicts atomic.Int32
	cc := &countingStateClient{
		Client: base,
		onCreate: func(context.Context, client.Object) error {
			if createConflicts.Add(1) <= 2 {
				return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "configmaps"}, BindingsConfigMap)
			}
			return nil
		},
		onUpdate: func(context.Context, client.Object) error {
			if updateConflicts.Add(1) <= 2 {
				return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, BindingsConfigMap, context.Canceled)
			}
			return nil
		},
	}
	bs := &BindingsStore{Client: cc, Namespace: "maku"}

	if _, _, err := bs.Put(t.Context(), "acme/app", config.RoutineBinding{Name: "dead-links", Mode: config.RoutineModePropose, Cron: "0 3 * * *"}); err != nil {
		t.Fatalf("Put should succeed after transient create conflicts: %v", err)
	}
	if _, _, err := bs.Put(t.Context(), "acme/app", config.RoutineBinding{Name: "dead-links", Mode: config.RoutineModeDraft, Cron: "0 3 * * *"}); err != nil {
		t.Fatalf("Put should succeed after transient update conflicts: %v", err)
	}

	// Exhaust 8 conflict retries.
	alwaysConflict := &countingStateClient{
		Client: base,
		onUpdate: func(context.Context, client.Object) error {
			return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, BindingsConfigMap, context.Canceled)
		},
	}
	bsFail := &BindingsStore{Client: alwaysConflict, Namespace: "maku"}
	if _, _, err := bsFail.Put(t.Context(), "acme/app", config.RoutineBinding{Name: "dead-links", Mode: config.RoutineModePR, Cron: "0 3 * * *"}); err == nil || !strings.Contains(err.Error(), "too many conflicts") {
		t.Fatalf("expected conflict exhaustion error, got %v", err)
	}
	if alwaysConflict.updates.Load() != 0 {
		t.Fatalf("expected onUpdate interception without base update")
	}
}

func TestDecodeBindingsMapStrict(t *testing.T) {
	valid := map[string]string{
		"README.txt":     "ignored non-json",
		"empty.json":     "",
		"acme__app.json": `{"repository":"Acme/App","routines":[{"name":"dead-links","mode":"propose","cron":"0 3 * * 1"}]}`,
	}
	got, err := DecodeBindingsMap(valid)
	if err != nil {
		t.Fatalf("valid DecodeBindingsMap: %v", err)
	}
	if len(got) != 1 || len(got["acme/app"]) != 1 || got["acme/app"][0].Name != "dead-links" {
		t.Fatalf("got = %+v", got)
	}

	for name, data := range map[string]map[string]string{
		"unknown top field":     {"a__b.json": `{"repository":"a/b","extra":1}`},
		"unknown binding field": {"a__b.json": `{"repository":"a/b","routines":[{"name":"dead-links","mode":"propose","unknown":true}]}`},
		"trailing json":         {"a__b.json": `{"repository":"a/b","routines":[]}{}`},
		"duplicate routine":     {"a__b.json": `{"repository":"a/b","routines":[{"name":"dead-links","mode":"propose"},{"name":"dead-links","mode":"draft"}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeBindingsMap(data); err == nil {
				t.Fatalf("expected strict decode error for %s", name)
			}
		})
	}
	_ = metav1.Now
}
