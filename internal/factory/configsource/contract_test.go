// SPDX-License-Identifier: Apache-2.0

package configsource

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/resources"
)

// OnSynced runs after every Sync, never under the Source's lock: it may call Outcomes, as plan 3's
// status writer does. Outcomes carries the last Sync's time and error.
func TestOnSyncedRunsAfterEachSyncOutsideTheLock(t *testing.T) {
	s, _ := source(t)
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return at }
	var calls int
	var got Synced
	s.OnSynced = func() {
		calls++
		_, got = s.Outcomes() // deadlocks if called under s.mu
	}
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !got.At.Equal(at) || got.Err != nil {
		t.Fatalf("calls %d synced %+v", calls, got)
	}
	s.File = []byte("factory: [") // later unservable: the current config is kept, the error recorded
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || got.Err == nil {
		t.Fatalf("calls %d synced %+v: Assemble's error is the Sync's", calls, got)
	}
}

type failingList struct{ client.Reader }

func (failingList) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("apiserver down")
}

func TestSyncedCarriesAListError(t *testing.T) {
	s := &Source{Reader: failingList{}, Namespace: ns, File: file(t), Log: slog.New(slog.DiscardHandler)}
	var calls int
	s.OnSynced = func() { calls++ }
	if err := s.Sync(t.Context()); err == nil {
		t.Fatal("a list error at startup is returned")
	}
	if _, sy := s.Outcomes(); calls != 1 || sy.Err == nil || !strings.Contains(sy.Err.Error(), "apiserver down") || sy.At.IsZero() {
		t.Fatalf("calls %d synced %+v", calls, sy)
	}
}

// Each outcome names its repository: a Repository's spec.repository, a MergePolicy's through its
// repositoryRef when that Repository is listed, and none otherwise.
func TestOutcomesNameTheirRepository(t *testing.T) {
	s, _ := source(t, repoObj("other", "Smana/Other", 1), policyObj(t, "other", "other", 1, nil),
		policyObj(t, "orphan", "missing", 1, nil))
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"other": "Smana/Other", "orphan": ""} {
		if o := outcome(t, s, resources.KindMergePolicy, name); o.Repository != want {
			t.Errorf("MergePolicy %s: repository %q, want %q", name, o.Repository, want)
		}
	}
	if o := outcome(t, s, resources.KindRepository, "other"); o.Repository != "Smana/Other" {
		t.Errorf("Repository other: %q", o.Repository)
	}
}

// I3: a status write (no generation change) on any replica must not make every replica re-sync.
func TestAStatusOnlyUpdateDoesNotKick(t *testing.T) {
	s, _ := source(t)
	h := s.handler()
	old := &v1alpha1.Repository{ObjectMeta: metav1.ObjectMeta{Name: "r", Generation: 4}}
	status := old.DeepCopy()
	status.Status.ObservedGeneration = 4
	h.OnUpdate(old, status)
	if len(s.kickCh()) != 0 {
		t.Fatal("a status-only update kicked a sync")
	}
	spec := old.DeepCopy()
	spec.Generation = 5
	h.OnUpdate(old, spec)
	if len(s.kickCh()) != 1 {
		t.Fatal("a spec change must kick")
	}
	<-s.kickCh()
	h.OnAdd(old, false)
	if len(s.kickCh()) != 1 {
		t.Fatal("an add must kick")
	}
	<-s.kickCh()
	h.OnDelete(old)
	if len(s.kickCh()) != 1 {
		t.Fatal("a delete must kick")
	}
}
