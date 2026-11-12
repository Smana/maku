// SPDX-License-Identifier: Apache-2.0

package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
)

const bundleRun = "7f3cq2xz"

func digestOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// output_status reads one output's events of one run: nothing of another run or another id.
func TestOutputEvents(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	rec := func(id string) *envelope.OutputRecord {
		return &envelope.OutputRecord{ID: id, Type: "push", Digest: strings.Repeat("a", 64)}
	}
	for i, d := range []struct {
		run, id string
	}{{bundleRun, "0123456789abcdef"}, {bundleRun, "1111111111111111"}, {"aaaaaaaa", "0123456789abcdef"}} {
		if _, _, err := s.Append(ctx, envelope.Draft{RoomID: room, RunID: d.run, Type: envelope.Message,
			Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + d.run}, Origin: envelope.OriginClient,
			OriginClient: "agent:" + d.run + ":tools", OriginSeq: int64(i + 1),
			Payload: envelope.Must(envelope.MessagePayload{Kind: envelope.KindOutputRequested, Delivery: envelope.DeliveryNone, Output: rec(d.id)})}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := s.OutputEvents(ctx, room, bundleRun, "0123456789abcdef")
	if err != nil || len(evs) != 1 || evs[0].RunID != bundleRun {
		t.Fatalf("%d events, %v", len(evs), err)
	}
	if k, r, ok := envelope.ReadOutput(evs[0], bundleRun); !ok || k != envelope.KindOutputRequested || r.ID != "0123456789abcdef" {
		t.Fatalf("ReadOutput %q %+v %v", k, r, ok)
	}
	if evs, err := s.OutputEvents(ctx, "abcdefgh", bundleRun, "0123456789abcdef"); err != nil || len(evs) != 0 {
		t.Fatalf("another room: %d %v", len(evs), err)
	}
}

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
