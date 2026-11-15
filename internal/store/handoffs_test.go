// SPDX-License-Identifier: Apache-2.0

package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func jsonEqual(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

func TestHandoffRoundTrip(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	want := Handoff{
		ID:        "abcdefghijklmnopqrstuvwx23",
		Principal: "human:alice",
		Args:      json.RawMessage(`{"text": "runbook 18 never says where the key lives"}`),
		Plan:      json.RawMessage(`{"revision": 1, "repository": "Smana/ai-native-ref"}`),
		Hash:      "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		CreatedAt: now,
		ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := s.PutHandoff(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetHandoff(ctx, want.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Principal != want.Principal || got.Hash != want.Hash ||
		!got.CreatedAt.Equal(want.CreatedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) ||
		!jsonEqual(got.Args, want.Args) || !jsonEqual(got.Plan, want.Plan) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	// PutHandoff replaces by ID on revision.
	want.Plan = json.RawMessage(`{"revision": 2, "repository": "Smana/ai-native-ref"}`)
	want.Hash = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	want.ExpiresAt = now.Add(25 * time.Hour)
	if err := s.PutHandoff(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetHandoff(ctx, want.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hash != want.Hash || !got.ExpiresAt.Equal(want.ExpiresAt) || !jsonEqual(got.Plan, want.Plan) {
		t.Fatalf("after replace: got %+v, want %+v", got, want)
	}

	if _, err := s.GetHandoff(ctx, "zzzzzzzzzzzzzzzzzzzzzzzz23", now); !errors.Is(err, ErrNoHandoff) {
		t.Fatalf("unknown id: err = %v, want ErrNoHandoff", err)
	}
}

func TestHandoffExpired(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	h := Handoff{
		ID:        "expiredhandoffidabcdefgh23",
		Principal: "human:alice",
		Args:      json.RawMessage(`{"text": "expired"}`),
		Plan:      json.RawMessage(`{"revision": 1}`),
		Hash:      "sha256:3333333333333333333333333333333333333333333333333333333333333333",
		CreatedAt: now.Add(-24 * time.Hour),
		ExpiresAt: now.Add(-time.Second),
	}
	if err := s.PutHandoff(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetHandoff(ctx, h.ID, now); !errors.Is(err, ErrNoHandoff) {
		t.Fatalf("expired id: err = %v, want ErrNoHandoff", err)
	}
}

func TestPruneHandoffs(t *testing.T) {
	ctx := t.Context()
	s, _, retention, _ := open(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	expired := Handoff{
		ID:        "expiredhandoffidabcdefgh23",
		Principal: "human:alice",
		Args:      json.RawMessage(`{"text": "old"}`),
		Plan:      json.RawMessage(`{"revision": 1}`),
		Hash:      "sha256:4444444444444444444444444444444444444444444444444444444444444444",
		CreatedAt: now.Add(-25 * time.Hour),
		ExpiresAt: now.Add(-time.Minute),
	}
	live := Handoff{
		ID:        "livehandoffidabcdefghijk23",
		Principal: "human:alice",
		Args:      json.RawMessage(`{"text": "fresh"}`),
		Plan:      json.RawMessage(`{"revision": 1}`),
		Hash:      "sha256:5555555555555555555555555555555555555555555555555555555555555555",
		CreatedAt: now,
		ExpiresAt: now.Add(24 * time.Hour),
	}
	for _, h := range []Handoff{expired, live} {
		if err := s.PutHandoff(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(ctx, retention, `SELECT plan FROM handoffs`); err == nil {
		t.Fatal("the retention role read a stored plan")
	}
	r, err := Open(ctx, retention, RoleRetention)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n, err := r.PruneHandoffs(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("PruneHandoffs = %d, %v; want 1, nil", n, err)
	}
	if _, err := s.GetHandoff(ctx, live.ID, now); err != nil {
		t.Fatalf("live row missing after prune: %v", err)
	}
}
