// SPDX-License-Identifier: Apache-2.0

package browsergate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrNoShot reports a screenshot number the keep does not hold.
var ErrNoShot = errors.New("no such screenshot")

// extensions maps a kept screenshot's MIME type to its file extension; nothing else is kept.
var extensions = map[string]string{"image/png": "png", "image/jpeg": "jpg"}

// Shot describes one kept screenshot: the /shots list entry and the <n>.json beside its bytes.
type Shot struct {
	// N is the screenshot's number in the run, from 1, the Counter's.
	N int `json:"n"`
	// Mime is image/png or image/jpeg, after CapImage.
	Mime string `json:"mime"`
	// Bytes is the stored image's size.
	Bytes  int `json:"bytes"`
	Width  int `json:"width"`
	Height int `json:"height"`
	// URL is the page the browser last reported, loopback by construction; empty when none was.
	URL     string    `json:"url"`
	TakenAt time.Time `json:"takenAt"`
	// Vision is whether the run's model received the image, or text in its place.
	Vision bool `json:"vision"`
}

// Keep is the gate's copy of every screenshot it served or stripped, for the room-bridge to relay:
// <n>.png or <n>.jpg and <n>.json in Dir. It is written only by the gate's own answer handling.
type Keep struct {
	Dir string
}

// NewKeep returns a keep in dir, creating it.
func NewKeep(dir string) (*Keep, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("screenshot keep: %w", err)
	}
	return &Keep{Dir: dir}, nil
}

// Put stores data as screenshot s.N; s.Bytes is set from data. The image is renamed into place
// before its description, so a listed screenshot always has its bytes.
func (k *Keep) Put(s Shot, data []byte) error {
	ext, ok := extensions[s.Mime]
	if !ok || s.N < 1 {
		return fmt.Errorf("keep screenshot %d: %q is not kept", s.N, s.Mime)
	}
	s.Bytes = len(data)
	meta, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(k.Dir, fmt.Sprintf("%d.%s", s.N, ext)), data); err != nil {
		return fmt.Errorf("keep screenshot %d: %w", s.N, err)
	}
	if err := writeAtomic(filepath.Join(k.Dir, fmt.Sprintf("%d.json", s.N)), meta); err != nil {
		return fmt.Errorf("keep screenshot %d: %w", s.N, err)
	}
	return nil
}

// List returns the kept screenshots numbered above after, in order.
func (k *Keep) List(after int) ([]Shot, error) {
	entries, err := os.ReadDir(k.Dir)
	if err != nil {
		return nil, fmt.Errorf("list screenshots: %w", err)
	}
	out := []Shot{}
	for _, e := range entries {
		base, ok := strings.CutSuffix(e.Name(), ".json")
		n, err := strconv.Atoi(base)
		if !ok || err != nil || n <= after || strconv.Itoa(n) != base {
			continue
		}
		s, err := k.meta(n)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Shot) int { return a.N - b.N })
	return out, nil
}

// Open returns screenshot n's bytes and MIME type, or ErrNoShot.
func (k *Keep) Open(n int) ([]byte, string, error) {
	s, err := k.meta(n)
	if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(filepath.Join(k.Dir, fmt.Sprintf("%d.%s", n, extensions[s.Mime])))
	if err != nil {
		return nil, "", fmt.Errorf("read screenshot %d: %w", n, err)
	}
	return b, s.Mime, nil
}

func (k *Keep) meta(n int) (Shot, error) {
	b, err := os.ReadFile(filepath.Join(k.Dir, fmt.Sprintf("%d.json", n)))
	if errors.Is(err, os.ErrNotExist) {
		return Shot{}, fmt.Errorf("%w: %d", ErrNoShot, n)
	}
	if err != nil {
		return Shot{}, fmt.Errorf("read screenshot %d: %w", n, err)
	}
	var s Shot
	if err := json.Unmarshal(b, &s); err != nil {
		return Shot{}, fmt.Errorf("screenshot %d: %w", n, err)
	}
	if _, ok := extensions[s.Mime]; !ok || s.N != n {
		return Shot{}, fmt.Errorf("screenshot %d: its description does not match", n)
	}
	return s, nil
}
