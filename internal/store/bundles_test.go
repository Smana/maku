// SPDX-License-Identifier: Apache-2.0

package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

const bundleRun = "7f3cq2xz"

func digestOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// HR22: a push's bundle is stored once per run and digest, beside the room log.
func TestPutBundle(t *testing.T) {
	ctx := t.Context()
	s, _, _, super := open(t)
	b := []byte("# v2 git bundle\nfake\n")
	if err := s.PutBundle(ctx, room, bundleRun, digestOf(b), b); err != nil {
		t.Fatal(err)
	}
	if err := s.PutBundle(ctx, room, bundleRun, digestOf(b), b); err != nil {
		t.Fatalf("a second put is a no-op: %v", err)
	}
	if got := query(t, super, `SELECT count(*)::text FROM output_bundles`); got != "1" {
		t.Fatalf("%s rows, want 1", got)
	}
	if got := query(t, super, `SELECT size::text FROM output_bundles`); got != "21" {
		t.Fatalf("size %s", got)
	}
	big := bytes.Repeat([]byte("x"), 8<<20+1)
	for name, tc := range map[string]struct {
		room, run, digest string
		b                 []byte
		want              error
	}{
		"a digest that is not the bytes'": {room, bundleRun, digestOf([]byte("other")), b, ErrDigestMismatch},
		"over 8 MiB":                      {room, bundleRun, digestOf(big), big, ErrBundleTooLarge},
		"empty":                           {room, bundleRun, digestOf(nil), nil, ErrBundleTooLarge},
		"no such room":                    {"zzzzzzzz", bundleRun, digestOf(b), b, ErrNoRoom},
	} {
		t.Run(name, func(t *testing.T) {
			if err := s.PutBundle(ctx, tc.room, tc.run, tc.digest, tc.b); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	t.Run("a sealed room", func(t *testing.T) {
		if err := s.CloseRoom(ctx, room, "test"); err != nil {
			t.Fatal(err)
		}
		c := []byte("another bundle")
		if err := s.PutBundle(ctx, room, bundleRun, digestOf(c), c); !errors.Is(err, ErrSealed) {
			t.Fatalf("err = %v, want ErrSealed", err)
		}
	})
}

// A bundle is read back by its room, run and digest only.
func TestBundle(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	b := []byte("# v2 git bundle\nread me\n")
	if err := s.PutBundle(ctx, room, bundleRun, digestOf(b), b); err != nil {
		t.Fatal(err)
	}
	got, err := s.Bundle(ctx, room, bundleRun, digestOf(b))
	if err != nil || !bytes.Equal(got, b) {
		t.Fatalf("Bundle = %q, %v", got, err)
	}
	for name, tc := range map[string]struct{ room, run, digest string }{
		"another room":   {"abcdefgh", bundleRun, digestOf(b)},
		"another run":    {room, "aaaaaaaa", digestOf(b)},
		"another digest": {room, bundleRun, digestOf([]byte("x"))},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Bundle(ctx, tc.room, tc.run, tc.digest); !errors.Is(err, ErrNoBundle) {
				t.Fatalf("err = %v, want ErrNoBundle", err)
			}
		})
	}
}

// Bundles older than 14 days are swept by the retention role; the broker cannot delete one.
func TestSweepBundles(t *testing.T) {
	ctx := t.Context()
	s, broker, retention, super := open(t)
	old, fresh := []byte("old bundle"), []byte("fresh bundle")
	for _, b := range [][]byte{old, fresh} {
		if err := s.PutBundle(ctx, room, bundleRun, digestOf(b), b); err != nil {
			t.Fatal(err)
		}
	}
	forge(t, super, `UPDATE output_bundles SET created_at = now() - interval '15 days' WHERE digest = '`+digestOf(old)+`'`)
	if _, err := s.SweepBundles(ctx, time.Now()); err == nil {
		t.Fatal("the broker's role must not be able to sweep bundles")
	}
	for name, sql := range map[string]string{
		"a DELETE":  `DELETE FROM output_bundles`,
		"an UPDATE": `UPDATE output_bundles SET bundle = 'x'`,
	} {
		if err := run(ctx, broker, sql); err == nil {
			t.Fatalf("the broker role ran %s", name)
		}
	}
	if err := run(ctx, retention, `SELECT bundle FROM output_bundles`); err == nil {
		t.Fatal("the retention role read a bundle")
	}
	r, err := Open(ctx, retention)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if n, err := r.SweepBundles(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("swept %d (%v), want the old one", n, err)
	}
	if _, err := s.Bundle(ctx, room, bundleRun, digestOf(old)); !errors.Is(err, ErrNoBundle) {
		t.Fatalf("the old bundle: %v", err)
	}
	if _, err := s.Bundle(ctx, room, bundleRun, digestOf(fresh)); err != nil {
		t.Fatalf("the fresh bundle: %v", err)
	}
}
