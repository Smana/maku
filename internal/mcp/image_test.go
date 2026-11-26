// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/runwatch"
	"github.com/Smana/maku/internal/store"
)

type memImages map[string][]store.Screenshot // by room

func (m memImages) Screenshots(_ context.Context, room string) ([]store.ScreenshotMeta, error) {
	var out []store.ScreenshotMeta
	for _, s := range m[room] {
		out = append(out, s.ScreenshotMeta)
	}
	return out, nil
}

func (m memImages) Screenshot(_ context.Context, room, digest string) (store.Screenshot, error) {
	for _, s := range m[room] {
		if s.Digest == digest {
			return s, nil
		}
	}
	return store.Screenshot{}, store.ErrNoScreenshot
}

func TestRoomImage(t *testing.T) {
	imgs := memImages{
		"roomaaaa": {
			{ScreenshotMeta: store.ScreenshotMeta{N: 1, Digest: "d-browser", Source: store.SourceBrowser, RunID: "runaaaaa", Mime: "image/png"}, Bytes: []byte("browser")},
			{ScreenshotMeta: store.ScreenshotMeta{N: 1, Digest: "d-req", Source: store.SourceRequest, Mime: "image/png", Width: 10, Height: 5}, Bytes: []byte("request")},
		},
		"roombbbb": {{ScreenshotMeta: store.ScreenshotMeta{N: 2, Digest: "d-other", Source: store.SourceRequest, Mime: "image/jpeg"}, Bytes: []byte("other")}},
	}
	tool := ImageTools(imgs)[0]
	run := runwatch.Run{ID: "runaaaaa", Room: "roomaaaa", Role: "implementer", Screenshots: 10}
	if !tool.servedTo(run, false) {
		t.Fatal("a screenshots implementer is served room_image")
	}
	for _, r := range []runwatch.Run{{Role: "implementer"}, {Role: "reviewer", Screenshots: 10}, {Role: "triager", Screenshots: 10}} {
		if tool.servedTo(r, false) {
			t.Errorf("%+v is served room_image", r)
		}
	}
	out, err := tool.Call(t.Context(), Caller{Run: run}, json.RawMessage(`{"n":1}`))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := out.(mcpwire.Content)
	if !ok {
		t.Fatalf("%T is not image content", out)
	}
	parts := c["content"].([]map[string]string)
	if len(parts) != 2 || parts[1]["type"] != "image" || parts[1]["mimeType"] != "image/png" ||
		parts[1]["data"] != base64.StdEncoding.EncodeToString([]byte("request")) {
		t.Fatalf("the request image, not the browser's same-numbered one: %v", parts)
	}
	var ae argError
	if _, err := tool.Call(t.Context(), Caller{Run: run}, json.RawMessage(`{"n":2}`)); !errors.As(err, &ae) {
		t.Fatalf("another room's image 2 is not this room's: %v", err)
	}
	if _, err := tool.Call(t.Context(), Caller{Run: run}, json.RawMessage(`{"n":0}`)); !errors.As(err, &ae) {
		t.Fatalf("n 0: %v", err)
	}
}
