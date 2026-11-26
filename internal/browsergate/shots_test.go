// SPDX-License-Identifier: Apache-2.0

package browsergate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKeepPutListOpen(t *testing.T) {
	k, err := NewKeep(filepath.Join(t.TempDir(), "shots"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	for n, mime := range map[int]string{1: "image/png", 2: "image/jpeg"} {
		if err := k.Put(Shot{N: n, Mime: mime, Width: 8, Height: 8, URL: "http://127.0.0.1:8080/", TakenAt: at}, []byte{byte(n)}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := k.List(0)
	if err != nil || len(all) != 2 || all[0].N != 1 || all[1].N != 2 || all[0].Bytes != 1 {
		t.Fatal(all, err)
	}
	if later, _ := k.List(1); len(later) != 1 || later[0].N != 2 {
		t.Fatal(later)
	}
	b, mime, err := k.Open(2)
	if err != nil || mime != "image/jpeg" || !bytes.Equal(b, []byte{2}) {
		t.Fatal(b, mime, err)
	}
	if _, err := os.Stat(filepath.Join(k.Dir, "2.jpg")); err != nil {
		t.Fatal("stored as <n>.jpg:", err)
	}
	if _, _, err := k.Open(3); !errors.Is(err, ErrNoShot) {
		t.Fatal(err)
	}
}

func TestKeepRefuses(t *testing.T) {
	k, err := NewKeep(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		s    Shot
	}{
		{"a mime type other than png or jpeg", Shot{N: 1, Mime: "image/svg+xml"}},
		{"a number below 1", Shot{N: 0, Mime: "image/png"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := k.Put(c.s, []byte("x")); err == nil {
				t.Error("accepted")
			}
		})
	}
}
